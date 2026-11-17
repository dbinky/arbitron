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
#   F. Fail closed on formatting: bold, bulleted and numbered prefixes still
#      classify; a non-empty gap with NO recognised prefix counts as blocking;
#      non-string entries make parse_ok false and count as blocking; the
#      has_unresolved / unresolved_count fields are a backstop when no gap counted.
#   E. The assess node id is $1 (default deps-assess), so the plan stage's second
#      gate counts deps-assess-2 and never the stale first-pass output.
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
run_id() { printf '%s' "$2" | sh "$SCRIPT" "$1"; }

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

# --- E ---
BOTH='{"deps-assess":{"gaps":["DECISION: a.md — first pass; decision: which?"]},
  "deps-assess-2":{"gaps":["NON-BLOCKING: a.md — settled by resolve-deps"]}}'
OUT="$(run_id deps-assess-2 "$BOTH")"
check "E: node-id arg reads deps-assess-2" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"
check "E: ...and counts its lines" "$(printf '%s' "$OUT" | jq -r .non_blocking)" "1"
OUT="$(run "$BOTH")"
check "E: default node id is deps-assess" "$(printf '%s' "$OUT" | jq -r .decisions)" "1"
OUT="$(run_id deps-assess-2 '{"deps-assess":{"gaps":[]}}')"
check "E: named node absent fails closed" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"

# --- F ---
OUT="$(run '{"deps-assess":{"gaps":["**BLOCKING:** plan 2 needs Foo.Bar", "- DECISION: pick storage backend", "A human must choose X"],"has_unresolved":true,"unresolved_count":3}}')"
check "F: bold + bullet + unprefixed all need resolution" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "3"
check "F: bold prefix is BLOCKING" "$(printf '%s' "$OUT" | jq -r .blocking)" "2"
check "F: bulleted prefix is a DECISION" "$(printf '%s' "$OUT" | jq -r .decisions)" "1"
check "F: the unprefixed gap is reported as unclassified" "$(printf '%s' "$OUT" | jq -r .unclassified)" "1"
OUT="$(run '{"deps-assess":{"gaps":["A human must choose the storage backend"],"has_unresolved":true,"unresolved_count":1}}')"
check "F: a lone unprefixed gap is not resolved" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"
OUT="$(run '{"deps-assess":{"gaps":["1. **Decision**: which store?", "* _non-blocking_: nothing", "**NON-BLOCKING**: x"]}}')"
check "F: numbered/bold-wrapped DECISION counts" "$(printf '%s' "$OUT" | jq -r .decisions)" "1"
check "F: emphasis-wrapped NON-BLOCKING lines do not need resolution" "$(printf '%s' "$OUT" | jq -r .non_blocking)" "2"
check "F: ...so only the DECISION does" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: ok", {"gap":"object, not a string"}, null, 7]}}')"
check "F: malformed entries make parse_ok false" "$(printf '%s' "$OUT" | jq -r .parse_ok)" "false"
check "F: ...and are counted, not dropped" "$(printf '%s' "$OUT" | jq -r .malformed)" "3"
check "F: ...as blocking" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "3"
OUT="$(run '{"deps-assess":{"gaps":["", "   ", "-"]}}')"
check "F: empty and bullet-only gaps are not gaps" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"
OUT="$(run '{"deps-assess":{"gaps":[],"has_unresolved":true,"unresolved_count":2}}')"
check "F: has_unresolved with nothing counted is raised to 1" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: a"],"has_unresolved":false,"unresolved_count":0}}')"
check "F: a consistent clean assessment proceeds" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"

# --- G: the backstop must not fire on a claim the NON-BLOCKING lines explain ---
# WHY (run a349951a, 2026-11-11): deps-assess-2 listed one NON-BLOCKING gap and
# counted it in has_unresolved/unresolved_count. The backstop raised
# needs_resolution to 1, so a fully resolved plan recorded itself blocked.
OUT="$(run_id deps-assess-2 '{"deps-assess-2":{"blocking_count":0,"gaps":["NON-BLOCKING: live verification remains gated; it runs after implementation"],"has_blocking":false,"has_unresolved":true,"unresolved_count":1}}')"
check "G: an unresolved claim explained by NON-BLOCKING lines proceeds" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: a","NON-BLOCKING: b"],"has_unresolved":true}}')"
check "G: has_unresolved without a count, with NON-BLOCKING lines, proceeds" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "0"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: a"],"has_unresolved":true,"unresolved_count":3}}')"
check "G: a claimed count above the NON-BLOCKING lines still blocks" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"
OUT="$(run '{"deps-assess":{"gaps":["NON-BLOCKING: a"],"unresolved_count":2}}')"
check "G: unresolved_count alone above the NON-BLOCKING lines still blocks" "$(printf '%s' "$OUT" | jq -r .needs_resolution)" "1"

echo "---"
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "$fails check(s) FAILED"; exit 1; fi
