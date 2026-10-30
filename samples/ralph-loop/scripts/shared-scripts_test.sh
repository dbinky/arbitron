#!/bin/sh
# Guards against sample drift between ralph-loop, legacy-feature-pipeline and _lib.
#
# ralph-loop reuses several scripts and one template from legacy-feature-pipeline
# verbatim. Verbatim copies rot silently: samples/legacy-feature-pipeline's
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
FP="$SAMPLES/legacy-feature-pipeline"

fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   - $1"; else
  echo "FAIL - $1"; echo "        expected: $3"; echo "        actual:   $2"; fails=$((fails+1)); fi; }

LIB="$SAMPLES/_lib"

# --- Scripts copied verbatim from legacy-feature-pipeline --------------------------
# push-and-pr.sh diverged 2026-08-31: only ralph-loop resumes onto an existing
# branch, where head==base means there is no PR to open.
SHARED="notify.sh"
for s in $SHARED; do
  if [ ! -f "$RL/scripts/$s" ]; then
    check "shared script present: $s" "missing" "present"
  elif [ ! -f "$FP/scripts/$s" ]; then
    check "source script present: $s" "missing" "present"
  elif diff -q "$RL/scripts/$s" "$FP/scripts/$s" >/dev/null 2>&1; then
    check "identical to legacy-feature-pipeline: $s" "same" "same"
  else
    check "identical to legacy-feature-pipeline: $s" "DRIFTED" "same"
  fi
done

# --- Scripts ralph-loop takes from the shared library (@lib) ----------------
# ralph-loop references these as @lib/<name>, so a local copy would be dead
# code that drifts. The _lib copies must stay byte-identical to the legacy
# legacy-feature-pipeline ones, which remain because legacy is out of scope.
LIBBED="checkpoint-push.sh notify-teams.sh"
for s in $LIBBED; do
  if [ ! -f "$LIB/$s" ]; then
    check "lib script present: $s" "missing" "present"
  elif [ ! -f "$FP/scripts/$s" ]; then
    check "legacy script present: $s" "missing" "present"
  elif diff -q "$LIB/$s" "$FP/scripts/$s" >/dev/null 2>&1; then
    check "_lib identical to legacy-feature-pipeline: $s" "same" "same"
  else
    check "_lib identical to legacy-feature-pipeline: $s" "DRIFTED" "same"
  fi
  if [ -e "$RL/scripts/$s" ]; then
    check "no ralph-loop copy of lib script: $s" "present" "absent"
  else
    check "no ralph-loop copy of lib script: $s" "absent" "absent"
  fi
  if grep -q "\"scripts/$s\"" "$RL/workflow.json"; then
    check "ralph-loop references @lib/$s" "local" "@lib"
  elif grep -q "\"@lib/$s\"" "$RL/workflow.json"; then
    check "ralph-loop references @lib/$s" "@lib" "@lib"
  else
    check "ralph-loop references @lib/$s" "unreferenced" "@lib"
  fi
done

# NOTE: ralph-loop is role-routed and legacy-feature-pipeline is not, so their loop
# templates and RALPH.md protocols have DIVERGED ON PURPOSE as of 2026-08-30.
# Only the genuinely shared utility scripts are guarded here. Re-add template
# checks if legacy-feature-pipeline is ever migrated to role routing.

if [ "$fails" -eq 0 ]; then echo "all shared-scripts tests passed"; else
  echo "$fails shared-scripts test(s) FAILED"; exit 1; fi
