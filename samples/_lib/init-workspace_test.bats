#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/init-workspace.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q --bare -b main "$T/origin.git"
  git clone -q "$T/origin.git" "$T/seed" 2>/dev/null
  git -C "$T/seed" checkout -q -b main
  echo a > "$T/seed/a.txt"
  git -C "$T/seed" add a.txt
  git -C "$T/seed" commit -q -m init
  git -C "$T/seed" push -q origin main
  git -C "$T/seed" checkout -q -b feat/x
  echo b > "$T/seed/b.txt"
  git -C "$T/seed" add b.txt
  git -C "$T/seed" commit -q -m featx
  git -C "$T/seed" push -q origin feat/x
  git -C "$T/seed" checkout -q main
  git -C "$T/seed" checkout -q -b dev
  git -C "$T/seed" push -q origin dev
  git -C "$T/origin.git" symbolic-ref HEAD refs/heads/main
  URL="file://$T/origin.git"
  WS="$T/instances/abcdef0123456789"
  mkdir -p "$WS"
}

# run_it_sh <json-extras> <args...>; snapshot = workspace + repo_url + extras.
run_it_sh() {
  local extra="$1"; shift
  local snap
  snap="$(jq -n --arg ws "$WS" --arg url "$URL" "{workspace:\$ws, repo_url:\$url} + $extra")"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$snap" "$SCRIPT" "$@"
}

fails_quietly() {
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

# --- Happy path

@test "checkout lands on the branch with the hook installed" {
  run_it_sh '{branch:"feat/x"}' --checkout
  [ "$status" -eq 0 ]
  work="$(printf '%s' "$output" | jq -r .work_dir)"
  [ "$(git -C "$work" symbolic-ref --short HEAD)" = "feat/x" ]
  [ -x "$work/.git/hooks/commit-msg" ] || [ -x "$(git -C "$work" rev-parse --git-common-dir)/hooks/commit-msg" ]
  [ "$(printf '%s' "$output" | jq -r .branch)" = "feat/x" ]
  [ "$(printf '%s' "$output" | jq -r .mode)" = "checkout" ]
  [ "$(printf '%s' "$output" | jq -r .head_sha)" = "$(git -C "$work" rev-parse HEAD)" ]
  [ "$(printf '%s' "$output" | jq -r .src_dir)" = "$WS/src" ]
  [ "$(printf '%s' "$output" | jq -r .work_dir)" = "$WS/work" ]
}

@test "new-branch names dev-<slug>-<date>-<short>" {
  run_it_sh '{slug:"my-feat", base_branch:"dev"}' --new-branch
  [ "$status" -eq 0 ]
  expected="dev-my-feat-$(date +%Y-%m-%d)-abcdef01"
  [ "$(printf '%s' "$output" | jq -r .branch)" = "$expected" ]
  [ "$(printf '%s' "$output" | jq -r .base_ref)" = "dev" ]
  [ "$(git -C "$WS/work" symbolic-ref --short HEAD)" = "$expected" ]
}

@test "detached-base is detached with empty branch" {
  run_it_sh '{base_branch:"dev"}' --detached-base
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .branch)" = "" ]
  ! git -C "$WS/work" symbolic-ref -q HEAD
  [ "$(printf '%s' "$output" | jq -r .mode)" = "detached-base" ]
}

@test "mode-var selects each mode" {
  for m in checkout detached-base new-branch; do
    run_it_sh "{branch:\"feat/x\", m:\"$m\"}" --mode-var m
    [ "$status" -eq 0 ]
    [ "$(printf '%s' "$output" | jq -r .mode)" = "$m" ]
  done
}

# --- Edge cases

@test "empty base_branch uses remote HEAD" {
  run_it_sh '{base_branch:""}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .base_ref)" = "main" ]
}

@test "slug sanitizes \"My Feat!!\" to my-feat" {
  run_it_sh '{slug:"My Feat!!"}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .slug)" = "my-feat" ]
}

@test "all-punctuation slug falls back to feature" {
  run_it_sh '{slug:"!!!"}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .slug)" = "feature" ]
}

@test "rerun clears stale src, work and .manifest-wt" {
  run_it_sh '{}' --new-branch
  [ "$status" -eq 0 ]
  echo stale > "$WS/src/stale.txt"
  echo stale > "$WS/work/stale.txt"
  mkdir -p "$WS/.manifest-wt"
  echo stale > "$WS/.manifest-wt/x"
  run_it_sh '{}' --new-branch
  [ "$status" -eq 0 ]
  [ ! -e "$WS/src/stale.txt" ]
  [ ! -e "$WS/work/stale.txt" ]
  [ ! -e "$WS/.manifest-wt" ]
}

# --- Errors

@test "rejects -leading repo_url" {
  URL="-oProxyCommand=x"
  run_it_sh '{}' --new-branch
  fails_quietly
}

@test "rejects ext:: and http:// urls" {
  URL="ext::sh -c touch% /tmp/pwn"
  run_it_sh '{}' --new-branch
  fails_quietly
  URL="http://example.com/r.git"
  run_it_sh '{}' --new-branch
  fails_quietly
}

@test "rejects ref a..b and -x" {
  run_it_sh '{base_branch:"a..b"}' --new-branch
  fails_quietly
  run_it_sh '{base_branch:"-x"}' --new-branch
  fails_quietly
  run_it_sh '{branch:"a..b"}' --checkout
  fails_quietly
  run_it_sh '{branch:"-x"}' --checkout
  fails_quietly
}

@test "checkout of a missing branch fails with \"not found on origin\"" {
  run_it_sh '{branch:"nope"}' --checkout
  fails_quietly
  [[ "$stderr" == *"not found on origin"* ]]
}

@test "no mode or two modes fails" {
  run_it_sh '{}'
  fails_quietly
  run_it_sh '{branch:"feat/x"}' --checkout --new-branch
  fails_quietly
}

@test "mode-var with an absent key or an unknown value fails" {
  run_it_sh '{}' --mode-var m
  fails_quietly
  run_it_sh '{m:"bogus"}' --mode-var m
  fails_quietly
}

# --- Boundary

@test "short_id is exactly 8 chars" {
  run_it_sh '{}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .short_id)" = "abcdef01" ]
  WS="$T/instances/abc"
  mkdir -p "$WS"
  run_it_sh '{}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .short_id)" = "abc" ]
}

@test "single-character slug is kept" {
  run_it_sh '{slug:"x"}' --new-branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .slug)" = "x" ]
}

# --- Integration

@test "commits get [skip ci] except with --no-verify" {
  run_it_sh '{}' --new-branch
  [ "$status" -eq 0 ]
  work="$WS/work"
  echo 1 > "$work/f1"
  git -C "$work" add f1
  git -C "$work" commit -q -m "one"
  git -C "$work" log -1 --format=%B | grep -q '\[skip ci\]'
  echo 2 > "$work/f2"
  git -C "$work" add f2
  git -C "$work" commit -q --no-verify -m "two"
  ! git -C "$work" log -1 --format=%B | grep -q '\[skip ci\]'
}
