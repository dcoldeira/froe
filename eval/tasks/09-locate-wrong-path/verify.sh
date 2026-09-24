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
out="${1:-/dev/null}"
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

# 5. Read-only is structural, so nothing may have changed on disk.
git diff --quiet || fail "locate modified the working tree"

exit 0
