#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/scope.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  ORIGIN="$T/origin.git"
  WORK="$T/work"
  git init -q --bare -b main "$ORIGIN"
  git init -q -b main "$WORK"
  git -C "$WORK" remote add origin "$ORIGIN"
  echo 1 > "$WORK/a.txt"
  git -C "$WORK" add a.txt
  git -C "$WORK" commit -q -m base
  git -C "$WORK" push -q origin main
  BASE_SHA="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" checkout -q -b feat
  echo 2 > "$WORK/b.txt"
  git -C "$WORK" add b.txt
  git -C "$WORK" commit -q -m feat
}

# snap <explicit-base_ref> <manifest-base_branch>
snap() {
  jq -n --arg w "$WORK" --arg e "$1" --arg m "$2" '
    {work_dir:$w, "resolve-inputs": {inputs: {base_ref:$e}, outputs: {base_branch:$m}}}'
}

run_it() {
  run --separate-stderr sh -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

# --- Happy path

@test "explicit base_ref wins and merge_base is the fork point" {
  git -C "$WORK" branch other "$BASE_SHA"
  run_it "$(snap main other)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .base_ref)" = "main" ]
  [ "$(printf '%s' "$output" | jq -r .merge_base)" = "$BASE_SHA" ]
}

@test "manifest base_branch is the fallback" {
  run_it "$(snap '' main)"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .base_ref)" = "main" ]
  [ "$(printf '%s' "$output" | jq -r .merge_base)" = "$BASE_SHA" ]
}

@test "merge_base is the fork point when base has moved on" {
  git -C "$WORK" checkout -q main
  echo 3 > "$WORK/c.txt"
  git -C "$WORK" add c.txt
  git -C "$WORK" commit -q -m moved
  git -C "$WORK" push -q origin main
  git -C "$WORK" checkout -q feat
  run_it "$(snap main '')"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .merge_base)" = "$BASE_SHA" ]
}

# --- Edge cases

@test "a shallow clone is unshallowed to find the merge-base" {
  git -C "$WORK" push -q origin feat
  git -C "$WORK" checkout -q main
  echo 3 > "$WORK/c.txt"
  git -C "$WORK" add c.txt
  git -C "$WORK" commit -q -m moved
  git -C "$WORK" push -q origin main
  echo 4 > "$WORK/d.txt"
  git -C "$WORK" add d.txt
  git -C "$WORK" commit -q -m moved2
  git -C "$WORK" push -q origin main
  git clone -q --depth 1 --branch feat "file://$ORIGIN" "$T/shallow"
  [ "$(git -C "$T/shallow" rev-parse --is-shallow-repository)" = "true" ]
  WORK="$T/shallow"
  run_it "$(snap main '')"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .merge_base)" = "$BASE_SHA" ]
}

@test "a repository with no origin resolves a local base branch" {
  git -C "$WORK" remote remove origin
  run_it "$(snap main '')"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .merge_base)" = "$BASE_SHA" ]
}

# --- Error cases

@test "neither explicit nor manifest base exits 1" {
  run_it "$(snap '' '')"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no base_ref given and no manifest base_branch"* ]]
  [ -z "$output" ]
}

@test "no merge-base exits 1" {
  git -C "$WORK" checkout -q --orphan unrelated
  git -C "$WORK" rm -rfq .
  echo x > "$WORK/x.txt"
  git -C "$WORK" add x.txt
  git -C "$WORK" commit -q -m unrelated
  run_it "$(snap main '')"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no merge-base"* ]]
  [ -z "$output" ]
}

@test "an unresolvable base exits 1" {
  run_it "$(snap nosuchbranch '')"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"nosuchbranch"* ]]
}

@test "work_dir unset exits 1" {
  run_it '{}'
  [ "$status" -eq 1 ]
}
