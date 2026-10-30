#!/bin/sh
# Scores the mutants in {{workspace}}/harden/state.json. Reads the variable
# snapshot as JSON on stdin (workspace, gate_threshold) and emits {score,
# gate_passed, reason, killed, survived, no_coverage, timeout, excluded, total,
# approved_equivalents, denominator, survivor_files}.
#
# killed and timeout count as killed; survived and no_coverage as survivors;
# excluded mutants (compile and runtime errors, unviable, unknown codes) are not
# counted at all.
# Approved equivalents leave both the numerator and the denominator, so
# score = killed ÷ (total − approved_equivalents) × 100, to 2 decimal places.
# A denominator of 0 gives score null. The gate then passes only when no mutant
# was produced (reason "no mutants") or every counted one is an approved
# equivalent ("all N remaining mutants approved equivalent"); if any mutant was
# excluded it FAILS ("all N mutants excluded ..."), because every mutant failing
# to build or run means the mutation build is broken, not that the code is safe. Otherwise the gate compares the score as
# rounded to 2 decimals: it passes when that rounded score >= gate_threshold
# (80.00 passes, 79.99 fails, and 79.996 rounds to 80.00 and passes).
#
# Writes {{workspace}}/harden/survivors-round.json ({score, threshold,
# gate_passed, survivors:[{mutant_id, file, line, col, mutator, replacement, status,
# language}]}) for the strengthen agent, and records the score on the last
# entry of state.rounds.
set -eu

IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
THRESH="$(printf '%s' "$IN" | jq -r '.gate_threshold // 80 | tonumber')"
[ -n "$WS" ] || { echo "score: workspace not set" >&2; exit 1; }
STATE="$WS/harden/state.json"
[ -f "$STATE" ] || { echo "score: no state.json at $STATE" >&2; exit 1; }

RESULT="$(jq --argjson t "$THRESH" '
  (.approved_equivalents // []) as $ap
  | ([.mutants[] | select(.status == "excluded")] | length) as $excl
  | [.mutants | to_entries[] | select(.value.status != "excluded")] as $counted
  | [$counted[] | select(.key | IN($ap[]) | not)] as $rest
  | def n($s): [$rest[] | select(.value.status == $s)] | length;
    ($rest | length) as $den
  | [$rest[] | select(.value.status == "survived" or .value.status == "no_coverage")] as $surv
  | (if $den == 0 then null else (((n("killed") + n("timeout")) * 10000 / $den) | round) / 100 end) as $score
  | (if $den > 0 then ""
     elif $excl > 0 then "all \($excl) mutants excluded (compile/runtime errors or unknown statuses) — mutation build likely broken"
     elif ($counted | length) > 0 then "all \($counted | length) remaining mutants approved equivalent"
     else "no mutants" end) as $reason
  | {result: {score: $score, gate_passed: (if $den == 0 then $excl == 0 else $score >= $t end), reason: $reason,
              killed: n("killed"), survived: n("survived"), no_coverage: n("no_coverage"), timeout: n("timeout"),
              excluded: $excl, total: ($counted | length), approved_equivalents: (($counted | length) - $den), denominator: $den,
              survivor_files: ([$surv[].value.file] | unique)},
     survivors: ([$surv[] | {mutant_id: .key, file: .value.file, line: .value.line, col: (.value.col // 0), mutator: .value.mutator,
                             replacement: .value.replacement, status: .value.status, language: .value.language}]
                 | sort_by(.file, .line, .mutant_id))}' "$STATE")" \
  || { echo "score: state.json is malformed: $STATE" >&2; exit 1; }

printf '%s' "$RESULT" | jq --argjson t "$THRESH" '{score: .result.score, threshold: $t, gate_passed: .result.gate_passed, survivors}' \
  > "$WS/harden/survivors-round.json"

SCORE="$(printf '%s' "$RESULT" | jq -c '.result.score')"
jq --argjson s "$SCORE" 'if (.rounds // []) | length > 0 then .rounds[-1].score = $s else . end' "$STATE" > "$STATE.tmp"
mv "$STATE.tmp" "$STATE"

printf '%s' "$RESULT" | jq '.result'
