#!/usr/bin/env bash
F=src/qrl/reporting/witness_report.py

# Found the real file despite the wrong path in the prompt, and removed the column.
grep -q "Causal Order" "$F" && exit 1
# Removed the matching width too - the import-time guard catches a partial edit.
python3 "$F" 2>&1 || exit 1
# Removing the whole table is not a fix.
grep -q '"Robustness"' "$F" || exit 1
grep -q '"P_win"' "$F" || exit 1
# Did not touch the lookalike in a different table.
grep -q "Causal Ordering" src/qrl/reporting/dag_report.py || exit 1
exit 0
