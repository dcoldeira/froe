#!/usr/bin/env bash
# The definition, not a caller. robustness.py and the test both use
# witness_value, so a model that answers from the first grep hit can land on
# either of them.
# Grade the MODEL'S answer only: the text of the last turn before froe's
# LAST stats line - locate may ask a second time, and the second answer is
# the one that stands. froe appends its own context after that (AROUND THOSE LINES, ALSO
# MATCHING) with real file:line citations in it, and grading the whole output
# let those pass for the model's. Measured 2026-09-24: qwen3-nothink:8b cited
# only the header in 09 and "passed" on a widths line froe printed itself.
answer=$(mktemp)
awk '/^── turn /{buf=""; next} /^ +[0-9]+ turns · /{ans=buf; next} {buf=buf $0 "\n"} END{printf "%s", ans}' \
  "${1:-/dev/null}" > "$answer"
grep -q "src/qrl/causal/witness.py" "$answer" || exit 1
