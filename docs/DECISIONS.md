# Design decisions

Each entry states a decision, why it was made, and where it is enforced. The
log was rewritten in September 2026. Measurements are quoted only where they
were re-taken or already sit in the code beside the decision; the rest cite the
code that enforces them. D3, D4, D6 and D7 keep the numbers the source code
cites.

## D1. Local first, one wire protocol

Source code should not have to leave the machine. A local model is the default
path. OpenAI-compatible `/v1/chat/completions` is the one protocol for local
runtimes, so llama.cpp, Ollama, LM Studio and vLLM share one adapter. The cloud
fallback is Mistral, which is EU-hosted, for work that governance allows off
the machine.

*Enforced in:* `internal/provider/openaicompat.go`; `internal/provider/factory.go`.

## D2. How tool calls are produced is a property of the model

A model's tool calls come by native tool-calling, a GBNF grammar, or a ReAct
text protocol, whichever works for that model. It is set per registry entry
(`tool_strategy`), never globally.

*Measured 2026-09-25:* the strategy has to be judged together with the runtime.
ministral-3:8b on Ollama scored 19/30 with native calls and 8/30 with ReAct,
because Ollama intercepts the model's tool-call syntax even when it was told no
tools exist.

*Enforced in:* `internal/registry/registry.go` (`ToolStrategy`); `internal/agent/react.go`, `grammar.go`.

## D3. Models are data, never code

No model id, context size or capability appears in Go source. Shipped defaults
are embedded TOML, and users overlay their own at `~/.config/froe/models.toml`
without recompiling. The best local model changes every few months. Since
2026-09-25 the default is chosen by a role in TOML (D13), so changing it again
means editing a TOML file, not Go.

*Enforced in:* `internal/registry/` and `internal/registry/defaults/*.toml`.

## D4. The model gets a map, not the files

On CPU-bound hardware prefill dominates, and one 2000-line file is about 25k
tokens and minutes of silence. The model receives a structural map (files and
what they define) and asks for the files it needs.

*Enforced in:* `internal/repo/` (`froe map` shows what the model is given).

## D5. One binary; the editor is a client of it

One static Go binary holds the agent loop, tools and provider adapters. Neovim
drives the same binary over JSON-RPC on stdio, the LSP pattern. Terminal and
editor therefore share one implementation, one permission gate and one session
store.

*Enforced in:* `internal/rpc/`; `nvim/`.

## D6. A runtime is a binary path, not a name

Several llama.cpp builds coexist. PrismML's fork is required for Bonsai's
`Q1_0_g128` kernels, and upstream cannot load that format. A runtime entry
names the binary and endpoint it means.

*Enforced in:* `internal/registry/defaults/runtimes.toml`; `internal/registry/registry.go`.

## D7. Fit is decided by footprint, not parameter count

The same 27B is a 16.5 GB CPU-bound crawl at Q4 and a 3.5 GB near-resident
model at 1.125 bits per weight. `froe doctor` decides whether a model is
resident, offloads, or does not fit from `size_gb` and measured `peak_mb`.
`params_b` is metadata only.

*Enforced in:* `internal/doctor/doctor.go`; `internal/registry/defaults/models.toml`.

## D8. Short single-job commands, not an autonomous loop

`locate`, `commit`, `ask` and `do` each do one job with a fresh context and a
small turn budget, then hand control back. On a long autonomous task at an
8192-token window the failure is memory, not capability: the model finds the
right file and then loses track of what it has already done. So the state
lives with the user and in the conversation, not in a context window that
cannot hold it. The README gives the run-by-run account.

*Enforced in:* `cmd/froe/` (one file per command); `agent.DefaultMaxTurns`.

## D9. Check the model's work after it stops, in plain Go

Deterministic checks after the model finishes proved a bigger lever than
prompting. They cost no turns and no tokens:

- `locate` re-runs the model's own searches in the files it cited and reports
  lines it missed. It corrects cited line numbers from the code the answer
  quotes.
- `do` sends a run back once if it:
  - edited files but never ran the check the task asked for,
  - ends with other spellings of what it removed still in the edited files, or
  - showed a change in a code block without making it.

Each push happens at most once per run, so a model that ignores it still
finishes.

*Measured 2026-09-24 on Bonsai:*

| Task | Before | After | Fix |
|---|---|---|---|
| 09 | 0/3 | 2/3 | line correction |
| 10 | 0/3 | 3/3 | narrow sweep plus one recheck turn |

*Enforced in:* `cmd/froe/locate_*.go`; `internal/agent/agent.go`, `leftovers.go`.

## D10. An ambiguous edit is an error

`edit_file` refuses an `old_string` that matches more than once. Replacing the
wrong occurrence is a silent corruption the model cannot notice. `replace_all`
is the deliberate way to change every match. When a match fails, the error
explains why, showing the file's real bytes with whitespace made visible.

*Enforced in:* `internal/tools/edit.go`.

## D11. Repeats are answered tersely, and loops are ended

A repeat of an identical successful call does not reach the tool. The model
gets a short "nothing new" notice where it expects new information. Three
identical calls end the run as stuck. froe's own actions do not count as loops:
if froe dropped a result to fit the context, asking for it again is legitimate,
and it is served again.

*Enforced in:* `internal/agent/agent.go` (`resultCache`, `maxIdenticalCalls`, `stillHeld`).

## D12. Evaluate by rate, against a clock

Every eval task runs three times, and models are compared on pass rates from
the same froe build. A run past 120 s fails, because speed is part of the
grade for an interactive tool. Answer-graded tasks grade only the model's last
answer, never the context froe appends to it.

*Enforced in:* `eval/run.sh`; see `eval/README.md`.

## D13. The default model is a measured, named choice

Model choice is smallest-first within a role, which suits constrained
hardware. The default is instead a model given the `default` role on the
strength of the eval. Ministral 8B scored 23/30 in 6m39s against Bonsai's
24/30 in 23m19s, and is the default. A model the runtime does not have is
never picked.

*Enforced in:* `internal/resolve/resolve.go` (`RolePreference`, `pulled`).

## D14. Runtime quirks are absorbed by froe, not left to the model

A model cannot fix a malformed request froe sends, or a reply the runtime
drops. froe handles each quirk seen in practice where it happens:

- **Parallel tool calls:** Ollama sends every parallel call at index 0. froe
  starts a new call at each new id rather than fusing them.
- **Invalid arguments:** arguments that are not valid JSON are replayed as
  `{}`. Ollama rejects a history that holds them with HTTP 400.
- **Unparseable tool calls:** a runtime failing to parse the model's tool call
  (HTTP 500) is retried up to twice, with a note.
- **Strict chat templates:** froe's notes are attached to the tool result.
  Mistral's template refuses a user turn after a tool result, which ended 25
  of 30 runs on LM Studio.
- **Leaked markup:** edits carrying chat-template markup the file did not
  already contain are refused.

*Enforced in:* `internal/provider/openaicompat.go`; `internal/agent/agent.go` (`withNote`); `internal/tools/markup.go`.
