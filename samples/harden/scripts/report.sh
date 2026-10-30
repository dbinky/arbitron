#!/bin/sh
# Writes and commits the harden report. Reads the variable snapshot as JSON on
# stdin (work_dir, workspace, slug, gate_threshold, harden_round, scope, detect)
# and emits {result, skip_reason, score, gate_passed, survivors_path,
# focus_seed_path, harden_report_path}.
#
# Writes .arbitron/pipeline/<slug>.harden.json and <slug>.survivors.json in
# work_dir and commits the two together. A snapshot without a numeric
# detect.runnable_count exits 1. With detect.runnable_count 0 the run is
# skipped: score null, gate passed, empty focus_items, summary "harden skipped:
# <reason>". Otherwise score.sh (beside this script) scores state.json, and the
# survivors are grouped per source file under "harden:<path>" ids, sorted by
# survivor count, descending; beyond 25 files, items 25 onward merge into
# "harden:other". focus_seed_path is survivors_path when the gate failed, else "".
# A scored run's skip_reason is score.sh's reason ("no mutants", or "all N
# mutants excluded ..." for a broken mutation build, which fails the gate and
# heads the summary as "harden gate failed: <reason>").
# Rounds 1..harden_round that never reached a score are recorded as voided.
set -eu

HERE="$(dirname "$0")"
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
SLUG="$(printf '%s' "$IN" | jq -r '.slug // ""')"
THRESH="$(printf '%s' "$IN" | jq -r '.gate_threshold // 80 | tonumber')"
ROUND="$(printf '%s' "$IN" | jq -r '.harden_round // 0 | tonumber')"
RUNNABLE="$(printf '%s' "$IN" | jq -r '.detect.runnable_count | if type == "number" then . else "" end')"

[ -n "$WORK" ] || { echo "report: work_dir not set" >&2; exit 1; }
[ -n "$WS" ] || { echo "report: workspace not set" >&2; exit 1; }
printf '%s' "$SLUG" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$' || { echo "report: bad slug: '$SLUG'" >&2; exit 1; }
# A missing detect is a wiring fault, not a skipped run.
[ -n "$RUNNABLE" ] || { echo "report: detect.runnable_count missing from the snapshot (did detect run?)" >&2; exit 1; }
git -C "$WORK" rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "report: not a git worktree: $WORK" >&2; exit 1; }

REL_H=".arbitron/pipeline/$SLUG.harden.json"
REL_S=".arbitron/pipeline/$SLUG.survivors.json"
mkdir -p "$WORK/.arbitron/pipeline"

if [ "$RUNNABLE" = 0 ]; then
  RESULT=skipped
  SCORE='{"score":null,"gate_passed":true}'
  SURVIVORS='[]'
  ROUNDS='[]'
  APPROVED='[]'
else
  RESULT=scored
  STATE="$WS/harden/state.json"
  [ -f "$STATE" ] || { echo "report: no state.json at $STATE" >&2; exit 1; }
  SCORE="$(printf '%s' "$IN" | sh "$HERE/score.sh")"
  SURVIVORS="$(jq -c '.survivors' "$WS/harden/survivors-round.json")"
  ROUNDS="$(jq -c --argjson r "$ROUND" '(.rounds // []) as $have
    | ($have + [range(1; $r + 1) | . as $i | select([$have[].round] | index($i) | not)
                | {round: ., voided: true, score: null}]) | sort_by(.round)' "$STATE")"
  APPROVED="$(jq -c '.approved_equivalents // []' "$STATE")"
fi

FINISHED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s' "$IN" | jq \
  --arg result "$RESULT" --argjson sc "$SCORE" --argjson surv "$SURVIVORS" --argjson rounds "$ROUNDS" \
  --argjson ap "$APPROVED" --argjson t "$THRESH" --argjson r "$ROUND" --arg at "$FINISHED" \
  --arg hp "$REL_H" --arg sp "$REL_S" --arg hout "$WORK/$REL_H" --arg sout "$WORK/$REL_S" '
  def fmt2: (. * 100 | round) as $n | "\($n / 100 | floor).\($n % 100 | tostring | if length < 2 then "0" + . else . end)";
  def plural($n; $w): "\($n) \($w)\(if $n == 1 then "" else "s" end)";
  (if $result == "skipped" then (.detect.skip_reason // "")
   else ($sc.reason // "") end) as $reason
  | ($surv | group_by(.file) | map({file: .[0].file, mutants: map({line, mutator, replacement})})
     | sort_by(-(.mutants | length), .file)) as $groups
  | (if ($groups | length) > 25
     then ($groups[:24] | map({id: "harden:\(.file)"} + .))
          + [{id: "harden:other", files: [$groups[24:][].file],
              mutants: [$groups[24:][] | .file as $f | .mutants[] | {file: $f} + .]}]
     else $groups | map({id: "harden:\(.file)"} + .) end) as $items
  | (if $result == "skipped" then "harden skipped: \($reason)"
     elif $sc.score == null and $sc.gate_passed then "no mutants in the changed code; gate passed"
     elif $sc.score == null then "harden gate failed: \($reason)"
     else "mutation score \($sc.score | fmt2)% (gate \($t)%) after \(plural($r; "round")); "
          + (if ($surv | length) == 0 then "no survivors"
             else "\(plural($surv | length; "survivor")) in \(plural($groups | length; "file"))" end)
          + (if $sc.gate_passed then "; gate passed" else "" end) end) as $summary
  | {harden: {slug, base_ref: (.scope.base_ref // ""), merge_base: (.scope.merge_base // ""), threshold: $t,
              result: $result, skip_reason: $reason, score: $sc.score, gate_passed: $sc.gate_passed, rounds: $rounds,
              languages: [(.detect.languages // [])[] | {language, tool, version, version_status, version_warning, status, reason, test_command, test_command_source}],
              totals: (if $result == "skipped" then null
                       else $sc | {killed, timeout, survived, no_coverage, excluded, total, approved_equivalents, denominator} end),
              approved_equivalents: $ap, finished_at: $at},
     survivors: {summary: $summary, score: $sc.score, threshold: $t, gate_passed: $sc.gate_passed, focus_items: $items},
     out: {result: $result, skip_reason: $reason, score: $sc.score, gate_passed: $sc.gate_passed,
           survivors_path: $sp, focus_seed_path: (if $sc.gate_passed then "" else $sp end), harden_report_path: $hp}}
  ' > "$WS/harden/report.json"

jq '.harden' "$WS/harden/report.json" > "$WORK/$REL_H"
jq '.survivors' "$WS/harden/report.json" > "$WORK/$REL_S"

git -C "$WORK" add -- "$REL_H" "$REL_S"
if ! git -C "$WORK" diff --cached --quiet -- "$REL_H" "$REL_S"; then
  git -C "$WORK" commit -q -m "chore(harden): record mutation report for $SLUG" -- "$REL_H" "$REL_S" 1>&2
fi

jq '.out' "$WS/harden/report.json"
