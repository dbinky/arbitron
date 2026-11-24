#!/usr/bin/env bats
# Tests for plan-audit.sh: the deterministic check that runs after every
# alignment pass.
#
# WHY:
# - remote-runners, 2026-11-23: align-plans rewrote full TDD plans into prose
#   with no `- [ ]` steps, and align-plans-2 added tasks with no steps. The
#   implement agent picks the first task with an unticked box, so it skipped
#   those producer tasks and 47 tasks blocked on files nobody built.
# - lunaroute, runs a5259bec / c028678b / 8fc37529: the plans held steps no
#   agent can do (operator observations, live vendor evidence). The implement
#   loop stalled on them for hours.

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/plan-audit.sh"
  WORK="$BATS_TEST_TMPDIR/work"
  PLANS="$WORK/docs/plans/age-filter"
  mkdir -p "$PLANS"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q -b main "$WORK"
  git -C "$WORK" commit -q --allow-empty -m init
}

# plan <basename> <content>: write a plan file under the plan dir.
plan() {
  printf '%b' "$2" > "$PLANS/$1"
}

commit() {
  git -C "$WORK" add -A
  git -C "$WORK" commit -q --allow-empty -m plans
}

# run_it [run_date]: commits the tree (the audit reads HEAD), then runs.
run_it() {
  commit
  run_uncommitted "${1:-2026-10-24}"
}

run_uncommitted() {
  local snap
  snap="$(jq -n --arg w "$WORK" --arg d "${1:-2026-10-24}" \
    '{work_dir:$w, plan_dir:"docs/plans/age-filter", run_date:$d}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

P=docs/plans/age-filter

# --- Happy path

@test "stepped plans with nothing human-only pass" {
  plan 2026-10-24-phase-1-task-1.md '# T1\n- [ ] Step 1: write the failing test\n- [ ] Step 2: make it pass\n'
  plan 2026-10-24-phase-1-task-2.md '# T2\n- [x] Step 1: done\n- [ ] Step 2: wire it\n'
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -c '[.plans,.stepless_count,.ok,.human_only]' <<<"$output")" = '[2,0,true,0]' ]
  [ "$(jq -c .stepless <<<"$output")" = '[]' ]
  [ "$(jq -c .human_only_lines <<<"$output")" = '[]' ]
}

# --- Success: stepless plans are named and stop the pass

@test "a plan rewritten into prose is stepless and not ok" {
  plan 2026-10-24-phase-1-task-1.md '# T1\n- [ ] Step 1: test\n'
  plan 2026-10-24-phase-1-task-2.md '# T2\nThe producer is built by wiring X into Y.\n'
  run_it
  [ "$(jq -c .stepless <<<"$output")" = "[\"$P/2026-10-24-phase-1-task-2.md\"]" ]
  [ "$(jq -r .stepless_count <<<"$output")" = "1" ]
  [ "$(jq -r .ok <<<"$output")" = "false" ]
  [ "$(jq -r '.gaps[0]' <<<"$output")" = "STEPLESS: $P/2026-10-24-phase-1-task-2.md has no checkbox steps" ]
}

@test "a plan whose only checkbox sits in a code fence is stepless" {
  plan 2026-10-24-phase-1-task-1.md '# T1\n```\n- [ ] example\n```\n'
  run_it
  [ "$(jq -r .stepless_count <<<"$output")" = "1" ]
  [ "$(jq -r .ok <<<"$output")" = "false" ]
}

@test "ticked-only plans are stepped (the boxes exist)" {
  plan 2026-10-24-phase-1-task-1.md '- [x] a\n* [X] b\n'
  run_it
  [ "$(jq -r .ok <<<"$output")" = "true" ]
}

# --- Human-only steps (the deterministic backstop)

@test "unchecked steps no agent can do are reported with path and line" {
  plan 2026-10-24-phase-2-task-1.md '# T\n- [ ] Step 1: write the test\n- [ ] Step 2: Record operator observations of the completion banner\n- [ ] Step 3: Manually verify the dashboard shows the alias\n'
  plan 2026-10-24-phase-2-task-2.md '- [ ] Step 1: Collect live vendor evidence for the revoke flow\n- [ ] Step 2: Use a fixture captured from production traffic\n'
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -r .human_only <<<"$output")" = "4" ]
  [ "$(jq -r '.human_only_lines[0]' <<<"$output")" = "$P/2026-10-24-phase-2-task-1.md:3: - [ ] Step 2: Record operator observations of the completion banner" ]
  [ "$(jq -r '.human_only_lines[1]' <<<"$output")" = "$P/2026-10-24-phase-2-task-1.md:4: - [ ] Step 3: Manually verify the dashboard shows the alias" ]
  # ok is about steps; human-only lines go to the dependency gate, not here.
  [ "$(jq -r .ok <<<"$output")" = "true" ]
}

@test "each human-only phrase family is caught" {
  plan 2026-10-24-phase-1-task-1.md "$(printf '%s\n' \
    '- [ ] Operator verification of the alert' \
    '- [ ] Operator sign-off on rollout' \
    '- [ ] manual-check the release notes' \
    '- [ ] Run the live provider acceptance matrix' \
    '- [ ] Gather live evidence from the gateway' \
    '- [ ] Replay a captured real fixture' \
    '- [ ] Use a real-world fixture of the stream' \
    '- [ ] Configure production credentials for the vendor' \
    '- [ ] Check the vendor dashboard for the key')"
  run_it
  [ "$(jq -r .human_only <<<"$output")" = "9" ]
}

@test "ticked steps and prose that mention operators are not human-only" {
  plan 2026-10-24-phase-1-task-1.md '- [x] Operator observation recorded\nThe operator observes the banner later; out of scope.\n## Operator follow-up (not for implementation)\n- Operator verification of the alert\n- [ ] Step 1: write the failing test\n'
  run_it
  [ "$(jq -r .human_only <<<"$output")" = "0" ]
}

@test "human-only phrases inside code fences are ignored" {
  plan 2026-10-24-phase-1-task-1.md '- [ ] Step 1: test\n```\n- [ ] manually verify\n```\n'
  run_it
  [ "$(jq -r .human_only <<<"$output")" = "0" ]
}

@test "agent-doable steps that only look similar are not flagged" {
  plan 2026-10-24-phase-1-task-1.md "$(printf '%s\n' \
    '- [ ] Step 1: write a test that the live-mode flag defaults off' \
    '- [ ] Step 2: add the operator field to the struct' \
    '- [ ] Step 3: verify manually-entered aliases are trimmed' \
    '- [ ] Step 4: load credentials from the env in the test helper')"
  run_it
  [ "$(jq -r .human_only <<<"$output")" = "0" ]
}

# --- Error: fail closed

@test "no plans at HEAD is not ok" {
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -c '[.plans,.ok]' <<<"$output")" = '[0,false]' ]
  jq -r '.gaps[0]' <<<"$output" | grep -q '^NO-PLANS:'
}

@test "a plan on disk but not committed is not ok" {
  plan 2026-10-24-phase-1-task-1.md '- [ ] a\n'
  run_it
  plan 2026-10-24-phase-1-task-2.md '- [ ] b\n'
  run_uncommitted
  [ "$(jq -c .uncommitted <<<"$output")" = "[\"$P/2026-10-24-phase-1-task-2.md\"]" ]
  [ "$(jq -r .ok <<<"$output")" = "false" ]
}

@test "steps removed in the working tree but committed at HEAD still count from HEAD" {
  plan 2026-10-24-phase-1-task-1.md '- [ ] a\n'
  run_it
  plan 2026-10-24-phase-1-task-1.md 'prose\n'
  run_uncommitted
  [ "$(jq -r .ok <<<"$output")" = "true" ]
}

@test "a work_dir that is not a repository fails closed" {
  local snap
  snap="$(jq -n --arg w "$BATS_TEST_TMPDIR/nowhere" '{work_dir:$w, plan_dir:"docs/plans/x", run_date:"2026-10-24"}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(jq -r .ok <<<"$output")" = "false" ]
}

@test "a malformed run_date fails closed" {
  plan 2026-10-24-phase-1-task-1.md '- [ ] a\n'
  run_it 'yesterday'
  [ "$(jq -r .ok <<<"$output")" = "false" ]
}

# --- Edge

@test "plans of another run_date and non-plan files are ignored" {
  plan 2026-10-24-phase-1-task-1.md '- [ ] a\n'
  plan 2026-10-20-phase-1-task-1.md 'old prose, no steps\n'
  plan 2026-10-24-notes.md 'no steps\n'
  run_it
  [ "$(jq -c '[.plans,.ok]' <<<"$output")" = '[1,true]' ]
}

@test "a long human-only line is truncated" {
  plan 2026-10-24-phase-1-task-1.md "- [ ] Manually verify $(printf 'x%.0s' $(seq 1 400))"
  run_it
  [ "$(jq -r '.human_only_lines[0] | length' <<<"$output")" -le 260 ]
}
