#!/bin/sh
# Reads the variable snapshot as JSON on stdin. Commits any fixes the senior
# engineer left uncommitted in the isolated worktree and pushes to the PR branch
# on the remote. Emits {pushed_head, branch} as JSON. Auth via the engine's
# script.envpassthrough. Runs only on the implement path (after senior-engineer);
# the review-only path (fix_scope=none) never reaches this node. This is the
# verified-clean path: verify-fix passed, so we also post a brief clean comment.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
REPO="$(printf '%s' "$IN" | jq -r '.repo')"
PR_BRANCH="$(printf '%s' "$IN" | jq -r '.pr_branch')"
PR_NUMBER="$(printf '%s' "$IN" | jq -r '.pr_number')"
FIX_SCOPE="$(printf '%s' "$IN" | jq -r '.fix_scope // "must-should"')"

[ -n "$WORK" ] && [ "$WORK" != "null" ] || { echo "work_dir not set" >&2; exit 1; }
[ -n "$PR_BRANCH" ] && [ "$PR_BRANCH" != "null" ] || { echo "pr_branch not set" >&2; exit 1; }

cd "$WORK"
gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# Commit anything the senior engineer left uncommitted. (It is told to commit its
# own work; this is a safety net so no fix is lost.)
git add -A
if ! git diff --cached --quiet; then
  git -c user.name="arbitron pr-review" \
      -c user.email="arbitron-pr-review@users.noreply.github.com" \
      commit -m "fix: apply $FIX_SCOPE review fixes for PR #$PR_NUMBER" 1>&2
fi

# Push to the PR branch on the remote. No-op ("Everything up-to-date") if there
# was nothing to fix. Fails LOUDLY (e.g. a fork PR without push rights) rather
# than swallowing the error.
git push origin "HEAD:$PR_BRANCH" 1>&2

HEAD_SHA="$(git rev-parse HEAD)"

# Verified-clean trace on the PR (best-effort; a comment failure must not fail the
# node now that the push has already succeeded).
if [ -n "$REPO" ] && [ "$REPO" != "null" ] && [ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ]; then
  gh pr comment "$PR_NUMBER" --repo "$REPO" --body "senior: $FIX_SCOPE fixes applied and verified clean (build/tests + wired-and-fed); pushed to \`$PR_BRANCH\`." 1>&2 || true
fi

jq -n --arg sha "$HEAD_SHA" --arg br "$PR_BRANCH" '{pushed_head:$sha, branch:$br}'
