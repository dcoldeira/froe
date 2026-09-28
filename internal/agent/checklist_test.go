package agent

import (
	"strings"
	"testing"
)

// The prompt from the Sept 28 session, verbatim.
const nearlyThere = `Nearly there. (1) Replace apply_depolarizing_to_one_qubit with depolarizing_channel(p).apply_to_subsystem(rho, 0, 2) from QRL. (2) Delete werner_chsh_analytic and the unused theoretical_chsh import. (3) Find the crossing by bisection on chsh_parameter_from_state and print it next to 1 - 1/sqrt(2). (4) Drop 0.29289 from p_values. Run it.`

func TestChecklistItemsFromTheRealPrompt(t *testing.T) {
	items := checklistItems(nearlyThere)
	if len(items) != 4 {
		t.Fatalf("want 4 items, got %d: %q", len(items), items)
	}
	if !strings.HasPrefix(items[2], "Find the crossing by bisection") ||
		!strings.Contains(items[2], "1 - 1/sqrt(2)") {
		t.Errorf("item 3 cut wrongly: %q", items[2])
	}
	if !strings.HasPrefix(items[3], "Drop 0.29289") {
		t.Errorf("item 4: %q", items[3])
	}
}

func TestChecklistItemsFromLines(t *testing.T) {
	items := checklistItems("Please:\n1. rename foo\n2. update the tests\n3) run them")
	if len(items) != 3 || items[1] != "update the tests" {
		t.Fatalf("got %q", items)
	}
}

// Code and maths are not lists, and one item is not a list.
func TestChecklistItemsIgnoresNonLists(t *testing.T) {
	for _, task := range []string{
		"call f(1) then g(2) and print 1 - 1/sqrt(2)",
		"(1) just one thing",
		"(2) starts at two (3) and goes on",
		"remove the column. Version 1. is old",
	} {
		if items := checklistItems(task); items != nil {
			t.Errorf("%q: got %q", task, items)
		}
	}
}

func TestAnswerCoversAll(t *testing.T) {
	if answerCoversAll("Done. Replaced the function. All results still match.", 4) {
		t.Error("the Sept 28 answer counted as covering four items")
	}
	if !answerCoversAll("(1) done\n(2) done\n(3) done - crossing 0.292893\n(4) done", 4) {
		t.Error("an item-by-item answer not recognised")
	}
	if !answerCoversAll("1. done\n2. skipped: already gone", 2) {
		t.Error("numbered lines not recognised")
	}
}

// End to end: an edit, then "Done" with no mention of the items, is sent back
// once with the items quoted; the answer after that is accepted.
func TestChecklistNudgeIsSentOnce(t *testing.T) {
	p := &scripted{replies: []reply{{call: editCall}, {text: "Done."}, {text: "Still done."}}}
	a := newTestAgent(t, p)
	seen, err := events(t, a, "(1) change hello to bye in a.txt (2) add a line saying hi")
	if err != nil {
		t.Fatal(err)
	}
	if seen["(checklist)"] != 1 {
		t.Fatalf("checklist push sent %d times, want 1", seen["(checklist)"])
	}
	last := lastUser(p.seen[2])
	if !strings.Contains(last, "(2) add a line saying hi") {
		t.Errorf("push does not quote the items:\n%s", last)
	}
}

// No push when the answer already accounts for each item, or nothing changed.
func TestChecklistNudgeNotSentWhenCoveredOrNoEdits(t *testing.T) {
	p := &scripted{replies: []reply{{call: editCall}, {text: "(1) done (2) done"}}}
	seen, _ := events(t, newTestAgent(t, p), "(1) change hello to bye in a.txt (2) add a line")
	if seen["(checklist)"] != 0 {
		t.Error("pushed although every item was accounted for")
	}

	p = &scripted{replies: []reply{{text: "Here is how."}}}
	seen, _ = events(t, newTestAgent(t, p), "(1) explain foo (2) explain bar")
	if seen["(checklist)"] != 0 {
		t.Error("pushed on a run that changed nothing")
	}
}

// The push is a user turn after an assistant answer, which Mistral accepts.
func TestChecklistNudgeKeepsRoleOrder(t *testing.T) {
	p := &scripted{replies: []reply{{call: editCall}, {text: "Done."}, {text: "(1) done (2) done"}}}
	events(t, newTestAgent(t, p), "(1) change hello to bye in a.txt (2) add a line")
	for i, req := range p.seen {
		if !mistralOrderOK(req) {
			t.Errorf("request %d breaks the role order: %v", i, roles(req))
		}
	}
}
