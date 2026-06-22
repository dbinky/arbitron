#!/bin/sh
# Lifts the branch-point test baseline into structured instance variables.
#
# The baseline-tests agent runs the project's test suite(s) BEFORE any
# implementation and writes its findings to {{workspace}}/test-baseline.json.
# This script reads {workspace} from stdin JSON, validates that file, and emits
# {test_baseline, test_commands, baseline_summary} so the ralph loop can gate on
# NEW failures vs this baseline instead of a fully green suite.
#
# Fails loudly if the baseline file is missing or malformed: a silent empty
# baseline would wrongly classify every pre-existing failure as "new" and the
# refinement loop would never converge — the exact bug this whole change fixes.
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

[ -f "$F" ] || { echo "baseline file not found: $F" >&2; exit 1; }
jq -e 'type=="object" and (.suites|type=="array")' "$F" >/dev/null 2>&1 \
  || { echo "baseline file malformed (need an object with a .suites array): $F" >&2; exit 1; }

BASELINE="$(jq -c '.' "$F")"
COMMANDS="$(jq -c '[.suites[].command]' "$F")"
TOTAL="$(jq -r '([.suites[].failed_count] | add) // 0' "$F")"
NSUITES="$(jq -r '.suites | length' "$F")"
SUMMARY="$TOTAL pre-existing failing test(s) across $NSUITES suite(s) at branch point"

jq -n --argjson b "$BASELINE" --argjson c "$COMMANDS" --arg s "$SUMMARY" \
  '{test_baseline:$b, test_commands:$c, baseline_summary:$s}'
