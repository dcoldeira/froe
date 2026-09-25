package agent

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

// Strict chat templates (Mistral's) refuse a user turn right after a tool
// result or between a tool call and its result. froe's notes - "already
// explored", the repeat warning - must ride on the tool result instead.
func TestNotesNeverBreakToolCallOrdering(t *testing.T) {
	read := &provider.ToolCall{ID: "r", Name: "read_file", Arguments: `{"path":"a.txt"}`}
	p := &scripted{replies: []reply{{call: read}, {call: read}, {call: read}, {text: "done"}}}
	a := newTestAgent(t, p)
	if _, err := events(t, a, "read a.txt"); err != nil {
		t.Fatal(err)
	}
	sawExplored, sawRepeat := false, false
	for n, msgs := range p.seen {
		for i := 1; i < len(msgs); i++ {
			prev, cur := msgs[i-1], msgs[i]
			if cur.Role == provider.RoleUser && (prev.Role == provider.RoleTool || len(prev.ToolCalls) > 0) {
				t.Fatalf("request %d: user message after %s at %d: %q", n, prev.Role, i, cur.Content)
			}
			if cur.Role == provider.RoleTool {
				sawExplored = sawExplored || strings.Contains(cur.Content, "Already explored")
				sawRepeat = sawRepeat || strings.Contains(cur.Content, "You already called")
			}
		}
	}
	if !sawExplored || !sawRepeat {
		t.Fatalf("notes lost: explored %v, repeat %v", sawExplored, sawRepeat)
	}
}
