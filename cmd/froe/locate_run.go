package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dcoldeira/froe/internal/agent"
	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/resolve"
	"github.com/dcoldeira/froe/internal/tools"
)

// This file is the locate RUN: picking a model, searching before the model
// does, spending the turn budget, and checking the answer afterwards. It is
// separate from the command in locate.go because the sequence has to be
// reachable from somewhere other than a flag set - the editor runs the same
// locate over RPC, and a second copy of this ordering would drift from the
// first the day one of them is fixed.
//
// What is NOT here is rendering. locate returns facts; a terminal and an editor
// want different things from the same ones.

// locateOpts is everything a caller decides before the run.
type locateOpts struct {
	Root     string // absolute; nothing outside it is reachable
	Issue    string
	Model    string // "" picks the best available by role
	MaxTurns int

	// Answer receives the model's prose as it arrives, fences stripped by
	// locate itself. At 8 tok/s the streaming IS the progress bar, which is
	// why this is a writer rather than a field on the result.
	Answer io.Writer

	// Style, Quiet and ShowReasoning control the progress written to stderr.
	// The editor path replaces this with an event sink.
	Style         style
	Quiet         bool
	ShowReasoning bool
}

// locateResult is what the run produced, checked but not yet rendered.
type locateResult struct {
	Found       []site
	Missing     []site
	Missed      []missed
	Surrounding []missed
	Resolutions []resolution
	Relined     []relined
	Metrics     *agent.Metrics

	// RunErr is how the agent loop ended, which is not the same as a failure to
	// run at all: a turn-limit overrun still carries a usable answer. The
	// caller gets both and decides what the exit code should be.
	RunErr error
}

// locate runs the whole command and checks its own answer. The returned error
// means the run could not happen; a run that happened badly is in RunErr.
func locate(ctx context.Context, o locateOpts) (*locateResult, error) {
	cat, err := registry.Load(config.Dir())
	if err != nil {
		return nil, err
	}
	choice, err := resolve.Pick(ctx, cat, o.Model)
	if err != nil {
		return nil, err
	}
	p, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return nil, err
	}

	st := o.Style
	projectRoot := repo.FindRoot(o.Root)

	// Fresh context every time, and no session history: the whole point is that
	// the window is spent on THIS question rather than on what happened before.
	projectCtx := buildContextWithRuntime(ctx, o.Root, o.Issue, choice.Model, choice.Runtime, st, o.Quiet)

	// Search before the model does. The terms are the report's own quoted
	// phrases, so this is not a guess, and it costs no turns.
	window := effectiveContext(ctx, choice.Model, choice.Runtime, st, true)
	evidence, presearched := gatherEvidence(ctx, o.Root, o.Issue, window/evidenceShare)
	if evidence != "" {
		projectCtx += "\n\n" + evidence
		if !o.Quiet {
			fmt.Fprintf(os.Stderr, "  %s\n", st.dim(fmt.Sprintf(
				"pre-searched %s from the report, %d tok",
				plural(len(presearched), "term", "terms"), repo.EstimateTokens(evidence))))
		}
	}

	reg := tools.NewReadOnlyRegistry()
	a := &agent.Agent{
		Provider: p,
		Model:    choice.Model,
		Tools:    reg,
		// Nothing here can mutate, so there is nothing to approve. Asking would
		// only stall a read-only command in a pipe.
		Gate:                perms.NewGate(perms.ModeYolo),
		Env:                 tools.Env{Root: o.Root, Project: projectRoot},
		MaxToolResultTokens: window / 4,
		MaxTurns:            o.MaxTurns,
		System:              locateSystem,
		Context:             projectCtx,
	}

	if !o.Quiet {
		fmt.Fprintf(os.Stderr, "%s %s via %s %s\n",
			st.dim("→"), st.bold(choice.Model.ID), st.dim(choice.Runtime.Name),
			st.dim(fmt.Sprintf("(read-only, %d tools)", len(reg.Names()))))
	}

	// The answer streams out with its fences stripped, and is captured as it
	// goes so the places it cites can be checked against the tree and swept for
	// the ones it stopped short of.
	var raw strings.Builder
	answer := newFenceStripper(o.Answer)

	// Every search that WORKED, in order, deduplicated by pattern. These are
	// what the sweep replays: patterns the model itself proved distinctive, so
	// the sweep never invents a search of its own.
	var searches []tools.Search
	seen := map[string]bool{}

	// The line spans the run actually opened, per path. This is what separates
	// a citation froe can corroborate from one it cannot - see surroundings().
	opened := map[string][]openRange{}

	var metrics *agent.Metrics
	runErr := renderAgentTap(ctx, a.Run(ctx, o.Issue), st, o.ShowReasoning, &runTap{
		Text: io.MultiWriter(answer, &raw),
		OnTool: func(name, args, result string) {
			if path, r, ok := readRangeFromCall(name, args, result); ok {
				opened[path] = append(opened[path], r)
				return
			}
			s, ok := searchFromGrepCall(name, args, result)
			if !ok || seen[s.Pattern] {
				return
			}
			seen[s.Pattern] = true
			searches = append(searches, s)
		},
		Metrics: &metrics,
	})
	answer.Close()

	// The searches froe ran before the model started, alongside whatever the
	// model searched for itself. Measured 2026-09-16: two runs of three never
	// ran a grep at all, so a sweep that replays only the model's searches had
	// nothing to replay and both answers stayed at one site of four.
	for _, s := range presearched {
		if seen[s.Pattern] {
			continue
		}
		seen[s.Pattern] = true
		searches = append(searches, s)
	}

	// Line numbers are corrected before anything else reads them: the sweep
	// and the surroundings would otherwise work from the wrong line.
	rel := relines(o.Root, raw.String())
	found, missing := splitByExistence(o.Root, applyRelines(parseSites(raw.String()), rel))
	res := &locateResult{Found: found, Missing: missing, Relined: rel, Metrics: metrics, RunErr: runErr}

	if len(found) > 0 {
		res.Missed = sweepSites(ctx, o.Root, found, searches)
		// A citation the run never opened has nothing corroborating it: the
		// sweep only replays searches, and no search matches a line that does
		// not contain the searched term. Position is the evidence left, and it
		// is free.
		res.Surrounding = surroundings(o.Root, found, opened)
	}

	// A citation that is not in the tree is resolved rather than merely
	// announced - but resolving is not promoting. See the comment block above
	// resolveMissing: the one measured case was a DECOY whose base name
	// resolved cleanly, so swapping the path in would have endorsed the error
	// froe had just caught.
	res.Resolutions = resolveMissing(ctx, o.Root, missing, searches)
	return res, nil
}
