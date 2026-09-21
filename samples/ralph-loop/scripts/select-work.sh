#!/bin/sh
# Chooses what the NEXT refinement pass works on, and therefore which model pair
# handles it. This is the router that makes per-role models possible.
#
# The loop runs two phases. Phase 1 works single-system areas, interleaved by
# risk: for the highest-risk area with an outstanding box, review 1 is refined by
# Sonnet and assessed by Sol; review 2 is refined by Sol and assessed by Opus.
# Only when every single-system box is ticked does phase 2 begin, working the
# paired integration table with Opus refining and Sol assessing.
#
# Every pair is cross-family ON PURPOSE. A single-system area needs two
# INDEPENDENT approvals, and two passes by the same model family share blind
# spots — that is not redundancy, it is the same opinion twice. Routing by which
# box is outstanding also means a withheld approval naturally sends the same box
# back to the same refiner next pass, with no extra bookkeeping.
#
# Reads {workspace, work_dir} from stdin JSON. Emits:
#   next_kind      string — single_1 | single_2 | paired | none
#   target_id      string — e.g. "single:14" or "paired:37" (for the trace)
#   target_label   string — the row's own text, for the mission prompt
#   singles_left   number — single-system AREAS with an outstanding box
#   paired_left    number — outstanding paired rows
#   withhold_streak number — consecutive passes that targeted this same item
#   escalate       bool   — streak >= 3; the mission must log an [ESCALATION] gap
#   summary        string
#
# Fails SOFT: an unreadable plan emits next_kind=none with a loud summary, which
# routes the loop to its exit rather than spinning on work it cannot identify.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"

SINGLES="$WORK/docs/reference/focus-areas.md"
PAIRED="$WORK/docs/reference/paired-reviews.md"

emit() { # kind id label singles_left paired_left streak escalate summary
  jq -n --arg k "$1" --arg i "$2" --arg l "$3" --argjson s "$4" --argjson p "$5" \
        --argjson w "$6" --argjson e "$7" --arg m "$8" \
    '{next_kind:$k, target_id:$i, target_label:$l, singles_left:$s, paired_left:$p,
      withhold_streak:$w, escalate:$e, summary:$m}'
}

# Rank rows high > med > low, then by file order, so a run stopped early has
# worked the things most likely to be broken. seed-ralph already writes them in
# risk order; sorting here means a hand-edited plan still routes sensibly.
rank() { case "$1" in [Hh]igh) echo 0 ;; [Mm]ed*) echo 1 ;; [Ll]ow) echo 2 ;; *) echo 1 ;; esac; }

pick_single() { # -> "boxnum<TAB>num<TAB>risk<TAB>label" of the best outstanding area
  [ -f "$SINGLES" ] || return 1
  awk -F'|' '
    /^[[:space:]]*\|/ {
      n=0; for (i=1;i<=NF;i++) if ($i ~ /\[[ xX]\]/) { n++; box[n]=$i; boxi[n]=i }
      if (n < 2) next                       # not a two-approval row
      num=$2; lab=$3; risk=$4
      gsub(/^[ \t]+|[ \t]+$/,"",num); gsub(/^[ \t]+|[ \t]+$/,"",lab); gsub(/^[ \t]+|[ \t]+$/,"",risk)
      if (num !~ /^[0-9]+$/) next           # skips the header/format example
      b1 = (box[1] ~ /\[[xX]\]/) ? 1 : 0
      b2 = (box[2] ~ /\[[xX]\]/) ? 1 : 0
      if (b1 && b2) next
      want = b1 ? 2 : 1
      printf "%s\t%s\t%s\t%s\n", want, num, risk, lab
    }' "$SINGLES"
}

pick_paired() { # -> "num<TAB>risk<TAB>label"
  [ -f "$PAIRED" ] || return 1
  awk -F'|' '
    /^[[:space:]]*\|/ {
      hit=0; for (i=1;i<=NF;i++) if ($i ~ /\[[ xX]\]/) { hit=1; box=$i }
      if (!hit) next
      if (box ~ /\[[xX]\]/) next
      num=$2; lab=$3; risk=$5
      gsub(/^[ \t]+|[ \t]+$/,"",num); gsub(/^[ \t]+|[ \t]+$/,"",lab); gsub(/^[ \t]+|[ \t]+$/,"",risk)
      if (num !~ /^[0-9]+$/) next
      printf "%s\t%s\t%s\n", num, risk, lab
    }' "$PAIRED"
}

SL=0; PL=0
S_ROWS="$(pick_single 2>/dev/null || true)"
P_ROWS="$(pick_paired 2>/dev/null || true)"
[ -n "$S_ROWS" ] && SL=$(printf '%s\n' "$S_ROWS" | grep -c .)
[ -n "$P_ROWS" ] && PL=$(printf '%s\n' "$P_ROWS" | grep -c .)

if [ ! -f "$SINGLES" ] && [ ! -f "$PAIRED" ]; then
  emit none "" "" 0 0 0 false \
    "NO CONTROL FILES: neither $SINGLES nor $PAIRED exists, so no work can be selected. The loop will exit; re-seed or resume against a branch that carries the review plan."
  exit 0
fi

KIND=none; ID=""; LABEL=""
if [ -n "$S_ROWS" ]; then
  BEST="$(printf '%s\n' "$S_ROWS" | while IFS="$(printf '\t')" read -r want num risk lab; do
            [ -n "$num" ] || continue; printf '%s\t%s\t%s\t%s\n' "$(rank "$risk")" "$want" "$num" "$lab"
          done | sort -k1,1n -k3,3n | head -1)"
  WANT="$(printf '%s' "$BEST" | cut -f2)"
  NUM="$(printf '%s' "$BEST" | cut -f3)"
  LABEL="$(printf '%s' "$BEST" | cut -f4-)"
  KIND="single_$WANT"; ID="single:$NUM/review$WANT"
elif [ -n "$P_ROWS" ]; then
  BEST="$(printf '%s\n' "$P_ROWS" | while IFS="$(printf '\t')" read -r num risk lab; do
            [ -n "$num" ] || continue; printf '%s\t%s\t%s\n' "$(rank "$risk")" "$num" "$lab"
          done | sort -k1,1n -k2,2n | head -1)"
  NUM="$(printf '%s' "$BEST" | cut -f2)"
  LABEL="$(printf '%s' "$BEST" | cut -f3-)"
  KIND=paired; ID="paired:$NUM"
fi

# Per-target withhold streak. Routing sends a withheld box straight back to the
# same refiner, which is correct but can grind; after three consecutive passes on
# one item the mission is told to log an [ESCALATION] gap so it surfaces to a
# human instead of silently consuming the budget.
STATE="$WS/last-target"; STREAK=0
PREV=""; [ -f "$STATE" ] && PREV="$(cat "$STATE" 2>/dev/null || true)"
PREV_ID="${PREV%% *}"; PREV_N="${PREV##* }"
case "$PREV_N" in ''|*[!0-9]*) PREV_N=0 ;; esac
if [ -n "$ID" ] && [ "$ID" = "$PREV_ID" ]; then STREAK=$((PREV_N + 1)); else STREAK=0; fi
[ -n "$ID" ] && printf '%s %s' "$ID" "$STREAK" > "$STATE"
ESC=false; [ "$STREAK" -ge 3 ] && ESC=true

case "$KIND" in
  single_1) SUM="PHASE 1 (single-system), review 1 of area $NUM — refined by Sonnet, assessed by Sol. $SL single-system area(s) and $PL paired review(s) outstanding." ;;
  single_2) SUM="PHASE 1 (single-system), review 2 of area $NUM — refined by Sol, assessed by Opus. Review 1 already passed, so this is the INDEPENDENT second look. $SL single-system area(s) and $PL paired review(s) outstanding." ;;
  paired)   SUM="PHASE 2 (paired integration), review $NUM — refined by Opus, assessed by Sol. All single-system areas are approved. $PL paired review(s) outstanding." ;;
  *)        SUM="NOTHING OUTSTANDING: every single-system box and every paired review carries its approval. The loop may exit." ;;
esac
[ "$ESC" = true ] && SUM="$SUM ESCALATE: this is pass $((STREAK + 1)) in a row on $ID — record an [ESCALATION] gap naming what would have to change for it to pass."

emit "$KIND" "$ID" "$LABEL" "$SL" "$PL" "$STREAK" "$ESC" "$SUM"
