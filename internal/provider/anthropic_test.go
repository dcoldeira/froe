package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func antServer(t *testing.T, frames []string, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header is required and was not sent")
		}
		if r.Header.Get("x-api-key") == "" {
			t.Error("x-api-key header was not sent")
		}
		if captured != nil {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			*captured = m
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			io.WriteString(w, f)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}))
}

func TestAnthropicStreamsTextThinkingAndUsage(t *testing.T) {
	srv := antServer(t, []string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":12}}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}` + "\n\n",
		`data: {"type":"message_delta","usage":{"output_tokens":5}}` + "\n\n",
		`data: {"type":"message_stop"}` + "\n\n",
	}, nil)
	defer srv.Close()

	c := NewAnthropic("anthropic", srv.URL, "key", Caps{})
	ch, err := c.Chat(context.Background(), Request{Model: "claude-opus-5"})
	if err != nil {
		t.Fatal(err)
	}
	text, reasoning, _, m, streamErr := collect(t, ch)

	if streamErr != nil {
		t.Fatalf("unexpected error: %v", streamErr)
	}
	if text != "Hi there" {
		t.Errorf("text = %q", text)
	}
	if reasoning != "hmm" {
		t.Errorf("thinking_delta must map to KindReasoning, got %q", reasoning)
	}
	if m.PromptTokens != 12 || m.CompletionTokens != 5 {
		t.Errorf("usage = %+v", m.Usage)
	}
}

// Anthropic takes the system prompt out of band rather than as a message.
// Getting this wrong silently drops the system prompt.
func TestAnthropicLiftsSystemPromptOutOfMessages(t *testing.T) {
	var body map[string]any
	srv := antServer(t, []string{`data: {"type":"message_stop"}` + "\n\n"}, &body)
	defer srv.Close()

	c := NewAnthropic("anthropic", srv.URL, "key", Caps{})
	ch, _ := c.Chat(context.Background(), Request{
		Model: "claude-opus-5",
		Messages: []Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "hello"},
		},
	})
	collect(t, ch)

	if body["system"] != "be terse" {
		t.Errorf("system = %v, want %q", body["system"], "be terse")
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (system must not remain in the list)", len(msgs))
	}
	if role := msgs[0].(map[string]any)["role"]; role != "user" {
		t.Errorf("remaining message role = %v, want user", role)
	}
}

// max_tokens is mandatory on this API; omitting it is a 400.
func TestAnthropicAlwaysSendsMaxTokens(t *testing.T) {
	var body map[string]any
	srv := antServer(t, []string{`data: {"type":"message_stop"}` + "\n\n"}, &body)
	defer srv.Close()

	c := NewAnthropic("anthropic", srv.URL, "key", Caps{})
	ch, _ := c.Chat(context.Background(), Request{Model: "claude-opus-5"})
	collect(t, ch)

	if _, ok := body["max_tokens"]; !ok {
		t.Fatal("max_tokens is required by the API and was not sent")
	}
}

func TestAnthropicReassemblesToolUse(t *testing.T) {
	srv := antServer(t, []string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"grep"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"todo\"}"}}` + "\n\n",
		`data: {"type":"message_stop"}` + "\n\n",
	}, nil)
	defer srv.Close()

	c := NewAnthropic("anthropic", srv.URL, "key", Caps{})
	ch, _ := c.Chat(context.Background(), Request{Model: "claude-opus-5"})
	_, _, tools, _, _ := collect(t, ch)

	if len(tools) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(tools))
	}
	if tools[0].ID != "tu_1" || tools[0].Name != "grep" || tools[0].Arguments != `{"q":"todo"}` {
		t.Errorf("tool = %+v", tools[0])
	}
}

// A tool result is a user message carrying a tool_result block, not a
// dedicated "tool" role as in the OpenAI shape.
func TestAnthropicMapsToolResults(t *testing.T) {
	var body map[string]any
	srv := antServer(t, []string{`data: {"type":"message_stop"}` + "\n\n"}, &body)
	defer srv.Close()

	c := NewAnthropic("anthropic", srv.URL, "key", Caps{})
	ch, _ := c.Chat(context.Background(), Request{
		Model:    "claude-opus-5",
		Messages: []Message{{Role: RoleTool, ToolCallID: "tu_1", Content: "42"}},
	})
	collect(t, ch)

	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("tool result role = %v, want user", m0["role"])
	}
	block := m0["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "tu_1" {
		t.Errorf("block = %+v", block)
	}
}

func TestAnthropicRequiresAPIKey(t *testing.T) {
	c := NewAnthropic("anthropic", "https://example.invalid", "", Caps{})
	if _, err := c.Chat(context.Background(), Request{Model: "m"}); err == nil {
		t.Fatal("expected an error when no API key is set")
	}
}

// Anthropic already uses content blocks for text, so an attached image is an
// additional block rather than a shape change - simpler than the OpenAI-
// compat case, but still needs its own test: base64 source, correct media
// type, and placed before the text block per Anthropic's own convention.
func TestAnthropicSendsImageBlockWhenImageAttached(t *testing.T) {
	var captured map[string]any
	srv := antServer(t, []string{"event: message_stop\ndata: {}\n\n"}, &captured)
	defer srv.Close()

	c := NewAnthropic("test", srv.URL, "key", Caps{Streaming: true, Vision: true})
	ch, err := c.Chat(context.Background(), Request{Model: "m", MaxTokens: 100, Messages: []Message{{
		Role: RoleUser, Content: "what is this?",
		Images: []ImageContent{{MediaType: "image/png", Data: "Zm9v"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	blocks := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (image + text)", len(blocks))
	}
	image := blocks[0].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("first block type = %v, want image", image["type"])
	}
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "Zm9v" {
		t.Errorf("source = %v", source)
	}
	text := blocks[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "what is this?" {
		t.Errorf("second block = %v", text)
	}
}
