package agent

import (
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

var globCall = &provider.ToolCall{ID: "g1", Name: "glob", Arguments: `{"pattern":"*.txt"}`}

// The 2026-10-01 failure: a count answered from memory. A careful task is sent
// back once to look, and the run then ends on the answer the tool backed.
func TestCarefulAnswerWithoutLookingIsSentBackOnce(t *testing.T) {
	p := &scripted{replies: []reply{{text: "8 files"}, {call: globCall}, {text: "1 file"}}}
	a := newTestAgent(t, p)
	a.RequireEvidence = true
	seen, err := events(t, a, "count the .txt files")
	if err != nil {
		t.Fatal(err)
	}
	if seen["(evidence)"] != 1 || len(p.seen) != 3 {
		t.Fatalf("evidence pushes %d, requests %d; want 1 and 3", seen["(evidence)"], len(p.seen))
	}
}

// Never twice: a model that still will not look is let go.
func TestEvidencePushIsNotRepeated(t *testing.T) {
	p := &scripted{replies: []reply{{text: "8 files"}, {text: "still 8"}}}
	a := newTestAgent(t, p)
	a.RequireEvidence = true
	seen, _ := events(t, a, "count the .txt files")
	if seen["(evidence)"] != 1 || len(p.seen) != 2 {
		t.Fatalf("evidence pushes %d, requests %d; want 1 and 2", seen["(evidence)"], len(p.seen))
	}
}

// An answer a tool call stands behind is not questioned.
func TestCarefulAnswerAfterLookingStands(t *testing.T) {
	p := &scripted{replies: []reply{{call: globCall}, {text: "1 file"}}}
	a := newTestAgent(t, p)
	a.RequireEvidence = true
	seen, _ := events(t, a, "count the .txt files")
	if seen["(evidence)"] != 0 || len(p.seen) != 2 {
		t.Fatalf("a checked answer was pushed back: %v", seen)
	}
}

// Quick tasks leave RequireEvidence off: an explanation is answered directly.
func TestQuickAnswerWithoutLookingStands(t *testing.T) {
	p := &scripted{replies: []reply{{text: "a process matrix is..."}}}
	seen, _ := events(t, newTestAgent(t, p), "explain a process matrix")
	if seen["(evidence)"] != 0 || len(p.seen) != 1 {
		t.Fatalf("a quick answer was pushed back: %v", seen)
	}
}
