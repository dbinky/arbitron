#!/bin/sh
# Deterministic persona-completeness gate. Reads the variable snapshot as JSON on
# stdin. Counts how many PR comments each required reviewer persona posted (by its
# attribution prefix) and FAILS the run if any posted nothing — so the manager
# never synthesizes a verdict on a partial panel (the single biggest review miss:
# a reviewer silently dropping out). Auth via the engine's script.envpassthrough.
set -eu
IN="$(cat)"
REPO="$(printf '%s' "$IN" | jq -r '.repo')"
PR_NUMBER="$(printf '%s' "$IN" | jq -r '.pr_number')"

[ -n "$REPO" ] && [ "$REPO" != "null" ] || { echo "repo not set" >&2; exit 1; }
[ -n "$PR_NUMBER" ] && [ "$PR_NUMBER" != "null" ] || { echo "pr_number not set" >&2; exit 1; }
gh auth status 1>&2 || { echo "gh not authenticated" >&2; exit 1; }

# All inline (review) + general (issue) comment bodies on the PR.
INLINE="$(gh api "repos/$REPO/pulls/$PR_NUMBER/comments?per_page=100" --paginate --jq '.[].body' 2>/dev/null || true)"
GENERAL="$(gh api "repos/$REPO/issues/$PR_NUMBER/comments?per_page=100" --paginate --jq '.[].body' 2>/dev/null || true)"
ALL="$INLINE
$GENERAL"

# Required reviewers that MUST have posted before the manager synthesizes.
missing=""
for p in architect 10x security; do
  n="$(printf '%s\n' "$ALL" | grep -c "^$p:" || true)"
  echo "persona $p: $n comment(s)" >&2
  [ "$n" -gt 0 ] || missing="$missing $p"
done

if [ -n "$missing" ]; then
  echo "PERSONA DROPOUT — required reviewer(s) posted nothing:$missing. Aborting before the manager renders a verdict on a partial panel (re-run after the dropped persona's tooling/model is fixed)." >&2
  exit 1
fi
jq -n '{personas_ok:true}'
