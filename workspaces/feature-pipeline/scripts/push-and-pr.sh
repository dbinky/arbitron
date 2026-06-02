#!/bin/sh
# Pushes the feature branch and opens a PR. Reads vars from stdin JSON; emits
# {pr_url, pr_number, branch} as JSON. Fails loudly if gh is unauthenticated.
# Auth (HOME/PATH/GH_TOKEN) is provided via the engine's script.envpassthrough.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch')"

gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }
git -C "$WORK" push --quiet -u origin "$BRANCH" 1>&2
PR_URL="$(cd "$WORK" && gh pr create --fill --head "$BRANCH" --title "feat: $SLUG" \
  --body "Automated feature pipeline for $SLUG. Spec/designs/plans under docs/. Ready for multi-persona PR review.")"
PR_NUMBER="$(printf '%s' "$PR_URL" | sed 's:.*/::')"
jq -n --arg u "$PR_URL" --arg n "$PR_NUMBER" --arg b "$BRANCH" '{pr_url:$u, pr_number:$n, branch:$b}'
