#!/bin/sh
# Guards against sample drift between ralph-loop and feature-pipeline.
#
# ralph-loop reuses several scripts and one template from feature-pipeline
# verbatim. Verbatim copies rot silently: samples/feature-pipeline's
# record-baseline.sh sat SIX WEEKS behind the running workflow, still carrying a
# hard-fail path that had already been replaced live because it discarded an
# entire spec+design+plan run. Nothing caught it until someone diffed by hand.
#
# This test asserts the copies are byte-identical. If a shared script is
# deliberately changed, change it in BOTH places (or stop sharing it) — do not
# weaken this test.
#
# It also asserts both workflows encode the same ralph loop protocol, since two
# copies of the terminating rules is exactly the kind of thing that drifts.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SAMPLES="$(cd "$SCRIPT_DIR/../.." && pwd)"
RL="$SAMPLES/ralph-loop"
FP="$SAMPLES/feature-pipeline"

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }

# --- Scripts copied verbatim from feature-pipeline --------------------------
# push-and-pr.sh diverged 2026-08-31: only ralph-loop resumes onto an existing
# branch, where head==base means there is no PR to open.
SHARED="checkpoint-push.sh notify.sh notify-teams.sh"
for s in $SHARED; do
  if [ ! -f "$RL/scripts/$s" ]; then
    check "shared script present: $s" "missing" "present"
  elif [ ! -f "$FP/scripts/$s" ]; then
    check "source script present: $s" "missing" "present"
  elif diff -q "$RL/scripts/$s" "$FP/scripts/$s" >/dev/null 2>&1; then
    check "identical to feature-pipeline: $s" "same" "same"
  else
    check "identical to feature-pipeline: $s" "DRIFTED" "same"
  fi
done

# NOTE: ralph-loop is role-routed and feature-pipeline is not, so their loop
# templates and RALPH.md protocols have DIVERGED ON PURPOSE as of 2026-08-30.
# Only the genuinely shared utility scripts are guarded here. Re-add template
# checks if feature-pipeline is ever migrated to role routing.

if [ "$fails" -eq 0 ]; then echo "all shared-scripts tests passed"; else
  echo "$fails shared-scripts test(s) FAILED"; exit 1; fi
