<p align="center">
  <img src="assets/froe.svg" width="132" alt="">
</p>

<h1 align="center">froe</h1>

<p align="center">
  <em>An open-source coding agent that runs on your own machine.<br>
  Terminal and Neovim, open models, and no source code leaving the machine.</em>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#using-it">Using it</a> ·
  <a href="#plain-code-checks-the-model">Checking the model</a> ·
  <a href="#evaluation">Evaluation</a> ·
  <a href="docs/ARCHITECTURE.md">Architecture</a>
</p>

---

Froe reads, edits and runs code in your project, the way a hosted coding agent
does, but the model runs on your own hardware: an open model on an open runtime
([llama.cpp](https://github.com/ggml-org/llama.cpp) by default). Plain Go code
checks the model's work wherever it can, and every edit, command and web search
waits for your approval.

- **Local and open, end to end.** Froe is MIT, every runtime it ships with is
  open source, and nothing local falls back to a hosted model unless you say so.
- **Plain code checks the model.** After the model answers, deterministic Go
  verifies what it can, and the model never writes a git command.
- **Measured on an ordinary laptop.** 24 of 30 eval runs pass in about 15
  minutes on an 8 GB GPU with 16 GB of RAM. [The numbers](#evaluation).
- **One binary, two clients.** The terminal and Neovim share one agent, one
  permission gate and one session store.

## Using it

Run `froe` in any project directory to start a session. It keeps its memory of
the project between sessions, and it can search the web.

```bash
cd ~/my-project
froe                          # interactive session (also: froe chat)
```

From Neovim, over JSON-RPC to the same binary:

```vim
:Froe fix the null guard in this function
```

Commands, keymaps and a short how-to: [`nvim/README.md`](nvim/README.md).

### Short jobs get their own commands

Some jobs are small enough that an open-ended loop only wastes time. Each of
these does one job with a fresh context and the smallest turn budget that fits,
then hands control back:

```bash
froe locate "<issue text>"    # WHERE in the code does this live? (read-only)
froe commit -a -m "…" -p      # staged diff → message → commit → push
froe ask "<question>"         # one question, one answer, no tools
froe do "<task>"              # one task, the full agent loop, then exit
froe map                      # the project map the model is given
froe doctor                   # what this machine can actually run
froe memory                   # durable facts about this project
froe sessions                 # list or replay past conversations
```

They exist because of a measurement. In September 2026 a 27B model with an
8192-token window was pointed at a real issue: remove one field from a report
table, four edit sites, in a 1435-line file inside a 5651-file repository.

| run | outcome |
|---|---|
| 1 | 21 turns, 7m14 — 3 of 4 sites, left a row with 6 cells against 5 headers |
| 2 | died at turn 4, context overflow |
| 3 | 24 turns, 7m10 — 2 of 4 sites |
| 4 (read-only) | found all sites by turn 4, then wandered to the turn limit |

The failure was never capability. In every run it found the right file from a
path the issue got *wrong*, and it noticed that a `col_widths` list was
positionally coupled to its headers, a constraint nobody had stated. The failure
was memory: run 3 spent turns 12 to 23 hunting for the issue's phantom path, two
turns *after* it had already edited the real file. A job split into bounded
steps keeps the state with you instead of in a window that cannot hold it
(decision D8 in [`docs/DECISIONS.md`](docs/DECISIONS.md)). Today's default runs
with a 32K window, which is why the open session is now the main way in.

## Plain code checks the model

The biggest gain did not come from a larger window or a better prompt. It came
from **deterministic Go that checks the model's answer once it has finished**.

`froe locate` re-runs the model's own successful searches inside the files it
cited, and reports the lines it missed. That costs no turns and no tokens, and
most of `locate`'s reliability came from it rather than from anything the model
was told. Measured on a fixture built at the real thing's difficulty — 405 Python
files, an 1876-line target, four decoys that all match the obvious glob:

| | pass rate (3 repeats) | median turns |
|---|---|---|
| baseline | 0/3 | 6 |
| + relaxed separators in the verification sweep | 1/3 | 6 |
| + searches derived from the report's own quoted phrases | 2/3 | 6 |
| + **searching the project before turn 1** | **3/3** | **4** |

For `froe commit`, the model is only ever asked for *message text*, never for
commands. Every git call is fixed Go with a fixed argv and no shell.

## Why it exists

1. **Data governance.** For anyone working under UK/EU data-residency rules,
   source code should not have to leave the machine, the LAN, or at the very
   least the EU. A local model is the default path here, not an afterthought
   bolted onto a cloud tool.
2. **Model churn.** The best local model changes every few months. Nothing about
   a model is compiled in: models are registry entries, hardware profiles are
   detected, and a runtime is a command to start it.
3. **Open source, end to end.** The code that sees your source should be code
   you can read. Froe is MIT, and every runtime it ships with is open source:
   llama.cpp (the default), Ollama, vLLM and
   [colibri](https://github.com/JustVugg/colibri).
4. **Measured on ordinary hardware.** The numbers below come from one laptop
   with 16 GB of RAM and an 8 GB GPU. Nothing is tuned for that box in particular:
   `froe doctor` measures what a machine can actually do instead of assuming,
   and a bigger one simply runs bigger models.

## Install

```bash
gh repo clone dcoldeira/froe
cd froe
go install ./cmd/froe     # → ~/go/bin/froe   (make install → ~/.local/bin instead)
froe doctor
```

Then point it at a model runtime. Froe speaks OpenAI-compatible
`/v1/chat/completions` as its lingua franca, so llama.cpp, Ollama, vLLM and
colibri all work through one adapter.

The default is **Qwen3-Coder-30B-A3B on [llama.cpp](https://github.com/ggml-org/llama.cpp)**'s
`llama-server`. It is a mixture-of-experts model with 3.3B of its 30.5B
parameters active per token, so it runs on an 8 GB card with the rest of the
experts on the CPU. With `prefix_reuse` set (it is, for llama.cpp) the
conversation only ever grows, so each turn reads only what is new. When
llama-server is down, froe starts it before the first request, from the terminal
or from Neovim, with `froe-llamacpp`, installed next to froe:

```bash
froe-llamacpp --model ~/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf --background
```

It runs one slot (so the conversation stays where it was read), the model's own
chat template for native tool calls, and a 32K window; llama.cpp's `--fit` sizes
the GPU/CPU split to the card.

[colibri](https://github.com/JustVugg/colibri) is supported too, started the same
way by `froe-colibri`. It was the default from 2026-11-01 to 2026-11-06; two fixes
found while building froe around it are merged upstream
([#2535](https://github.com/JustVugg/colibri/pull/2535): a tool-call turn
re-rendered one newline off, so nothing was reused;
[#2537](https://github.com/JustVugg/colibri/pull/2537): resume a new task from the
shared system prompt instead of reading it again). On an 8 GB card the same
model runs about six times faster on llama.cpp, which is why the default moved.

Mistral (EU-hosted) is the cloud fallback for when local hardware can't fit the
job — the sane choice when governance forbids US processing; Anthropic has its
own adapter for those who want it regardless. Nothing local ever falls back to
a hosted model without being told to.
Full walkthrough, including the Neovim plugin: [`docs/SETUP.md`](docs/SETUP.md).

⚠ One runtime footgun worth knowing before you hit it: Ollama's `num_ctx`
defaults to 4096 and **silently truncates** everything past it.

## Design in one paragraph

A single static Go binary (~13k lines) holds the agent loop, the tool
implementations and the provider adapters. Tool calls are produced three
different ways — GBNF grammar, native tool-calling, or a ReAct text protocol —
because which one works is a property of the *model*, not a global default. A
~580-line Lua plugin drives the same binary from Neovim over JSON-RPC on stdio,
the LSP pattern, so the editor and the terminal share one implementation, one
permission gate and one session store.

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — how the pieces fit
- [`docs/MODELS.md`](docs/MODELS.md) — the registry, hardware profiles, runtimes
- [`docs/SETUP.md`](docs/SETUP.md) — bare machine to working install
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — open work, each item from an observed problem
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — the design decisions and where each is enforced
- [`TESTING.md`](TESTING.md) — unit tests, the switched-off rule, eval hygiene

## Evaluation

`eval/` holds ten tasks, each scored as a **pass rate over repeats** against a
120 s bar per run. Variance between identical runs of an agent is large enough
to swamp the effect of a real fix, so one run cannot tell you whether a change
helped. A task can name the command it exercises, so `froe locate` is measured
as itself instead of being approximated with `do`.

```bash
bash eval/run.sh            # all tasks
ONLY=01-fix-panic bash eval/run.sh
```

On a laptop with an 8 GB RTX 4060 and 16 GB of RAM, 3 runs per task:

| Model | Runtime | Score | Suite time | When |
|---|---|---|---|---|
| **Qwen3-Coder 30B-A3B** (default, 32K window) | **llama.cpp** | **24/30** | **15m13s** | 2026-11-06 |
| Qwen3-Coder 30B-A3B, 8K window, before the keep-alive fix | llama.cpp | 22/30 | 11m31s | 2026-11-06 |
| Qwen3-Coder 30B-A3B, same file | colibri | 16/30 * | 62m55s | 2026-11-06 |
| Qwen3-Coder 30B-A3B, same file | colibri | 18/30 * | 56m | 2026-11-01 |
| Ministral 3 8B | Ollama | 19/30 | 6m24s | 2026-09-25 |

On llama.cpp the model generates ~22-35 tokens a second and reads prompts at
~200-425, so no run timed out. At 32K every task passes 3/3 except the two that
locate code from a bug report (09, 10), where the model answers too early. On colibri the same file runs at ~5 tokens a second on this laptop,
because most experts sit on the CPU, and a run that needs 12-16 turns misses
the bar.

\* With `FIRST_TURN_ALLOWANCE=180`: colibri reads a project's prompt cold the
first time (50-150 s on this laptop), and the 120 s bar applies to the turns
after it. llama.cpp needs no allowance: its first turns took 0-9 s.

The full table, the tasks and the failure modes are in
[`eval/README.md`](eval/README.md).

## Status, honestly

Public since 2026-09-23.

In daily use for commits: `froe commit` drives the release process for
[QRL](https://github.com/entangledcode/qrl) and [Bell](https://bell.entangledcode.dev),
the two projects it was built to develop. `froe locate` is the first of the stepwise
commands and is measured above.

**Runtime:** llama.cpp is the default since 2026-11-06, measured against colibri
on the same model; `froe-llamacpp` starts it with the settings froe needs.
colibri stays supported, and two colibri pull requests that came out of the work
are merged upstream ([#2535](https://github.com/JustVugg/colibri/pull/2535),
[#2537](https://github.com/JustVugg/colibri/pull/2537)). More stepwise commands
(`explain`, `plan`, `step`) are planned, not built.

**Platforms:** Linux is where it is used and measured. It builds for macOS and
Windows, but neither has been run end to end; on Windows, use WSL2 for now
([`docs/ROADMAP.md`](docs/ROADMAP.md) lists the gaps).

**Not claimed:** this is not a replacement for a frontier-model coding agent, and
it is not trying to be. It is what local open models can honestly do on the
machine in front of you, plus a lot of deterministic Go making sure the model did
not quietly get it wrong.

## Licence

MIT — see [`LICENSE`](LICENSE). Contributions are covered by [`CLA.md`](CLA.md);
sign it before a pull request can be merged.
