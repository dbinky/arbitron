#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/intake.sh"
}

# run_it <json>: the snapshot arrives on stdin.
run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$1" "$SCRIPT"
}

GH='https://github.com/acme/widgets.git'

# --- Happy path

@test "three modes with init_mode" {
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .mode <<<"$output")" = resume ]
  [ "$(jq -r .init_mode <<<"$output")" = checkout ]
  run_it '{"repo_url":"'"$GH"'","idea_path":"docs/ideas/a.md"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .mode <<<"$output")" = idea ]
  [ "$(jq -r .init_mode <<<"$output")" = new-branch ]
  run_it '{"repo_url":"'"$GH"'"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .mode <<<"$output")" = fresh ]
  [ "$(jq -r .init_mode <<<"$output")" = detached-base ]
  [ "$(jq -r .repo_slug <<<"$output")" = acme/widgets ]
}

@test "fresh and idea with empty base_branch emit main; explicit kept; resume with empty emits empty" {
  run_it '{"repo_url":"'"$GH"'"}'
  [ "$(jq -r .base_branch <<<"$output")" = main ]
  run_it '{"repo_url":"'"$GH"'","idea_path":"i.md","base_branch":""}'
  [ "$(jq -r .base_branch <<<"$output")" = main ]
  run_it '{"repo_url":"'"$GH"'","idea_path":"i.md","base_branch":"dev"}'
  [ "$(jq -r .base_branch <<<"$output")" = dev ]
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","base_branch":""}'
  [ "$(jq -r .base_branch <<<"$output")" = "" ]
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","base_branch":"rel/1"}'
  [ "$(jq -r .base_branch <<<"$output")" = rel/1 ]
}

# --- Edge cases

@test "idea-mode slug from basename" {
  run_it '{"repo_url":"'"$GH"'","idea_path":"docs/ideas/My_Idea.md"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = my-idea ]
}

@test "explicit slug is kept in resume mode" {
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","slug":"abc"}'
  [ "$(jq -r .slug <<<"$output")" = abc ]
}

@test "an explicit slug is sanitized like init-workspace's, in every mode" {
  # The slug names the manifest file, docs/specs/<date>-<slug>-spec.md and the
  # finalize-docs globs (which split on spaces), so it must be [a-z0-9-] only.
  run_it '{"repo_url":"'"$GH"'","idea_path":"docs/ideas/x.md","slug":"My Feature/../x y"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = my-feature-x-y ]
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","slug":"Two  Words"}'
  [ "$(jq -r .slug <<<"$output")" = two-words ]
  run_it '{"repo_url":"'"$GH"'","slug":"--Only Fresh--"}'
  [ "$(jq -r .slug <<<"$output")" = only-fresh ]
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","slug":"!!!"}'
  [ "$(jq -r .slug <<<"$output")" = feature ]
}

@test "no slug in fresh or resume mode stays empty" {
  run_it '{"repo_url":"'"$GH"'"}'
  [ "$(jq -r .slug <<<"$output")" = "" ]
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x"}'
  [ "$(jq -r .slug <<<"$output")" = "" ]
}

@test "start_at stages accepted on resume" {
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","start_at":"plan"}'
  [ "$status" -eq 0 ]
}

# --- Error cases

@test "start_at=ideate with branch exits 1" {
  run_it '{"repo_url":"'"$GH"'","branch":"feat/x","start_at":"ideate"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *ideate* ]]
}

@test "start_at=ideate with idea_path exits 1 with message" {
  run_it '{"repo_url":"'"$GH"'","idea_path":"i.md","start_at":"ideate"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"idea_path given — ideate is skipped; omit start_at or use start_at=spec"* ]]
}

@test "start_at=plan with no branch exits 1" {
  run_it '{"repo_url":"'"$GH"'","start_at":"plan"}'
  [ "$status" -eq 1 ]
}

@test "invalid start_at exits 1" {
  run_it '{"repo_url":"'"$GH"'","branch":"b","start_at":"bogus"}'
  [ "$status" -eq 1 ]
}

@test "non-GitHub url exits 1" {
  run_it '{"repo_url":"https://gitlab.com/a/b.git"}'
  [ "$status" -eq 1 ]
  run_it '{"repo_url":"git@bitbucket.org:a/b.git"}'
  [ "$status" -eq 1 ]
}

@test "empty repo_url exits 1" {
  run_it '{}'
  [ "$status" -eq 1 ]
}

# --- Boundary

@test "repo_slug from https, ssh and scp forms" {
  for u in https://github.com/acme/widgets https://github.com/acme/widgets.git https://github.com/acme/widgets/ ssh://git@github.com/acme/widgets.git git@github.com:acme/widgets.git; do
    run_it '{"repo_url":"'"$u"'"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .repo_slug <<<"$output")" = acme/widgets ]
  done
}

@test "file:// gives empty repo_slug" {
  run_it '{"repo_url":"file:///tmp/x.git"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .repo_slug <<<"$output")" = "" ]
}
