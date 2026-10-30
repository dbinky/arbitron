#!/bin/sh
# Commits whatever a coding agent left uncommitted in work_dir, so the tamper
# gate, the plan counter, the checkpoint push and the stage manifest all describe
# the same committed HEAD.
#
#   commit-leftovers.sh [--message <subject>]
#
# Reads the variable snapshot as JSON on stdin (work_dir) and emits
# {committed, files, head}. A clean tree commits nothing. Otherwise every
# modified, staged and untracked (not ignored) path is committed on top of HEAD,
# which lies after the pass base, so the tamper gate judges those edits like the
# agent's own commits. The commit carries [skip ci] and skips hooks, as the
# tamper verdict's commits do. FAILS CLOSED: exit 1 when git status fails (a
# corrupt index, say), the commit fails or the tree is still not clean
# afterwards, so a pass whose work could not be recorded never counts.
set -eu

MSG="chore(impl): commit uncommitted agent output [skip ci]"
while [ $# -gt 0 ]; do
  case "$1" in
    --message)
      [ $# -ge 2 ] || { echo "commit-leftovers: --message needs a value" >&2; exit 1; }
      MSG="$2 [skip ci]"; shift ;;
    *) echo "commit-leftovers: unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
[ -n "$WORK" ] || { echo "commit-leftovers: work_dir not set" >&2; exit 1; }
git -C "$WORK" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
  || { echo "commit-leftovers: not a git worktree: $WORK" >&2; exit 1; }

g() { git -C "$WORK" -c core.quotepath=off "$@"; }
# The dirty paths as a JSON list. A git status that fails (a corrupt index, say)
# is an error, never an empty list: an unreadable tree must not pass as clean.
dirty() {
  st="$(g status --porcelain --no-renames --untracked-files=all)" \
    || { echo "commit-leftovers: git status failed in $WORK; cannot tell whether the tree is clean" >&2; return 1; }
  printf '%s\n' "$st" | cut -c4- | jq -Rsc 'split("\n") | map(select(. != ""))'
}

FILES="$(dirty)" || exit 1
COMMITTED=false
if [ "$FILES" != "[]" ]; then
  echo "commit-leftovers: committing uncommitted changes: $FILES" >&2
  IDENT=""
  if [ -z "$(g config user.email 2>/dev/null || true)" ]; then IDENT="-c user.name=arbitron -c user.email=arbitron@localhost"; fi
  g add -A >&2 || { echo "commit-leftovers: git add failed" >&2; exit 1; }
  # shellcheck disable=SC2086 # IDENT is two fixed -c options
  g $IDENT commit -q --no-verify -m "$MSG" >&2 || { echo "commit-leftovers: commit failed" >&2; exit 1; }
  COMMITTED=true
  LEFT="$(dirty)" || exit 1
  if [ "$LEFT" != "[]" ]; then
    echo "commit-leftovers: tree still not clean after committing: $LEFT" >&2
    exit 1
  fi
fi

HEAD_SHA="$(g rev-parse HEAD)"
jq -n --argjson c "$COMMITTED" --argjson f "$FILES" --arg h "$HEAD_SHA" '{committed: $c, files: $f, head: $h}'
