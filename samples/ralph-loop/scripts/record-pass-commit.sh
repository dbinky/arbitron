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
#   progress_evidence bool    — new_commit_count > 0; the gate's hard signal
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
      head_sha:"", commit_stat:"(no commit evidence available)", summary:$s}'
}

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

if printf '%s' "$CHANGED" | grep -qE '(focus-areas|gaps-identified)\.md$'; then
  TRACKING=true
else
  TRACKING=false
fi

if [ "$COUNT" -gt 0 ]; then
  EVIDENCE=true
  SUMMARY="$COUNT new commit(s) this pass; tracking files touched: $TRACKING"
else
  EVIDENCE=false
  SUMMARY="NO new commits this pass — nothing was committed since the previous pass"
fi

printf '%s' "$HEAD" > "$STATE"

jq -n --argjson n "$COUNT" --argjson t "$TRACKING" --argjson e "$EVIDENCE" \
      --arg h "$HEAD" --arg c "$STAT" --arg s "$SUMMARY" \
  '{new_commit_count:$n, tracking_touched:$t, progress_evidence:$e,
    head_sha:$h, commit_stat:$c, summary:$s}'
