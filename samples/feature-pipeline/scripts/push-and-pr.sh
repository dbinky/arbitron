#!/bin/sh
# Pushes the feature branch and opens a PR. Reads vars from stdin JSON; emits
# {pr_url, pr_number, branch} as JSON. Fails loudly if gh is unauthenticated.
# Auth (HOME/PATH/GH_TOKEN) is provided via the engine's script.envpassthrough.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch')"
# Base branch the feature was cut from (e.g. "dev"), so the PR targets it rather
# than the repo's default branch. Resolve from base_branch, falling back to the
# init-workspace base_ref. The worktree is a SHALLOW clone of only this base ref,
# so we must NOT use `gh pr create --fill` (it derives defaults from
# origin/<default-branch>...HEAD, and origin/main is absent here) — title and body
# are supplied explicitly below instead.
BASE="$(printf '%s' "$IN" | jq -r '((.base_branch | select(. != "")) // (.["init-workspace"].base_ref) // "") | select(. != null)')"
# Branch-point test baseline summary (recorded before implementation), surfaced
# in the PR body so a reviewer knows which failures pre-date this work. Absent on
# older runs -> empty, and the note is omitted.
BASELINE_SUMMARY="$(printf '%s' "$IN" | jq -r '.baseline_summary // empty')"
BASELINE_NOTE=""
[ -n "$BASELINE_SUMMARY" ] && BASELINE_NOTE="

Test baseline: $BASELINE_SUMMARY. The refinement loop gates on introducing no new test failures vs this branch-point baseline (pre-existing failures excluded)."

# A blocked implementation (impl_blocked, set when every remaining plan task was
# blocked) still reaches here so its finished work is reviewable, but the PR must
# not read as the whole feature: run 5e86d796 opened a ready-for-review
# "feat: <slug>" PR with 14 of 17 tasks blocked and no mention of it. Blocked runs
# open a DRAFT whose title and body say so.
TITLE="feat: $SLUG"
DRAFT_ARG=""
BLOCKED_NOTE=""
if [ "$(printf '%s' "$IN" | jq -r '.impl_blocked // false')" = "true" ]; then
  TITLE="[BLOCKED] feat: $SLUG"
  DRAFT_ARG="--draft"
  REMAINING="$(printf '%s' "$IN" | jq -r '.["impl-assess"].tasks_remaining // "unknown"')"
  WHY="$(printf '%s' "$IN" | jq -r '.["impl-assess"].notes // "No assessor notes were recorded."')"
  BLOCKED_NOTE="

IMPLEMENTATION BLOCKED: $REMAINING plan task(s) remain unimplemented. This PR contains only the work that could be done; it is not the complete feature.
Why: $WHY
Each blocked plan file under docs/plans/ carries a BLOCKED note naming what it is waiting on."
fi

gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# CI runs once, on the finished branch. Every in-loop commit carried [skip ci]
# via the commit-msg hook init-workspace.sh installed, so nothing pushed during
# the loop has been checked by CI (which is the point: one review triggered over
# 1,000 workflow runs on intermediate commits). This empty commit is made with
# --no-verify so it lacks the marker; GitHub runs the workflows on it when it
# becomes the PR head. --allow-empty because the finished state is already
# committed: this commit exists only to be the head that CI evaluates. Harmless
# on a worktree with no hook.
git -C "$WORK" commit --quiet --allow-empty --no-verify \
  -m "chore(ralph): review loop finished, run CI on the finished branch" 1>&2
git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2
# --base targets the feature's actual base branch when known; omitted (gh uses the
# repo default) only if neither base_branch nor base_ref was provided. No --fill:
# title/body are explicit, and --fill would fail on the shallow clone (see above).
BASE_ARG=""
[ -n "$BASE" ] && BASE_ARG="--base $BASE"
PR_URL="$(cd "$WORK" && gh pr create $BASE_ARG $DRAFT_ARG --head "$BRANCH" --title "$TITLE" \
  --body "Automated feature pipeline for $SLUG. Spec/designs/plans under docs/. Ready for multi-persona PR review.$BLOCKED_NOTE$BASELINE_NOTE")"
PR_NUMBER="$(printf '%s' "$PR_URL" | sed 's:.*/::')"
jq -n --arg u "$PR_URL" --arg n "$PR_NUMBER" --arg b "$BRANCH" '{pr_url:$u, pr_number:$n, branch:$b}'
