# Roadmap

Open work, newest findings first. Every item comes from something observed or
measured, and says where. The phase-by-phase history that led here is being
rewritten against fresh measurements and will be added back when it is.

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
anywhere else. Ollama or LM Studio can run as the normal Windows app and be
reached from WSL.

## Ollama context length

Ollama loads a model at a 4096-token context unless told otherwise, and says
nothing when a prompt is longer: it truncates. `froe` budgets 8192 when the
runtime will not report its window, so on Ollama every prompt between 4096 and
8192 tokens was being cut without either side knowing. Seen 2026-09-24 in
`ollama ps` during the eval (one task sent ~19k input tokens across 6 turns).
The workaround is a model variant with `PARAMETER num_ctx 8192`.

To do: have `froe` set or read the real window for Ollama instead of assuming
it, and make `froe doctor` warn when the two disagree.

## Default model choice ignores whether a model is pulled

`resolve.ChooseByRole` checks that a model's runtime is up, not that the model
itself is present. With Ollama running, a registry entry that was never pulled
can be picked as the default, and the run fails on the first request. Seen
2026-09-24: plain `froe do` resolved to `qwen2.5-coder:7b`, which was not
pulled. An explicit `-model` is unaffected.

To do: skip models the runtime reports as absent, the way `froe doctor`
already labels them "not pulled".

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
