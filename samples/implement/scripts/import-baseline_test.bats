#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/import-baseline.sh"
  T="$BATS_TEST_TMPDIR"
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WORK/.arbitron/pipeline" "$WS"
  REL=.arbitron/pipeline/x.baseline.json
}

run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}
snap() { jq -n --arg w "$WORK" --arg ws "$WS" --arg p "$1" '{work_dir:$w, workspace:$ws, baseline_rel:$p}'; }

GOOD='{"suites":[{"name":"go","command":"go test ./...","failed_count":1,"passed_count":4,"failing_tests":["A"]}]}'

@test "copies" {
  printf '%s' "$GOOD" > "$WORK/$REL"
  run_it "$(snap "$REL")"
  [ "$status" -eq 0 ]
  [ "$(jq -r .imported <<<"$output")" = "true" ]
  [ "$(jq -r .path <<<"$output")" = "$WS/test-baseline.json" ]
  [ "$(jq -c . "$WS/test-baseline.json")" = "$(printf '%s' "$GOOD" | jq -c .)" ]
}

@test "invalid exits 1" {
  printf '{"suites":"nope"}' > "$WORK/$REL"
  run_it "$(snap "$REL")"
  [ "$status" -eq 1 ]
  [ ! -e "$WS/test-baseline.json" ]
}

@test "missing file exits 1" {
  run_it "$(snap "$REL")"
  [ "$status" -eq 1 ]
}

@test "path traversal exits 1" {
  printf '%s' "$GOOD" > "$T/outside.json"
  run_it "$(snap ../outside.json)"
  [ "$status" -eq 1 ]
  [ ! -e "$WS/test-baseline.json" ]
}
