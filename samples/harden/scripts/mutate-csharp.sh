#!/bin/sh
# C# mutation adapter (Stryker.NET).
#
#   mutate-csharp.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace,
# scope.merge_base). Targets are test project (.csproj) paths relative to
# work_dir, as detect.sh derives them from the ProjectReference graph. Writes the
# normalized list [{file, line, col, mutator, replacement, status}] to <out json> and
# emits {language, tool, mutants[, warning]}.
#
# Scoping is native: `dotnet-stryker --since:<sha>` (HEAD~N is rejected, so the
# merge-base is resolved to a sha first), run from each test project directory,
# with `--reporter json --output <dir under {{workspace}}/harden/csharp>`.
# Stryker runs in a plain local clone of work_dir's HEAD, never in work_dir:
# work_dir is a linked worktree, and Stryker.NET (LibGit2Sharp) diffs against the
# repository's MAIN working tree, so in a worktree no changed file matches a
# mutated one and every mutant is "Removed by since filter" (verified on
# dotnet-stryker 5.0.0). The clone also keeps bin/ and obj/ out of work_dir.
# Stryker.NET drives `dotnet test` itself, so the test command cannot be
# injected. Report files are keyed by absolute path; they are made relative to
# the clone root, which mirrors work_dir. Status: Killed killed, Survived survived, NoCoverage no_coverage,
# Timeout timeout; CompileError, RuntimeError and Ignored excluded, except an
# Ignored mutant whose statusReason is "Removed by since filter": that one is
# outside the diff and is dropped. Drop counts go to stderr. Reports from several test projects merge per mutant to the best
# status (killed > timeout > survived > no_coverage > excluded).
#
# When mutants were reported but every one was since-filtered, a warning goes to
# stderr and the JSON gains "warning": "all mutants since-filtered"; the exit
# status is unchanged (the score gate decides what zero mutants means).
#
# Fails closed (exit 1) on report format drift: a missing or non-numeric line,
# an unknown status, or mutants reported none of which map to a file in the
# worktree.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x).
set -eu

die() { echo "mutate-csharp: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-csharp.sh <targets json file> <test command> <out json>"
TARGETS="$1"; CMD="$2"; OUT="$3"
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
MB="$(printf '%s' "$IN" | jq -r '.scope.merge_base // ""')"

# Repository code (the mutation tool and the tests it runs) only ever runs through
# sandbox.sh: a scrubbed environment, a read-only toolchain, write access to the
# --rw paths only, no route off the host. Status 125 means the sandbox could not
# be built, which ends the adapter: there is no unisolated fallback.
SBX="$(dirname "$0")/sandbox.sh"
sbx() {
  _rc=0
  sh "$SBX" --run-dir "$WS" "$@" || _rc=$?
  [ "$_rc" -ne 125 ] || die "the sandbox refused to run it (its reason is above: no isolation available, or a toolchain it could not resolve safely); refusing to run repository code unisolated"
  return "$_rc"
}

if command -v dotnet-stryker >/dev/null 2>&1; then
  STRYKER="dotnet-stryker"
elif command -v dotnet >/dev/null 2>&1; then
  STRYKER="dotnet stryker"
else
  die "dotnet-stryker not found on PATH (nor dotnet for a local tool)"
fi
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
SHA="$(git -C "$WORK" rev-parse --verify -q "$MB^{commit}")" || die "merge-base is not a commit: '$MB'"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

case "$CMD" in ''|'dotnet test') ;; *) echo "mutate-csharp: Stryker.NET always runs 'dotnet test'; ignoring test command '$CMD'" >&2 ;; esac

RUN="$WS/harden/csharp"
rm -rf "$RUN"
mkdir -p "$RUN"
CLONE="$RUN/clone"
HEAD_SHA="$(git -C "$WORK" rev-parse --verify HEAD)" || die "work_dir has no HEAD commit"
git clone -q --no-checkout "$WORK" "$CLONE" >&2 || die "could not clone $WORK for Stryker.NET"
git -C "$CLONE" checkout -q --detach "$HEAD_SHA" >&2 || die "could not check out $HEAD_SHA in the Stryker.NET clone"
git -C "$CLONE" rev-parse --verify -q "$SHA^{commit}" >/dev/null || die "merge-base $SHA is not in the Stryker.NET clone"
CLONE_P="$(cd "$CLONE" && pwd -P)"

# drift <records ndjson> <raw mutant count> <where> : fail closed when the report
# no longer matches the format this adapter knows.
drift() {
  nl="$(jq -s '[.[] | select((.line | type) != "number")] | length' "$1")"
  [ "$nl" = 0 ] || die "$nl mutant(s) with a missing or non-numeric line in $3 (report format drift?)"
  bad="$(jq -r 'select(.status | startswith("?")) | .status[1:]' "$1" | sort -u | tr '\n' ' ')"
  [ -z "$bad" ] || die "unknown status(es) in $3: $bad(report format drift?)"
  [ "$2" -eq 0 ] || [ -s "$1" ] || die "$2 mutant(s) reported in $3 but none map to a file in the worktree (report format drift?)"
}

# Stryker.NET marks every mutant outside the --since diff Ignored with this
# statusReason (verified on dotnet-stryker 5.0.0); only those are out of scope.
SINCE='def since: .status == "Ignored" and ((.statusReason // "") | test("since filter"; "i"));'

jq -r '.[]' "$TARGETS" > "$RUN/projects.txt"
SINCE_TOTAL=0
KEPT_TOTAL=0
[ -s "$RUN/projects.txt" ] || die "no test project targets"
i=0
while IFS= read -r proj; do
  i=$((i + 1))
  [ -d "$WORK/$(dirname "$proj")" ] || die "test project directory not found: $proj"
  dir="$CLONE/$(dirname "$proj")"
  out="$RUN/run-$i"
  # shellcheck disable=SC2086 # STRYKER is "dotnet-stryker" or "dotnet stryker"
  sbx --rw "$RUN" --rw "$CLONE" --cwd "$dir" -- $STRYKER --since:"$SHA" --reporter json --output "$out" < /dev/null >&2 \
    || echo "mutate-csharp: Stryker.NET exited non-zero for $proj" >&2
  rep="$out/reports/mutation-report.json"
  jq -e '(.files | type) == "object"' "$rep" >/dev/null 2>&1 || die "Stryker.NET wrote no usable report for $proj"
  jq -c --arg w "$CLONE/" --arg wp "$CLONE_P/" "$SINCE"'
    {"Killed": "killed", "Survived": "survived", "NoCoverage": "no_coverage", "Timeout": "timeout",
     "CompileError": "excluded", "RuntimeError": "excluded", "Ignored": "excluded"} as $map
    | .files | to_entries[]
    | (.key | gsub("\\\\"; "/")) as $abs
    | (if ($abs | startswith($wp)) then $abs[($wp | length):]
       elif ($abs | startswith($w)) then $abs[($w | length):]
       else null end) as $f
    | select($f != null)
    | .value.mutants[] | select(since | not)
    | {file: $f, line: .location.start.line, col: (.location.start.column | if type == "number" then . else 0 end), mutator: .mutatorName, replacement, status: ($map[.status] // "?\(.status)")}' "$rep" \
    > "$RUN/project-$i.ndjson"
  since_n="$(jq "$SINCE"' [.files[].mutants[] | select(since)] | length' "$rep")"
  raw="$(jq "$SINCE"' [.files[].mutants[] | select(since | not)] | length' "$rep")"
  echo "mutate-csharp: dropped $since_n mutant(s) removed by the --since filter (outside the diff) and $((raw - $(wc -l < "$RUN/project-$i.ndjson"))) outside the worktree for $proj" >&2
  drift "$RUN/project-$i.ndjson" "$raw" "the report for $proj"
  SINCE_TOTAL=$((SINCE_TOTAL + since_n))
  KEPT_TOTAL=$((KEPT_TOTAL + raw))
done < "$RUN/projects.txt"

# Several test projects can cover one source file, so the same mutant appears in
# several reports: merge those to the best status. A mutant is identified across
# reports by (file, line, start column, mutator, replacement). Report order and
# occurrence ordinals are NOT identity: when one report holds the same identity
# twice (an unresolved collision) its mutants cannot be told apart between
# projects, so every one of them takes the most pessimistic non-excluded status
# seen for that identity and a collision can never fabricate a kill.
for p in "$RUN"/project-*.ndjson; do jq -s -c . "$p"; done | jq -s '
  {"killed": 4, "timeout": 3, "survived": 2, "no_coverage": 1, "excluded": 0} as $rank
  | ["excluded", "no_coverage", "survived", "timeout", "killed"] as $byrank
  | def idk: "\(.file):\(.line):\(.col):\(.mutator):\(.replacement)";
    [to_entries[] | .key as $p | .value[] | . + {_p: $p, _k: idk}] | to_entries | map(.value + {_i: .key})
  | group_by(._k)
  | map(. as $g
        | ($g | group_by(._p) | map(length) | max) as $dup
        | if $dup == 1
          then [(($g | map($rank[.status]) | max) as $best | first($g[] | select($rank[.status] == $best)))
                + {_i: ($g | map(._i) | min)}]
          else ([$g[] | $rank[.status] | select(. > 0)] | (if length == 0 then 0 else min end)) as $worst
               | [range(0; $dup) as $n | $g[0] + {status: $byrank[$worst], _i: ($g | map(._i) | min)}]
          end)
  | add // [] | sort_by(._i) | map(del(._k, ._p, ._i))' > "$OUT"
# Mutants were reported but every one was dropped by --since (none in the diff
# matched): the run would pass with nothing mutated, so say so.
WARN=""
if [ "$SINCE_TOTAL" -gt 0 ] && [ "$KEPT_TOTAL" -eq 0 ]; then
  WARN="all mutants since-filtered"
  echo "mutate-csharp: warning: $WARN ($SINCE_TOTAL mutant(s) dropped, none left to score); nothing in the diff was mutated" >&2
fi
jq -n --slurpfile l "$OUT" --arg w "$WARN" '{language: "csharp", tool: "Stryker.NET", mutants: ($l[0] | length)}
  + (if $w == "" then {} else {warning: $w} end)'
