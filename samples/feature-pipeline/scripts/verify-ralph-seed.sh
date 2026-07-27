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
# Reads {work_dir} from stdin JSON. Emits {ok, present, missing} on success.
set -eu
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
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

jq -n --arg p "${present# }" '{ok:true, present:($p|split(" ")), missing:[]}'
