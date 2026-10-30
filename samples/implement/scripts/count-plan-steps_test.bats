#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/count-plan-steps.sh"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK/docs/plans"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q -b main "$WORK"
  git -C "$WORK" commit -q --allow-empty -m init
}

# run_it <paths-json>: commits the tree first (the counter reads HEAD), then runs.
run_it() {
  git -C "$WORK" add -A
  git -C "$WORK" commit -q --allow-empty -m plans
  run_uncommitted "$1"
}

# run_uncommitted <paths-json>: runs against whatever HEAD holds.
run_uncommitted() {
  local snap
  snap="$(jq -n --arg w "$WORK" --argjson p "$1" '{work_dir:$w, "resolve-inputs":{inputs:{plan_paths:$p}}}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

# --- Happy path

@test "mixed checkboxes" {
  printf -- '- [x] a\n- [ ] b\n  - [ ] c\n* [X] d\n' > "$WORK/docs/plans/a.md"
  printf -- '- [ ] e\n' > "$WORK/docs/plans/b-two.md"
  run_it '["docs/plans/a.md","docs/plans/b-two.md"]'
  [ "$status" -eq 0 ]
  [ "$(jq -c '[.unchecked_steps,.checked_steps,.all_tasks_complete,.tasks_remaining]' <<<"$output")" = "[3,2,false,3]" ]
  [ "$(jq -c .unreadable <<<"$output")" = "[]" ]
}

# --- Edge cases

@test "all checked is complete" {
  printf -- '- [x] a\n- [x] b\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md"]'
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "true" ]
  [ "$(jq -r .tasks_remaining <<<"$output")" = "0" ]
}

@test "no checkboxes is incomplete and flagged" {
  printf 'just prose\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md"]'
  [ "$status" -eq 0 ]
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "false" ]
  [ "$(jq -r .no_checkboxes <<<"$output")" = "true" ]
}

@test "empty plan list is incomplete" {
  run_it '[]'
  [ "$status" -eq 0 ]
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "false" ]
}

@test "checkboxes inside code fences are ignored" {
  printf -- '- [x] real\n```\n- [ ] example\n```\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md"]'
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "true" ]
}

@test "path with a hyphen and space is read" {
  printf -- '- [ ] x\n' > "$WORK/docs/plans/my plan-1.md"
  run_it '["docs/plans/my plan-1.md"]'
  [ "$(jq -r .unchecked_steps <<<"$output")" = "1" ]
}

# --- Error

@test "missing file reported in unreadable and incomplete" {
  printf -- '- [x] a\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md","docs/plans/gone.md"]'
  [ "$status" -eq 0 ]
  [ "$(jq -c .unreadable <<<"$output")" = '["docs/plans/gone.md"]' ]
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "false" ]
}

# --- Boundary

@test "one unchecked step" {
  printf -- '- [x] a\n- [ ] b\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md"]'
  [ "$(jq -c '[.unchecked_steps,.checked_steps,.all_tasks_complete]' <<<"$output")" = "[1,1,false]" ]
}

# --- Committed state is the authority

@test "ticked boxes left in the working tree do not count" {
  printf -- '- [ ] a\n- [ ] b\n' > "$WORK/docs/plans/a.md"
  run_it '["docs/plans/a.md"]'
  printf -- '- [x] a\n- [x] b\n' > "$WORK/docs/plans/a.md"
  run_uncommitted '["docs/plans/a.md"]'
  [ "$status" -eq 0 ]
  [ "$(jq -c '[.unchecked_steps,.checked_steps,.all_tasks_complete]' <<<"$output")" = "[2,0,false]" ]
}

@test "a plan that exists only in the working tree is unreadable" {
  run_it '[]'
  printf -- '- [x] a\n' > "$WORK/docs/plans/new.md"
  run_uncommitted '["docs/plans/new.md"]'
  [ "$(jq -c .unreadable <<<"$output")" = '["docs/plans/new.md"]' ]
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "false" ]
}

@test "a committed tick counts, and an absolute path inside work_dir is read at HEAD" {
  printf -- '- [x] a\n' > "$WORK/docs/plans/a.md"
  run_it "[\"$WORK/docs/plans/a.md\"]"
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "true" ]
}

@test "an absolute path outside work_dir is unreadable" {
  printf -- '- [x] a\n' > "$BATS_TEST_TMPDIR/out.md"
  run_it "[\"$BATS_TEST_TMPDIR/out.md\"]"
  [ "$(jq -r .all_tasks_complete <<<"$output")" = "false" ]
  [ "$(jq -r '.unreadable|length' <<<"$output")" = "1" ]
}
