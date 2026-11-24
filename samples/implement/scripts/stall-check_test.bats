#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/stall-check.sh"
  WORK="$BATS_TEST_TMPDIR/work"
  WS="$BATS_TEST_TMPDIR/ws"
  STATE="$WS/impl-progress.json"
  mkdir -p "$WORK/docs/plans" "$WS"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q -b main "$WORK"
  printf -- '- [ ] a\n' > "$WORK/docs/plans/a.md"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m init
}

# snap <unchecked> <checked> [preflight-json]: the run's variables after count-plan-steps.
snap() {
  jq -n --arg w "$WORK" --arg ws "$WS" --argjson u "$1" --argjson c "$2" \
    --argjson pre "${3:-null}" '
    {work_dir:$w, workspace:$ws, impl_iteration:0,
     "resolve-inputs":{inputs:{plan_paths:["docs/plans/a.md"]}},
     "count-plan-steps":{unchecked_steps:$u, checked_steps:$c}}
    + (if $pre == null then {} else {"plan-preflight":$pre} end)'
}

# pass <unchecked> <checked> [args...]: runs one pass of the check.
pass() {
  local u="$1" c="$2"
  shift 2
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$(snap "$u" "$c")" "$SCRIPT" "$@"
}

commit_plan() {
  printf '%b' "$1" > "$WORK/docs/plans/a.md"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m plan
}

field() { jq -c "$1" <<<"$output"; }

# --- Happy path

@test "a progressing loop never stalls" {
  pass 10 0
  pass 9 1
  pass 8 2
  pass 7 3
  [ "$status" -eq 0 ]
  [ "$(field '[.stalled,.passes_without_progress,.window,.unchecked_steps]')" = "[false,0,3,7]" ]
}

@test "three passes without progress stall the loop" {
  pass 16 4
  pass 16 4
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,1]" ]
  pass 16 4
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,2]" ]
  pass 16 4
  [ "$status" -eq 0 ]
  [ "$(field '[.stalled,.passes_without_progress,.window,.unchecked_steps,.checked_steps]')" = "[true,3,3,16,4]" ]
}

@test "the first pass is measured against plan-preflight" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ \
    "$(snap 16 4 '{"unchecked_steps":16,"checked_steps":4}')" "$SCRIPT"
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,1]" ]
}

@test "a first pass that ticked a box against plan-preflight is progress" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ \
    "$(snap 15 5 '{"unchecked_steps":16,"checked_steps":4}')" "$SCRIPT"
  [ "$(field .passes_without_progress)" = "0" ]
}

# --- Progress definition

@test "fewer unchecked steps is progress" {
  pass 16 4
  pass 16 4
  pass 15 4
  [ "$(field .passes_without_progress)" = "0" ]
}

@test "more checked steps is progress even when the plan grew" {
  pass 16 4
  pass 16 4
  pass 18 5
  [ "$(field .passes_without_progress)" = "0" ]
}

@test "a regained tick after a voided pass is not new progress" {
  pass 16 4
  pass 15 5
  pass 16 4
  pass 15 5
  [ "$(field .passes_without_progress)" = "2" ]
}

@test "more unchecked steps alone is not progress" {
  pass 16 4
  pass 17 4
  [ "$(field .passes_without_progress)" = "1" ]
}

# --- Window argument

@test "the window is a script argument" {
  pass 16 4 2
  pass 16 4 2
  pass 16 4 2
  [ "$(field '[.stalled,.passes_without_progress,.window]')" = "[true,2,2]" ]
}

@test "an empty window argument means the default 3" {
  pass 16 4 ""
  [ "$(field .window)" = "3" ]
}

@test "a non-numeric window is an error" {
  pass 16 4 zero
  [ "$status" -ne 0 ]
}

@test "a zero window is an error" {
  pass 16 4 0
  [ "$status" -ne 0 ]
}

# --- Fail safe

@test "a corrupt history restarts the count and is never stalled" {
  pass 16 4
  pass 16 4
  pass 16 4
  printf 'not json{' > "$STATE"
  pass 16 4 1
  [ "$status" -eq 0 ]
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,0]" ]
  jq -e . "$STATE" >/dev/null
}

@test "a history of the wrong shape restarts the count" {
  printf '{"best_unchecked":"x"}' > "$STATE"
  pass 16 4 1
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,0]" ]
}

@test "missing counts never stall and leave the history alone" {
  pass 16 4
  pass 16 4
  pass 16 4
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ \
    "$(jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws}')" "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(field .stalled)" = "false" ]
  [ "$(jq -c '.passes_without_progress' "$STATE")" = "2" ]
}

@test "an unwritable workspace still answers and is not stalled" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" 1' _ \
    "$(snap 16 4 '{"unchecked_steps":16,"checked_steps":4}' | jq '.workspace = "/nonexistent/ws"')" "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,1]" ]
}

@test "an unset workspace still answers and is not stalled" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" 1' _ \
    "$(snap 16 4 '{"unchecked_steps":16,"checked_steps":4}' | jq 'del(.workspace)')" "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(field '[.stalled,.passes_without_progress]')" = "[false,1]" ]
}

@test "the history is kept short" {
  for _ in $(seq 1 30); do pass 16 4 100; done
  [ "$(jq '.history | length' "$STATE")" -le 20 ]
  [ "$(jq -c '.history[-1] | [.unchecked_steps,.checked_steps]' "$STATE")" = "[16,4]" ]
}

# --- Blocked tasks

@test "an unresolved BLOCKED note names its plan, task and step" {
  commit_plan '# Plan\n\n### Task 1: one\n- [x] done\n\n### Task 3: sas\n- [ ] **Step 2: wire it**\n  > BLOCKED: no SAS producer exists\n- [ ] Step 3\n'
  pass 16 4
  [ "$(field '.blocked_tasks | length')" = "1" ]
  [ "$(field '.blocked_tasks[0].plan')" = '"docs/plans/a.md"' ]
  [ "$(field '.blocked_tasks[0].task')" = '"Task 3: sas"' ]
  [ "$(field '.blocked_tasks[0].step')" = '"**Step 2: wire it**"' ]
  [ "$(field '.blocked_tasks[0].note')" = '"no SAS producer exists"' ]
  [[ "$(field .blocked_summary)" == *"Task 3: sas"*"no SAS producer exists"* ]]
}

@test "a RESOLVED note clears the BLOCKED note before it" {
  commit_plan '### Task 3\n- [ ] Step 2\n  > BLOCKED: x\n  > RESOLVED: producer added in task 2\n'
  pass 16 4
  [ "$(field .blocked_tasks)" = "[]" ]
  [ "$(field .blocked_summary)" = '"none recorded"' ]
}

@test "a BLOCKED note under a ticked step is not listed" {
  commit_plan '### Task 3\n- [x] Step 2\n  > BLOCKED: x\n'
  pass 16 4
  [ "$(field .blocked_tasks)" = "[]" ]
}

@test "a RESOLVED note under a later step does not clear an earlier BLOCKED" {
  commit_plan '### Task 3\n- [ ] Step 2\n  > BLOCKED: x\n- [ ] Step 3\n  > RESOLVED: y\n'
  pass 16 4
  [ "$(field '[.blocked_tasks[].step]')" = '["Step 2"]' ]
}

@test "BLOCKED notes inside code fences are ignored" {
  commit_plan '### Task 3\n- [ ] Step 2\n```\n> BLOCKED: example\n```\n'
  pass 16 4
  [ "$(field .blocked_tasks)" = "[]" ]
}

@test "only committed BLOCKED notes count" {
  printf -- '### Task 3\n- [ ] Step 2\n  > BLOCKED: x\n' > "$WORK/docs/plans/a.md"
  pass 16 4
  [ "$(field .blocked_tasks)" = "[]" ]
}

@test "an unreadable plan contributes no blocked tasks" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ \
    "$(snap 16 4 | jq '.["resolve-inputs"].inputs.plan_paths = ["docs/plans/gone.md", "/abs/out.md"]')" "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(field .blocked_tasks)" = "[]" ]
}
