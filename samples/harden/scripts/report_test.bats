#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/report.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WS="$T/ws"
  WORK="$WS/work"
  mkdir -p "$WS/harden"
  git init -q -b feat "$WORK"
  echo x > "$WORK/a.go"
  git -C "$WORK" add a.go
  git -C "$WORK" commit -q -m base
  HJ=".arbitron/pipeline/s.harden.json"
  SJ=".arbitron/pipeline/s.survivors.json"
  LANGS='[{"language":"go","tool":"gremlins","version":"0.6.0","version_status":"supported","version_warning":"","status":"runnable","reason":"","targets":["a.go"],"test_command":"go test ./...","test_command_source":"registry-default"}]'
}

# mutant <file> <line> <status>
mutant() {
  jq -nc --arg f "$1" --argjson l "$2" --arg s "$3" \
    '{key: "\($f):\($l):M:r", value: {status:$s, language:"go", tool:"gremlins", file:$f, line:$l, mutator:"M", replacement:"r"}}'
}

# state <entry>... : state.json with one scored round
state() {
  printf '%s\n' "$@" | jq -s '{mutants: from_entries, approved_equivalents: [], rounds: [{round:0, voided:false, score:null}]}' \
    > "$WS/harden/state.json"
}

# files <n> : n files, file i has i survivors, plus one kill each
files() {
  i=1
  while [ "$i" -le "$1" ]; do
    j=1
    mutant "f$i.go" 1000 killed
    while [ "$j" -le "$i" ]; do mutant "f$i.go" "$j" survived; j=$((j + 1)); done
    i=$((i + 1))
  done
}

# snap [runnable_count] [skip_reason] [harden_round]
snap() {
  jq -nc --arg w "$WORK" --arg ws "$WS" --argjson rc "${1:-1}" --arg sr "${2-}" --argjson r "${3:-0}" --argjson langs "$LANGS" \
    '{work_dir:$w, workspace:$ws, slug:"s", gate_threshold:80, harden_round:$r, max_rounds:3,
      scope:{base_ref:"main", merge_base:"abc123"},
      detect:{languages:$langs, runnable_count:$rc, skip_reason:$sr} }'
}

run_it() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

field() { printf '%s' "$output" | jq -r "$@"; }
seed() { jq -r "$@" "$WORK/$SJ"; }

# --- Happy path

@test "one item per file" {
  state "$(mutant a.go 1 killed)" "$(mutant a.go 2 survived)" "$(mutant b.go 3 survived)" "$(mutant b.go 4 no_coverage)"
  run_it "$(snap 1 '' 2)"
  [ "$status" -eq 0 ]
  [ "$(field .result)" = "scored" ]
  [ "$(field .score)" = "25" ]
  [ "$(field .gate_passed)" = "false" ]
  [ "$(field .survivors_path)" = "$SJ" ]
  [ "$(field .focus_seed_path)" = "$SJ" ]
  [ "$(field .harden_report_path)" = "$HJ" ]
  # Sorted by survivor count, descending.
  [ "$(seed '[.focus_items[].id] | join(",")')" = "harden:b.go,harden:a.go" ]
  [ "$(seed '.focus_items[0].file')" = "b.go" ]
  [ "$(seed -c '.focus_items[0].mutants[0]')" = '{"line":3,"mutator":"M","replacement":"r"}' ]
  [ "$(seed .summary)" = "mutation score 25.00% (gate 80%) after 2 rounds; 3 survivors in 2 files" ]
  [ "$(seed .score)" = "25" ]
  [ "$(seed .threshold)" = "80" ]
  [ "$(seed .gate_passed)" = "false" ]
}

@test "harden.json records the run" {
  state "$(mutant a.go 1 killed)" "$(mutant a.go 2 survived)"
  run_it "$(snap 1 '' 1)"
  [ "$status" -eq 0 ]
  H="$WORK/$HJ"
  [ "$(jq -r '[.slug, .base_ref, .merge_base, .threshold, .result, .score, .gate_passed] | map(tostring) | join(" ")' "$H")" = "s main abc123 80 scored 50 false" ]
  [ "$(jq -c '.languages[0]' "$H")" = '{"language":"go","tool":"gremlins","version":"0.6.0","version_status":"supported","version_warning":"","status":"runnable","reason":"","test_command":"go test ./...","test_command_source":"registry-default"}' ]
  [ "$(jq -r '.totals.killed, .totals.survived, .totals.denominator' "$H" | tr '\n' ' ')" = "1 1 2 " ]
  # Round 0 was scored; round 1 never reached score-r, so it was voided.
  [ "$(jq -c '.rounds' "$H")" = '[{"round":0,"voided":false,"score":50},{"round":1,"voided":true,"score":null}]' ]
  [ "$(jq -r '.finished_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")' "$H")" = "true" ]
}

@test "both files committed" {
  state "$(mutant a.go 1 killed)" "$(mutant a.go 2 survived)"
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
  [ "$(git -C "$WORK" show --name-only --format= HEAD | sort | tr '\n' ' ')" = "$HJ $SJ " ]
  [ "$(git -C "$WORK" rev-list --count HEAD)" = "2" ]
}

# --- Edge cases

@test "26 files yields 25 items ending harden:other" {
  # shellcheck disable=SC2046
  state $(files 26)
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(seed '.focus_items | length')" = "25" ]
  [ "$(seed '.focus_items[0].id')" = "harden:f26.go" ]
  [ "$(seed '.focus_items[23].id')" = "harden:f3.go" ]
  [ "$(seed '.focus_items[24].id')" = "harden:other" ]
  [ "$(seed -c '.focus_items[24].files')" = '["f2.go","f1.go"]' ]
  [ "$(seed '.focus_items[24].mutants | length')" = "3" ]
  [ "$(seed -c '.focus_items[24].mutants[2]')" = '{"file":"f1.go","line":1,"mutator":"M","replacement":"r"}' ]
}

@test "25 files yields 25 items with no harden:other" {
  # shellcheck disable=SC2046
  state $(files 25)
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(seed '.focus_items | length')" = "25" ]
  [ "$(seed '[.focus_items[].id] | index("harden:other")')" = "null" ]
}

@test "focus_seed_path empty when passed" {
  state "$(mutant a.go 1 killed)" "$(mutant a.go 2 killed)" "$(mutant a.go 3 killed)" "$(mutant a.go 4 killed)" "$(mutant a.go 5 survived)"
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(field .gate_passed)" = "true" ]
  [ "$(field .score)" = "80" ]
  [ "$(field .focus_seed_path)" = "" ]
  [ "$(field .survivors_path)" = "$SJ" ]
  [ -f "$WORK/$SJ" ]
  [ "$(seed '.focus_items | length')" = "1" ]
}

@test "no mutants: scored, null score, gate passed, reason recorded" {
  state
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(field .result)" = "scored" ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "true" ]
  [ "$(field .skip_reason)" = "no mutants" ]
  [ "$(field .focus_seed_path)" = "" ]
  [ "$(seed '.focus_items | length')" = "0" ]
}

@test "all mutants excluded: gate failed, reason in the summary" {
  state "$(mutant a.go 1 excluded)" "$(mutant b.go 2 excluded)"
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(field .result)" = "scored" ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "false" ]
  R="all 2 mutants excluded (compile/runtime errors or unknown statuses) — mutation build likely broken"
  [ "$(field .skip_reason)" = "$R" ]
  [ "$(field .focus_seed_path)" = "$SJ" ]
  [ "$(seed .summary)" = "harden gate failed: $R" ]
  [ "$(seed -c .focus_items)" = "[]" ]
  [ "$(jq -r .totals.excluded "$WORK/$HJ")" = "2" ]
}

@test "skipped run: result skipped, score null, gate_passed true, empty focus_items" {
  LANGS='[{"language":"cobol","tool":"","version":"","status":"skipped","reason":"no mutation tool registered for cobol","targets":[],"test_command":"","test_command_source":""}]'
  run_it "$(snap 0 'no mutation tool registered for cobol')"
  [ "$status" -eq 0 ]
  [ "$(field .result)" = "skipped" ]
  [ "$(field .skip_reason)" = "no mutation tool registered for cobol" ]
  [ "$(field .score)" = "null" ]
  [ "$(field .gate_passed)" = "true" ]
  [ "$(field .focus_seed_path)" = "" ]
  [ "$(seed -c .focus_items)" = "[]" ]
  [ "$(seed .summary)" = "harden skipped: no mutation tool registered for cobol" ]
  [ "$(jq -r '.languages[0].reason' "$WORK/$HJ")" = "no mutation tool registered for cobol" ]
  [ "$(jq -r '.result' "$WORK/$HJ")" = "skipped" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- Error cases

@test "a slug with a path separator exits 1" {
  state "$(mutant a.go 1 killed)"
  run_it "$(snap | jq -c '.slug = "../x"')"
  [ "$status" -eq 1 ]
  [ ! -e "$WS/x.harden.json" ]
}

@test "a scored run without state.json exits 1" {
  run_it "$(snap)"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"state.json"* ]]
}

@test "a snapshot without detect exits 1" {
  state "$(mutant a.go 1 killed)"
  run_it "$(snap | jq -c 'del(.detect)')"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"detect"* ]]
  [ ! -e "$WORK/$HJ" ]
}

@test "a snapshot without detect.runnable_count exits 1" {
  run_it "$(snap | jq -c 'del(.detect.runnable_count)')"
  [ "$status" -eq 1 ]
  [ ! -e "$WORK/$HJ" ]
}
