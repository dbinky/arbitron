#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

ORDER="ideate,spec,design,plan,implement,harden,refine,review"

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/manifest-read.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  git init -q --bare -b feat "$T/origin.git"
  WORK="$T/work"
  git clone -q "$T/origin.git" "$WORK" 2>/dev/null
  git -C "$WORK" checkout -q -b feat
  echo a > "$WORK/a.txt"
  git -C "$WORK" add a.txt
  git -C "$WORK" commit -q -m init
  git -C "$WORK" push -q origin feat
  mkdir -p "$WORK/.arbitron/pipeline"
}

# entry <stage> <status> [outputs-json]
entry() {
  jq -nc --arg s "$1" --arg st "$2" --argjson o "${3:-{\}}" '{stage:$s, status:$st, outputs:$o}'
}

# write_manifest <stages-json-array> [extra-top-level-json]
write_manifest() {
  jq -n --argjson st "$1" --argjson x "${2:-{\}}" \
    '{slug:"s1", base_branch:"main", branch:"feat", stages:$st} + $x' \
    > "$WORK/.arbitron/pipeline/s1.json"
}

# snap [extra-json]
snap() {
  jq -n --arg w "$WORK" --argjson x "${1:-{\}}" '{work_dir:$w, branch:"feat", slug:"s1"} + $x'
}

# run_it <snapshot-json> <args...>
run_it() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$1" "$SCRIPT" "${@:2}"
}

# --- Happy path

@test "reads the worktree file" {
  write_manifest "[$(entry spec done '{"spec_path":"docs/s.md"}')]"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .exists)" = "true" ]
  [ "$(printf '%s' "$output" | jq -r .path)" = ".arbitron/pipeline/s1.json" ]
  [ "$(printf '%s' "$output" | jq -r .outputs.spec_path)" = "docs/s.md" ]
  [ "$(printf '%s' "$output" | jq -r .latest.spec.status)" = "done" ]
  [ "$(printf '%s' "$output" | jq -c .done_stages)" = '["spec"]' ]
  [ "$(printf '%s' "$output" | jq -r .manifest.slug)" = "s1" ]
}

@test "--fetch reads origin, not a stale worktree copy" {
  write_manifest "[$(entry ideate done '{"idea_path":"i.md"}')]"
  git -C "$WORK" add .arbitron
  git -C "$WORK" commit -q -m m1
  git -C "$WORK" push -q origin feat
  git clone -q "$T/origin.git" "$T/other" 2>/dev/null
  git -C "$T/other" checkout -q feat
  jq '.stages += [{stage:"spec",status:"done",outputs:{spec_path:"new.md"}}]' \
    "$T/other/.arbitron/pipeline/s1.json" > "$T/m.json"
  cp "$T/m.json" "$T/other/.arbitron/pipeline/s1.json"
  git -C "$T/other" commit -q -am m2
  git -C "$T/other" push -q origin feat
  run_it "$(snap)" --fetch --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .outputs.spec_path)" = "new.md" ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "design" ]
}

# --- Detection

@test "latest entry wins: blocked after done is not done" {
  write_manifest "[$(entry ideate done), $(entry spec done), $(entry spec blocked)]"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "spec" ]
  [ "$(printf '%s' "$output" | jq -c .done_stages)" = '["ideate"]' ]
}

@test "blocked output does not feed the merged view" {
  write_manifest "[$(entry spec done '{"spec_path":"a"}'), $(entry spec blocked '{"spec_path":"b"}')]"
  run_it "$(snap)" --order "$ORDER"
  [ "$(printf '%s' "$output" | jq -r '.outputs.spec_path // "none"')" = "none" ]
}

@test "next is the first not-done stage in order" {
  write_manifest "[$(entry ideate done), $(entry spec done), $(entry plan done)]"
  run_it "$(snap)" --order "$ORDER"
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "design" ]
}

@test "all done gives complete" {
  all="$(printf '%s' "$ORDER" | jq -Rc 'split(",") | map({stage:., status:"done", outputs:{}})')"
  write_manifest "$all"
  run_it "$(snap)" --order "$ORDER"
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "complete" ]
}

@test "no manifest gives the first stage and exists:false" {
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .exists)" = "false" ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "ideate" ]
  [ "$(printf '%s' "$output" | jq -c .outputs)" = "{}" ]
  [ "$(printf '%s' "$output" | jq -c .done_stages)" = "[]" ]
}

@test "start-at-var overrides detection" {
  write_manifest "[$(entry ideate done)]"
  run_it "$(snap '{"start_at":"plan"}')" --order "$ORDER" --start-at-var start_at
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "plan" ]
}

@test "empty start-at-var value does not override" {
  write_manifest "[$(entry ideate done)]"
  run_it "$(snap '{"start_at":""}')" --order "$ORDER" --start-at-var start_at
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "spec" ]
}

@test "without --order the canonical stage order applies" {
  write_manifest "[$(entry ideate done)]"
  run_it "$(snap)"
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "spec" ]
}

# --- Errors

@test "malformed JSON exits 1" {
  echo '{not json' > "$WORK/.arbitron/pipeline/s1.json"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"malformed manifest .arbitron/pipeline/s1.json"* ]]
}

@test "missing stages array exits 1" {
  echo '{"slug":"s1"}' > "$WORK/.arbitron/pipeline/s1.json"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"malformed manifest"* ]]
  [[ "$stderr" == *"stages"* ]]
}

@test "unknown stage exits 1" {
  write_manifest "[$(entry nonsense done)]"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"unknown stage"* ]]
  [[ "$stderr" == *"nonsense"* ]]
}

@test "unknown status exits 1" {
  write_manifest "[$(entry spec failed)]"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"unknown status"* ]]
}

@test "empty slug exits 1" {
  run_it "$(snap '{"slug":""}')" --order "$ORDER"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"slug"* ]]
}

@test "start_at outside --order exits 1" {
  write_manifest "[]"
  run_it "$(snap '{"start_at":"bogus"}')" --order "$ORDER" --start-at-var start_at
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"bogus"* ]]
}

@test "--require missing names the key and stage" {
  write_manifest "[$(entry spec done '{"spec_path":"s"}')]"
  run_it "$(snap)" --order "$ORDER" --explicit spec_path,design_paths --require spec_path,design_paths
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"design_paths"* ]]
  [[ "$stderr" == *"stage design"* ]]
}

@test "--require is satisfied by the manifest" {
  write_manifest "[$(entry design done '{"design_paths":["d.md"]}')]"
  run_it "$(snap)" --require design_paths
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .inputs.design_paths)" = '["d.md"]' ]
}

@test "--require is satisfied by an explicit value with no manifest" {
  run_it "$(snap '{"plan_paths":["p.md"]}')" --explicit plan_paths --require plan_paths
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .inputs.plan_paths)" = '["p.md"]' ]
}

@test "--optional alone does not satisfy --require" {
  run_it "$(snap)" --require idea_path --optional idea_path=
  [ "$status" -eq 1 ]
}

# --- Edge cases

@test "--explicit non-empty wins" {
  write_manifest "[$(entry spec done '{"spec_path":"manifest.md"}')]"
  run_it "$(snap '{"spec_path":"given.md"}')" --explicit spec_path
  [ "$(printf '%s' "$output" | jq -r .inputs.spec_path)" = "given.md" ]
}

@test "explicit numbers and bools count as non-empty" {
  write_manifest "[$(entry refine done '{"pr_number":7,"flag":true}')]"
  run_it "$(snap '{"pr_number":0,"flag":false}')" --explicit pr_number,flag
  [ "$(printf '%s' "$output" | jq -cS '.inputs')" = '{"flag":false,"pr_number":0}' ]
}

@test "explicit empty string and empty array fall through to the manifest" {
  write_manifest "[$(entry spec done '{"spec_path":"m.md"}'), $(entry design done '{"design_paths":["d"]}')]"
  run_it "$(snap '{"spec_path":"","design_paths":[]}')" --explicit spec_path,design_paths
  [ "$(printf '%s' "$output" | jq -r .inputs.spec_path)" = "m.md" ]
  [ "$(printf '%s' "$output" | jq -c .inputs.design_paths)" = '["d"]' ]
}

@test "base_branch: explicit wins, otherwise the manifest top-level applies" {
  write_manifest "[]" '{"base_branch":"dev"}'
  run_it "$(snap '{"base_branch":"rel"}')" --explicit base_branch --require base_branch
  [ "$(printf '%s' "$output" | jq -r .inputs.base_branch)" = "rel" ]
  run_it "$(snap '{"base_branch":""}')" --explicit base_branch --require base_branch
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .inputs.base_branch)" = "dev" ]
  [ "$(printf '%s' "$output" | jq -r .outputs.base_branch)" = "dev" ]
}

@test "outputs carry the top-level slug and branch" {
  write_manifest "[]"
  run_it "$(snap)"
  [ "$(printf '%s' "$output" | jq -r .outputs.slug)" = "s1" ]
  [ "$(printf '%s' "$output" | jq -r .outputs.branch)" = "feat" ]
}

@test "--optional typed defaults" {
  run_it "$(snap)" --optional 'a=[]' --optional 'b=false' --optional 'c=' --optional 'd=text' --optional 'e=3' --optional 'f=x=y'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -cS .inputs)" = '{"a":[],"b":false,"c":"","d":"text","e":3,"f":"x=y"}' ]
}

@test "--optional yields to the manifest and to explicit" {
  write_manifest "[$(entry spec done '{"spec_path":"m.md"}')]"
  run_it "$(snap)" --optional spec_path=dflt
  [ "$(printf '%s' "$output" | jq -r .inputs.spec_path)" = "m.md" ]
  run_it "$(snap '{"spec_path":"x.md"}')" --explicit spec_path --optional spec_path=dflt
  [ "$(printf '%s' "$output" | jq -r .inputs.spec_path)" = "x.md" ]
}

@test "unset explicit key with nothing behind it is emitted as an empty string" {
  run_it "$(snap)" --explicit idea_path
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c .inputs)" = '{"idea_path":""}' ]
}

@test "an empty manifest value counts as absent, so the --optional default applies" {
  write_manifest "[$(entry spec done '{"spec_path":""}')]"
  run_it "$(snap)" --optional spec_path=dflt.md
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .inputs.spec_path)" = "dflt.md" ]
}

@test "a top-level branch overrides a stage output named branch" {
  write_manifest "[$(entry ideate done '{"branch":"stale"}')]"
  run_it "$(snap)"
  [ "$(printf '%s' "$output" | jq -r .outputs.branch)" = "feat" ]
}

@test "later stage key wins on collision" {
  write_manifest "[$(entry spec done '{"k":"spec"}'), $(entry design done '{"k":"design"}')]"
  run_it "$(snap)" --order "$ORDER"
  [ "$(printf '%s' "$output" | jq -r .outputs.k)" = "design" ]
}

@test "an earlier stage appended after a later one makes the later one stale" {
  write_manifest "[$(entry design done '{"k":"design"}'), $(entry spec done '{"k":"spec"}')]"
  run_it "$(snap)"
  [ "$(printf '%s' "$output" | jq -r .outputs.k)" = "spec" ]
  [ "$(printf '%s' "$output" | jq -c .done_stages)" = '["spec"]' ]
  [ "$(printf '%s' "$output" | jq -c .stale)" = '["design"]' ]
}

@test "unknown extra fields are tolerated and preserved" {
  write_manifest '[{"stage":"spec","status":"done","outputs":{"spec_path":"s.md"},"note":"hi","extra":{"x":1}}]' '{"owner":"me","tags":["a"]}'
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .manifest.owner)" = "me" ]
  [ "$(printf '%s' "$output" | jq -c .manifest.tags)" = '["a"]' ]
  [ "$(printf '%s' "$output" | jq -r .manifest.stages[0].note)" = "hi" ]
  [ "$(printf '%s' "$output" | jq -r .latest.spec.note)" = "hi" ]
  [ "$(printf '%s' "$output" | jq -c .latest.spec.extra)" = '{"x":1}' ]
}

# --- Boundary

@test "zero stages" {
  write_manifest "[]"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .exists)" = "true" ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "ideate" ]
  [ "$(printf '%s' "$output" | jq -c .latest)" = "{}" ]
}

@test "empty branch gives exists:false, even with an empty slug" {
  run_it "$(snap '{"branch":"","slug":""}')" --fetch --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .exists)" = "false" ]
  [ "$(printf '%s' "$output" | jq -r .next_stage)" = "ideate" ]
}

@test "empty branch still resolves explicit and optional inputs" {
  run_it "$(snap '{"branch":"","slug":"","idea_path":"i.md"}')" --explicit idea_path --require idea_path --optional z=1
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -cS .inputs)" = '{"idea_path":"i.md","z":1}' ]
}

@test "--fetch with the manifest absent on origin gives exists:false" {
  run_it "$(snap)" --fetch --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .exists)" = "false" ]
}

@test "unknown argument exits 1" {
  run_it "$(snap)" --bogus
  [ "$status" -eq 1 ]
}

@test "unset work_dir with a branch exits 1" {
  run_it '{"branch":"feat","slug":"s1"}'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"work_dir"* ]]
}

@test "slug with a path separator exits 1" {
  run_it "$(snap '{"slug":"../x"}')"
  [ "$status" -eq 1 ]
}

# --- Freshness: a rerun of an upstream stage invalidates its descendants

full_pipeline() {
  printf '%s' "$ORDER" | jq -Rc 'split(",") | map({stage:., status:"done", outputs:{(. + "_out"): .}})'
}

@test "rerun of spec on a completed pipeline, interrupted before design, resumes at design" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$status" -eq 0 ]
  [ "$(jq -r .next_stage <<<"$output")" = design ]
  [ "$(jq -c .done_stages <<<"$output")" = '["ideate","spec"]' ]
  [ "$(jq -c .stale <<<"$output")" = '["design","plan","implement","harden","refine","review"]' ]
}

@test "stale descendants do not leak outputs or resolved inputs" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}}]")"
  run_it "$(snap)" --order "$ORDER" --require spec_out --optional design_out=none
  [ "$status" -eq 0 ]
  [ "$(jq -r '.outputs.spec_out' <<<"$output")" = spec2 ]
  [ "$(jq -r '.outputs.design_out // "none"' <<<"$output")" = none ]
  [ "$(jq -r '.outputs.plan_out // "none"' <<<"$output")" = none ]
  [ "$(jq -r '.inputs.design_out' <<<"$output")" = none ]
}

@test "a stale descendant's output no longer satisfies --require" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}}]")"
  run_it "$(snap)" --order "$ORDER" --require plan_out
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"required input"* ]]
}

@test "rerun, interruption, resume: the replacement design makes the old plans stale only until plan reruns" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -r .next_stage <<<"$output")" = design ]
  # resume: design reruns, then plan; each makes the stages after it re-run.
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}},{stage:\"design\",status:\"done\",outputs:{design_out:\"d2\"}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -r .next_stage <<<"$output")" = plan ]
  [ "$(jq -r '.outputs.design_out' <<<"$output")" = d2 ]
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"spec\",status:\"done\",outputs:{spec_out:\"spec2\"}},{stage:\"design\",status:\"done\",outputs:{design_out:\"d2\"}},{stage:\"plan\",status:\"done\",outputs:{plan_out:\"p2\"}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -r .next_stage <<<"$output")" = implement ]
}

@test "a newer blocked upstream entry invalidates descendants" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"design\",status:\"blocked\",outputs:{}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -r .next_stage <<<"$output")" = design ]
  [ "$(jq -c .done_stages <<<"$output")" = '["ideate","spec"]' ]
  [ "$(jq -r '.outputs.plan_out // "none"' <<<"$output")" = none ]
  [ "$(jq -r '.outputs.spec_out' <<<"$output")" = spec ]
}

@test "a rerun of the last stage invalidates nothing" {
  write_manifest "$(full_pipeline | jq -c ". + [{stage:\"review\",status:\"done\",outputs:{review_out:\"r2\"}}]")"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -r .next_stage <<<"$output")" = complete ]
  [ "$(jq -c .stale <<<"$output")" = '[]' ]
  [ "$(jq -r .outputs.review_out <<<"$output")" = r2 ]
}

@test "a pipeline started past the first stages (no upstream entries) is fresh" {
  write_manifest "[$(entry design done '{"design_out":"d"}'), $(entry plan done '{"plan_out":"p"}')]"
  run_it "$(snap)" --order "$ORDER"
  [ "$(jq -c .done_stages <<<"$output")" = '["design","plan"]' ]
  [ "$(jq -r .next_stage <<<"$output")" = ideate ]
}

# --- Rename compatibility

@test "entries stamped feature-pipeline-next count the same as feature-pipeline ones" {
  write_manifest "$(full_pipeline | jq -c 'to_entries | map(.value + {workflow: (if .key % 2 == 0 then "feature-pipeline-next" else "feature-pipeline" end)})')"
  run_it "$(snap)" --order "$ORDER" --require refine_out,review_out
  [ "$status" -eq 0 ]
  [ "$(jq -r .next_stage <<<"$output")" = complete ]
  [ "$(jq -c .stale <<<"$output")" = '[]' ]
  [ "$(jq -c .done_stages <<<"$output")" = "$(printf '%s' "$ORDER" | jq -Rc 'split(",")')" ]
  [ "$(jq -r '.manifest.stages[6].workflow' <<<"$output")" = feature-pipeline-next ]
  [ "$(jq -r .inputs.review_out <<<"$output")" = review ]
}
