#!/bin/sh
# Tests for deps-count.sh — deterministic routing input for deps-gate.
#
# WHY (run 5e86d796, 2026-09-29): deps-assess listed 7 open questions that only a
# human could answer, classed every one NON-BLOCKING because the design named a
# fallback, and deps-gate's first-match `blocking_count == 0 -> proceed` waved the
# run into implementation. The plans themselves required those decisions, so the
# implementer stopped at 3 of 17 tasks and the run ended done-blocked without the
# human ever being asked. Routing now keys on a COUNT of the gap lines that need a
# person, computed here from the line prefixes rather than trusted from the LLM.
#
# Verifies:
#   A. BLOCKING: and DECISION: lines both count toward needs_resolution;
#      NON-BLOCKING: lines do not.
#   B. No gaps at all -> needs_resolution 0 (the run proceeds).
#   C. Malformed deps-assess output (missing or non-array gaps) fails CLOSED:
#      needs_resolution > 0, so a person looks rather than the run proceeding.
#   D. Prefix matching is case-insensitive and tolerates leading whitespace, and a
#      line that merely MENTIONS "decision" mid-text is not counted.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/deps-count.sh"

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
run() { printf '%s' "$1" | sh "$SCRIPT"; }

# --- A ---
OUT="$(run '{"deps-assess":{"gaps":[
  "BLOCKING: plan-1.md — X needs Y; decision: who builds Y?",
  "DECISION: plan-3.md — usability needs a proof contract; decision: which proof?",
  "DECISION: plan-4.md — delivery guarantee; decision: at-least-once or exactly-once?",
  "NON-BLOCKING: plan-2.md — webhook URL is provisioned outside the repo"]}}')"
check "A: blocking counted" "$(printf '%s' "$OUT" | jq -r .blocking)" "1"
check "A: decisions counted" "$(printf '%s' "$OUT" | jq -r .decisions)" "2"
check "A: non-blocking counted separately" "$(printf '%s' "$OUT" | jq -r .non_blocking)" "1"
check "A: needs_resolution = blocking + decisions" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "3"
check "A: parse_ok" "$(printf '%s' "$OUT" | jq -r .parse_ok)" "true"

# --- B ---
OUT="$(run '{"deps-assess":{"gaps":[]}}')"
check "B: no gaps -> nothing to resolve" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: a.md — provisioned outside the repo"]}}')"
check "B: only non-blocking -> nothing to resolve" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"

# --- C ---
OUT="$(run '{"deps-assess":{"notes":"forgot the gaps field"}}')"
check "C: missing gaps fails closed" "$( [ "$(printf '%s' "$OUT" | jq -r .needs_resolution)" -gt 0 ] && echo closed || echo open)" "closed"
check "C: ...and says it could not parse" "$(printf '%s' "$OUT" | jq -r .parse_ok)" "false"
OUT="$(run '{"deps-assess":{"gaps":"BLOCKING: not an array"}}')"
check "C: non-array gaps fails closed" "$( [ "$(printf '%s' "$OUT" | jq -r .needs_resolution)" -gt 0 ] && echo closed || echo open)" "closed"
OUT="$(run '{}')"
check "C: no deps-assess output at all fails closed" "$( [ "$(printf '%s' "$OUT" | jq -r .needs_resolution)" -gt 0 ] && echo closed || echo open)" "closed"

# --- D ---
OUT="$(run '{"deps-assess":{"gaps":["  decision: plan.md — lower-case prefix",
  "NON-BLOCKING: plan.md — a decision was already made upstream"]}}')"
check "D: case-insensitive, leading space tolerated" "$(printf '%s' "$OUT" | jq -r .decisions)" "1"
check "D: 'decision' mid-line does not count" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"

echo "---"
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "$fails check(s) FAILED"; exit 1; fi
