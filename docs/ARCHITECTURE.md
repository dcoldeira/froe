# Architecture

**Status:** Design. Nothing here is built yet.

---

## 1. Shape of the system

```
                    ┌──────────────────────────────────┐
   terminal  ──────▶│                                  │
                    │        froe (Go binary)          │
   :Froe     ──────▶│                                  │
   (Lua plugin,     │  ┌────────────────────────────┐  │
    JSON-RPC/stdio) │  │      agent loop            │  │
                    │  │  plan → tool → observe     │  │
                    │  └──────────┬─────────────────┘  │
                    │             │                    │
                    │  ┌──────────▼──────┐ ┌────────┐  │
                    │  │   tool registry │ │ context│  │
                    │  │ read edit grep  │ │ engine │  │
                    │  │ bash git gh     │ │ (repo  │  │
                    │  └─────────────────┘ │  map)  │  │
                    │                      └────────┘  │
                    │  ┌────────────────────────────┐  │
                    │  │    provider abstraction    │  │
                    │  └──┬──────────┬──────────┬───┘  │
                    └─────┼──────────┼──────────┼──────┘
                          │          │          │
                    openai-compat  anthropic  llamacpp
                          │                     (native:
                    ┌─────┴──────┐               GBNF
                    │            │               grammar)
              llama-server    Ollama
              LM Studio       vLLM
              Mistral         DeepSeek
```

One binary. Two front ends. The Neovim plugin is a *client*, not a
reimplementation — it owns no agent logic, no provider code, no prompts.

## 2. Repository layout

```
froe/
├── cmd/froe/               # entry point, flag parsing, subcommands
├── internal/
│   ├── provider/             # model backends. ONE package, several files: a
│   │   ├── provider.go       #   factory in a parent package importing children
│   │   ├── openaicompat.go   #   that import the parent for its types is an
│   │   ├── anthropic.go      #   import cycle. Flat beats clever here.
│   │   └── factory.go        #   Switches on Runtime.Kind, never runtime name.
│   ├── registry/             # model + runtime catalogues
│   │   └── defaults/         #   shipped TOML, go:embed'd
│   ├── probe/                # is a runtime live? shared by doctor and resolve
│   ├── resolve/              # which model serves this request
│   ├── doctor/               # what can this machine run
│   ├── hw/                   # hardware detection
│   ├── config/               # config paths, ~ expansion
│   ├── agent/                # (Phase 3) the loop: plan → tool → observe
│   ├── tools/                # (Phase 3) one tool per file
│   ├── perms/                # (Phase 3) gating for side-effecting tools
│   ├── context/              # (Phase 4) repo map, ranking, FROE.md
│   ├── session/              # (Phase 5) SQLite persistence, token accounting
│   ├── tui/                  # (Phase 5) Bubble Tea interface
│   ├── rpc/                  # (Phase 6) JSON-RPC over stdio for Neovim
│   └── router/               # (Phase 8) task → role → model
├── nvim/lua/froe/          # (Phase 6) the Neovim plugin
└── docs/
```

## 3. Provider abstraction

The interface is deliberately thin. Everything hard — retries, tool-call
strategy selection, context budgeting — lives above it in the agent, so adding
a backend stays cheap.

```go
type Provider interface {
    Name() string
    Caps() Caps
    Chat(ctx context.Context, req Request) (<-chan Event, error)
}

type Caps struct {
    NativeTools  bool   // has a real function-calling API
    Grammar      bool   // can constrain output to a GBNF/JSON schema
    Streaming    bool
    MaxContext   int
    Vision       bool
}
```

`Chat` returns a channel of `Event` — `TextDelta`, `ToolCallDelta`, `ToolCall`,
`Usage`, `Done`, `Err` — so streaming, cancellation and partial tool calls are
uniform across backends. `ctx` cancellation must abort the in-flight HTTP
request; on llama.cpp that also frees the slot.

**Why OpenAI-compatible is the lingua franca:** llama.cpp's `llama-server`,
Ollama, LM Studio, vLLM, Mistral and DeepSeek all expose
`/v1/chat/completions`. One adapter, six backends, and any future runtime that
follows the convention works on day one. Anthropic does not follow it, so it
gets its own adapter. llama.cpp gets a *second*, native adapter because its
extra surface (below) is worth having.

## 4. Tool calling — the hard part for small models

This is where a local-first agent lives or dies. A 7B model that free-forms its
tool calls produces malformed JSON constantly, and every malformed call is a
wasted round trip you pay for in seconds, not cents. Three strategies, selected
per-model by capability:

| Strategy | When | Mechanism |
|---|---|---|
| `native` | Provider advertises function calling and the model was trained for it (Qwen2.5-Coder, Mistral, Claude) | Pass the tool schemas through; trust the API |
| `grammar` | llama.cpp backend, any model | Compile the tool set into a GBNF grammar; the sampler **cannot emit invalid output**. This is the single biggest reliability win available locally |
| `react` | Anything else | Constrained text protocol, strict parser, auto-retry feeding the parse error back |

The grammar path is the main argument for using `llama-server` directly rather
than through Ollama — Ollama does not expose GBNF.

## 5. Context engineering

With a 200k-context frontier model you can be lazy and dump whole files. With a
7B at 32k you cannot. Context discipline is a *core feature*, not an
optimisation:

- **Repo map first, files second.** Tree-sitter extracts symbol signatures
  across the repo; the model sees a structural map and asks for the specific
  files it needs. Dumping a 2000-line file costs ~25k tokens and, on a
  CPU-bound machine, minutes of prefill before a single token comes back.
- **Ranked retrieval.** ripgrep is already on the box and beats embeddings for
  code lookup at this scale. No vector DB unless something proves it necessary.
- **Explicit budget.** Every model entry declares a context ceiling; the
  builder packs to a budget and reports what it dropped rather than silently
  truncating. (Ollama silently truncating at its 4096 `num_ctx` default is a
  known footgun — see `docs/MODELS.md`.)
- **`FROE.md`.** Per-project instructions, same idea as `CLAUDE.md`. Discovered
  by walking up from cwd. Existing `CLAUDE.md` files are read too — no reason
  to make him write the file twice.

## 6. Permissions

A local 7B is less predictable than Claude, so gating matters more, not less.

- Read-only tools (`read`, `grep`, `glob`, `repo_map`) — no prompt.
- Mutating tools (`edit`, `write`, `bash`, `git`) — prompt, with a diff or the
  literal command shown.
- Per-project allowlists in `FROE.md` for the commands you always approve.
- A hard denylist that no config can override (`rm -rf /`, force-push to
  default branches, writes outside the project root).

## 7. Session state

SQLite via `modernc.org/sqlite` — pure Go, no cgo, so cross-compilation stays
trivial. Stores sessions, message history, tool calls and results, and token
accounting per model. Enables `froe --resume`, and gives the bench harness
(`docs/MODELS.md`) real data on how each model actually performs on your work
rather than on a public leaderboard.

## 8. Neovim integration

Standard LSP-style plumbing: the plugin spawns `froe --rpc` and speaks JSON-RPC
over stdio. `vim.system()` and `vim.json` cover it; no external Lua deps.

Planned surface:

- `:Froe <prompt>` — task against the current file/project
- visual selection → prompt, scoped to the range
- diffs land in a preview buffer with accept/reject, never silent writes
- streaming output into a split
- `<C-c>` cancels — propagated to context cancellation and an aborted HTTP
  request, not just a hidden background job

The plugin holds no agent logic. If it grows past ~500 lines of Lua something
has leaked out of the binary and needs pushing back in.

## 9. Deferred, deliberately

Recorded so they do not get quietly reinvented later:

- **MCP client support.** Real value, but not before the core loop is solid.
- **Embeddings/vector search.** ripgrep plus a tree-sitter repo map first;
  revisit only if retrieval measurably fails.
- **Multi-agent / subagent spawning.** Expensive on local hardware, and the
  single-agent loop needs to be good first.
- **Its own model server.** Froe orchestrates llama.cpp; it does not reimplement
  it.
