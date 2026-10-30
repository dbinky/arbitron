#!/bin/sh
# Clears the document set a stage owns before the stage regenerates it, so a
# same-day rerun that produces fewer files cannot leave the first run's extras
# beside the new ones (list-plans and the design globs match by date, not by
# generation).
#
#   clear-owned-docs.sh --message <msg> <glob> [<glob> ...]
#
# Reads the variable snapshot as JSON on stdin (work_dir). Every glob is
# repo-relative and must have exactly the shape of a stage's own output:
#   docs/plans/<slug>/<YYYY-MM-DD>-phase-*-task-*.md
#   docs/designs/<YYYY-MM-DD>-<slug>-design-phase-*.md
# Any other pattern exits 1, so this can never be pointed at anything else. The
# matching tracked files are `git rm`ed and committed (only those paths; history
# keeps the earlier set), matching untracked files are deleted, and symlinks and
# directories are never touched. Emits {removed: [...], committed: bool}. Never
# pushes. A rerun that is interrupted after this step is invalidated by the
# manifest's freshness rule, so nothing treats the cleared set as complete.
set -eu

die() { echo "clear-owned-docs: $*" >&2; exit 1; }

MSG=""
PATTERNS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --message)
      [ $# -ge 2 ] || die "--message needs a value"
      MSG="$2"; shift ;;
    -*) die "unknown option: $1" ;;
    *) PATTERNS="$PATTERNS$1
" ;;
  esac
  shift
done
[ -n "$MSG" ] || die "--message is required"
[ -n "$PATTERNS" ] || die "at least one glob is required"

PLAN_RE='^docs/plans/[A-Za-z0-9][A-Za-z0-9._-]*/[0-9]{4}-[0-9]{2}-[0-9]{2}-phase-\*-task-\*\.md$'
DESIGN_RE='^docs/designs/[0-9]{4}-[0-9]{2}-[0-9]{2}-[A-Za-z0-9][A-Za-z0-9._-]*-design-phase-\*\.md$'
OLDIFS="$IFS"
IFS='
'
for p in $PATTERNS; do
  printf '%s\n' "$p" | grep -Eq "$PLAN_RE|$DESIGN_RE" || die "not a stage-owned output pattern: $p"
  case "$p" in *..*) die "pattern must not contain '..': $p" ;; esac
done
IFS="$OLDIFS"

WORK="$(cat | jq -r '.work_dir // ""')"
[ -n "$WORK" ] || die "work_dir not set"
cd "$WORK" 2>/dev/null || die "cannot enter $WORK"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not a git worktree: $WORK"

REMOVED=""
TRACKED=""
IFS='
'
for p in $PATTERNS; do
  IFS="$OLDIFS"
  # shellcheck disable=SC2086 # the glob is the point
  set -- $p
  for f in "$@"; do
    [ -f "$f" ] && [ ! -L "$f" ] || continue
    REMOVED="$REMOVED$f
"
    if git ls-files --error-unmatch -- "$f" >/dev/null 2>&1; then
      git rm -q -f -- "$f" 1>&2 || die "git rm failed: $f"
      TRACKED="$TRACKED$f
"
    else
      rm -f -- "$f"
    fi
  done
  IFS='
'
done
IFS="$OLDIFS"

COMMITTED=false
if [ -n "$TRACKED" ]; then
  printf '%s' "$TRACKED" | git commit -q -m "$MSG" --pathspec-from-file=- 1>&2 || die "commit failed"
  COMMITTED=true
fi

printf '%s' "$REMOVED" | sed '/^$/d' | sort -V | jq -R . | jq -sc --argjson c "$COMMITTED" '{removed:., committed:$c}'
