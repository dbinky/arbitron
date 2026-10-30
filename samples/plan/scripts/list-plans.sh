#!/bin/sh
# Lists this run's plan files. Reads the variable snapshot as JSON on stdin
# (work_dir, plan_dir, run_date). Matches only
#   <work_dir>/<plan_dir>/<run_date>-phase-<N>-task-<M>.md
# so a rerun on a branch that still holds another day's plans never collects
# them. Emits {plan_paths: [...]}, paths relative to work_dir, sorted by phase
# then task numerically (task-10 after task-9). No match exits 1.
set -eu

die() { echo "list-plans: $*" >&2; exit 1; }

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
DIR="$(printf '%s' "$IN" | jq -r '.plan_dir // ""')"
DATE="$(printf '%s' "$IN" | jq -r '.run_date // ""')"
[ -n "$WORK" ] || die "work_dir not set"
[ -n "$DIR" ] || die "plan_dir not set"
printf '%s' "$DATE" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || die "run_date is not YYYY-MM-DD: $DATE"
cd "$WORK" 2>/dev/null || die "cannot enter $WORK"

# One "<phase> <task> <path>" line per match; sort keys are the two numbers.
FOUND=""
if [ -d "$DIR" ]; then
  for f in "$DIR/$DATE"-phase-*-task-*.md; do
    [ -f "$f" ] || continue
    nums="$(basename "$f" | sed -nE "s/^$DATE-phase-([0-9]+)-task-([0-9]+)\.md$/\1 \2/p")"
    [ -n "$nums" ] || continue
    FOUND="$FOUND$nums $f
"
  done
fi
[ -n "$FOUND" ] || die "no plan files match $DIR/$DATE-phase-<N>-task-<M>.md"

printf '%s' "$FOUND" | sort -n -k1,1 -k2,2 | cut -d' ' -f3- \
  | jq -R . | jq -sc '{plan_paths:.}'
