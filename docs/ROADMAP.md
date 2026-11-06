# Roadmap

Open work, newest findings first. Every item comes from something observed or
measured, and says where. The phase-by-phase history that led here is being
rewritten against fresh measurements and will be added back when it is.

## llama.cpp as the default runtime

**Decided 2026-11-06.** Froe ships only open-source runtimes. The same
Qwen3-Coder-30B-A3B Q4_K_M file, the full eval (3 runs per task, 120 s bar,
`FIRST_TURN_ALLOWANCE=180`, `prefix_reuse` on for both), RTX 4060 8 GB, 16 GB RAM:

| Runtime | Score | Suite time | Generation | Prompt reading |
|---|---|---|---|---|
| llama.cpp b12169, CUDA, `--fit` / `--n-cpu-moe 34` | 22/30 | 11m31s | ~31-35 tok/s | ~425 tok/s |
| colibri `dev` (both PRs below merged), CUDA | 16/30 | 62m55s | ~5 tok/s | ~22-50 tok/s |

llama.cpp had no timeouts; its eight misses are wrong answers (06, 07, 09, 10),
so the model, not the runtime, is now the limit. `froe-llamacpp` starts it with
`--jinja`, one slot (so prefix reuse holds) and an 8K window, and froe runs it
when the runtime is down. colibri stays supported with no default roles.

To do:
- Measure Ministral 3 8B on llama.cpp against the 30B: smaller and fully on the
  GPU, it may be the better pick for `quick`.
- A larger window: the 30B leaves ~1 GB of VRAM free at 8K; measure what 16K
  costs in speed (more experts on the CPU).
- Release binaries and Windows (next section) should bundle or find
  `llama-server` the way `froe-llamacpp` does on Linux.

## colibri (the default 2026-11-01 to 2026-11-06)

**Decided 2026-11-01, replaced 2026-11-06.** [colibri](https://github.com/JustVugg/colibri)
(Apache 2.0, pure C, Linux/macOS/Windows) runs mixture-of-experts models by
keeping the active part in RAM and streaming the experts from disk, so a
machine's RAM and disk matter more than its VRAM. On an 8 GB card that made it
too slow to stay the default (above); the integration work below is what made
`prefix_reuse` exist, and llama.cpp benefits from it too.

Facts that shape the integration, from colibri's own docs (v2.1.0):
- `coli serve` speaks OpenAI-compatible `/v1/chat/completions` with native tool
  calling on its chat engines (the Qwen3.6 engine also runs Qwen3-Coder and
  Flint). OLMoE and Inkling have no tool calling.
- Its GBNF support only drafts tokens for speed; it never constrains sampling.
  So `grammar = "no"`, and tool calls go through the native format.
- One model per server. Routing between two models means restarting the
  server, or one model in both roles.

### What was measured (2026-11-01, RTX 4060 8 GB, 16 GB RAM)

Colibri reads a prompt slowly the first time and almost for free after that:
a request that extends the conversation the server holds reads only what is
new. On a 2094-token prompt with Qwen3-Coder-30B-A3B: 129.5 s cold, 3.9 s as the
next turn. Generation is ~5 tok/s, CUDA or Vulkan alike: most experts do not
fit in 8 GB of VRAM and run on the CPU (~130 ms a token).

Froe was built for runtimes that start from nothing each time, so it threw the
reuse away. Eval task 01 took 433-459 s. Three things broke the match:
- notes ("Already explored", format hints) added to a per-request copy of the
  last message, so the next request lacked them;
- `fitContext` rewriting earlier tool results a little at a time;
- in colibri itself, a Qwen3-Coder tool-call turn re-rendered with one newline
  more than the model wrote (see Upstream below).

Done:
- Runtime flag `prefix_reuse` (on for colibri). With it, notes are kept in the
  history, and `fitContext` shrinks to 60% of its budget at once when it has
  to, so the next turns are append-only again.
- `fitContext` budgets against the window the runtime serves. It used the
  registry's `ctx_max` (262144 for these models), so it never trimmed.
- `FROE_DUMP_REQUESTS=<dir>` writes every request as sent.
- `froe do` reports the first turn's time; `eval/run.sh` has
  `FIRST_TURN_ALLOWANCE`.
- Result: with the Froe changes and the colibri patch, every turn after the
  first reuses 91-99% of the prompt.

### Upstream (colibri)

- **Tool-call turn re-rendered with an extra newline** (Qwen3-Coder renderer).
  The model writes `text\n<tool_call>`; the template rendered `text\n\n<tool_call>`,
  so reuse missed on every turn that had text before a call. **Merged** into
  `dev` 2026-11-05 ([#2535](https://github.com/JustVugg/colibri/pull/2535));
  the blank-line case for the other Qwen renderers is open as
  [#2544](https://github.com/JustVugg/colibri/issues/2544).
- **Rewind for attention-only models.** A new conversation that starts with
  what the server holds (the same Froe preamble) was read again in full.
  **Merged** into `dev` 2026-11-05 ([#2537](https://github.com/JustVugg/colibri/pull/2537)):
  the first turn of a repeated task fell from 52-99 s to 2-3 s.
- Benchmark rows for this laptop, as their protocol asks.
- `nvcc` 12.4 warnings in `backend_cuda.cu` (their PRs require zero warnings).

To do:
- ~~`[runtime.colibri]` and registry entries~~ Done: Qwen3-Coder-30B-A3B and
  Flint 40B-A2.5B, with measured speeds.
- ~~Start colibri when it is down~~ Done 2026-11-01: the runtime's `start`
  command (`froe-colibri --background`) runs before falling back to another
  local model, from the CLI and Neovim alike.
- A warm project session: one long conversation per project on its own KV slot
  (`--kv-slots`, `cache_slot`), each command a new turn, so the preamble is
  read once. It reverses D8's fresh context per command, so it has to be
  measured.
- ~~Read the served context window~~ Done: `/v1/models` `context_length`.
- ~~`froe doctor` counted colibri's own VRAM against it~~ Done: "serving" and
  "not served" verdicts for single-model runtimes.
- ~~Run the full eval and set roles from the result~~ Done 2026-11-06: 16/30
  in 62m55s on `dev`; the roles moved to llama.cpp (above).
- Upstream: send colibri the benchmark rows (they ask for them, negative results
  included) and any tool-call bugs the eval finds.

## The CLI as a full client, and Windows

**Decided 2026-11-01.** Many people will use froe without Neovim, so the
terminal is a first-class client, not a fallback. Features move between the two
together, through shared code.

To do:
- Decide what `froe chat` lacks for daily use, and whether the hand-written
  screen (`cmd/froe/screen.go`) keeps up or a TUI library replaces it.
- Windows: the gaps in the next section, plus a Windows start script for
  llama-server (`froe-llamacpp` is bash), a newline key the Windows console can deliver (Ctrl+Shift+Enter
  may not reach the program), ANSI output enabled at start, the config
  directory under `%APPDATA%`, and CRLF files in the edit tool.
- A Windows CI job running `go test`.

## Per-task model routing

**Started 2026-09-29, Neovim only so far.** With no model pinned, each task in
the editor is routed to a `quick` or `careful` model (`internal/resolve/route.go`).
The runtime's `start` command (`runtimes.toml`) brings a down runtime up; the
shipped default holds both roles on one llama-server, so there is no switching
between loaded models. The output split says which class, which model, and why.
Pinning a model (`:FroeModel <id>`) turns routing off.

Why: on 2026-09-29, one read-only lookup (count the `.py` files under
`src/qrl/lang`, name the type-check function, name the CLI file) went
ministral-3-8b 4.7s with a wrong count and unchecked names, against
bonsai-27b 38.6s and 3/3. Neither model is right for every task, and
choosing by hand before every task is friction.

Live check the same day, through `froe rpc`: "explain ... a process matrix"
routed quick to ministral (6.9s including the load); a counting task routed
careful and loaded Bonsai (19.3s). Bonsai miscounted (8, not 9) on that run -
routing picks the better model, it does not make it right.

To do:
- The rule is word patterns, checked only against the cases in
  `route_test.go`. Measure it: route every task in `eval/` and compare the
  routed model's pass rate and time with each model pinned.
- `froe ask` and `froe do` still use the single default. Route them too, or
  say why not.
- Each switch costs a load (about 2-5s here). A run of tasks that alternate
  classes pays it every time; consider staying on the loaded model when the
  route is marginal.

## Native Windows support

`froe` cross-compiles for Windows (`GOOS=windows go build ./cmd/froe`, checked
2026-09-24) but has never been run there, and four parts assume Linux:

| Part | Gap on Windows |
|---|---|
| `bash` tool (`internal/tools/bash.go`) | Runs every command as `bash -c`. With no bash on `PATH` the agent cannot run a test, a build, or anything else. |
| Hardware detection (`internal/hw/hw.go`) | Reads CPU and memory from `/proc`, which does not exist there, so `froe doctor` reports 0 MB of RAM and every fit verdict is wrong. `nvidia-smi` works as-is. |
| Denylist (`internal/tools/bash.go`) | Written for Unix commands (`rm -rf`, `dd`, `sudo`). The Windows equivalents (`del /s`, `rd /s`, `format`, `runas`) are not refused, and under `-yolo` the denylist is the last line of defence. |
| `rg` and `git` | Required, and available for Windows. No code change, but `docs/SETUP.md` needs the steps. |

To do:
- Read memory through the Windows API when `/proc/meminfo` is missing.
- Pick the shell at startup: bash if it is on `PATH`, otherwise PowerShell,
  with a denylist for whichever shell is actually in use.
- Run the eval on a real Windows machine before claiming support. A clean
  cross-compile says nothing about behaviour.

**Until then: WSL2.** Inside WSL, `froe` is on Linux and behaves as it does
anywhere else. The model runtime (Ollama, or colibri, which ships a native
Windows build) can run on Windows itself and be reached from WSL.

## Ollama context length

Ollama loads a model at a 4096-token context unless told otherwise, and says
nothing when a prompt is longer: it truncates. `froe` budgets 8192 when the
runtime will not report its window, so on Ollama every prompt between 4096 and
8192 tokens was being cut without either side knowing. Seen 2026-09-24 in
`ollama ps` during the eval (one task sent ~19k input tokens across 6 turns).
The workaround is a model variant with `PARAMETER num_ctx 8192`.

To do: have `froe` set or read the real window for Ollama instead of assuming
it, and make `froe doctor` warn when the two disagree.

## Makefile Go path

The Makefile defaults `GO` to `~/.local/go/bin/go`. On a machine where Go is
installed elsewhere, `make` fails with "No such file or directory" until `GO`
is overridden (`make GO=/usr/bin/go install`). Seen 2026-09-24.

To do: default to `go` on `PATH` and fall back to the local install.

## `froe bench`

`docs/MODELS.md` §6 describes `froe bench`: fixed tasks, real prefill and
generation throughput, tool-call validity, recorded per machine. **It does not
exist yet.** Today the only benchmark is `eval/run.sh`, which measures pass
rate and time but not throughput or tool-call validity.
