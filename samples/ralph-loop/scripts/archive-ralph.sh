#!/bin/sh
# Archives the previous ralph tracking documents before this run overwrites them.
#
# Layout (all paths relative to the worktree root):
#   RALPH.md                            <- charter, lives at the repo root
#   docs/reference/focus-areas.md       <- single-system review plan
#   docs/reference/paired-reviews.md    <- paired integration review plan
#   docs/reference/gaps-identified.md   <- known gaps
#   docs/reference/historical/          <- dated copies of all three
#
# WHY: these three files are per-run scratch state that lives on a branch. A
# second ralph run silently overwrites the first run's plan and its record of
# what was found — including gaps that were logged but never fixed. Copying them
# aside first makes each run's reasoning recoverable afterwards.
#
# Intra-day numbering: several runs a day are normal, so each backup set gets a
# sequence number. The number is derived from the highest suffix already present
# for today rather than from a file count, so a partial backup (only one or two
# of the three files existed) cannot cause a later run to collide with it.
#
# Reads {work_dir} from stdin JSON. Emits {archived, plan_number, archived_count}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || { echo "archive-ralph: work_dir not set" >&2; exit 1; }

DATE="$(date +%Y-%m-%d)"
HIST="$WORK/docs/reference/historical"

# label:relative-path — label is what appears in the archived filename.
SET="RALPH:RALPH.md focus-areas:docs/reference/focus-areas.md paired-reviews:docs/reference/paired-reviews.md gaps-identified:docs/reference/gaps-identified.md"

# Nothing to archive is a normal first-run state, not an error.
any=0
for pair in $SET; do
  rel="${pair#*:}"
  [ -f "$WORK/$rel" ] && any=1
done
if [ "$any" -eq 0 ]; then
  jq -n '{archived:[], plan_number:0, archived_count:0}'
  exit 0
fi

mkdir -p "$HIST"

# Highest existing sequence for today, e.g. 2026-07-27-historical-RALPH-3.md -> 3.
MAX="$(ls "$HIST/$DATE-historical-"*-*.md 2>/dev/null \
      | sed -E 's/.*-([0-9]+)\.md$/\1/' \
      | grep -E '^[0-9]+$' | sort -n | tail -1)"
N=$(( ${MAX:-0} + 1 ))

archived=""
for pair in $SET; do
  label="${pair%%:*}"
  rel="${pair#*:}"
  if [ -f "$WORK/$rel" ]; then
    dest="$HIST/$DATE-historical-$label-$N.md"
    cp "$WORK/$rel" "$dest"
    archived="$archived docs/reference/historical/$DATE-historical-$label-$N.md"
    echo "archive-ralph: $rel -> $DATE-historical-$label-$N.md" >&2
  fi
done

jq -n --arg a "${archived# }" --argjson n "$N" \
  '{archived:($a | split(" ") | map(select(length>0))),
    plan_number:$n,
    archived_count:($a | split(" ") | map(select(length>0)) | length)}'
