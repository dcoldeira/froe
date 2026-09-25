package agent

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
)

func budgetAgent(ctx int) *Agent {
	return &Agent{Model: registry.Model{CtxMax: ctx}}
}

func tokens(n int) string { return strings.Repeat("x", n*4) }

// The biggest tool result goes first, not the oldest: a small early grep
// usually holds the finding, a large late read holds the bulk.
func TestFitContextDropsTheBiggestResultFirst(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "where is witness_value defined?"},
		{Role: provider.RoleTool, Content: "src/qrl/causal/witness.py:1: def witness_value(W):"},
		{Role: provider.RoleTool, Content: tokens(700)},
		{Role: provider.RoleTool, Content: tokens(300)},
	}
	budgetAgent(1024).fitContext(msgs, map[int]bool{0: true, 1: true})
	if msgs[3].Content != droppedToolResult {
		t.Error("the biggest result survived")
	}
	if !strings.Contains(msgs[2].Content, "witness_value") {
		t.Error("the small early finding was dropped")
	}
	if msgs[4].Content == droppedToolResult {
		t.Error("dropped more than needed")
	}
}

// Protected messages - the system prompt and the task - are never touched,
// even when the context still does not fit.
func TestFitContextNeverTouchesProtectedMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: tokens(2000)},
		{Role: provider.RoleUser, Content: tokens(2000)},
		{Role: provider.RoleTool, Content: tokens(10)},
	}
	budgetAgent(1024).fitContext(msgs, map[int]bool{0: true, 1: true})
	if msgs[0].Content != tokens(2000) || msgs[1].Content != tokens(2000) {
		t.Fatal("a protected message was shrunk")
	}
	if msgs[2].Content != droppedToolResult {
		t.Error("the one droppable result was kept while over budget")
	}
}

// Only tool results are given up; assistant and user turns carry the thread.
func TestFitContextOnlyDropsToolResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleAssistant, Content: tokens(900)},
		{Role: provider.RoleTool, Content: tokens(100)},
	}
	budgetAgent(1024).fitContext(msgs, map[int]bool{0: true})
	if msgs[1].Content != tokens(900) {
		t.Fatal("an assistant turn was dropped")
	}
}

func TestFitContextLeavesAFittingConversationAlone(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleTool, Content: tokens(100)},
	}
	budgetAgent(8192).fitContext(msgs, nil)
	if msgs[1].Content == droppedToolResult {
		t.Fatal("dropped a result with room to spare")
	}
}

// A result dropped to fit, then asked for again, is served again and does not
// count as a loop (see TestEvictedResultIsServedAgainNotCountedAsALoop); here
// the building block: stillHeld sees the drop.
func TestStillHeldSeesADroppedResult(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleTool, Content: "witness.py:1"}}
	if !stillHeld(msgs, "witness.py:1") {
		t.Fatal("held result not found")
	}
	msgs[0].Content = droppedToolResult
	if stillHeld(msgs, "witness.py:1") {
		t.Fatal("dropped result reported as held")
	}
}
