#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() { SCRIPT="$BATS_TEST_DIRNAME/halt.sh"; }

run_it() { run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"; }

@test "stage message" {
  run_it '{"failed_stage":"plan","failed_reason":"deps unresolved"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pipeline stopped: plan: deps unresolved"* ]]
}

@test "generic message when vars absent" {
  run_it '{}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pipeline stopped: a pipeline step failed"* ]]
}

@test "always exit 1 even with empty stdin" {
  run --separate-stderr bash -c 'sh "$1" </dev/null' _ "$SCRIPT"
  [ "$status" -eq 1 ]
}
