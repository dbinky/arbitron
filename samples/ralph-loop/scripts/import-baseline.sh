#!/bin/sh
# Imports a committed test baseline instead of measuring one.
#
# Used when baseline_path names a file already in the repo (the init-workspace
# step confirmed it exists at the tip). Validates the record-baseline shape (an
# object with a .suites array) and copies it to {{workspace}}/test-baseline.json,
# which record-baseline.sh then lifts into variables and commits as usual.
#
# An imported baseline was measured at an unknown, earlier commit, so nothing in
# the workspace may stand in for "the commit the baseline measured". This writes
# {{workspace}}/baseline-imported and removes any baseline-head and
# import-verified: record-baseline.sh then records no baseline-head, and
# verify-tests.sh runs its first verification as a FULL sweep (ignoring
# last-pass-head too) before it writes import-verified.
#
# Reads {work_dir, workspace, baseline_path} from stdin JSON. Emits
# {imported:true, path}. Exits 1 on any invalid input; unlike record-baseline
# there is no empty fallback, because an explicitly named baseline that cannot be
# used is an input error, not a flaky agent.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
REL="$(printf '%s' "$IN" | jq -r '.baseline_path // empty')"
[ -n "$WS" ] || { echo "import-baseline: workspace not set" >&2; exit 1; }
[ -n "$WORK" ] || { echo "import-baseline: work_dir not set" >&2; exit 1; }
[ -n "$REL" ] || { echo "import-baseline: baseline_path not set" >&2; exit 1; }
case "$REL" in
  /*|-*|..|../*|*/..|*/../*)
    echo "import-baseline: baseline_path must be repo-relative without '..': $REL" >&2
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
rm -f "$WS/baseline-head" "$WS/import-verified"
printf '%s\n' "$REL" > "$WS/baseline-imported"
jq -n --arg p "$DEST" '{imported:true, path:$p}'
