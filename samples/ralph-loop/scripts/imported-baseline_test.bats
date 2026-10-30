#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# An imported baseline was measured before implementation, but record-baseline
# used to stamp the CURRENT HEAD as the commit it measured, so verify-tests saw
# an inspection-only first pass and never ran the (now failing) suite.

setup() {
  D="$BATS_TEST_DIRNAME"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/ws/work"
  WS="$T/ws"
  mkdir -p "$WORK/docs/reference"
  git init -q -b main "$WORK"
  printf 'exit 0\n' > "$WORK/test.sh"
  printf '%s\n' '{"suites":[{"name":"s","command":"sh test.sh","paths":["test.sh"],"build_ok":true,"failed_count":0,"passed_count":1,"failing_tests":[]}]}' \
    > "$WORK/docs/reference/test-baseline.json"
  git -C "$WORK" add -A; git -C "$WORK" commit -q -m measured
}

snap() { jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws, baseline_path:"docs/reference/test-baseline.json"}'; }
sh_() { printf '%s' "$(snap)" | sh "$D/$1"; }
break_suite() { printf 'exit 1\n' > "$WORK/test.sh"; git -C "$WORK" add -A; git -C "$WORK" commit -q -m "implementation breaks the suite"; }

# An inspection-only pass: only prose changed since the last pass.
inspection_only_pass() {
  git -C "$WORK" rev-parse HEAD > "$WS/last-pass-head"
  printf 'notes\n' > "$WORK/RALPH.md"; git -C "$WORK" add -A; git -C "$WORK" commit -q -m "ralph notes"
}

@test "import marks the workspace and clears a stale baseline-head" {
  printf 'stale' > "$WS/baseline-head"
  run sh_ import-baseline.sh
  [ "$status" -eq 0 ]
  [ -f "$WS/baseline-imported" ]
  [ ! -e "$WS/baseline-head" ]
}

@test "record-baseline after an import writes no baseline-head and removes a stale one" {
  sh_ import-baseline.sh >/dev/null
  printf 'stale' > "$WS/baseline-head"
  break_suite
  sh_ record-baseline.sh >/dev/null 2>&1
  [ ! -e "$WS/baseline-head" ]
}

@test "a measured baseline still records baseline-head" {
  cp "$WORK/docs/reference/test-baseline.json" "$WS/test-baseline.json"
  sh_ record-baseline.sh >/dev/null 2>&1
  [ "$(cat "$WS/baseline-head")" = "$(git -C "$WORK" rev-parse HEAD)" ]
}

@test "import -> break suite -> record-baseline -> inspection-only pass runs the suite in full" {
  sh_ import-baseline.sh >/dev/null
  break_suite
  sh_ record-baseline.sh >/dev/null 2>&1
  inspection_only_pass
  run sh_ verify-tests.sh
  [ "$status" -eq 0 ]
  [ "$(jq -r .scope <<<"$output")" = full ]
  [ "$(jq -r .tests_ran <<<"$output")" = true ]
  [ "$(jq -r '.new_failures > 0' <<<"$output")" = true ]
}

@test "a last-pass-head cannot bypass the first full verification" {
  sh_ import-baseline.sh >/dev/null
  break_suite
  sh_ record-baseline.sh >/dev/null 2>&1
  git -C "$WORK" rev-parse HEAD > "$WS/last-pass-head"
  run sh_ verify-tests.sh
  [ "$(jq -r .scope <<<"$output")" = full ]
  [ "$(jq -r '.new_failures > 0' <<<"$output")" = true ]
}

@test "after the first full sweep, diff scoping resumes from last-pass-head" {
  sh_ import-baseline.sh >/dev/null
  sh_ record-baseline.sh >/dev/null 2>&1
  run sh_ verify-tests.sh
  [ "$(jq -r .scope <<<"$output")" = full ]
  [ -f "$WS/import-verified" ]
  inspection_only_pass
  run sh_ verify-tests.sh
  [ "$(jq -r .scope <<<"$output")" = skipped ]
}

@test "control: a measured baseline scopes pass 1 against baseline-head" {
  cp "$WORK/docs/reference/test-baseline.json" "$WS/test-baseline.json"
  sh_ record-baseline.sh >/dev/null 2>&1
  printf 'notes\n' > "$WORK/RALPH.md"; git -C "$WORK" add -A; git -C "$WORK" commit -q -m notes
  run sh_ verify-tests.sh
  [ "$(jq -r .scope <<<"$output")" = skipped ]
}
