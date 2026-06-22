#!/bin/sh
# Tests for record-baseline.sh — lifts {{workspace}}/test-baseline.json into vars.
#
# Verifies:
#   A. A well-formed baseline yields {test_baseline, test_commands, baseline_summary}
#      with the summary counting failures across suites.
#   B. A missing baseline file fails loudly (non-zero).
#   C. A malformed baseline (no .suites array) fails loudly (non-zero).
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/record-baseline.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

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
check_contains() { # desc haystack needle
  case "$2" in
    *"$3"*) echo "ok   - $1" ;;
    *) echo "FAIL - $1 (missing '$3' in: $2)"; fails=$((fails + 1)) ;;
  esac
}

# --- Case A: well-formed baseline (two suites, 3 failures total) ---
WS="$TMP/ws"
mkdir -p "$WS"
cat > "$WS/test-baseline.json" <<'JSON'
{
  "suites": [
    {"name":"go","command":"go test ./...","build_ok":true,"failed_count":1,"passed_count":40,"failing_tests":["pkg/x.TestA"]},
    {"name":"vitest","command":"npm test","build_ok":true,"failed_count":2,"passed_count":10,"failing_tests":["a.test.ts > x","b.test.ts > y"]}
  ]
}
JSON

out="$(printf '{"workspace":"%s"}' "$WS" | sh "$SCRIPT" 2>/dev/null)"
cmds="$(printf '%s' "$out" | jq -rc '.test_commands')"
summary="$(printf '%s' "$out" | jq -r '.baseline_summary')"
nfail_in_baseline="$(printf '%s' "$out" | jq -r '[.test_baseline.suites[].failed_count]|add')"
check "test_commands lifts every suite command" "$cmds" '["go test ./...","npm test"]'
check "test_baseline preserves failure counts" "$nfail_in_baseline" "3"
check_contains "summary counts total failures" "$summary" "3 pre-existing failing test(s)"
check_contains "summary counts suites" "$summary" "across 2 suite(s)"

# --- Case B: missing file fails loudly ---
WS2="$TMP/ws-missing"
mkdir -p "$WS2"
if printf '{"workspace":"%s"}' "$WS2" | sh "$SCRIPT" >/dev/null 2>&1; then
  echo "FAIL - missing baseline should fail but succeeded"
  fails=$((fails + 1))
else
  echo "ok   - missing baseline fails loudly"
fi

# --- Case C: malformed baseline (no .suites) fails loudly ---
WS3="$TMP/ws-bad"
mkdir -p "$WS3"
echo '{"oops":true}' > "$WS3/test-baseline.json"
if printf '{"workspace":"%s"}' "$WS3" | sh "$SCRIPT" >/dev/null 2>&1; then
  echo "FAIL - malformed baseline should fail but succeeded"
  fails=$((fails + 1))
else
  echo "ok   - malformed baseline fails loudly"
fi

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
