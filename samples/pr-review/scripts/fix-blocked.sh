#!/bin/sh
# Reached when the verify-fix gate decides the senior engineer's fixes are NOT
# safe to push. Posts a BLOCKED comment listing the blockers and does NOT push —
# the commits stay in the (unpushed) review worktree. Reads vars as JSON on stdin.
set -eu
IN="$(cat)"
REPO="$(printf '%s' "$IN" | jq -r '.repo')"
PR_NUMBER="$(printf '%s' "$IN" | jq -r '.pr_number')"
BLOCKERS="$(printf '%s' "$IN" | jq -r '(.["verify-fix"].blockers // []) | if length==0 then "- (no specifics reported)" else map("- " + .) | join("\n") end')"

[ -n "$REPO" ] && [ "$REPO" != "null" ] || { echo "repo not set" >&2; exit 1; }
[ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ] || { echo "pr_number not set" >&2; exit 1; }
gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

gh pr comment "$PR_NUMBER" --repo "$REPO" --body "senior: BLOCKED — auto-fixes were NOT pushed. The verification gate found:

$BLOCKERS

The commits remain in the review worktree (unpushed). Resolve and re-run, or apply the fixes manually." 1>&2

jq -n '{fix_pushed:false}'
