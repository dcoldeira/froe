# Testing froe

Two layers: Go unit tests, which are fast, deterministic and need no model, and
the eval harness, which is slow and runs real models on real tasks.

## Unit tests

```bash
go vet ./... && go test ./...
```

No model, runtime or network is needed. Agent-loop tests drive a scripted fake
provider (`internal/agent/verify_test.go`), and provider tests replay recorded
SSE frames from an `httptest` server (`internal/provider/openaicompat_test.go`).

### The rule: a fix ships with a test that fails without it

Every behaviour fix is committed with a test, and that test is checked by
switching the fix off: stub the new condition to `false` (or the function to
return early), run the tests, and confirm the new test fails. A test that
passes either way pins nothing.

Examples of what this has caught:

- The first draft of the leaked-markup test (`internal/tools/markup_test.go`)
  asserted on a quoted substring that `%q` escapes. It failed for the wrong
  reason; the switched-off run showed it.
- Before its own test file existed, the bash denylist was untested.
  `internal/tools/bash_test.go` now pins every entry.

### Where the tests cluster

| Area | Files | What they pin |
|---|---|---|
| Agent loop | `internal/agent/*_test.go` | loop detection (identical, reworded and fruitless calls); context fitting; the verify, apply and leftover pushes; retries after a backend parse failure; note placement for strict chat templates; the result cache (stale and evicted reads); react and grammar parsing; tool-result clamping |
| Provider adapters | `internal/provider/*_test.go` | inline `<think>` separation; streamed tool-call reassembly (including Ollama's parallel calls at one index), request encoding, error typing |
| Tools | `internal/tools/*_test.go` | the project-root boundary, `read_file` windows, `glob`, `git_log`, `edit_file` mismatch diagnostics, leaked-markup refusal, the bash denylist, word-boundary grep |
| Model choice | `internal/resolve/resolve_test.go` | role order, the `default` role, skipping models a runtime does not have |
| Registry | `internal/registry/registry_test.go` | the embedded catalogue loads; runtime ids, roles, peak-memory estimates |
| Repo map | `internal/repo/repo_test.go` | symbol extraction, the context budget split, instruction loading and truncation notices, ranking |
| `locate` | `cmd/froe/locate_*_test.go` | parsing WHERE, the completeness sweep, evidence before turn 1, citations not in the tree, surroundings of unread citations |
| Commands | `cmd/froe/{commit,buildctx}_test.go` | commit-message cleaning and diff truncation; the context window a model is actually loaded with |

## Eval harness

See [`eval/README.md`](eval/README.md): ten tasks, 3 runs each, a 120 s bar per
run, and a failure classification. Use it for anything a unit test cannot
settle, which is whether a model actually does better.

### Before blaming the model, read the transcript

Each run's full output is kept (`.froe-output.txt` in its sandbox). Several
failures that looked like model limits were froe bugs:

| Seen | Looked like | Was |
|---|---|---|
| 2026-09-24, Bonsai 06 regression | model got worse | reading a file after editing it returned a stale "unchanged" |
| 2026-09-24, Bonsai 07 loops | model repeating itself | a result dropped to fit the context was answered "use what you already have" |
| 2026-09-25, Ministral on LM Studio 4/30 | model can't use tools | froe sent a user message straight after a tool result, which Mistral's chat template refuses |
| 2026-09-25, Ministral 05 on Ollama | malformed tool call | froe fused two parallel calls sent at the same index into one |

### Harness hygiene

- **Measure one model at a time.** Unload the others first (`lms unload --all`,
  or Ollama `keep_alive: 0`). A second model on the GPU distorts timings.
- **Compare models on the same froe build.** A fix made after one model's run
  makes the comparison meaningless until the other is re-run.
- **Account for the runtime.** Ollama loads models at a 4096 context unless
  told otherwise and silently truncates past it. Its tool-call parser also
  fails on output that LM Studio passes through.
- **Don't pattern-kill.** `pkill -f` patterns match the shell that runs them.
  Match the command's first word instead, e.g.
  `awk '$2=="bash" && $3=="eval/run.sh"'`.
