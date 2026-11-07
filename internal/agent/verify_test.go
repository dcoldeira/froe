package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

// scripted is a fake Provider that plays back one reply per turn: a tool call,
// or plain text when call is nil. It records every request so a test can see
// what the model was sent.
type scripted struct {
	replies []reply
	seen    [][]provider.Message
}

type reply struct {
	text string
	call *provider.ToolCall
}

func (*scripted) Name() string        { return "fake" }
func (*scripted) Caps() provider.Caps { return provider.Caps{NativeTools: true} }

func (s *scripted) Chat(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	s.seen = append(s.seen, req.Messages)
	r := reply{text: "(script exhausted)"}
	if n := len(s.seen) - 1; n < len(s.replies) {
		r = s.replies[n]
	}
	ch := make(chan provider.Event, 4)
	if r.call != nil {
		if r.text != "" {
			ch <- provider.Event{Kind: provider.KindText, Text: r.text}
		}
		c := *r.call
		ch <- provider.Event{Kind: provider.KindToolCall, ToolCall: &c}
	} else {
		ch <- provider.Event{Kind: provider.KindText, Text: r.text}
	}
	ch <- provider.Event{Kind: provider.KindDone, Metrics: &provider.Metrics{}}
	close(ch)
	return ch, nil
}

var (
	editCall = &provider.ToolCall{ID: "e1", Name: "edit_file",
		Arguments: `{"path":"a.txt","old_string":"hello","new_string":"bye"}`}
	bashCall = &provider.ToolCall{ID: "b1", Name: "bash", Arguments: `{"command":"true"}`}
)

// runScript drives an agent over a script and returns the fake and whether
// the verify push was sent.
func runScript(t *testing.T, task string, replies ...reply) (*scripted, bool) {
	t.Helper()
	p := &scripted{replies: replies}
	a := newTestAgent(t, p)
	pushed := false
	for ev := range a.Run(context.Background(), task) {
		if ev.Kind == KindToolResult && ev.Tool == "(verify)" {
			pushed = true
		}
		if ev.Kind == KindError {
			t.Fatalf("run failed: %v", ev.Err)
		}
	}
	return p, pushed
}

func lastUser(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// The 07-multi-site failure: edit, declare done, never run the check the task
// asked for. The model must be sent back once to run it.
func TestFinishingWithUncheckedEditsIsSentBackOnce(t *testing.T) {
	p, pushed := runScript(t, "Change hello to bye. The tests must still pass.",
		reply{call: editCall},
		reply{text: "done"},
		reply{call: bashCall},
		reply{text: "done, and the check passes"},
	)
	if !pushed {
		t.Fatal("finished with an unchecked edit and was not sent back")
	}
	if len(p.seen) != 4 {
		t.Fatalf("want 4 model calls (edit, done, check, done), got %d", len(p.seen))
	}
	if got := lastUser(p.seen[2]); !strings.Contains(got, "ran nothing afterwards") || !strings.Contains(got, "a.txt") {
		t.Fatalf("third request should carry the push naming the file, got %q", got)
	}
}

// Once only: a model that ignores the push is not pushed again - the run ends
// with its answer rather than looping.
func TestVerifyPushIsNotRepeated(t *testing.T) {
	p, pushed := runScript(t, "Change hello to bye and run the tests.",
		reply{call: editCall},
		reply{text: "done"},
		reply{text: "still done"},
	)
	if !pushed {
		t.Fatal("expected one push")
	}
	if len(p.seen) != 3 {
		t.Fatalf("want 3 model calls, got %d - the push repeated or the run did not end", len(p.seen))
	}
}

// A model that ran a command after its last edit has checked its work.
func TestCheckedEditsFinishWithoutAPush(t *testing.T) {
	p, pushed := runScript(t, "Change hello to bye. The tests must still pass.",
		reply{call: editCall},
		reply{call: bashCall},
		reply{text: "done"},
	)
	if pushed || len(p.seen) != 3 {
		t.Fatalf("checked run was pushed (pushed=%v, calls=%d)", pushed, len(p.seen))
	}
}

// An edit AFTER the last check is unchecked again.
func TestEditAfterTheCheckNeedsANewCheck(t *testing.T) {
	_, pushed := runScript(t, "Change hello to bye. The build must still work.",
		reply{call: editCall},
		reply{call: bashCall},
		reply{call: &provider.ToolCall{ID: "e2", Name: "edit_file",
			Arguments: `{"path":"a.txt","old_string":"bye","new_string":"ciao"}`}},
		reply{text: "done"},
		reply{call: bashCall},
		reply{text: "done"},
	)
	if !pushed {
		t.Fatal("a later edit was never checked, but no push was sent")
	}
}

// A task that asks for no check does not pay a turn for one.
func TestNoPushWhenTheTaskAsksForNoCheck(t *testing.T) {
	p, pushed := runScript(t, "Change hello to bye.",
		reply{call: editCall},
		reply{text: "done"},
	)
	if pushed || len(p.seen) != 2 {
		t.Fatalf("pushed a task that asked for no check (pushed=%v, calls=%d)", pushed, len(p.seen))
	}
}

// A run that only read files has nothing to check.
func TestNoPushWithoutEdits(t *testing.T) {
	_, pushed := runScript(t, "Read a.txt and confirm what it says.",
		reply{call: &provider.ToolCall{ID: "r1", Name: "read_file", Arguments: `{"path":"a.txt"}`}},
		reply{text: "it says hello"},
	)
	if pushed {
		t.Fatal("pushed a run that changed nothing")
	}
}

// The push quotes the task's own check, so the model runs that and not a
// guess at what "checking" might mean.
func TestVerifyPushQuotesTheTasksOwnCheck(t *testing.T) {
	p, _ := runScript(t, "Change hello to bye.\n\nThe module must still import cleanly when you are done.",
		reply{call: editCall},
		reply{text: "done"},
		reply{call: bashCall},
		reply{text: "done"},
	)
	if got := lastUser(p.seen[2]); !strings.Contains(got, `"The module must still import cleanly when you are done."`) {
		t.Fatalf("push should quote the task's check sentence, got %q", got)
	}
}

func TestAsksForCheck(t *testing.T) {
	for task, want := range map[string]bool{
		"Run the tests to confirm.":                       true,
		"The project must still build when you are done.": true,
		"The module must still import cleanly.":           true,
		"Rename Fetch to Get everywhere.":                 false,
		"Change DEFAULT_SHOTS from 1000 to 4096.":         false,
	} {
		if got := asksForCheck(task); got != want {
			t.Errorf("asksForCheck(%q) = %v, want %v", task, got, want)
		}
	}
}

var failCall = &provider.ToolCall{ID: "f1", Name: "bash", Arguments: `{"command":"false"}`}

// Eval task 01, 2026-11-01: fixed, tested green, then "The task is complete."
// with another look every turn until the stuck detector ended the run. With
// the change checked and passing, the claim ends the run.
func TestDoneAfterAPassingCheckEndsTheRun(t *testing.T) {
	p, _ := runScript(t, "Change hello to bye. The tests must still pass.",
		reply{call: editCall},
		reply{call: bashCall},
		reply{text: "The task is complete.", call: readCall("r1", "a.txt")},
		reply{text: "should never be asked"},
	)
	if len(p.seen) != 3 {
		t.Fatalf("%d requests, want 3: the done claim after a passing check ends the run", len(p.seen))
	}
}

// A failing check is not finished, whatever the model says.
func TestDoneAfterAFailingCheckContinues(t *testing.T) {
	p, _ := runScript(t, "Change hello to bye. The tests must still pass.",
		reply{call: editCall},
		reply{call: failCall},
		reply{text: "The task is complete.", call: readCall("r1", "a.txt")},
		reply{text: "it fails; looking again"},
	)
	if len(p.seen) != 4 {
		t.Fatalf("%d requests, want 4: a failing check must not end the run", len(p.seen))
	}
}

// Nothing checked since the edit: the claim alone is not enough.
func TestDoneWithoutACheckContinues(t *testing.T) {
	p, _ := runScript(t, "Change hello to bye.",
		reply{call: editCall},
		reply{text: "The task is complete.", call: readCall("r1", "a.txt")},
		reply{text: "done"},
	)
	if len(p.seen) != 3 {
		t.Fatalf("%d requests, want 3: an unchecked edit must not end the run early", len(p.seen))
	}
}

// Another edit means more to change: the claim does not cut it off.
func TestDoneWithAnEditContinues(t *testing.T) {
	edit2 := &provider.ToolCall{ID: "e2", Name: "edit_file",
		Arguments: `{"path":"a.txt","old_string":"bye","new_string":"ciao"}`}
	p, _ := runScript(t, "Change hello to bye, then to ciao. The tests must still pass.",
		reply{call: editCall},
		reply{call: bashCall},
		reply{text: "The task is complete.", call: edit2},
		reply{call: bashCall},
		reply{text: "done"},
	)
	if len(p.seen) != 5 {
		t.Fatalf("%d requests, want 5: an edit after the claim must run", len(p.seen))
	}
}

// 2026-11-07, qwen3-coder-30b on llama.cpp: "I've created the file" and the
// same write_file again, every turn. The echo of a call that just succeeded
// ends the run instead of running (and asking for) it again.
func TestDoneWithAnEchoOfTheLastCallEndsTheRun(t *testing.T) {
	write := &provider.ToolCall{ID: "w1", Name: "write_file", Arguments: `{"path":"b.md","content":"borrame"}`}
	again := &provider.ToolCall{ID: "w2", Name: "write_file", Arguments: write.Arguments}
	p, _ := runScript(t, "Create b.md containing borrame.",
		reply{call: write},
		reply{text: "I've created the file b.md.", call: again},
		reply{text: "should never be asked"},
	)
	if len(p.seen) != 2 {
		t.Fatalf("%d requests, want 2: the echo should end the run", len(p.seen))
	}
}

// A command that failed may fairly be run again: no echo, no early end.
func TestRerunOfAFailedCommandContinues(t *testing.T) {
	p, _ := runScript(t, "Run the tests.",
		reply{call: failCall},
		reply{text: "That failed; running it again.", call: failCall},
		reply{text: "done"},
	)
	if len(p.seen) != 3 {
		t.Fatalf("%d requests, want 3: a failed command's rerun must run", len(p.seen))
	}
}

// The same call without any message is the stuck detector's business, not this.
func TestSilentRepeatIsNotAnEcho(t *testing.T) {
	write := &provider.ToolCall{ID: "w1", Name: "write_file", Arguments: `{"path":"b.md","content":"x"}`}
	p, _ := runScript(t, "Create b.md containing x.",
		reply{call: write},
		reply{call: write},
		reply{text: "done"},
	)
	if len(p.seen) != 3 {
		t.Fatalf("%d requests, want 3", len(p.seen))
	}
}
