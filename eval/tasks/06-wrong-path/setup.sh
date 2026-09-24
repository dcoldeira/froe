#!/usr/bin/env bash
# Build a tree big enough that the project map cannot list every file, so the
# agent has to SEARCH rather than read the map. 140 filler modules is enough to
# blow the ~1700-token map budget at ctx 8192 while keeping the eval fast.
set -e

for area in physics categorical mbqc sensing chemistry; do
  for n in $(seq 1 28); do
    d="src/qrl/$area/mod$n"
    mkdir -p "$d"
    cat > "$d/summary.py" <<PY
"""Filler module so the project map has real work to do."""


def summary():
    return "$area/mod$n"
PY
  done
done

# The real target, at a path the prompt gets WRONG.
mkdir -p src/qrl/reporting
cat > src/qrl/reporting/witness_report.py <<'PY'
"""Witness Summary table for the causal-structure report."""

# Headers and widths are positional: the Nth header is drawn at the Nth
# width, so removing a column means removing its width too.
WITNESS_SUMMARY_HEADERS = (
    "Process", "Dimension", "Witness Value",
    "Robustness", "Causal Order", "P_win",
)

WITNESS_SUMMARY_WIDTHS = (24, 12, 20, 18, 18, 12)

# Import-time guard: a header with no width is a broken table, so a partial
# edit fails the moment the module loads rather than when a PDF is drawn.
assert len(WITNESS_SUMMARY_HEADERS) == len(WITNESS_SUMMARY_WIDTHS)
PY

# The lookalike that must NOT be touched: a different field, a different table.
cat > src/qrl/reporting/dag_report.py <<'PY'
"""DAG Details table. A DIFFERENT table with a SIMILARLY NAMED field."""

DAG_DETAILS_HEADERS = (
    "Node", "Parents", "Causal Ordering", "Markov Condition",
)
PY

git add -A
git -c user.email=e@e -c user.name=e commit -qm "add qrl tree"
