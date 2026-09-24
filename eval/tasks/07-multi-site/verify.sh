#!/usr/bin/env bash
F=switch_table.py
python3 "$F" 2>&1 || exit 1
grep -q "Causal" "$F" && exit 1
grep -q "causal_order" "$F" && exit 1
# The other three columns must survive - deleting the table is not a fix.
grep -q '"Witness Value"' "$F" || exit 1
grep -q '"P_win"' "$F" || exit 1
grep -q "p_win()" "$F" || exit 1
exit 0
