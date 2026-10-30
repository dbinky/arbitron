#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/persist-baseline.sh"
  T="$BATS_TEST_TMPDIR"
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WORK" "$WS"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q -b main "$WORK"
  echo x > "$WORK/x.txt"
  git -C "$WORK" add . && git -C "$WORK" commit -q -m init
  REL=.arbitron/pipeline/my-slug.baseline.json
}

run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}
snap() { jq -n --arg w "$WORK" --arg ws "$WS" --arg r "$REL" '{work_dir:$w, workspace:$ws, baseline_rel:$r, slug:"my-slug"}'; }

GOOD='{"suites":[{"name":"go","command":"go test ./...","failed_count":0,"passed_count":4,"failing_tests":[]}]}'

@test "commits and leaves a clean tree" {
  printf '%s' "$GOOD" > "$WS/test-baseline.json"
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(jq -c '[.persisted,.path]' <<<"$output")" = "[true,\".arbitron/pipeline/my-slug.baseline.json\"]" ]
  [ "$(git -C "$WORK" log -1 --format=%s)" = "chore(pipeline): record my-slug branch-point baseline" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
  [ "$(git -C "$WORK" show HEAD:"$REL" | jq -c .)" = "$(printf '%s' "$GOOD" | jq -c .)" ]
}

@test "identical rerun makes no commit" {
  printf '%s' "$GOOD" > "$WS/test-baseline.json"
  run_it "$(snap)"
  before="$(git -C "$WORK" rev-list --count HEAD)"
  run_it "$(snap)"
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-list --count HEAD)" = "$before" ]
}

@test "invalid shape exits 1" {
  printf '{"suites":"nope"}' > "$WS/test-baseline.json"
  run_it "$(snap)"
  [ "$status" -eq 1 ]
  [ ! -e "$WORK/$REL" ]
}

@test "missing baseline exits 1" {
  run_it "$(snap)"
  [ "$status" -eq 1 ]
}

@test "path traversal exits 1" {
  printf '%s' "$GOOD" > "$WS/test-baseline.json"
  REL=../evil.json
  run_it "$(snap)"
  [ "$status" -eq 1 ]
}
