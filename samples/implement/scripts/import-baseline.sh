#!/bin/sh
# Imports the committed branch-point baseline instead of measuring one.
#
# Used when baseline-check found ./work/<baseline_rel> already in the repo.
# Validates the record-baseline shape (an object with a .suites array) and copies
# it to {{workspace}}/test-baseline.json, which record-baseline.sh then lifts.
# A committed baseline is reused and never re-measured.
#
# Reads {work_dir, workspace, baseline_rel} from stdin JSON. Emits
# {imported:true, path}. Exits 1 on any invalid input: an unusable committed
# baseline is an input error, not a flaky agent, so there is no empty fallback.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
REL="$(printf '%s' "$IN" | jq -r '.baseline_rel // empty')"
[ -n "$WS" ] || { echo "import-baseline: workspace not set" >&2; exit 1; }
[ -n "$WORK" ] || { echo "import-baseline: work_dir not set" >&2; exit 1; }
[ -n "$REL" ] || { echo "import-baseline: baseline_rel not set" >&2; exit 1; }
case "$REL" in
  /*|-*|..|../*|*/..|*/../*)
    echo "import-baseline: baseline_rel must be repo-relative without '..': $REL" >&2
    exit 1 ;;
esac

SRC="$WORK/$REL"
[ -f "$SRC" ] || { echo "import-baseline: baseline file not found: $SRC" >&2; exit 1; }
if ! jq -e 'type=="object" and (.suites|type=="array")' "$SRC" >/dev/null 2>&1; then
  echo "import-baseline: $REL is not a baseline (need an object with a .suites array)" >&2
  exit 1
fi

DEST="$WS/test-baseline.json"
cp "$SRC" "$DEST"
jq -n --arg p "$DEST" '{imported:true, path:$p}'
