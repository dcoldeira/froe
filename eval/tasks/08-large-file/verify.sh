#!/usr/bin/env bash
F=sampling_pipeline.py
python3 "$F" >/dev/null 2>&1 || exit 1
grep -q "^DEFAULT_SHOTS = 4096$" "$F" || exit 1
# Nothing else touched: all 1400 stage functions still present, and the diff
# against the fixture is exactly the one line. Compared to the fixture commit
# by name, so a run that commits its own work is judged the same as one that
# does not.
[ "$(grep -c '^def stage_' "$F")" = "1400" ] || exit 1
base=$(git log --format=%H --grep='^add pipeline$' | tail -1)
[ -n "$base" ] || exit 1
[ "$(git diff --numstat "$base" -- "$F" | cut -f1,2)" = "1	1" ] || exit 1
exit 0
