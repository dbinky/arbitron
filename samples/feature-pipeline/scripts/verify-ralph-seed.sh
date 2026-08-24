#!/bin/sh
# Verifies that the RALPH.md written by seed-ralph actually carries the loop
# protocol, BEFORE the refinement loop starts consuming it.
#
# WHY THIS EXISTS: templates/seed-ralph.md used to ask for the charter content
# (goal, test commands, definition of done, links) and say nothing about how a
# pass ENDS. The seeding agent filled that vacuum by improvising a "Loop
# protocol" section with no terminating instruction — no Wrap Up, no promise
# tag, no "stop after the tag". The result looked structurally fine and was
# silently broken: the refinement agent had no rule for ending a pass, so the
# loop could not distinguish progress from churn.
#
# A grep is enough to catch that, and it costs nothing. Fail loudly here rather
# than discover it 200 iterations later.
#
# It ALSO enforces that the seeded plan fits the pass budget it will run under.
# seed-ralph has always REPORTED the implied minimum pass count (2*singles +
# paired) and nothing ever checked it. Two runs printed 248 and 202 against a 500
# cap whose observed throughput is ~0.38 boxes/pass — both were unfinishable at
# seed time, and both ran for days before anyone noticed. The number that proved
# it was computed, displayed, and compared to nothing.
#
# Reads {work_dir, max_ralph_iterations} from stdin JSON.
# Emits {ok, present, missing, singles, paired, implied_passes, budget}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
CAP="$(printf '%s' "$IN" | jq -r '.max_ralph_iterations // 500')"
case "$CAP" in ''|*[!0-9]*) CAP=500 ;; esac
[ -n "$WORK" ] || { echo "verify-ralph-seed: work_dir not set" >&2; exit 1; }

RALPH="$WORK/RALPH.md"
[ -f "$RALPH" ] || { echo "verify-ralph-seed: $RALPH does not exist — seed-ralph did not write it" >&2; exit 1; }

# Each marker is a literal substring that must appear. Kept deliberately short
# and lowercase-insensitive so reasonable rewording still passes, while the
# load-bearing terminators (the two tags, the stop rule) are matched exactly.
missing=""
present=""

want() { # label pattern grep-flags
  if grep $3 -q -- "$2" "$RALPH"; then
    present="$present $1"
  else
    missing="$missing $1"
  fi
}

# NOTE: these markers are deliberately no stricter than the canonical
# ralph-plan template (skills/ralph-plan/docs/templates/RALPH.md). A verifier
# that rejects a correctly-seeded file is worse than no verifier — the first
# draft of this check demanded "Do not go back", which the canonical template
# does not contain, and it failed the canonical itself.
want finit-tag       '<promise>FINIT</promise>'   -F
want closer-tag      '<promise>CLOSER</promise>'  -F
want wrap-up         'Wrap Up'                    -i
want stop-after-tag  'Stop after the tag'         -i
want one-issue       'then stop'                  -i
want wrapup-always   'proceed to Wrap Up'         -i

if [ -n "$missing" ]; then
  echo "verify-ralph-seed: RALPH.md is missing loop-protocol elements:$missing" >&2
  echo "  present:$present" >&2
  echo "  A RALPH.md without a terminating rule makes the refinement loop" >&2
  echo "  unbounded: passes never end deliberately and the gate cannot tell" >&2
  echo "  progress from churn. Re-seed with the verbatim protocol block from" >&2
  echo "  templates/seed-ralph.md." >&2
  exit 1
fi

# --- budget check -------------------------------------------------------------
# Fails the run rather than retrying: a deterministic count will not change on a
# second attempt, and an over-budget plan wastes days before it is noticed.
PLAN="$WORK/docs/reference/focus-areas.md"
SINGLES=0; PAIRED=0
if [ -f "$PLAN" ]; then
  COUNTS="$(awk '
    /^[[:space:]]*#/ {
      h = tolower($0)
      if (h ~ /pair/)        sect = "paired"
      else if (h ~ /single/) sect = "single"
      else                   sect = "other"
      next
    }
    { if ($0 !~ /^[[:space:]]*\|/) next
      if ($0 !~ /\[[ xX]\]/) next
      if (sect == "paired")      pt++
      else if (sect == "single") st++ }
    END { printf "%d %d", st+0, pt+0 }' "$PLAN")"
  set -- $COUNTS; SINGLES=$1; PAIRED=$2
fi
IMPLIED=$((2 * SINGLES + PAIRED))
BUDGET=$((CAP * 35 / 100))

if [ "$IMPLIED" -gt "$BUDGET" ]; then
  echo "verify-ralph-seed: the seeded plan CANNOT FINISH in its pass budget." >&2
  echo "  $SINGLES single-system areas x2 + $PAIRED paired reviews = $IMPLIED passes MINIMUM," >&2
  echo "  with zero rework, against a budget of $BUDGET (35% of max_ralph_iterations=$CAP)." >&2
  echo "  Observed throughput is ~0.38 approval boxes per pass, so the real cost is" >&2
  echo "  roughly 2.6x the minimum. A plan this size runs for days and still caps out" >&2
  echo "  mid-review — one such run reached 13% of its plan in 50 hours." >&2
  echo "  Re-seed with the paired table pruned to the highest-risk seams; move the" >&2
  echo "  rest to '## No seam' marked DEFERRED (budget). Do NOT shrink the area list." >&2
  exit 1
fi

jq -n --arg p "${present# }" --argjson s "$SINGLES" --argjson pr "$PAIRED" \
      --argjson im "$IMPLIED" --argjson b "$BUDGET" \
  '{ok:true, present:($p|split(" ")), missing:[],
    singles:$s, paired:$pr, implied_passes:$im, budget:$b}'
