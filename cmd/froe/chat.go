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

	// No pinned model means each task is routed, as in Neovim.
	m := &chatModels{cat: cat, routing: *model == "", choice: choice}
	if m.routing {
		fmt.Fprintf(os.Stderr, "%s %s %s\n",
			style.dim("→"), style.bold("auto"),
			style.dim(fmt.Sprintf("· each task goes to a quick or careful model · %d tools · %s", len(reg.Names()), shortID(sess))))
	} else {
		fmt.Fprintf(os.Stderr, "%s %s via %s %s\n",
			style.dim("→"), style.bold(choice.Model.ID), style.dim(choice.Runtime.Name),
			style.dim(fmt.Sprintf("· %d tools · %s", len(reg.Names()), shortID(sess))))
	}
	fmt.Fprintln(os.Stderr, style.dim("  /help for commands, /model to pin or route, Ctrl-D to exit"))

	return chatLoop(ctx, a, st, sess, root, cwd, m, *reasoning, *noContext, style)
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

// chatModels is which model the chat uses: routed per task, or pinned.
type chatModels struct {
	cat     *registry.Catalogue
	routing bool
	choice  resolve.Choice
}

// chatLoop reads tasks and runs the agent until EOF.
func chatLoop(ctx context.Context, a *agent.Agent, st *session.Store, sess *session.Session,
	root, cwd string, m *chatModels, reasoning, noContext bool, style style) error {

	scr, err := newScreen(style)
	if err != nil {
		return err
	}
	defer func() {
		scr.close()
		fmt.Fprintln(os.Stderr, style.dim("bye"))
	}()
	if g, ok := a.Gate.(*perms.Gate); ok {
		g.Prompt = scr.ask
	}

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
		a.Context = buildContextWithRuntime(ctx, cwd, "", m.choice.Model, m.choice.Runtime, style, false)
		if st != nil {
			a.Context = withMemories(a.Context, st, root)
		}
	}

	for {
		line, err := scr.next()
		if err != nil {
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if cmd, rest, _ := strings.Cut(line, " "); cmd == "/model" {
			modelCommand(ctx, m, strings.TrimSpace(rest), style)
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := handleSlash(line, a, st, sess, root, style); quit {
				return nil
			}
			continue
		}

		images, err := loadAttachedImages(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
			continue
		}

		// Ctrl+C in the box stops this run, not the chat - including a model
		// load that routing started.
		runCtx, cancel := context.WithCancel(ctx)
		scr.startRun(cancel)

		class, choice, err := settleModel(runCtx, m.cat, m.choice, m.routing, line, false,
			func(s string) { fmt.Fprintf(os.Stderr, "  %s\n", style.dim("↪ "+s)) })
		if err == nil && choice.Model.ID != a.Model.ID {
			var prov provider.Provider
			if prov, err = provider.New(choice.Runtime, choice.Model); err == nil {
				a.Provider, a.Model = prov, choice.Model
				a.MaxToolResultTokens = effectiveContext(runCtx, choice.Model, choice.Runtime, style, true) / 4
				// The map was budgeted for the previous model's window.
				a.Context = ""
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
			scr.endRun()
			cancel()
			continue
		}
		m.choice = choice
		a.RequireEvidence = class == resolve.RouteCareful

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
			a.Context = buildContextWithRuntime(ctx, cwd, line, m.choice.Model, m.choice.Runtime, style, false)
			if st != nil {
				a.Context = withMemories(a.Context, st, root)
			}
		}

		if sess != nil && sess.Title == "" {
			sess.Title = line
			_ = st.SetTitle(sess.ID, line)
		}

		var metrics *agent.Metrics
		if err := renderAgentCollect(runCtx, a.Run(runCtx, line, images...), style, reasoning, &metrics); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
		}
		scr.endRun()
		cancel()
		saveRun(st, sess, a, metrics, a.Model.ID)

		// Carry the exchange forward so the next turn has context.
		a.History = trimHistory(append(a.History, a.Transcript()...), maxHistoryChars)
	}
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
  /model [id|auto]   show the model, pin one, or route each task (auto)
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

// modelCommand shows, pins or unpins the chat's model - the terminal's
// counterpart to the Neovim model picker. A pinned model takes effect on the
// next task, which loads it if it is not the loaded one.
func modelCommand(ctx context.Context, m *chatModels, arg string, style style) {
	switch arg {
	case "":
		if m.routing {
			fmt.Fprintln(os.Stderr, style.dim("  auto: each task goes to a quick or careful model (last: "+m.choice.Model.ID+")"))
		} else {
			fmt.Fprintln(os.Stderr, style.dim("  pinned to "+m.choice.Model.ID+" · /model auto to route per task"))
		}
	case "auto":
		m.routing = true
		fmt.Fprintln(os.Stderr, style.dim("  routing each task"))
	default:
		c, err := resolve.Pick(ctx, m.cat, arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", style.yellow("error:"), err)
			return
		}
		m.choice, m.routing = c, false
		fmt.Fprintln(os.Stderr, style.dim("  pinned to "+c.Model.ID))
	}
}
