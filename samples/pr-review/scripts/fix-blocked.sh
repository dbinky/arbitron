#!/bin/sh
# Reached when the verify-fix gate flagged concerns. We STILL commit + push the
# senior engineer's work — the per-instance workspace is GC'd after the run, so
# NOT pushing means the fixes are lost forever (the failure mode that wasted a
# whole expensive senior-engineer pass). After pushing we post a comment noting
# the concerns so a human reviews before merge. Reads vars as JSON on stdin.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
REPO="$(printf '%s' "$IN" | jq -r '.repo')"
PR_BRANCH="$(printf '%s' "$IN" | jq -r '.pr_branch')"
PR_NUMBER="$(printf '%s' "$IN" | jq -r '.pr_number')"
FIX_SCOPE="$(printf '%s' "$IN" | jq -r '.fix_scope // "must-should"')"
BLOCKERS="$(printf '%s' "$IN" | jq -r '(.["record-verdict"].blockers // []) | if length==0 then "- (no specifics reported)" else map("- " + .) | join("\n") end')"

[ -n "$WORK" ] && [ "$WORK" != "null" ] || { echo "work_dir not set" >&2; exit 1; }
[ -n "$REPO" ] && [ "$REPO" != "null" ] || { echo "repo not set" >&2; exit 1; }
[ -n "$PR_BRANCH" ] && [ "$PR_BRANCH" != "null" ] || { echo "pr_branch not set" >&2; exit 1; }
[ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ] || { echo "pr_number not set" >&2; exit 1; }

cd "$WORK"
gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# Commit anything the senior engineer left uncommitted, then push. The push fails
# LOUDLY (e.g. a fork PR without push rights) rather than swallowing the error.
git add -A
if ! git diff --cached --quiet; then
  git -c user.name="arbitron pr-review" \
      -c user.email="arbitron-pr-review@users.noreply.github.com" \
      commit -m "fix: apply $FIX_SCOPE review fixes for PR #$PR_NUMBER (verification flagged concerns)" 1>&2
fi
git push origin "HEAD:$PR_BRANCH" 1>&2
HEAD_SHA="$(git rev-parse HEAD)"

gh pr comment "$PR_NUMBER" --repo "$REPO" --body "senior: fixes PUSHED, but the verification gate flagged concerns — please review before merge:

$BLOCKERS

The commits are on the PR branch (\`$PR_BRANCH\`); CI and a human reviewer should confirm before merge." 1>&2

jq -n --arg sha "$HEAD_SHA" --arg br "$PR_BRANCH" '{fix_pushed:true, verified:false, pushed_head:$sha, branch:$br}'
