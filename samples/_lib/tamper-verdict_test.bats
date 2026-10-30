#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Each test builds a repo in setup(), commits a BASE, mutates it, runs the real
# tamper gate (so findings.json is genuine), writes a verdict fixture, then runs
# tamper-verdict.

setup() {
  LIB="$BATS_TEST_DIRNAME"
  GATE="$LIB/tamper-gate.sh"
  SCRIPT="$LIB/tamper-verdict.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WS"
  git init -q -b main "$WORK"
  mkdir -p "$WORK/pkg" "$WORK/docs/plans/x"
  printf 'package pkg\n\nfunc A() int { return 1 }\n' > "$WORK/pkg/a.go"
  printf 'package pkg\n\nfunc TestA(t *testing.T) {\n\tt.Errorf("a")\n\tt.Errorf("b")\n}\n' > "$WORK/pkg/a_test.go"
  printf 'package pkg\n\nfunc TestB(t *testing.T) {\n\tt.Errorf("a")\n}\n' > "$WORK/pkg/b_test.go"
  printf 'plan a\n' > "$WORK/docs/plans/x/a.md"
  printf 'notes\n' > "$WORK/notes.txt"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m base
  BASE="$(git -C "$WORK" rev-parse HEAD)"
}

change() { git -C "$WORK" add -A; git -C "$WORK" commit -q -m "${1:-change}"; }

# gate [args]: runs the real gate over BASE..HEAD.
gate() {
  jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws}' \
    | sh "$GATE" "$BASE" HEAD "$@" > "$T/gate.out"
}

# verdict_json <json>
verdict_json() { mkdir -p "$WS/tamper"; printf '%s' "$1" > "$WS/tamper/verdict.json"; }

# snapshot [extra-json]
snap() {
  jq -n --arg w "$WORK" --arg ws "$WS" --argjson extra "${1:-{\}}" \
    '{work_dir:$w, workspace:$ws, "pass-mark":{sha:"x", pass_id:7}} + $extra'
}

# vrun <snapshot> <args...>
vrun() {
  local s="$1"; shift
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$s" "$SCRIPT" "$@"
}

# verdict <args...>: default snapshot.
verdict() { vrun "$(snap)" "$BASE" "$@"; }

j() { printf '%s' "$output" | jq -r "$1"; }

exists() { [ -e "$WORK/$1" ]; }
commits() { git -C "$WORK" rev-list --count "$BASE..HEAD"; }

# tamper_test_delete: deletes b_test.go (finding T1) and commits.
tamper_delete() { git -C "$WORK" rm -q pkg/b_test.go; change "drop test"; }

# --- Happy path --------------------------------------------------------------

@test "all legitimate: voided false, nothing restored, must-fix removed" {
  tamper_delete
  gate
  mkdir -p "$WS/tamper"; echo old > "$WS/tamper/must-fix.md"
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"plan removed it"}]}'
  verdict
  [ "$status" -eq 0 ]
  [ "$(j .voided)" = false ]
  [ "$(j '.restored_files | length')" = 0 ]
  [ ! -e "$WS/tamper/must-fix.md" ]
  ! exists pkg/b_test.go
  [ "$(commits)" = 1 ]
}

# --- Void --------------------------------------------------------------------

@test "illegitimate ruling restores gated categories" {
  tamper_delete
  printf 'package pkg\n\nfunc TestNew(t *testing.T) {\n\tt.Skip("x")\n}\n' > "$WORK/pkg/new_test.go"
  change "add skipped test"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"},{"id":"T2","legitimate":false,"reason":"no"}]}'
  verdict
  [ "$status" -eq 0 ]
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = illegitimate_ruling ]
  exists pkg/b_test.go
  ! exists pkg/new_test.go
  [ "$(j '.restored_files | sort | join(",")')" = "pkg/b_test.go,pkg/new_test.go" ]
}

@test "--restore and --restore-json paths restored" {
  tamper_delete
  printf 'plan changed\n' > "$WORK/docs/plans/x/a.md"
  printf 'notes changed\n' > "$WORK/notes.txt"
  change "touch plan and notes"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  vrun "$(snap '{"p":{"paths":["notes.txt"]}}')" "$BASE" --restore docs/plans/x/a.md --restore-json p.paths
  [ "$(j .voided)" = true ]
  [ "$(cat "$WORK/docs/plans/x/a.md")" = "plan a" ]
  [ "$(cat "$WORK/notes.txt")" = notes ]
}

@test "revert commit created" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict
  [ "$(commits)" = 2 ]
  [[ "$(git -C "$WORK" log -1 --format=%s)" == "revert(tamper): void pass "* ]]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

@test "must-fix lists the finding" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"tests were load bearing"}]}'
  verdict
  [ "$(j .must_fix_path)" = "$WS/tamper/must-fix.md" ]
  head -1 "$WS/tamper/must-fix.md" | grep -q 'MUST FIX before anything else this pass'
  grep -q 'T1 (test_deleted, pkg/b_test.go): tests were load bearing' "$WS/tamper/must-fix.md"
}

@test "--gaps-file appends TAMPER lines in the same commit" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict --gaps-file docs/gaps.md
  grep -q '^- \[ \] TAMPER T1 (' "$WORK/docs/gaps.md"
  [ "$(commits)" = 2 ]
  git -C "$WORK" show --stat --format= HEAD | grep -q 'docs/gaps.md'
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- Edge cases --------------------------------------------------------------

@test "--reset hard-resets" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict --reset
  [ "$(j .voided)" = true ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
  exists pkg/b_test.go
}

@test "--force-void without verdict voids" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate
  rm -f "$WS/tamper/verdict.json"
  verdict --force-void
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = force_void ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
}

@test "--reason names a forced void in the output and must-fix" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  rm -rf "$WS/tamper"
  verdict --force-void --reason uncommitted_changes_discarded
  [ "$status" -eq 0 ]
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = uncommitted_changes_discarded ]
  grep -q 'voided: uncommitted_changes_discarded' "$WS/tamper/must-fix.md"
  grep -q 'voided as a whole (uncommitted_changes_discarded)' "$WS/tamper/must-fix.md"
}

@test "--reason without --force-void exits 1" {
  verdict --reason x
  [ "$status" -eq 1 ]
}

@test "--reason must be a plain word" {
  verdict --force-void --reason 'a b'
  [ "$status" -eq 1 ]
}

@test "source_changed from the gate hard-resets" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate --tests-only
  [ "$(jq -r .force_void "$T/gate.out")" = true ]
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = source_changed ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
}

@test "history_rewritten hard-resets without --reset and a later plain push fast-forwards" {
  git init -q --bare "$T/remote.git"
  git -C "$WORK" remote add origin "$T/remote.git"
  git -C "$WORK" push -q origin main
  # Amend the pass base itself, so BASE is no longer an ancestor of HEAD.
  git -C "$WORK" commit -q --amend -m "rewritten base"
  gate
  [ "$(jq -r .history_rewritten "$T/gate.out")" = true ]
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = history_rewritten ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
  printf 'again\n' >> "$WORK/notes.txt"; change "next pass"
  git -C "$WORK" push -q origin main
}

@test "--clear removes scratch only on void" {
  mkdir -p "$WS/scratch/deep"; echo s > "$WS/scratch/deep/f"
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"ok"}]}'
  verdict --clear scratch
  [ -e "$WS/scratch/deep/f" ]
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict --clear scratch
  [ ! -e "$WS/scratch" ]
}

@test "--clear refuses paths escaping the workspace" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  echo keep > "$T/keep"
  verdict --clear ../keep
  [ -e "$T/keep" ]
}

@test "nothing to restore makes no empty commit" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  # A manual revert already put the state back before the verdict ran.
  git -C "$WORK" checkout -q "$BASE" -- pkg/b_test.go; change "manual restore"
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j '.restored_files | length')" = 0 ]
  [ "$(commits)" = 2 ]
  [ "$(git -C "$WORK" log -1 --format=%s)" = "manual restore" ]
}

@test "unrelated uncommitted edits survive a void and are not committed" {
  tamper_delete
  gate
  printf 'dirty\n' >> "$WORK/notes.txt"
  printf 'untracked\n' > "$WORK/scratch.txt"
  printf 'wip\n' >> "$WORK/pkg/a.go"
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict
  [ "$(j .voided)" = true ]
  exists pkg/b_test.go
  [ "$(tail -1 "$WORK/notes.txt")" = dirty ]
  [ "$(cat "$WORK/scratch.txt")" = untracked ]
  [ "$(tail -1 "$WORK/pkg/a.go")" = wip ]
  ! git -C "$WORK" show --stat --format= HEAD | grep -q 'notes.txt\|scratch.txt\|a.go'
  git -C "$WORK" status --porcelain | grep -q 'notes.txt'
}

@test "a renamed test file restores the old path and removes the new one" {
  git -C "$WORK" mv pkg/b_test.go pkg/c_test.go; change "rename"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict
  exists pkg/b_test.go
  ! exists pkg/c_test.go
}

# --- Errors ------------------------------------------------------------------

@test "missing verdict fails closed" {
  tamper_delete
  gate
  verdict
  [ "$status" -eq 0 ]
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = verdict_missing ]
  exists pkg/b_test.go
}

@test "malformed verdict fails closed" {
  tamper_delete
  gate
  verdict_json '{"rulings": not json'
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = verdict_malformed ]
  exists pkg/b_test.go
}

@test "verdict with wrong shape fails closed" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":"yes"}]}'
  verdict
  [ "$(j .reason)" = verdict_malformed ]
}

@test "verdict missing a ruling fails closed" {
  tamper_delete
  printf 'package pkg\n\nfunc TestNew(t *testing.T) {\n\tt.Skip("x")\n}\n' > "$WORK/pkg/new_test.go"
  change "add skipped test"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"ok"}]}'
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = unruled_findings ]
  [ "$(j .rulings_summary.unruled)" = 1 ]
  grep -q 'no ruling given' "$WS/tamper/must-fix.md"
}

@test "missing findings.json fails closed" {
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reason)" = findings_missing ]
}

@test "bad base exits 1" {
  vrun "$(snap)" deadbeef
  [ "$status" -eq 1 ]
}

# --- Boundary ----------------------------------------------------------------

@test "single legitimate finding" {
  printf 'package pkg\n\nfunc TestNew(t *testing.T) {\n\tt.Skip("x")\n}\n' > "$WORK/pkg/new_test.go"
  change "add skipped test"
  gate
  [ "$(jq '.findings | length' "$T/gate.out")" = 1 ]
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"plan says skip"}]}'
  verdict
  [ "$(j .voided)" = false ]
  [ "$(j .rulings_summary.legitimate)" = 1 ]
  exists pkg/new_test.go
}

# --- Integration -------------------------------------------------------------

@test "pass_base equals the base argument" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"ok"}]}'
  verdict
  [ "$(j .pass_base)" = "$BASE" ]
}

@test "pass_id is copied from pass-mark.pass_id" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":true,"reason":"ok"}]}'
  verdict
  [ "$(j .pass_id)" = 7 ]
  verdict --force-void
  [ "$(j .pass_id)" = 7 ]
}

# --- P15 ---------------------------------------------------------------------

@test "--restore-json resolve-inputs.inputs.plan_paths walks the hyphenated node id" {
  tamper_delete
  printf 'plan changed\n' > "$WORK/docs/plans/x/a.md"; change "plan"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  vrun "$(snap '{"resolve-inputs":{"inputs":{"plan_paths":["docs/plans/x/a.md"]}}}')" "$BASE" --restore-json resolve-inputs.inputs.plan_paths
  [ "$(j .voided)" = true ]
  [ "$(cat "$WORK/docs/plans/x/a.md")" = "plan a" ]
  [ "$(j '.restored_files | index("docs/plans/x/a.md") != null')" = true ]
}

# --- Fix round 1 -------------------------------------------------------------

@test "must-fix lists legitimately-changed files reverted by the void" {
  tamper_delete
  printf 'package pkg\n\nfunc TestNew(t *testing.T) {\n\tt.Skip("x")\n}\n' > "$WORK/pkg/new_test.go"
  printf 'package pkg\n\nimport "strings"\n\nfunc TestGoodnessOfFit(t *testing.T) {\n\tif strings.ToUpper("zz") != "ZZ" {\n\t\tt.Errorf("upper")\n\t}\n\tt.Errorf("one")\n\tt.Errorf("two")\n}\n' > "$WORK/pkg/good_test.go"
  change "mixed"
  gate
  [ "$(jq -r '.findings | map("\(.id)=\(.file)") | join(",")' "$T/gate.out")" = "T1=pkg/b_test.go,T2=pkg/new_test.go" ] || { jq -c .findings "$T/gate.out" >&2; false; }
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"},{"id":"T2","legitimate":true,"reason":"plan defers"}]}'
  verdict
  [ "$(j .voided)" = true ]
  ! exists pkg/good_test.go
  grep -q 'Reverted by this void (re-apply if still needed):' "$WS/tamper/must-fix.md"
  sed -n '/Reverted by this void/,$p' "$WS/tamper/must-fix.md" > "$T/section"
  grep -q 'pkg/good_test.go' "$T/section"
  grep -q 'pkg/new_test.go' "$T/section"
  ! grep -q 'pkg/b_test.go' "$T/section"
}

@test "hard-reset void appends the gaps line in one commit on top of base" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate --tests-only
  verdict --gaps-file docs/gaps.md
  [ "$(j .voided)" = true ]
  [ "$(commits)" = 1 ]
  [ "$(git -C "$WORK" rev-parse HEAD~1)" = "$BASE" ]
  grep -q '^- \[ \] TAMPER ' "$WORK/docs/gaps.md"
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

@test "hard-reset void keeps unrelated dirty tracked edits" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate --tests-only
  printf 'dirty\n' >> "$WORK/notes.txt"
  verdict
  [ "$(j .reset_mode)" = keep ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
  [ "$(tail -1 "$WORK/notes.txt")" = dirty ]
}

@test "hard-reset void falls back to --hard when keep refuses" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate --tests-only
  printf 'conflict\n' >> "$WORK/pkg/a.go"
  verdict
  [ "$(j .voided)" = true ]
  [ "$(j .reset_mode)" = hard_fallback ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$BASE" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

@test "unsafe --restore, --gaps-file and --restore-json paths exit 2 with an untouched index" {
  tamper_delete
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"no"}]}'
  verdict --restore ../outside
  [ "$status" -eq 2 ]
  verdict --restore /etc/passwd
  [ "$status" -eq 2 ]
  verdict --gaps-file ../gaps.md
  [ "$status" -eq 2 ]
  vrun "$(snap '{"p":{"paths":["a/../../x"]}}')" "$BASE" --restore-json p.paths
  [ "$status" -eq 2 ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
  ! exists pkg/b_test.go
  [ "$(commits)" = 1 ]
}

@test "non-judgeable finding shows its detail in must-fix, not 'no ruling given'" {
  printf 'x\n' >> "$WORK/pkg/a.go"; change "src"
  gate --tests-only
  verdict
  grep -q 'source_changed' "$WS/tamper/must-fix.md"
  ! grep -q 'no ruling given' "$WS/tamper/must-fix.md"
}

@test "zero findings and no verdict stands" {
  printf 'more\n' >> "$WORK/notes.txt"; change "benign"
  gate
  [ "$(jq -r .clean "$T/gate.out")" = true ]
  verdict
  [ "$status" -eq 0 ]
  [ "$(j .voided)" = false ]
  [ "$(git -C "$WORK" rev-parse HEAD)" != "$BASE" ]
}

@test "a replace ref cannot make the restore read a substituted blob" {
  printf '\tt.Skip("x")\n' >> "$WORK/pkg/a_test.go"
  change "tamper"
  tampered="$(git -C "$WORK" rev-parse HEAD:pkg/a_test.go)"
  original="$(git -C "$WORK" rev-parse "$BASE:pkg/a_test.go")"
  # Replace the BASE blob with the tampered one: a restore that honored the ref
  # would "restore" the tampered content.
  git -C "$WORK" replace -f "$original" "$tampered"
  gate
  verdict_json '{"rulings":[{"id":"T1","legitimate":false,"reason":"skip added"}]}'
  verdict --restore pkg/a_test.go
  [ "$status" -eq 0 ]
  ! grep -q 't.Skip' "$WORK/pkg/a_test.go"
}
