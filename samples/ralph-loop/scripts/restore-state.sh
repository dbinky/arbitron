#!/bin/sh
# Rebuilds a resumed run's loop state from what the branch already carries, so a
# run interrupted by a quota wall, a crash or a cancel picks up exactly where it
# stopped instead of starting the review over.
#
# WHY THIS EXISTS: everything the loop needs to continue is already committed —
# the two control files hold which reviews are approved, gaps-identified.md holds
# what was found, and the branch history holds the work. The only things living
# outside git were the per-workspace counters, and a fresh instance has none of
# them. This restores those, and nothing else: the plan itself is NOT re-seeded,
# so earned approvals survive.
#
# The baseline is the subtle part. It measures the BRANCH POINT. Re-running
# baseline-tests on a partly-refined branch would record the previous run's own
# fixes as "pre-existing" and permanently blind the regression gate to anything
# it had already broken — so the committed copy is restored verbatim instead.
#
# Reads {workspace, work_dir} from stdin JSON. Emits:
#   restored          bool   — a committed baseline was found and restored
#   baseline_source   string — committed | missing
#   ticked_boxes      number — approvals already earned (preserved, not reset)
#   open_gaps         number
#   head_sha          string
#   summary           string
#
# Never fails the run: a missing committed baseline emits restored=false and a
# loud summary, leaving record-baseline to fall back to its empty-baseline path.
set -eu
IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // empty')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || WORK="$WS/work"

PLAN="$WORK/docs/reference/focus-areas.md"
PAIRPLAN="$WORK/docs/reference/paired-reviews.md"
GAPS="$WORK/docs/reference/gaps-identified.md"
COMMITTED="$WORK/docs/reference/test-baseline.json"

count() { [ -f "$1" ] && grep -o "$2" "$1" 2>/dev/null | wc -l | tr -d ' ' || echo 0; }
TICKED=$(( $(count "$PLAN" '\[[xX]\]') + $(count "$PAIRPLAN" '\[[xX]\]') ))
OPEN_GAPS=$(count "$GAPS" '\[ \]')
HEAD="$(git -C "$WORK" rev-parse HEAD 2>/dev/null || echo '')"

# 1. Baseline: restore the committed branch-point measurement into the workspace
#    where record-baseline expects to find it.
RESTORED=false; SRC=missing
if [ -f "$COMMITTED" ] && jq -e 'type=="object" and (.suites|type=="array")' "$COMMITTED" >/dev/null 2>&1; then
  cp "$COMMITTED" "$WS/test-baseline.json"
  RESTORED=true; SRC=committed
fi

# 2. Progress counters. Seeding last-box-counts with the CURRENT totals is what
#    stops the first resumed pass reporting every previously-earned approval as
#    fresh progress; seeding last-pass-head with HEAD stops it counting the whole
#    prior history as this pass's commits.
printf '%s %s' "$TICKED" "$(count "$GAPS" '\[[xX]\]')" > "$WS/last-box-counts"
printf '0' > "$WS/passes-since-approval"
[ -n "$HEAD" ] && printf '%s' "$HEAD" > "$WS/last-pass-head"

# 3. Router state. Cleared, not carried: the first resumed pass has no
#    predecessor, so it must not look like the fourth consecutive attempt on a
#    target and trip the escalation guard before doing any work.
rm -f "$WS/last-target" "$WS/verify-sweep-counter"

if [ "$RESTORED" = true ]; then
  SUM="RESUMED: $TICKED approval(s) already earned are preserved, $OPEN_GAPS gap(s) still open, and the committed branch-point baseline was restored. The review plan was NOT re-seeded — the loop continues from the next unapproved review."
else
  SUM="RESUMED WITHOUT A BASELINE: $TICKED approval(s) preserved, but no committed test-baseline.json was found at $COMMITTED. The regression gate will fall back to an EMPTY baseline, so every currently-failing test will look NEW and approvals may be blocked. Re-run baseline-tests deliberately, or accept that this run cannot distinguish pre-existing failures."
fi

jq -n --argjson r "$RESTORED" --arg src "$SRC" --argjson t "$TICKED" \
      --argjson g "$OPEN_GAPS" --arg h "$HEAD" --arg s "$SUM" \
  '{restored:$r, baseline_source:$src, ticked_boxes:$t, open_gaps:$g, head_sha:$h, summary:$s}'
