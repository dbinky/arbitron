#!/bin/sh
# Counts the dependency gaps that need a person, so deps-gate routes on
# arithmetic instead of on fields an LLM may or may not emit.
#
# WHY (run 5e86d796, 2026-09-29): deps-assess listed 7 open questions only a human
# could answer, classed every one NON-BLOCKING because the design named a
# fallback, and deps-gate's first-match `blocking_count == 0 -> proceed` sent the
# run into implementation. The plans themselves required those decisions, so the
# implementer stopped at 3 of 17 tasks and the run ended done-blocked without the
# human ever being asked. llm_call outputSchema is requested, not enforced, so
# the counts are derived here from the gap-line prefixes deps-assess must use.
#
# Usage: deps-count.sh [<assess node id>]   (default deps-assess)
# The plan stage runs it once per assessment (deps-assess, deps-assess-2), so
# each gate counts its own pass and never a stale earlier one.
#
# Reads the run's variables from stdin JSON (<assess node id>.gaps, plus the
# has_unresolved / unresolved_count fields as a backstop). Emits:
#   blocking         number — lines prefixed BLOCKING:, plus unrecognised and
#                             malformed entries (see below)
#   decisions        number — lines prefixed DECISION:
#   non_blocking     number — lines prefixed NON-BLOCKING:
#   unclassified     number — non-empty gaps with none of the three prefixes
#   malformed        number — gap entries that are not strings
#   needs_resolution number — blocking + decisions; deps-gate proceeds only at 0
#   parse_ok         bool   — false when <id>.gaps was missing or not an array,
#                             or held a non-string entry
#
# Fails CLOSED. A prefix is matched after stripping list bullets, numbering and
# markdown emphasis ("- **BLOCKING:** ..." counts as BLOCKING). A non-empty gap
# with no recognised prefix is treated as BLOCKING, never dropped: a model that
# breaks the prefix must not read as "resolved". A non-string entry makes
# parse_ok false and counts as blocking. If the assessment itself says something
# is unresolved (has_unresolved true, or unresolved_count > 0) but no gap counted,
# needs_resolution is raised to 1. Unreadable gaps report needs_resolution=1, so a
# person looks at the plans rather than the run proceeding on missing evidence.
set -eu
ID="${1:-deps-assess}"
IN="$(cat)"

if ! printf '%s' "$IN" | jq -e --arg id "$ID" '.[$id].gaps | type == "array"' > /dev/null 2>&1; then
  jq -n '{blocking:0, decisions:0, non_blocking:0, unclassified:0, malformed:0, needs_resolution:1, parse_ok:false}'
  exit 0
fi

printf '%s' "$IN" | jq --arg id "$ID" '
  def norm: ascii_downcase | sub("^(?:[\\s\\-*\u2022_>#`]|[0-9]+[.)])+"; "");
  def hasbody: test("[[:alnum:]]");
  .[$id] as $a
  | $a.gaps as $all
  | [$all[] | select(type != "string")] as $bad
  | [$all[] | strings | select(hasbody) | norm] as $g
  | ($g | map(select(test("^blocking[*_ ]*:"))) | length) as $b
  | ($g | map(select(test("^decision[*_ ]*:"))) | length) as $d
  | ($g | map(select(test("^non-blocking[*_ ]*:"))) | length) as $n
  | (($g | length) - $b - $d - $n) as $u
  | ($b + $u + ($bad | length)) as $blocking
  | ($blocking + $d) as $need
  | (($a.has_unresolved == true) or (($a.unresolved_count | type) == "number" and $a.unresolved_count > 0)) as $claims
  | {blocking:$blocking, decisions:$d, non_blocking:$n, unclassified:$u, malformed:($bad | length),
     needs_resolution:(if $need == 0 and $claims then 1 else $need end),
     parse_ok:(($bad | length) == 0)}'
