package repo

import "strings"

// EstimateTokens approximates a token count from text.
//
// This is a HEURISTIC, not a tokenizer. Calling the backend's /tokenize would
// be exact, but it is a network round trip per estimate and not every backend
// offers one — and the budget only needs to be roughly right to stop the map
// swallowing the context window.
//
// Code tokenises more densely than prose because identifiers, punctuation and
// indentation each cost tokens, so ~3.2 chars/token is a closer fit for source
// than the ~4 usually quoted for English. Erring low is deliberate: an
// overestimate drops files that would have fitted, which is the cheaper mistake.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(s)/3 + strings.Count(s, "\n")
}

// Budget splits a context window between the parts of a prompt.
//
// Everything that goes in front of the model competes for one window:
// instructions, the repo map, tool schemas, the conversation and the reply. The
// budget exists so that competition is decided deliberately rather than by
// whichever component is built first.
type Budget struct {
	Total int
	// Reserve is held back for the conversation and the reply. The map and the
	// instructions are only useful if there is room left to act on them.
	Reserve int
	// Tools approximates the tool schemas, which are sent every turn and are
	// not optional. Measured at ~600 tokens for the current nine.
	Tools int
}

// DefaultBudget derives a split from the context window a model is ACTUALLY
// loaded with — not what it could support. Pass the detected size.
func DefaultBudget(ctxMax int) Budget {
	if ctxMax <= 0 {
		ctxMax = 8192
	}
	reserve := ctxMax / 2
	if reserve > 16384 {
		reserve = 16384
	}
	return Budget{Total: ctxMax, Reserve: reserve, Tools: 700}
}

// free is what remains for context after the reply reserve and the tool
// schemas, both of which are non-negotiable.
func (b Budget) free() int {
	f := b.Total - b.Reserve - b.Tools
	if f < 0 {
		return 0
	}
	return f
}

// InstructionAllowance is how many tokens project instructions may use.
//
// Capped rather than unbounded: a large CLAUDE.md is easily 5000 tokens, which
// on an 8192 window leaves nothing for the map, the conversation or the reply.
func (b Budget) InstructionAllowance() int {
	a := b.free() / 2
	if a > 4000 {
		a = 4000
	}
	return a
}

// MapAllowance is how many tokens the repo map may use.
//
// A bigger map buys less than it costs: past a point the model is reading
// structure it will never use, paid for in prefill on every turn.
func (b Budget) MapAllowance() int {
	a := b.free() / 2
	// A map under a few hundred tokens says nothing useful; better to spend
	// nothing and let the model use glob.
	if a < 256 {
		return 0
	}
	if a > 6000 {
		a = 6000
	}
	return a
}
