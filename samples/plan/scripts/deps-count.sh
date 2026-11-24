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
# Usage: deps-count.sh [<assess node id> [<audit node id>...]]
# (assess id defaults to deps-assess). The plan stage runs it once per
# assessment (deps-assess, deps-assess-2, deps-assess-3), so each gate counts
# its own pass and never a stale earlier one.
#
# Reads the run's variables from stdin JSON (<assess node id>.gaps, plus the
# has_unresolved / unresolved_count fields as a backstop). Emits:
#   blocking         number — lines prefixed BLOCKING:, plus unrecognised and
#                             malformed entries (see below)
#   decisions        number — lines prefixed DECISION:
#   non_blocking     number — lines prefixed NON-BLOCKING:
#   unclassified     number — non-empty gaps with none of the three prefixes
#   malformed        number — gap entries that are not strings
#   needs_resolution number — blocking + decisions + human_only; deps-gate
#                             proceeds only at 0
#   parse_ok         bool   — false when <id>.gaps was missing or not an array,
#                             or held a non-string entry
#   human_only       number — human-only plan steps the audit found (0 without one)
#   gaps             array  — the assessment's string gaps, then one
#                             "HUMAN-ONLY: <path>:<line>: <step>" per audit hit;
#                             an "UNREADABLE: ..." line for anything unreadable.
#                             resolve-deps shows it; mark-unresolved records it.
#
# Fails CLOSED. A prefix is matched after stripping list bullets, numbering and
# markdown emphasis ("- **BLOCKING:** ..." counts as BLOCKING). A non-empty gap
# with no recognised prefix is treated as BLOCKING, never dropped: a model that
# breaks the prefix must not read as "resolved". A non-string entry makes
# parse_ok false and counts as blocking. If the assessment itself says something
# is unresolved but no gap counted, needs_resolution is raised to 1 -- unless its
# NON-BLOCKING lines explain the claim: unresolved_count must exceed them, or
# has_unresolved must come with none. (Run a349951a, 2026-11-11: a lone
# NON-BLOCKING gap counted as "unresolved" blocked a fully resolved plan.) Unreadable gaps report needs_resolution=1, so a
# person looks at the plans rather than the run proceeding on missing evidence.
#
# Audit ids name plan-audit.sh outputs. When any are given, the LAST one present
# in the snapshot is read (a re-audit after a checkbox-step restore supersedes
# the pass's first audit), and its human_only steps -- unchecked plan steps
# only a person can do: operator observation, live vendor evidence, captured
# production fixtures, real credentials -- add to needs_resolution. (Lunaroute
# implement runs a5259bec / c028678b / 8fc37529 stalled for hours on such
# steps.) A named audit that is absent, or whose human_only is not a number,
# adds 1: fail closed. With no audit id the output is the old one plus
# human_only 0 and gaps.
set -eu
ID="${1:-deps-assess}"
[ "$#" -gt 0 ] && shift
IN="$(cat)"

if printf '%s' "$IN" | jq -e --arg id "$ID" '.[$id].gaps | type == "array"' > /dev/null 2>&1; then
  ASSESS="$(printf '%s' "$IN" | jq --arg id "$ID" '
  def norm: ascii_downcase | sub("^(?:[\\s\\-*•_>#`]|[0-9]+[.)])+"; "");
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
  | ((($a.unresolved_count | type) == "number" and $a.unresolved_count > $n)
     or ($a.has_unresolved == true and $n == 0)) as $claims
  | {blocking:$blocking, decisions:$d, non_blocking:$n, unclassified:$u, malformed:($bad | length),
     needs_resolution:(if $need == 0 and $claims then 1 else $need end),
     parse_ok:(($bad | length) == 0),
     gaps:[$all[] | strings | select(hasbody)]}')"
else
  ASSESS="$(jq -n --arg id "$ID" '{blocking:0, decisions:0, non_blocking:0, unclassified:0, malformed:0,
    needs_resolution:1, parse_ok:false, gaps:["UNREADABLE: \($id) output has no gaps array"]}')"
fi

# The last named audit present in the snapshot, or "" when none is.
AUDIT=""
for a in "$@"; do
  if printf '%s' "$IN" | jq -e --arg a "$a" 'has($a)' > /dev/null 2>&1; then AUDIT="$a"; fi
done

if [ "$#" -eq 0 ]; then
  printf '%s' "$ASSESS" | jq '. + {human_only:0}'
elif [ -z "$AUDIT" ]; then
  printf '%s' "$ASSESS" | jq --arg ids "$*" '. + {human_only:0, needs_resolution:(.needs_resolution + 1),
    gaps:(["UNREADABLE: no plan audit output (\($ids))"] + .gaps)}'
else
  printf '%s' "$IN" | jq --arg a "$AUDIT" --argjson s "$ASSESS" '
    .[$a] as $au
    | if ($au.human_only | type) != "number" then
        $s + {human_only:0, needs_resolution:($s.needs_resolution + 1),
              gaps:(["UNREADABLE: \($a).human_only is not a number"] + $s.gaps)}
      else
        $s + {human_only:$au.human_only, needs_resolution:($s.needs_resolution + $au.human_only),
              gaps:($s.gaps + [($au.human_only_lines // [])[] | strings | "HUMAN-ONLY: \(.)"])}
      end'
fi
