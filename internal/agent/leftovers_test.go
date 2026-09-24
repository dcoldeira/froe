package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

// The 07-multi-site shape in miniature: one column spelled three ways, and a
// lookalike that must not be flagged.
const switchTable = `REGISTERED = ("Process", "Causal Order", "P_win")
HEADERS = ("Process", "Causal\nOrder", "P_win")
DAG = ("Node", "Causal Ordering")


def row():
    return ["switch", causal_order(), "0.85"]


def causal_order():
    return "indefinite"
`

func leftoverAgent(t *testing.T, p provider.Provider) *Agent {
	t.Helper()
	a := newTestAgent(t, p)
	if err := os.WriteFile(filepath.Join(a.Env.Root, "switch_table.py"), []byte(switchTable), 0o644); err != nil {
		t.Fatal(err)
	}
	return a
}

// run drives an agent over a script and reports whether each push was sent.
func runLeftovers(t *testing.T, task string, replies ...reply) (*scripted, bool) {
	t.Helper()
	p := &scripted{replies: replies}
	a := leftoverAgent(t, p)
	pushed := false
	for ev := range a.Run(context.Background(), task) {
		if ev.Kind == KindToolResult && ev.Tool == "(leftovers)" {
			pushed = true
		}
		if ev.Kind == KindError {
			t.Fatalf("run failed: %v", ev.Err)
		}
	}
	return p, pushed
}

var blankFirst = &provider.ToolCall{ID: "e1", Name: "edit_file",
	Arguments: `{"path":"switch_table.py","old_string":"\"Causal Order\", ","new_string":""}`}

// What qwen3-nothink:8b did on 07: change the first spelling and stop. The
// other spellings must come back to it; the lookalike must not.
func TestFinishingWithOtherSpellingsLeftIsSentBack(t *testing.T) {
	p, pushed := runLeftovers(t, `Remove the "Causal Order" column from switch_table.py completely.`,
		reply{call: blankFirst},
		reply{text: "done"},
		reply{text: "done, the rest was handled"},
	)
	if !pushed {
		t.Fatal("finished with three spellings left and was not sent back")
	}
	push := lastUser(p.seen[2])
	for _, want := range []string{"switch_table.py:2", "switch_table.py:7", "switch_table.py:10"} {
		if !strings.Contains(push, want) {
			t.Errorf("push should list %s:\n%s", want, push)
		}
	}
	if strings.Contains(push, "switch_table.py:3") || strings.Contains(push, "Ordering") {
		t.Errorf("push listed the Causal Ordering lookalike:\n%s", push)
	}
	if len(p.seen) != 3 {
		t.Fatalf("want 3 model calls (edit, done, done), got %d - the push repeated", len(p.seen))
	}
}

// A run that changed every spelling has nothing left to be told about.
func TestNoLeftoverPushWhenEverySpellingIsGone(t *testing.T) {
	clearAll := &provider.ToolCall{ID: "w1", Name: "write_file",
		Arguments: `{"path":"switch_table.py","content":"REGISTERED = (\"Process\", \"P_win\")\nDAG = (\"Node\", \"Causal Ordering\")\n"}`}
	_, pushed := runLeftovers(t, `Remove the "Causal Order" column from switch_table.py completely.`,
		reply{call: clearAll},
		reply{text: "done"},
	)
	if pushed {
		t.Fatal("pushed although only the lookalike remains")
	}
}

// No edits, nothing to check.
func TestNoLeftoverPushWithoutEdits(t *testing.T) {
	_, pushed := runLeftovers(t, `Where is the "Causal Order" column defined?`,
		reply{call: &provider.ToolCall{ID: "g1", Name: "grep", Arguments: `{"pattern":"Causal Order"}`}},
		reply{text: "switch_table.py:1"},
	)
	if pushed {
		t.Fatal("pushed a run that changed nothing")
	}
}

// One word out of a sentence is not a change term - it would match anything.
func TestChangeTermsNeedTwoWords(t *testing.T) {
	var c changeTerms
	c.fromTask(`Rename "Fetch" to "Get", and remove the "Causal Order" column.`)
	c.fromCall("edit_file", `{"path":"a.py","old_string":"18","new_string":""}`, "edited")
	c.fromCall("grep", `{"pattern":"Causal"}`, "a.py:1: Causal")
	if len(c.list) != 1 || c.list[0].Pattern != "Causal Order" {
		t.Fatalf("want only \"Causal Order\", got %+v", c.list)
	}
}

// After an edit, re-reading the file must show the edit - not "identical to a
// call you already made". Measured 2026-09-24: that stale answer, three times,
// got bonsai-27b aborted as stuck on 06 in three runs of three.
func TestReadAfterEditIsNotServedFromCache(t *testing.T) {
	read := &provider.ToolCall{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`}
	p, _ := runScript(t, "Change hello to bye.",
		reply{call: read},
		reply{call: editCall},
		reply{call: read},
		reply{text: "done"},
	)
	var last string
	for _, m := range p.seen[3] {
		if m.Role == provider.RoleTool {
			last = m.Content
		}
	}
	if strings.Contains(last, "identical to a call") || !strings.Contains(last, "bye") {
		t.Fatalf("re-read after the edit was served stale: %q", last)
	}
}

// A name the run searched for to find its way, and that its edit kept, is not
// something it changed. Measured 2026-09-24 on 06: flagging the assert that
// reads the table's name cost bonsai-27b a turn and a correct run its time.
func TestSearchesForNavigationAreNotChangeTerms(t *testing.T) {
	var c changeTerms
	c.fromTask(`The "Causal Order" column is redundant; remove it.`)
	c.fromCall("grep", `{"pattern":"WITNESS_SUMMARY_WIDTHS"}`, "r.py:10: WITNESS_SUMMARY_WIDTHS = (24, 18)")
	c.fromCall("edit_file", `{"path":"r.py","old_string":"\"Robustness\", \"Causal Order\", \"P_win\",","new_string":"\"Robustness\", \"P_win\","}`, "edited")
	c.fromCall("edit_file", `{"path":"r.py","old_string":"WITNESS_SUMMARY_WIDTHS = (24, 12, 20, 18, 18, 12)","new_string":"WITNESS_SUMMARY_WIDTHS = (24, 12, 20, 18, 12)"}`, "edited")
	got := c.removed()
	if len(got) != 1 || got[0].Pattern != "Causal Order" {
		t.Fatalf("want only \"Causal Order\" as removed, got %+v", got)
	}
}

// A result fitContext has replaced is served again on request, not answered
// with "use what you already have" - the model no longer has it - and the
// repeat is not counted toward the loop limit.
func TestEvictedResultIsServedAgainNotCountedAsALoop(t *testing.T) {
	p := &scripted{}
	a := newTestAgent(t, p)
	a.Model.CtxMax = 2048 // three large reads cannot all stay in the window
	var calls []reply
	for _, f := range []string{"a", "b", "c"} {
		body := strings.Repeat(f+" line of a large file\n", 400)
		if err := os.WriteFile(filepath.Join(a.Env.Root, f+".txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, reply{call: &provider.ToolCall{ID: f, Name: "read_file",
			Arguments: `{"path":"` + f + `.txt"}`}})
	}
	again := reply{call: &provider.ToolCall{ID: "a2", Name: "read_file", Arguments: `{"path":"a.txt"}`}}
	p.replies = append(calls, again, again, again, reply{text: "done"})

	var runErr error
	for ev := range a.Run(context.Background(), "Read a.txt, b.txt and c.txt.") {
		if ev.Kind == KindError {
			runErr = ev.Err
		}
	}
	if runErr != nil {
		t.Fatalf("re-reading an evicted file was treated as a loop: %v", runErr)
	}
	// The first re-read of a.txt (request 4 answers it) must carry the file.
	var got string
	for _, m := range p.seen[4] {
		if m.Role == provider.RoleTool {
			got = m.Content
		}
	}
	if !strings.Contains(got, "a line of a large file") {
		t.Fatalf("re-read of an evicted file was not served: %.120q", got)
	}
}
