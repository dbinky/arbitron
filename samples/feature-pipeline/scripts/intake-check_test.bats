#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/intake-check.sh"
  MREAD="$BATS_TEST_DIRNAME/../../_lib/manifest-read.sh"
  T="$BATS_TEST_TMPDIR"
  W="$T/work"
  mkdir -p "$W/.arbitron/pipeline" "$W/docs/ideas"
}

# manifest <slug> <base_branch|""> <stages-json>
manifest() {
  jq -n --arg s "$1" --arg b "$2" --argjson st "$3" \
    '{slug:$s, branch:"feat/x"} + (if $b == "" then {} else {base_branch:$b} end) + {stages:$st}' \
    > "$W/.arbitron/pipeline/$1.json"
}
IDEATE='[{"stage":"ideate","status":"done","outputs":{"idea_path":"docs/ideas/i.md"}}]'

# run_it <mode> <extra-json>: snapshot = intake.mode + clone + extras
run_it() {
  local snap
  snap="$(jq -n --arg m "$1" --arg w "$W" "{intake:{mode:\$m}, clone:{work_dir:\$w, branch:\"feat/x\"}} + $2")"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2"' _ "$snap" "$SCRIPT"
}

# --- Happy path

@test "outputs correct (resume with ideate entry)" {
  manifest s1 main "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1", base_branch:""}'
  [ "$status" -eq 0 ]
  [ "$(jq -c . <<<"$output")" = "$(jq -nc --arg w "$W" '{slug:"s1",branch:"feat/x",work_dir:$w,idea_path:"",record_idea:false,base_branch:"main"}')" ]
}

@test "resume, ideate entry, no idea_path ok" {
  manifest s1 main "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .record_idea <<<"$output")" = false ]
}

@test "resume with idea_path: must exist; record_idea true even with an ideate entry" {
  manifest s1 main "$IDEATE"
  echo x > "$W/docs/ideas/i.md"
  run_it resume '{branch:"feat/x", slug:"s1", idea_path:"docs/ideas/i.md"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .record_idea <<<"$output")" = true ]
  [ "$(jq -r .idea_path <<<"$output")" = docs/ideas/i.md ]
  run_it resume '{branch:"feat/x", slug:"s1", idea_path:"docs/ideas/nope.md"}'
  [ "$status" -eq 1 ]
}

@test "resume, no ideate entry, start_at=spec ok with record_idea false" {
  manifest s1 main '[]'
  run_it resume '{branch:"feat/x", slug:"s1", start_at:"spec"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .record_idea <<<"$output")" = false ]
}

@test "resume with no manifest at all, start_at=spec and explicit base_branch is ok" {
  run_it resume '{branch:"feat/x", slug:"s1", start_at:"spec", base_branch:"dev"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .record_idea <<<"$output")" = false ]
  [ "$(jq -r .base_branch <<<"$output")" = dev ]
}

# --- Slug discovery

@test "slug discovery 0/1/2 manifests" {
  run_it resume '{branch:"feat/x", start_at:"spec", base_branch:"main"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *slug* ]]
  manifest only main "$IDEATE"
  run_it resume '{branch:"feat/x"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = only ]
  manifest other main "$IDEATE"
  run_it resume '{branch:"feat/x"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *slug* ]]
}

@test "explicit slug wins" {
  manifest a main "$IDEATE"
  manifest b main "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"b"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = b ]
}

# --- Errors

@test "resume, no idea_path, no ideate entry, empty start_at exits 1" {
  manifest s1 main '[]'
  run_it resume '{branch:"feat/x", slug:"s1"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"resume branch has no idea doc; pass idea_path or start_at=spec"* ]]
}

@test "idea mode idea_path missing on base exits 1" {
  run_it idea '{slug:"i", idea_path:"docs/ideas/nope.md", base_branch:"main"}'
  [ "$status" -eq 1 ]
}

@test "idea mode ok: branch from clone, record_idea true" {
  echo x > "$W/docs/ideas/i.md"
  run_it idea '{slug:"i", idea_path:"docs/ideas/i.md", base_branch:"main"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .branch <<<"$output")" = feat/x ]
  [ "$(jq -r .record_idea <<<"$output")" = true ]
  [ "$(jq -r .base_branch <<<"$output")" = main ]
}

@test "idea_path escaping the worktree exits 1" {
  run_it idea '{slug:"i", idea_path:"../etc/passwd", base_branch:"main"}'
  [ "$status" -eq 1 ]
  run_it idea '{slug:"i", idea_path:"/etc/passwd", base_branch:"main"}'
  [ "$status" -eq 1 ]
}

@test "fresh mode emits no branch, record_idea false" {
  run_it fresh '{base_branch:"main"}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .branch <<<"$output")" = "" ]
  [ "$(jq -r .record_idea <<<"$output")" = false ]
}

# --- base_branch (P2b)

@test "explicit base_branch wins over the manifest" {
  manifest s1 dev "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1", base_branch:"rel"}'
  [ "$(jq -r .base_branch <<<"$output")" = rel ]
}

@test "resume of a branch cut from dev without base_branch emits dev from the manifest" {
  manifest s1 dev "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1", base_branch:""}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .base_branch <<<"$output")" = dev ]
}

@test "resume with no explicit and no manifest base_branch exits 1" {
  manifest s1 "" "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1", base_branch:""}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"resume branch has no recorded base_branch; pass base_branch"* ]]
}

@test "resume from dev: output piped into manifest-read yields inputs.base_branch=dev" {
  manifest s1 dev "$IDEATE"
  run_it resume '{branch:"feat/x", slug:"s1", base_branch:""}'
  [ "$status" -eq 0 ]
  out="$output"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" --explicit base_branch --require base_branch' _ "$out" "$MREAD"
  [ "$status" -eq 0 ]
  [ "$(jq -r .inputs.base_branch <<<"$output")" = dev ]
}

# --- Manifest discovery counts manifests, not the artifacts stored beside them

@test "resume without slug resolves the one manifest beside baseline, harden and survivors artifacts" {
  manifest one main "$IDEATE"
  echo '{"suites":[]}' > "$W/.arbitron/pipeline/one.baseline.json"
  echo '{"harden":{"slug":"one"}}' > "$W/.arbitron/pipeline/one.harden.json"
  echo '{"survivors":[]}' > "$W/.arbitron/pipeline/one.survivors.json"
  run_it resume '{branch:"feat/x", slug:""}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = one ]
}

@test "resume without slug: two genuine manifests are still ambiguous" {
  manifest one main "$IDEATE"
  manifest two main "$IDEATE"
  echo '{"suites":[]}' > "$W/.arbitron/pipeline/one.baseline.json"
  run_it resume '{branch:"feat/x", slug:""}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"found 2 manifests"* ]]
}

@test "resume without slug: files lacking a string slug or an array of stages are not manifests" {
  manifest one main "$IDEATE"
  echo '{"slug":"","stages":[]}' > "$W/.arbitron/pipeline/empty-slug.json"
  echo '{"slug":7,"stages":[]}' > "$W/.arbitron/pipeline/num-slug.json"
  echo '{"slug":"x","stages":{}}' > "$W/.arbitron/pipeline/obj-stages.json"
  echo '[]' > "$W/.arbitron/pipeline/array.json"
  echo 'not json' > "$W/.arbitron/pipeline/junk.json"
  run_it resume '{branch:"feat/x", slug:""}'
  [ "$status" -eq 0 ]
  [ "$(jq -r .slug <<<"$output")" = one ]
}

@test "resume without slug: only artifacts and no manifest finds 0" {
  echo '{"suites":[]}' > "$W/.arbitron/pipeline/one.baseline.json"
  run_it resume '{branch:"feat/x", slug:""}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"found 0 manifests"* ]]
}
