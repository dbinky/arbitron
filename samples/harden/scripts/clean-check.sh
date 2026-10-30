#!/bin/sh
# Checks that the strengthen agent left work_dir clean: every change committed.
#
#   clean-check.sh [--discard]
#
# Reads the variable snapshot as JSON on stdin (work_dir) and emits {dirty,
# files, discarded}. files lists the uncommitted paths (modified, staged or
# untracked; ignored files do not count). The mutation adapters refuse a dirty
# tree, so with --discard a dirty tree is restored to HEAD (`git reset --hard`
# and `git clean -fd`, never -x) and the caller voids the round. Commits are
# never touched. Exit 1 means bad inputs or a tree still dirty after discarding.
#
# It also removes <workspace>/tamper/findings.json. tamper-gate writes that file
# fresh every round it runs, but a void that skips the gate (void-dirty,
# void-state) would otherwise turn the previous round's findings into this
# round's must-fix.md.
set -eu

DISCARD=0
while [ $# -gt 0 ]; do
  case "$1" in
    --discard) DISCARD=1 ;;
    *) echo "clean-check: unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
[ -n "$WORK" ] || { echo "clean-check: work_dir not set" >&2; exit 1; }
git -C "$WORK" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
  || { echo "clean-check: not a git worktree: $WORK" >&2; exit 1; }

WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
[ -z "$WS" ] || rm -f "$WS/tamper/findings.json"

FILES="$(git -C "$WORK" -c core.quotepath=off status --porcelain --no-renames --untracked-files=all \
  | cut -c4- | jq -Rsc 'split("\n") | map(select(. != ""))')"

DIRTY=false
DISCARDED=false
if [ "$FILES" != "[]" ]; then
  DIRTY=true
  echo "clean-check: uncommitted changes: $FILES" >&2
  if [ "$DISCARD" = 1 ]; then
    git -C "$WORK" reset -q --hard HEAD >&2
    git -C "$WORK" clean -fdq >&2
    [ -z "$(git -C "$WORK" status --porcelain)" ] \
      || { echo "clean-check: tree still dirty after discarding" >&2; exit 1; }
    DISCARDED=true
  fi
fi

jq -n --argjson d "$DIRTY" --argjson f "$FILES" --argjson x "$DISCARDED" '{dirty: $d, files: $f, discarded: $x}'
