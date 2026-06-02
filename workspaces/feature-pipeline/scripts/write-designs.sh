#!/bin/sh
# Writes each brainstormed design phase to docs/designs and commits. Reads vars
# from stdin JSON; emits {design_count, design_glob} as JSON.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir')"
RUN_DATE="$(printf '%s' "$IN" | jq -r '.run_date')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug')"
mkdir -p "$WORK/docs/designs"
N="$(printf '%s' "$IN" | jq '.["brainstorm-design"].phases | length')"
[ "$N" -ge 1 ] || { echo "no design phases" >&2; exit 1; }
i=0
while [ "$i" -lt "$N" ]; do
  PHASE_NO=$((i + 1))
  printf '%s' "$IN" | jq -r ".[\"brainstorm-design\"].phases[$i].markdown" \
    > "$WORK/docs/designs/$RUN_DATE-$SLUG-design-phase-$PHASE_NO.md"
  i=$PHASE_NO
done
git -C "$WORK" add docs/designs/ 1>&2
git -C "$WORK" commit --quiet -m "docs: add $SLUG phased designs ($N phases)" 1>&2
jq -n --argjson n "$N" --arg g "docs/designs/$RUN_DATE-$SLUG-design-phase-*.md" \
  '{design_count:$n, design_glob:$g}'
