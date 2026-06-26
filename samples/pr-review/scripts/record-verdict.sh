#!/bin/sh
# Lifts the verify-fix verdict into structured outputs {push_ok, blockers}.
#
# Why this exists: the verify-fix node is an agent_call, and agent_call does NOT
# enforce/parse its outputSchema — the verdict ends up as free-text prose in
# verify-fix.response, so the fix-gate can never read verify-fix.push_ok and would
# default to "block" on every run (silently dropping the senior engineer's work
# when the workspace is later GC'd). The verify-fix agent now ALSO writes its
# verdict JSON to {{workspace}}/verify-verdict.json; this script is the reliable
# bridge from that file into gate-readable vars.
#
# If the file is missing or unparseable we default push_ok=false (concerns path).
# Both fix paths push, so committed work is never lost regardless of this value.
set -eu
IN="$(cat)"
WORKSPACE="$(printf '%s' "$IN" | jq -r '.workspace')"
VERDICT="$WORKSPACE/verify-verdict.json"

OK=false
BLOCKERS='[]'
if [ -n "$WORKSPACE" ] && [ "$WORKSPACE" != "null" ] && [ -f "$VERDICT" ] && jq -e . "$VERDICT" >/dev/null 2>&1; then
  OK="$(jq -r 'if (.push_ok==true) then "true" else "false" end' "$VERDICT")"
  BLOCKERS="$(jq -c '(.blockers // [])' "$VERDICT")"
else
  echo "verify-verdict.json missing/invalid; defaulting push_ok=false (fixes still pushed by both paths)" >&2
fi

jq -n --argjson ok "$OK" --argjson bl "$BLOCKERS" '{push_ok:$ok, blockers:$bl}'
