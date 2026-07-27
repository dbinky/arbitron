#!/bin/sh
# Tests for archive-ralph.sh — preserves the previous run's tracking docs.
#
# Covers all five scenario categories:
#   happy-path : all three files present -> archived as set 1
#   success    : content is preserved verbatim; originals are left in place
#   failure    : nothing to archive is a no-op, not an error
#   error      : missing work_dir exits non-zero
#   edge-case  : repeat runs increment; a PARTIAL set must not reuse a number
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/archive-ralph.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }

W="$TMP/work"; mkdir -p "$W/docs/reference"
run() { printf '{"work_dir":"%s"}' "$W" | sh "$SCRIPT" 2>/dev/null; }
HIST="$W/docs/reference/historical"
DATE="$(date +%Y-%m-%d)"

seed_all() {
  printf 'charter v%s\n' "$1" > "$W/RALPH.md"
  printf 'focus v%s\n'   "$1" > "$W/docs/reference/focus-areas.md"
  printf 'gaps v%s\n'    "$1" > "$W/docs/reference/gaps-identified.md"
}

# ------------------------------------------------------------------- failure
# A first run has nothing to preserve. That is normal, not an error — exiting
# non-zero here would abort every fresh branch.
out="$(run)"
check "failure: nothing to archive is a no-op" "$(printf '%s' "$out" | jq -r .archived_count)" "0"
check "failure: no-op reports plan 0"          "$(printf '%s' "$out" | jq -r .plan_number)"    "0"

# ---------------------------------------------------------------- happy path
seed_all 1
out="$(run)"
check "happy: archives all three"    "$(printf '%s' "$out" | jq -r .archived_count)" "3"
check "happy: first set is plan 1"   "$(printf '%s' "$out" | jq -r .plan_number)"    "1"
check "happy: RALPH archived"        "$([ -f "$HIST/$DATE-historical-RALPH-1.md" ] && echo y)" "y"
check "happy: focus-areas archived"  "$([ -f "$HIST/$DATE-historical-focus-areas-1.md" ] && echo y)" "y"
check "happy: gaps archived"         "$([ -f "$HIST/$DATE-historical-gaps-identified-1.md" ] && echo y)" "y"

# ------------------------------------------------------------------- success
check "success: content preserved verbatim" \
  "$(cat "$HIST/$DATE-historical-focus-areas-1.md")" "focus v1"
# Archiving must COPY, not move: the seeding step overwrites these next, and a
# move would leave the loop with no current plan if seeding then failed.
check "success: original left in place" \
  "$([ -f "$W/docs/reference/focus-areas.md" ] && echo y)" "y"

# ----------------------------------------------------------------- edge case
# Second run the same day must not clobber set 1.
seed_all 2
out="$(run)"
check "edge: repeat run increments to 2" "$(printf '%s' "$out" | jq -r .plan_number)" "2"
check "edge: set 1 still intact"         "$(cat "$HIST/$DATE-historical-focus-areas-1.md")" "focus v1"
check "edge: set 2 has new content"      "$(cat "$HIST/$DATE-historical-focus-areas-2.md")" "focus v2"

# A PARTIAL set is the case that breaks count-based numbering: 5 files present
# would compute 5/3+1 = 2 and overwrite set 2. Numbering is derived from the
# highest existing suffix instead, so this must land on 3.
rm -f "$W/docs/reference/focus-areas.md" "$W/docs/reference/gaps-identified.md"
printf 'charter v3\n' > "$W/RALPH.md"
out="$(run)"
check "edge: partial set does not reuse a number" "$(printf '%s' "$out" | jq -r .plan_number)" "3"
check "edge: partial set archives only what exists" "$(printf '%s' "$out" | jq -r .archived_count)" "1"
check "edge: set 2 survived the partial run" "$(cat "$HIST/$DATE-historical-focus-areas-2.md")" "focus v2"

# --------------------------------------------------------------------- error
if printf '{}' | sh "$SCRIPT" >/dev/null 2>&1; then
  check "error: missing work_dir exits non-zero" "exited 0" "non-zero"
else
  check "error: missing work_dir exits non-zero" "non-zero" "non-zero"
fi

echo
if [ "$fails" -eq 0 ]; then echo "all archive-ralph tests passed"; else
  echo "$fails archive-ralph test(s) FAILED"; exit 1; fi
