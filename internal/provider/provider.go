// Package provider talks to language models.
//
// The interface is deliberately thin (docs/ARCHITECTURE.md §3): everything
// hard — retries, tool-call strategy, context budgeting — lives above it in the
// agent, so adding a backend stays cheap. Backends live in this same package as
// separate files rather than subpackages, because a factory in a parent package
// importing child packages that import the parent for its types is an import
// cycle. One package, several files, no gymnastics.
package provider

import (
	"context"
	"encoding/json"
	"time"
)

// Role identifies who produced a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn of conversation.
type Message struct {
	Role       Role
	Content    string
	Images     []ImageContent // attached to a user message; empty for every other role
	ToolCalls  []ToolCall     // assistant messages requesting tools
	ToolCallID string         // tool messages answering a specific call
}

// ImageContent is one image attached to a user message, already loaded and
// base64-encoded (see LoadImageFile). Kept as data rather than a path because
// a Provider must not touch the filesystem - that boundary belongs to
// internal/tools, not to the wire layer.
type ImageContent struct {
	// MediaType is a standard image MIME type, e.g. "image/png".
	MediaType string
	// Data is base64-encoded image bytes, no data: URL prefix.
	Data string
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	ID   string
	Name string
	// Arguments is raw JSON. It is NOT decoded here: a model can emit invalid
	// JSON, and the decision about what to do then belongs to the agent's
	// tool-call strategy, not to the wire layer.
	Arguments string
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// Request is one model call.
type Request struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature float64
	Tools       []ToolDef

	// ThinkingBudget caps reasoning tokens on models that reason by default.
	// A pointer because 0 is a meaningful value distinct from "unset": nil
	// sends nothing (the model's own default, which for Bonsai is uncapped),
	// while a pointer to 0 explicitly asks for no reasoning at all.
	// Measured on Bonsai-27B 2026-09-11: uncapped took 128.7s for a four-line
	// function (504 of 567 tokens were reasoning); a budget of 64 gave the same
	// answer in 27.8s. /no_think, chat_template_kwargs.enable_thinking and
	// reasoning_effort are all silently ignored by that model — only this works.
	// Zero means "do not send the parameter".
	ThinkingBudget *int

	// Extra carries model-specific parameters straight from the registry, so a
	// new knob is a TOML edit rather than a code change (D3).
	Extra map[string]any
}

// Caps describes what a backend can do. The agent uses this to pick a
// tool-calling strategy rather than assuming one.
type Caps struct {
	NativeTools bool
	Grammar     bool
	Streaming   bool
	MaxContext  int
	Vision      bool
}

// EventKind discriminates the event stream.
type EventKind int

const (
	// KindText is a chunk of the visible answer.
	KindText EventKind = iota
	// KindReasoning is a chunk of hidden chain-of-thought. Models that reason
	// return it on a separate channel (OpenAI-compat: reasoning_content;
	// Anthropic: thinking deltas) and it must not be confused with the answer.
	KindReasoning
	// KindToolCall is a fully assembled tool call.
	KindToolCall
	// KindDone is the final event, carrying metrics. Always sent last.
	KindDone
	// KindError terminates the stream.
	KindError
)

// Event is one item in a response stream.
type Event struct {
	Kind     EventKind
	Text     string
	ToolCall *ToolCall
	Metrics  *Metrics
	Err      error
}

// Usage counts tokens. Estimated is set when the backend did not report usage
// and the numbers were derived from counting stream chunks — a rough proxy
// that must never be presented as measured.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	Estimated        bool
}

// Metrics is the timing record for one call. Kept per-call from the very first
// request (Roadmap Phase 1) so `froe bench` has real data to aggregate rather
// than having to be retrofitted.
type Metrics struct {
	Usage
	// TTFT is time to first token: the closest proxy for prefill cost, which
	// dominates on CPU-bound hardware.
	TTFT  time.Duration
	Total time.Duration
}

// GenTokensPerSec is generation throughput, excluding prefill.
func (m Metrics) GenTokensPerSec() float64 {
	gen := m.Total - m.TTFT
	if gen <= 0 || m.CompletionTokens == 0 {
		return 0
	}
	return float64(m.CompletionTokens) / gen.Seconds()
}

// PrefillTokensPerSec is prompt processing throughput. Returns 0 when there is
// nothing meaningful to divide.
func (m Metrics) PrefillTokensPerSec() float64 {
	if m.TTFT <= 0 || m.PromptTokens == 0 {
		return 0
	}
	return float64(m.PromptTokens) / m.TTFT.Seconds()
}

// Provider is one model backend.
//
// Chat returns a channel closed when the exchange ends. Cancelling ctx must
// abort the in-flight HTTP request, not merely stop reading — on llama.cpp that
// also frees the server slot.
type Provider interface {
	Name() string
	Caps() Caps
	Chat(ctx context.Context, req Request) (<-chan Event, error)
}
