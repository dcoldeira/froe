#!/usr/bin/env bash
# locate's job is an ANSWER, not an edit, so this reads the output rather than
# the tree - except for the one thing about the tree that must stay true.
#
# The bar is what a user would have to be told in order to do the edit
# themselves: which file, which two places, and that they move together. The
# widths line is deliberately in scope - a header removed without its width is
# the partial edit the import-time guard in this fixture exists to catch.
#
# A citation is accepted anywhere inside the STATEMENT that holds the thing,
# not only on its exact line: any line of the headers tuple lands the reader in
# the right place. Naming the symbol without any line still fails.
#
# Ground truth is computed from the fixture rather than written down, so a
# change to setup.sh cannot silently leave this checking the wrong lines.
# Grade the MODEL'S answer only: the last turn's text, up to froe's stats
# line. froe appends its own context after that (AROUND THOSE LINES, ALSO
# MATCHING) with real file:line citations in it, and grading the whole output
# let those pass for the model's. Measured 2026-09-24: qwen3-nothink:8b cited
# only the header in 09 and "passed" on a widths line froe printed itself.
answer=$(mktemp)
awk '/^── turn /{buf=""; next} /^ +[0-9]+ turns · /{printf "%s", buf; exit} {buf=buf $0 "\n"}' \
  "${1:-/dev/null}" > "$answer"
# ...plus froe's own LINE NUMBERS CORRECTED section, which is not context but a
# checked claim: the code the model quoted beside a citation, found on exactly
# one other line of that file. Measured 2026-09-24, bonsai-27b named the widths
# coupling in 2 of 3 runs of 09 and cited it two lines out each time.
awk '/^LINE NUMBERS CORRECTED/{f=1; next} f && /^[A-Z]/{f=0} f' "${1:-/dev/null}" >> "$answer"
out="$answer"
F=src/qrl/reporting/witness_report.py
fail() { echo "MISS: $1" >&2; exit 1; }

line_of() { grep -nF -- "$1" "$F" | head -1 | cut -d: -f1; }
open=$(line_of 'WITNESS_SUMMARY_HEADERS = (')
close=$(( $(line_of '"Causal Order"') + 1 ))
widths=$(line_of 'WITNESS_SUMMARY_WIDTHS = (')
[ -n "$open" ] && [ -n "$widths" ] || fail "fixture changed shape - cannot compute ground truth"

cites() { # cites <first-line> <last-line>
  local n
  for (( n = $1; n <= $2; n++ )); do
    grep -qE "$F:$n\b" "$out" && return 0
  done
  return 1
}

# 1. Found the real file despite the prompt naming one that does not exist.
grep -q "$F" "$out" || fail "never named $F"

# 2. Cited the headers tuple by line.
cites "$open" "$close" || fail "did not cite the Causal Order header ($open-$close)"

# 3. Cited the widths line too - the coupling, and the half that gets forgotten.
cites $(( widths - 1 )) "$widths" || fail "missed the width coupling (:$widths)"

# 4. Did not present the prompt's invented path as a real place.
grep -qE "src/qrl/witness/witness_report\.py:[0-9]+" "$out" && fail "cited the phantom path with a line number"

# 5. Did not cite the lookalike: a different table with a similarly named field.
grep -qE "src/qrl/reporting/dag_report\.py:[0-9]+" "$out" && fail "cited the lookalike table as a place"

# 6. Read-only is structural, so nothing may have changed on disk.
git diff --quiet || fail "locate modified the working tree"

exit 0
