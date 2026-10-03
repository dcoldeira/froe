package perms

import (
	"os"
	"testing"

	"golang.org/x/term"
)

// Prompt replaces only the question; an "always" answer is still remembered
// by the gate, so the next call is not asked again.
func TestPromptHookKeepsStandingGrants(t *testing.T) {
	tty, err := os.Open("/dev/tty")
	if err != nil || !term.IsTerminal(int(tty.Fd())) {
		t.Skip("needs a terminal: the gate refuses to ask without one")
	}
	defer tty.Close()

	asked := 0
	g := &Gate{Mode: ModeAsk, In: tty, Out: os.Stderr, allowed: map[string]bool{},
		Prompt: func(Request) Decision { asked++; return AlwaysAllow }}
	g.Ask(Request{Tool: "bash"})
	if d := g.Ask(Request{Tool: "bash"}); d != Allow || asked != 1 {
		t.Fatalf("second call: %v after %d prompts, want Allow after 1", d, asked)
	}
}
