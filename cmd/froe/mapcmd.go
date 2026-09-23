package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dcoldeira/froe/internal/repo"
)

// runMap prints the repo map the agent would see. It exists so the context a
// model receives is inspectable rather than a black box — if the agent is
// guessing at paths, this is where you look first.
func runMap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		root   = fs.String("root", ".", "project root")
		budget = fs.Int("budget", 6000, "token budget for the rendered map")
		full   = fs.Bool("full", false, "ignore the budget and print everything")
		task   = fs.String("task", "", "rank files by relevance to this task")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe map [flags]\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	dir := repo.FindRoot(*root)
	m, err := repo.Build(ctx, dir)
	if err != nil {
		return err
	}

	b := *budget
	if *full {
		b = 1 << 30
	}
	var rendered string
	if *task != "" {
		rendered = repo.RenderRanked(m, repo.Rank(ctx, m, *task), b)
	} else {
		rendered = m.Render(b)
	}
	fmt.Print(rendered)

	st := newStyle(os.Stderr)
	files, symbols := m.Stats()
	fmt.Fprintf(os.Stderr, "\n%s\n", st.dim(fmt.Sprintf(
		"  root %s · %d files · %d symbols · %d skipped · ~%d tokens rendered",
		dir, files, symbols, m.Skipped, repo.EstimateTokens(rendered))))

	if ins, err := repo.LoadInstructions(dir, *root); err == nil && len(ins.Sources) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", st.dim(fmt.Sprintf(
			"  instructions: %v (~%d tokens)", ins.Sources, repo.EstimateTokens(ins.Text))))
	}
	return nil
}
