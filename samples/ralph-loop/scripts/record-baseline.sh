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
#       {"name","command","build_ok":true,
#        "failed_count":N,"passed_count":M,"failing_tests":[...]}
#     ]
#   }
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace')"
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

BASELINE="$(jq -c '.' "$F")"
COMMANDS="$(jq -c '[.suites[].command]' "$F")"
TOTAL="$(jq -r '([.suites[].failed_count] | add) // 0' "$F")"
NSUITES="$(jq -r '.suites | length' "$F")"
SUMMARY="$TOTAL pre-existing failing test(s) across $NSUITES suite(s) at branch point"

jq -n --argjson b "$BASELINE" --argjson c "$COMMANDS" --arg s "$SUMMARY" \
  '{test_baseline:$b, test_commands:$c, baseline_summary:$s, baseline_source:"measured"}'
