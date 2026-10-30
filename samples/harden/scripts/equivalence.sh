#!/bin/sh
# Equivalent-mutant claims for a harden round.
#
#   equivalence.sh --count    emits {claim_count}
#   equivalence.sh --record   emits {approved, rejected}
#
# Reads the variable snapshot as JSON on stdin (workspace). The strengthen agent
# may write {{workspace}}/harden/equivalence-claims.json ({claims:[{mutant_id,
# justification}]}); the cross-family judge writes
# {{workspace}}/harden/equivalence-verdicts.json ({rulings:[{mutant_id,
# equivalent, reason}]}). A missing claims file counts as 0 claims; a malformed
# one counts as 0 and logs a warning.
#
# --record approves a claim only when a ruling for that mutant_id says
# equivalent:true (the boolean) and the mutant is in state.json. Anything else,
# including an unruled claim or a malformed verdicts file, is rejected. Approved
# keys are merged into state.approved_equivalents; both files are then removed.
# --count deletes any verdicts file first, so strengthen cannot pre-write one.
set -eu

MODE="${1:-}"
case "$MODE" in
  --count|--record) ;;
  *) echo "usage: equivalence.sh --count | --record" >&2; exit 1 ;;
esac

IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
[ -n "$WS" ] || { echo "equivalence: workspace not set" >&2; exit 1; }
H="$WS/harden"
CLAIMS="$H/equivalence-claims.json"
VERDICTS="$H/equivalence-verdicts.json"
STATE="$H/state.json"

# claimed_ids: the unique claimed mutant ids as a JSON array ([] when missing or malformed).
claimed_ids() {
  [ -f "$CLAIMS" ] || { echo '[]'; return 0; }
  if ! jq -e '(.claims | type) == "array"' "$CLAIMS" >/dev/null 2>&1; then
    echo "equivalence: $CLAIMS is malformed; counting 0 claims" >&2
    echo '[]'
    return 0
  fi
  jq -c '[.claims[] | .mutant_id? | select(type == "string")] | unique' "$CLAIMS"
}

if [ "$MODE" = --count ]; then
  # Only the judge, which runs after this, may write rulings: a verdicts file
  # already here was written by strengthen (or left over) and is not trusted.
  rm -f "$VERDICTS"
  claimed_ids | jq '{claim_count: length}'
  exit 0
fi

[ -f "$STATE" ] || { echo "equivalence: no state.json at $STATE" >&2; exit 1; }
IDS="$(claimed_ids)"
TRUE_IDS='[]'
if [ -f "$VERDICTS" ]; then
  if jq -e '(.rulings | type) == "array"' "$VERDICTS" >/dev/null 2>&1; then
    TRUE_IDS="$(jq -c '[.rulings[] | select(.equivalent? == true) | .mutant_id? | select(type == "string")] | unique' "$VERDICTS")"
  else
    echo "equivalence: $VERDICTS is malformed; rejecting every claim" >&2
  fi
fi

APPROVED="$(jq -c --argjson ids "$IDS" --argjson yes "$TRUE_IDS" \
  '.mutants as $m | [$ids[] | select(IN($yes[])) | select($m[.] != null)]' "$STATE")"
jq --argjson a "$APPROVED" '.approved_equivalents = (((.approved_equivalents // []) + $a) | unique)' "$STATE" > "$STATE.tmp"
mv "$STATE.tmp" "$STATE"
rm -f "$CLAIMS" "$VERDICTS"

jq -n --argjson ids "$IDS" --argjson a "$APPROVED" '{approved: ($a | length), rejected: (($ids | length) - ($a | length))}'
