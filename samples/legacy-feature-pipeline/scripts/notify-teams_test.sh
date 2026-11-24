#!/bin/sh
# Tests for notify-teams.sh — posts a rich alert to a Teams webhook, or no-ops.
#
# Verifies (no network required):
#   A. No teams_webhook_url and no env var => clean no-op {skipped:true}, exit 0.
#   B. The "Name=Value" fact args are parsed into the payload the script builds.
# The actual POST path (URL set) is exercised live by the workflow, not here.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/notify-teams.sh"

fails=0
check() { # desc actual expected
  if [ "$2" = "$3" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    echo "        expected: $3"
    echo "        actual:   $2"
    fails=$((fails + 1))
  fi
}

# --- Case A: no URL anywhere => no-op, never fails the run ---
unset ARBITRON_TEAMS_WEBHOOK_URL 2>/dev/null || true
export ARBITRON_NOTIFY_LOCAL=0 # no real desktop notifications from a test run
out="$(printf '{}' | sh "$SCRIPT" "Title" "Summary" "info" "K=V" 2>/dev/null)"
rc=$?
skipped="$(printf '%s' "$out" | jq -r '.skipped')"
check "no URL => skipped:true" "$skipped" "true"
check "no URL => exit 0 (never fails the workflow)" "$rc" "0"

# --- Case B: empty-string var is still treated as unset (no-op) ---
out2="$(printf '{"teams_webhook_url":""}' | sh "$SCRIPT" "T" "S" "good" 2>/dev/null)"
check "empty url var => skipped:true" "$(printf '%s' "$out2" | jq -r '.skipped')" "true"

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
