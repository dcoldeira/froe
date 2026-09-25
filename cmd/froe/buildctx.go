package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dcoldeira/froe/internal/probe"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/repo"
)

// buildContext assembles the project context for a run: instructions found on
// disk, plus a repo map ranked against the task.
//
// It is built once and placed in the system message so it is prefilled once,
// not re-sent every turn — which on CPU-bound hardware is the difference
// between a usable loop and minutes of silence per iteration (D4).
func buildContext(ctx context.Context, dir string, task string, ctxMax int, st style, quiet bool) string {
	return buildContextFor(ctx, dir, task, ctxMax, st, quiet)
}

// effectiveContext is the window the model is ACTUALLY loaded with.
//
// The registry records what a model supports — Bonsai advertises 262144 — while
// the runtime decides what it loads, and LM Studio's default is 8192. Every
// budget in the program must use this number, not the registry's: using the
// theoretical figure for the tool-result cap let a 234 KB file through and
// overflowed the window at 22787 tokens even after the cap existed.
func effectiveContext(ctx context.Context, m registry.Model, rt registry.Runtime, st style, quiet bool) int {
	effective := m.CtxMax
	// ServeID, not ID: the runtime knows the model by its own name.
	n := probe.ContextSize(ctx, rt, m.ServeID())

	switch {
	case n > 0 && (effective <= 0 || n < effective):
		if !quiet && n < m.CtxMax {
			fmt.Fprintf(os.Stderr, "%s\n", st.dim(fmt.Sprintf(
				"  context: model supports %d but is loaded with %d - budgeting for %d",
				m.CtxMax, n, n)))
		}
		effective = n

	case n == 0 && isLocalRuntime(rt) && effective > conservativeContext:
		// A local runtime OWNS its loaded window, so silence means unknown,
		// never "the registry maximum". Assuming the maximum sets
		// clampResult's cap and fitContext's budget far above the real
		// window and disables both at once - measured on a real-issue run
		// where an expired LM Studio TTL meant nothing was loaded at probe
		// time and a 48 KB read killed the run at 28709 tokens against a
		// JIT-loaded 8192.
		if !quiet {
			fmt.Fprintf(os.Stderr, "%s\n", st.yellow(fmt.Sprintf(
				"  context: %s would not say what %s is loaded with - budgeting for %d, not the registry's %d. "+
					"Bring the runtime up first (froe-up) for its real window.",
				rt.Name, m.ID, conservativeContext, m.CtxMax)))
		}
		effective = conservativeContext
	}

	if effective <= 0 {
		effective = conservativeContext
	}
	return effective
}

// conservativeContext is the window assumed for a local runtime that will not
// report one. It matches llama.cpp's and LM Studio's own default load size, so
// it is the least-bad guess when the backend is silent.
const conservativeContext = 8192

// isLocalRuntime reports whether the runtime decides its own loaded context.
// Hosted backends authenticate with a key and expose no loaded-context
// endpoint, so their registry figure is authoritative and must not be clamped.
func isLocalRuntime(rt registry.Runtime) bool { return !rt.Hosted() }

// buildContextWithRuntime builds the project context against the real window.
func buildContextWithRuntime(ctx context.Context, dir, task string, m registry.Model,
	rt registry.Runtime, st style, quiet bool) string {
	return buildContextFor(ctx, dir, task, effectiveContext(ctx, m, rt, st, quiet), st, quiet)
}

func buildContextFor(ctx context.Context, dir string, task string, ctxMax int, st style, quiet bool) string {
	root := repo.FindRoot(dir)
	budget := repo.DefaultBudget(ctxMax)
	allowance := budget.MapAllowance()

	var parts []string
	var notes []string

	if ins, err := repo.LoadInstructions(root, dir); err == nil && ins.Text != "" {
		text, dropped := ins.Fit(budget.InstructionAllowance())
		if text != "" {
			parts = append(parts, "Project instructions:\n\n"+text)
			note := fmt.Sprintf("instructions %v (~%d tok", ins.Sources, repo.EstimateTokens(text))
			if dropped {
				note += fmt.Sprintf(", truncated from ~%d", repo.EstimateTokens(ins.Text))
			}
			notes = append(notes, note+")")
		}
	}

	if allowance > 0 {
		m, err := repo.Build(ctx, root)
		if err == nil && len(m.Files) > 0 {
			rendered := repo.RenderRanked(m, repo.Rank(ctx, m, task), allowance)
			parts = append(parts, rendered)
			files, symbols := m.Stats()
			notes = append(notes, fmt.Sprintf("map %d files / %d symbols → ~%d tok of %d",
				files, symbols, repo.EstimateTokens(rendered), allowance))
		}
	} else {
		notes = append(notes, "map skipped: context window too small to be worth it")
	}

	if !quiet && len(notes) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", st.dim("  context: "+strings.Join(notes, " · ")))
	}
	return strings.Join(parts, "\n\n")
}
