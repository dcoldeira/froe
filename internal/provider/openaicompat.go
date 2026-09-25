package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat speaks the /v1/chat/completions shape, which is the lingua
// franca of local inference (D2): llama.cpp's llama-server, Ollama, LM Studio,
// vLLM, Mistral and DeepSeek all expose it. One adapter, six backends.
type OpenAICompat struct {
	name    string
	baseURL string
	apiKey  string
	caps    Caps
	http    *http.Client
}

// NewOpenAICompat builds an adapter. apiKey may be empty for local backends.
func NewOpenAICompat(name, baseURL, apiKey string, caps Caps) *OpenAICompat {
	return &OpenAICompat{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		caps:    caps,
		// No global timeout: a slow local model legitimately takes minutes, and
		// cancellation is ctx's job. The transport still bounds connection setup.
		http: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 120 * time.Second,
			},
		},
	}
}

func (c *OpenAICompat) Name() string { return c.name }
func (c *OpenAICompat) Caps() Caps   { return c.caps }

// wire types ----------------------------------------------------------------

type ocMessage struct {
	Role string `json:"role"`
	// Content is a plain string for a text-only message, or a []map[string]any
	// of {type, text|image_url} parts once an image is attached - the OpenAI
	// vision shape requires the array form even for a single image, so `any`
	// rather than `string` is unavoidable here.
	Content    any          `json:"content"`
	ToolCalls  []ocToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type ocToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type ocDelta struct {
	Content string `json:"content"`
	// ReasoningContent carries hidden chain-of-thought. Bonsai-27B and DeepSeek
	// both use this field; it must never be mixed into the visible answer.
	ReasoningContent string       `json:"reasoning_content"`
	ToolCalls        []ocToolCall `json:"tool_calls"`
}

type ocChunk struct {
	Choices []struct {
		Delta        ocDelta `json:"delta"`
		Message      ocDelta `json:"message"` // non-streaming responses
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		Details          *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// buildBody assembles the request payload.
func (c *OpenAICompat) buildBody(req Request) map[string]any {
	msgs := make([]ocMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := ocMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		if len(m.Images) > 0 {
			parts := []map[string]any{{"type": "text", "text": m.Content}}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + img.MediaType + ";base64," + img.Data,
					},
				})
			}
			om.Content = parts
		}
		for i, tc := range m.ToolCalls {
			o := ocToolCall{Index: i, ID: tc.ID, Type: "function"}
			o.Function.Name = tc.Name
			// A model can emit a tool call with no arguments at all (observed:
			// Bonsai calling read_file bare). Left as "", Arguments'
			// `omitempty` tag drops the field from the JSON entirely once this
			// message is replayed back in history on a later turn - LM Studio
			// then rejects the whole request as a malformed payload. "{}" is
			// what an empty-but-valid arguments object looks like on the wire.
			//
			// Arguments that are not valid JSON go back as "{}" too. The tool
			// already told the model its call was malformed; Ollama answers a
			// history holding the raw text with HTTP 400 "invalid tool call
			// arguments" and the whole run ends there.
			o.Function.Arguments = tc.Arguments
			if !json.Valid([]byte(o.Function.Arguments)) {
				o.Function.Arguments = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, o)
		}
		msgs = append(msgs, om)
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
		"stream":   true,
		// Without this most backends omit usage entirely from streamed
		// responses, and metrics silently become guesses.
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	// Only send when set. Measured on Bonsai-27B: the single most important
	// parameter for a model that reasons by default (see Request.ThinkingBudget).
	if req.ThinkingBudget != nil {
		body["thinking_budget_tokens"] = *req.ThinkingBudget
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	// Registry-supplied knobs win, so a new backend parameter is a TOML edit.
	for k, v := range req.Extra {
		body[k] = v
	}
	return body
}

// Chat streams a completion.
func (c *OpenAICompat) Chat(ctx context.Context, req Request) (<-chan Event, error) {
	payload, err := json.Marshal(c.buildBody(req))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
		msg := fmt.Sprintf("%s: %s", c.name, explainHTTPError(c.name, resp.StatusCode, snippet))
		if resp.StatusCode == http.StatusInternalServerError && len(req.Tools) > 0 && isJSONParseError(string(snippet)) {
			return nil, &ToolCallFormatError{Msg: msg}
		}
		return nil, errors.New(msg)
	}

	out := make(chan Event, 32)
	go c.stream(ctx, resp, start, out)
	return out, nil
}

// stream parses the SSE body and emits events. It owns closing resp and out.
func (c *OpenAICompat) stream(ctx context.Context, resp *http.Response, start time.Time, out chan<- Event) {
	defer resp.Body.Close()
	defer close(out)

	var (
		m          Metrics
		ttftSet    bool
		textChunks int
		pending    = map[int]*ToolCall{}
		order      []*ToolCall
		think      thinkFilter
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
	// Reasoning traces produce long lines; the 64KB default is not enough.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk ocChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // a malformed keep-alive should not kill the stream
		}
		if chunk.Error != nil {
			emit(Event{Kind: KindError, Err: fmt.Errorf("%s: %s", c.name, chunk.Error.Message)})
			return
		}

		if chunk.Usage != nil {
			m.PromptTokens = chunk.Usage.PromptTokens
			m.CompletionTokens = chunk.Usage.CompletionTokens
			if chunk.Usage.Details != nil {
				m.ReasoningTokens = chunk.Usage.Details.ReasoningTokens
			}
		}

		for _, ch := range chunk.Choices {
			d := ch.Delta
			if d.Content == "" && d.ReasoningContent == "" && len(d.ToolCalls) == 0 {
				d = ch.Message // non-streaming fallback
			}

			if !ttftSet && (d.Content != "" || d.ReasoningContent != "") {
				m.TTFT = time.Since(start)
				ttftSet = true
			}
			if d.ReasoningContent != "" {
				textChunks++
				if !emit(Event{Kind: KindReasoning, Text: d.ReasoningContent}) {
					return
				}
			}
			if d.Content != "" {
				textChunks++
				// Split out any inline <think> block the backend failed to
				// separate, so reasoning never reaches the answer stream.
				visible, thought := think.feed(d.Content)
				if thought != "" {
					if !emit(Event{Kind: KindReasoning, Text: thought}) {
						return
					}
				}
				if visible != "" {
					if !emit(Event{Kind: KindText, Text: visible}) {
						return
					}
				}
			}
			// Tool calls stream in fragments keyed by index and must be
			// reassembled before they mean anything.
			//
			// A fragment carrying a NEW id is a new call even at a used index.
			// Ollama sends each parallel call whole, every one at index 0
			// (measured 2026-09-25, ministral-3:8b): keyed on index alone, two
			// edit_file calls fused into one '{...}{...}' argument string, the
			// tool rejected it, and replaying it in history got HTTP 400.
			for _, tc := range d.ToolCalls {
				p, ok := pending[tc.Index]
				if !ok || (tc.ID != "" && p.ID != "" && tc.ID != p.ID) {
					p = &ToolCall{}
					pending[tc.Index] = p
					order = append(order, p)
				}
				if tc.ID != "" {
					p.ID = tc.ID
				}
				if tc.Function.Name != "" {
					p.Name = tc.Function.Name
				}
				p.Arguments += tc.Function.Arguments
			}
		}
	}

	if err := sc.Err(); err != nil && ctx.Err() == nil {
		emit(Event{Kind: KindError, Err: fmt.Errorf("%s: read stream: %w", c.name, err)})
		return
	}

	for _, tc := range order {
		if !emit(Event{Kind: KindToolCall, ToolCall: tc}) {
			return
		}
	}

	if tail := think.flush(); tail != "" {
		emit(Event{Kind: KindText, Text: tail})
	}

	m.Total = time.Since(start)
	// Some backends never report usage even when asked. Counting chunks is a
	// poor proxy, so it is flagged rather than passed off as measured.
	if m.CompletionTokens == 0 && textChunks > 0 {
		m.CompletionTokens = textChunks
		m.Estimated = true
	}
	emit(Event{Kind: KindDone, Metrics: &m})
}

// explainHTTPError turns a backend's raw JSON error into something actionable.
//
// A runtime can be up and still unable to serve: LM Studio answers /v1/models
// happily with no model loaded, so the probe says "running" and the request
// then fails. Printing the raw blob leaves the user to work that out; naming the
// fix does not.
func explainHTTPError(runtime string, status int, body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}

	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no models loaded"), strings.Contains(lower, "model_not_found"),
		strings.Contains(lower, "not found") && status == http.StatusNotFound:
		hint := "load a model first"
		switch runtime {
		case "lmstudio":
			hint = "run: lms load <model>   (lms ls lists what is downloaded)"
		case "ollama":
			hint = "run: ollama pull <model>"
		case "llamacpp", "llamacpp-prismml":
			hint = "start llama-server with -m <path-to.gguf>"
		}
		return fmt.Sprintf("%s has no model loaded - %s", runtime, hint)

	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return fmt.Sprintf("HTTP %d: %s (check the API key)", status, msg)

	case strings.Contains(lower, "out of memory"), strings.Contains(lower, "cuda error"):
		return fmt.Sprintf("out of memory: %s\n  another model is probably holding the GPU - try `froe doctor`, or a cpu_only model", msg)
	}
	return fmt.Sprintf("HTTP %d: %s", status, msg)
}

// ToolCallFormatError is a backend failing to parse the model's own tool call.
// Ollama answers HTTP 500 when the model emits a call it cannot parse, and the
// reply is lost: measured 2026-09-25, 8 of ministral-3:8b's 30 eval runs ended
// this way. Sampling again usually produces a well-formed call, so the agent
// retries rather than ending the run.
type ToolCallFormatError struct{ Msg string }

func (e *ToolCallFormatError) Error() string { return e.Msg }

// isJSONParseError reports whether a server error body is Go's JSON decoder
// complaining, which is how Ollama's tool-call parser fails.
func isJSONParseError(body string) bool {
	for _, s := range []string{"unexpected end of JSON input", "invalid character", "error parsing tool call"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}
