#!/bin/sh
# Audits this run's committed plans after an alignment pass, so the plan stage
# routes on facts a script read from the plan files and never on an agent's
# claim about them.
#
# WHY:
# - remote-runners, 2026-11-23: align-plans rewrote full TDD plans into prose
#   with no `- [ ]` steps, and align-plans-2 added tasks with no steps. The
#   implement agent picks the first task with an unticked box, so it skipped
#   those producer tasks and 47 tasks blocked on files nobody built.
# - lunaroute (implement runs a5259bec, c028678b, 8fc37529): the plans held
#   steps no agent can do (operator observations, live vendor evidence). The
#   implement loop stalled on them for hours instead of a person being asked
#   at plan time.
#
# Reads the run's variables from stdin JSON: work_dir, plan_dir, run_date.
# Plans are <plan_dir>/<run_date>-phase-<N>-task-<M>.md, read from the
# committed HEAD (implement reads HEAD too, so an uncommitted rewrite is not
# what it will see). Lines inside fenced code blocks are ignored.
#
# Emits:
#   plans            number — plans committed at HEAD
#   stepless         array  — plans with no checkbox (`- [ ]` / `- [x]`) of their own
#   uncommitted      array  — plan files on disk that HEAD does not hold
#   stepless_count   number — length of stepless
#   ok               bool   — plans > 0, nothing stepless or uncommitted, readable
#   human_only       number — unchecked steps that only a person can do
#   human_only_lines array  — "<path>:<line>: <step>" for each of them
#   gaps             array  — STEPLESS:/UNCOMMITTED:/NO-PLANS:/UNREADABLE: lines
#
# Fails CLOSED: no plans, an unreadable repository or a malformed run_date
# report ok=false. The human-only scan is a backstop for the deps-assess
# DECISION rule: it greps UNCHECKED steps only, so a step a person settled is
# moved out of the checkbox list (e.g. into an "Operator follow-up" section),
# which is also where it belongs: no implement agent can tick it.
set -eu
# The repository belongs to the agents whose work is audited: ignore its replace refs.
GIT_NO_REPLACE_OBJECTS=1
export GIT_NO_REPLACE_OBJECTS

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
DIR="$(printf '%s' "$IN" | jq -r '.plan_dir // ""')"
DATE="$(printf '%s' "$IN" | jq -r '.run_date // ""')"

fail() { # reason
  echo "plan-audit: $1" >&2
  jq -n --arg r "UNREADABLE: $1" '{plans:0, stepless:[], uncommitted:[], stepless_count:0, ok:false,
    human_only:0, human_only_lines:[], gaps:[$r]}'
  exit 0
}

[ -n "$WORK" ] && [ -n "$DIR" ] || fail "work_dir or plan_dir not set"
printf '%s' "$DATE" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || fail "run_date is not YYYY-MM-DD: $DATE"
git -C "$WORK" rev-parse --verify -q HEAD > /dev/null 2>&1 || fail "no commit at HEAD in $WORK"

NAME_RE="^$DATE-phase-[0-9]+-task-[0-9]+\\.md\$"
NL='
'

# Committed plans, sorted by phase then task numerically.
COMMITTED="$(git -C "$WORK" ls-tree -r --name-only HEAD -- "$DIR/" 2>/dev/null \
  | while IFS= read -r f; do
      b="$(basename "$f")"
      printf '%s' "$b" | grep -Eq "$NAME_RE" || continue
      [ "$(dirname "$f")" = "$DIR" ] || continue
      printf '%s\n' "$(printf '%s' "$b" | sed -E 's/^[0-9-]+-phase-([0-9]+)-task-([0-9]+)\.md$/\1 \2/') $f"
    done | sort -n -k1,1 -k2,2 | cut -d' ' -f3-)"

# Plan files on disk that HEAD does not hold.
uncommitted=""
if [ -d "$WORK/$DIR" ]; then
  for f in "$WORK/$DIR/$DATE"-phase-*-task-*.md; do
    [ -f "$f" ] || continue
    rel="$DIR/$(basename "$f")"
    printf '%s' "$(basename "$f")" | grep -Eq "$NAME_RE" || continue
    printf '%s\n' "$COMMITTED" | grep -Fxq "$rel" || uncommitted="$uncommitted$rel$NL"
  done
fi

# Phrases that mark a step only a person can do: operator observation or
# sign-off, manual checks, live vendor evidence, captured real-world fixtures,
# real credentials, vendor consoles.
HUMAN_RE='operator[- ]+(observ|verif|evidence|collect|accept|confirm|sign)'
HUMAN_RE="$HUMAN_RE|manual(ly)?[- ]+(verif|observ|confirm|accept|inspect|collect|check)"
HUMAN_RE="$HUMAN_RE|live[- ]+([a-z]+[- ]+){0,2}(evidence|observation|acceptance|verification)"
HUMAN_RE="$HUMAN_RE|captured[- ]+(from|in)[- ]+production|captured[- ]+(real|live|production)|production[- ]captured|real[- ]world[- ]fixture"
HUMAN_RE="$HUMAN_RE|(production|live|vendor|real)[- ]+(api[- ]+)?(credential|api[- ]key|secret)"
HUMAN_RE="$HUMAN_RE|(vendor|provider)[- ]+(dashboard|console|portal)"

plans=0
stepless=""
human=""
while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  plans=$((plans + 1))
  body="$(git -C "$WORK" show "HEAD:$rel")"
  boxes="$(printf '%s\n' "$body" | awk '
    /^[ \t]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    /^[ \t]*[-*][ \t]+\[[ xX]\]/ { n++ }
    END { print n + 0 }')"
  if [ "$boxes" -eq 0 ]; then
    echo "plan-audit: plan has no checkbox steps: $rel" >&2
    stepless="$stepless$rel$NL"
  fi
  # "<line>:<step>" for unchecked steps outside fences, then the phrase match.
  hits="$(printf '%s\n' "$body" | awk '
    /^[ \t]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    /^[ \t]*[-*][ \t]+\[ \]/ { s = $0; sub(/^[ \t]+/, "", s); print NR ":" s }' \
    | grep -Ei -- "$HUMAN_RE" || true)"
  if [ -n "$hits" ]; then
    human="$human$(printf '%s\n' "$hits" | sed "s|^|$rel:|; s|^\\([^:]*:[0-9]*\\):|\\1: |" | cut -c1-240)$NL"
  fi
done <<EOP
$COMMITTED
EOP

lines() { printf '%s' "$1" | jq -Rs 'split("\n") | map(select(length > 0))'; }
jq -n --argjson plans "$plans" --argjson stepless "$(lines "$stepless")" \
  --argjson uncommitted "$(lines "$uncommitted")" --argjson human "$(lines "$human")" '
  ([$stepless[] | "STEPLESS: \(.) has no checkbox steps"]
   + [$uncommitted[] | "UNCOMMITTED: \(.) is not committed at HEAD"]
   + (if $plans == 0 then ["NO-PLANS: no committed plan matches the run-dated layout"] else [] end)) as $gaps
  | {plans:$plans, stepless:$stepless, uncommitted:$uncommitted,
     stepless_count:($stepless | length),
     ok:($plans > 0 and ($stepless | length) == 0 and ($uncommitted | length) == 0),
     human_only:($human | length), human_only_lines:$human, gaps:$gaps}'
