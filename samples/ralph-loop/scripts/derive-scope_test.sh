#!/bin/sh
# Tests for derive-scope.sh — works out what the branch under refinement changed.
#
# Uses a local file:// origin so shallow clones behave exactly as they do against
# GitHub (git silently ignores --depth for plain-path clones, so file:// matters).
# No network.
#
# Covers all five scenario categories:
#   happy-path : shallow clone with origin/main absent still finds the fork point
#   success    : the changed-file set is exactly the branch's own work
#   failure    : an empty scope is refused rather than seeding an empty plan
#   error      : missing work_dir exits non-zero
#   edge-case  : scope_note passthrough, non-main default branch, deep history
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/derive-scope.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }

git_q() { git -c user.email=t@t -c user.name=t -c init.defaultBranch=main "$@" >/dev/null 2>&1; }

# --- Fixture: origin with `main` (several commits) and a feature branch ------
ORIGIN="$TMP/origin.git"
SEED="$TMP/seed"
git_q init --bare "$ORIGIN"
git_q init "$SEED"
cd "$SEED"
for i in 1 2 3 4 5; do echo "base $i" > "base$i.txt"; git_q add .; git_q commit -m "base $i"; done
git_q remote add origin "file://$ORIGIN"
git_q push -u origin main
git_q checkout -b feature/work
echo hello > feature-a.txt
mkdir -p sub && echo world > sub/feature-b.txt
git_q add .; git_q commit -m "feature work"
git_q push -u origin feature/work
# main moves on AFTER the fork, so a two-dot fallback would over-report
git_q checkout main
echo later > landed-after-fork.txt
git_q add .; git_q commit -m "landed after fork"
git_q push origin main
cd - >/dev/null

# --- Reproduce init-workspace.sh: shallow clone of ONLY the base ref ---------
SRC="$TMP/src"; WORK="$TMP/work"
git_q clone --depth 1 --branch=feature/work "file://$ORIGIN" "$SRC"
git_q -C "$SRC" worktree add -b ralph-x "$WORK" feature/work

run() { printf '{"work_dir":"%s","base_branch":"feature/work","scope_note":"%s"}' "$WORK" "${1:-}" | sh "$SCRIPT" 2>/dev/null; }

# ---------------------------------------------------------------- happy path
check "happy: clone really is shallow" "$(git -C "$WORK" rev-parse --is-shallow-repository)" "true"
if git -C "$WORK" rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
  check "happy: origin/main absent before the script runs" "present" "absent"
else
  check "happy: origin/main absent before the script runs" "absent" "absent"
fi
out="$(run)"
check "happy: recovers the true fork point" "$(printf '%s' "$out" | jq -r .scope_method)" "merge-base"
check "happy: identifies the default branch" "$(printf '%s' "$out" | jq -r .default_branch)" "main"

# ------------------------------------------------------------------- success
# Exactly the branch's own two files — not the commit that landed on main after
# the fork, which a two-dot diff would wrongly include.
check "success: correct file count" "$(printf '%s' "$out" | jq -r .file_count)" "2"
check "success: includes nested file" \
  "$(printf '%s' "$out" | jq -r '.changed_files|index("sub/feature-b.txt")!=null')" "true"
check "success: excludes post-fork main commit" \
  "$(printf '%s' "$out" | jq -r '.changed_files|index("landed-after-fork.txt")==null')" "true"
check "success: commit log captured" \
  "$(printf '%s' "$out" | jq -r '.commit_log|test("feature work")')" "true"
check "success: not truncated at this size" "$(printf '%s' "$out" | jq -r .files_truncated)" "false"

# ------------------------------------------------------------------- failure
# A branch with nothing of its own gives the planner no scope. Seeding a review
# plan from an empty diff would produce a plan that reviews nothing, so refuse.
SRC2="$TMP/src2"; WORK2="$TMP/work2"
git_q clone --depth 1 --branch=main "file://$ORIGIN" "$SRC2"
git_q -C "$SRC2" worktree add -b ralph-y "$WORK2" main
if printf '{"work_dir":"%s","base_branch":"main"}' "$WORK2" | sh "$SCRIPT" >/dev/null 2>&1; then
  check "failure: empty scope is refused" "exited 0" "non-zero"
else
  check "failure: empty scope is refused" "non-zero" "non-zero"
fi

# --------------------------------------------------------------------- error
if printf '{}' | sh "$SCRIPT" >/dev/null 2>&1; then
  check "error: missing work_dir exits non-zero" "exited 0" "non-zero"
else
  check "error: missing work_dir exits non-zero" "non-zero" "non-zero"
fi

# ----------------------------------------------------------------- edge case
check "edge: scope_note passes through" \
  "$(run 'only the consent ledger' | jq -r .scope_note)" "only the consent ledger"
check "edge: merge_base is a real sha" \
  "$(printf '%s' "$out" | jq -r '.merge_base|test("^[0-9a-f]{40}$")')" "true"

# A repo whose default branch is not main must still resolve.
ORIGIN2="$TMP/origin2.git"; SEED2="$TMP/seed2"
git_q init --bare "$ORIGIN2"
git_q init "$SEED2"
cd "$SEED2"
git_q checkout -b trunk
echo a > a.txt; git_q add .; git_q commit -m a
git_q remote add origin "file://$ORIGIN2"; git_q push -u origin trunk
git_q -C "$ORIGIN2" symbolic-ref HEAD refs/heads/trunk
git_q checkout -b feat2
echo b > b.txt; git_q add .; git_q commit -m b; git_q push -u origin feat2
cd - >/dev/null
SRC3="$TMP/src3"; WORK3="$TMP/work3"
git_q clone --depth 1 --branch=feat2 "file://$ORIGIN2" "$SRC3"
git_q -C "$SRC3" worktree add -b ralph-z "$WORK3" feat2
o3="$(printf '{"work_dir":"%s","base_branch":"feat2"}' "$WORK3" | sh "$SCRIPT" 2>/dev/null)"
check "edge: non-main default branch resolves" "$(printf '%s' "$o3" | jq -r .default_branch)" "trunk"
check "edge: non-main scope is correct"        "$(printf '%s' "$o3" | jq -r .file_count)"     "1"

echo
if [ "$fails" -eq 0 ]; then echo "all derive-scope tests passed"; else
  echo "$fails derive-scope test(s) FAILED"; exit 1; fi
