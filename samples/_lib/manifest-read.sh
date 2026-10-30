#!/bin/sh
# Reads the variable snapshot as JSON on stdin (work_dir, branch, slug, plus any
# key named by --explicit); reads .arbitron/pipeline/<slug>.json and emits
# {exists, path, manifest, latest, done_stages, stale, outputs, next_stage, inputs}.
#
#   --fetch              read origin/<branch>:<path> after fetching it
#   --order a,b,...      stage order for detection (default: the canonical order)
#   --start-at-var NAME  a non-empty snapshot[NAME] overrides detection
#   --explicit k,...     a non-empty snapshot[k] wins over the manifest
#   --require k,...      each k must resolve non-empty, else exit 1
#   --optional k=value   default when k is absent (JSON-typed if it parses)
#
# An empty branch, or no manifest, gives exists:false and an empty view; the
# explicit/require/optional inputs are still resolved.
#
# FRESHNESS. Entries are append-only, so a stage's position in the stages array
# says when it ran. A stage counts as done only if its latest entry is done AND
# sits after the latest entry (of any status, blocked included) of every earlier
# canonical stage: re-running spec, or a blocked spec attempt, makes the design,
# plans and everything downstream of it stale until they run again. done_stages,
# next_stage, outputs and the resolved inputs all use that rule; `stale` lists
# the stages whose latest entry was done but is superseded. This assumes ONE
# writer appends stage entries at a time (the serial pipeline); independent
# concurrent stage runs would need real provenance on each entry.
set -eu

FETCH=0
ORDER=""
START_VAR=""
EXPLICIT=""
REQUIRE=""
OPTIONAL='[]'

need_val() { [ "$1" -ge 2 ] || { echo "manifest-read: $2 needs a value" >&2; exit 1; }; }

while [ $# -gt 0 ]; do
  case "$1" in
    --fetch) FETCH=1 ;;
    --order) need_val $# "$1"; ORDER="${ORDER:+$ORDER,}$2"; shift ;;
    --start-at-var) need_val $# "$1"; START_VAR="$2"; shift ;;
    --explicit) need_val $# "$1"; EXPLICIT="${EXPLICIT:+$EXPLICIT,}$2"; shift ;;
    --require) need_val $# "$1"; REQUIRE="${REQUIRE:+$REQUIRE,}$2"; shift ;;
    --optional)
      need_val $# "$1"
      case "$2" in
        *=*) ;;
        *) echo "manifest-read: --optional expects k=default, got: $2" >&2; exit 1 ;;
      esac
      OPTIONAL="$(jq -nc --argjson o "$OPTIONAL" --arg kv "$2" \
        '$o + [($kv | capture("^(?<k>[^=]*)=(?<v>.*)$"; "s") | [.k, .v])]')"
      shift ;;
    *) echo "manifest-read: unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

list_json() { jq -nc --arg s "$1" '$s | split(",") | map(select(. != ""))'; }

IN="$(cat)"
BRANCH="$(printf '%s' "$IN" | jq -r '.branch // ""')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug // ""')"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"

EXISTS=false
MPATH=""
MTEXT=""
if [ -n "$BRANCH" ]; then
  [ -n "$SLUG" ] || { echo "manifest-read: slug is empty" >&2; exit 1; }
  case "$SLUG" in
    */*|.*) echo "manifest-read: slug must not contain '/' or start with '.': $SLUG" >&2; exit 1 ;;
  esac
  [ -n "$WORK" ] || { echo "manifest-read: work_dir not set" >&2; exit 1; }
  MPATH=".arbitron/pipeline/$SLUG.json"
  if [ "$FETCH" -eq 1 ]; then
    git -C "$WORK" fetch -q origin "+refs/heads/$BRANCH:refs/remotes/origin/$BRANCH" >&2 \
      || { echo "manifest-read: git fetch origin $BRANCH failed" >&2; exit 1; }
    if git -C "$WORK" cat-file -e "origin/$BRANCH:$MPATH" 2>/dev/null; then
      MTEXT="$(git -C "$WORK" show "origin/$BRANCH:$MPATH")"
      EXISTS=true
    fi
  elif [ -f "$WORK/$MPATH" ]; then
    MTEXT="$(cat "$WORK/$MPATH")"
    EXISTS=true
  fi
fi

OUT="$(printf '%s' "$IN" | jq -c \
  --argjson exists "$EXISTS" --arg path "$MPATH" --arg mtext "$MTEXT" --arg branch "$BRANCH" \
  --argjson order "$(list_json "$ORDER")" --arg startvar "$START_VAR" \
  --argjson explicit "$(list_json "$EXPLICIT")" --argjson require "$(list_json "$REQUIRE")" \
  --argjson optional "$OPTIONAL" '
def canon: ["ideate","spec","design","plan","implement","harden","refine","review"];
def nonempty:
  if . == null then false
  elif type == "string" then . != ""
  elif type == "array" or type == "object" then length > 0
  else true end;
# Which stage normally produces a key, for the failure message.
def producer:
  {idea_path:"ideate", provided:"ideate", spec_path:"spec", design_paths:"design",
   plan_paths:"plan", deps_resolved:"plan", deps_gaps:"plan",
   status:"implement", blocked_reason:"implement", baseline_path:"implement",
   result:"harden", skip_reason:"harden", score:"harden", gate_passed:"harden",
   survivors_path:"harden", focus_seed_path:"harden", harden_report_path:"harden",
   pr_number:"refine", pr_url:"refine", push_ok:"review", fix_scope:"review"}[.];
def validate($m):
  "malformed manifest \($path): " as $pre
  | if ($m | type) != "object" then error($pre + "not a JSON object")
    elif ($m.stages | type) != "array" then error($pre + "no stages array")
    else
      $m.stages | to_entries[]
      | .key as $i | .value as $e
      | if ($e | type) != "object" then error($pre + "stage entry \($i) is not an object")
        elif (canon | any(. == $e.stage) | not) then error($pre + "unknown stage \($e.stage | tojson)")
        elif ($e.status != "done" and $e.status != "blocked") then error($pre + "unknown status \($e.status | tojson) for stage \($e.stage)")
        elif ($e.outputs != null and ($e.outputs | type) != "object") then error($pre + "outputs of stage \($e.stage) is not an object")
        else empty end
    end;

. as $in
| try (
    ($order | if length == 0 then canon else . end) as $ord
    | (if ($ord | all(. as $s | canon | any(. == $s))) then . else error("--order has an unknown stage") end) as $_o
    | (if $startvar == "" then "" else ($in[$startvar] // "") end) as $startat
    | (if ($startat | type) != "string" then error("\($startvar) must be a string") else . end) as $_t
    | (if $startat != "" and ($ord | any(. == $startat) | not)
         then error("\($startvar)=\($startat | tojson) is not in --order (\($ord | join(",")))")
         else . end) as $_s
    | (if $exists then
         ($mtext | try fromjson catch error("malformed manifest \($path): invalid JSON (\(.))"))
       else null end) as $m
    | (if $exists then (reduce validate($m) as $x (0; .)) else 0 end) as $_v
    | (if $exists then
         reduce $m.stages[] as $e ({}; .[$e.stage] = $e)
       else {} end) as $latest
    | (if $exists then (reduce ($m.stages | to_entries[]) as $e ({}; .[$e.value.stage] = $e.key)) else {} end) as $idx
    | (reduce canon[] as $s ({up: -1, fresh: {}};
         ($idx[$s] // -1) as $i
         | .fresh[$s] = ($i > .up)
         | .up = ([.up, $i] | max))
       | .fresh) as $fresh
    | (canon | map(select($latest[.].status == "done" and $fresh[.]))) as $done
    | (canon | map(select($latest[.].status == "done" and ($fresh[.] | not)))) as $stale
    | (reduce $done[] as $s ({}; . + ($latest[$s].outputs // {}))
       + (if $exists then
            ($m | {slug, base_branch, branch} | with_entries(select(.value | nonempty)))
          else {} end)) as $outputs
    | ($optional | map({key: .[0], value: (.[1] as $r | try ($r | fromjson) catch $r)}) | from_entries) as $opt
    | ($explicit + $require + ($optional | map(.[0])) | unique) as $keys
    | (reduce $keys[] as $k ({};
         if (($explicit | any(. == $k)) and ($in[$k] | nonempty)) then .[$k] = $in[$k]
         elif ($outputs[$k] | nonempty) then .[$k] = $outputs[$k]
         elif $opt | has($k) then .[$k] = $opt[$k]
         elif ($explicit | any(. == $k)) then .[$k] = ""
         else . end)) as $inputs
    | ($require | map(select(. as $k | $inputs[$k] | nonempty | not))) as $missing
    | (if ($missing | length) > 0 then
         $missing[0] as $k
         | error("required input \($k | tojson) is missing: no explicit value, and branch \($branch | tojson) has no manifest output for it (stage \($k | producer // "unknown") likely not done)")
       else . end) as $_r
    | {
        exists: $exists,
        path: $path,
        manifest: $m,
        latest: $latest,
        done_stages: $done,
        stale: $stale,
        outputs: $outputs,
        next_stage: (if $startat != "" then $startat
                     else ($ord | map(select(. as $o | $done | index($o) | not)) | .[0]) // "complete" end),
        inputs: $inputs
      }
  ) catch {error: .}
')" || { echo "manifest-read: jq failed" >&2; exit 1; }

ERR="$(printf '%s' "$OUT" | jq -r '.error // empty')"
if [ -n "$ERR" ]; then
  echo "manifest-read: $ERR" >&2
  exit 1
fi
printf '%s\n' "$OUT"
