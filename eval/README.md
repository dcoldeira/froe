# froe evaluation harness

`eval/run.sh` runs `froe` against ten small tasks, each in a throwaway git repo,
and grades each run by a mechanical check: code compiles, a test passes, an
answer contains a required string. No human judgement is involved, so results
compare across models and across changes to froe itself.

```bash
MODEL=ministral-3-8b-lmstudio bash eval/run.sh          # full suite, 3 runs per task
MODEL=bonsai-27b-lmstudio ONLY=07-multi-site bash eval/run.sh
```

| Variable | Default | Meaning |
|---|---|---|
| `MODEL` | froe's own pick | registry id to run |
| `REPEATS` | `3` | runs per task; the score is a rate |
| `ONLY` | all | run one task by directory name |
| `TIMEOUT` | `120` | seconds per run; a run past it is a FAIL |
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
| `timeout` | still running at `TIMEOUT` |
| `context-overflow` | the runtime refused a prompt larger than its context |
| `runtime-error` | the runtime answered HTTP 5xx and froe stopped |
| `stuck-failing` / `stuck-fruitless` / `stuck-no-progress` | froe's loop detector ended the run |
| `max-turns` | the turn budget ran out |
| `withdrawn-citation` | froe caught a cited path that is not in the tree |
| `wrong-result` | the run finished and the check failed |

## Latest results

2026-09-25. Same froe build for every row, 8K context, 3 runs per task, 120 s
bar, on an RTX 4060 8 GB laptop with 15 GB RAM.

| Model | Runtime | Score | Suite time |
|---|---|---|---|
| bonsai-27b (Q1_0) | LM Studio | 24/30 | 23m19s |
| **ministral-3 8B (Q4_K_M)** | **LM Studio** | **23/30** | **6m39s** |
| ministral-3 14B (Q4_K_M) | LM Studio | 21/30 | 19m56s |
| ministral-3 3B (Q4_K_M) | LM Studio | 19/30 | 3m39s |
| ministral-3 8B (Q4_K_M) | Ollama | 19/30 | 6m24s |

- **07-multi-site fails for every model.**
- **Ministral 8B is froe's default.** It matches Bonsai within one run at 3.5× the speed.
- **The 14B spills past 8 GB of VRAM** and times out on 03.
- **The runtime matters as much as the model.** On Ollama, the same 8B weights lose runs because Ollama's tool-call parser rejects raw tab characters inside the model's Go code and answers HTTP 500. Switching froe to its text tool-call protocol made it worse (8/30), because Ollama still intercepts the calls.
