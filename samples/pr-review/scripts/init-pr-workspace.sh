#!/bin/sh
# Reads the variable snapshot as JSON on stdin. Shallow-clones {{repo}} FROM THE
# REMOTE into {{workspace}}/work and checks out PR #{{pr_number}} on its head
# branch, then emits {work_dir, pr_branch, pr_head, base} as JSON. No local repo
# under ~/ is ever used. gh auth (HOME/PATH/GH_TOKEN) comes from the engine's
# script.envpassthrough (the same mechanism feature-pipeline's push-and-pr.sh uses).
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
REPO="$(printf '%s' "$IN" | jq -r '.repo')"
PR_NUMBER="$(printf '%s' "$IN" | jq -r '.pr_number')"

[ -n "$WS" ] && [ "$WS" != "null" ] || { echo "workspace not set" >&2; exit 1; }
[ -n "$REPO" ] && [ "$REPO" != "null" ] || { echo "repo not set" >&2; exit 1; }
[ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ] || { echo "pr_number not set" >&2; exit 1; }

# SECURITY: repo and pr_number are workflow inputs that cross into gh/git argument
# contexts. Validate BEFORE use (mirrors feature-pipeline's init-workspace.sh):
# repo must be a plain owner/name (no leading dash, no extra path segments), and
# pr_number must be a positive integer. Reject anything else rather than passing
# it to gh/git.
case "$REPO" in
  -*) echo "repo must not begin with '-': $REPO" >&2; exit 1 ;;
esac
printf '%s' "$REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' \
  || { echo "repo is not a valid owner/name: $REPO" >&2; exit 1; }
printf '%s' "$PR_NUMBER" | grep -Eq '^[1-9][0-9]*$' \
  || { echo "pr_number is not a positive integer: $PR_NUMBER" >&2; exit 1; }

WORK="$WS/work"
gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# IDEMPOTENT across retries: the engine reuses this workspace dir when the node
# is retried, so a clone left by a failed earlier attempt must be cleared first —
# otherwise `gh repo clone` aborts with "destination path already exists and is
# not an empty directory".
rm -rf "$WORK"

# Shallow clone from the remote (never a local ~/ checkout). --depth 1 keeps the
# per-instance clone code-sized, matching feature-pipeline. gh forwards git flags
# placed after '--'.
gh repo clone "$REPO" "$WORK" -- --depth 1 1>&2
cd "$WORK"

# PR metadata (works for same-repo and fork PRs).
PR_BRANCH="$(gh pr view "$PR_NUMBER" --json headRefName -q .headRefName)"
PR_HEAD="$(gh pr view "$PR_NUMBER" --json headRefOid -q .headRefOid)"
BASE="$(gh pr view "$PR_NUMBER" --json baseRefName -q .baseRefName)"

# Check out the PR head. We do NOT use `gh pr checkout`: on a --depth 1
# single-branch clone it fails with "starting point 'origin/<branch>' is not a
# branch" because no remote-tracking ref for the PR branch was fetched. Instead
# fetch GitHub's refs/pull/<n>/head (exposed for BOTH same-repo and fork PRs)
# shallowly and create a local branch named after the PR head branch, so a later
# `git push origin HEAD:<pr_branch>` updates the PR (same-repo; forks fail loudly).
git fetch --depth 1 origin "pull/$PR_NUMBER/head" 1>&2
git checkout -B "$PR_BRANCH" FETCH_HEAD 1>&2

jq -n --arg work "$WORK" --arg br "$PR_BRANCH" --arg head "$PR_HEAD" --arg base "$BASE" \
  '{work_dir:$work, pr_branch:$br, pr_head:$head, base:$base}'
