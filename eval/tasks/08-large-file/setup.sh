#!/usr/bin/env bash
# A file far larger than one read can return. read_file clamps a result to
# ctx/4 (~2048 tokens, about 158 lines at 8192), so the agent MUST narrow with
# grep or offset/limit rather than reading the whole thing. The shape is
# qrl-public's own src/qrl/causal.py, which is ~2800 lines.
set -e
{
  echo '"""Sampling pipeline: 1400 post-processing stages around one constant."""'
  echo
  for i in $(seq 1 700); do
    echo
    echo "def stage_$i(v):"
    echo "    \"\"\"Stage $i of the sampling pipeline.\"\"\""
    echo "    return v + $i"
    echo
  done
  echo
  echo '# DEFAULT_SHOTS is the value to change. It is deep in the file on purpose:'
  echo '# a single unbounded read cannot reach it.'
  echo 'DEFAULT_SHOTS = 1000'
  echo
  for i in $(seq 701 1400); do
    echo
    echo "def stage_$i(v):"
    echo "    \"\"\"Stage $i of the sampling pipeline.\"\"\""
    echo "    return v - $i"
    echo
  done
  echo
  echo 'if __name__ == "__main__":'
  echo '    print(DEFAULT_SHOTS, stage_1(0), stage_1400(0))'
} > sampling_pipeline.py
git add -A
git -c user.email=e@e -c user.name=e commit -qm "add pipeline"
