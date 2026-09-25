package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

const shownNotMade = "Change a.txt to:\n```\nbye\n```\nRun tests to confirm."

// events runs an agent and returns the synthetic tool events it emitted, by name.
func events(t *testing.T, a *Agent, task string) (map[string]int, error) {
	t.Helper()
	seen := map[string]int{}
	var err error
	for ev := range a.Run(context.Background(), task) {
		if ev.Kind == KindToolResult && strings.HasPrefix(ev.Tool, "(") {
			seen[ev.Tool]++
		}
		if ev.Kind == KindError {
			err = ev.Err
		}
	}
	return seen, err
}

// The 03-add-function failure: a correct change in a code block, no edit.
// A do run is sent back once, and then makes the edit.
func TestShowingTheChangeWithoutMakingItIsSentBackOnce(t *testing.T) {
	p := &scripted{replies: []reply{{text: shownNotMade}, {call: editCall}, {text: "done"}}}
	a := newTestAgent(t, p)
	a.ApplyEdits = true
	seen, err := events(t, a, "change hello to bye in a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if seen["(apply)"] != 1 {
		t.Fatalf("apply push sent %d times, want 1", seen["(apply)"])
	}
	got, _ := os.ReadFile(filepath.Join(a.Env.Root, "a.txt"))
	if string(got) != "bye\n" {
		t.Fatalf("edit not made after the push: %q", got)
	}
}

// Never twice: a model that answers with a code block again is let go.
func TestApplyPushIsNotRepeated(t *testing.T) {
	p := &scripted{replies: []reply{{text: shownNotMade}, {text: shownNotMade}}}
	a := newTestAgent(t, p)
	a.ApplyEdits = true
	seen, _ := events(t, a, "change hello to bye in a.txt")
	if seen["(apply)"] != 1 || len(p.seen) != 2 {
		t.Fatalf("apply pushes %d, requests %d; want 1 and 2", seen["(apply)"], len(p.seen))
	}
}

// Chat leaves ApplyEdits off: there a code block is the answer.
func TestCodeBlockAnswerStandsOutsideDo(t *testing.T) {
	p := &scripted{replies: []reply{{text: shownNotMade}}}
	seen, _ := events(t, newTestAgent(t, p), "how would I change a.txt?")
	if seen["(apply)"] != 0 || len(p.seen) != 1 {
		t.Fatalf("chat answer was pushed back: %v", seen)
	}
}

// flaky fails its first `fails` requests the way Ollama does when it cannot
// parse the model's tool call, then plays the script.
type flaky struct {
	scripted
	fails int
}

func (f *flaky) Chat(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	if f.fails > 0 {
		f.fails--
		f.seen = append(f.seen, req.Messages)
		f.replies = append([]reply{{}}, f.replies...) // keep script aligned with requests
		return nil, &provider.ToolCallFormatError{Msg: "ollama: HTTP 500: unexpected end of JSON input"}
	}
	return f.scripted.Chat(ctx, req)
}

// A lost reply is asked for again, with a note, instead of ending the run.
func TestUnparseableToolCallIsRetried(t *testing.T) {
	p := &flaky{scripted: scripted{replies: []reply{{call: editCall}, {text: "done"}}}, fails: 1}
	a := newTestAgent(t, p)
	seen, err := events(t, a, "change hello to bye in a.txt")
	if err != nil {
		t.Fatalf("run ended on a retryable error: %v", err)
	}
	if seen["(retry)"] != 1 {
		t.Fatalf("retries %d, want 1", seen["(retry)"])
	}
	if !strings.Contains(lastUser(p.seen[1]), "not valid JSON") {
		t.Fatalf("retry carried no note: %q", lastUser(p.seen[1]))
	}
	if strings.Contains(lastUser(p.seen[2]), "not valid JSON") {
		t.Fatal("note kept after a successful turn")
	}
	got, _ := os.ReadFile(filepath.Join(a.Env.Root, "a.txt"))
	if string(got) != "bye\n" {
		t.Fatalf("edit not made after retry: %q", got)
	}
}

// Bounded: a backend that never parses the call ends the run.
func TestUnparseableToolCallRetriesAreBounded(t *testing.T) {
	p := &flaky{scripted: scripted{replies: []reply{{text: "done"}}}, fails: 10}
	_, err := events(t, newTestAgent(t, p), "x")
	if err == nil {
		t.Fatal("run did not end")
	}
	if len(p.seen) != 1+maxFormatRetries {
		t.Fatalf("requests %d, want %d", len(p.seen), 1+maxFormatRetries)
	}
}
