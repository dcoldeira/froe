// Package perms gates side-effecting tool calls.
//
// The gate exists because the model is not trusted, and a local 7B needs it
// more than a frontier model does: it misuses tools more often and the blast
// radius is identical. docs/ARCHITECTURE.md §6.
package perms

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Decision is the outcome of an approval request.
type Decision int

const (
	Deny Decision = iota
	Allow
	AlwaysAllow
	// DenyPermanently means this tool cannot be approved in this session at
	// all — there is no terminal to ask on. Distinct from Deny because the
	// agent must tell the model not to retry: measured, a model denied a
	// transient-sounding refusal called `go test` six more times and burned
	// seven turns on a decision that was never going to change.
	DenyPermanently
)

// Mode controls how requests are resolved.
type Mode int

const (
	// ModeAsk prompts for every mutating call. The default.
	ModeAsk Mode = iota
	// ModeAcceptEdits auto-approves file edits but still prompts for shell
	// commands, which are unbounded in a way an edit is not.
	ModeAcceptEdits
	// ModeYolo approves everything except the hard denylist, which is never
	// overridable. For sandboxes and throwaway checkouts.
	ModeYolo
)

// Request is one approval question.
type Request struct {
	Tool    string
	Summary string // one line: what will happen
	Detail  string // a diff, or the literal command
}

// Asker decides whether a tool call may proceed.
//
// An interface because the answer comes from somewhere different in each front
// end: a terminal prompt, a Neovim dialog over RPC, or a policy with no human
// at all. The agent must not know or care which.
type Asker interface {
	Ask(Request) Decision
}

// Gate decides whether a tool call may proceed by prompting on a terminal.
type Gate struct {
	Mode Mode
	In   io.Reader
	Out  io.Writer
	// allowed accumulates AlwaysAllow answers for this session only. Nothing
	// is persisted: a standing grant should be a deliberate config edit, not a
	// side effect of one impatient keypress.
	allowed map[string]bool
}

// NewGate builds a gate reading from stdin and writing to stderr, so prompts
// never contaminate stdout.
func NewGate(mode Mode) *Gate {
	return &Gate{Mode: mode, In: os.Stdin, Out: os.Stderr, allowed: map[string]bool{}}
}

// Interactive reports whether the gate can actually ask a human. When it
// cannot, ModeAsk must deny rather than prompt into the void.
//
// This uses a real terminal check rather than testing for a character device:
// /dev/null IS a character device, so `froe do ... < /dev/null` would
// otherwise be treated as interactive and print a prompt nobody can answer.
func (g *Gate) Interactive() bool {
	f, ok := g.In.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Ask resolves an approval request.
func (g *Gate) Ask(req Request) Decision {
	if g.Mode == ModeYolo {
		return Allow
	}
	if g.allowed[req.Tool] {
		return Allow
	}
	if g.Mode == ModeAcceptEdits && req.Tool != "bash" {
		return Allow
	}
	if !g.Interactive() {
		fmt.Fprintf(g.Out, "  refused (%s): no terminal to ask on - rerun interactively or use --yolo\n", req.Tool)
		return DenyPermanently
	}

	fmt.Fprintf(g.Out, "\n  %s\n", req.Summary)
	if req.Detail != "" {
		for _, line := range strings.Split(strings.TrimRight(req.Detail, "\n"), "\n") {
			fmt.Fprintf(g.Out, "  │ %s\n", line)
		}
	}
	fmt.Fprintf(g.Out, "  allow? [y]es / [n]o / [a]lways this tool: ")

	sc := bufio.NewScanner(g.In)
	if !sc.Scan() {
		return Deny
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return Allow
	case "a", "always":
		g.allowed[req.Tool] = true
		return AlwaysAllow
	default:
		return Deny
	}
}
