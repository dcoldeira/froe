package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dcoldeira/froe/internal/agent"
	"github.com/dcoldeira/froe/internal/config"
	"github.com/dcoldeira/froe/internal/perms"
	"github.com/dcoldeira/froe/internal/provider"
	"github.com/dcoldeira/froe/internal/registry"
	"github.com/dcoldeira/froe/internal/repo"
	"github.com/dcoldeira/froe/internal/resolve"
	"github.com/dcoldeira/froe/internal/rpc"
	"github.com/dcoldeira/froe/internal/session"
	"github.com/dcoldeira/froe/internal/tools"
)

// rpcHandler owns the agent state behind the protocol.
//
// All the logic lives here, in Go. The editor plugin is a transport and a
// renderer — the moment it starts making decisions, the two front ends have
// begun to diverge (docs/ARCHITECTURE.md §8).
type rpcHandler struct {
	root string
	cat  *registry.Catalogue
	// routing is on when initialize pinned no model: each Run then routes its
	// task to a quick or careful model (resolve.Route).
	routing  bool
	choice   resolve.Choice
	provider provider.Provider
	store    *session.Store
	sess     *session.Session
	tools    *tools.Registry
	history  []provider.Message
	asker    perms.Asker
}

func runRPC(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	h := &rpcHandler{tools: tools.NewRegistry()}
	// stdout is the protocol channel and must carry nothing else. Anything
	// printed there by accident corrupts the stream.
	srv := rpc.NewServer(os.Stdin, os.Stdout, h)
	h.asker = srv

	defer func() {
		if h.store != nil {
			h.store.Close()
		}
	}()
	return srv.Serve(ctx)
}

func (h *rpcHandler) Initialize(ctx context.Context, p rpc.InitializeParams) (*rpc.InitializeResult, error) {
	root := p.Root
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root = cwd
	}
	h.root = repo.FindRoot(root)

	cat, err := registry.Load(config.Dir())
	if err != nil {
		return nil, err
	}
	choice, err := resolve.Pick(ctx, cat, p.Model)
	if err != nil {
		return nil, err
	}
	prov, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return nil, err
	}
	h.choice, h.provider = choice, prov
	h.cat, h.routing = cat, p.Model == ""

	h.store = openStore(true)
	sessionID := ""
	if h.store != nil {
		if s, err := h.store.NewSession(h.root, "", choice.Model.ID); err == nil {
			h.sess = s
			sessionID = s.ID
		}
	}

	strategy := string(choice.Model.ToolStrategy)
	if strategy == "" {
		strategy = string(registry.ToolReact)
	}
	return &rpc.InitializeResult{
		Version:  rpc.Version,
		Model:    choice.Model.ID,
		Runtime:  choice.Runtime.Name,
		Strategy: strategy,
		Tools:    h.tools.Names(),
		Root:     h.root,
		Session:  sessionID,
		Routing:  h.routing,
	}, nil
}

func (h *rpcHandler) Run(ctx context.Context, p rpc.RunParams, emit func(rpc.EventParams)) (*rpc.RunResult, error) {
	if h.provider == nil {
		return nil, errors.New("initialize has not been called")
	}
	task := strings.TrimSpace(p.Task)
	if task == "" {
		return nil, errors.New("empty task")
	}

	class, err := h.route(ctx, task, p.Selection != "", emit)
	if err != nil {
		return nil, err
	}

	task = withEditorContext(task, p)

	if p.Resume && h.store != nil && len(h.history) == 0 {
		if s, err := h.store.Latest(h.root); err == nil && s != nil {
			if stored, err := h.store.Messages(s.ID); err == nil {
				h.history = trimHistory(fromStored(stored), maxHistoryChars)
				h.sess = s
			}
		}
	}

	projectCtx := buildContextWithRuntime(ctx, h.root, task, h.choice.Model, h.choice.Runtime, style{}, true)
	if h.store != nil {
		projectCtx = withMemories(projectCtx, h.store, h.root)
	}

	env := tools.Env{Root: h.root, Project: h.root}
	if h.store != nil {
		env.Memory = h.store
	}

	gate := h.asker
	switch p.Mode {
	case "yolo":
		gate = alwaysAllow{}
	case "accept-edits":
		gate = acceptEdits{inner: h.asker}
	}

	a := &agent.Agent{
		Provider:            h.provider,
		Model:               h.choice.Model,
		Tools:               h.tools,
		Gate:                gate,
		Env:                 env,
		System:              defaultSystem,
		Context:             projectCtx,
		History:             h.history,
		MaxToolResultTokens: effectiveContext(ctx, h.choice.Model, h.choice.Runtime, style{}, true) / 4,
		RequireEvidence:     class == resolve.RouteCareful,
	}

	var (
		answer  strings.Builder
		metrics *agent.Metrics
		runErr  error
	)
	for ev := range a.Run(ctx, task) {
		switch ev.Kind {
		case agent.KindTurn:
			emit(rpc.EventParams{Kind: "turn", Turn: ev.Turn})
		case agent.KindText:
			answer.WriteString(ev.Text)
			emit(rpc.EventParams{Kind: "text", Text: ev.Text})
		case agent.KindReasoning:
			emit(rpc.EventParams{Kind: "reasoning", Text: ev.Text})
		case agent.KindToolStart:
			emit(rpc.EventParams{Kind: "tool", Tool: ev.Tool, Args: ev.Args})
		case agent.KindToolResult:
			emit(rpc.EventParams{Kind: "tool_result", Tool: ev.Tool, Result: ev.Result})
		case agent.KindToolDenied:
			emit(rpc.EventParams{Kind: "denied", Tool: ev.Tool})
		case agent.KindError:
			runErr = ev.Err
			metrics = ev.Metrics
			emit(rpc.EventParams{Kind: "error", Text: ev.Err.Error()})
		case agent.KindDone:
			metrics = ev.Metrics
		}
	}

	saveRun(h.store, h.sess, a, metrics, h.choice.Model.ID)
	h.history = trimHistory(append(h.history, a.Transcript()...), maxHistoryChars)

	if runErr != nil {
		return nil, runErr
	}
	res := &rpc.RunResult{Answer: strings.TrimSpace(answer.String())}
	if metrics != nil {
		res.Turns = metrics.Turns
		res.ToolCalls = metrics.ToolCalls
		res.ToolErrors = metrics.ToolErrors
		res.Tokens = metrics.PromptTokens + metrics.CompletionTokens
		res.ElapsedMS = metrics.Elapsed.Milliseconds()
	}
	return res, nil
}

// route settles the model for one task (settleModel) and swaps the provider
// when the model changed.
func (h *rpcHandler) route(ctx context.Context, task string, hasSelection bool, emit func(rpc.EventParams)) (string, error) {
	class, choice, err := settleModel(ctx, h.cat, h.choice, h.routing, task, hasSelection,
		func(s string) { emit(rpc.EventParams{Kind: "route", Text: s}) })
	if err != nil {
		return "", err
	}
	if choice.Model.ID == h.choice.Model.ID {
		return class, nil
	}
	prov, err := provider.New(choice.Runtime, choice.Model)
	if err != nil {
		return "", err
	}
	h.choice, h.provider = choice, prov
	return class, nil
}

// alwaysAllow approves everything the hard denylist has not already refused.
type alwaysAllow struct{}

func (alwaysAllow) Ask(perms.Request) perms.Decision { return perms.Allow }

// acceptEdits approves file edits but still asks about shell commands, which
// are unbounded in a way an edit is not.
type acceptEdits struct{ inner perms.Asker }

func (a acceptEdits) Ask(req perms.Request) perms.Decision {
	if req.Tool == "bash" {
		return a.inner.Ask(req)
	}
	return perms.Allow
}

// withEditorContext folds the editor's selection or current file into the
// task, so "here" means what the user has highlighted rather than whatever the
// model guesses.
//
// The selection comes first and the request last, and the wording says the
// text is already present. Measured 2026-09-28 in Neovim, ministral-3-8b:
// asked "can you see what I highlighted?" with the old "The user has selected
// lines 5-5 of :" after the question - blank because the buffer was unsaved -
// it answered "I cannot see or interpret selections".
func withEditorContext(task string, p rpc.RunParams) string {
	if p.Selection == "" {
		if p.File != "" {
			return fmt.Sprintf("%s\n\n(The user is editing %s.)", task, p.File)
		}
		return task
	}
	where := "an unsaved buffer (not a file on disk)"
	if p.File != "" {
		where = p.File
	}
	return fmt.Sprintf("The user highlighted this text in %s, lines %d-%d. "+
		"It is included here in full - you can read it directly, no tool is needed:\n\n"+
		"```\n%s\n```\n\nThe user's request about the highlighted text: %s",
		where, p.StartLine, p.EndLine, p.Selection, task)
}
