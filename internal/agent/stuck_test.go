package agent

import (
	"strings"
	"testing"

	"github.com/dcoldeira/froe/internal/provider"
)

func call(id, name, args string) reply {
	return reply{call: &provider.ToolCall{ID: id, Name: name, Arguments: args}}
}

// A verbatim repeat never reaches the tool: the model is told it has the
// result. It is warned once, and a model that keeps repeating is stopped.
func TestIdenticalCallsAreAnsweredThenWarnedThenStopped(t *testing.T) {
	read := call("r", "read_file", `{"path":"a.txt"}`)
	p := &scripted{replies: []reply{read, read, read, read, read}}
	_, err := events(t, newTestAgent(t, p), "read a.txt")
	if err == nil || !strings.Contains(err.Error(), "stuck: read_file was called 3 times") {
		t.Fatalf("err = %v, want a stuck abort after 3 identical calls", err)
	}
	var toolResults []string
	for _, m := range p.seen[len(p.seen)-1] {
		if m.Role == provider.RoleTool {
			toolResults = append(toolResults, m.Content)
		}
	}
	if len(toolResults) < 3 {
		t.Fatalf("got %d tool results", len(toolResults))
	}
	if !strings.Contains(toolResults[0], "hello") {
		t.Errorf("first call not served by the tool: %q", toolResults[0])
	}
	if !strings.Contains(toolResults[1], "identical to a call you already made") {
		t.Errorf("repeat not answered from cache: %q", toolResults[1])
	}
	if !strings.Contains(toolResults[2], "stop repeating it") {
		t.Errorf("no warning before the abort: %q", toolResults[2])
	}
}

// Differently worded calls landing on the same output are the same loop.
func TestSameResultFromRewordedCallsIsStopped(t *testing.T) {
	p := &scripted{replies: []reply{
		call("1", "read_file", `{"path":"a.txt"}`),
		call("2", "read_file", `{"path":"./a.txt"}`),
		call("3", "read_file", `{"path":"a.txt","offset":1}`),
		{text: "done"},
	}}
	_, err := events(t, newTestAgent(t, p), "read a.txt")
	if err == nil || !strings.Contains(err.Error(), "returned the same result 3 times") {
		t.Fatalf("err = %v", err)
	}
}

// Reworded searches that all find nothing share one counter, on a longer
// leash than identical calls: a third of the turn budget, capped at five.
func TestFruitlessSearchesAreStoppedAtTheLimit(t *testing.T) {
	var replies []reply
	for i, pat := range []string{"**/*.rs", "**/switch*.go", "src/qrl/**/*.cpp", "**/witness.ts", "**/*.hs", "**/*.jl"} {
		replies = append(replies, call(string(rune('a'+i)), "glob", `{"pattern":"`+pat+`"}`))
	}
	p := &scripted{replies: replies}
	_, err := events(t, newTestAgent(t, p), "find the switch")
	if err == nil || !strings.Contains(err.Error(), "found nothing 5 times") {
		t.Fatalf("err = %v, want a fruitless abort at 5", err)
	}
}

// A search that finds something forgives one miss, so misses either side of
// a hit are not read as a streak.
func TestAFindForgivesAMiss(t *testing.T) {
	var replies []reply
	pats := []string{"**/*.rs", "**/*.hs", "**/*.jl", "**/*.txt", "**/*.cpp", "**/*.ts"}
	for i, pat := range pats {
		replies = append(replies, call(string(rune('a'+i)), "glob", `{"pattern":"`+pat+`"}`))
	}
	replies = append(replies, reply{text: "done"})
	p := &scripted{replies: replies}
	if _, err := events(t, newTestAgent(t, p), "find it"); err != nil {
		t.Fatalf("5 misses around a find were treated as a streak: %v", err)
	}
}

func TestFruitlessLimitScalesWithTheTurnBudget(t *testing.T) {
	a := &Agent{}
	for _, tc := range []struct{ turns, want int }{{25, 5}, {6, 2}, {3, 2}, {12, 4}} {
		if got := a.fruitlessLimit(tc.turns); got != tc.want {
			t.Errorf("fruitlessLimit(%d) = %d, want %d", tc.turns, got, tc.want)
		}
	}
}

// Every "unchanged" rests on the files being as they were: an edit voids the
// cache and the repeat counters.
func TestAnEditResetsTheRepeatCounters(t *testing.T) {
	read := call("r", "read_file", `{"path":"a.txt"}`)
	p := &scripted{replies: []reply{read, read, {call: editCall}, read, read, {text: "done"}}}
	if _, err := events(t, newTestAgent(t, p), "change hello to bye"); err != nil {
		t.Fatalf("reads after an edit counted against reads before it: %v", err)
	}
}
