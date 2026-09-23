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
# pr_base: the branch the PR should target when it is not base_branch. A RESUMED
# run continues ON base_branch, so head and base are the same ref and no PR can
# be opened at the end (see the resume block below); the original run would have
# targeted the feature's real base. pr_base names that base so a resumed run can
# still open the PR. Empty keeps the previous behaviour.
PR_BASE="$(printf '%s' "$IN" | jq -r '.pr_base // ""')"
if [ -n "$PR_BASE" ] && [ "$PR_BASE" != "null" ]; then BASE="$PR_BASE"; fi
# Branch-point test baseline summary (recorded before implementation), surfaced
# in the PR body so a reviewer knows which failures pre-date this work. Absent on
# older runs -> empty, and the note is omitted.
BASELINE_SUMMARY="$(printf '%s' "$IN" | jq -r '.baseline_summary // empty')"
BASELINE_NOTE=""
[ -n "$BASELINE_SUMMARY" ] && BASELINE_NOTE="

Test baseline: $BASELINE_SUMMARY. The refinement loop gates on introducing no new test failures vs this branch-point baseline (pre-existing failures excluded)."

# Counted review-plan state at the moment the loop exited. A ralph PR asserts
# "reviewed", and the ONLY thing that makes that true is every approval box being
# ticked. Run a1aab074 exited on a stagnation backstop with 98 of its boxes
# unticked and opened a PR that said nothing about it; the shortfall was visible
# only to someone who went and read the instance trace. An early exit is a
# legitimate outcome, but it must be legible in the artifact it produces, so the
# body states the count either way. Absent on older runs -> the note is omitted.
UNTICKED="$(printf '%s' "$IN" | jq -r '.["count-review-plan"].unchecked_boxes // empty')"
PLAN_SUMMARY="$(printf '%s' "$IN" | jq -r '.["count-review-plan"].summary // empty')"
PLAN_NOTE=""
if [ -n "$UNTICKED" ] && [ "$UNTICKED" != "0" ]; then
  PLAN_NOTE="

> **INCOMPLETE REVIEW — $UNTICKED approval box(es) were still unticked when the loop exited.**
> $PLAN_SUMMARY
> Re-run ralph-loop against this branch to continue; a resumed run keeps every approval already earned."
elif [ -n "$PLAN_SUMMARY" ]; then
  PLAN_NOTE="

Review plan: $PLAN_SUMMARY"
fi

gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# CI runs once, on the finished branch. Every in-loop commit carried [skip ci]
# via the commit-msg hook init-workspace.sh installed, so nothing pushed during
# the loop has been checked by CI (which is the point: one review triggered over
# 1,000 workflow runs on intermediate commits). This empty commit is made with
# --no-verify so it lacks the marker; GitHub runs the workflows on it when it
# becomes the PR head, or the resumed branch's new tip. --allow-empty because the
# finished state is already committed: this commit exists only to be the head
# that CI evaluates. Harmless on a worktree with no hook.
git -C "$WORK" commit --quiet --allow-empty --no-verify \
  -m "chore(ralph): review loop finished, run CI on the finished branch" 1>&2
git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2

# RESUME: a resumed run continues ON the branch it was told to refine, so head
# and base are the SAME ref and there is nothing to open a PR against — GitHub
# rejects it with "No commits between X and X" and the run fails at the very end,
# after every approval was earned. The work is already pushed at this point, so
# the correct behaviour is to report and stop, not to fail. Whatever PR that
# branch already belongs to now carries the refinements.
if [ -n "$BASE" ] && [ "$BASE" = "$BRANCH" ]; then
  echo "push-and-pr: resumed run — head and base are both '$BRANCH', so no PR is opened." >&2
  echo "push-and-pr: all refinements are pushed to that branch; its existing PR (if any) now carries them." >&2
  RESUME_NOTE="Resumed run: refinements were pushed to the branch under review. No PR was opened because head and base are the same ref."
  [ -n "$UNTICKED" ] && [ "$UNTICKED" != "0" ] && RESUME_NOTE="$RESUME_NOTE INCOMPLETE REVIEW: $UNTICKED approval box(es) were still unticked when the loop exited. $PLAN_SUMMARY"
  jq -n --arg b "$BRANCH" --arg n "$RESUME_NOTE" \
    '{branch:$b, pushed:true, pr_url:"", pr_created:false, note:$n}'
  exit 0
fi
# --base targets the feature's actual base branch when known; omitted (gh uses the
# repo default) only if neither base_branch nor base_ref was provided. No --fill:
# title/body are explicit, and --fill would fail on the shallow clone (see above).
BASE_ARG=""
[ -n "$BASE" ] && BASE_ARG="--base $BASE"
PR_URL="$(cd "$WORK" && gh pr create $BASE_ARG --head "$BRANCH" --title "feat: $SLUG" \
  --body "Automated feature pipeline for $SLUG. Spec/designs/plans under docs/. Ready for multi-persona PR review.$PLAN_NOTE$BASELINE_NOTE")"
PR_NUMBER="$(printf '%s' "$PR_URL" | sed 's:.*/::')"
jq -n --arg u "$PR_URL" --arg n "$PR_NUMBER" --arg b "$BRANCH" '{pr_url:$u, pr_number:$n, branch:$b}'
