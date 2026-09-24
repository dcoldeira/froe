#!/usr/bin/env bash
# The definition, not a caller. robustness.py and the test both use
# witness_value, so a model that answers from the first grep hit can land on
# either of them.
grep -q "src/qrl/causal/witness.py" "$1" || exit 1
