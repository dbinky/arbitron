#!/bin/sh
# Reads the variable snapshot as JSON on stdin (workspace, work_dir, run_date).
# The jam leaves {slug} in <workspace>/.arbitron/ideate-result.json (outside the
# worktree) and the doc at docs/ideas/<slug>.md. Checks both, moves the detached
# worktree onto dev-<slug>-<run_date>-<short_id>, and commits the doc if it is
# uncommitted. Emits {slug, branch, idea_path}. Never pushes: the finish node's
# manifest-append pushes the new branch.
set -eu

IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
RUN_DATE="$(printf '%s' "$IN" | jq -r '.run_date // ""')"
[ -n "$WS" ] || { echo "finalize-ideate: workspace not set" >&2; exit 1; }
[ -n "$WORK" ] || { echo "finalize-ideate: work_dir not set" >&2; exit 1; }
[ -n "$RUN_DATE" ] || { echo "finalize-ideate: run_date not set" >&2; exit 1; }

RESULT="$WS/.arbitron/ideate-result.json"
[ -f "$RESULT" ] || { echo "finalize-ideate: no idea doc written ($RESULT is absent)" >&2; exit 1; }
SLUG="$(jq -r '.slug | if type == "string" then . else "" end' "$RESULT" 2>/dev/null)" \
  || { echo "finalize-ideate: $RESULT is not valid JSON" >&2; exit 1; }
[ -n "$SLUG" ] || { echo "finalize-ideate: $RESULT has no string slug" >&2; exit 1; }

# The slug becomes a branch name and a path, so it must be plain kebab-case.
# jq anchors on the whole value; grep would pass any one matching line.
if ! jq -e '.slug | test("\\A[a-z0-9]+(-[a-z0-9]+)*\\z")' "$RESULT" >/dev/null; then
  echo "finalize-ideate: invalid slug '$SLUG' (want lowercase kebab-case)" >&2
  exit 1
fi
[ "${#SLUG}" -le 48 ] || { echo "finalize-ideate: slug '$SLUG' is longer than 48 characters" >&2; exit 1; }

IDEA="docs/ideas/$SLUG.md"
cd "$WORK" || { echo "finalize-ideate: cannot enter $WORK" >&2; exit 1; }
[ ! -L "$IDEA" ] || { echo "finalize-ideate: $IDEA is a symlink" >&2; exit 1; }
[ -f "$IDEA" ] || { echo "finalize-ideate: no idea doc written at $IDEA" >&2; exit 1; }
[ -s "$IDEA" ] || { echo "finalize-ideate: empty idea doc at $IDEA" >&2; exit 1; }

# The worktree was cut detached at the base. Name the branch the way
# init-workspace --new-branch does, so a later stage's checkout finds it.
SHORT_ID="$(basename "$WS" | cut -c1-8)"
BRANCH="dev-$SLUG-$RUN_DATE-$SHORT_ID"
git switch -q -C "$BRANCH" 1>&2

git add -- "$IDEA" 1>&2
if ! git diff --cached --quiet -- "$IDEA"; then
  git commit -q -m "docs: add $SLUG idea doc" -- "$IDEA" 1>&2
fi

jq -n --arg s "$SLUG" --arg b "$BRANCH" --arg p "$IDEA" '{slug:$s, branch:$b, idea_path:$p}'
