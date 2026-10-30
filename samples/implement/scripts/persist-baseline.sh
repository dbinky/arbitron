#!/bin/sh
# Commits the measured branch-point baseline into the repo (R3).
#
# The baseline-tests agent writes {{workspace}}/test-baseline.json. This validates
# its shape, copies it to ./work/<baseline_rel> and commits it, so the baseline is
# in history before the first pass-mark and a later run imports it rather than
# re-measuring. A file already committed with identical content makes no commit.
#
# Reads {work_dir, workspace, baseline_rel, slug} from stdin JSON. Emits
# {persisted:true, path} where path is the repo-relative baseline path. Exits 1
# on a missing or malformed baseline or a failed commit: unlike record-baseline
# there is no empty fallback, since an empty baseline must never be committed.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
REL="$(printf '%s' "$IN" | jq -r '.baseline_rel // empty')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug // empty')"
[ -n "$WS" ] || { echo "persist-baseline: workspace not set" >&2; exit 1; }
[ -n "$WORK" ] || { echo "persist-baseline: work_dir not set" >&2; exit 1; }
[ -n "$REL" ] || { echo "persist-baseline: baseline_rel not set" >&2; exit 1; }
case "$REL" in
  /*|-*|..|../*|*/..|*/../*)
    echo "persist-baseline: baseline_rel must be repo-relative without '..': $REL" >&2
    exit 1 ;;
esac
if [ -z "$SLUG" ]; then
  SLUG="$(basename "$REL" .baseline.json)"
fi

SRC="$WS/test-baseline.json"
[ -f "$SRC" ] || { echo "persist-baseline: baseline not found: $SRC" >&2; exit 1; }
if ! jq -e 'type=="object" and (.suites|type=="array")' "$SRC" >/dev/null 2>&1; then
  echo "persist-baseline: $SRC is not a baseline (need an object with a .suites array)" >&2
  exit 1
fi

mkdir -p "$(dirname "$WORK/$REL")"
cp "$SRC" "$WORK/$REL"
if [ -n "$(git -C "$WORK" status --porcelain -- "$REL")" ]; then
  git -C "$WORK" add -- "$REL" 1>&2
  git -C "$WORK" commit --quiet -m "chore(pipeline): record $SLUG branch-point baseline" -- "$REL" 1>&2 || \
    { echo "persist-baseline: commit failed (git identity?)" >&2; exit 1; }
fi
jq -n --arg p "$REL" '{persisted:true, path:$p}'
