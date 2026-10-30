#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/clean-check.sh"
  W="$BATS_TEST_TMPDIR/work"
  mkdir -p "$W"
  git -C "$W" init -q
  git -C "$W" config user.email t@example.com
  git -C "$W" config user.name t
  printf 'build/\n' > "$W/.gitignore"
  printf 'package a\n' > "$W/a.go"
  printf 'package a\n' > "$W/a_test.go"
  git -C "$W" add -A
  git -C "$W" commit -q -m init
  HEAD0="$(git -C "$W" rev-parse HEAD)"
  WS="$BATS_TEST_TMPDIR/ws"
  mkdir -p "$WS/tamper"
}

run_it() {
  run --separate-stderr sh -c 'in=$1 s=$2; shift 2; printf "%s" "$in" | sh "$s" "$@"' _ "$(jq -nc --arg w "$W" --arg ws "$WS" '{work_dir:$w, workspace:$ws}')" "$SCRIPT" "$@"
}

field() { printf '%s' "$output" | jq -r "$@"; }

# --- Happy path

@test "clean tree: dirty false, nothing discarded" {
  run_it --discard
  [ "$status" -eq 0 ]
  [ "$(field .dirty)" = "false" ]
  [ "$(field .discarded)" = "false" ]
  [ "$(field '.files | length')" = "0" ]
}

@test "--discard: modified, staged and untracked files are discarded; commits kept" {
  printf 'package a\nfunc T() {}\n' > "$W/a_test.go"
  git -C "$W" add a_test.go
  git -C "$W" commit -q -m "test: kept"
  HEAD1="$(git -C "$W" rev-parse HEAD)"
  printf 'changed\n' >> "$W/a.go"
  printf 'new\n' > "$W/b_test.go"
  git -C "$W" add b_test.go
  printf 'stray\n' > "$W/notes.txt"
  run_it --discard
  [ "$status" -eq 0 ]
  [ "$(field .dirty)" = "true" ]
  [ "$(field .discarded)" = "true" ]
  [ "$(field -c '.files | sort')" = '["a.go","b_test.go","notes.txt"]' ]
  [ -z "$(git -C "$W" status --porcelain)" ]
  [ "$(git -C "$W" rev-parse HEAD)" = "$HEAD1" ]
}

@test "a previous round's findings.json is removed, clean or dirty" {
  printf '{"findings":[{"id":"T1"}]}' > "$WS/tamper/findings.json"
  run_it --discard
  [ "$status" -eq 0 ]
  [ ! -e "$WS/tamper/findings.json" ]
  printf '{"findings":[{"id":"T1"}]}' > "$WS/tamper/findings.json"
  printf 'changed\n' >> "$W/a.go"
  run_it --discard
  [ "$status" -eq 0 ]
  [ "$(field .dirty)" = "true" ]
  [ ! -e "$WS/tamper/findings.json" ]
}

# --- Edge

@test "ignored files are neither reported nor removed" {
  mkdir -p "$W/build"
  printf 'x\n' > "$W/build/out.o"
  run_it --discard
  [ "$status" -eq 0 ]
  [ "$(field .dirty)" = "false" ]
  [ -f "$W/build/out.o" ]
}

@test "without --discard a dirty tree is reported and left alone" {
  printf 'changed\n' >> "$W/a.go"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .dirty)" = "true" ]
  [ "$(field .discarded)" = "false" ]
  [ -n "$(git -C "$W" status --porcelain)" ]
}

# --- Error

@test "work_dir unset exits 1" {
  run --separate-stderr sh -c 'printf "{}" | sh "$1"' _ "$SCRIPT"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "not a worktree exits 1" {
  W="$BATS_TEST_TMPDIR/nope"
  mkdir -p "$W"
  run_it
  [ "$status" -eq 1 ]
}

@test "unknown argument exits 1" {
  run_it --bogus
  [ "$status" -eq 1 ]
}
