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
	ch := make(chan provider.Event, 3)
	if r.call != nil {
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
