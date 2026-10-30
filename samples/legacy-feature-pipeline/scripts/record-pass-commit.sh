#!/bin/sh
# Records what the refinement pass ACTUALLY committed, so the progress gate keys
# off git rather than off the pass agent's prose.
#
# WHY THIS EXISTS: refine-assess is toolless by design — its only evidence used
# to be {{refine.response}}. But refine.md's Wrap Up asks the agent two questions
# ("does every review have its approvals?" and "are there open gaps?"), both
# about what REMAINS. A compliant pass therefore writes a report that
# structurally omits what it accomplished, and the assessor was left scoring
# "did this pass progress?" against a narrative that could not answer it.
#
# The observed result was a coin flip. Two near-identical reports — same shape,
# same substance — scored progressed=true and progressed=false on consecutive
# passes, and the second one had a real commit behind it (a review box ticked,
# focus-areas.md updated). Two unlucky flips in a row trip the stagnation
# backstop and end an otherwise healthy run; that is what killed the loop at
# pass 48 once and pass 13 again.
#
# A commit is not a narrative. If the pass committed, it did work.
#
# Reads {workspace, work_dir} from stdin JSON. Emits:
#   new_commit_count  number  — commits added since the previous pass
#   tracking_touched  bool    — focus-areas.md / gaps-identified.md in that set
#   code_changed      bool    — a commit touched something outside the tracking files
#   boxes_ticked      number  — approval boxes newly ticked since the previous pass
#   gaps_closed       number  — gap items newly checked since the previous pass
#   passes_since_approval number — consecutive passes with no approval box ticked
#   approval_stalled  bool    — that counter has crossed APPROVAL_STALL_LIMIT
#   progress_evidence bool    — (code_changed OR box ticked OR gap closed) AND NOT approval_stalled
#
# WHY THERE IS A SECOND AXIS (learned from deep-ralph-loop run bea09188): commit
# activity alone cannot see a loop working hard against a receding target. That
# run spent 15 consecutive passes writing REAL production code — each pass fixed
# one variant of a bad value surviving a down-migration, and each fix uncovered
# the adjacent variant (NaN -> Infinity -> undecodable -> non-number -> scalar).
# Open gaps went 40 -> 45 while 49 were closed. code_changed was true every pass,
# so progress_evidence was true every pass, the streak stayed pinned at 0, and the
# escalation never fired: ZERO rescues in 50 passes while approvals sat at 11/206.
#
# Real work is not the same as advancement. A loop whose approval count has not
# moved in APPROVAL_STALL_LIMIT passes is not converging, however busy it looks,
# and something other than another identical pass needs to happen. The flag is
# STICKY once tripped and clears only when an approval actually lands, so the
# no-progress streak can reach 2 on consecutive passes and escalate; a one-shot
# signal would be swallowed by the next committing pass.
#
# WHY progress_evidence IS NOT SIMPLY "did it commit" (learned from run b61b4f48):
# the approver commits its verdict every pass, including when it WITHHOLDS. A
# bare commit count therefore reported progress on every single pass, pinned
# no_progress_streak at 0, and made the stagnation backstop and the Opus rescue
# unreachable. That run spent 14 consecutive passes committing nothing but
# tracking-file prose — zero code changes, zero of 246 boxes ticked — while the
# loop believed it was advancing and would have run to the 500-pass cap.
#
# A commit is not a narrative, but a bookkeeping commit is not progress either.
# Note what is deliberately NOT counted: ADDING a new unchecked gap item. Logging
# a finding is useful, but counting it would restore exactly the churn hole this
# closes. A genuinely discovering pass simply scores no-progress and the next
# pass acts on it; two in a row escalate to the rescue, which is the intended
# behaviour rather than a failure.
#   head_sha          string
#   commit_stat       string  — git log --stat for those commits (truncated)
#   summary           string  — one line for humans reading the trace
#
# NEVER fails the run: a missing repo or unreadable git history emits a
# conservative "no evidence" verdict and exits 0, leaving the assessor to fall
# back to the pass's own Progress: line. Killing a long refinement run over a
# bookkeeping script would be a far worse failure than a soft verdict.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"

# State lives in the per-instance workspace and survives between loop passes,
# which is what lets us diff THIS pass against the previous one.
STATE="$WS/last-pass-head"
MAXLEN=4000

emit_none() {
  jq -n --arg s "$1" \
    '{new_commit_count:0, tracking_touched:false, progress_evidence:false,
      code_changed:false, boxes_ticked:0, gaps_closed:0,
      passes_since_approval:0, approval_stalled:false,
      head_sha:"", commit_stat:"(no commit evidence available)", summary:$s}'
}

# Count ticked boxes so "did a review actually get approved?" is arithmetic.
count_ticked() { grep -o '\[[xX]\]' "$1" 2>/dev/null | wc -l | tr -d ' '; }

if ! HEAD="$(git -C "$WORK" rev-parse HEAD 2>/dev/null)"; then
  echo "record-pass-commit: no git repo at $WORK — emitting no-evidence verdict" >&2
  emit_none "NO COMMIT EVIDENCE: could not read a git repository at $WORK. Judge progress from the pass's Progress: line instead."
  exit 0
fi

PREV=""
if [ -f "$STATE" ]; then PREV="$(cat "$STATE")"; fi

if [ -n "$PREV" ] && git -C "$WORK" cat-file -e "${PREV}^{commit}" 2>/dev/null; then
  # Normal case: we know exactly where the previous pass left HEAD.
  COUNT="$(git -C "$WORK" rev-list --count "${PREV}..HEAD" 2>/dev/null || echo 0)"
  LOGARGS="${PREV}..HEAD"
else
  # First pass of the run (or a rewritten history): there is no previous mark to
  # diff against, so fall back to the tip commit. The seeding commit lands before
  # pass 1, so a tip that is still the seed means this pass committed nothing.
  SUBJ="$(git -C "$WORK" log -1 --pretty=%s 2>/dev/null || echo '')"
  case "$SUBJ" in
    chore:\ seed*|chore\(*\):\ seed*) COUNT=0 ;;
    *)                                COUNT=1 ;;
  esac
  LOGARGS="-1"
fi

CHANGED="$(git -C "$WORK" log $LOGARGS --name-only --pretty=format: 2>/dev/null | sed '/^$/d' | sort -u || true)"
STAT="$(git -C "$WORK" log $LOGARGS --stat --pretty='commit %h%n    %s' 2>/dev/null || true)"
[ -n "$STAT" ] || STAT="(no new commits since the previous pass)"

# Truncate: a large refactor's --stat can be thousands of lines, and this is
# pasted into a prompt.
if [ "$(printf '%s' "$STAT" | wc -c)" -gt "$MAXLEN" ]; then
  STAT="$(printf '%s' "$STAT" | cut -c1-$MAXLEN)
… (truncated)"
fi

if printf '%s' "$CHANGED" | grep -qE '(focus-areas|paired-reviews|gaps-identified)\.md$'; then
  TRACKING=true
else
  TRACKING=false
fi

# Did anything OUTSIDE the tracking files change? That is real work.
if printf '%s' "$CHANGED" | grep -qvE '^(RALPH\.md|docs/reference/(focus-areas|paired-reviews|gaps-identified)\.md)$'; then
  CODE_CHANGED=true
else
  CODE_CHANGED=false
fi
[ -n "$CHANGED" ] || CODE_CHANGED=false

PLAN="$WORK/docs/reference/focus-areas.md"
PAIRPLAN="$WORK/docs/reference/paired-reviews.md"
GAPS="$WORK/docs/reference/gaps-identified.md"
BOXSTATE="$WS/last-box-counts"
NOW_BOXES=$(( $(count_ticked "$PLAN") + $(count_ticked "$PAIRPLAN") ))
NOW_GAPS="$(count_ticked "$GAPS")"
PREV_BOXES=0; PREV_GAPS=0
if [ -f "$BOXSTATE" ]; then
  PREV_BOXES="$(cut -d' ' -f1 "$BOXSTATE" 2>/dev/null || echo 0)"
  PREV_GAPS="$(cut -d' ' -f2 "$BOXSTATE" 2>/dev/null || echo 0)"
fi
: "${PREV_BOXES:=0}" "${PREV_GAPS:=0}"
TICKED=$((NOW_BOXES - PREV_BOXES)); [ "$TICKED" -gt 0 ] || TICKED=0
CLOSED=$((NOW_GAPS - PREV_GAPS));   [ "$CLOSED" -gt 0 ] || CLOSED=0
printf '%s %s' "$NOW_BOXES" "$NOW_GAPS" > "$BOXSTATE"

# --- second axis: are approvals actually accumulating? -----------------------
# Tuned against observed healthy behaviour: a productive run interleaves ticks
# with withholds every few passes (one observed healthy window: 12 ticked / 6
# withheld over 12h, never more than ~4 in a row), so 8 consecutive approval-less
# passes is well outside normal variance and not a stretch spent fixing one
# genuine regression.
APPROVAL_STALL_LIMIT="${APPROVAL_STALL_LIMIT:-8}"
STALLSTATE="$WS/passes-since-approval"
PSA=0
[ -f "$STALLSTATE" ] && PSA="$(cat "$STALLSTATE" 2>/dev/null || echo 0)"
case "$PSA" in ''|*[!0-9]*) PSA=0 ;; esac
if [ "$TICKED" -gt 0 ]; then
  PSA=0            # an approval landed: the loop is converging again
else
  PSA=$((PSA + 1))
fi
printf '%s' "$PSA" > "$STALLSTATE"
if [ "$PSA" -ge "$APPROVAL_STALL_LIMIT" ]; then
  STALLED=true
else
  STALLED=false
fi

if [ "$COUNT" -eq 0 ]; then
  EVIDENCE=false
  SUMMARY="NO new commits this pass — nothing was committed since the previous pass"
elif [ "$STALLED" = true ]; then
  EVIDENCE=false
  SUMMARY="NO PROGRESS (approval stall): $PSA consecutive passes have ticked no approval box, despite this pass committing work (code changed=$CODE_CHANGED, gaps closed=$CLOSED). The loop is busy but not converging — it is fixing a receding target. Score it as churn so the backstop can escalate; this clears only when an approval actually lands."
elif [ "$CODE_CHANGED" = true ] || [ "$TICKED" -gt 0 ] || [ "$CLOSED" -gt 0 ]; then
  EVIDENCE=true
  SUMMARY="$COUNT new commit(s): code changed=$CODE_CHANGED, approval boxes newly ticked=$TICKED, gap items newly closed=$CLOSED (passes since last approval: $PSA)"
else
  EVIDENCE=false
  SUMMARY="NO PROGRESS: $COUNT commit(s) this pass touched ONLY the tracking files, ticked no approval box ($NOW_BOXES total, unchanged) and closed no gap. This is bookkeeping, not advancement — score it as churn so the stagnation backstop can escalate."
fi

printf '%s' "$HEAD" > "$STATE"

jq -n --argjson n "$COUNT" --argjson t "$TRACKING" --argjson e "$EVIDENCE" \
      --argjson cc "$CODE_CHANGED" --argjson bt "$TICKED" --argjson gc "$CLOSED" \
      --argjson psa "$PSA" --argjson st "$STALLED" \
      --arg h "$HEAD" --arg c "$STAT" --arg s "$SUMMARY" \
  '{new_commit_count:$n, tracking_touched:$t, progress_evidence:$e,
    code_changed:$cc, boxes_ticked:$bt, gaps_closed:$gc,
    passes_since_approval:$psa, approval_stalled:$st,
    head_sha:$h, commit_stat:$c, summary:$s}'
