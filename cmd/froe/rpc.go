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
	root     string
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

	// Fold the editor's selection into the task, so "here" means what the user
	// has highlighted rather than whatever the model guesses.
	if p.Selection != "" {
		task = fmt.Sprintf("%s\n\nThe user has selected lines %d-%d of %s:\n\n```\n%s\n```",
			task, p.StartLine, p.EndLine, p.File, p.Selection)
	} else if p.File != "" {
		task = fmt.Sprintf("%s\n\n(The user is editing %s.)", task, p.File)
	}

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
