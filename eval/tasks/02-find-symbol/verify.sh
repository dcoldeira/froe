#!/usr/bin/env bash
# The definition, not a caller. robustness.py and the test both use
# witness_value, so a model that answers from the first grep hit can land on
# either of them.
# Grade the MODEL'S answer only: the last turn's text, up to froe's stats
# line. froe appends its own context after that (AROUND THOSE LINES, ALSO
# MATCHING) with real file:line citations in it, and grading the whole output
# let those pass for the model's. Measured 2026-09-24: qwen3-nothink:8b cited
# only the header in 09 and "passed" on a widths line froe printed itself.
answer=$(mktemp)
awk '/^── turn /{buf=""; next} /^ +[0-9]+ turns · /{printf "%s", buf; exit} {buf=buf $0 "\n"}' \
  "${1:-/dev/null}" > "$answer"
grep -q "src/qrl/causal/witness.py" "$answer" || exit 1
