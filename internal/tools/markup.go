package tools

import (
	"fmt"
	"strings"
)

// toolCallMarkup lists chat-template tokens that end or begin a tool call.
// None of them belongs in source code the model means to write.
var toolCallMarkup = []string{
	"</parameter>", "<parameter=",
	"</function>", "<function=",
	"<tool_call>", "</tool_call>",
	"[TOOL_CALLS]",
	"<|im_start|>", "<|im_end|>",
}

// leakedMarkup refuses text that carries tool-call markup the file does not
// already hold. Measured 2026-09-24: bonsai-27b-lmstudio finished all four
// sites of eval task 07, then wrote a new_string ending in
// "</parameter></function><tool_call>..." - the tail of its own call spilled
// into the argument, the file stopped parsing, and the run timed out. A file
// that already contains a token (a template parser, say) may keep it.
func leakedMarkup(field, text, existing string) error {
	for _, tok := range toolCallMarkup {
		i := strings.Index(text, tok)
		if i < 0 || strings.Contains(existing, tok) {
			continue
		}
		return fmt.Errorf("%s contains tool-call markup %q, which is the end of your own tool call "+
			"leaking into the argument; nothing was written. Send the same call again with %s "+
			"ending before that markup: %q",
			field, tok, field, tail(text[:i], 60))
	}
	return nil
}

// tail returns the last n bytes of s, so the model sees where to stop.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
