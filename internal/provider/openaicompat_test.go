package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer replays fixed SSE frames and captures the request body.
func sseServer(t *testing.T, frames []string, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func collect(t *testing.T, ch <-chan Event) (text, reasoning string, tools []ToolCall, metrics *Metrics, err error) {
	t.Helper()
	for ev := range ch {
		switch ev.Kind {
		case KindText:
			text += ev.Text
		case KindReasoning:
			reasoning += ev.Text
		case KindToolCall:
			tools = append(tools, *ev.ToolCall)
		case KindDone:
			metrics = ev.Metrics
		case KindError:
			err = ev.Err
		}
	}
	return
}

func TestOpenAICompatStreamsTextAndReasoning(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"reasoning_content":"let me think"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"Hello"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":", world"}}]}` + "\n\n",
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\n",
		"data: [DONE]\n\n",
	}, nil)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{Streaming: true})
	ch, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	text, reasoning, _, m, streamErr := collect(t, ch)

	if streamErr != nil {
		t.Fatalf("unexpected stream error: %v", streamErr)
	}
	if text != "Hello, world" {
		t.Errorf("text = %q, want %q", text, "Hello, world")
	}
	// Reasoning must never leak into the visible answer.
	if reasoning != "let me think" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if m == nil {
		t.Fatal("no metrics emitted")
	}
	if m.PromptTokens != 11 || m.CompletionTokens != 7 || m.ReasoningTokens != 3 {
		t.Errorf("usage = %+v", m.Usage)
	}
	if m.Estimated {
		t.Error("usage was reported by the server, must not be flagged estimated")
	}
}

// Tool calls arrive as fragments keyed by index and are meaningless until
// reassembled.
func TestOpenAICompatReassemblesToolCalls(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, nil)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{})
	ch, _ := c.Chat(context.Background(), Request{Model: "m"})
	_, _, tools, _, _ := collect(t, ch)

	if len(tools) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(tools))
	}
	if tools[0].ID != "call_1" || tools[0].Name != "read_file" {
		t.Errorf("tool = %+v", tools[0])
	}
	if tools[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("arguments = %q, want %q", tools[0].Arguments, `{"path":"a.go"}`)
	}
}

// Some backends never report usage. Counting chunks is a poor proxy and must
// be flagged rather than passed off as measured.
func TestOpenAICompatFlagsEstimatedUsage(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"a"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"b"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, nil)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{})
	ch, _ := c.Chat(context.Background(), Request{Model: "m"})
	_, _, _, m, _ := collect(t, ch)

	if m == nil || !m.Estimated {
		t.Fatalf("missing usage must be flagged estimated, got %+v", m)
	}
	if m.CompletionTokens != 2 {
		t.Errorf("chunk count = %d, want 2", m.CompletionTokens)
	}
}

// nil means "send nothing" (model default); a pointer to 0 is an explicit
// request. Measured on Bonsai-27B: neither actually stops it reasoning, but
// the wire behaviour must still be exact.
func TestThinkingBudgetIsSentOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget *int
		want   bool
	}{
		{"unset", nil, false},
		{"explicit zero", ptr(0), true},
		{"positive", ptr(64), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			srv := sseServer(t, []string{"data: [DONE]\n\n"}, &body)
			defer srv.Close()

			c := NewOpenAICompat("test", srv.URL, "", Caps{})
			ch, _ := c.Chat(context.Background(), Request{Model: "m", ThinkingBudget: tc.budget})
			collect(t, ch)

			_, present := body["thinking_budget_tokens"]
			if present != tc.want {
				t.Errorf("thinking_budget_tokens present = %v, want %v (body: %v)", present, tc.want, body)
			}
			if tc.want && tc.budget != nil {
				if got := int(body["thinking_budget_tokens"].(float64)); got != *tc.budget {
					t.Errorf("budget = %d, want %d", got, *tc.budget)
				}
			}
		})
	}
}

func TestOpenAICompatSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"model not loaded"}}`)
	}))
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{})
	_, err := c.Chat(context.Background(), Request{Model: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("error should carry the server's message, got: %v", err)
	}
}

// Cancelling must abort rather than hang: on llama.cpp it also frees the slot.
func TestOpenAICompatCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 1000; i++ {
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewOpenAICompat("test", srv.URL, "", Caps{})
	ch, err := c.Chat(ctx, Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	<-ch // wait for the stream to actually start
	cancel()

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not terminate within 3s of cancellation")
	}
}

func ptr(i int) *int { return &i }

// An attached image must switch content from a plain string to the vision
// array shape - a string content field with an image is silently ignored by
// every OpenAI-compatible backend, which is a much worse failure than the
// slightly odd request shape below.
func TestOpenAICompatSendsVisionContentArrayWhenImageAttached(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{"data: [DONE]\n\n"}, &captured)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{Streaming: true, Vision: true})
	ch, err := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{
		Role: RoleUser, Content: "what is this?",
		Images: []ImageContent{{MediaType: "image/png", Data: "Zm9v"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)

	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	msg := msgs[0].(map[string]any)
	parts, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content = %T, want an array once an image is attached: %v", msg["content"], msg["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("got %d content parts, want 2 (text + image)", len(parts))
	}
	text := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "what is this?" {
		t.Errorf("text part = %v", text)
	}
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Errorf("image part type = %v", image["type"])
	}
	url := image["image_url"].(map[string]any)["url"]
	if url != "data:image/png;base64,Zm9v" {
		t.Errorf("image url = %v", url)
	}
}

// Reproduces the 2026-09-15 finding: a model can call a tool with no
// arguments at all (Bonsai called read_file bare). Left as an empty string,
// the `omitempty` tag on Arguments drops "arguments" from the JSON entirely
// once this assistant message is replayed back in history on a later turn -
// LM Studio then rejected the whole request with "Invalid 'messages' in
// payload". The wire representation of "no arguments" must be "{}", never a
// missing field.
func TestOpenAICompatDefaultsEmptyToolArgumentsToEmptyObject(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{"data: [DONE]\n\n"}, &captured)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{Streaming: true})
	ch, _ := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{
		Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "read_file", Arguments: ""}},
	}}})
	collect(t, ch)

	msgs, _ := captured["messages"].([]any)
	msg := msgs[0].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	fn := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if _, present := fn["arguments"]; !present {
		t.Fatal("arguments field is missing from the JSON entirely - must be present as \"{}\"")
	}
	if fn["arguments"] != "{}" {
		t.Errorf("arguments = %v, want \"{}\"", fn["arguments"])
	}
}

// No image, no behaviour change: the ordinary case must stay a plain string,
// not an array-of-one, or every non-vision backend that expects a string
// starts failing on messages that never touched an image.
func TestOpenAICompatContentStaysStringWithoutImages(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{"data: [DONE]\n\n"}, &captured)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{Streaming: true})
	ch, _ := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	collect(t, ch)

	msgs, _ := captured["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, isString := msg["content"].(string); !isString {
		t.Errorf("content = %T, want a plain string when no image is attached", msg["content"])
	}
}

// Ollama streams each parallel call whole, every one at index 0, with its own
// id. They are separate calls, not fragments of one.
func TestOpenAICompatSplitsParallelCallsSharingAnIndex(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, nil)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{})
	ch, _ := c.Chat(context.Background(), Request{Model: "m"})
	_, _, tools, _, _ := collect(t, ch)

	if len(tools) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(tools), tools)
	}
	if tools[0].Arguments != `{"path":"a.go"}` || tools[1].Arguments != `{"path":"b.go"}` {
		t.Errorf("calls = %+v", tools)
	}
}

// Replaying unparseable arguments gets the whole request refused by Ollama.
func TestOpenAICompatReplaysInvalidToolArgumentsAsEmptyObject(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, []string{"data: [DONE]\n\n"}, &captured)
	defer srv.Close()

	c := NewOpenAICompat("test", srv.URL, "", Caps{Streaming: true})
	ch, _ := c.Chat(context.Background(), Request{Model: "m", Messages: []Message{{
		Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: "edit_file", Arguments: `{"a":1}{"b":2}`}},
	}}})
	collect(t, ch)

	msgs, _ := captured["messages"].([]any)
	fn := msgs[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["arguments"] != "{}" {
		t.Errorf("arguments = %v, want \"{}\"", fn["arguments"])
	}
}

// A 500 that is the backend's JSON parser failing on a tool call is typed, so
// the agent can retry it; any other 500 is not.
func TestOpenAICompatTypesToolCallParseFailures(t *testing.T) {
	for _, tc := range []struct {
		body  string
		tools bool
		want  bool
	}{
		{`{"error":"unexpected end of JSON input"}`, true, true},
		{`{"error":"invalid character 'o' looking for beginning of object key string"}`, true, true},
		{`{"error":"unexpected end of JSON input"}`, false, false},
		{`{"error":"model runner has unexpectedly stopped"}`, true, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, tc.body)
		}))
		req := Request{Model: "m"}
		if tc.tools {
			req.Tools = []ToolDef{{Name: "read_file"}}
		}
		_, err := NewOpenAICompat("test", srv.URL, "", Caps{}).Chat(context.Background(), req)
		srv.Close()
		var fe *ToolCallFormatError
		if got := errors.As(err, &fe); got != tc.want {
			t.Errorf("%s (tools %v): typed = %v, want %v (err %v)", tc.body, tc.tools, got, tc.want, err)
		}
	}
}
