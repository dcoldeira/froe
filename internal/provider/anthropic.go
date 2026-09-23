package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// anthropicVersion is the required API version header.
const anthropicVersion = "2023-06-01"

// Anthropic speaks /v1/messages. It gets its own adapter for one reason: the
// wire format is not OpenAI-shaped (D2). System prompts are a top-level field
// rather than a message, content is a block list rather than a string, and the
// SSE stream is typed events rather than uniform chunks.
type Anthropic struct {
	name    string
	baseURL string
	apiKey  string
	caps    Caps
	http    *http.Client
}

// NewAnthropic builds an adapter.
func NewAnthropic(name, baseURL, apiKey string, caps Caps) *Anthropic {
	return &Anthropic{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		caps:    caps,
		http: &http.Client{
			Transport: &http.Transport{ResponseHeaderTimeout: 120 * time.Second},
		},
	}
}

func (c *Anthropic) Name() string { return c.name }
func (c *Anthropic) Caps() Caps   { return c.caps }

type antBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	// image
	Source *antImageSource `json:"source,omitempty"`
}

type antImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type antMessage struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock *antBlock `json:"content_block"`
	Message      *struct {
		Usage *antUsage `json:"usage"`
	} `json:"message"`
	Usage *antUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// buildBody converts a Request into Anthropic's shape.
func (c *Anthropic) buildBody(req Request) map[string]any {
	var system string
	msgs := make([]antMessage, 0, len(req.Messages))

	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			// Anthropic takes the system prompt out of band.
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
			continue
		case RoleTool:
			msgs = append(msgs, antMessage{Role: "user", Content: []antBlock{{
				Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
			}}})
			continue
		}

		blocks := make([]antBlock, 0, 1+len(m.Images)+len(m.ToolCalls))
		// Images before the text block: Anthropic's own examples put the
		// image first so the accompanying question reads as "about this".
		for _, img := range m.Images {
			blocks = append(blocks, antBlock{Type: "image", Source: &antImageSource{
				Type: "base64", MediaType: img.MediaType, Data: img.Data,
			}})
		}
		if m.Content != "" {
			blocks = append(blocks, antBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			blocks = append(blocks, antBlock{
				Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: args,
			})
		}
		if len(blocks) == 0 {
			continue
		}
		msgs = append(msgs, antMessage{Role: string(m.Role), Content: blocks})
	}

	// max_tokens is mandatory here, unlike the OpenAI shape.
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      req.Model,
		"messages":   msgs,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			})
		}
		body["tools"] = tools
	}
	for k, v := range req.Extra {
		body[k] = v
	}
	return body
}

// Chat streams a completion.
func (c *Anthropic) Chat(ctx context.Context, req Request) (<-chan Event, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("%s: no API key", c.name)
	}
	payload, err := json.Marshal(c.buildBody(req))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
		return nil, fmt.Errorf("%s: HTTP %d: %s", c.name, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	out := make(chan Event, 32)
	go c.stream(ctx, resp, start, out)
	return out, nil
}

func (c *Anthropic) stream(ctx context.Context, resp *http.Response, start time.Time, out chan<- Event) {
	defer resp.Body.Close()
	defer close(out)

	var (
		m       Metrics
		ttftSet bool
		pending = map[int]*ToolCall{}
	)

	emit := func(e Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Anthropic sends "event:" lines too; the JSON on "data:" is
		// self-describing via its own type field, so they can be ignored.
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		var ev antEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "error":
			msg := "unknown error"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			emit(Event{Kind: KindError, Err: fmt.Errorf("%s: %s", c.name, msg)})
			return

		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				m.PromptTokens = ev.Message.Usage.InputTokens
			}

		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				pending[ev.Index] = &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
			}

		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			if !ttftSet {
				m.TTFT = time.Since(start)
				ttftSet = true
			}
			switch ev.Delta.Type {
			case "text_delta":
				if !emit(Event{Kind: KindText, Text: ev.Delta.Text}) {
					return
				}
			case "thinking_delta":
				if !emit(Event{Kind: KindReasoning, Text: ev.Delta.Thinking}) {
					return
				}
			case "input_json_delta":
				if p, ok := pending[ev.Index]; ok {
					p.Arguments += ev.Delta.PartialJSON
				}
			}

		case "message_delta":
			if ev.Usage != nil {
				m.CompletionTokens = ev.Usage.OutputTokens
			}

		case "message_stop":
			// handled after the loop
		}
	}

	if err := sc.Err(); err != nil && ctx.Err() == nil {
		emit(Event{Kind: KindError, Err: fmt.Errorf("%s: read stream: %w", c.name, err)})
		return
	}

	for i := 0; i < len(pending)+len(pending); i++ {
		if tc, ok := pending[i]; ok {
			if !emit(Event{Kind: KindToolCall, ToolCall: tc}) {
				return
			}
			delete(pending, i)
		}
	}

	m.Total = time.Since(start)
	emit(Event{Kind: KindDone, Metrics: &m})
}
