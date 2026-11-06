# froe evaluation harness

`eval/run.sh` runs `froe` against ten small tasks, each in a throwaway git repo,
and grades each run by a mechanical check: code compiles, a test passes, an
answer contains a required string. No human judgement is involved, so results
compare across models and across changes to froe itself.

```bash
MODEL=qwen3-coder-30b-llamacpp bash eval/run.sh                 # full suite, 3 runs per task
MODEL=qwen3-coder-30b-colibri FIRST_TURN_ALLOWANCE=180 ONLY=07-multi-site bash eval/run.sh
```

| Variable | Default | Meaning |
|---|---|---|
| `MODEL` | froe's own pick | registry id to run |
| `REPEATS` | `3` | runs per task; the score is a rate |
| `ONLY` | all | run one task by directory name |
| `TIMEOUT` | `120` | seconds per run; a run past it is a FAIL |
| `FIRST_TURN_ALLOWANCE` | `0` | extra seconds for the first turn only, for a runtime that reuses the prompt it holds (colibri); the turns after the first must still fit `TIMEOUT` |
| `TURNS` | `25` | turn budget, the same as `froe do` |
| `MODE` | `-yolo` | permission mode; each task runs in a scratch repo |
| `FROE` | `froe` | binary under test |

Run artefacts (each run's sandbox and its full `froe` output) are kept under
the `mktemp` directory printed at the end.

## Why repeats, and why a timeout

A single run of a stochastic process cannot detect a change worth one task. On
2026-09-15 two runs of the same task with the same config produced materially
different patches. So every task runs `REPEATS` times and you compare pass
**rates**.

Speed is part of the grade. A model that gets there in ten minutes has failed
a tool meant to be used interactively, so `TIMEOUT` is a hard bar.

## The tasks

| Task | Command | What it tests | Graded by |
|---|---|---|---|
| 01-fix-panic | `do` | fix an empty-slice panic in Go | `go test` |
| 02-find-symbol | `do` | find a definition among its callers | the answer names the file |
| 03-add-function | `do` | implement a function a test already expects | `go test` |
| 04-git-history | `do` | read git history | the answer quotes the latest subject |
| 05-multi-file | `do` | rename a Go function and its call sites | `go build` plus no old name left |
| 06-wrong-path | `do` | the task names a path that does not exist | module imports, column removed, others kept |
| 07-multi-site | `do` | remove a column spelled 4 ways across 4 coupled sites | module imports, every spelling gone |
| 08-large-file | `do` | change one line of a 1400-function file | only that line differs from the base commit |
| 09-locate-wrong-path | `locate` | issue text with a wrong path and decoys | cited lines, lookalikes only under WATCH OUT |
| 10-locate-real-shape | `locate` | a full issue-tracker report, decoy matches | cited lines, lookalikes only under WATCH OUT |

The Python tasks (02, 06–10) share a quantum-causal-structure theme taken from
the QRL project and use only the standard library. Every `verify.sh` was
self-tested: set up the fixture, apply a hand-written correct fix or a known
wrong answer, and check that it passes or fails as it should.

**Answer-graded tasks (02, 04, 09, 10) grade only the model's last answer.**
froe adds context after the answer (AROUND THOSE LINES, ALSO MATCHING). On
2026-09-24 grading the whole output let that context pass runs, and one model
scored 6/10 where the truth was 4/10. froe's LINE NUMBERS CORRECTED section is
the exception and is graded, because froe checked it against the file.

## Failure modes

A failing run is classified so a change that turns one failure into another
(an overflow into a clean abort, say) shows up as movement rather than the same
pass count.

| Mode | Meaning |
|---|---|
| `timeout` | still running at `TIMEOUT` (plus `FIRST_TURN_ALLOWANCE`) |
| `too-slow` | finished, but the turns after the first took longer than `TIMEOUT` |
| `context-overflow` | the runtime refused a prompt larger than its context |
| `runtime-error` | the runtime answered HTTP 5xx and froe stopped |
| `stuck-failing` / `stuck-fruitless` / `stuck-no-progress` | froe's loop detector ended the run |
| `max-turns` | the turn budget ran out |
| `withdrawn-citation` | froe caught a cited path that is not in the tree |
| `wrong-result` | the run finished and the check failed |

## Latest results

### llama.cpp against colibri, same model (2026-11-06)

Qwen3-Coder-30B-A3B, the same Q4_K_M file on both runtimes, 8K context,
`prefix_reuse` on for both, 3 runs per task, `FIRST_TURN_ALLOWANCE=180`, on an
RTX 4060 8 GB laptop with 16 GB RAM. One runtime at a time, nothing else running.

| Task | llama.cpp b12169 (CUDA) | Median secs (first turn) | colibri `dev` 50ec53ee (CUDA) | Median secs (first turn) |
|---|---|---|---|---|
| 01-fix-panic | 3/3 | 24 (1) | 2/3 | 96 (3) |
| 02-find-symbol | 3/3 | 1 | 3/3 | 1 |
| 03-add-function | 3/3 | 19 (1) | 1/3 | 191 (48) |
| 04-git-history | 3/3 | 2 | 3/3 | 6 (1) |
| 05-multi-file | 3/3 | 29 (1) | 3/3 | 122 (44) |
| 06-wrong-path | 2/3 | 43 (1) | 1/3 | 245 (5) |
| 07-multi-site | 1/3 | 50 | 0/3 | 300 |
| 08-large-file | 3/3 | 22 (2) | 2/3 | 60 (6) |
| 09-locate-wrong-path | 1/3 | 15 (6) | 1/3 | 40 (8) |
| 10-locate-real-shape | 0/3 | 19 (9) | 0/3 | 46 |
| **total** | **22/30** | **11m31s** | **16/30** | **62m55s** |

- **Every llama.cpp failure is a wrong result**; none timed out or ran too
  slow. On colibri, 7 of the 14 failures are timeouts or too-slow runs.
- **Speed is the difference.** llama-bench on the same file: ~35 tok/s
  generation and ~425 tok/s prompt reading at `--n-cpu-moe 34` (26 runs out of
  VRAM; 38 gives 33 tok/s, 42 gives 27). colibri: ~5 tok/s and ~22-50 tok/s.
  `froe-llamacpp` leaves the split to `--fit`, which measured ~31-32 tok/s warm.
- **What is left is the model.** 07 now passes once; 09 and 10 still answer
  from the pre-search in one or two turns at the wrong place.
- llama.cpp needs no first-turn allowance: its first turns took 0-9 s.

### colibri (2026-11-01)

The first run on an open-source runtime. Qwen3-Coder-30B-A3B (Q4_K_M GGUF) on
[colibri](https://github.com/JustVugg/colibri) v2.1.0, CUDA build, 8K context,
`COLI_PARK=0`, 3 runs per task, on an RTX 4060 8 GB laptop with 16 GB RAM.
`FIRST_TURN_ALLOWANCE=180`: colibri reads the first prompt of a run cold (50-150 s
here) and every later turn reads only what is new, so the 120 s bar applies to
the turns after the first. Not comparable one-to-one with the table below, which
had no allowance.

| Task | Score | Median secs (first turn) | Failure |
|---|---|---|---|
| 01-fix-panic | 0/3 | 260 (53) | fixes it, then keeps calling tools instead of stopping |
| 02-find-symbol | 3/3 | 65 | |
| 03-add-function | 3/3 | 113 (52) | |
| 04-git-history | 3/3 | 52 (48) | |
| 05-multi-file | 3/3 | 87 (48) | |
| 06-wrong-path | 2/3 | 223 (102) | one timeout |
| 07-multi-site | 0/3 | 300 | timeouts: four edit sites at ~5 tok/s |
| 08-large-file | 3/3 | 107 (61) | |
| 09-locate-wrong-path | 1/3 | 111 (101) | answers in one turn from the pre-search, at the wrong place |
| 10-locate-real-shape | 0/3 | 87 | same |
| **total** | **18/30** | | |

06 is the rerun with the leaked-call retry (it was 0/3 in the suite run, every
run ending on a tool call written as text); the other rows are the suite run.
Later the same day, with a run that says it is done after a passing check ending
there, 01 scored 3/3 (160 s median, first turn 62 s). With colibri's resume
from the shared prompt ([#2537](https://github.com/JustVugg/colibri/pull/2537)),
the first turn of the second and third runs of a task fell from 52-99 s to 2-3 s.
That morning the same model and laptop scored 0: froe re-sent a rewritten
history every turn, and colibri read the whole prompt again each time. What
changed it, in `docs/ROADMAP.md` ("colibri as the default runtime"): an
append-only history under `prefix_reuse`, a colibri fix for a one-newline
mismatch in tool-call turns
([JustVugg/colibri#2535](https://github.com/JustVugg/colibri/pull/2535)), and
`COLI_PARK=0`.

- **Speed is the limit on this laptop, not reuse.** Generation is ~5 tok/s:
  most experts do not fit in 8 GB of VRAM and run on the CPU. 07 runs out of
  time; a machine with more VRAM and RAM is a different picture.
- **01 and 09-10 are the model's behaviour**, not the runtime's.

### colibri, second full run (2026-11-01, evening)

Same model and laptop, with everything from the first run's notes in place:
the leaked-call retry, a done claim after a passing check ending the run, and
colibri with both fixes (#2535, #2537) and `COLI_PARK=0`. 55m39s.

| Task | Score | Median secs (first turn) | Failure |
|---|---|---|---|
| 01-fix-panic | 2/3 | 129 (3) | one too-slow |
| 02-find-symbol | 3/3 | 1 | |
| 03-add-function | 1/3 | 155 (50) | two too-slow (12-15 turns) |
| 04-git-history | 3/3 | 7 (2) | |
| 05-multi-file | 2/3 | 122 (51) | one too-slow (16 turns) |
| 06-wrong-path | 2/3 | 120 (3) | one wrong result |
| 07-multi-site | 0/3 | 300 | timeouts |
| 08-large-file | 3/3 | 56 (5) | |
| 09-locate-wrong-path | 2/3 | 46 (8) | one wrong result |
| 10-locate-real-shape | 0/3 | 37 | answers in one turn, at the wrong place |
| **total** | **18/30** | | |

The same total as the first run, but a different 18 and faster: the second and
third runs of a task start in 1-8 s (the first run's first turn is still 50-135
s cold), and the suite took 56 minutes instead of 64. Four of the twelve
failures are too-slow by 2-33 s on runs of 12-16 turns: at ~5 tokens a second,
the bar decides them, and which runs land under it varies.

### Ollama (2026-09-25)

8K context, 3 runs per task, 120 s bar, on an RTX 4060 8 GB laptop with 15 GB
RAM, with the froe build of that day.

| Model | Runtime | Score | Suite time |
|---|---|---|---|
| ministral-3 8B (Q4_K_M) | Ollama | 19/30 | 6m24s |

- **07-multi-site failed.**
- **The runtime matters as much as the model.** On Ollama, the 8B loses runs because Ollama's tool-call parser rejects raw tab characters inside the model's Go code and answers HTTP 500. Switching froe to its text tool-call protocol made it worse (8/30), because Ollama still intercepts the calls.
