#!/bin/sh
# Reads the variable snapshot as JSON on stdin; clones repo_url into
# {{workspace}}/src, derives slug + run_date, creates the dev-<slug> worktree at
# {{workspace}}/work, and emits {slug, run_date, work_dir, src_dir, branch} as JSON.
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

if [ -z "$SLUG_IN" ] || [ "$SLUG_IN" = "null" ]; then
  SLUG="$(basename "$REPO_URL" .git | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | sed 's/^-//;s/-$//')"
else
  SLUG="$SLUG_IN"
fi

git clone --quiet "$REPO_URL" "$SRC" 1>&2
DEFAULT_BRANCH="$(git -C "$SRC" symbolic-ref --short HEAD)"
BRANCH="dev-$SLUG-$RUN_DATE"
git -C "$SRC" worktree add --quiet -b "$BRANCH" "$WORK" "$DEFAULT_BRANCH" 1>&2

jq -n --arg slug "$SLUG" --arg rd "$RUN_DATE" --arg work "$WORK" --arg src "$SRC" --arg br "$BRANCH" \
  '{slug:$slug, run_date:$rd, work_dir:$work, src_dir:$src, branch:$br}'
