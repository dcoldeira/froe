// Package rpc implements the JSON-RPC the editor plugin speaks.
//
// Framing is newline-delimited JSON rather than LSP's Content-Length headers.
// Both ends are ours, and a line is something Neovim's jobstart hands over
// already split — which keeps the Lua side to parsing, not framing.
package rpc

import "encoding/json"

// Version is the protocol version reported by initialize. The plugin checks it
// so a stale binary fails loudly rather than behaving strangely.
const Version = 1

// Message is any inbound line. Which fields are set says what it is: a request
// has an id and a method, a response has an id and a result or error, a
// notification has a method and no id.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Standard JSON-RPC error codes, plus one of our own.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	// CodeCancelled reports a run the user stopped. Distinct from an internal
	// error so the editor can stay quiet about it.
	CodeCancelled = -32800
)

// --- client → server ---

// InitializeParams starts a session.
type InitializeParams struct {
	Root  string `json:"root"`
	Model string `json:"model,omitempty"`
}

// InitializeResult describes what the server settled on.
type InitializeResult struct {
	Version  int      `json:"version"`
	Model    string   `json:"model"`
	Runtime  string   `json:"runtime"`
	Strategy string   `json:"strategy"`
	Tools    []string `json:"tools"`
	Root     string   `json:"root"`
	Session  string   `json:"session"`
}

// RunParams is one task.
type RunParams struct {
	Task string `json:"task"`
	// File and Selection scope a task to what the user has highlighted, so the
	// editor's notion of "here" survives into the prompt.
	File      string `json:"file,omitempty"`
	Selection string `json:"selection,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Mode is "ask", "accept-edits" or "yolo".
	Mode string `json:"mode,omitempty"`
	// Resume continues the project's most recent session.
	Resume bool `json:"resume,omitempty"`
}

// RunResult is returned when a run ends.
type RunResult struct {
	Answer     string `json:"answer"`
	Turns      int    `json:"turns"`
	ToolCalls  int    `json:"tool_calls"`
	ToolErrors int    `json:"tool_errors"`
	Tokens     int    `json:"tokens"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

// CancelParams stops the running task.
type CancelParams struct{}

// --- server → client ---

// EventParams is a progress notification, sent as "froe/event".
type EventParams struct {
	// Kind is one of: turn, text, reasoning, tool, tool_result, denied, error.
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Turn   int    `json:"turn,omitempty"`
}

// ApproveParams asks the editor to approve a mutating tool call. Sent as a
// REQUEST, not a notification: the run genuinely blocks on the answer.
type ApproveParams struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

// ApproveResult is the editor's answer.
type ApproveResult struct {
	// Decision is "allow", "deny" or "always".
	Decision string `json:"decision"`
}
