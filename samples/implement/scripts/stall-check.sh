#!/bin/sh
# Stops the implement loop when it stops making progress. impl-gate routes on
# `stalled` before any LLM-claimed exit: a pass that commits a "partial slice"
# without ticking a box would otherwise loop to the iteration cap (lunaroute,
# 2026-11: 39 passes over 12.7h with unchecked_steps frozen at 16, and the
# assessor never said "all remaining blocked").
#
# Usage: stall-check.sh [window]   (window K, a positive integer, default 3)
#
# Reads the run's variables from stdin JSON: workspace, work_dir,
# resolve-inputs.inputs.plan_paths, count-plan-steps.{unchecked_steps,
# checked_steps}, and on the first pass plan-preflight.{unchecked_steps,
# checked_steps} as the starting point.
#
# Progress is fewer unchecked steps or more checked steps than the best seen so
# far, so a tick lost to a voided pass and regained later is not new progress.
# The history lives in <workspace>/impl-progress.json. `stalled` is true once K
# consecutive passes made no progress.
#
# blocked_tasks lists the `> BLOCKED` notes in the plans committed at HEAD (as
# count-plan-steps reads them) that sit under an unticked step and are not
# followed by a `> RESOLVED` note before the next step or heading. Notes inside
# fenced code blocks are ignored. Each entry is {plan, task, step, note};
# blocked_summary is the same list as one line for the stop reason.
#
# Fails SAFE: a missing, corrupt or misshapen history restarts the count, and
# missing counts or an unwritable workspace never report a stall.
set -eu
GIT_NO_REPLACE_OBJECTS=1
export GIT_NO_REPLACE_OBJECTS

K="${1:-}"
[ -n "$K" ] || K=3
case "$K" in
  *[!0-9]*) echo "stall-check: window must be a positive integer, got: $K" >&2; exit 2 ;;
esac
[ "$K" -gt 0 ] || { echo "stall-check: window must be a positive integer, got: $K" >&2; exit 2; }

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
STATE=""
[ -z "$WS" ] || STATE="$WS/impl-progress.json"

# --- Blocked notes at HEAD
TAB="$(printf '\t')"
blocked="[]"
PATHS="$(printf '%s' "$IN" | jq -r '(.["resolve-inputs"].inputs.plan_paths // []) | if type=="array" then .[] | strings else empty end')"
while IFS= read -r p; do
  [ -n "$p" ] || continue
  rel="$p"
  case "$p" in
    "$WORK"/*) rel="${p#"$WORK"/}" ;;
    /*) continue ;;
  esac
  [ -n "$WORK" ] && [ "$(git -C "$WORK" cat-file -t "HEAD:$rel" 2>/dev/null)" = blob ] || continue
  rows="$(git -C "$WORK" show "HEAD:$rel" | awk '
    function trim(s) { gsub(/\t/, " ", s); sub(/^[ ]+/, "", s); sub(/[ ]+$/, "", s); return s }
    function flush() { if (pending != "" && !checked) print task "\t" step "\t" pending; pending = "" }
    /^[ \t]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    /^#+[ \t]/ { flush(); t = $0; sub(/^#+[ \t]+/, "", t); task = trim(t); step = ""; checked = 0; next }
    /^[ \t]*[-*][ \t]+\[[ xX]\]/ {
      flush()
      s = $0; checked = (s ~ /^[ \t]*[-*][ \t]+\[[xX]\]/)
      sub(/^[ \t]*[-*][ \t]+\[[ xX]\][ \t]*/, "", s); step = trim(s); next
    }
    /^[ \t]*>[ \t]*(\*\*)?BLOCKED/ {
      flush()
      n = $0; sub(/^[ \t]*>[ \t]*(\*\*)?BLOCKED(\*\*)?[ \t]*:?(\*\*)?[ \t]*/, "", n)
      pending = trim(n); if (pending == "") pending = "(no reason given)"; next
    }
    /^[ \t]*>[ \t]*(\*\*)?RESOLVED/ { pending = ""; next }
    END { flush() }')"
  [ -n "$rows" ] || continue
  blocked="$(printf '%s\n' "$rows" | jq -R --arg plan "$p" --argjson acc "$blocked" -s '
    $acc + (split("\n") | map(select(length > 0) | split("'"$TAB"'")
      | {plan: $plan, task: .[0], step: .[1], note: .[2]}))')"
done <<EOP
$PATHS
EOP

# --- Progress history
U="$(printf '%s' "$IN" | jq '.["count-plan-steps"].unchecked_steps | if type=="number" then . else null end')"
C="$(printf '%s' "$IN" | jq '.["count-plan-steps"].checked_steps | if type=="number" then . else null end')"
ITER="$(printf '%s' "$IN" | jq '.impl_iteration | if type=="number" then . else null end')"

valid_state() { jq -e '(.best_unchecked|type)=="number" and (.best_checked|type)=="number"
  and (.passes_without_progress|type)=="number" and ((.history // [])|type)=="array"' "$1" >/dev/null 2>&1; }

prev="null"
if [ -n "$STATE" ] && [ -f "$STATE" ]; then
  if valid_state "$STATE"; then
    prev="$(cat "$STATE")"
  else
    echo "stall-check: $STATE is corrupt; restarting the progress count" >&2
    prev='"corrupt"'
  fi
fi

if [ "$U" = null ] || [ "$C" = null ]; then
  echo "stall-check: count-plan-steps counts missing; not judging this pass" >&2
  next="null"
  passes="$(printf '%s' "$prev" | jq 'if type=="object" then .passes_without_progress else 0 end')"
  stalled=false
else
  pre="$(printf '%s' "$IN" | jq '.["plan-preflight"] | if type=="object" and (.unchecked_steps|type)=="number"
    and (.checked_steps|type)=="number" then {u:.unchecked_steps, c:.checked_steps} else null end')"
  next="$(jq -n --argjson prev "$prev" --argjson pre "$pre" --argjson u "$U" --argjson c "$C" \
    --argjson it "$ITER" '
    (if ($prev|type)=="object" then $prev
     elif $prev == null and $pre != null then {best_unchecked:$pre.u, best_checked:$pre.c, passes_without_progress:0, history:[]}
     else {best_unchecked:$u, best_checked:$c, passes_without_progress:-1, history:[]} end) as $s
    | ($u < $s.best_unchecked or $c > $s.best_checked) as $progress
    | {best_unchecked: ([$s.best_unchecked, $u] | min),
       best_checked: ([$s.best_checked, $c] | max),
       passes_without_progress: (if $progress then 0 else $s.passes_without_progress + 1 end),
       history: (($s.history // []) + [{iteration:$it, unchecked_steps:$u, checked_steps:$c, progress:$progress}])[-20:]}')"
  passes="$(printf '%s' "$next" | jq .passes_without_progress)"
  stalled=false
  [ "$passes" -lt "$K" ] || stalled=true
  if [ -n "$STATE" ]; then
    if ! { printf '%s\n' "$next" > "$STATE.tmp" && mv "$STATE.tmp" "$STATE"; } 2>/dev/null; then
      echo "stall-check: cannot write $STATE; this pass is not judged a stall" >&2
      stalled=false
    fi
  else
    echo "stall-check: workspace not set; this pass is not judged a stall" >&2
    stalled=false
  fi
fi

jq -n --argjson stalled "$stalled" --argjson n "$passes" --argjson k "$K" \
  --argjson u "$U" --argjson c "$C" --argjson b "$blocked" '
  {stalled:$stalled, passes_without_progress:$n, window:$k,
   unchecked_steps:$u, checked_steps:$c, blocked_tasks:$b,
   blocked_summary: (if ($b|length) == 0 then "none recorded"
     else ([$b[:10][] | "\(.plan) / \(.task) / \(.step): \(.note)"] | join("; "))
       + (if ($b|length) > 10 then "; +\(($b|length) - 10) more" else "" end) end)}'
