#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/import-baseline.sh"
  T="$BATS_TEST_TMPDIR"
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WORK/docs/reference" "$WS"
}

run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

snap() { # <baseline_path>
  jq -n --arg w "$WORK" --arg ws "$WS" --arg p "$1" '{work_dir:$w, workspace:$ws, baseline_path:$p}'
}

GOOD='{"suites":[{"name":"go","command":"go test ./...","failed_count":1,"passed_count":4,"failing_tests":["A"]}]}'

# --- Happy path

@test "valid file copied" {
  printf '%s' "$GOOD" > "$WORK/docs/reference/test-baseline.json"
  run_it "$(snap docs/reference/test-baseline.json)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .imported)" = "true" ]
  [ "$(printf '%s' "$output" | jq -r .path)" = "$WS/test-baseline.json" ]
  [ "$(jq -c . "$WS/test-baseline.json")" = "$(printf '%s' "$GOOD" | jq -c .)" ]
}

# --- Error

@test "invalid shape exits 1 and writes nothing" {
  printf '{"suites":"nope"}' > "$WORK/docs/reference/test-baseline.json"
  run_it "$(snap docs/reference/test-baseline.json)"
  [ "$status" -eq 1 ]
  [ ! -e "$WS/test-baseline.json" ]
}

@test "non-JSON file exits 1" {
  printf 'not json' > "$WORK/docs/reference/test-baseline.json"
  run_it "$(snap docs/reference/test-baseline.json)"
  [ "$status" -eq 1 ]
}

# --- Edge cases

@test "missing file exits 1" {
  run_it "$(snap docs/reference/absent.json)"
  [ "$status" -eq 1 ]
}

@test "empty baseline_path exits 1" {
  run_it "$(snap '')"
  [ "$status" -eq 1 ]
}

@test "path traversal exits 1" {
  printf '%s' "$GOOD" > "$T/outside.json"
  run_it "$(snap ../outside.json)"
  [ "$status" -eq 1 ]
  [ ! -e "$WS/test-baseline.json" ]
}
