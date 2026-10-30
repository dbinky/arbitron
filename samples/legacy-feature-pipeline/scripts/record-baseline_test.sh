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

# --- Case B: missing file falls back to an EMPTY baseline and CONTINUES ---
# This deliberately replaced an earlier hard-fail contract. A single premature
# exit by the baseline-tests agent (e.g. backgrounding a slow suite and ending
# its turn before writing the file) used to discard an entire spec+design+plan
# run one step before it could be used, and retrying was futile because only the
# agent can regenerate that artifact. The fallback must be LOUD, not silent:
# without a real snapshot every currently-failing test looks "new", so
# baseline_source is the flag downstream stages and humans read.
WS2="$TMP/ws-missing"
mkdir -p "$WS2"
if out2="$(printf '{"workspace":"%s"}' "$WS2" | sh "$SCRIPT" 2>/dev/null)"; then
  check "missing baseline continues (does not kill the run)" "ok" "ok"
  check "missing baseline is flagged as fallback" \
    "$(printf '%s' "$out2" | jq -r .baseline_source)" "fallback-empty"
  check_contains "missing baseline warns in the summary" \
    "$(printf '%s' "$out2" | jq -r .baseline_summary)" "FALLBACK EMPTY BASELINE"
else
  echo "FAIL - missing baseline should fall back and continue, but exited non-zero"
  fails=$((fails + 1))
fi

# --- Case C: malformed baseline (no .suites) also falls back, flagged ---
WS3="$TMP/ws-bad"
mkdir -p "$WS3"
echo '{"oops":true}' > "$WS3/test-baseline.json"
if out3="$(printf '{"workspace":"%s"}' "$WS3" | sh "$SCRIPT" 2>/dev/null)"; then
  check "malformed baseline is flagged as fallback" \
    "$(printf '%s' "$out3" | jq -r .baseline_source)" "fallback-empty"
else
  echo "FAIL - malformed baseline should fall back and continue, but exited non-zero"
  fails=$((fails + 1))
fi

# --- Case D: a VALID baseline must NOT be flagged as a fallback ---
# Guards the inverse: if baseline_source were hardcoded, every run would look
# degraded and the flag would stop meaning anything.
check "valid baseline is not flagged as fallback" \
  "$(printf '%s' "$out" | jq -r '.baseline_source // "unset"')" "measured"

# --- Case E: the measured commit is saved as baseline-head ---
# verify-tests diffs pass 1 against it; without it pass 1 re-ran every suite.
WS5="$TMP/ws-git"
mkdir -p "$WS5/work"
cp "$WS/test-baseline.json" "$WS5/test-baseline.json"
git -C "$WS5/work" init -q
echo x > "$WS5/work/f"
git -C "$WS5/work" -c user.email=t@t -c user.name=t add -A
git -C "$WS5/work" -c user.email=t@t -c user.name=t commit -qm base
printf '{"workspace":"%s","work_dir":"%s/work"}' "$WS5" "$WS5" | sh "$SCRIPT" >/dev/null 2>&1
check "baseline-head records the measured HEAD" \
  "$(cat "$WS5/baseline-head" 2>/dev/null || echo missing)" "$(git -C "$WS5/work" rev-parse HEAD)"

# E2: no git repo -> no baseline-head, and no failure.
check "no git repo writes no baseline-head" \
  "$( [ -f "$WS/baseline-head" ] && echo present || echo absent)" "absent"

echo "---"
if [ "$fails" -eq 0 ]; then
  echo "PASS"
else
  echo "$fails check(s) FAILED"
  exit 1
fi
