package agent

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/tools"
)

func TestParseReactExtractsCallAndStripsBlock(t *testing.T) {
	text := "I'll look at the file.\n\n```tool\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"calc.go\"}}\n```\n"

	clean, calls, err := parseReact(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Errorf("name = %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, "calc.go") {
		t.Errorf("arguments = %q", calls[0].Arguments)
	}
	if strings.Contains(clean, "```tool") {
		t.Errorf("block was not stripped: %q", clean)
	}
	if !strings.Contains(clean, "I'll look at the file.") {
		t.Errorf("surrounding prose was lost: %q", clean)
	}
}

func TestParseReactNoBlockIsNotAnError(t *testing.T) {
	clean, calls, err := parseReact("Just an answer, no tools needed.")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("got %d calls, want 0", len(calls))
	}
	if clean != "Just an answer, no tools needed." {
		t.Errorf("text was altered: %q", clean)
	}
}

// Malformed JSON is the characteristic small-model failure. It must surface as
// an error the agent can feed back, never be silently dropped — the retry loop
// is the whole reason react is viable on a 1.5B at all.
func TestParseReactSurfacesMalformedJSON(t *testing.T) {
	_, _, err := parseReact("```tool\n{\"name\": \"read_file\", \"arguments\": {path: calc.go}}\n```")
	if err == nil {
		t.Fatal("malformed JSON must be reported, not dropped")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should tell the model the expected shape, got: %v", err)
	}
}

func TestParseReactRejectsMissingName(t *testing.T) {
	_, _, err := parseReact("```tool\n{\"arguments\": {\"path\": \"x\"}}\n```")
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("got %v, want a missing-name error", err)
	}
}

// Absent arguments must become {} rather than "null", which no tool can decode.
func TestParseReactDefaultsEmptyArguments(t *testing.T) {
	_, calls, err := parseReact("```tool\n{\"name\": \"glob\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Arguments != "{}" {
		t.Fatalf("arguments = %q, want {}", calls[0].Arguments)
	}
}

func TestParseReactHandlesMultipleBlocks(t *testing.T) {
	text := "```tool\n{\"name\":\"a\",\"arguments\":{}}\n```\nthen\n```tool\n{\"name\":\"b\",\"arguments\":{}}\n```"
	_, calls, err := parseReact(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].ID == calls[1].ID {
		t.Error("tool call IDs must be distinct so results map back correctly")
	}
}

// The protocol description must actually list the tools, or a small model has
// no way to know what it may call.
func TestReactPromptListsEveryTool(t *testing.T) {
	reg := tools.NewRegistry()
	prompt := reactPrompt(reg)
	for _, name := range reg.Names() {
		if !strings.Contains(prompt, name) {
			t.Errorf("tool %q is missing from the react prompt", name)
		}
	}
	if !strings.Contains(prompt, "```tool") {
		t.Error("prompt must show the exact block format")
	}
}

// --- grammar strategy ---

func TestGrammarSchemaCoversEveryToolWithItsOwnArguments(t *testing.T) {
	reg := tools.NewRegistry()
	variants := grammarSchema(reg)["oneOf"].([]any)

	// One variant per tool, plus the terminal answer variant.
	if len(variants) != len(reg.Names())+1 {
		t.Fatalf("got %d variants, want %d tools + 1 answer", len(variants), len(reg.Names()))
	}

	seen := map[string]bool{}
	for _, v := range variants {
		props := v.(map[string]any)["properties"].(map[string]any)
		toolProp, isTool := props["tool"]
		if !isTool {
			continue // the answer variant
		}
		name := toolProp.(map[string]any)["const"].(string)
		seen[name] = true

		// The whole point of the grammar is constraining argument NAMES, not
		// just the shape: a free-form object let Bonsai invent "file_path".
		argSchema, ok := props["arguments"].(map[string]any)
		if !ok {
			t.Errorf("tool %q has no argument schema", name)
			continue
		}
		if _, hasProps := argSchema["properties"]; !hasProps {
			t.Errorf("tool %q arguments are free-form; parameter names would be unconstrained", name)
		}
	}
	for _, n := range reg.Names() {
		if !seen[n] {
			t.Errorf("tool %q missing from the grammar", n)
		}
	}
}

func TestParseGrammarToolCall(t *testing.T) {
	answer, calls, err := parseGrammar(`{"reasoning":"need the file","tool":"read_file","arguments":{"path":"a.go"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("call = %+v", calls[0])
	}
	if answer != "" {
		t.Errorf("answer should be empty while a tool is pending, got %q", answer)
	}
}

func TestParseGrammarFinalAnswer(t *testing.T) {
	answer, calls, err := parseGrammar(`{"reasoning":"done","answer":"The bug is in Divide."}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("got %d calls, want 0", len(calls))
	}
	if answer != "The bug is in Divide." {
		t.Errorf("answer = %q", answer)
	}
}

func TestParseGrammarDefaultsEmptyArguments(t *testing.T) {
	_, calls, err := parseGrammar(`{"tool":"glob"}`)
	if err != nil {
		t.Fatal(err)
	}
	if calls[0].Arguments != "{}" {
		t.Errorf("arguments = %q, want {}", calls[0].Arguments)
	}
}

// A decode failure means the BACKEND did not honour the constraint. That is a
// backend problem, not a model problem, so it must be reported plainly rather
// than fed back as a retry the model cannot act on.
func TestParseGrammarReportsBackendViolation(t *testing.T) {
	_, _, err := parseGrammar("Sorry, I cannot do that.")
	if err == nil {
		t.Fatal("expected an error for unconstrained output")
	}
	if !strings.Contains(err.Error(), "grammar") {
		t.Errorf("error should name the cause, got: %v", err)
	}
}

// One unbounded tool result can end a session. Measured 2026-09-11: a 234 KB
// file came back as ~78k tokens against an 8192-token window.
func TestClampResultBoundsHugeToolOutput(t *testing.T) {
	a := &Agent{Model: registry.Model{CtxMax: 8192}}
	huge := strings.Repeat("x. this is a line of file content\n", 8000) // ~270 KB

	got := a.clampResult("read_file", huge)

	if len(got) >= len(huge) {
		t.Fatalf("result was not truncated: %d bytes", len(got))
	}
	// Must fit comfortably inside the window.
	if est := len(got) / 3; est > 8192/2 {
		t.Errorf("truncated result is still ~%d tokens for an 8192 window", est)
	}
	// Silent truncation is the failure to avoid.
	if !strings.Contains(got, "truncated") {
		t.Error("truncation was not reported to the model")
	}
	if !strings.Contains(got, "offset") {
		t.Error("the message should say how to get the rest")
	}
}

func TestClampResultLeavesSmallOutputAlone(t *testing.T) {
	a := &Agent{Model: registry.Model{CtxMax: 8192}}
	small := "package main\n\nfunc main() {}\n"
	if got := a.clampResult("read_file", small); got != small {
		t.Errorf("small result was altered: %q", got)
	}
}

// A bigger window should permit a bigger result.
func TestClampResultScalesWithContext(t *testing.T) {
	huge := strings.Repeat("line of content\n", 20000)
	small := (&Agent{Model: registry.Model{CtxMax: 8192}}).clampResult("read_file", huge)
	big := (&Agent{Model: registry.Model{CtxMax: 131072}}).clampResult("read_file", huge)
	if len(big) <= len(small) {
		t.Errorf("a 131k window (%d bytes) allowed no more than an 8k one (%d bytes)", len(big), len(small))
	}
}
