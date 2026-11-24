#!/bin/sh
# Counts the plan checkboxes that say whether implementation is finished, so
# impl-gate routes on arithmetic and never on an LLM's claim of completion.
#
# Reads the run's variables from stdin JSON: work_dir and
# resolve-inputs.inputs.plan_paths (an array of repo-relative paths; an absolute
# path must lie inside work_dir). Plans are read from the committed HEAD
# (`git show HEAD:<path>`), never the working tree: completion must describe the
# same commit the tamper gate judged, the checkpoint pushed and the manifest
# records, so ticked boxes an agent left uncommitted do not count. Counts `- [ ]` (unchecked) and `- [x]` (checked)
# list items across every plan. Lines inside fenced code blocks are ignored, so
# an example checkbox in a plan's prose cannot hold the loop open.
#
# Emits:
#   unchecked_steps   number — unticked boxes across all readable plans
#   checked_steps     number — ticked boxes
#   all_tasks_complete bool  — unchecked == 0 AND checked > 0 AND nothing unreadable
#                              AND no plan is stepless
#   tasks_remaining   number — same count as unchecked_steps
#   unreadable        array  — plan paths that could not be read at HEAD
#   no_checkboxes     bool   — readable plans held no checkboxes at all
#   stepless          array  — readable plans that hold no checkbox of their own
#
# Fails CLOSED: a missing or uncommitted plan, an empty plan list, or plans with
# no checkboxes are never "complete" (nothing proves the work was done). That
# holds per plan, not only in aggregate: the impl agent picks the first task
# with an unticked box, so a plan whose steps were rewritten as prose would be
# skipped as if finished while every later task waited on what it builds
# (remote-runners, 2026-11-23: seven stepless producer plans blocked 47 tasks).
set -eu
# The repository belongs to the agent whose work is counted: ignore its replace refs.
GIT_NO_REPLACE_OBJECTS=1
export GIT_NO_REPLACE_OBJECTS
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // empty')"
PATHS="$(printf '%s' "$IN" | jq -r '(.["resolve-inputs"].inputs.plan_paths // []) | if type=="array" then .[] | strings else empty end')"

unchecked=0
checked=0
unreadable=""
stepless=""
NL='
'
while IFS= read -r p; do
  [ -n "$p" ] || continue
  rel="$p"
  case "$p" in
    "$WORK"/*) rel="${p#"$WORK"/}" ;;
    /*) rel="" ;;
  esac
  if [ -z "$rel" ] || [ "$(git -C "$WORK" cat-file -t "HEAD:$rel" 2>/dev/null)" != blob ]; then
    echo "count-plan-steps: plan is not committed at HEAD: $p" >&2
    unreadable="$unreadable$p$NL"
    continue
  fi
  counts="$(git -C "$WORK" show "HEAD:$rel" | awk '
    /^[ \t]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    /^[ \t]*[-*][ \t]+\[ \]/ { u++ }
    /^[ \t]*[-*][ \t]+\[[xX]\]/ { c++ }
    END { printf "%d %d", u, c }')"
  unchecked=$((unchecked + ${counts% *}))
  checked=$((checked + ${counts#* }))
  if [ "$counts" = "0 0" ]; then
    echo "count-plan-steps: plan has no checkbox steps: $p" >&2
    stepless="$stepless$p$NL"
  fi
done <<EOP
$PATHS
EOP

lines() { printf '%s' "$1" | jq -Rs 'split("\n") | map(select(length > 0))'; }
jq -n --argjson u "$unchecked" --argjson c "$checked" \
  --argjson bad "$(lines "$unreadable")" --argjson stepless "$(lines "$stepless")" '
  {unchecked_steps:$u, checked_steps:$c,
   all_tasks_complete: ($u == 0 and $c > 0 and ($bad | length) == 0 and ($stepless | length) == 0),
   tasks_remaining:$u, unreadable:$bad, no_checkboxes: (($u + $c) == 0), stepless:$stepless}'
