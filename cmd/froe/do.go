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
	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/resolve"
	"github.com/dcoldeira/froe/internal/tools"
)

// defaultSystem is deliberately explicit about brevity.
//
// On a local model at ~5 tok/s every generated token costs the user 0.2s, so a
// 450-token reply is ninety seconds of watching text appear. Measured on a
// bare greeting: generation accounted for 90 of 146 seconds. "Reply with a
// short summary" was already in this prompt and produced headers, bold text
// and bullet lists — vague instructions about length do not work, so this
// states the cost and gives concrete rules.
const defaultSystem = `You are froe, a coding agent working in a local project.

Work in small steps. Before changing code, look at it: use grep and glob to
locate things and read_file to see them. Prefer edit_file over write_file for
existing files. Do not guess at file contents.

BE BRIEF. You generate roughly five tokens per second, so every word costs the
user real time. Specifically:
- Answer in the fewest words that are correct and complete. One or two
  sentences is usually right.
- No preamble. Do not restate the question, do not say what you are about to
  do, do not offer a menu of things you could do next.
- Plain prose. No headers, no bold, no bullet lists unless the user asks for a
  list or the content genuinely is one.
- Do not summarise a file you just read unless asked. Say what matters for the
  task and stop.
- When the task is done, say so in one line and stop calling tools.`

func runDo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("do", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		model     = fs.String("model", "", "model id (default: best available by role)")
		root      = fs.String("root", ".", "project root; nothing outside it is reachable")
		maxTurns  = fs.Int("max-turns", agent.DefaultMaxTurns, "stop after this many turns")
		yolo      = fs.Bool("yolo", false, "approve every action except the hard denylist")
		accept    = fs.Bool("accept-edits", false, "auto-approve file edits, still prompt for shell commands")
		reasoning = fs.Bool("reasoning", false, "show the model's hidden reasoning")
		system    = fs.String("system", "", "replace the default system prompt")
		noContext = fs.Bool("no-context", false, "skip the project map and instructions")
		resume    = fs.Bool("resume", false, "continue the most recent session in this project")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe do [flags] <task>\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		fs.Usage()
		return errors.New("no task given")
	}

	absRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	if *root != "." {
		absRoot = *root
	}

	cat, err := registry.Load(config.Dir())
	if err != nil {
		return err
	}
	choice, err := resolve.Pick(ctx, cat, *model)
	if err != nil {
		return err
	}
	p, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return err
	}

	mode := perms.ModeAsk
	switch {
	case *yolo:
		mode = perms.ModeYolo
	case *accept:
		mode = perms.ModeAcceptEdits
	}

	sys := defaultSystem
	if *system != "" {
		sys = *system
	}

	st := newStyle(os.Stderr)

	store := openStore(true)
	if store != nil {
		defer store.Close()
	}

	projectRoot := repo.FindRoot(absRoot)
	sess, history, err := startSession(store, projectRoot, resumeArg(*resume), choice.Model.ID, task, st)
	if err != nil {
		return err
	}

	var projectCtx string
	if !*noContext {
		projectCtx = buildContextWithRuntime(ctx, absRoot, task, choice.Model, choice.Runtime, st, false)
	}
	if store != nil {
		projectCtx = withMemories(projectCtx, store, projectRoot)
	}

	reg := tools.NewRegistry()
	env := tools.Env{Root: absRoot, Project: projectRoot}
	if store != nil {
		env.Memory = store
	}
	a := &agent.Agent{
		Provider:            p,
		Model:               choice.Model,
		Tools:               reg,
		Gate:                perms.NewGate(mode),
		Env:                 env,
		History:             history,
		MaxToolResultTokens: effectiveContext(ctx, choice.Model, choice.Runtime, st, true) / 4,
		MaxTurns:            *maxTurns,
		System:              sys,
		Context:             projectCtx,
	}

	strategy := choice.Model.ToolStrategy
	if strategy == "" {
		strategy = registry.ToolReact
	}
	fmt.Fprintf(os.Stderr, "%s %s via %s %s\n",
		st.dim("→"), st.bold(choice.Model.ID), st.dim(choice.Runtime.Name),
		st.dim(fmt.Sprintf("(%s tools, %d available)", strategy, len(reg.Names()))))

	images, err := loadAttachedImages(task)
	if err != nil {
		return err
	}

	var metrics *agent.Metrics
	runErr := renderAgentCollect(ctx, a.Run(ctx, task, images...), st, *reasoning, &metrics)
	saveRun(store, sess, a, metrics, choice.Model.ID)
	return runErr
}

// resumeArg maps the boolean flag onto the shared session selector.
func resumeArg(resume bool) string {
	if resume {
		return "last"
	}
	return ""
}

// renderAgent draws the loop. Progress goes to stderr; only the final answer
// reaches stdout, so `froe do ... > summary.md` captures the result alone.
func renderAgent(ctx context.Context, events <-chan agent.Event, st style, showReasoning bool) error {
	return renderAgentCollect(ctx, events, st, showReasoning, nil)
}

// renderAgentCollect draws the loop and, when out is non-nil, hands back the
// run metrics so the caller can persist them.
func renderAgentCollect(ctx context.Context, events <-chan agent.Event, st style, showReasoning bool, out **agent.Metrics) error {
	return renderAgentTap(ctx, events, st, showReasoning, &runTap{Metrics: out})
}

// runTap lets a command watch a run it is also rendering. Every field is
// optional.
//
// It exists because `froe locate` needs two things the plain renderer cannot
// give it: the answer as a string (to check the places it cites actually
// exist, and to unwrap a fenced block before it reaches the user) and the
// searches that worked (to re-run them inside the files it found). Both are
// already flowing through this loop; a tap is cheaper than a second renderer
// that would drift from this one.
type runTap struct {
	// Text receives the model's prose instead of stdout when non-nil.
	Text io.Writer
	// OnTool is called once per completed tool call, with the arguments from
	// its start event paired to its result.
	OnTool func(name, args, result string)
	// Metrics, when non-nil, receives the run metrics.
	Metrics **agent.Metrics
}

func renderAgentTap(ctx context.Context, events <-chan agent.Event, st style, showReasoning bool, tap *runTap) error {
	if tap == nil {
		tap = &runTap{}
	}
	out := tap.Metrics
	textOut := tap.Text

	// The model's prose streams to stdout as it arrives; progress, tool calls
	// and metrics go to stderr. Reprinting the answer at KindDone would
	// duplicate it in every case, including `2>&1 | less`.
	var inText bool
	// The tool name and arguments arrive one event before the result they
	// belong to, so they are held here to be handed over as one call.
	var lastTool, lastArgs string

	// endText closes a streamed block wherever that text went. Printing it to
	// stdout unconditionally would leave stray blank lines there for a caller
	// that redirected the prose somewhere else.
	endText := func() {
		if !inText {
			return
		}
		inText = false
		if textOut != nil {
			io.WriteString(textOut, "\n")
			return
		}
		fmt.Println()
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, st.dim("\ncancelled"))
			return nil

		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.Kind {
			case agent.KindTurn:
				endText()
				fmt.Fprintf(os.Stderr, "%s\n", st.dim(fmt.Sprintf("── turn %d", ev.Turn)))

			case agent.KindText:
				inText = true
				if textOut != nil {
					io.WriteString(textOut, ev.Text)
				} else {
					fmt.Print(ev.Text)
				}

			case agent.KindReasoning:
				if showReasoning {
					fmt.Fprint(os.Stderr, st.dim(ev.Text))
				}

			case agent.KindToolStart:
				endText()
				lastTool, lastArgs = ev.Tool, ev.Args
				fmt.Fprintf(os.Stderr, "  %s %s %s\n",
					st.yellow("▸"), st.bold(ev.Tool), st.dim(truncate(ev.Args, 100)))

			case agent.KindToolResult:
				// The verify push arrives right after the model's answer text,
				// which may not end in a newline - close it so the push is not
				// read as part of the answer.
				if ev.Tool == "(verify)" {
					endText()
					fmt.Fprintf(os.Stderr, "  %s %s\n", st.yellow("↺ not checked:"), st.dim(truncate(ev.Result, 300)))
					continue
				}
				if ev.Tool == "(leftovers)" {
					endText()
					fmt.Fprintf(os.Stderr, "  %s %s\n", st.yellow("↺ still matching:"), st.dim(truncate(firstLines(ev.Result, 8), 600)))
					continue
				}
				if tap.OnTool != nil {
					name := ev.Tool
					if name == "" {
						name = lastTool
					}
					tap.OnTool(name, lastArgs, ev.Result)
				}
				fmt.Fprintf(os.Stderr, "    %s\n", st.dim(truncate(firstLines(ev.Result, 3), 300)))

			case agent.KindToolDenied:
				fmt.Fprintf(os.Stderr, "    %s\n", st.yellow("declined"))

			case agent.KindError:
				endText()
				if ev.Metrics != nil {
					if out != nil {
						*out = ev.Metrics
					}
					fmt.Fprintln(os.Stderr, agentMetrics(st, *ev.Metrics))
					// An aborted run keeps whatever it already wrote. Saying
					// only "stuck" reads as if the working tree is untouched.
					if line := changedFiles(st, *ev.Metrics, true); line != "" {
						fmt.Fprintln(os.Stderr, line)
					}
				}
				return ev.Err

			case agent.KindDone:
				endText()
				if ev.Metrics != nil {
					if out != nil {
						*out = ev.Metrics
					}
					fmt.Fprintln(os.Stderr, agentMetrics(st, *ev.Metrics))
					if line := changedFiles(st, *ev.Metrics, false); line != "" {
						fmt.Fprintln(os.Stderr, line)
					}
				}
				return nil
			}
		}
	}
}

// changedFiles names the files a run wrote. After an abort this is a warning,
// not a summary: the edits are partial, on disk, and nothing rolled them back.
func changedFiles(st style, m agent.Metrics, aborted bool) string {
	if len(m.FilesChanged) == 0 {
		return ""
	}
	noun := "file"
	if len(m.FilesChanged) != 1 {
		noun += "s"
	}
	list := "  " + strings.Join(m.FilesChanged, "\n  ")
	if !aborted {
		return st.dim(fmt.Sprintf("  changed %d %s:\n%s", len(m.FilesChanged), noun, list))
	}
	return st.yellow(fmt.Sprintf(
		"  the run did not finish, but %d %s already changed on disk - review before keeping:\n%s",
		len(m.FilesChanged), noun, list))
}

func agentMetrics(st style, m agent.Metrics) string {
	s := fmt.Sprintf("  %d turns · %d tool calls", m.Turns, m.ToolCalls)
	if m.ToolErrors > 0 {
		s += fmt.Sprintf(" (%d failed)", m.ToolErrors)
	}
	s += fmt.Sprintf(" · %d→%d tokens", m.PromptTokens, m.CompletionTokens)
	if m.ReasoningTokens > 0 {
		s += fmt.Sprintf(" (%d reasoning)", m.ReasoningTokens)
	}
	s += fmt.Sprintf(" · %.1fs", m.Elapsed.Seconds())
	return st.dim(s)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf(" … (+%d lines)", len(lines)-n)
}
