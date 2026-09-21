#!/bin/sh
# Verifies the seeded plan BEFORE the refinement loop consumes it. Two checks,
# both deterministic, both fatal — a grep and a count will not pass on a retry,
# so this halts rather than retrying.
#
# CHECK 1 — the loop protocol, PER ROLE.
# seed-ralph once specified the charter content but not how a pass ENDS. The
# agent filled the vacuum with a protocol that had no terminator, and the loop
# ran with no way to tell progress from churn. That cost a whole run.
#
# Now there are THREE roles in one RALPH.md (single review 1, single review 2,
# paired), and the failure mode is worse: a file can carry a perfect protocol for
# one role and silently omit the self-assessment or stop rule for another. The
# missing one then surfaces sixty passes later as a timeout. So the markers are
# checked INSIDE EACH ROLE BLOCK, not merely somewhere in the file.
#
# CHECK 2 — the plan must fit the pass budget.
# seed-ralph has always reported the implied minimum pass count and nothing ever
# compared it to anything. Two runs shipped plans of 248 and 202 against a 500
# cap whose real throughput is ~0.38 boxes/pass; both ran for days and capped out
# mid-review. Paired rows now batch, so they are charged at a third of a pass.
#
# Reads {work_dir, max_ralph_iterations} from stdin JSON.
# Emits {ok, roles, singles, paired, implied_passes, budget}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || { echo "verify-ralph-seed: work_dir not set" >&2; exit 1; }
CAP="$(printf '%s' "$IN" | jq -r '.max_ralph_iterations // 500')"
case "$CAP" in ''|*[!0-9]*) CAP=500 ;; esac

RALPH="$WORK/RALPH.md"
PLAN="$WORK/docs/reference/focus-areas.md"
PAIRPLAN="$WORK/docs/reference/paired-reviews.md"

[ -f "$RALPH" ] || { echo "verify-ralph-seed: $RALPH does not exist — seed-ralph did not write it" >&2; exit 1; }

TMP="${TMPDIR:-/tmp}/vrs.$$"; mkdir -p "$TMP"; trap 'rm -rf "$TMP"' EXIT

# The three role blocks. Each must be present and each must carry its own
# terminators; a shared Wrap Up "somewhere in the file" is exactly the ambiguity
# that let a role lose its stop rule last time.
ROLES="single-review-1 single-review-2 paired-review"
missing=""; present=""

for role in $ROLES; do
  # Block runs from this role's heading to the next role heading (or EOF).
  awk -v r="$role" '
    BEGIN{ IGNORECASE=1 }
    tolower($0) ~ ("^#+[[:space:]]*role:[[:space:]]*" r "([^a-z0-9]|$)") { f=1; print; next }
    f && tolower($0) ~ /^#+[[:space:]]*role:/ { f=0 }
    f { print }' "$RALPH" > "$TMP/$role" 2>/dev/null || : > "$TMP/$role"

  if [ ! -s "$TMP/$role" ]; then
    missing="$missing $role(no-block)"
    continue
  fi
  for chk in 'finit:<promise>FINIT</promise>:-F' 'closer:<promise>CLOSER</promise>:-F' \
             'wrapup:Wrap Up:-i' 'stoptag:Stop after the tag:-i' \
             'thenstop:then stop:-i' 'towrapup:proceed to Wrap Up:-i'; do
    lbl="${chk%%:*}"; rest="${chk#*:}"; pat="${rest%:*}"; flg="${rest##*:}"
    if grep $flg -q -- "$pat" "$TMP/$role"; then
      present="$present $role/$lbl"
    else
      missing="$missing $role/$lbl"
    fi
  done
done

if [ -n "$missing" ]; then
  echo "verify-ralph-seed: RALPH.md is missing loop-protocol elements:$missing" >&2
  echo "  present:$present" >&2
  echo "  Each of the three roles needs its OWN Wrap Up, self-assessment and" >&2
  echo "  terminating promise tag. A role without a stop rule makes that half of" >&2
  echo "  the loop unbounded: its passes never end deliberately and the gate" >&2
  echo "  cannot tell progress from churn. Re-seed with the verbatim per-role" >&2
  echo "  protocol blocks from templates/seed-ralph.md." >&2
  exit 1
fi

count_rows() { # file -> row count
  [ -f "$1" ] || { echo 0; return; }
  awk '{ if ($0 !~ /^[[:space:]]*\|/) next
         if ($0 !~ /\[[ xX]\]/) next
         if ($0 !~ /\|[[:space:]]*[0-9]+[[:space:]]*\|/) next
         n++ } END { printf "%d", n+0 }' "$1"
}
SINGLES="$(count_rows "$PLAN")"
PAIRED="$(count_rows "$PAIRPLAN")"

# Paired rows batch up to four per pass; charged at three to stay conservative.
PAIRED_PASSES=$(( (PAIRED + 2) / 3 ))
IMPLIED=$(( 2 * SINGLES + PAIRED_PASSES ))
BUDGET=$(( CAP * 35 / 100 ))

if [ "$SINGLES" -eq 0 ] && [ "$PAIRED" -eq 0 ]; then
  echo "verify-ralph-seed: no review rows found in $PLAN or $PAIRPLAN — the plan is empty or unparseable." >&2
  exit 1
fi

if [ "$IMPLIED" -gt "$BUDGET" ]; then
  echo "verify-ralph-seed: the seeded plan CANNOT FINISH in its pass budget." >&2
  echo "  $SINGLES single-system areas x2 + $PAIRED paired rows batched at 3 ($PAIRED_PASSES passes)" >&2
  echo "  = $IMPLIED passes MINIMUM with zero rework, against a budget of $BUDGET" >&2
  echo "  (35% of max_ralph_iterations=$CAP)." >&2
  echo "  Observed throughput is ~0.38 approval boxes per pass, so the real cost is" >&2
  echo "  roughly 2.6x the minimum. A plan this size runs for days and still caps out" >&2
  echo "  mid-review — one such run reached 13% of its plan in 50 hours." >&2
  echo "  Re-seed with the paired table pruned to the highest-risk seams; move the" >&2
  echo "  rest to '## No seam' marked DEFERRED (budget). Do NOT shrink the area list." >&2
  exit 1
fi

jq -n --arg p "${present# }" --argjson s "$SINGLES" --argjson pr "$PAIRED" \
      --argjson im "$IMPLIED" --argjson b "$BUDGET" \
  '{ok:true, roles:($p|split(" ")), singles:$s, paired:$pr,
    implied_passes:$im, budget:$b}'
