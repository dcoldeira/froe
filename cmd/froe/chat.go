package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dcoldeira/froe/internal/agent"
	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/resolve"
	"github.com/dcoldeira/froe/internal/session"
	"github.com/dcoldeira/froe/internal/tools"
	"golang.org/x/term"
)

// maxHistoryChars bounds replayed conversation. Roughly 6k tokens at the ~3.2
// chars/token that code tokenises at, which leaves room on a 32K local model
// for the repo map, the tools and the turn itself.
const maxHistoryChars = 20000

func runChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		model     = fs.String("model", "", "model id (default: best available by role)")
		resume    = fs.String("resume", "", "resume a session id, or 'last' for the most recent in this project")
		yolo      = fs.Bool("yolo", false, "approve every action except the hard denylist")
		accept    = fs.Bool("accept-edits", false, "auto-approve file edits, still prompt for shell commands")
		reasoning = fs.Bool("reasoning", false, "show the model's hidden reasoning")
		noContext = fs.Bool("no-context", false, "skip the project map and instructions")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: froe chat [flags]\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("chat needs a terminal; use `froe do` for scripted runs")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := repo.FindRoot(cwd)

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

	st := openStore(false)
	if st != nil {
		defer st.Close()
	}

	style := newStyle(os.Stderr)
	sess, history, err := startSession(st, root, *resume, choice.Model.ID, "", style)
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

	reg := tools.NewRegistry()
	env := tools.Env{Root: cwd, Project: root}
	if st != nil {
		env.Memory = st
	}

	a := &agent.Agent{
		Provider:            p,
		Model:               choice.Model,
		Tools:               reg,
		Gate:                perms.NewGate(mode),
		Env:                 env,
		System:              defaultSystem,
		History:             history,
		MaxToolResultTokens: effectiveContext(ctx, choice.Model, choice.Runtime, style, true) / 4,
	}

	fmt.Fprintf(os.Stderr, "%s %s via %s %s\n",
		style.dim("→"), style.bold(choice.Model.ID), style.dim(choice.Runtime.Name),
		style.dim(fmt.Sprintf("· %d tools · %s", len(reg.Names()), shortID(sess))))
	fmt.Fprintln(os.Stderr, style.dim("  /help for commands, Ctrl-D to exit"))

	return chatLoop(ctx, a, st, sess, root, cwd, choice.Model, choice.Runtime, *reasoning, *noContext, style)
}

// startSession resumes an existing conversation or begins a new one.
func startSession(st *session.Store, root, resume, model, title string, style style) (*session.Session, []provider.Message, error) {
	if st == nil {
		return nil, nil, nil
	}
	if resume == "" {
		s, err := st.NewSession(root, title, model)
		return s, nil, err
	}

	var s *session.Session
	var err error
	if resume == "last" {
		s, err = st.Latest(root)
	} else {
		s, err = st.Get(resume)
	}
	if err != nil {
		return nil, nil, err
	}
	if s == nil {
		return nil, nil, fmt.Errorf("no session to resume (try `froe sessions`)")
	}

	stored, err := st.Messages(s.ID)
	if err != nil {
		return nil, nil, err
	}
	history := trimHistory(fromStored(stored), maxHistoryChars)
	fmt.Fprintf(os.Stderr, "%s\n", style.dim(fmt.Sprintf(
		"  resumed %s — %d earlier messages, %d replayed", s.ID, len(stored), len(history))))
	return s, history, nil
}

// chatLoop reads tasks and runs the agent until EOF.
func chatLoop(ctx context.Context, a *agent.Agent, st *session.Store, sess *session.Session,
	root, cwd string, model registry.Model, runtime registry.Runtime,
	reasoning, noContext bool, style style) error {

	// Build the project context up front so the first turn does not also pay
	// for building it.
	//
	// Cache PRIMING was tried here and removed: sending a throwaway request to
	// warm the backend's KV cache measured 66s against 75s — inside the noise —
	// because the prefill it is meant to hide takes ~38s, far longer than
	// anyone spends typing. With --parallel 1 it is actively harmful, since the
	// prime occupies the only slot and the real request queues behind it.
	//
	// The first turn's cost is prefill of ~4000 tokens. The only real lever on
	// that is a smaller prompt, not a cleverer schedule.
	if !noContext {
		a.Context = buildContextWithRuntime(ctx, cwd, "", model, runtime, style, false)
		if st != nil {
			a.Context = withMemories(a.Context, st, root)
		}
	}

	input := newLineReader("› ")

	for {
		line, err := input.read()
		if err != nil {
			fmt.Fprintln(os.Stderr, style.dim("\nbye"))
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := handleSlash(line, a, st, sess, root, style); quit {
				return nil
			}
			continue
		}

		// Build the project context ONCE and reuse it verbatim.
		//
		// Rebuilding per turn re-ranks the map against the new task, which
		// changes the system prompt and destroys the backend's prompt cache.
		// Measured on Bonsai: an identical prefix prefills in 1.2s where a
		// changed one takes 11.3s — a 9.4x penalty paid on EVERY turn, which
		// dwarfs any relevance a fresh ranking would buy. /refresh rebuilds it
		// deliberately when the project has actually changed.
		// /refresh clears the context; rebuild it ranked against this message.
		if a.Context == "" && !noContext {
			a.Context = buildContextWithRuntime(ctx, cwd, line, model, runtime, style, false)
			if st != nil {
				a.Context = withMemories(a.Context, st, root)
			}
		}

		if sess != nil && sess.Title == "" {
			sess.Title = line
			_ = st.SetTitle(sess.ID, line)
		}

		images, err := loadAttachedImages(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
			continue
		}

		var metrics *agent.Metrics
		if err := renderAgentCollect(ctx, a.Run(ctx, line, images...), style, reasoning, &metrics); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
		}
		saveRun(st, sess, a, metrics, a.Model.ID)

		// Carry the exchange forward so the next turn has context.
		a.History = trimHistory(append(a.History, a.Transcript()...), maxHistoryChars)
	}
}

// lineReader reads prompts with editing and history.
//
// The terminal is created ONCE and reused. A fresh term.Terminal per line
// discards whatever it had already buffered — which silently swallowed every
// command after the first when input was piped — and loses history, so arrow-up
// would do nothing. Raw mode is toggled around each read so that streamed model
// output in between keeps ordinary newline handling.
type lineReader struct {
	fd     int
	term   *term.Terminal
	prompt string
}

func newLineReader(prompt string) *lineReader {
	fd := int(os.Stdin.Fd())
	return &lineReader{fd: fd, term: term.NewTerminal(os.Stdin, prompt), prompt: prompt}
}

func (l *lineReader) read() (string, error) {
	old, err := term.MakeRaw(l.fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(l.fd, old)
	return l.term.ReadLine()
}

func handleSlash(line string, a *agent.Agent, st *session.Store, sess *session.Session, root string, style style) bool {
	cmd, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "help", "?":
		fmt.Fprintln(os.Stderr, style.dim(`  /remember <fact>   save a durable project fact
  /memories          list what is remembered
  /forget <id>       delete a memory
  /clear             forget this conversation (memories are kept)
  /refresh           rebuild the project map (do this after big changes)
  /session           show the current session id
  /exit              leave`))
	case "exit", "quit", "q":
		return true
	case "clear":
		a.History = nil
		fmt.Fprintln(os.Stderr, style.dim("  conversation cleared"))
	case "refresh":
		// Dropping the cached context forces a rebuild on the next turn.
		a.Context = ""
		fmt.Fprintln(os.Stderr, style.dim("  project context will be rebuilt on the next message"))
	case "session":
		fmt.Fprintln(os.Stderr, style.dim("  "+shortID(sess)))
	case "remember":
		if st == nil || rest == "" {
			fmt.Fprintln(os.Stderr, style.yellow("  usage: /remember <fact>"))
			break
		}
		if err := st.Remember(root, rest, "user"); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, style.dim("  remembered"))
		}
	case "memories":
		printMemories(st, root, style)
	case "forget":
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || st == nil {
			fmt.Fprintln(os.Stderr, style.yellow("  usage: /forget <id>"))
			break
		}
		if err := st.Forget(id); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, style.dim("  forgotten"))
		}
	default:
		fmt.Fprintf(os.Stderr, "%s\n", style.yellow("  unknown command "+cmd+" — /help"))
	}
	return false
}

func printMemories(st *session.Store, root string, style style) {
	if st == nil {
		return
	}
	mems, err := st.Memories(root, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
		return
	}
	if len(mems) == 0 {
		fmt.Fprintln(os.Stderr, style.dim("  nothing remembered for this project yet"))
		return
	}
	for _, m := range mems {
		fmt.Fprintf(os.Stderr, "  %s %s %s\n",
			style.dim(fmt.Sprintf("%3d", m.ID)), m.Text, style.dim("("+m.Source+")"))
	}
}

// withMemories appends remembered facts to the project context.
func withMemories(base string, st *session.Store, root string) string {
	mems, err := st.Memories(root, 0)
	if err != nil || len(mems) == 0 {
		return base
	}
	var b strings.Builder
	if base != "" {
		b.WriteString(base)
		b.WriteString("\n\n")
	}
	b.WriteString("Remembered about this project:\n")
	for _, m := range mems {
		fmt.Fprintf(&b, "- %s\n", m.Text)
	}
	return b.String()
}

func shortID(s *session.Session) string {
	if s == nil {
		return "no session (memory disabled)"
	}
	return "session " + s.ID
}
