#!/bin/sh
# Tests for the ralph-loop init-workspace.sh: in_place, pr_base guard and
# baseline_path handling. Uses a local file:// origin; no network.
#
#   happy-path : in_place fresh and resume stay on base_branch
#   success    : baseline_present true/false
#   failure    : in_place without a usable pr_base
#   error      : non-boolean in_place, missing baseline_path file
#   edge-case  : failures happen before any git work
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/init-workspace.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }

git_q() { git -c user.email=t@t -c user.name=t -c init.defaultBranch=main "$@" >/dev/null 2>&1; }

ORIGIN="$TMP/origin.git"
SEED="$TMP/seed"
git_q init --bare "$ORIGIN"
git_q init "$SEED"
(
  cd "$SEED"
  echo a > a.txt; mkdir -p docs/reference; echo '{"suites":[]}' > docs/reference/test-baseline.json
  git_q add .; git_q commit -m base
  git_q remote add origin "file://$ORIGIN"
  git_q push -u origin main
  git_q checkout -b feat/x
  echo b > b.txt; git_q add .; git_q commit -m feat
  git_q push -u origin feat/x
  git_q checkout -b feat/resumed
  echo f > docs/reference/focus-areas.md; git_q add .; git_q commit -m plan
  git_q push -u origin feat/resumed
  # A review plan merged to a pr_base (plan-main) is inherited by every branch
  # cut from it; only a branch that changed the plan owns its approvals.
  git_q checkout main
  git_q checkout -b plan-main
  echo base-plan > docs/reference/focus-areas.md; git_q add .; git_q commit -m "plan merged to pr_base"
  git_q push -u origin plan-main
  git_q checkout -b feat/inherits
  echo c > c.txt; git_q add .; git_q commit -m "feature work only"
  git_q push -u origin feat/inherits
  git_q checkout plan-main
  git_q checkout -b feat/owned
  echo earned-approvals >> docs/reference/focus-areas.md; git_q add .; git_q commit -m "branch-owned plan"
  git_q push -u origin feat/owned
)

# run <ws-name> <extra-json-object-fields...>; ws dir is $TMP/<name>/<instance>.
ws() { mkdir -p "$TMP/$1/inst00001"; echo "$TMP/$1/inst00001"; }
run() {
  W="$1"; shift
  jq -n --arg ws "$W" --arg url "file://$ORIGIN" '{workspace:$ws, repo_url:$url}' \
    | jq -c ". + $1" | sh "$SCRIPT" 2>"$W.err"
}

# ---------------------------------------------------------------- happy path
W="$(ws fresh)"
out="$(run "$W" '{"base_branch":"feat/x","in_place":true,"pr_base":"main"}')"
check "in_place fresh stays on base_branch" "$(printf '%s' "$out" | jq -r .branch)" "feat/x"
check "in_place fresh emits in_place true" "$(printf '%s' "$out" | jq -r .in_place)" "true"
check "in_place fresh worktree is on base_branch" "$(git -C "$W/work" rev-parse --abbrev-ref HEAD)" "feat/x"

W="$(ws resume)"
out="$(run "$W" '{"base_branch":"feat/resumed","in_place":true,"pr_base":"main"}')"
check "in_place resume stays on base_branch" "$(printf '%s' "$out" | jq -r .branch)" "feat/resumed"
check "in_place resume is detected as resumed" "$(printf '%s' "$out" | jq -r .resumed)" "true"

W="$(ws inherited)"
out="$(run "$W" '{"base_branch":"feat/inherits","in_place":true,"pr_base":"plan-main"}')"
check "in_place with a plan inherited from pr_base is NOT resumed" "$(printf '%s' "$out" | jq -r .resumed)" "false"
check "...but still works on base_branch" "$(printf '%s' "$out" | jq -r .branch)" "feat/inherits"
check "...and the worktree is on base_branch" "$(git -C "$W/work" rev-parse --abbrev-ref HEAD)" "feat/inherits"
check "...and says why" "$(grep -c 'inherited' "$W.err")" "1"

W="$(ws owned)"
out="$(run "$W" '{"base_branch":"feat/owned","in_place":true,"pr_base":"plan-main"}')"
check "in_place with a plan changed on the branch IS resumed" "$(printf '%s' "$out" | jq -r .resumed)" "true"
check "...on base_branch" "$(printf '%s' "$out" | jq -r .branch)" "feat/owned"

W="$(ws inherited-ff)"
out="$(run "$W" '{"base_branch":"feat/inherits","in_place":true,"pr_base":"plan-main","force_fresh":true}')"
check "force_fresh is still fresh" "$(printf '%s' "$out" | jq -r .resumed)" "false"

W="$(ws notinplace-plan)"
out="$(run "$W" '{"base_branch":"feat/inherits"}')"
check "without in_place an existing plan still resumes (unchanged behaviour)" "$(printf '%s' "$out" | jq -r .resumed)" "true"

W="$(ws notinplace)"
out="$(run "$W" '{"base_branch":"feat/x"}')"
check "default still cuts a dev- branch" "$(printf '%s' "$out" | jq -r '.branch|startswith("dev-feature-")')" "true"
check "default emits in_place false" "$(printf '%s' "$out" | jq -r .in_place)" "false"

# ------------------------------------------------------------------- success
check "baseline_present false without baseline_path" "$(printf '%s' "$out" | jq -r .baseline_present)" "false"
W="$(ws bl)"
out="$(run "$W" '{"base_branch":"feat/x","baseline_path":"docs/reference/test-baseline.json"}')"
check "baseline_present true when the file is at the tip" "$(printf '%s' "$out" | jq -r .baseline_present)" "true"

# ------------------------------------------------------------------- failure
# Failing before git means no clone was ever made.
W="$(ws nopr)"
if run "$W" '{"base_branch":"feat/x","in_place":true,"pr_base":""}' >/dev/null; then
  check "in_place with empty pr_base exits 1" "exited 0" "non-zero"
else
  check "in_place with empty pr_base exits 1" "non-zero" "non-zero"
fi
check "in_place with empty pr_base exits 1 before git (no src dir)" "$([ -d "$W/src" ] && echo yes || echo no)" "no"
check "in_place failure explains itself" "$(grep -c 'pr_base different from base_branch' "$W.err")" "1"

W="$(ws eqpr)"
if run "$W" '{"base_branch":"feat/x","in_place":true,"pr_base":"feat/x"}' >/dev/null; then
  check "in_place with pr_base == base_branch exits 1" "exited 0" "non-zero"
else
  check "in_place with pr_base == base_branch exits 1" "non-zero" "non-zero"
fi
check "pr_base == base_branch fails before git (no src dir)" "$([ -d "$W/src" ] && echo yes || echo no)" "no"

W="$(ws nobase)"
if run "$W" '{"in_place":true,"pr_base":"main"}' >/dev/null; then
  check "in_place with empty base_branch exits 1" "exited 0" "non-zero"
else
  check "in_place with empty base_branch exits 1" "non-zero" "non-zero"
fi
check "empty base_branch fails before git (no src dir)" "$([ -d "$W/src" ] && echo yes || echo no)" "no"

W="$(ws typopr)"
if run "$W" '{"base_branch":"feat/x","in_place":true,"pr_base":"no-such-branch"}' >/dev/null; then
  check "in_place with nonexistent pr_base exits 1" "exited 0" "non-zero"
else
  check "in_place with nonexistent pr_base exits 1" "non-zero" "non-zero"
fi
check "nonexistent pr_base names the branch" "$(grep -c 'no-such-branch' "$W.err")" "1"
check "nonexistent pr_base creates no worktree" "$([ -d "$W/work" ] && echo yes || echo no)" "no"

# --------------------------------------------------------------------- error
W="$(ws badbool)"
if run "$W" '{"base_branch":"feat/x","in_place":"yes","pr_base":"main"}' >/dev/null; then
  check "non-boolean in_place exits 1" "exited 0" "non-zero"
else
  check "non-boolean in_place exits 1" "non-zero" "non-zero"
fi

W="$(ws nobl)"
if run "$W" '{"base_branch":"feat/x","baseline_path":"docs/reference/missing.json"}' >/dev/null; then
  check "missing baseline_path file exits 1" "exited 0" "non-zero"
else
  check "missing baseline_path file exits 1" "non-zero" "non-zero"
fi

echo
if [ "$fails" -eq 0 ]; then echo "all init-workspace tests passed"; else
  echo "$fails init-workspace test(s) FAILED"; exit 1; fi
