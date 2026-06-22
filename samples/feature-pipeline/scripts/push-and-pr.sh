#!/bin/sh
# Pushes the feature branch and opens a PR. Reads vars from stdin JSON; emits
# {pr_url, pr_number, branch} as JSON. Fails loudly if gh is unauthenticated.
# Auth (HOME/PATH/GH_TOKEN) is provided via the engine's script.envpassthrough.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch')"
# Branch-point test baseline summary (recorded before implementation), surfaced
# in the PR body so a reviewer knows which failures pre-date this work. Absent on
# older runs -> empty, and the note is omitted.
BASELINE_SUMMARY="$(printf '%s' "$IN" | jq -r '.baseline_summary // empty')"
BASELINE_NOTE=""
[ -n "$BASELINE_SUMMARY" ] && BASELINE_NOTE="

Test baseline: $BASELINE_SUMMARY. The refinement loop gates on introducing no new test failures vs this branch-point baseline (pre-existing failures excluded)."

gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }
git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2
PR_URL="$(cd "$WORK" && gh pr create --fill --head "$BRANCH" --title "feat: $SLUG" \
  --body "Automated feature pipeline for $SLUG. Spec/designs/plans under docs/. Ready for multi-persona PR review.$BASELINE_NOTE")"
PR_NUMBER="$(printf '%s' "$PR_URL" | sed 's:.*/::')"
jq -n --arg u "$PR_URL" --arg n "$PR_NUMBER" --arg b "$BRANCH" '{pr_url:$u, pr_number:$n, branch:$b}'
