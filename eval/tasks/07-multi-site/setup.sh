#!/usr/bin/env bash
# One file, one logical change, FOUR edit sites spread far apart. Padding keeps
# the sites out of a single read window so the agent must hold state across
# turns.
set -e
{
  echo '"""Quantum switch results table."""'
  echo
  echo '# ── site 1 of 4: the registered field list ──'
  echo 'REGISTERED = ('
  echo '    "Process", "Witness Value", "Causal Order", "P_win",'
  echo ')'
  echo
  for i in $(seq 1 90); do
    echo
    echo "# filler $i: keeps the four sites out of one read window"
    echo "def filler_$i():"
    echo "    return $i"
    echo
  done
  echo
  echo '# ── site 2 of 4: the printed headers ──'
  echo 'HEADERS = ('
  echo '    "Process", "Witness\nValue", "Causal\nOrder", "P_win",'
  echo ')'
  echo
  echo '# ── site 3 of 4: one width per header, positional ──'
  echo 'WIDTHS = (24, 20, 18, 12)'
  echo
  for i in $(seq 91 180); do
    echo
    echo "# filler $i"
    echo "def filler_$i():"
    echo "    return $i"
    echo
  done
  echo
  echo '# ── site 4 of 4: the row builder ──'
  echo 'def row():'
  echo '    return ['
  echo '        "quantum switch",'
  echo '        witness_value(),'
  echo '        causal_order(),'
  echo '        p_win(),'
  echo '    ]'
  echo
  echo
  echo 'def witness_value():'
  echo '    return "-0.1036"'
  echo
  echo
  echo 'def causal_order():'
  echo '    return "indefinite"'
  echo
  echo
  echo 'def p_win():'
  echo '    return "0.8536"'
  echo
  echo
  echo '# Import-time guard: every site must agree, so a partial edit fails the'
  echo '# moment the module loads rather than when a table is drawn.'
  echo 'assert len(REGISTERED) == len(HEADERS) == len(WIDTHS) == len(row())'
} > switch_table.py
git add -A
git -c user.email=e@e -c user.name=e commit -qm "add switch table"
