package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/resolve"
)

// commitRolePreference tries the dedicated "commit" role first, then falls
// back. A commit message is a short, tightly shaped job - exactly what a small
// model is good at - and the main model would spend minutes of local inference
// on it.
//
// The dedicated role exists because "fast" alone is ambiguous here: two models
// share it at the same size_gb, and the tie falls to ID order, which picks the
// CPU-pinned one - 71.8s against 7.5s on the same diff with both cold,
// measured 2026-09-15.
var commitRolePreference = []string{"commit", "fast", "main", "heavy"}

// maxDiffShare is the fraction of the context window the diff may occupy,
// leaving room for the instructions and the reply.
const maxDiffShare = 2

const commitSystemPrompt = `You write git commit messages.

Reply with the commit message and NOTHING else. No preamble, no explanation,
no markdown fences, no surrounding quotes.

Format:
  - First line: imperative mood, under 72 characters, no trailing full stop.
  - Then a blank line and a short body ONLY if the change needs explaining.
  - Describe what the change does and why, not which files moved.`

// runCommit writes a commit message from the staged diff and commits it.
//
// This replaces the `marco do -y "git add -A, commit ..., push"` habit, and is
// deliberately NOT an agent loop. Three failures from a single 2026-09-15
// session drove the design, and each is structurally impossible here:
//
//   - marco always planned `git add .` first, so committing a subset was
//     impossible. Here staging is the caller's job and -a is an explicit
//     opt-in.
//   - marco invented a branch name on push, sending a branch to
//     refs/heads/new-branch. Here the branch is read from git and passed as
//     argv, so there is nothing for a model to invent.
//   - marco parsed its own prose into commands and tried to run them. Here the
//     model only ever produces MESSAGE TEXT. Every git invocation is fixed Go
//     code with a fixed argument list, never a shell string.
func runCommit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		all     = fs.Bool("a", false, "stage every change in the repo first, including new files")
		message = fs.String("m", "", "use this message instead of generating one")
		push    = fs.Bool("p", false, "push to origin after committing; with nothing to commit, just push")
		dryRun  = fs.Bool("n", false, "print the message that would be used and stop")
		model   = fs.String("model", "", "model id (default: best available, fast role first)")
		yes     = fs.Bool("y", false, "do not ask for confirmation")
		quiet   = fs.Bool("quiet", false, "suppress the model line")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe commit [flags]\n\n"+
			"Writes a commit message from the STAGED diff and commits it.\n"+
			"Stage with `git add` first, or pass -a to stage everything.\n"+
			"On a clean tree, -p pushes the commits already made.\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	st := newStyle(os.Stderr)

	if _, err := git(ctx, "rev-parse", "--git-dir"); err != nil {
		return errors.New("not a git repository")
	}

	if *all {
		if _, err := git(ctx, "add", "-A"); err != nil {
			return fmt.Errorf("staging changes: %w", err)
		}
	}

	diff, err := git(ctx, "diff", "--cached")
	if err != nil {
		return fmt.Errorf("reading the staged diff: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		// Distinguish "nothing to do" from "you forgot to stage", because the
		// fix is completely different.
		dirty, _ := git(ctx, "status", "--porcelain")
		if pushOnly(*push, diff, dirty) {
			// -p with nothing to commit means "push what is already
			// committed". A clean tree with unpushed commits is the ordinary
			// end of a session, and refusing it here sends the user to `git
			// push` - the exact habit this command exists to replace.
			if !*yes && !confirm(st, "Push "+currentBranch(ctx)+"?") {
				return errors.New("cancelled")
			}
			return pushCurrentBranch(ctx, st)
		}
		if strings.TrimSpace(dirty) == "" {
			return errors.New("nothing to commit - the working tree is clean" +
				" (pass -p to push commits that are already made)")
		}
		return errors.New("nothing staged - `git add` the files you want, or pass -a to stage everything")
	}

	stat, _ := git(ctx, "diff", "--cached", "--stat")

	msg := strings.TrimSpace(*message)
	if msg == "" {
		msg, err = generateCommitMessage(ctx, stat, diff, *model, st, *quiet)
		if err != nil {
			return err
		}
	}
	if msg == "" {
		return errors.New("the model returned an empty commit message")
	}

	fmt.Fprintln(os.Stderr, st.dim(strings.TrimRight(stat, "\n")))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, indent(msg))
	fmt.Fprintln(os.Stderr)

	if *dryRun {
		return nil
	}
	if !*yes && !confirm(st, "Commit this?") {
		return errors.New("cancelled")
	}

	if _, err := git(ctx, "commit", "-m", msg); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	head, _ := git(ctx, "rev-parse", "--short", "HEAD")
	fmt.Fprintf(os.Stderr, "%s %s\n", st.dim("committed"), strings.TrimSpace(head))

	if !*push {
		return nil
	}
	return pushCurrentBranch(ctx, st)
}

// pushOnly reports whether this invocation is "push what is already committed":
// -p given, nothing staged, and nothing waiting to be staged either.
//
// Both emptiness checks matter. A dirty tree with -p is far more likely to be
// someone who forgot to stage than someone who wants a bare push, and pushing
// silently past uncommitted work is not a guess worth making for them.
func pushOnly(push bool, staged, dirty string) bool {
	return push && strings.TrimSpace(staged) == "" && strings.TrimSpace(dirty) == ""
}

// currentBranch is the branch name for a prompt, empty if git cannot say.
func currentBranch(ctx context.Context) string {
	b, err := git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(b)
}

// pushCurrentBranch pushes HEAD's branch by name.
//
// The branch is read from git and passed as an argument. Nothing here is
// generated, which is the whole point: marco pushed a branch to
// refs/heads/new-branch because a 1.5B model was asked to produce the command.
func pushCurrentBranch(ctx context.Context, st style) error {
	branch, err := git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("finding the current branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" {
		return errors.New("detached HEAD - check out a branch before pushing")
	}

	argv := pushArgs(branch, hasUpstream(ctx))
	if _, err := git(ctx, argv...); err != nil {
		return fmt.Errorf("pushing %s: %w", branch, err)
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", st.dim("pushed"), branch)
	return nil
}

// hasUpstream reports whether the current branch already tracks a remote.
func hasUpstream(ctx context.Context) bool {
	_, err := git(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// pushArgs builds the push command. A branch with no upstream gets -u so the
// next push needs no arguments; an existing one is pushed by name rather than
// relying on push.default.
func pushArgs(branch string, upstream bool) []string {
	if upstream {
		return []string{"push", "origin", branch}
	}
	return []string{"push", "-u", "origin", branch}
}

// generateCommitMessage asks a model to describe the staged diff.
func generateCommitMessage(ctx context.Context, stat, diff, modelID string, st style, quiet bool) (string, error) {
	cat, err := registry.Load(config.Dir())
	if err != nil {
		return "", err
	}
	choice, err := resolve.PickWithPreference(ctx, cat, modelID, commitRolePreference)
	if err != nil {
		return "", err
	}
	p, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return "", err
	}

	window := effectiveContext(ctx, choice.Model, choice.Runtime, st, true)
	prompt := buildCommitPrompt(stat, diff, window/maxDiffShare)

	if !quiet {
		fmt.Fprintf(os.Stderr, "%s %s via %s\n",
			st.dim("→"), st.bold(choice.Model.ID), st.dim(choice.Runtime.Name))
	}

	req := provider.RequestFor(choice.Model, []provider.Message{
		{Role: provider.RoleSystem, Content: commitSystemPrompt},
		{Role: provider.RoleUser, Content: prompt},
	})
	req.MaxTokens = 512
	req.Temperature = 0.2

	events, err := p.Chat(ctx, req)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for ev := range events {
		switch ev.Kind {
		case provider.KindText:
			b.WriteString(ev.Text)
		case provider.KindError:
			return "", ev.Err
		}
	}
	return cleanCommitMessage(b.String()), nil
}

// buildCommitPrompt assembles what the model sees. The stat block always
// survives: when a diff is too large to show, knowing which files changed is
// far better than a truncated hunk from one of them.
func buildCommitPrompt(stat, diff string, budgetTokens int) string {
	maxChars := budgetTokens * 3
	if maxChars < 1000 {
		maxChars = 1000
	}
	truncated := false
	if len(diff) > maxChars {
		cut := strings.LastIndex(diff[:maxChars], "\n")
		if cut < maxChars/2 {
			cut = maxChars
		}
		diff = diff[:cut]
		truncated = true
	}

	var b strings.Builder
	b.WriteString("Files changed:\n")
	b.WriteString(strings.TrimRight(stat, "\n"))
	b.WriteString("\n\nStaged diff:\n")
	b.WriteString(diff)
	if truncated {
		b.WriteString("\n\n[diff truncated - describe the change from the file list above]")
	}
	return b.String()
}

// cleanCommitMessage strips the wrapping a model puts around an answer.
//
// Small models are especially prone to this: a fenced block, a "Here is the
// commit message:" preamble, or the whole thing in quotes. Committing any of
// that verbatim is worse than a bad message, because it is invisible until
// someone reads the log.
func cleanCommitMessage(s string) string {
	s = strings.TrimSpace(s)

	// Drop a leading preamble line ending in a colon, e.g. "Commit message:".
	if i := strings.IndexByte(s, '\n'); i > 0 {
		first := strings.TrimSpace(s[:i])
		if strings.HasSuffix(first, ":") && len(first) < 60 && !strings.HasPrefix(first, "```") {
			s = strings.TrimSpace(s[i+1:])
		}
	}

	// Unwrap a fenced block, keeping only what is inside it.
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}

	// Unwrap surrounding quotes, but only when they wrap the WHOLE message -
	// a subject that merely ends in a quoted word must survive intact.
	for _, q := range []string{`"`, "'", "`"} {
		if len(s) > 1 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) &&
			!strings.Contains(strings.TrimSuffix(strings.TrimPrefix(s, q), q), q) {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}

	// Collapse runs of blank lines and drop trailing whitespace per line.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// indent offsets a multi-line message for display.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// confirm asks a yes/no question. A non-interactive stdin is a "no": a commit
// that happens because nobody could be asked is exactly the surprise this
// command exists to avoid. -y is the way through in a script.
func confirm(st style, question string) bool {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, st.yellow("stdin is not a terminal - pass -y to commit without confirmation"))
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// git runs one git command with a fixed argument list. No shell, so nothing a
// model produces can ever be interpreted as a command.
func git(ctx context.Context, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", argv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return string(out), nil
}
