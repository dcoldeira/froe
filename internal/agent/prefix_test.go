package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

// recording is scripted with a deep copy of every request. The agent edits
// messages in place under PrefixReuse, and a shared slice would let a later
// edit rewrite what an earlier request appeared to contain.
type recording struct {
	scripted
	sent [][]provider.Message
}

func (r *recording) Chat(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	r.sent = append(r.sent, append([]provider.Message(nil), req.Messages...))
	return r.scripted.Chat(ctx, req)
}

func sameMessage(a, b provider.Message) bool {
	if a.Role != b.Role || a.Content != b.Content || a.ToolCallID != b.ToolCallID ||
		len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		if a.ToolCalls[i] != b.ToolCalls[i] {
			return false
		}
	}
	return true
}

// extends reports whether next starts with every message of prev.
func extends(prev, next []provider.Message) (bool, int) {
	if len(next) < len(prev) {
		return false, len(next)
	}
	for i := range prev {
		if !sameMessage(prev[i], next[i]) {
			return false, i
		}
	}
	return true, -1
}

func readCall(id, path string) *provider.ToolCall {
	return &provider.ToolCall{ID: id, Name: "read_file", Arguments: `{"path":"` + path + `"}`}
}

func runRecorded(t *testing.T, prefixReuse bool) *recording {
	t.Helper()
	p := &recording{scripted: scripted{replies: []reply{
		{call: readCall("r1", "a.txt")},
		{call: readCall("r2", "b.txt")},
		{call: readCall("r3", "c.txt")},
		{text: "done"},
	}}}
	a := newTestAgent(t, p)
	a.PrefixReuse = prefixReuse
	for ev := range a.Run(context.Background(), "read the three files") {
		if ev.Kind == KindError {
			t.Fatalf("run failed: %v", ev.Err)
		}
	}
	if len(p.sent) < 3 {
		t.Fatalf("only %d requests were sent", len(p.sent))
	}
	return p
}

// Under PrefixReuse every request repeats the previous one exactly and adds to
// its end, so a server like colibri reads only the new part. Measured
// 2026-11-01: 129.5 s for a cold 2094-token prompt, 3.9 s for the next turn.
func TestPrefixReuseRequestsOnlyGrow(t *testing.T) {
	p := runRecorded(t, true)
	for i := 1; i < len(p.sent); i++ {
		if ok, at := extends(p.sent[i-1], p.sent[i]); !ok {
			t.Fatalf("request %d does not extend request %d: they differ at message %d\nbefore: %q\nafter:  %q",
				i+1, i, at, p.sent[i-1][at].Content, p.sent[i][at].Content)
		}
	}
	// The "already explored" note still reaches the model.
	if last := p.sent[len(p.sent)-1]; !strings.Contains(last[len(last)-1].Content, "Already explored") {
		t.Error("the explored note is missing from the newest message")
	}
}

// Without it, notes stay off the history (recency for a small model, no
// accumulation), which is exactly what breaks the extension property. This
// pins the difference so the mode cannot silently stop mattering.
func TestWithoutPrefixReuseNotesAreNotKept(t *testing.T) {
	p := runRecorded(t, false)
	broken := false
	for i := 1; i < len(p.sent); i++ {
		if ok, _ := extends(p.sent[i-1], p.sent[i]); !ok {
			broken = true
		}
	}
	if !broken {
		t.Fatal("requests extend each other without PrefixReuse; the mode no longer changes anything")
	}
}

// When it must shrink, PrefixReuse frees room for several turns at once:
// every shrink rewrites history and costs a full re-read.
func TestFitContextPrefixReuseShrinksToLowWater(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleTool, Content: tokens(400)},
		{Role: provider.RoleTool, Content: tokens(300)},
		{Role: provider.RoleTool, Content: tokens(200)},
	}
	plain := append([]provider.Message(nil), msgs...)
	// 1024 window: budget 768. Total ~1200 tokens at 3 chars a token.
	budgetAgent(1024).fitContext(plain, map[int]bool{0: true, 1: true}, 0)
	a := budgetAgent(1024)
	a.PrefixReuse = true
	a.fitContext(msgs, map[int]bool{0: true, 1: true}, 0)

	dropped := func(ms []provider.Message) int {
		n := 0
		for _, m := range ms {
			if m.Content == droppedToolResult {
				n++
			}
		}
		return n
	}
	if dropped(msgs) <= dropped(plain) {
		t.Fatalf("PrefixReuse dropped %d results, plain %d: it should free more at once", dropped(msgs), dropped(plain))
	}
	total := 0
	for _, m := range msgs {
		total += messageTokens(m)
	}
	budget := 768
	if limit := int(float64(budget) * prefixReuseLowWater); total > limit {
		t.Errorf("after shrinking: %d tokens, want at most %d", total, limit)
	}
}

// fitContext budgets against the window the runtime serves, not the
// registry's maximum. With only CtxMax it never trimmed a 262144-token model
// served at 8192.
func TestFitContextUsesTheServedWindow(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleTool, Content: tokens(9000)},
	}
	a := budgetAgent(262144)
	a.ContextWindow = 8192
	a.fitContext(msgs, map[int]bool{0: true, 1: true}, 0)
	if msgs[2].Content != droppedToolResult {
		t.Fatal("a result larger than the served window was kept")
	}
}
