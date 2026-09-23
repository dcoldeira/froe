package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/tools"
)

// The react strategy is the fallback for models with no usable function-calling
// API and no grammar-capable backend (docs/ARCHITECTURE.md §4).
//
// The protocol is a fenced block because small models reproduce fenced code
// reliably — it is the single most common shape in their training data — where
// bespoke delimiters like "Action:" drift almost immediately.
var reactBlock = regexp.MustCompile("(?s)```tool\\s*\\n(.*?)```")

// reactPrompt describes the protocol to the model.
func reactPrompt(reg *tools.Registry) string {
	var b strings.Builder
	b.WriteString("You can use tools. To call one, emit a fenced block exactly like this:\n\n")
	b.WriteString("```tool\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"main.go\"}}\n```\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- One tool call per block. Emit the block and STOP; the result comes back in the next message.\n")
	b.WriteString("- Use only the tools listed below, with exactly these argument names.\n")
	b.WriteString("- When you have finished and need no more tools, reply with the answer and no block.\n\n")
	b.WriteString("Available tools:\n")
	for _, name := range reg.Names() {
		t, _ := reg.Get(name)
		b.WriteString(fmt.Sprintf("\n- %s: %s\n  arguments: %s\n",
			t.Name(), t.Description(), compactJSON(t.Schema())))
	}
	return b.String()
}

// parseReact extracts tool calls from model text, returning the text with the
// blocks stripped.
//
// A malformed block is returned as an error rather than silently dropped: the
// agent feeds the parse failure back to the model, which is the only way a
// small model recovers from its own bad output.
func parseReact(text string) (clean string, calls []provider.ToolCall, err error) {
	matches := reactBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil, nil
	}

	for i, m := range matches {
		body := strings.TrimSpace(m[1])
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if e := json.Unmarshal([]byte(body), &call); e != nil {
			return text, nil, fmt.Errorf("tool block is not valid JSON: %v. Emit exactly {\"name\": ..., \"arguments\": {...}}", e)
		}
		if call.Name == "" {
			return text, nil, fmt.Errorf("tool block has no \"name\" field")
		}
		args := string(call.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		calls = append(calls, provider.ToolCall{
			ID:        fmt.Sprintf("react_%d", i),
			Name:      call.Name,
			Arguments: args,
		})
	}
	return strings.TrimSpace(reactBlock.ReplaceAllString(text, "")), calls, nil
}

// compactJSON strips whitespace so schemas do not bloat the prompt. Every token
// spent describing a tool is a token unavailable for actual work, and on a 32K
// window that trade is real.
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
