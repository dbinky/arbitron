#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/list-plans.sh"
  WORK="$BATS_TEST_TMPDIR/work"
  PLANS="$WORK/docs/plans/age-filter"
  mkdir -p "$PLANS"
}

# plan <basename> [content]: write a plan file under the plan dir.
plan() {
  printf '%s' "${2:-# Plan
}" > "$PLANS/$1"
}

# run_it [run_date]: snapshot carries work_dir, plan_dir and run_date.
run_it() {
  local snap
  snap="$(jq -n --arg w "$WORK" --arg d "${1:-2026-10-24}" \
    '{work_dir:$w, plan_dir:"docs/plans/age-filter", run_date:$d}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

# --- Happy path

@test "sorts phase then task numerically (task-10 after task-9)" {
  plan 2026-10-24-phase-2-task-1.md
  plan 2026-10-24-phase-1-task-10.md
  plan 2026-10-24-phase-1-task-9.md
  plan 2026-10-24-phase-10-task-1.md
  plan 2026-10-24-phase-1-task-2.md
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"plan_paths":["docs/plans/age-filter/2026-10-24-phase-1-task-2.md","docs/plans/age-filter/2026-10-24-phase-1-task-9.md","docs/plans/age-filter/2026-10-24-phase-1-task-10.md","docs/plans/age-filter/2026-10-24-phase-2-task-1.md","docs/plans/age-filter/2026-10-24-phase-10-task-1.md"]}' ]
}

# --- Edge cases

@test "ignores files of another run_date" {
  plan 2026-10-20-phase-1-task-1.md
  plan 2026-10-20-phase-1-task-2.md
  plan 2026-10-24-phase-1-task-1.md
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .plan_paths)" = '["docs/plans/age-filter/2026-10-24-phase-1-task-1.md"]' ]
}

@test "ignores names that are not <run_date>-phase-N-task-M.md" {
  plan 2026-10-24-phase-1-task-1.md
  plan 2026-10-24-phase-1-task-1-notes.md
  plan 2026-10-24-phase-x-task-1.md
  plan README.md
  mkdir "$PLANS/2026-10-24-phase-1-task-2.md"
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .plan_paths)" = '["docs/plans/age-filter/2026-10-24-phase-1-task-1.md"]' ]
}

# --- Error

@test "none exits 1" {
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no plan files"* ]]
}

@test "only another run_date's files exits 1" {
  plan 2026-10-20-phase-1-task-1.md
  run_it
  [ "$status" -eq 1 ]
}

@test "missing plan dir exits 1" {
  rm -rf "$PLANS"
  run_it
  [ "$status" -eq 1 ]
}

@test "a run_date that is not YYYY-MM-DD exits 1" {
  plan 2026-10-24-phase-1-task-1.md
  run_it '*'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"run_date"* ]]
}

@test "empty work_dir exits 1" {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ \
    '{"work_dir":"","plan_dir":"docs/plans/age-filter","run_date":"2026-10-24"}' "$SCRIPT"
  [ "$status" -eq 1 ]
}

# --- Boundary

@test "single file" {
  plan 2026-10-24-phase-1-task-1.md
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"plan_paths":["docs/plans/age-filter/2026-10-24-phase-1-task-1.md"]}' ]
}
