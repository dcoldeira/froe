package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dcoldeira/froe/internal/agent"
)

// locateMaxTurns is deliberately far below DefaultMaxTurns.
//
// Measured on a real production repo 2026-09-15 across three runs of one issue: the model found
// the right file from a WRONG path in the issue every single time, and did it
// by turn 4. Everything after that was decay — on the third run it spent turns
// 12-23 re-searching for the path that does not exist, two turns after having
// already edited the real file. Locating is turn 1-4 work; a budget that allows
// turn 20 only buys the decay. Measured again 2026-09-15 with this command:
// ONE grep on turn 4 returned all three edit sites and the lookalike - the
// complete answer - and turns 5-8 were re-runs of searches already done.
const locateMaxTurns = 6

const locateSystem = `You are froe. You find WHERE in a codebase something lives. You never change anything.

You have read-only tools: grep, glob, read_file, git_log, git_blame. There is no
edit tool. Do not offer to make the change.

Method:
- READ THE EVIDENCE BLOCK FIRST. froe has already searched this project for
  the terms the report quotes, and every line in it came from a real file. Start
  from the files it names. Do NOT glob for the filename the report gives, and do
  not re-run a search the evidence has already answered.
- The path in the report is often WRONG or out of date, and the evidence block
  is how you find the real one. If you do need to search, search for the THING
  — a distinctive string, a symbol, a label — never for the stated filename.
- Once you have a candidate, read enough around it to be sure, and to see what
  else would have to change with it.
- A term almost never appears once. The moment you know WHICH file it is, grep
  again with "path" set to THAT FILE, so you see every line in it that mentions
  the thing. List them all. One site out of three is a wrong answer, and a
  lookalike you never looked at cannot be warned about.
- Watch for lookalikes: a similarly named field in a different place is a
  different thing. Say so explicitly when you find one.

Answer in exactly this shape and nothing else:

WHERE
  <path>:<line>  <symbol or short description>
  ... one line per place that is involved

WHAT IT IS
  One or two sentences on what this code does and how the places above relate.

WATCH OUT
  Anything that looks relevant but is NOT, or a coupling that would break if
  only some of the places were changed. Write "nothing" if there is none.

CITE ONLY WHAT YOU HAVE SEEN. Every path and line number in "WHERE" must have
appeared in a tool result in this session. Never repeat a path from the report
unless a tool confirmed it exists, and never invent a line number. If the path
the report gives does not exist, say so in WATCH OUT and give the real one.

STOP AS SOON AS "WHERE" IS COMPLETE. Two searches is usually the whole job: one
across the project to find the file, one inside that file to find every place in
it. Do not re-run a search you have already run, and do not keep looking for
more confirmation once you have the places - answer with what you have.

Do not write a plan. Do not suggest edits. Do not add headings beyond those
three. Be brief: you generate about five tokens per second, so every extra word
costs the user real time.`

// runLocate answers "where in the code is this?" and stops.
//
// This is the first of the stepwise commands, and it exists because of what was
// measured rather than what was hoped for. A 27B model at an 8192-token context
// cannot hold a multi-site task across twenty turns — it loses track and starts
// again. It CAN find the right code, reliably, in the first few turns. So this
// command does only that, with a fresh context every time, and hands control
// back to the user instead of pressing on.
//
// Read-only is structural: the agent gets tools.NewReadOnlyRegistry(), which
// contains no edit_file, no write_file and no bash. Nothing in a prompt can
// make it change a file.
func runLocate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("locate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		model     = fs.String("model", "", "model id (default: best available by role)")
		root      = fs.String("root", ".", "project root; nothing outside it is reachable")
		maxTurns  = fs.Int("max-turns", locateMaxTurns, "stop after this many turns")
		reasoning = fs.Bool("reasoning", false, "show the model's hidden reasoning")
		quiet     = fs.Bool("quiet", false, "suppress the model and metrics lines")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe locate [flags] <issue text>\n\n"+
			"Finds WHERE in the code something lives. Read-only: it cannot change a file.\n"+
			"Paste the issue text, or pipe it in.\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	issue := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if issue == "" {
		// Allow piping a whole issue body: `gh issue view 664 | froe locate`
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
			b, _ := io.ReadAll(os.Stdin)
			issue = strings.TrimSpace(string(b))
		}
	}
	if issue == "" {
		fs.Usage()
		return errors.New("no issue text given")
	}

	absRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	if *root != "." {
		absRoot = *root
	}

	st := newStyle(os.Stderr)
	res, err := locate(ctx, locateOpts{
		Root:          absRoot,
		Issue:         issue,
		Model:         *model,
		MaxTurns:      *maxTurns,
		Answer:        os.Stdout,
		Style:         st,
		Quiet:         *quiet,
		ShowReasoning: *reasoning,
	})
	if err != nil {
		return err
	}

	fmt.Print(renderMissed(res.Missed))
	fmt.Print(renderSurroundings(res.Surrounding))

	if len(res.Resolutions) > 0 {
		fmt.Print(renderResolutions(res.Resolutions))
		fmt.Fprintf(os.Stderr, "%s %s withdrawn - see NOT IN THIS TREE\n",
			st.yellow("!"), plural(len(res.Resolutions), "citation", "citations"))
	}

	// Running out of turns is not the same as failing. finalAnswer has already
	// run by the time that error arrives, so the answer usually comes back with
	// it - and if the places it names really exist, the user got what they
	// asked for. Exiting 1 on a good answer teaches them to ignore the exit
	// code, which is worse than losing the distinction.
	var overrun *agent.MaxTurnsError
	if errors.As(res.RunErr, &overrun) && len(res.Found) > 0 {
		fmt.Fprintln(os.Stderr, st.dim(fmt.Sprintf(
			"→ answered on the last turn: the %d-turn budget ran out, so it may have stopped short",
			overrun.Turns)))
		return nil
	}
	return res.RunErr
}
