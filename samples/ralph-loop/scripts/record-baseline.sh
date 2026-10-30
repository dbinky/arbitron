#!/bin/sh
# Lifts the branch-point test baseline into structured instance variables.
#
# The baseline-tests agent runs the project's test suite(s) BEFORE any
# implementation and writes its findings to {{workspace}}/test-baseline.json.
# This script reads {workspace} from stdin JSON, validates that file, and emits
# {test_baseline, test_commands, baseline_summary, baseline_source} so the ralph
# loop can gate on NEW failures vs this baseline instead of a fully green suite.
#
# Resilience: a missing or malformed baseline file USED to be a hard failure
# here (exit 1). That meant a single premature exit by the baseline-tests agent
# — e.g. it backgrounded a slow go+dotnet suite and ended its turn "waiting for
# tests to finish" without writing the file — threw away the entire, expensive
# spec+design+plan run one step before it could be used, with retries futile
# (re-running this script can't regenerate an artifact only the agent produces).
#
# We now fall back to an EMPTY baseline and CONTINUE, but flag it LOUDLY via
# baseline_summary / baseline_source="fallback-empty" so downstream stages and
# humans know the gate is running without a real snapshot (every currently-
# failing test will look "new"). The refine loop's stagnation backstop (2
# consecutive no-progress passes) and iteration cap bound the downside of that
# conservative fallback, so a live-but-churning run always beats a dead one.
#
# Expected file shape:
#   {
#     "suites": [
#       {"name","command","paths":[...],"build_ok":true,
#        "failed_count":N,"passed_count":M,"failing_tests":[...]}
#     ]
#   }
# `paths` (optional) is what verify-tests uses to scope a pass to the suites it
# touched; this script passes it through untouched.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"

# Record the commit this baseline measured. verify-tests diffs pass 1 against it
# (there is no last-pass-head until record-pass-commit runs), so pass 1 re-runs
# only the suites its changes touch instead of all of them. A separate file, not
# last-pass-head: that one drives record-pass-commit's progress counting, and in
# feature-pipeline this runs BEFORE implementation, whose commits must not count
# as the first refine pass's progress.
# An IMPORTED baseline (import-baseline.sh left baseline-imported) measured some
# earlier commit, not this HEAD: stamping HEAD would hide every change since the
# measurement from the first verification. Remove any stale baseline-head
# instead, so verify-tests runs its first pass as a full sweep.
if [ -f "$WS/baseline-imported" ]; then
  rm -f "$WS/baseline-head"
elif BH="$(git -C "$WORK" rev-parse HEAD 2>/dev/null)"; then
  printf '%s' "$BH" > "$WS/baseline-head"
fi
F="$WS/test-baseline.json"

emit_empty() {
  reason="$1"
  echo "record-baseline: $reason — falling back to an EMPTY baseline and continuing" >&2
  jq -n --arg s "FALLBACK EMPTY BASELINE ($reason): no pre-existing failures were recorded, so the refine loop will treat every currently-failing test as NEW. Investigate the baseline-tests step." \
    '{test_baseline:{suites:[]}, test_commands:[], baseline_summary:$s, baseline_source:"fallback-empty"}'
}

if [ ! -f "$F" ]; then
  emit_empty "baseline file not found at $F"
  exit 0
fi
if ! jq -e 'type=="object" and (.suites|type=="array")' "$F" >/dev/null 2>&1; then
  emit_empty "baseline file malformed (need an object with a .suites array) at $F"
  exit 0
fi

# Persist the baseline INTO the repo. It measures the BRANCH POINT, and a
# resumed run must reuse this exact file rather than re-measuring: re-running
# baseline-tests on a partly-refined branch would record the previous run's own
# fixes as "pre-existing", permanently blinding the regression gate to anything
# it had already broken.
# The file is COMMITTED (R3), so the baseline is in history before the first
# pass-mark and survives a checkpoint push. A resumed run's restored file is
# already committed and identical, so nothing changes and no commit is made.
REL="docs/reference/test-baseline.json"
mkdir -p "$WORK/docs/reference"
cp "$F" "$WORK/$REL"
if [ -n "$(git -C "$WORK" status --porcelain -- "$REL")" ]; then
  git -C "$WORK" add -- "$REL" 1>&2
  git -C "$WORK" commit --quiet -m "chore(ralph): record branch-point baseline" -- "$REL" 1>&2 || \
    { echo "record-baseline: commit failed (git identity?)" >&2; exit 1; }
fi

BASELINE="$(jq -c '.' "$F")"
COMMANDS="$(jq -c '[.suites[].command]' "$F")"
TOTAL="$(jq -r '([.suites[].failed_count] | add) // 0' "$F")"
NSUITES="$(jq -r '.suites | length' "$F")"
SUMMARY="$TOTAL pre-existing failing test(s) across $NSUITES suite(s) at branch point"

jq -n --argjson b "$BASELINE" --argjson c "$COMMANDS" --arg s "$SUMMARY" \
  '{test_baseline:$b, test_commands:$c, baseline_summary:$s, baseline_source:"measured"}'
