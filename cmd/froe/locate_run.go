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

// recheckTurns bounds the second pass: room to read one of the lines it was
// shown, then answer.
const recheckTurns = 3

// recheckPrompt hands the sweep's unjudged lines back to the model.
func recheckPrompt(ms []missed) string {
	var b strings.Builder
	b.WriteString("froe searched inside the files you cited, using the searches that worked, " +
		"and found these lines that are not in your WHERE:\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "  %s:%d  %s\n", m.Path, m.Line, truncate(m.Text, 90))
	}
	b.WriteString("\nFor each one: if it must change along with the places you gave - the same " +
		"column's header, width, row value, a caller - add it to WHERE. If it is a lookalike " +
		"or unrelated, name it in WATCH OUT instead. Then give the complete answer again, " +
		"in the same three sections.")
	return b.String()
}

// checkAnswer runs every deterministic check on one answer: corrects its line
// numbers, splits its citations by existence, sweeps the cited files, and
// finds the surroundings of citations the run never opened.
func checkAnswer(ctx context.Context, root, answer string, searches []tools.Search, opened map[string][]openRange) *locateResult {
	// Line numbers are corrected before anything else reads them: the sweep
	// and the surroundings would otherwise work from the wrong line.
	rel := relines(root, answer)
	found, missing := splitByExistence(root, applyRelines(parseSites(answer), rel))
	res := &locateResult{Found: found, Missing: missing, Relined: rel}
	if len(found) > 0 {
		// A line the answer names anywhere - including as a lookalike in
		// WATCH OUT - has been judged, so the sweep does not list it again.
		res.Missed = withoutMentioned(sweepSites(ctx, root, found, searches), answer)
		// A citation the run never opened has nothing corroborating it: the
		// sweep only replays searches, and no search matches a line that does
		// not contain the searched term. Position is the evidence left, and it
		// is free.
		res.Surrounding = surroundings(root, found, opened)
	}
	return res
}

// withoutMentioned drops swept lines that the answer cites anywhere.
func withoutMentioned(ms []missed, answer string) []missed {
	named := map[string]bool{}
	for _, m := range siteLine.FindAllStringSubmatch(answer, -1) {
		named[strings.TrimPrefix(strings.Trim(m[1], "`\"',"), "./")+":"+m[2]] = true
	}
	var out []missed
	for _, m := range ms {
		if !named[fmt.Sprintf("%s:%d", m.Path, m.Line)] {
			out = append(out, m)
		}
	}
	return out
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

	onTool := func(name, args, result string) {
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
	}

	var metrics *agent.Metrics
	runErr := renderAgentTap(ctx, a.Run(ctx, o.Issue), st, o.ShowReasoning, &runTap{
		Text:    io.MultiWriter(answer, &raw),
		OnTool:  onTool,
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

	res := checkAnswer(ctx, o.Root, raw.String(), searches, opened)
	res.Metrics, res.RunErr = metrics, runErr

	// The sweep's findings go back to the model once, before the user sees
	// them as a bare list. Measured 2026-09-24 on 10-locate-real-shape: in two
	// runs of three the answer named one place, and the sweep found the other
	// two - under ALSO MATCHING, unjudged, beside nothing that said which of
	// them had to change. The model has read the file and can judge that; the
	// sweep cannot. One turn, and only when there is something to judge.
	if len(res.Missed) > 0 && runErr == nil {
		if !o.Quiet {
			fmt.Fprintf(os.Stderr, "  %s\n", st.yellow(fmt.Sprintf(
				"↺ the sweep found %s the answer did not cite - asking once more",
				plural(len(res.Missed), "matching line", "matching lines"))))
		}
		a.History = a.Transcript()
		a.MaxTurns = recheckTurns
		var raw2 strings.Builder
		answer2 := newFenceStripper(o.Answer)
		var metrics2 *agent.Metrics
		err2 := renderAgentTap(ctx, a.Run(ctx, recheckPrompt(res.Missed)), st, o.ShowReasoning, &runTap{
			Text:    io.MultiWriter(answer2, &raw2),
			OnTool:  onTool,
			Metrics: &metrics2,
		})
		answer2.Close()
		// Only a complete second answer replaces the first. A recheck that
		// failed or dropped the format must not cost the user what they had.
		if err2 == nil && strings.TrimSpace(whereSection(raw2.String())) != "" {
			res2 := checkAnswer(ctx, o.Root, raw2.String(), searches, opened)
			res2.Metrics, res2.RunErr = metrics2, nil
			res = res2
		}
	}

	// A citation that is not in the tree is resolved rather than merely
	// announced - but resolving is not promoting. See the comment block above
	// resolveMissing: the one measured case was a DECOY whose base name
	// resolved cleanly, so swapping the path in would have endorsed the error
	// froe had just caught.
	res.Resolutions = resolveMissing(ctx, o.Root, res.Missing, searches)
	return res, nil
}
