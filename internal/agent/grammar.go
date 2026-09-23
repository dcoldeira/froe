package agent

import (
	"encoding/json"
	"fmt"

	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/tools"
)

// The grammar strategy constrains the sampler so invalid output is impossible
// (docs/ARCHITECTURE.md §4). Where react asks a model nicely to emit valid JSON
// and retries when it does not, grammar makes the malformed token unreachable.
//
// We send a JSON Schema rather than a hand-written GBNF: llama.cpp's server
// compiles schema to grammar internally, which is better tested than anything
// worth writing here and keeps the same payload working on other backends that
// implement OpenAI's response_format.
//
// The schema is a oneOf over per-tool shapes rather than a single object with
// a free-form "arguments" field. That costs a larger grammar, but a free-form
// object constrains only the SHAPE and not the argument NAMES — measured
// 2026-09-11, Bonsai emitted {"tool":"read_file","arguments":{"file_path":...}}
// against a free-form schema, inventing a parameter that does not exist.
// Constraining names is most of the value of using a grammar at all.
func grammarSchema(reg *tools.Registry) map[string]any {
	var variants []any

	for _, name := range reg.Names() {
		t, _ := reg.Get(name)
		var argSchema map[string]any
		if err := json.Unmarshal(t.Schema(), &argSchema); err != nil {
			// A tool with an unparseable schema falls back to a free-form
			// object rather than being dropped from the grammar entirely.
			argSchema = map[string]any{"type": "object"}
		}
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool":      map[string]any{"const": name},
				"arguments": argSchema,
			},
			"required":             []string{"tool", "arguments"},
			"additionalProperties": false,
		})
	}

	// The terminal variant: an answer and no tool.
	variants = append(variants, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{
				"type":        "string",
				"description": "Final answer, when no further tool is needed",
			},
		},
		"required":             []string{"answer"},
		"additionalProperties": false,
	})

	return map[string]any{"oneOf": variants}
}

// grammarResponseFormat renders the schema as an OpenAI response_format value.
func grammarResponseFormat(reg *tools.Registry) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "froe_step",
			"strict": true,
			"schema": grammarSchema(reg),
		},
	}
}

// grammarPrompt describes what the tools DO, and deliberately does not teach
// the output format — the grammar enforces that at the sampler, so spending
// context on a template the model cannot violate is waste.
//
// Measured 2026-09-11 on Qwen2.5-Coder-1.5B, 50 trials per cell: react needs an
// exact format template to work at all (100% with one, 0% without it at every
// temperature tested), while grammar held 100% on a terse prompt. Prompt detail,
// not temperature, was the dominant factor — temperature cost react only ~6%.
func grammarPrompt(reg *tools.Registry) string {
	var b []byte
	b = append(b, "Call one tool at a time; its result comes back in the next message. "...)
	b = append(b, "Answer directly when no tool is needed.\n\nTools:\n"...)
	for _, name := range reg.Names() {
		t, _ := reg.Get(name)
		b = append(b, fmt.Sprintf("\n- %s: %s\n  arguments: %s\n",
			t.Name(), t.Description(), compactJSON(t.Schema()))...)
	}
	return string(b)
}

// parseGrammar decodes a constrained reply into prose plus an optional call.
//
// A decode failure here means the backend did not honour the constraint, which
// is a backend problem rather than a model problem — so it is reported plainly
// instead of being fed back as a retry the model cannot act on.
func parseGrammar(text string) (answer string, calls []provider.ToolCall, err error) {
	var step struct {
		Reasoning string          `json:"reasoning"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Answer    string          `json:"answer"`
	}
	if e := json.Unmarshal([]byte(text), &step); e != nil {
		return "", nil, fmt.Errorf("constrained output was not valid JSON (backend did not honour the grammar): %v", e)
	}

	if step.Tool == "" {
		return step.Answer, nil, nil
	}
	args := string(step.Arguments)
	if args == "" || args == "null" {
		args = "{}"
	}
	return step.Answer, []provider.ToolCall{{
		ID:        "grammar_0",
		Name:      step.Tool,
		Arguments: args,
	}}, nil
}
