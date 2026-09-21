#!/bin/sh
# Tests for ralph-loop's PER-ROLE verify-ralph-seed.sh.
#
# The loop now has three roles in one RALPH.md, and the failure mode this guards
# is specific: a file can carry a perfect protocol for one role and silently omit
# the self-assessment or stop rule for another. The missing one then surfaces
# sixty passes later as a timeout, which is exactly how a run was lost before.
set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/verify-ralph-seed.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1 (want $3, got $2)"; fails=$((fails+1)); fi; }

# The protocol the seeder actually tells the agent to transcribe.
awk '/---8<--- copy from here/{f=1;next} /---8<--- copy to here/{f=0} f' \
  "$SCRIPT_DIR/../templates/seed-ralph.md" > "$TMP/PROTO"
mkdir -p "$TMP/docs/reference"
plan() { { echo "## S"; echo "| # | a | Risk | R1 | R2 |"; echo "|---|---|---|---|---|"
           i=1; while [ $i -le $1 ]; do echo "| $i | a | high | [ ] | [ ] |"; i=$((i+1)); done
         } > "$TMP/docs/reference/focus-areas.md"
         { echo "## P"; echo "| # | p | s | Risk | S |"; echo "|---|---|---|---|---|"
           i=1; while [ $i -le $2 ]; do echo "| $i | p | s | high | [ ] |"; i=$((i+1)); done
         } > "$TMP/docs/reference/paired-reviews.md"; }
run() { printf '{"work_dir":"%s","max_ralph_iterations":%s}' "$TMP" "$1" | sh "$SCRIPT" 2>/dev/null; }

cp "$TMP/PROTO" "$TMP/RALPH.md"; plan 28 86
OUT="$(run 500)" && rc=0 || rc=1
check "the seeder's own protocol passes its verifier" "$rc" "0"
check "all three roles are recognised" "$(printf '%s' "$OUT" | jq -r '.roles|length')" "18"
check "paired rows are charged at a third of a pass" "$(printf '%s' "$OUT" | jq -r '.implied_passes')" "85"

sed '/Stop after the tag/d' "$TMP/PROTO" > "$TMP/RALPH.md"
run 500 >/dev/null 2>&1 && { echo "FAIL - a role missing its stop rule was accepted"; fails=$((fails+1)); } \
  || echo "ok   - a role missing its stop rule is rejected"

awk '/^## Role: single-review-2/{skip=1} /^## Role: paired-review/{skip=0} !skip' "$TMP/PROTO" > "$TMP/RALPH.md"
run 500 >/dev/null 2>&1 && { echo "FAIL - an absent role block was accepted"; fails=$((fails+1)); } \
  || echo "ok   - an entirely absent role block is rejected"

cp "$TMP/PROTO" "$TMP/RALPH.md"; plan 60 200
run 500 >/dev/null 2>&1 && { echo "FAIL - an unfinishable plan was accepted"; fails=$((fails+1)); } \
  || echo "ok   - a plan that cannot finish in its budget is rejected"

[ "$fails" -eq 0 ] || { echo "$fails check(s) failed"; exit 1; }
echo "all checks passed"
