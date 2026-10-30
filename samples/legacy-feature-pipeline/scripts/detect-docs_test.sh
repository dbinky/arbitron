#!/bin/sh
# Tests for detect-docs.sh — the resume-mode doc detector.
#
# Runs the real script against synthetic docs trees (no network, no git).
# Covers all five scenario categories:
#   happy-path : spec + designs present, exact slug -> resume at "plans"
#   success    : the full ladder (none / spec / spec+designs / +plans)
#   failure    : docs directories absent -> degrades to "spec", does not crash
#   error      : work_dir missing -> non-zero exit
#   edge-case  : fuzzy slug match, ambiguity detection, unrelated slug
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SCRIPT_DIR/detect-docs.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fails=0
check() { # desc actual expected
  if [ "$2" = "$3" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1"
    echo "        expected: $3"
    echo "        actual:   $2"
    fails=$((fails + 1))
  fi
}

run() { # work_dir slug -> JSON on stdout
  printf '{"work_dir":"%s","slug":"%s"}' "$1" "$2" | sh "$SCRIPT"
}
field() { printf '%s' "$1" | jq -r "$2"; }

mkrepo() { # dir
  mkdir -p "$1/docs/specs" "$1/docs/designs" "$1/docs/plans"
}

# ---------------------------------------------------------------- happy path
H="$TMP/happy"; mkrepo "$H"
: > "$H/docs/specs/2026-07-25-push-notifications-spec.md"
for n in 1 2 3 4 5; do : > "$H/docs/designs/2026-07-25-push-notifications-design-phase-$n.md"; done

out="$(run "$H" push-notifications)"
check "happy: resumes at plans"          "$(field "$out" .resume_stage)" "plans"
check "happy: matches the slug"          "$(field "$out" .matched_slug)" "push-notifications"
check "happy: finds the spec path"       "$(field "$out" .spec_path)"    "docs/specs/2026-07-25-push-notifications-spec.md"
check "happy: counts 5 designs"          "$(field "$out" .design_count)" "5"
check "happy: not ambiguous"             "$(field "$out" .ambiguous)"    "false"

# ------------------------------------------------------------------- success
# The full ladder, walked one rung at a time in a single tree.
L="$TMP/ladder"; mkrepo "$L"
check "ladder: nothing found -> spec"    "$(field "$(run "$L" widgets)" .resume_stage)" "spec"

: > "$L/docs/specs/2026-01-01-widgets-spec.md"
check "ladder: spec only -> design"      "$(field "$(run "$L" widgets)" .resume_stage)" "design"

: > "$L/docs/designs/2026-01-01-widgets-design-phase-1.md"
check "ladder: spec+designs -> plans"    "$(field "$(run "$L" widgets)" .resume_stage)" "plans"

: > "$L/docs/plans/2026-01-01-widgets-implementation-phase-1-task-1.md"
check "ladder: +plans -> implementation" "$(field "$(run "$L" widgets)" .resume_stage)" "implementation"
check "ladder: counts the plan"          "$(field "$(run "$L" widgets)" .plan_count)"   "1"

# ------------------------------------------------------------------- failure
# A checkout with no docs/ at all must degrade to the full pipeline, not die:
# a resumed run against a repo that never had docs is a normal situation.
F="$TMP/nodocs"; mkdir -p "$F"
out="$(run "$F" anything 2>/dev/null)"
check "failure: absent docs dirs -> spec" "$(field "$out" .resume_stage)" "spec"
check "failure: no spec path"             "$(field "$out" .spec_path)"    ""

# --------------------------------------------------------------------- error
# work_dir is required; a missing one is a wiring bug and must fail loudly
# rather than silently reporting "nothing found" and re-running the pipeline.
if printf '{"slug":"x"}' | sh "$SCRIPT" >/dev/null 2>&1; then
  check "error: missing work_dir exits non-zero" "exit 0" "non-zero"
else
  check "error: missing work_dir exits non-zero" "non-zero" "non-zero"
fi

# ------------------------------------------------------------------ edge case
# 1. The whole reason this is fuzzy: the caller's slug carries an extra token.
out="$(run "$H" app-push-notifications)"
check "edge: fuzzy slug still matches"   "$(field "$out" .matched_slug)" "push-notifications"
check "edge: fuzzy slug -> plans"        "$(field "$out" .resume_stage)" "plans"
check "edge: fuzzy scores 1.0"           "$(field "$out" .best_score)"   "1"

# 2. An unrelated slug must NOT adopt someone else's docs.
out="$(run "$H" lora-training-quality)"
check "edge: unrelated slug -> spec"     "$(field "$out" .resume_stage)" "spec"
check "edge: unrelated slug unmatched"   "$(field "$out" .matched_slug)" ""

# 3. Two features sharing a generic token score close -> flag for the tiebreaker.
A="$TMP/ambig"; mkrepo "$A"
: > "$A/docs/specs/2026-01-01-email-notifications-spec.md"
: > "$A/docs/specs/2026-01-01-sms-notifications-spec.md"
out="$(run "$A" notifications)"
check "edge: near-tie is ambiguous"      "$(field "$out" .ambiguous)" "true"
check "edge: still offers candidates"    "$(printf '%s' "$out" | jq -r '.candidates|length >= 2')" "true"

# 4. Undated filenames (the older in-repo convention) must match too.
U="$TMP/undated"; mkrepo "$U"
: > "$U/docs/specs/swipe-match-spec.md"
check "edge: undated filename matches"   "$(field "$(run "$U" swipe-match)" .resume_stage)" "design"

# 5. safe_slug must never be empty: promoting matched_slug (which IS empty when
#    nothing matched) blanked the slug and produced a placeholder branch.
out="$(run "$H" totally-unrelated-thing)"
check "edge: matched_slug empty when no match" "$(field "$out" .matched_slug)" ""
check "edge: safe_slug falls back to caller"   "$(field "$out" .safe_slug)"    "totally-unrelated-thing"
out="$(run "$H" app-push-notifications)"
check "edge: safe_slug adopts the match"       "$(field "$out" .safe_slug)"    "push-notifications"

# 6. Nested plan sets must be visible; a maxdepth-1 scan reported none.
N="$TMP/nested"; mkrepo "$N"
: > "$N/docs/specs/2026-01-01-widgets-spec.md"
: > "$N/docs/designs/2026-01-01-widgets-design-phase-1.md"
mkdir -p "$N/docs/plans/widgets"
: > "$N/docs/plans/widgets/2026-01-01-widgets-implementation-phase-1-task-1.md"
out="$(run "$N" widgets)"
check "edge: nested plans are found"           "$(field "$out" .plan_count)"    "1"
check "edge: nested plans -> implementation"   "$(field "$out" .resume_stage)"  "implementation"

# ------------------------------------------------------------ safe_run_date
# Every template globs docs by {{run_date}}, which init-workspace sets to the day
# the run STARTS. A run resumed on a later day looked for plans dated today, found
# none, and reported "0 tasks remain" (PR #71: plans were 2026-09-29, the resume
# ran 2026-10-01). safe_run_date carries the existing docs' date instead.
run_dated() { # work_dir slug run_date -> JSON on stdout
  printf '{"work_dir":"%s","slug":"%s","run_date":"%s"}' "$1" "$2" "$3" | sh "$SCRIPT"
}

# happy: plans exist under an older date -> that date wins over today's.
R="$TMP/rundate"; mkrepo "$R"
: > "$R/docs/specs/2026-09-29-latest-model-profiles-spec.md"
: > "$R/docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md"
: > "$R/docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md"
out="$(run_dated "$R" latest-model-profiles 2026-10-01)"
check "run_date happy: adopts the plans' date"     "$(field "$out" .safe_run_date)" "2026-09-29"

# success: no plans yet -> the designs' date; spec only -> the spec's date.
check "run_date success: designs' date"            "$(field "$(run_dated "$H" push-notifications 2026-10-01)" .safe_run_date)" "2026-07-25"
S="$TMP/speconly"; mkrepo "$S"
: > "$S/docs/specs/2026-03-03-widgets-spec.md"
check "run_date success: spec's date"              "$(field "$(run_dated "$S" widgets 2026-10-01)" .safe_run_date)" "2026-03-03"

# failure: nothing matches -> keep the caller's run_date (a fresh-docs resume).
check "run_date failure: no match keeps caller's"  "$(field "$(run_dated "$H" totally-unrelated 2026-10-01)" .safe_run_date)" "2026-10-01"

# error: no match AND no caller run_date -> still a valid date, never empty
# (promoting "" would produce docs named "-<slug>-spec.md").
out="$(run "$H" totally-unrelated)"
check "run_date error: never empty" \
  "$(field "$out" '.safe_run_date | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}$")')" "true"

# edge: undated filenames carry no date -> caller's run_date.
check "run_date edge: undated keeps caller's"      "$(field "$(run_dated "$U" swipe-match 2026-10-01)" .safe_run_date)" "2026-10-01"

# edge: plans re-dated by a later run -> the newest plan date.
: > "$R/docs/plans/2026-09-30-latest-model-profiles-implementation-phase-2-task-1.md"
check "run_date edge: newest plan date wins"       "$(field "$(run_dated "$R" latest-model-profiles 2026-10-01)" .safe_run_date)" "2026-09-30"

# edge: nested plan sets are dated too.
check "run_date edge: nested plans' date"          "$(field "$(run_dated "$N" widgets 2026-10-01)" .safe_run_date)" "2026-01-01"

echo
if [ "$fails" -eq 0 ]; then
  echo "all detect-docs tests passed"
else
  echo "$fails detect-docs test(s) FAILED"
  exit 1
fi
