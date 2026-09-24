#!/usr/bin/env bash
# The bar is what a user needs in order to make the edit themselves: the right
# file, the header site, and the half that gets forgotten - the positional
# width or the row value that fills it. A header removed without both leaves a
# table with more cells than columns.
#
# Ground truth is computed from the fixture here rather than written down by
# setup.sh, so nothing the model can read contains the answer.
#
# Line numbers are accepted within a tolerance because a citation anywhere
# inside the list literal lands the reader in the right place; what is NOT
# tolerated is the failure this task exists to catch - citing a decoy file, or
# the path the report invented.
# Grade the MODEL'S answer only: the text of the last turn before froe's
# LAST stats line - locate may ask a second time, and the second answer is
# the one that stands. froe appends its own context after that (AROUND THOSE LINES, ALSO
# MATCHING) with real file:line citations in it, and grading the whole output
# let those pass for the model's. Measured 2026-09-24: qwen3-nothink:8b cited
# only the header in 09 and "passed" on a widths line froe printed itself.
answer=$(mktemp)
awk '/^── turn /{buf=""; next} /^ +[0-9]+ turns · /{ans=buf; next} {buf=buf $0 "\n"} END{printf "%s", ans}' \
  "${1:-/dev/null}" > "$answer"
# ...plus froe's own LINE NUMBERS CORRECTED section, which is not context but a
# checked claim: the code the model quoted beside a citation, found on exactly
# one other line of that file. Measured 2026-09-24, bonsai-27b named the widths
# coupling in 2 of 3 runs of 09 and cited it two lines out each time.
awk '/^LINE NUMBERS CORRECTED/{f=1; next} f && /^[A-Z]/{f=0} f' "${1:-/dev/null}" >> "$answer"
out="$answer"
# WHERE alone - the places the answer says to change. Naming a lookalike or a
# decoy under WATCH OUT is what the answer is asked to do; citing one under
# WHERE sends the user to edit it. With no WHERE heading the whole answer
# counts, as it does for froe's own checks.
where=$(mktemp)
awk '/^[[:space:]]*#*[[:space:]]*\**[[:space:]]*WHERE/{buf=""; on=1; seen=1; next}
     /^[[:space:]]*#*[[:space:]]*\**[[:space:]]*(WHAT IT IS|WATCH OUT)/{on=0}
     on{buf=buf $0 "\n"} END{printf "%s", buf}' "$answer" > "$where"
grep -qE '^[[:space:]]*#*[[:space:]]*\**[[:space:]]*WHERE' "$answer" || cp "$answer" "$where"
F=src/qrl/reporting/witness_pdf_report.py
fail() { echo "MISS: $1" >&2; exit 1; }

line_of() { grep -nF -- "$1" "$F" | head -1 | cut -d: -f1; }

hdr=$(line_of "'Causal\nOrder'")
widths=$(line_of 'col_widths=[24, 40')
row=$(line_of "data.get('causal_order'")
look=$(line_of "'Causal\nOrdering'")
[ -n "$hdr" ] && [ -n "$widths" ] && [ -n "$row" ] && [ -n "$look" ] || fail "fixture changed shape - cannot compute ground truth"

# The bands must not overlap, or one citation satisfies two criteria. The
# header sits on the last line of the headers list and col_widths is two lines
# below it, so the header band is hdr-1..hdr+1 and the width band is exact.
cites() { # cites <first-line> <last-line> [file, default the whole answer]
  local n in="${3:-$out}"
  for (( n = $1; n <= $2; n++ )); do
    grep -qE "$F:$n\b" "$in" && return 0
  done
  return 1
}

# 1. Found the real file despite the report naming one that does not exist.
grep -q "$F" "$out" || fail "never named $F"

# 2. Cited the header site: anywhere inside the headers list literal.
cites $(( hdr - 1 )) $(( hdr + 1 )) || fail "did not cite the Causal Order header near :$hdr"

# 3. Cited the coupling: the width the column is drawn at, or the row entry
#    that fills it. Either shows it understood the column is more than a label.
cites "$widths" "$widths" || cites $(( row - 2 )) $(( row + 2 )) \
  || fail "missed the width (:$widths) and the row value (:$row)"

# 4. Did not present the report's invented path as a real place.
grep -qE "src/qrl/witness/witness_pdf_report\.py:[0-9]+" "$out" && fail "cited the phantom path with a line number"

# 5. Did not cite a decoy. These match **/witness* and contain a causal-order
#    term. Naming one in WATCH OUT is fine; citing one as a place is not.
grep -qE "(backends/witness/witness_[a-z]+\.py|witness_endpoints\.py):[0-9]+" "$where" \
  && fail "cited a decoy file as a place"

# 6. Did not cite the lookalike: the DAG Details table's "Causal Ordering"
#    header, a different table in the same file. Measured 2026-09-24:
#    qwen3-nothink:8b listed it in WHERE as a third site to remove.
cites $(( look - 1 )) $(( look + 1 )) "$where" && fail "cited the DAG Details lookalike near :$look"

# 7. Read-only is structural, so nothing may have changed on disk.
git diff --quiet || fail "locate modified the working tree"

exit 0
