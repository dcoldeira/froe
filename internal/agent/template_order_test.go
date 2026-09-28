package agent

import (
	"context"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

// mistralOrderOK applies Mistral's template rule: after the system message,
// user turns and assistant turns WITHOUT tool calls must alternate, starting
// with the user. Tool calls and results are exempt.
func mistralOrderOK(msgs []provider.Message) bool {
	i := 0
	for _, m := range msgs {
		if m.Role == provider.RoleSystem {
			continue
		}
		if m.Role == provider.RoleUser || (m.Role == provider.RoleAssistant && len(m.ToolCalls) == 0) {
			if (m.Role == provider.RoleUser) != (i%2 == 0) {
				return false
			}
			i++
		}
	}
	return true
}

// Running out of turns straight after a tool result must not send a user turn
// there: Mistral's template answers that with HTTP 500 and the answer is lost.
func TestOutOfTurnsKeepsMistralRoleOrder(t *testing.T) {
	read := &provider.ToolCall{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`}
	p := &scripted{replies: []reply{{call: read}, {text: "done"}}}
	a := newTestAgent(t, p)
	a.MaxTurns = 1
	for range a.Run(context.Background(), "read a.txt") {
	}
	if len(p.seen) < 2 {
		t.Fatalf("final answer never requested (%d requests)", len(p.seen))
	}
	for i, req := range p.seen {
		if !mistralOrderOK(req) {
			t.Errorf("request %d breaks the role order: %v", i, roles(req))
		}
	}
}

func roles(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		r := string(m.Role)
		if len(m.ToolCalls) > 0 {
			r += "+calls"
		}
		out = append(out, r)
	}
	return out
}

// Tool-call arguments count: an assistant turn carrying a large old_string is
// as real to the window as a tool result of the same size.
func TestFitContextCountsToolCallArguments(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{Name: "edit_file", Arguments: tokens(600)}}},
		{Role: provider.RoleTool, Content: tokens(300)},
	}
	budgetAgent(1024).fitContext(msgs, map[int]bool{0: true}, 0)
	if msgs[2].Content != droppedToolResult {
		t.Error("over budget once arguments count, but nothing was dropped")
	}
}

// Overhead - tool definitions, froe's notes - comes off the same budget.
func TestFitContextSubtractsOverhead(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleTool, Content: tokens(500)},
	}
	budgetAgent(1024).fitContext(msgs, nil, 0)
	if msgs[1].Content == droppedToolResult {
		t.Fatal("dropped with no overhead")
	}
	budgetAgent(1024).fitContext(msgs, nil, 400)
	if msgs[1].Content != droppedToolResult {
		t.Error("overhead ignored")
	}
}

// Quoting the task back is not showing a change; new code is.
func TestShowsCodeIgnoresAQuoteOfTheTask(t *testing.T) {
	task := "The user highlighted this text:\n\n```\ncan you read this?\n```\n\nThe user's request: can you see it?"
	if showsCode("Yes, I see it:\n\n```\ncan you read this?\n```", task) {
		t.Error("a quote of the selection counted as a proposed change")
	}
	if !showsCode("Add this:\n\n```python\ndef f():\n    return 1\n```", task) {
		t.Error("new code not recognised")
	}
}
