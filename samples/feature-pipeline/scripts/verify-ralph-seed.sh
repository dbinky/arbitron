#!/bin/sh
# Verifies the seeded (and critiqued) plan BEFORE the refinement loop consumes
# it. Every check is deterministic and fatal — a grep and a count will not pass
# on a retry, so this halts rather than retrying.
#
# CHECK 1 — the loop protocol, PER ROLE.
# seed-ralph once specified the charter content but not how a pass ENDS. The
# agent filled the vacuum with a protocol that had no terminator, and the loop
# ran with no way to tell progress from churn. That cost a whole run.
#
# There are THREE roles in one RALPH.md (single review 1 = wiring lens, single
# review 2 = failure lens, paired), and the failure mode is worse: a file can
# carry a perfect protocol for one role and silently omit the self-assessment or
# stop rule for another. The missing one then surfaces sixty passes later as a
# timeout. So the markers are checked INSIDE EACH ROLE BLOCK, not merely
# somewhere in the file.
#
# CHECK 2 — the plan must fit the pass budget.
# 2*singles + paired <= 35% of the cap. Paired rows are charged at ONE pass each.
# The previous charge of a third of a pass assumed rows batch four at a time;
# measured reality was 96 rows in 66 passes with batching in 7 of them, and that
# charge let a 116-row plan into the loop as 39 passes. Two earlier runs shipped
# plans of 248 and 202 against a 500 cap whose real throughput is ~0.38
# boxes/pass; both ran for days and capped out mid-review.
#
# CHECK 3 — plan shape.
# A paired row is ONE seam, "A ↔ B", approved in both directions at once. A
# directional "A as impacted by B" row means the reverse-row format is back and
# the paired table is twice its real size (92 of one plan's rows were mirror
# pairs). Checkbox syntax may appear only inside the two tables:
# record-pass-commit counts [x] file-wide, so a checklist in a detail block
# corrupts the approval arithmetic.
#
# CHECK 4 — the critic's record.
# When a critic ran (critique-seed on a fresh run, critique-replan on a resumed
# run with replan=true; its output is in the variable snapshot), RALPH.md must
# carry a "## Plan critique" section. A critic that returned without writing it
# did not do the job, and the plan it waved through is the seeder's own slicing.
# Older snapshots that have no critic node are not held to this.
#
# Reads the variable snapshot on stdin; uses {work_dir, max_ralph_iterations,
# critique-seed}. Emits {ok, roles, singles, paired, implied_passes, budget,
# critiqued}.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
[ -n "$WORK" ] || { echo "verify-ralph-seed: work_dir not set" >&2; exit 1; }
CAP="$(printf '%s' "$IN" | jq -r '.max_ralph_iterations // 500')"
case "$CAP" in ''|*[!0-9]*) CAP=500 ;; esac
# Either critic: critique-seed on the fresh path, critique-replan on a resumed
# run re-planned in flight (replan=true). Both must leave the record.
CRITIC_RAN="$(printf '%s' "$IN" | jq -r 'if has("critique-seed") or has("critique-replan") then "yes" else "no" end')"

RALPH="$WORK/RALPH.md"
PLAN="$WORK/docs/reference/focus-areas.md"
PAIRPLAN="$WORK/docs/reference/paired-reviews.md"

[ -f "$RALPH" ] || { echo "verify-ralph-seed: $RALPH does not exist — seed-ralph did not write it" >&2; exit 1; }

TMP="${TMPDIR:-/tmp}/vrs.$$"; mkdir -p "$TMP"; trap 'rm -rf "$TMP"' EXIT

# --- CHECK 1: the three role blocks --------------------------------------------
# Each must be present and each must carry its own terminators; a shared Wrap Up
# "somewhere in the file" is exactly the ambiguity that let a role lose its stop
# rule last time.
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
  echo "  terminating promise tag. A role without a stop rule makes that third of" >&2
  echo "  the loop unbounded: its passes never end deliberately and the gate" >&2
  echo "  cannot tell progress from churn. Re-seed with the verbatim per-role" >&2
  echo "  protocol blocks from templates/seed-ralph.md." >&2
  exit 1
fi

# --- CHECK 2: the budget ---------------------------------------------------------
count_rows() { # file -> row count (table rows carrying a box and a numeric #)
  [ -f "$1" ] || { echo 0; return; }
  awk '{ if ($0 !~ /^[[:space:]]*\|/) next
         if ($0 !~ /\[[ xX]\]/) next
         if ($0 !~ /\|[[:space:]]*[0-9]+[[:space:]]*\|/) next
         n++ } END { printf "%d", n+0 }' "$1"
}
SINGLES="$(count_rows "$PLAN")"
PAIRED="$(count_rows "$PAIRPLAN")"

IMPLIED=$(( 2 * SINGLES + PAIRED ))
BUDGET=$(( CAP * 35 / 100 ))

if [ "$SINGLES" -eq 0 ] && [ "$PAIRED" -eq 0 ]; then
  echo "verify-ralph-seed: no review rows found in $PLAN or $PAIRPLAN — the plan is empty or unparseable." >&2
  exit 1
fi

if [ "$IMPLIED" -gt "$BUDGET" ]; then
  echo "verify-ralph-seed: the seeded plan CANNOT FINISH in its pass budget." >&2
  echo "  $SINGLES single-system areas x2 + $PAIRED paired seams x1" >&2
  echo "  = $IMPLIED passes MINIMUM with zero rework, against a budget of $BUDGET" >&2
  echo "  (35% of max_ralph_iterations=$CAP)." >&2
  echo "  Paired rows cost a whole pass each: 96 rows took 66 passes and batched in 7." >&2
  echo "  Observed throughput is ~0.38 approval boxes per pass, so the real cost is" >&2
  echo "  roughly 2.6x the minimum. A plan this size runs for days and still caps out" >&2
  echo "  mid-review — one such run reached 13% of its plan in 50 hours." >&2
  echo "  Areas are BEHAVIOURS (entry to sink), not layers or files: apply the merge" >&2
  echo "  test in templates/seed-ralph.md, keep only seams that pass the seam test," >&2
  echo "  and defer the lowest-risk seams as DEFERRED (budget) under '## No seam'." >&2
  exit 1
fi

# --- CHECK 3: plan shape ---------------------------------------------------------
DIRECTIONAL="$(awk 'tolower($0) ~ /^[[:space:]]*\|.*\[[ xX]\]/ && tolower($0) ~ / as impacted by / { n++ } END { printf "%d", n+0 }' "$PAIRPLAN" 2>/dev/null || echo 0)"
if [ "$DIRECTIONAL" -gt 0 ]; then
  echo "verify-ralph-seed: $DIRECTIONAL paired row(s) use the directional 'A as impacted by B' format." >&2
  echo "  A paired row is ONE seam, written 'A ↔ B', and its single approval covers" >&2
  echo "  both directions. Directional rows double the table (92 of one plan's rows" >&2
  echo "  were mirror pairs) and each mirror costs a whole pass. Merge every pair of" >&2
  echo "  mirrors into one row whose Seam cell states SHARED, TRACE and BREAK." >&2
  exit 1
fi

STRAY="$(cat "$PLAN" "$PAIRPLAN" 2>/dev/null | awk '/\[[ xX]\]/ && $0 !~ /^[[:space:]]*\|/ { n++ } END { printf "%d", n+0 }')"
if [ "$STRAY" -gt 0 ]; then
  echo "verify-ralph-seed: $STRAY line(s) carry checkbox syntax outside a table row in focus-areas.md / paired-reviews.md." >&2
  echo "  [ ] and [x] are counted file-wide by record-pass-commit, so a checklist in" >&2
  echo "  an area's detail block corrupts the approval arithmetic. Use numbered lists" >&2
  echo "  in the detail blocks; boxes live only in the two tables." >&2
  exit 1
fi

# --- CHECK 4: the critic's record ------------------------------------------------
CRITIQUED=false
if [ "$CRITIC_RAN" = yes ]; then
  if ! grep -qiE '^#+[[:space:]]*Plan critique' "$RALPH"; then
    echo "verify-ralph-seed: critique-seed ran but RALPH.md carries no '## Plan critique' section." >&2
    echo "  The critic must record what it merged, struck, added and left alone, so a" >&2
    echo "  clean plan can be told from an unchecked one. A critic that wrote nothing" >&2
    echo "  did not apply the merge test or the seam test; the plan is the seeder's" >&2
    echo "  unreviewed slicing. Re-run the critic (templates/critique-seed.md)." >&2
    exit 1
  fi
  CRITIQUED=true
fi

jq -n --arg p "${present# }" --argjson s "$SINGLES" --argjson pr "$PAIRED" \
      --argjson im "$IMPLIED" --argjson b "$BUDGET" --argjson c "$CRITIQUED" \
  '{ok:true, roles:($p|split(" ")), singles:$s, paired:$pr,
    implied_passes:$im, budget:$b, critiqued:$c}'
