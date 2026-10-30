#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# mutate.sh drives the real adapters against PATH stubs of gremlins and
# StrykerJS. Each stub serves report N on its Nth run and records what it saw.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/mutate.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WS="$T/ws"
  WORK="$WS/work"
  STUBS="$T/stubs"
  SYS="$T/sys"
  FX="$T/fx"
  mkdir -p "$WORK" "$STUBS" "$SYS" "$FX"
  # The tool stubs record what they saw beside the run (not inside it) and read
  # their fixtures from there; grant the test directory, as an operator could.
  export HARDEN_SANDBOX_RW="$T"
  unset HARDEN_SANDBOX_RO
  for u in sh jq git grep sed awk find sort tr head tail cat dirname basename mkdir rm mktemp ls cp mv tar wc cut uniq date env xargs ln uname readlink sandbox-exec bwrap; do
    p="$(command -v "$u")" && ln -sf "$p" "$SYS/$u"
  done
  git init -q -b main "$WORK"
  echo base > "$WORK/README"
  git -C "$WORK" add README
  git -C "$WORK" commit -q -m base
  BASE_SHA="$(git -C "$WORK" rev-parse HEAD)"
  mkdir -p "$WORK/pkg" "$WORK/web"
  echo 'module toy' > "$WORK/go.mod"
  echo 'package pkg' > "$WORK/pkg/a.go"
  echo 'package pkg' > "$WORK/pkg/b.go"
  echo '{}' > "$WORK/web/package.json"
  echo x > "$WORK/web/x.js"
  echo y > "$WORK/web/y.js"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m feature

  cat > "$STUBS/gremlins" <<EOF
#!/bin/sh
n=\$(( \$(cat '$T/gremlins.count' 2>/dev/null || echo 0) + 1 ))
echo "\$n" > '$T/gremlins.count'
while [ \$# -gt 0 ]; do [ "\$1" = -o ] && o="\$2"; shift; done
cp "$FX/gremlins.\$n.json" "\$o"
EOF
  cat > "$STUBS/stryker" <<EOF
#!/bin/sh
n=\$(( \$(cat '$T/stryker.count' 2>/dev/null || echo 0) + 1 ))
echo "\$n" > '$T/stryker.count'
cp "\$2" "$T/stryker.\$n.cfg"
cp "$FX/stryker.\$n.json" "\$(jq -r .jsonReporter.fileName "\$2")"
EOF
  chmod +x "$STUBS/gremlins" "$STUBS/stryker"

  # Run 1: a.go one kill, one survivor, one skipped; b.go no coverage.
  # x.js one kill, one survivor; y.js a timeout and a compile error.
  gr 1 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"KILLED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":4,"column":9},
          {"type":"CONDITIONALS_NEGATION","status":"SKIPPED","line":9,"column":1}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 1 '"x.js": {"mutants":[
          {"mutatorName":"ArithmeticOperator","replacement":"a - b","status":"Killed","location":{"start":{"line":1,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}}]},
        "y.js": {"mutants":[
          {"mutatorName":"StringLiteral","replacement":"\"\"","status":"Timeout","location":{"start":{"line":1,"column":1}}},
          {"mutatorName":"EqualityOperator","replacement":"a != b","status":"CompileError","location":{"start":{"line":2,"column":1}}}]}'
}

# gr <n> <files-json-body> : gremlins report for run n
gr() { printf '{"files":[%s]}' "$2" > "$FX/gremlins.$1.json"; }
# sj <n> <files-json-body> : StrykerJS report for run n
sj() { printf '{"schemaVersion":"1","files":{%s}}' "$2" > "$FX/stryker.$1.json"; }

LANGS='[
  {"language":"go","tool":"gremlins","status":"runnable","targets":["pkg/a.go","pkg/b.go"],"test_command":"go test ./..."},
  {"language":"js","tool":"StrykerJS","status":"runnable","targets":["web/x.js","web/y.js"],"test_command":"npm test"},
  {"language":"cobol","tool":"","status":"skipped","targets":[],"test_command":""}]'

run_it() {
  snap="$(jq -nc --arg w "$WORK" --arg ws "$WS" --arg mb "$BASE_SHA" --argjson l "$LANGS" --argjson r "${2:-0}" \
    '{work_dir:$w, workspace:$ws, harden_round:$r, scope:{merge_base:$mb}, detect:{languages:$l} }')"
  run --separate-stderr env PATH="$STUBS:$SYS" sh -c 'printf "%s" "$1" | sh "$2" $3' _ "$snap" "$SCRIPT" "${1-}"
}

st() { jq -r "$@" "$WS/harden/state.json"; }
key_status() { jq -r --arg k "$1" '.mutants[$k].status' "$WS/harden/state.json"; }

# --- Happy path

@test "dispatches per runnable language and normalizes" {
  run_it
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st '.mutants | length')" = "7" ]
  [ "$(st -c '.mutants["pkg/a.go:3:5:ARITHMETIC_BASE:col:5"]')" = '{"status":"killed","language":"go","tool":"gremlins","file":"pkg/a.go","line":3,"col":5,"mutator":"ARITHMETIC_BASE","replacement":"col:5"}' ]
  [ "$(key_status 'pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9')" = "survived" ]
  [ "$(key_status 'pkg/b.go:2:1:INVERT_NEGATIVES:col:1')" = "no_coverage" ]
  [ "$(st -c '.mutants["web/x.js:2:1:ConditionalExpression:true"]')" = '{"status":"survived","language":"js","tool":"StrykerJS","file":"web/x.js","line":2,"col":1,"mutator":"ConditionalExpression","replacement":"true"}' ]
  [ "$(key_status 'web/y.js:1:1:StringLiteral:""')" = "timeout" ]
  [ "$(key_status 'web/y.js:2:1:EqualityOperator:a != b')" = "excluded" ]
  [ "$(st -c .approved_equivalents)" = "[]" ]
  [ "$(st -c .rounds)" = '[{"round":0,"voided":false,"score":null}]' ]
  # Every scored file's blob is recorded: it is the proof an approval needs later.
  [ "$(st -r '.file_blobs["pkg/a.go"]')" = "$(git -C "$WORK" rev-parse HEAD:pkg/a.go)" ]
  [ "$(st -c '.file_blobs | keys')" = '["pkg/a.go","pkg/b.go","web/x.js","web/y.js"]' ]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"mutants":7,"killed":2,"timeout":1,"survived":2,"no_coverage":1,"excluded":1,"languages":["go","js"]}' ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- Rescore: fresh evidence is authoritative

@test "--survivors-only replaces the result set of every requested file and keeps the rest" {
  run_it
  [ "$status" -eq 0 ]
  # Run 2: the earlier kill now reports LIVED (fresh evidence stands), the
  # survivor is now killed, b.go still has no coverage.
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"LIVED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"KILLED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ArithmeticOperator","replacement":"a - b","status":"Survived","location":{"start":{"line":1,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Killed","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 1
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  # y.js has no survivor, so StrykerJS was asked for x.js only.
  [ "$(jq -c .mutate "$T/stryker.2.cfg")" = '["x.js"]' ]
  [ "$(key_status 'pkg/a.go:3:5:ARITHMETIC_BASE:col:5')" = "survived" ]
  [ "$(key_status 'pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9')" = "killed" ]
  [ "$(key_status 'pkg/b.go:2:1:INVERT_NEGATIVES:col:1')" = "no_coverage" ]
  [ "$(key_status 'web/x.js:1:1:ArithmeticOperator:a - b')" = "survived" ]
  [ "$(key_status 'web/x.js:2:1:ConditionalExpression:true')" = "killed" ]
  # y.js was not requested: its earlier results stand.
  [ "$(key_status 'web/y.js:1:1:StringLiteral:""')" = "timeout" ]
  [ "$(st '.mutants | length')" = "7" ]
  [ "$(st -c '[.rounds[].round]')" = '[0,2]' ]
}

@test "a rescored file whose fresh result is empty clears its stale mutants" {
  run_it
  [ "$status" -eq 0 ]
  gr 2 '{"file_name":"pkg/a.go","mutations":[]},{"file_name":"pkg/b.go","mutations":[]}'
  sj 2 '"x.js": {"mutants":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st '[.mutants[] | select(.file == "pkg/a.go" or .file == "pkg/b.go" or .file == "web/x.js")] | length')" = "0" ]
  [ "$(st '.mutants | length')" = "2" ]
  [ "$(key_status 'web/y.js:1:1:StringLiteral:""')" = "timeout" ]
}

# A permitted inline Rust test module can shift unchanged production code to a
# new line. The shifted survivor must not inherit the kill (or the approval) that
# belonged to whatever mutant used to live at its new line.
@test "a line shift between rounds leaves no stale survivor and inherits no kill" {
  run_it
  [ "$status" -eq 0 ]
  # a.go's survivor (line 4, col 9) moves to line 14; line 3's old kill is gone.
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":14,"column":9},
          {"type":"ARITHMETIC_BASE","status":"LIVED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[]}'
  sj 2 '"x.js": {"mutants":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st '[.mutants | to_entries[] | select(.value.file == "pkg/a.go")] | length')" = "2" ]
  [ "$(st '.mutants["pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9"] // "gone"')" = "gone" ]
  [ "$(key_status 'pkg/a.go:14:9:CONDITIONALS_BOUNDARY:col:9')" = "survived" ]
  # The mutant now at line 4 is a different one: it does not inherit the old
  # line-4 survivor's key nor any earlier kill.
  [ "$(key_status 'pkg/a.go:4:9:ARITHMETIC_BASE:col:9')" = "survived" ]
}

@test "an approval survives a rescore of its file only while the file and the key are unchanged" {
  run_it
  [ "$status" -eq 0 ]
  jq '.approved_equivalents = ["pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9"]' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  # Only files with an UNAPPROVED survivor are rescored: give a.go another one.
  jq '.mutants["pkg/a.go:3:5:ARITHMETIC_BASE:col:5"].status = "survived"' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"KILLED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 2 '"x.js": {"mutants":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  # Same blob, key still reported: the approval stands.
  [ "$(st -c .approved_equivalents)" = '["pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9"]' ]

  # The same rescore after a.go's source changed: identity is unproven, so the
  # approval is dropped even though the key is reported again.
  jq '.mutants["pkg/a.go:3:5:ARITHMETIC_BASE:col:5"].status = "survived"' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  echo '// shifted' >> "$WORK/pkg/a.go"
  git -C "$WORK" commit -q -am "inline test module"
  gr 3 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"KILLED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 3 '"x.js": {"mutants":[]}'
  run_it --survivors-only 1
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
}

@test "an approval whose key is no longer reported is dropped" {
  run_it
  [ "$status" -eq 0 ]
  jq '.approved_equivalents = ["pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9"]' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  jq '.mutants["pkg/a.go:3:5:ARITHMETIC_BASE:col:5"].status = "survived"' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"LIVED","line":3,"column":5}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 2 '"x.js": {"mutants":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
}

@test "an approval in a file that was not requested stands" {
  run_it
  [ "$status" -eq 0 ]
  jq '.approved_equivalents = ["web/x.js:2:1:ConditionalExpression:true"]' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[]},{"file_name":"pkg/b.go","mutations":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '["web/x.js:2:1:ConditionalExpression:true"]' ]
}

@test "an approval from a state without file_blobs is dropped on rescore of its file" {
  run_it
  [ "$status" -eq 0 ]
  jq 'del(.file_blobs) | .approved_equivalents = ["pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9"]
      | .mutants["pkg/a.go:3:5:ARITHMETIC_BASE:col:5"].status = "survived"' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"LIVED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[]}'
  sj 2 '"x.js": {"mutants":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
}

# Identical keys within one tool's results are told apart only by their #N
# ordinal, which is report order. A rescore that reports them in another order
# would move an approval onto a different mutant, so an approval on any member
# of a collided key group is dropped on rescore and the mutant counts again.
CK='web/x.js:2:1:ConditionalExpression:true'
collided_round1() {
  sj 1 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"StringLiteral","replacement":"\"\"","status":"Survived","location":{"start":{"line":5,"column":3}}}]}'
  run_it
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  gr 2 '{"file_name":"pkg/a.go","mutations":[]},{"file_name":"pkg/b.go","mutations":[]}'
}
approve() { jq --argjson a "$1" '.approved_equivalents = $a' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"; }
score_it() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2"' _ "$(jq -nc --arg ws "$WS" '{workspace:$ws, gate_threshold:80}')" "$BATS_TEST_DIRNAME/score.sh"
}

@test "regression: a reordered collided key does not move an approval onto a survivor" {
  collided_round1
  # The first ConditionalExpression is the equivalent one and is approved; the
  # second is a real survivor. The rescore reports them the other way round, and
  # the equivalent one times out this time.
  approve "[\"$CK\"]"
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Timeout","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
  [[ "$stderr" == *"approval"*"$CK"* ]]
  score_it
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  # Without the fix this was 100.00 and passed: the survivor inherited the approval.
  [ "$(printf '%s' "$output" | jq -c '[.score, .gate_passed, .survived, .approved_equivalents]')" = '[50,false,1,0]' ]
}

@test "an approval on the #N member of a collided key is dropped on rescore" {
  collided_round1
  approve "[\"$CK#2\"]"
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
  [ "$(key_status "$CK#2")" = survived ]
}

@test "an approval on a non-colliding key in the same rescored file stands" {
  collided_round1
  approve "[\"web/x.js:5:3:StringLiteral:\\\"\\\"\", \"$CK\"]"
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"StringLiteral","replacement":"\"\"","status":"Survived","location":{"start":{"line":5,"column":3}}}]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '["web/x.js:5:3:StringLiteral:\"\""]' ]
}

@test "a key that collides only in the fresh results loses its approval" {
  run_it
  [ "$status" -eq 0 ]
  approve "[\"$CK\"]"
  # Give x.js an unapproved survivor so it is rescored.
  jq '.mutants["web/x.js:1:1:ArithmeticOperator:a - b"].status = "survived"' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[]},{"file_name":"pkg/b.go","mutations":[]}'
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
}

@test "a key that collided only in the earlier results loses its approval" {
  collided_round1
  approve "[\"$CK\"]"
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(st -c .approved_equivalents)" = '[]' ]
  [ "$(key_status "$CK")" = survived ]
}

@test "--survivors-only skips a language with no survivors" {
  run_it
  [ "$status" -eq 0 ]
  # Every JS mutant is now killed or excluded.
  jq '.mutants |= with_entries(if .value.language == "js" and .value.status == "survived" then .value.status = "killed" else . end)' \
    "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/stryker.count")" = "1" ]
  [ "$(cat "$T/gremlins.count")" = "2" ]
}

@test "an approved equivalent is not a survivor file" {
  run_it
  [ "$status" -eq 0 ]
  jq '.approved_equivalents = ["web/x.js:2:1:ConditionalExpression:true"]' "$WS/harden/state.json" > "$T/s" && mv "$T/s" "$WS/harden/state.json"
  gr 2 '{"file_name":"pkg/a.go","mutations":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ]
  [ "$(cat "$T/stryker.count")" = "1" ]
}

@test "mutants on one line that differ only by column are distinct keys" {
  # Two ConditionalExpression -> true mutants on line 2 of x.js, columns 1 and 9.
  sj 1 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Killed","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":9}}}]}'
  run_it
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(key_status 'web/x.js:2:1:ConditionalExpression:true')" = "killed" ]
  [ "$(key_status 'web/x.js:2:9:ConditionalExpression:true')" = "survived" ]
  [[ "$stderr" != *"collision"* ]]
  # Reordered per-run reports key to the same mutants.
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":9}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Killed","location":{"start":{"line":2,"column":1}}}]}'
  gr 2 '{"file_name":"pkg/a.go","mutations":[]}'
  run_it --survivors-only 0
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(key_status 'web/x.js:2:1:ConditionalExpression:true')" = "killed" ]
  [ "$(key_status 'web/x.js:2:9:ConditionalExpression:true')" = "survived" ]
}

@test "an identical key inside one tool's results is kept as distinct mutants (#N)" {
  sj 1 '"x.js": {"mutants":[
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Killed","location":{"start":{"line":2,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":1}}}]}'
  run_it
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(key_status 'web/x.js:2:1:ConditionalExpression:true')" = "killed" ]
  [ "$(key_status 'web/x.js:2:1:ConditionalExpression:true#2')" = "survived" ]
  [ "$(st -c '.mutants["web/x.js:2:1:ConditionalExpression:true#2"] | [.file, .line, .col, .replacement]')" = '["web/x.js",2,1,"true"]' ]
  [[ "$stderr" == *"collision"* ]]
}

# --- Error cases

@test "an adapter failure exits 1" {
  rm "$FX/stryker.1.json"
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"js"* ]]
}

@test "--survivors-only without state.json exits 1" {
  run_it --survivors-only
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"state.json"* ]]
}

@test "an unknown argument exits 1" {
  run_it --bogus
  [ "$status" -eq 1 ]
}

# --- Isolation: the first run and every rescore run the tools sandboxed ----------------

# hostilize <stub>: make the stub behave like hostile repository code first.
hostilize() {
  { echo '#!/bin/sh'; printf '%s\n' "$HOSTILE_PRE"; sed 1d "$STUBS/$1"; } > "$STUBS/$1.new"
  mv "$STUBS/$1.new" "$STUBS/$1"
  chmod +x "$STUBS/$1"
}

hostile_run_setup() {
  DUMMY=dummy-token-9b21
  export GH_TOKEN="$DUMMY" GITHUB_TOKEN="$DUMMY" ANTHROPIC_API_KEY="$DUMMY" CLAUDE_CONFIG_DIR="$T/fakehome/.claude" SSH_AUTH_SOCK="$T/agent.sock"
  FAKEHOME="$T/fakehome"; SIBLING="$T/siblings/inst2"
  mkdir -p "$FAKEHOME/.ssh" "$SIBLING"
  echo ssh-secret > "$FAKEHOME/.ssh/id_ed25519"; echo sibling-secret > "$SIBLING/secret"
  export HOME="$FAKEHOME"
  # Only the stubs' own counters are granted (not the test directory), so the
  # sibling and the fake HOME beside the run stay out of reach.
  : > "$T/gremlins.count"; : > "$T/stryker.count"
  export HARDEN_SANDBOX_RW="$T/gremlins.count:$T/stryker.count"
  export HARDEN_SANDBOX_RO="$FX"
  HOSTILE_PRE="{ echo HOSTILE-ENV-BEGIN; env; echo HOSTILE-ENV-END; } >&2
for t in '$SIBLING/planted' '$FAKEHOME/planted'; do
  if ( : > \"\$t\" ) 2>/dev/null; then echo \"HOSTILE-WRITE:\$t:WROTE\" >&2; else echo \"HOSTILE-WRITE:\$t:denied\" >&2; fi
done
for t in '$SIBLING/secret' '$FAKEHOME/.ssh/id_ed25519'; do
  if cat \"\$t\" >/dev/null 2>&1; then echo \"HOSTILE-READ:\$t:READ\" >&2; else echo \"HOSTILE-READ:\$t:denied\" >&2; fi
done"
  hostilize gremlins
  hostilize stryker
}

assert_run_isolated() {
  [ "$status" -eq 0 ] || { echo "exit $status: $stderr"; return 1; }
  [[ "$stderr" == *HOSTILE-ENV-BEGIN* ]] || { echo "no tool ran: $stderr"; return 1; }
  [[ "$stderr" != *"$DUMMY"* ]] || { echo "a token reached a tool"; return 1; }
  [[ "$stderr" != *":WROTE"* && "$stderr" != *":READ"* ]] || { echo "$stderr"; return 1; }
  [ ! -e "$SIBLING/planted" ] && [ ! -e "$FAKEHOME/planted" ]
}

@test "isolation: the first run and a --survivors-only rescore both run the tools sandboxed" {
  hostile_run_setup
  run_it
  assert_run_isolated
  [ "$(st '.mutants | length')" = "7" ]
  gr 2 '{"file_name":"pkg/a.go","mutations":[
          {"type":"ARITHMETIC_BASE","status":"LIVED","line":3,"column":5},
          {"type":"CONDITIONALS_BOUNDARY","status":"KILLED","line":4,"column":9}]},
        {"file_name":"pkg/b.go","mutations":[
          {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1}]}'
  sj 2 '"x.js": {"mutants":[
          {"mutatorName":"ArithmeticOperator","replacement":"a - b","status":"Survived","location":{"start":{"line":1,"column":1}}},
          {"mutatorName":"ConditionalExpression","replacement":"true","status":"Killed","location":{"start":{"line":2,"column":1}}}]}'
  run_it --survivors-only 1
  assert_run_isolated
  # The rescore really ran (and replaced the earlier result), under the sandbox.
  [ "$(key_status 'pkg/a.go:4:9:CONDITIONALS_BOUNDARY:col:9')" = "killed" ]
}

@test "isolation: with no sandbox available the first run and the rescore both refuse" {
  hostile_run_setup
  rm -f "$SYS/sandbox-exec" "$SYS/bwrap"
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no isolation available"* ]]
  [[ "$stderr" != *HOSTILE-ENV-BEGIN* ]]
  [ ! -s "$T/gremlins.count" ]
}
