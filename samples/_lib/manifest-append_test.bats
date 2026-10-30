#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/manifest-append.sh"
  READ="$BATS_TEST_DIRNAME/manifest-read.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  ORIGIN="$T/origin.git"
  git init -q --bare -b main "$ORIGIN"
  git clone -q "$ORIGIN" "$T/seed" 2>/dev/null
  git -C "$T/seed" checkout -q -b main
  echo a > "$T/seed/a.txt"
  git -C "$T/seed" add a.txt
  git -C "$T/seed" commit -q -m init
  git -C "$T/seed" push -q origin main
  git -C "$T/seed" checkout -q -b dev-x
  git -C "$T/seed" push -q origin dev-x
  git -C "$T/seed" checkout -q -b feat/x
  git -C "$T/seed" push -q origin feat/x
  git -C "$ORIGIN" symbolic-ref HEAD refs/heads/main
  WORK="$T/work"
  git clone -q "$ORIGIN" "$WORK" 2>/dev/null
  git -C "$WORK" checkout -q -b dev-x origin/dev-x
  WS="$T/instances/abcdef0123456789"
  mkdir -p "$WS"
  MP=".arbitron/pipeline/s.json"
}

# snap [extras-json]
snap() {
  local extra="${1:-}"
  [ -n "$extra" ] || extra='{}'
  jq -n --arg w "$WORK" --arg ws "$WS" --arg b "${BRANCH:-dev-x}" \
    "{work_dir:\$w, workspace:\$ws, branch:\$b, slug:\"s\", base_branch:\"main\"} + $extra"
}

# run_it <snapshot-json> <args...>
run_it() {
  local s="$1"; shift
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$s" "$SCRIPT" "$@"
}

# manifest <ref> reads the manifest from origin.
manifest() { git -C "$ORIGIN" show "$1:$MP"; }
osha() { git -C "$ORIGIN" rev-parse "refs/heads/$1"; }

# foreign_push <branch> <name>: a second clone lands an unrelated commit.
foreign_push() {
  local c="$T/other"
  [ -d "$c" ] || git clone -q "$ORIGIN" "$c" 2>/dev/null
  env -u GIT_DIR -u GIT_WORK_TREE git -C "$c" fetch -q origin
  env -u GIT_DIR -u GIT_WORK_TREE git -C "$c" checkout -q -B "$1" "origin/$1"
  echo "$2" > "$c/$2.txt"
  env -u GIT_DIR -u GIT_WORK_TREE git -C "$c" add "$2.txt"
  env -u GIT_DIR -u GIT_WORK_TREE git -C "$c" commit -q -m "$2"
  env -u GIT_DIR -u GIT_WORK_TREE git -C "$c" push -q origin "$1"
}

fails_clean() {
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

# --- Happy path

@test "creates the manifest on first append" {
  run_it "$(snap)" --workflow spec spec done '{spec_path: "docs/s.md"}'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .appended)" = true ]
  [ "$(printf '%s' "$output" | jq -r .path)" = "$MP" ]
  [ "$(printf '%s' "$output" | jq -r .manifest_commit)" = "$(osha dev-x)" ]
  [ "$(printf '%s' "$output" | jq -r .entry.stage)" = spec ]
  [ "$(manifest dev-x | jq -r '.slug, .branch, .base_branch, (.stages | length)' | tr '\n' ' ')" = "s dev-x main 1 " ]
  [ "$(manifest dev-x | jq -r '.stages[0].outputs.spec_path')" = "docs/s.md" ]
  [ "$(manifest dev-x | jq -r '.stages[0].instance_id')" = abcdef0123456789 ]
  [ "$(manifest dev-x | jq -r '.stages[0].finished_at')" != null ]
}

@test "first write records base_branch from stdin; a later append with a different base_branch does not overwrite it" {
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  git -C "$WORK" fetch -q origin
  run_it "$(snap '{base_branch:"other"}')" --workflow design design done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -r .base_branch)" = main ]
}

@test "no base_branch on stdin: the manifest omits it" {
  run_it "$(snap '{base_branch:""}')" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq 'has("base_branch")')" = false ]
}

@test "second append appends" {
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  run_it "$(snap)" --workflow design design blocked '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -r '[.stages[].stage] | join(",")')" = "spec,design" ]
  [ "$(manifest dev-x | jq -r '.stages[1].status')" = blocked ]
  [ "$(git -C "$ORIGIN" rev-list --count main..dev-x)" = 2 ]
}

@test "outputs stay typed (array, number, bool)" {
  run_it "$(snap '{list:["a","b"], n:3, ok:true}')" --workflow plan plan done '{list, n, ok}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -c '.stages[0].outputs')" = '{"list":["a","b"],"n":3,"ok":true}' ]
  [ "$(printf '%s' "$output" | jq -c .outputs)" = '{"list":["a","b"],"n":3,"ok":true}' ]
}

@test "entry.workflow equals --workflow" {
  run_it "$(snap)" --workflow my-flow spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -r '.stages[0].workflow')" = my-flow ]
  [ "$(printf '%s' "$output" | jq -r .entry.workflow)" = my-flow ]
}

@test "entry.commit is the origin sha before the manifest commit" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .entry.commit)" = "$before" ]
}

@test "pushes to origin" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(osha dev-x)" != "$before" ]
  [ "$(git -C "$ORIGIN" log -1 --format=%s dev-x)" = "chore(pipeline): record spec done" ]
}

# --- Edge cases

@test "detached work_dir: lands on origin; work_dir stays detached at its old sha" {
  git -C "$WORK" checkout -q --detach
  sha="$(git -C "$WORK" rev-parse HEAD)"
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq '.stages | length')" = 1 ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$sha" ]
  run git -C "$WORK" symbolic-ref -q HEAD
  [ "$status" -ne 0 ]
  [ ! -e "$WORK/$MP" ]
}

@test "unpushed local branch is pushed first with -u" {
  git -C "$WORK" checkout -q -b newb
  echo w > "$WORK/w.txt"
  git -C "$WORK" add w.txt
  git -C "$WORK" commit -q -m work
  BRANCH=newb run_it "$(BRANCH=newb snap)" --workflow ideate ideate done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$ORIGIN" show newb:w.txt)" = w ]
  [ "$(manifest newb | jq '.stages | length')" = 1 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref 'newb@{u}')" = origin/newb ]
}

@test "work_dir on the branch is fast-forwarded after" {
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$(osha dev-x)" ]
  [ -f "$WORK/$MP" ]
  [ "$(git -C "$WORK" symbolic-ref HEAD)" = refs/heads/dev-x ]
}

@test "unrelated dirty file in work_dir is untouched and uncommitted" {
  echo dirty >> "$WORK/a.txt"
  echo scratch > "$WORK/scratch.txt"
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" status --porcelain | sort | tr '\n' ' ')" = " M a.txt ?? scratch.txt " ]
  [ "$(tail -1 "$WORK/a.txt")" = dirty ]
  [ "$(git -C "$ORIGIN" show dev-x:a.txt)" = a ]
}

@test "remote ahead by a foreign commit: manifest worktree starts from origin" {
  foreign_push dev-x foreign
  git -C "$WORK" checkout -q --detach
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$ORIGIN" show dev-x:foreign.txt)" = foreign ]
  [ "$(manifest dev-x | jq '.stages | length')" = 1 ]
}

@test "remote ahead with work_dir on the branch: pushes nothing stale and fast-forwards" {
  foreign_push dev-x foreign
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$(osha dev-x)" ]
  [ -f "$WORK/foreign.txt" ]
}

@test "branch named feat/x works" {
  BRANCH=feat/x run_it "$(BRANCH=feat/x snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest feat/x | jq -r .branch)" = feat/x ]
  git -C "$WORK" checkout -q -b feat/x origin/feat/x
  git -C "$WORK" fetch -q origin
  BRANCH=feat/x run_it "$(BRANCH=feat/x snap)" --workflow design design done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest feat/x | jq '.stages | length')" = 2 ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$(osha feat/x)" ]
}

@test "stale private worktree from an earlier stage is reused" {
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ -e "$WS/.manifest-wt/.git" ]
  run_it "$(snap)" --workflow design design done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq '.stages | length')" = 2 ]
}

@test "unknown extra manifest fields are preserved" {
  c="$T/seedx"
  git clone -q "$ORIGIN" "$c" 2>/dev/null
  git -C "$c" checkout -q -B dev-x origin/dev-x
  mkdir -p "$c/.arbitron/pipeline"
  jq -n '{slug:"s", base_branch:"main", branch:"dev-x", future:{k:[1,2]},
          stages:[{stage:"spec", status:"done", outputs:{spec_path:"p"}, commit:"c", finished_at:"t",
                   instance_id:"i", workflow:"spec", note:"keep me"}]}' > "$c/$MP"
  git -C "$c" add "$MP"
  git -C "$c" commit -q -m seed
  git -C "$c" push -q origin dev-x
  git -C "$WORK" fetch -q origin
  git -C "$WORK" merge -q --ff-only origin/dev-x
  run_it "$(snap)" --workflow design design done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -c .future)" = '{"k":[1,2]}' ]
  [ "$(manifest dev-x | jq -r '.stages[0].note')" = "keep me" ]
  [ "$(manifest dev-x | jq '.stages | length')" = 2 ]
}

# --- Errors

@test "bad stage" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec nonsense done '{}'
  fails_clean
  [ "$(osha dev-x)" = "$before" ]
}

@test "bad status" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec failed '{}'
  fails_clean
  [ "$(osha dev-x)" = "$before" ]
}

@test "missing --workflow" {
  before="$(osha dev-x)"
  run_it "$(snap)" spec done '{}'
  fails_clean
  [ "$(osha dev-x)" = "$before" ]
}

@test "empty --workflow" {
  run_it "$(snap)" --workflow "" spec done '{}'
  fails_clean
}

@test "empty branch" {
  run_it "$(snap '{branch:""}')" --workflow spec spec done '{}'
  fails_clean
}

@test "filter yields non-object" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done '[1]'
  fails_clean
  run_it "$(snap)" --workflow spec spec done 'empty'
  fails_clean
  run_it "$(snap)" --workflow spec spec done '{}, {}'
  fails_clean
  [ "$(osha dev-x)" = "$before" ]
}

@test "filter calls error()" {
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done 'error("boom")'
  fails_clean
  [[ "$stderr" == *boom* ]]
  [ "$(osha dev-x)" = "$before" ]
}

@test "local branch diverged from origin" {
  echo l > "$WORK/l.txt"
  git -C "$WORK" add l.txt
  git -C "$WORK" commit -q -m local
  foreign_push dev-x foreign
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done '{}'
  fails_clean
  [[ "$stderr" == *"diverged from origin"* ]]
  [ "$(osha dev-x)" = "$before" ]
}

@test "branch absent on origin with a detached work_dir" {
  git -C "$WORK" checkout -q --detach
  BRANCH=nope run_it "$(BRANCH=nope snap)" --workflow spec spec done '{}'
  fails_clean
  [[ "$stderr" == *"branch nope not on origin"* ]]
  run git -C "$ORIGIN" rev-parse --verify -q refs/heads/nope
  [ "$status" -ne 0 ]
}

@test "an unreachable origin is reported as a fetch failure, not a missing branch" {
  git -C "$WORK" checkout -q --detach
  git -C "$WORK" remote set-url origin "$T/no-such-origin.git"
  run_it "$(snap)" --workflow spec spec done '{}'
  fails_clean
  [[ "$stderr" == *"fetch of dev-x from origin failed"* ]]
  [[ "$stderr" != *"not on origin"* ]]
  [[ "$stderr" == *"no-such-origin"* ]]
}

@test "an unreachable origin with work_dir on the branch is not reported as divergence" {
  git -C "$WORK" remote set-url origin "$T/no-such-origin.git"
  run_it "$(snap)" --workflow spec spec done '{}'
  fails_clean
  [[ "$stderr" == *"fetch of dev-x from origin failed"* ]]
  [[ "$stderr" != *"diverged"* ]]
}

@test "a push failure that is not a rejection exits 1 with git's message and no retry" {
  git -C "$WORK" checkout -q --detach
  mkdir -p "$WORK/.git/hooks"
  printf '#!/bin/sh\necho hook-says-no >&2\nexit 1\n' > "$WORK/.git/hooks/pre-push"
  chmod +x "$WORK/.git/hooks/pre-push"
  before="$(osha dev-x)"
  run_it "$(snap)" --workflow spec spec done '{}'
  fails_clean
  [[ "$stderr" == *hook-says-no* ]]
  [[ "$stderr" != *retrying* ]]
  [ "$(osha dev-x)" = "$before" ]
}

@test "a private worktree whose gitdir was removed is recreated" {
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  rm -rf "$WORK/.git/worktrees"
  run_it "$(snap)" --workflow design design done '{}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq '.stages | length')" = 2 ]
}

@test "a branch already on origin gets its upstream set even with nothing to push" {
  git -C "$WORK" branch -q --unset-upstream dev-x
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref 'dev-x@{u}')" = origin/dev-x ]
}

# install_skip_ci_hook: the commit-msg hook init-workspace installs.
install_skip_ci_hook() {
  mkdir -p "$WORK/.git/hooks"
  printf '#!/bin/sh\ngrep -q "skip ci" "$1" || printf "\\n[skip ci]\\n" >> "$1"\n' > "$WORK/.git/hooks/commit-msg"
  chmod +x "$WORK/.git/hooks/commit-msg"
}

@test "the manifest commit carries the [skip ci] hook's marker by default" {
  install_skip_ci_hook
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [[ "$(git -C "$ORIGIN" log -1 --format=%B dev-x)" == *"[skip ci]"* ]]
}

@test "--no-skip-ci commits past the hook so the branch head gets CI" {
  install_skip_ci_hook
  run_it "$(snap)" --workflow feature-pipeline-next --no-skip-ci refine done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$ORIGIN" log -1 --format=%s dev-x)" = "chore(pipeline): record refine done" ]
  [[ "$(git -C "$ORIGIN" log -1 --format=%B dev-x)" != *"skip ci"* ]]
}

# --- Boundary

install_racing_hook() {
  # $1 = max number of foreign pushes
  mkdir -p "$WORK/.git/hooks"
  cat > "$WORK/.git/hooks/pre-push" <<HOOK
#!/bin/sh
n=\$(cat "$T/race-count" 2>/dev/null || echo 0)
[ "\$n" -lt $1 ] || exit 0
echo \$((n + 1)) > "$T/race-count"
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
c="$T/racer"
[ -d "\$c" ] || git clone -q "$ORIGIN" "\$c" 2>/dev/null
git -C "\$c" fetch -q origin
git -C "\$c" checkout -q -B dev-x origin/dev-x
echo \$n > "\$c/race-\$n.txt"
git -C "\$c" add .
git -C "\$c" commit -q -m "race \$n"
git -C "\$c" push -q origin dev-x
HOOK
  chmod +x "$WORK/.git/hooks/pre-push"
}

@test "concurrent append: a second clone pushes first, then one retry succeeds" {
  git -C "$WORK" checkout -q --detach
  install_racing_hook 1
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(cat "$T/race-count")" = 1 ]
  [ "$(git -C "$ORIGIN" show dev-x:race-0.txt)" = 0 ]
  [ "$(manifest dev-x | jq '.stages | length')" = 1 ]
  [ "$(printf '%s' "$output" | jq -r .manifest_commit)" = "$(osha dev-x)" ]
  # the entry's commit is the new origin tip before the manifest commit
  [ "$(printf '%s' "$output" | jq -r .entry.commit)" = "$(git -C "$ORIGIN" rev-parse dev-x~1)" ]
}

@test "conflict again on retry exits 1" {
  git -C "$WORK" checkout -q --detach
  install_racing_hook 5
  run_it "$(snap)" --workflow spec spec done '{}'
  fails_clean
  [ "$(cat "$T/race-count")" = 2 ]
  run git -C "$ORIGIN" cat-file -e "dev-x:$MP"
  [ "$status" -ne 0 ]
}

# --- Integration

@test "never commits on a detached HEAD" {
  git -C "$WORK" checkout -q --detach
  before="$(git -C "$WORK" reflog)"
  sha="$(git -C "$WORK" rev-parse HEAD)"
  run_it "$(snap)" --workflow spec spec done '{}'
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" reflog)" = "$before" ]
  [ "$(git -C "$WORK" rev-parse HEAD)" = "$sha" ]
  [ "$(git -C "$WS/.manifest-wt" symbolic-ref HEAD)" = refs/heads/arbitron-manifest/abcdef01 ]
}

@test "round-trips with manifest-read" {
  run_it "$(snap '{list:["a","b"], n:3, ok:true}')" --workflow spec spec done '{spec_path: "docs/s.md"}'
  [ "$status" -eq 0 ]
  run_it "$(snap '{list:["a","b"], n:3, ok:true}')" --workflow design design done '{design_paths: .list, n, ok}'
  [ "$status" -eq 0 ]
  git -C "$WORK" checkout -q --detach
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" --fetch' _ "$(snap)" "$READ"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r '.latest.design.status')" = done ]
  [ "$(printf '%s' "$output" | jq -c '.outputs.design_paths')" = '["a","b"]' ]
  [ "$(printf '%s' "$output" | jq '.outputs.n')" = 3 ]
  [ "$(printf '%s' "$output" | jq '.outputs.ok')" = true ]
  [ "$(printf '%s' "$output" | jq -r '.outputs.spec_path')" = docs/s.md ]
  [ "$(printf '%s' "$output" | jq -r '.next_stage')" = ideate ]
  # a later blocked entry for design wins as latest and re-opens the stage
  git -C "$WORK" checkout -q dev-x
  git -C "$WORK" fetch -q origin
  git -C "$WORK" merge -q --ff-only origin/dev-x
  run_it "$(snap)" --workflow design design blocked '{}'
  [ "$status" -eq 0 ]
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" --fetch' _ "$(snap)" "$READ"
  [ "$(printf '%s' "$output" | jq -r '.latest.design.status')" = blocked ]
  [ "$(printf '%s' "$output" | jq -r '.next_stage')" = ideate ]
}

# The parent was renamed feature-pipeline-next -> feature-pipeline. A branch
# whose manifest entries were stamped with the old name must still resume: the
# stamp is provenance only, never a filter.
@test "resumes a manifest stamped feature-pipeline-next under the renamed parent" {
  run_it "$(snap)" --workflow feature-pipeline-next ideate done '{idea_path: "docs/i.md", provided: true}'
  [ "$status" -eq 0 ]
  run_it "$(snap)" --workflow spec spec done '{spec_path: "docs/s.md"}'
  [ "$status" -eq 0 ]
  run_it "$(snap)" --workflow feature-pipeline spec blocked '{}'
  [ "$status" -eq 0 ]
  run_it "$(snap)" --workflow spec spec done '{spec_path: "docs/s2.md"}'
  [ "$status" -eq 0 ]
  [ "$(manifest dev-x | jq -c '[.stages[].workflow]')" = '["feature-pipeline-next","spec","feature-pipeline","spec"]' ]
  git -C "$WORK" checkout -q --detach
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" --fetch --order ideate,spec,design,plan,implement,harden,refine,review' _ "$(snap)" "$READ"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c '.done_stages')" = '["ideate","spec"]' ]
  [ "$(printf '%s' "$output" | jq -r '.outputs.idea_path')" = docs/i.md ]
  [ "$(printf '%s' "$output" | jq -r '.outputs.spec_path')" = docs/s2.md ]
  [ "$(printf '%s' "$output" | jq -r '.next_stage')" = design ]
}
