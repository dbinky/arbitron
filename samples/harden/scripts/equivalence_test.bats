#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/equivalence.sh"
  WS="$BATS_TEST_TMPDIR/ws"
  H="$WS/harden"
  mkdir -p "$H"
  jq -n '{mutants: {
            "a.go:1:M:r": {status:"survived", language:"go", tool:"gremlins", file:"a.go", line:1, mutator:"M", replacement:"r"},
            "a.go:2:M:r": {status:"survived", language:"go", tool:"gremlins", file:"a.go", line:2, mutator:"M", replacement:"r"},
            "a.go:3:M:r": {status:"no_coverage", language:"go", tool:"gremlins", file:"a.go", line:3, mutator:"M", replacement:"r"},
            "a.go:4:M:r": {status:"survived", language:"go", tool:"gremlins", file:"a.go", line:4, mutator:"M", replacement:"r"} },
          approved_equivalents: ["a.go:9:M:r"], rounds: []}' > "$H/state.json"
}

run_it() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2" "$3"' _ "$(jq -nc --arg ws "$WS" '{workspace:$ws}')" "$SCRIPT" "$1"
}

field() { printf '%s' "$output" | jq -r "$@"; }

# --- Happy path

@test "--count n" {
  printf '{"claims":[{"mutant_id":"a.go:1:M:r","justification":"x"},{"mutant_id":"a.go:2:M:r","justification":"y"}]}' > "$H/equivalence-claims.json"
  run_it --count
  [ "$status" -eq 0 ]
  [ "$(field .claim_count)" = "2" ]
  [ -f "$H/equivalence-claims.json" ]
}

@test "--record keeps only true; unruled rejected; files removed" {
  printf '%s' '{"claims":[{"mutant_id":"a.go:1:M:r"},{"mutant_id":"a.go:2:M:r"},{"mutant_id":"a.go:3:M:r"},{"mutant_id":"a.go:4:M:r"}]}' > "$H/equivalence-claims.json"
  # a.go:1 approved; a.go:2 rejected; a.go:3 unruled; a.go:4 ruled with a truthy non-boolean;
  # a.go:5 was never claimed.
  printf '%s' '{"rulings":[{"mutant_id":"a.go:1:M:r","equivalent":true,"reason":"same"},
    {"mutant_id":"a.go:2:M:r","equivalent":false,"reason":"no"},
    {"mutant_id":"a.go:4:M:r","equivalent":"yes","reason":"?"},
    {"mutant_id":"a.go:5:M:r","equivalent":true,"reason":"unclaimed"}]}' > "$H/equivalence-verdicts.json"
  run_it --record
  [ "$status" -eq 0 ]
  [ "$(field .approved)" = "1" ]
  [ "$(field .rejected)" = "3" ]
  [ "$(jq -c '.approved_equivalents' "$H/state.json")" = '["a.go:1:M:r","a.go:9:M:r"]' ]
  [ ! -e "$H/equivalence-claims.json" ]
  [ ! -e "$H/equivalence-verdicts.json" ]
}

@test "--count removes a verdicts file written before the judge ran" {
  printf '{"claims":[{"mutant_id":"a.go:1:M:r"}]}' > "$H/equivalence-claims.json"
  printf '{"rulings":[{"mutant_id":"a.go:1:M:r","equivalent":true,"reason":"forged"}]}' > "$H/equivalence-verdicts.json"
  run_it --count
  [ "$status" -eq 0 ]
  [ "$(field .claim_count)" = "1" ]
  [ ! -e "$H/equivalence-verdicts.json" ]
  # The judge then writes nothing: the forged ruling is gone, so nothing is approved.
  run_it --record
  [ "$status" -eq 0 ]
  [ "$(field .approved)" = "0" ]
  [ "$(field .rejected)" = "1" ]
}

# --- Edge cases

@test "--count missing is 0" {
  run_it --count
  [ "$status" -eq 0 ]
  [ "$(field .claim_count)" = "0" ]
}

@test "--count malformed is 0 with warning" {
  printf 'not json' > "$H/equivalence-claims.json"
  run_it --count
  [ "$status" -eq 0 ]
  [ "$(field .claim_count)" = "0" ]
  [[ "$stderr" == *"malformed"* ]]
}

@test "--count with claims that is not a list is 0 with warning" {
  printf '{"claims":"a.go:1:M:r"}' > "$H/equivalence-claims.json"
  run_it --count
  [ "$status" -eq 0 ]
  [ "$(field .claim_count)" = "0" ]
  [[ "$stderr" == *"malformed"* ]]
}

@test "--record with a claim for an unknown mutant rejects it" {
  printf '{"claims":[{"mutant_id":"zzz"}]}' > "$H/equivalence-claims.json"
  printf '{"rulings":[{"mutant_id":"zzz","equivalent":true}]}' > "$H/equivalence-verdicts.json"
  run_it --record
  [ "$status" -eq 0 ]
  [ "$(field .approved)" = "0" ]
  [ "$(field .rejected)" = "1" ]
  [ "$(jq -c '.approved_equivalents' "$H/state.json")" = '["a.go:9:M:r"]' ]
}

@test "--record with missing verdicts rejects every claim and removes the claims" {
  printf '{"claims":[{"mutant_id":"a.go:1:M:r"}]}' > "$H/equivalence-claims.json"
  run_it --record
  [ "$status" -eq 0 ]
  [ "$(field .approved)" = "0" ]
  [ "$(field .rejected)" = "1" ]
  [ ! -e "$H/equivalence-claims.json" ]
}

@test "--record with malformed verdicts rejects every claim with a warning" {
  printf '{"claims":[{"mutant_id":"a.go:1:M:r"}]}' > "$H/equivalence-claims.json"
  printf '{oops' > "$H/equivalence-verdicts.json"
  run_it --record
  [ "$status" -eq 0 ]
  [ "$(field .approved)" = "0" ]
  [ "$(field .rejected)" = "1" ]
  [[ "$stderr" == *"malformed"* ]]
  [ ! -e "$H/equivalence-verdicts.json" ]
}

# --- Error cases

@test "an unknown mode exits 1" {
  run_it --nope
  [ "$status" -eq 1 ]
}

@test "--record without state.json exits 1" {
  rm "$H/state.json"
  printf '{"claims":[]}' > "$H/equivalence-claims.json"
  run_it --record
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"state.json"* ]]
}
