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
SHARED="init-workspace.sh checkpoint-push.sh push-and-pr.sh notify.sh notify-teams.sh record-baseline.sh verify-ralph-seed.sh record-pass-commit.sh count-review-plan.sh"
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

# --- Loop templates are shared too ------------------------------------------
for t in refine.md refine-assess.md; do
  if diff -q "$RL/templates/$t" "$FP/templates/$t" >/dev/null 2>&1; then
    check "identical loop template: $t" "same" "same"
  else
    check "identical loop template: $t" "DRIFTED" "same"
  fi
done

# --- Both seed templates must carry the same terminators --------------------
# seed-ralph.md legitimately differs between the two (one scopes from a git
# diff, the other from plan docs), so the files are not compared — but the
# protocol block they emit must agree, or the two loops terminate differently.
for w in "$RL" "$FP"; do
  name="$(basename "$w")"
  for marker in '<promise>FINIT</promise>' '<promise>CLOSER</promise>' 'Stop after the tag' 'proceed to Wrap Up'; do
    if grep -qF -- "$marker" "$w/templates/seed-ralph.md"; then
      check "$name seed emits '$marker'" "present" "present"
    else
      check "$name seed emits '$marker'" "MISSING" "present"
    fi
  done
done

# --- The verifier must accept both seeds' protocol blocks -------------------
for w in "$RL" "$FP"; do
  name="$(basename "$w")"
  T="$(mktemp -d)"
  sed -n '/---8<--- copy from here/,/---8<--- copy to here/p' "$w/templates/seed-ralph.md" > "$T/RALPH.md"
  if printf '{"work_dir":"%s"}' "$T" | sh "$RL/scripts/verify-ralph-seed.sh" >/dev/null 2>&1; then
    check "$name protocol block passes verify-ralph-seed" "accepted" "accepted"
  else
    check "$name protocol block passes verify-ralph-seed" "REJECTED" "accepted"
  fi
  rm -rf "$T"
done

echo
if [ "$fails" -eq 0 ]; then echo "all shared-scripts tests passed"; else
  echo "$fails shared-scripts test(s) FAILED"; exit 1; fi
