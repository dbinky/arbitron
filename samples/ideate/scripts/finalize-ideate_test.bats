#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/finalize-ideate.sh"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WS="$BATS_TEST_TMPDIR/1a2b3c4d-5e6f-7a8b-9c0d-ef0123456789"
  WORK="$WS/work"
  git init -q -b main "$WORK"
  echo a > "$WORK/a.txt"
  git -C "$WORK" add a.txt
  git -C "$WORK" commit -q -m init
  git -C "$WORK" checkout -q --detach
  mkdir -p "$WORK/docs/ideas" "$WS/.arbitron"
}

# result <slug>: write ideate-result.json the way the jam does.
result() {
  jq -n --arg s "$1" '{slug:$s}' > "$WS/.arbitron/ideate-result.json"
}

# idea <slug> [content]: write the idea doc.
idea() {
  printf '%s' "${2:-# Idea
}" > "$WORK/docs/ideas/$1.md"
}

# run_it: snapshot carries workspace, work_dir and run_date.
run_it() {
  local snap
  snap="$(jq -n --arg ws "$WS" --arg w "$WORK" '{workspace:$ws, work_dir:$w, run_date:"2026-10-24"}')"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

# --- Happy path

@test "valid result creates dev-<slug>-<date>-<short>, commits the doc, HEAD not detached" {
  result age-filter
  idea age-filter
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"slug":"age-filter","branch":"dev-age-filter-2026-10-24-1a2b3c4d","idea_path":"docs/ideas/age-filter.md"}' ]
  [ "$(git -C "$WORK" symbolic-ref --short HEAD)" = "dev-age-filter-2026-10-24-1a2b3c4d" ]
  git -C "$WORK" show --stat HEAD | grep -q 'docs/ideas/age-filter.md'
  git -C "$WORK" log -1 --format=%s | grep -q '^docs: add age-filter idea doc'
  [ -z "$(git -C "$WORK" status --porcelain -- docs/ideas)" ]
}

@test "a doc the jam already committed is kept, with no extra commit" {
  result age-filter
  idea age-filter
  git -C "$WORK" add docs/ideas
  git -C "$WORK" commit -q -m "docs: jam commit"
  before="$(git -C "$WORK" rev-parse HEAD)"
  run_it
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$before" ]
  [ "$(git -C "$WORK" symbolic-ref --short HEAD)" = "dev-age-filter-2026-10-24-1a2b3c4d" ]
}

@test "commits only the idea doc" {
  result age-filter
  idea age-filter
  echo stray > "$WORK/stray.txt"
  git -C "$WORK" add stray.txt
  run_it
  [ "$status" -eq 0 ]
  ! git -C "$WORK" show --stat HEAD | grep -q stray.txt
}

@test "does not push" {
  git init -q --bare -b main "$BATS_TEST_TMPDIR/origin.git"
  git -C "$WORK" remote add origin "$BATS_TEST_TMPDIR/origin.git"
  result age-filter
  idea age-filter
  run_it
  [ "$status" -eq 0 ]
  [ -z "$(git -C "$BATS_TEST_TMPDIR/origin.git" branch --list)" ]
}

@test "a rerun after success is idempotent" {
  result age-filter
  idea age-filter
  run_it
  [ "$status" -eq 0 ]
  first="$output"
  run_it
  [ "$status" -eq 0 ]
  [ "$output" = "$first" ]
  [ "$(git -C "$WORK" symbolic-ref --short HEAD)" = "dev-age-filter-2026-10-24-1a2b3c4d" ]
}

# --- Errors

@test "missing result file" {
  idea age-filter
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no idea doc written"* ]]
}

@test "missing doc" {
  result age-filter
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no idea doc written"* ]]
}

@test "empty doc" {
  result age-filter
  : > "$WORK/docs/ideas/age-filter.md"
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"empty"* ]]
}

@test "uppercase slug" {
  result Age-filter
  idea Age-filter
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}

@test "leading - slug" {
  result -age
  idea -age
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}

@test "49-char slug" {
  s="$(printf 'a%.0s' $(seq 1 49))"
  result "$s"
  idea "$s"
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}

@test "a multi-line slug is refused" {
  result "$(printf 'ok\n../x')"
  mkdir -p "$WORK/docs/x"
  idea ok
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "HEAD" ]
}

@test "malformed result file" {
  echo 'not json' > "$WS/.arbitron/ideate-result.json"
  idea age-filter
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"ideate-result.json"* ]]
}

@test "a non-string slug" {
  echo '{"slug":7}' > "$WS/.arbitron/ideate-result.json"
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}

@test "a symlinked doc is refused" {
  result age-filter
  echo secret > "$BATS_TEST_TMPDIR/outside.md"
  ln -s "$BATS_TEST_TMPDIR/outside.md" "$WORK/docs/ideas/age-filter.md"
  run_it
  [ "$status" -eq 1 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "HEAD" ]
}

@test "a failed check leaves HEAD detached" {
  result age-filter
  run_it
  [ "$status" -eq 1 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "HEAD" ]
}

# --- Boundary

@test "48-char slug ok" {
  s="$(printf 'a%.0s' $(seq 1 48))"
  result "$s"
  idea "$s"
  run_it
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .slug)" = "$s" ]
}

@test "a one-char slug ok" {
  result a
  idea a
  run_it
  [ "$status" -eq 0 ]
}

@test "a doubled hyphen is refused" {
  result age--filter
  idea age--filter
  run_it
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}
