#!/usr/bin/env bash
# The hard shape that 09-locate-wrong-path does not reproduce. 09 is 140 files
# with one plausible target. Three things make this one harder:
#
#   1. DECOYS THAT MATCH. Four files match **/witness* and are not the target,
#      and each contains a causal-order symbol. A decoy that contains nothing
#      is not a decoy.
#   2. A TARGET TOO BIG TO READ. The report is ~1900 lines, so a model that
#      reads it gets a truncated head and has to search or page to find the
#      table rather than guess line numbers.
#   3. SCALE. 400 filler modules, so the project map cannot list them and the
#      model has to search rather than read the map.
set -e

# Filler, enough that the map cannot hold the tree.
for area in physics categorical mbqc lang sensing chemistry biology backends; do
  for n in $(seq 1 50); do
    d="src/qrl/$area/mod$n"
    mkdir -p "$d"
    cat > "$d/processor.py" <<PY
"""Filler module so the project map has real work to do."""


def process(rows):
    return [r for r in rows if r]


def summarise(rows):
    return {"area": "$area", "module": $n, "count": len(process(rows))}
PY
  done
done

# Decoys: they match **/witness*, and they CONTAIN a causal-order term.
mkdir -p src/qrl/backends/witness
for stage in sdp export fit; do
  cat > "src/qrl/backends/witness/witness_${stage}.py" <<PY
"""Witness $stage backend. NOT the report."""

COLUMNS = {
    "Process": "process",
    "Value": "witness_value",
    "CausalOrder": "causal_order",
    "Dim": "dimension",
}


def convert(rows):
    return [{COLUMNS.get(k, k): v for k, v in r.items()} for r in rows]
PY
done

cat > witness_endpoints.py <<'PY'
"""Witness HTTP endpoints. Routing only - no report layout here."""

from flask import Blueprint

witness_bp = Blueprint("witness", __name__)


@witness_bp.route("/api/witness/<int:run_id>")
def get_witness_run(run_id):
    return {"id": run_id, "causal_order": "indefinite"}
PY

# The real target, at a path the prompt gets WRONG, padded to ~1900 lines.
mkdir -p src/qrl/reporting
{
  cat <<'HEAD'
"""Causal-structure PDF report.

Page 1 carries the process, witness-summary and DAG tables.
"""

from reportlab.lib.units import mm


def _s(value, fmt=None):
    if value is None:
        return ""
    return fmt % value if fmt else str(value)


def register_tables(pdf):
    """Declare every table on the report, in page order."""
    pdf.register_table('Process Details', fields=[
        'Process', 'Parties', 'Dimension', 'Valid (Tr W = d_out)',
    ])
    pdf.register_table('Witness Summary', fields=[
        'Process', 'Witness Value', 'Robustness',
        'Causal Order', 'P_win',
    ])
    pdf.register_table('DAG Details', fields=[
        'Node', 'Parents', 'Markov Condition', 'Causal Ordering',
    ])
HEAD

  # Padding before the target: far enough in that a model reading the head
  # misses it, not so far that the file is simply "the thing at the bottom".
  for i in $(seq 1 24); do
    cat <<PY

def add_filler_table_${i}(pdf, data, runs):
    """Filler table ${i}, page $(( i / 8 + 2 ))."""
    tbl(
        "Filler ${i}",
        headers=[
            'Run', 'Reading A', 'Reading B', 'Reading C',
        ],
        col_widths=[12, 46, 46, 46],
        rows=[
            [
                f"Run {n+1}",
                _s(data.get('reading_a_${i}')),
                _s(data.get('reading_b_${i}')),
                _s(data.get('reading_c_${i}')),
            ]
            for n, run in enumerate(runs)
        ],
    )
PY
  done

  cat <<'MIDDLE'

def add_witness_summary_table(pdf, data, runs):
    """Page 1, table 2. Headers, widths and rows are POSITIONAL: the Nth
    header is drawn at the Nth width and filled from the Nth row entry."""
    tbl(
        "Witness Summary",
        headers=[
            'Process', 'Witness\nValue', 'Robustness\n(r*)',
            'Causal\nOrder', 'P_win',
        ],
        col_widths=[24, 40, 36, 30, 30],
        rows=[
            [
                _s(run.get('process_name')),
                _s(run.get('witness_value'), '%.4f'),
                _s(run.get('robustness'), '%.4f'),
                _s(data.get('causal_order', 'indefinite')),
                _s(run.get('p_win'), '%.4f'),
            ]
            for n, run in enumerate(runs)
        ],
    )


def add_dag_table(pdf, data, runs):
    """Page 1, table 3. A DIFFERENT table with a SIMILARLY NAMED field."""
    tbl(
        "DAG Details",
        headers=[
            'Node', 'Parents', 'Markov\nCondition',
            'Causal\nOrdering',
        ],
        col_widths=[24, 46, 46, 46],
        rows=[
            [
                _s(node.get('name')),
                _s(node.get('parents')),
                _s(node.get('markov_ok')),
                _s(data.get('dag_ordering')),
            ]
            for node in data.get('nodes', [])
        ],
    )
MIDDLE

  # Padding after the target.
  for i in $(seq 25 95); do
    cat <<PY

def add_filler_table_${i}(pdf, data, runs):
    """Filler table ${i}, page $(( i / 8 + 2 ))."""
    tbl(
        "Filler ${i}",
        headers=[
            'Run', 'Reading A', 'Reading B', 'Reading C',
        ],
        col_widths=[12, 46, 46, 46],
        rows=[
            [
                f"Run {n+1}",
                _s(data.get('reading_a_${i}')),
                _s(data.get('reading_b_${i}')),
                _s(data.get('reading_c_${i}')),
            ]
            for n, run in enumerate(runs)
        ],
    )
PY
  done
} > src/qrl/reporting/witness_pdf_report.py

git add -A
git -c user.email=e@e -c user.name=e commit -qm "add qrl tree"
