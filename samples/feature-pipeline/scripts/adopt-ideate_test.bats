#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() { SCRIPT="$BATS_TEST_DIRNAME/adopt-ideate.sh"; }

run_it() { run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"; }

@test "valid" {
  run_it '{"ideate":{"childOutput":{"finish":{"outputs":{"branch":"dev-x-1","slug":"x"}}}}}'
  [ "$status" -eq 0 ]
  [ "$(jq -c . <<<"$output")" = '{"branch":"dev-x-1","slug":"x"}' ]
}

@test "missing branch exits 1" {
  run_it '{"ideate":{"childOutput":{"finish":{"outputs":{"slug":"x"}}}}}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"ideate returned no branch/slug"* ]]
}

@test "empty slug exits 1" {
  run_it '{"ideate":{"childOutput":{"finish":{"outputs":{"branch":"b","slug":""}}}}}'
  [ "$status" -eq 1 ]
}

@test "non-string exits 1" {
  run_it '{"ideate":{"childOutput":{"finish":{"outputs":{"branch":5,"slug":"x"}}}}}'
  [ "$status" -eq 1 ]
}

@test "no ideate output at all exits 1" {
  run_it '{}'
  [ "$status" -eq 1 ]
}
