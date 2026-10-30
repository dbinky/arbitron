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
# Reads the run's variables from stdin JSON (deps-assess.gaps). Emits:
#   blocking         number — lines prefixed BLOCKING:
#   decisions        number — lines prefixed DECISION:
#   non_blocking     number — lines prefixed NON-BLOCKING:
#   needs_resolution number — blocking + decisions; deps-gate proceeds only at 0
#   parse_ok         bool   — false when deps-assess.gaps was missing or not an array
#
# Fails CLOSED: unreadable gaps report needs_resolution=1, so a person looks at
# the plans rather than the run proceeding on missing evidence.
set -eu
IN="$(cat)"

if ! printf '%s' "$IN" | jq -e '.["deps-assess"].gaps | type == "array"' > /dev/null 2>&1; then
  jq -n '{blocking:0, decisions:0, non_blocking:0, needs_resolution:1, parse_ok:false}'
  exit 0
fi

printf '%s' "$IN" | jq '
  [.["deps-assess"].gaps[] | strings | ascii_downcase | sub("^\\s+"; "")] as $g
  | ($g | map(select(startswith("blocking:"))) | length) as $b
  | ($g | map(select(startswith("decision:"))) | length) as $d
  | ($g | map(select(startswith("non-blocking:"))) | length) as $n
  | {blocking:$b, decisions:$d, non_blocking:$n, needs_resolution:($b + $d), parse_ok:true}'
