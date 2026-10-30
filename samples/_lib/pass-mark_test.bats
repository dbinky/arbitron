#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/pass-mark.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WS"
  git init -q -b main "$WORK"
  echo a > "$WORK/a.txt"
  git -C "$WORK" add a.txt
  git -C "$WORK" commit -q -m one
}

# run_it <snapshot-json>
run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

snap() {
  jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws}'
}

# --- Happy path

@test "emits HEAD sha" {
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .sha)" = "$(git -C "$WORK" rev-parse HEAD)" ]
}

@test "pass_id increments 1,2,3 across runs" {
  ids=""
  for _ in 1 2 3; do
    run_it "$(snap)"
    [ "$status" -eq 0 ]
    ids="$ids$(printf '%s' "$output" | jq -r .pass_id) "
  done
  [ "$ids" = "1 2 3 " ]
}

@test "pass_id is persisted at tamper/pass-id" {
  run_it "$(snap)"
  [ "$(cat "$WS/tamper/pass-id")" = "1" ]
}

# --- Edge cases

@test "pass_id still increments when HEAD is unchanged between runs" {
  run_it "$(snap)"
  first="$output"
  run_it "$(snap)"
  [ "$(printf '%s' "$first" | jq -r .sha)" = "$(printf '%s' "$output" | jq -r .sha)" ]
  [ "$(printf '%s' "$first" | jq .pass_id)" -lt "$(printf '%s' "$output" | jq .pass_id)" ]
}

@test "pass_id is an integer, not a string" {
  run_it "$(snap)"
  [ "$(printf '%s' "$output" | jq -r '.pass_id|type')" = "number" ]
}

@test "works on detached HEAD" {
  git -C "$WORK" checkout -q --detach
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .sha)" = "$(git -C "$WORK" rev-parse HEAD)" ]
}

# --- Error cases

@test "unset work_dir exits 1" {
  run_it "$(jq -n --arg ws "$WS" '{workspace:$ws}')"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
  [[ "$stderr" == *"work_dir"* ]]
}

@test "unset workspace exits 1" {
  run_it "$(jq -n --arg w "$WORK" '{work_dir:$w}')"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "non-repo exits 1" {
  mkdir "$T/plain"
  run_it "$(jq -n --arg w "$T/plain" --arg ws "$WS" '{work_dir:$w, workspace:$ws}')"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "non-integer pass-id file is treated as an error" {
  mkdir -p "$WS/tamper"
  echo junk > "$WS/tamper/pass-id"
  run_it "$(snap)"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

# --- Boundary

@test "single-commit repo" {
  [ "$(git -C "$WORK" rev-list --count HEAD)" = "1" ]
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .pass_id)" = "1" ]
}
