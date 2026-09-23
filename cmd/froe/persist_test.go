package main

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

func TestTrimHistoryKeepsRecentTurns(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("old", 400)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("mid", 400)},
		{Role: provider.RoleUser, Content: "the recent question"},
		{Role: provider.RoleAssistant, Content: "the recent answer"},
	}
	got := trimHistory(msgs, 200)

	if len(got) >= len(msgs) {
		t.Fatalf("nothing was trimmed: %d messages", len(got))
	}
	// The most recent exchange is what the next turn depends on.
	last := got[len(got)-1]
	if last.Content != "the recent answer" {
		t.Errorf("the newest message was dropped: %q", last.Content)
	}
}

// A tool result referencing a call the model can no longer see is meaningless,
// and some backends reject it outright.
func TestTrimHistoryNeverStartsWithOrphanedToolResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("x", 5000)},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file"}}},
		{Role: provider.RoleTool, ToolCallID: "1", Content: "result"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
	got := trimHistory(msgs, 50)

	if len(got) > 0 && got[0].Role == provider.RoleTool {
		t.Errorf("history begins with an orphaned tool result: %+v", got[0])
	}
}

func TestTrimHistoryLeavesShortHistoryAlone(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi"},
	}
	if got := trimHistory(msgs, 10000); len(got) != len(msgs) {
		t.Errorf("short history was trimmed: %d of %d", len(got), len(msgs))
	}
}

func TestStoredRoundTripPreservesToolCalls(t *testing.T) {
	orig := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "grep", Arguments: `{"pattern":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "call_1", Content: "match"},
	}
	got := fromStored(toStored(orig))

	if len(got) != 2 {
		t.Fatalf("got %d messages", len(got))
	}
	if len(got[0].ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", got[0])
	}
	if got[0].ToolCalls[0].Name != "grep" || got[0].ToolCalls[0].Arguments != `{"pattern":"x"}` {
		t.Errorf("tool call corrupted: %+v", got[0].ToolCalls[0])
	}
	if got[1].ToolCallID != "call_1" {
		t.Errorf("tool_call_id lost: %+v", got[1])
	}
}
