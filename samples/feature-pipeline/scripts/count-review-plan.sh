#!/bin/sh
# Counts the ralph review plan's outstanding approvals straight from
# focus-areas.md, so "is the plan complete?" is arithmetic instead of narrative.
#
# WHY THIS EXISTS: refine-assess is toolless — it cannot read focus-areas.md. It
# decided `done` from {{refine.response}}, i.e. from whether the pass emitted
# <promise>FINIT</promise>. FINIT is the pass agent asserting completion about
# itself, and the assessor has no way to contradict it; one real verdict read
# "the pass asserts FINIT with nothing in the evidence contradicting completion",
# which is the only thing a toolless reader can ever observe.
#
# Run ae3ec85f (public-chat-memory) exited that way at pass 56 of a 200 cap with
# 25/26 single-system areas approved and ALL 112 paired integration reviews
# untouched — 114 of 164 required approvals outstanding — and shipped a PR whose
# review plan was 70% unexecuted. Raising the iteration cap would not have
# changed it: the loop did not run out of passes, it wrongly believed it had
# finished.
#
# So the gate now keys off this counter. The assessor still judges what a
# counter cannot see (new-vs-baseline test failures, wiring, open gaps); both
# must agree before the loop may exit.
#
# Reads {workspace, work_dir} from stdin JSON. Emits:
#   singles_total/singles_unapproved   number — rows under the single-system table
#   paired_total/paired_unapproved     number — rows under the paired table
#   unchecked_boxes                    number — every unticked [ ] across both
#   open_gaps                          number — advisory; excludes "Won't Fix"
#   plan_complete                      bool   — unchecked_boxes == 0 AND parsed
#   parse_ok                           bool
#   summary                            string
#
# plan_complete is deliberately NOT based on open_gaps: gaps-identified.md's
# formatting varies and its Won't Fix section is intentionally never closed, so
# gating on it could wedge a finished plan. Gaps stay the assessor's call and are
# reported here for its benefit.
#
# Fails CLOSED, never fatally: an unreadable plan emits plan_complete=false so
# the loop cannot exit on missing evidence. The stagnation backstop and the
# iteration cap still bound the run, and a plan the refine agent cannot tick is
# a plan it makes no progress against.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"

PLAN="$WORK/docs/reference/focus-areas.md"
GAPS="$WORK/docs/reference/gaps-identified.md"

emit() { # parse_ok plan_complete st su pt pu unchecked gaps summary
  jq -n --argjson ok "$1" --argjson pc "$2" --argjson st "$3" --argjson su "$4" \
        --argjson pt "$5" --argjson pu "$6" --argjson ub "$7" --argjson og "$8" --arg s "$9" \
    '{parse_ok:$ok, plan_complete:$pc, singles_total:$st, singles_unapproved:$su,
      paired_total:$pt, paired_unapproved:$pu, unchecked_boxes:$ub, open_gaps:$og, summary:$s}'
}

if [ ! -f "$PLAN" ]; then
  echo "count-review-plan: $PLAN not found" >&2
  emit false false 0 0 0 0 0 0 \
    "PLAN NOT READABLE: $PLAN does not exist, so completion cannot be verified. Treating the plan as INCOMPLETE — do not emit done:true on this evidence."
  exit 0
fi

# Section-aware count. A row counts only if it is a table row carrying at least
# one [ ] / [x] box, which skips prose, headers and the format example.
COUNTS="$(awk '
  /^[[:space:]]*#/ {
    h = tolower($0)
    if (h ~ /pair/)        sect = "paired"
    else if (h ~ /single/) sect = "single"
    else                   sect = "other"
    next
  }
  {
    line = $0
    if (line !~ /^[[:space:]]*\|/) next
    if (line !~ /\[[ xX]\]/) next
    open = gsub(/\[ \]/, "[ ]", line)
    if (sect == "paired")      { pt++; ub += open; if (open > 0) pu++ }
    else if (sect == "single") { st++; ub += open; if (open > 0) su++ }
    else                       { ot++; ub += open; if (open > 0) ou++ }
  }
  END { printf "%d %d %d %d %d %d %d", st+0, su+0, pt+0, pu+0, ot+0, ou+0, ub+0 }
' "$PLAN")"

set -- $COUNTS
ST=$1; SU=$2; PT=$3; PU=$4; OT=$5; OU=$6; UB=$7

# Gap items, excluding the Won't Fix section (never expected to close).
OG=0
if [ -f "$GAPS" ]; then
  OG="$(awk '
    /^[[:space:]]*#/ { skip = (tolower($0) ~ /won.?t fix/) ? 1 : 0; next }
    skip { next }
    /\[ \]/ { n += gsub(/\[ \]/, "[ ]") }
    END { printf "%d", n+0 }
  ' "$GAPS")"
fi

if [ "$ST" -eq 0 ] && [ "$PT" -eq 0 ] && [ "$OT" -eq 0 ]; then
  emit false false 0 0 0 0 0 "$OG" \
    "PLAN NOT PARSEABLE: $PLAN contains no review rows with approval boxes. Treating the plan as INCOMPLETE — do not emit done:true on this evidence."
  exit 0
fi

if [ "$UB" -eq 0 ]; then
  PC=true
  SUM="Review plan COMPLETE on the boxes: all $ST single-system and $PT paired reviews carry their required approvals. $OG open gap item(s) remain for you to weigh."
else
  PC=false
  SUM="Review plan INCOMPLETE: $UB approval box(es) still unticked — $SU of $ST single-system reviews and $PU of $PT paired integration reviews outstanding. A FINIT claim against this state is provably wrong."
fi
[ "$OT" -eq 0 ] || SUM="$SUM ($OT row(s) sat under no recognised table heading; $OU unapproved.)"

emit true "$PC" "$ST" "$SU" "$PT" "$PU" "$UB" "$OG" "$SUM"
