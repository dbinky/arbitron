#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/score.sh"
  WS="$BATS_TEST_TMPDIR/ws"
  mkdir -p "$WS/harden"
}

# m <file> <line> <status> : one state.json mutant entry as "key": {...}
m() {
  jq -nc --arg f "$1" --argjson l "$2" --arg s "$3" \
    '{key: "\($f):\($l):M:r", value: {status:$s, language:"go", tool:"gremlins", file:$f, line:$l, mutator:"M", replacement:"r"}}'
}

# state <approved-keys-json> <entry>... : writes state.json
state() {
  ap="$1"; shift
  printf '%s\n' "$@" | jq -s --argjson ap "$ap" \
    '{mutants: from_entries, approved_equivalents: $ap, rounds: [{round:0, voided:false, score:null}]}' > "$WS/harden/state.json"
}

# n_of <count> <file> <status> [start-line] : <count> entries for one file
n_of() {
  i=0
  while [ "$i" -lt "$1" ]; do
    m "$2" "$((${4:-1} + i))" "$3"
    i=$((i + 1))
  done
}

run_it() {
  snap="$(jq -nc --arg ws "$WS" --argjson t "${1:-80}" '{workspace:$ws, gate_threshold:$t, harden_round:0}')"
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

field() { printf '%s' "$output" | jq -r "$@"; }

# --- Happy path

@test "status mapping" {
  state '[]' "$(m a.go 1 killed)" "$(m a.go 2 timeout)" "$(m a.go 3 survived)" "$(m b.go 4 no_coverage)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .killed)" = "1" ]
  [ "$(field .timeout)" = "1" ]
  [ "$(field .survived)" = "1" ]
  [ "$(field .no_coverage)" = "1" ]
  [ "$(field .total)" = "4" ]
  [ "$(field .denominator)" = "4" ]
  # timeout counts as killed, no_coverage as survived: 2 of 4.
  [ "$(field .score)" = "50" ]
  [ "$(field .gate_passed)" = "false" ]
  [ "$(field -c .survivor_files)" = '["a.go","b.go"]' ]
}

@test "survivors-round lists every surviving mutant with its key" {
  state '[]' "$(m a.go 1 killed)" "$(m a.go 3 survived)" "$(m b.go 4 no_coverage)"
  run_it
  [ "$status" -eq 0 ]
  R="$WS/harden/survivors-round.json"
  [ "$(jq -r '[.survivors[].mutant_id] | join(",")' "$R")" = "a.go:3:M:r,b.go:4:M:r" ]
  [ "$(jq -r '.survivors[1].status' "$R")" = "no_coverage" ]
  [ "$(jq -r '.threshold' "$R")" = "80" ]
}

@test "the score is recorded on the current round" {
  state '[]' "$(m a.go 1 killed)" "$(m a.go 2 survived)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(jq -c '.rounds' "$WS/harden/state.json")" = '[{"round":0,"voided":false,"score":50}]' ]
}

# --- Edge cases

@test "compile errors excluded" {
  state '[]' "$(m a.go 1 killed)" "$(m a.go 2 excluded)" "$(m a.go 3 excluded)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .total)" = "1" ]
  [ "$(field .score)" = "100" ]
  [ "$(field .gate_passed)" = "true" ]
}

@test "equivalents shrink the denominator" {
  state '["a.go:2:M:r"]' "$(m a.go 1 killed)" "$(m a.go 2 survived)" "$(m a.go 3 survived)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .total)" = "3" ]
  [ "$(field .approved_equivalents)" = "1" ]
  [ "$(field .denominator)" = "2" ]
  [ "$(field .survived)" = "1" ]
  [ "$(field .score)" = "50" ]
  [ "$(jq -r '[.survivors[].mutant_id] | join(",")' "$WS/harden/survivors-round.json")" = "a.go:3:M:r" ]
}

@test "every remaining mutant an approved equivalent passes with its reason" {
  state '["a.go:1:M:r","a.go:2:M:r"]' "$(m a.go 1 survived)" "$(m a.go 2 survived)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "true" ]
  [ "$(field .denominator)" = "0" ]
  [ "$(field .reason)" = "all 2 remaining mutants approved equivalent" ]
}

@test "0 mutants gives null score and passes" {
  state '[]'
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "true" ]
  [ "$(field .denominator)" = "0" ]
  [ "$(field .reason)" = "no mutants" ]
  [ "$(field -c .survivor_files)" = "[]" ]
}

@test "every mutant excluded fails the gate with a reason" {
  state '[]' "$(m a.go 1 excluded)" "$(m a.go 2 excluded)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "false" ]
  [ "$(field .denominator)" = "0" ]
  [ "$(field .excluded)" = "2" ]
  [ "$(field .reason)" = "all 2 mutants excluded (compile/runtime errors or unknown statuses) — mutation build likely broken" ]
  [ "$(jq -r .gate_passed "$WS/harden/survivors-round.json")" = "false" ]
}

@test "a scored run has an empty reason" {
  state '[]' "$(m a.go 1 killed)" "$(m a.go 2 excluded)"
  run_it
  [ "$(field .reason)" = "" ]
  [ "$(field .excluded)" = "1" ]
}

@test "80.00 passes" {
  # shellcheck disable=SC2046
  state '[]' $(n_of 8 a.go killed 1) $(n_of 2 a.go survived 100)
  run_it 80
  [ "$status" -eq 0 ]
  [ "$(field .score)" = "80" ]
  [ "$(field .gate_passed)" = "true" ]
}

@test "79.99 fails" {
  # 7999 of 10000 is exactly 79.99.
  jq -n '{mutants: ([range(0; 10000)] | map({key: "a.go:\(.):M:r",
           value: {status: (if . < 7999 then "killed" else "survived" end), language:"go", tool:"gremlins",
                   file:"a.go", line:., mutator:"M", replacement:"r"} }) | from_entries),
          approved_equivalents: [], rounds: []}' > "$WS/harden/state.json"
  run_it 80
  [ "$status" -eq 0 ]
  [ "$(field .score)" = "79.99" ]
  [ "$(field .gate_passed)" = "false" ]
}

# --- Error cases

@test "missing state.json exits 1" {
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"state.json"* ]]
}
