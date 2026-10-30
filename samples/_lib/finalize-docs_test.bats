#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/finalize-docs.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/work"
  git init -q -b main "$WORK"
  echo a > "$WORK/a.txt"
  git -C "$WORK" add a.txt
  git -C "$WORK" commit -q -m init
  mkdir -p "$WORK/docs/specs"
}

# run_it <args...>; snapshot carries work_dir.
run_it() {
  local snap
  snap="$(jq -n --arg w "$WORK" '{work_dir:$w}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$snap" "$SCRIPT" "$@"
}

# --- Happy path

@test "commits an uncommitted doc" {
  echo spec > "$WORK/docs/specs/s.md"
  run_it --message "docs: spec" docs/specs/s.md
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .committed)" = "true" ]
  [ "$(printf '%s' "$output" | jq -c .paths)" = '["docs/specs/s.md"]' ]
  git -C "$WORK" show --stat HEAD | grep -q 'docs/specs/s.md'
  git -C "$WORK" log -1 --format=%s | grep -q '^docs: spec'
}

@test "does not push" {
  git init -q --bare -b main "$T/origin.git"
  git -C "$WORK" remote add origin "$T/origin.git"
  echo spec > "$WORK/docs/specs/s.md"
  run_it --message m docs/specs/s.md
  [ "$status" -eq 0 ]
  [ -z "$(git -C "$T/origin.git" branch --list)" ]
}

# --- Edge cases

@test "already committed doc gives committed:false" {
  echo spec > "$WORK/docs/specs/s.md"
  git -C "$WORK" add docs
  git -C "$WORK" commit -q -m pre
  before="$(git -C "$WORK" rev-parse HEAD)"
  run_it --message m docs/specs/s.md
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .committed)" = "false" ]
  [ "$(printf '%s' "$output" | jq -c .paths)" = '["docs/specs/s.md"]' ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$before" ]
}

@test "phase-2 sorts before phase-10" {
  for n in 10 2 1; do echo p > "$WORK/docs/specs/phase-$n.md"; done
  run_it --message m 'docs/specs/phase-*.md'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .paths)" = '["docs/specs/phase-1.md","docs/specs/phase-2.md","docs/specs/phase-10.md"]' ]
}

@test "commits only the named paths" {
  echo spec > "$WORK/docs/specs/s.md"
  echo other > "$WORK/other.txt"
  git -C "$WORK" add other.txt
  run_it --message m docs/specs/s.md
  [ "$status" -eq 0 ]
  ! git -C "$WORK" show --stat HEAD | grep -q other.txt
}

@test "multiple patterns are unioned and de-duplicated" {
  echo x > "$WORK/docs/specs/a.md"
  run_it --message m docs/specs/a.md 'docs/specs/*.md'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .paths)" = '["docs/specs/a.md"]' ]
}

# --- Error cases

@test "no match exits 1 with no document written" {
  run_it --message m 'docs/specs/*.md'
  [ "$status" -eq 1 ]
  [ -z "$output" ]
  [[ "$stderr" == *"no document written"* ]]
}

@test "empty file exits 1" {
  : > "$WORK/docs/specs/s.md"
  run_it --message m docs/specs/s.md
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "missing --message exits 1" {
  echo spec > "$WORK/docs/specs/s.md"
  run_it docs/specs/s.md
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "no paths exits 1" {
  run_it --message m
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "unset work_dir exits 1" {
  run --separate-stderr bash -c 'printf "{}" | sh "$1" --message m x' _ "$SCRIPT"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

# --- Boundary

@test "exactly one match" {
  echo spec > "$WORK/docs/specs/only.md"
  run_it --message m 'docs/specs/*.md'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq '.paths|length')" = "1" ]
}
