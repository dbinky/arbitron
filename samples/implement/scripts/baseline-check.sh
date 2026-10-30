#!/bin/sh
# Reports whether the committed branch-point baseline already exists, so a rerun
# imports it instead of re-measuring (a re-measure on a partly-implemented branch
# would record the earlier run's own failures as "pre-existing").
#
# Reads {work_dir, baseline_rel} from stdin JSON. Emits {present}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
REL="$(printf '%s' "$IN" | jq -r '.baseline_rel // empty')"
[ -n "$WORK" ] || { echo "baseline-check: work_dir not set" >&2; exit 1; }
[ -n "$REL" ] || { echo "baseline-check: baseline_rel not set" >&2; exit 1; }
case "$REL" in
  /*|-*|..|../*|*/..|*/../*)
    echo "baseline-check: baseline_rel must be repo-relative without '..': $REL" >&2
    exit 1 ;;
esac
if [ -f "$WORK/$REL" ]; then
  jq -n '{present:true}'
else
  jq -n '{present:false}'
fi
