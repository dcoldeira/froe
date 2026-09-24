#!/usr/bin/env bash
# froe evaluation harness.
#
# Every task has an objectively checkable outcome — code compiles, a test
# passes, an answer contains a required string. No human judgement, so results
# are comparable across models and across changes to froe itself.
set -u

FROE="${FROE:-froe}"
MODEL="${MODEL:-}"
# -yolo by default: each task runs in a throwaway git repo, and -accept-edits
# still prompts for bash, which in a harness with no TTY means every `go test`
# is denied and the model burns its turns retrying.
MODE="${MODE:--yolo}"
# Matches agent.DefaultMaxTurns. It was 10, which meant the harness measured a
# tighter budget than real use and could not see a loop that needed 15 turns
# to show itself.
TURNS="${TURNS:-25}"
# A single run of a stochastic process cannot detect a change worth one task.
# Measured 2026-09-15: two runs of the same task with the same config produced
# materially different patches, and the better one came from the OLDER build.
# So every task runs REPEATS times and the score is a rate, not a verdict.
REPEATS="${REPEATS:-3}"
# ONLY=06-wrong-path runs a single task. A full suite at this hardware's speeds
# is roughly an hour, which is too slow a loop to iterate against.
ONLY="${ONLY:-}"
# A run that has not finished in TIMEOUT seconds is a FAIL, whatever the tree
# looks like when it is cut off: a model that needs longer than this for tasks
# this size is not usable interactively, so waiting it out measures nothing
# worth knowing. It was a fixed 900s, which let one slow model hold a suite for
# hours. 120s is the bar a model has to clear to be worth using at all - a
# capable one does these tasks in 1-20s (qwen3-nothink:8b, 2026-09-24).
TIMEOUT="${TIMEOUT:-120}"
WORK="$(mktemp -d)"
RESULTS="$WORK/results.tsv"
TASKS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/tasks" && pwd)"

model_arg=()
[ -n "$MODEL" ] && model_arg=(-model "$MODEL")

printf 'task\tpassed\trate\tsecs(med)\tturns(med)\tfailure modes\n' > "$RESULTS"

# classify turns a run's output into WHY it failed, so a change that converts
# context overflows into clean stuck-aborts is visible as progress rather than
# showing as the same pass count.
classify() {
  local out="$1"
  if grep -q '^froe-eval: timed out' "$out";              then echo timeout
  elif grep -q 'exceeds the available context size' "$out"; then echo context-overflow
  elif grep -q 'stuck:.*kept failing' "$out";              then echo stuck-failing
  elif grep -q 'stuck:.*found nothing' "$out";             then echo stuck-fruitless
  elif grep -q 'stuck:' "$out";                            then echo stuck-no-progress
  elif grep -q 'stopped after .* turns' "$out";            then echo max-turns
  # The model answered, and froe caught a citation in that answer pointing at a
  # path not in the tree. Separated from wrong-result because they are opposite
  # outcomes for a user: one sends them to a file that is not there, the other
  # tells them so before they go. Measured 2026-09-17, mistral-medium hit this
  # on 3/3 runs of 10-locate-real-shape while finding every real site.
  #
  # It says a withdrawal happened, NOT that the withdrawal is the whole reason
  # the run failed - a run can both withdraw a citation and miss the target. It
  # sits last before the fallback so the harder failures above still win.
  elif grep -q 'NOT IN THIS TREE' "$out";                  then echo withdrawn-citation
  else echo wrong-result
  fi
}

median() { sort -n | awk '{a[NR]=$1} END{ if(NR==0){print 0} else {print a[int((NR+1)/2)]} }'; }

total_passed=0
total_runs=0
suite_start=$(date +%s)

for task_dir in "$TASKS_DIR"/*/; do
  name="$(basename "$task_dir")"
  [ -n "$ONLY" ] && [ "$name" != "$ONLY" ] && continue
  prompt="$(cat "$task_dir/prompt.txt")"
  # A task may name the command it exercises. The default is the agent loop;
  # `locate` and the stepwise commands that follow it are a different shape -
  # read-only, their own turn budget, judged on the ANSWER rather than on the
  # tree - so the harness must be able to run them as themselves rather than
  # approximating them with `do`.
  cmd="do"
  [ -f "$task_dir/cmd" ] && cmd="$(tr -d '[:space:]' < "$task_dir/cmd")"
  echo "── $name ($cmd)"

  passed=0
  secs_all=""; turns_all=""; modes=""

  for run in $(seq 1 "$REPEATS"); do
    sandbox="$WORK/$name/run$run"
    mkdir -p "$sandbox"
    cp -r "$task_dir"/repo/. "$sandbox/" 2>/dev/null || true

    ( cd "$sandbox" && git init -q 2>/dev/null \
      && git add -A 2>/dev/null \
      && git -c user.email=e@e -c user.name=e commit -qm "initial commit" >/dev/null 2>&1 )

    # Optional per-task extra setup: history, or a generated file tree too big
    # to keep in git.
    [ -f "$task_dir/setup.sh" ] && ( cd "$sandbox" && bash "$task_dir/setup.sh" >/dev/null 2>&1 )

    # A stepwise command carries its own turn budget on purpose - locate's 6 is
    # a measured design decision, not a default to override - and has no
    # permission mode to set, because its registry holds no mutating tool.
    if [ "$cmd" = "do" ]; then
      argv=( do "${model_arg[@]}" $MODE -max-turns "$TURNS" )
    else
      argv=( "$cmd" "${model_arg[@]}" )
    fi

    start=$(date +%s)
    out="$( cd "$sandbox" && timeout "$TIMEOUT" "$FROE" "${argv[@]}" "$prompt" 2>&1 )"
    rc=$?
    secs=$(( $(date +%s) - start ))
    [ "$rc" = 124 ] && out="$out"$'\n'"froe-eval: timed out after ${TIMEOUT}s"

    turns=$(printf '%s' "$out" | grep -oE '[0-9]+ turns' | tail -1 | grep -oE '[0-9]+' || echo 0)
    printf '%s' "$out" > "$sandbox/.froe-output.txt"

    if [ "$rc" != 124 ] && ( cd "$sandbox" && bash "$task_dir/verify.sh" "$sandbox/.froe-output.txt" >/dev/null 2>&1 ); then
      result=PASS
      passed=$(( passed + 1 ))
    else
      result=FAIL
      modes="$modes $(classify "$sandbox/.froe-output.txt")"
    fi

    secs_all="$secs_all$secs"$'\n'
    turns_all="$turns_all$turns"$'\n'
    echo "   run $run/$REPEATS  $result  ${secs}s  ${turns} turns"
  done

  med_secs=$(printf '%s' "$secs_all" | grep -v '^$' | median)
  med_turns=$(printf '%s' "$turns_all" | grep -v '^$' | median)
  mode_summary=$(printf '%s' "$modes" | tr ' ' '\n' | grep -v '^$' | sort | uniq -c \
                 | awk '{printf "%sx%s ", $1, $2}')
  [ -z "$mode_summary" ] && mode_summary="-"

  total_passed=$(( total_passed + passed ))
  total_runs=$(( total_runs + REPEATS ))

  echo "   => $passed/$REPEATS"
  printf '%s\t%s/%s\t%s%%\t%s\t%s\t%s\n' \
    "$name" "$passed" "$REPEATS" "$(( passed * 100 / REPEATS ))" \
    "$med_secs" "$med_turns" "$mode_summary" >> "$RESULTS"
done

echo
echo "════════════════════════════════════════════════════"
column -t -s$'\t' "$RESULTS"
echo "════════════════════════════════════════════════════"
echo "  ${total_passed}/${total_runs} runs passed   (${REPEATS} repeats per task)"
echo "  model: ${MODEL:-auto}   mode: $MODE   max-turns: $TURNS   timeout: ${TIMEOUT}s"
wall=$(( $(date +%s) - suite_start ))
printf '  wall time: %dm%02ds   (started %s)\n' $(( wall / 60 )) $(( wall % 60 )) "$(date -d @"$suite_start" '+%F %T')"
echo "  artefacts: $WORK"
echo
echo "  A pass RATE is the number to compare. A single run proves nothing:"
echo "  measured 2026-09-15, two runs of one task at the same config differed"
echo "  more than three real bug fixes did."
