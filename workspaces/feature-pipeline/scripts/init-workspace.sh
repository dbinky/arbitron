#!/bin/sh
# Reads the variable snapshot as JSON on stdin; clones repo_url into
# {{workspace}}/src, derives slug + run_date, creates the
# dev-<slug>-<run_date>-<instance-short-id> worktree at {{workspace}}/work, and
# emits {slug, run_date, work_dir, src_dir, branch} as JSON.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
REPO_URL="$(printf '%s' "$IN" | jq -r '.repo_url')"
SLUG_IN="$(printf '%s' "$IN" | jq -r '.slug // ""')"

[ -n "$WS" ] && [ "$WS" != "null" ] || { echo "workspace not set" >&2; exit 1; }
[ -n "$REPO_URL" ] && [ "$REPO_URL" != "null" ] || { echo "repo_url not set" >&2; exit 1; }

SRC="$WS/src"
WORK="$WS/work"
RUN_DATE="$(date +%Y-%m-%d)"

# The per-instance workspace dir is .../instances/<instance-id>, so its basename
# is the arbitron instance id (engine invariant: filesystem.instanceDir). A short
# prefix is appended to the branch name so two instances building the same repo
# and feature on the same day never collide on the shared remote — each clone is
# already isolated on disk by instance id; this extends that isolation to the
# pushed branch / PR.
INSTANCE_ID="$(basename "$WS")"
SHORT_ID="$(printf '%s' "$INSTANCE_ID" | cut -c1-8)"

# Slug is the feature identity. When the caller supplies none we use a neutral
# placeholder rather than the repo name: the repo name is not a feature, so
# naming the branch after it is both misleading and collision-prone (every
# feature in that repo would share it). The real per-feature slug is resolved
# after the spec brainstorm (see the repromote-slug node) and drives the
# docs/templates; the branch keeps this provisional, instance-unique name.
if [ -z "$SLUG_IN" ] || [ "$SLUG_IN" = "null" ]; then
  SLUG="feature"
else
  SLUG="$SLUG_IN"
fi

# Shallow clone: the pipeline only needs the tip of the default branch to create
# the feature worktree, commit, push, and open a PR — it never walks deep
# history. --depth 1 keeps each per-instance clone code-sized instead of hauling
# the full repo history. (git ignores --depth for plain local-path clones, hence
# real runs clone an https/ssh URL.)
git clone --depth 1 --quiet "$REPO_URL" "$SRC" 1>&2
DEFAULT_BRANCH="$(git -C "$SRC" symbolic-ref --short HEAD)"
BRANCH="dev-$SLUG-$RUN_DATE-$SHORT_ID"
git -C "$SRC" worktree add --quiet -b "$BRANCH" "$WORK" "$DEFAULT_BRANCH" 1>&2

jq -n --arg slug "$SLUG" --arg rd "$RUN_DATE" --arg work "$WORK" --arg src "$SRC" --arg br "$BRANCH" \
  '{slug:$slug, run_date:$rd, work_dir:$work, src_dir:$src, branch:$br}'
