#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# state-guard.sh --snapshot records the scoring state before strengthen;
# --verify <node> checks it afterwards and restores it from the snapshot copy.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/state-guard.sh"
  WS="$BATS_TEST_TMPDIR/ws"
  H="$WS/harden"
  mkdir -p "$H"
  printf '{"mutants":{"m1":{"status":"survived"}},"approved_equivalents":[]}\n' > "$H/state.json"
  printf '{"survivors":[{"id":"m1"}]}\n' > "$H/survivors-round.json"
}

snapshot() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2" --snapshot' _ "$(jq -nc --arg ws "$WS" '{workspace:$ws}')" "$SCRIPT"
  SNAP="$output"
}

# verify: feeds the snapshot node's output back in as "state-snapshot".
verify() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2" --verify state-snapshot' _ \
    "$(jq -nc --arg ws "$WS" --argjson s "$SNAP" '{workspace:$ws, "state-snapshot":$s}')" "$SCRIPT"
}

field() { printf '%s' "$output" | jq -r "$@"; }

# --- Happy path

@test "untouched state verifies clean" {
  snapshot
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$SNAP" | jq -r '.hashes | keys | join(",")')" = "state.json,survivors-round.json" ]
  verify
  [ "$status" -eq 0 ]
  [ "$(field .tampered)" = "false" ]
  [ "$(field '.files | length')" = "0" ]
}

# --- Tampering

@test "an edited state.json is detected and restored" {
  snapshot
  before="$(cat "$H/state.json")"
  printf '{"mutants":{"m1":{"status":"killed"}},"approved_equivalents":["m1"]}\n' > "$H/state.json"
  verify
  [ "$status" -eq 0 ]
  [ "$(field .tampered)" = "true" ]
  [ "$(field .restored)" = "true" ]
  [ "$(field -c .files)" = '["state.json"]' ]
  [ "$(cat "$H/state.json")" = "$before" ]
}

@test "a deleted survivors-round.json is restored" {
  snapshot
  rm "$H/survivors-round.json"
  verify
  [ "$status" -eq 0 ]
  [ "$(field .tampered)" = "true" ]
  [ -f "$H/survivors-round.json" ]
}

@test "a score file absent at snapshot time and created later is removed" {
  rm "$H/survivors-round.json"
  snapshot
  printf '{"survivors":[]}\n' > "$H/survivors-round.json"
  verify
  [ "$status" -eq 0 ]
  [ "$(field .tampered)" = "true" ]
  [ ! -e "$H/survivors-round.json" ]
}

# --- Error

@test "a tampered snapshot copy cannot be restored from: exit 1" {
  snapshot
  printf '{"forged":1}\n' > "$H/state.json"
  for f in "$H"/.state-guard/*; do printf '{"forged":1}\n' > "$f"; done
  verify
  [ "$status" -eq 1 ]
}

@test "--verify without a recorded snapshot exits 1" {
  SNAP='{}'
  verify
  [ "$status" -eq 1 ]
}

@test "workspace unset exits 1" {
  run --separate-stderr sh -c 'printf "{}" | sh "$1" --snapshot' _ "$SCRIPT"
  [ "$status" -eq 1 ]
}

@test "unknown mode exits 1" {
  run --separate-stderr sh -c 'printf "{}" | sh "$1" --bogus' _ "$SCRIPT"
  [ "$status" -eq 1 ]
}
