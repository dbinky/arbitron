#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/record-baseline.sh"
  T="$BATS_TEST_TMPDIR"
  WORK="$T/ws/work"
  WS="$T/ws"
  mkdir -p "$WORK"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q -b main "$WORK"
  echo x > "$WORK/x.txt"
  git -C "$WORK" add . && git -C "$WORK" commit -q -m init
}

run_it() {
  run --separate-stderr bash -c 'printf "{\"workspace\":\"%s\",\"work_dir\":\"%s\"}" "$1" "$2" | sh "$3"' _ "$WS" "$WORK" "$SCRIPT"
}

BASE='{"suites":[{"name":"go","command":"go test ./...","paths":["."],"build_ok":true,"failed_count":2,"passed_count":9,"failing_tests":["A","B"]},{"name":"ui","command":"npx vitest run","failed_count":1,"passed_count":3,"failing_tests":["C"]}]}'

@test "lifts commands" {
  printf '%s' "$BASE" > "$WS/test-baseline.json"
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -c .test_commands <<<"$output")" = '["go test ./...","npx vitest run"]' ]
  [ "$(jq -r .baseline_source <<<"$output")" = "measured" ]
  [ "$(jq -r .baseline_summary <<<"$output")" = "3 pre-existing failing test(s) across 2 suite(s) at branch point" ]
  [ "$(cat "$WS/baseline-head")" = "$(git -C "$WORK" rev-parse HEAD)" ]
}

@test "fallback-empty on missing file" {
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -r .baseline_source <<<"$output")" = "fallback-empty" ]
  [ "$(jq -c .test_baseline <<<"$output")" = '{"suites":[]}' ]
}

@test "fallback-empty on malformed file" {
  printf '{"suites":"nope"}' > "$WS/test-baseline.json"
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -r .baseline_source <<<"$output")" = "fallback-empty" ]
}
