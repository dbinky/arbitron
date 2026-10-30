#!/bin/sh
# Runs mutation testing for every runnable language and records the results.
#
#   mutate.sh                   first run: a fresh state
#   mutate.sh --survivors-only  rescore: re-run only files that still have survivors
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace,
# harden_round, scope.merge_base, detect.languages) and pipes it on to
# mutate-<language>.sh beside this script, once per runnable language, with
# ["<targets json file>", "<test command>", "<out json>"]. Writes
# {{workspace}}/harden/state.json:
#   {mutants: {"<file>:<line>:<col>:<mutator>:<replacement>": {status, language,
#    tool, file, line, col, mutator, replacement}}, approved_equivalents: [keys],
#    file_blobs: {<file>: <blob sha scored>}, rounds: [{round, voided, score}]}
# and emits {mutants, killed, timeout, survived, no_coverage, excluded, languages}.
#
# <col> is the start column of the mutation (0 where the tool reports none). A
# key repeated within one tool's results is a distinct mutant and gets a #N
# suffix (key#2, key#3, ...) in report order; merging duplicates to a best
# status happens only inside an adapter's per-project merge (C#), which treats
# an unresolved collision pessimistically. The first run starts round 0.
#
# --survivors-only RESCORES: it targets only the files with a survived or
# no_coverage mutant that is not an approved equivalent (C# keeps its
# test-project targets and its results are filtered to those files) and skips
# languages with none. Fresh evidence is authoritative for every requested file:
# its whole earlier result set is REPLACED by the new one, an empty new result
# included, and nothing is inherited from a reused line key (an inline test edit
# can shift lines so that a key now names a different mutant). Files that were
# not requested keep their earlier results. An approved equivalent in a rescored
# file survives only when its key is still reported AND the file's blob is the
# one the approval was scored against (file_blobs), and the key is not part of
# a collided group (key, key#2, ...) in the earlier or the fresh results, since
# a #N ordinal is report order and cannot prove identity; otherwise the approval
# is dropped and the mutant counts again. It records round harden_round + 1.
set -eu

die() { echo "mutate: $*" >&2; exit 1; }

HERE="$(dirname "$0")"
MODE=full
case "${1:-}" in
  '') ;;
  --survivors-only) MODE=survivors ;;
  *) die "usage: mutate.sh [--survivors-only]" ;;
esac

IN="$(cat)"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
ROUND="$(printf '%s' "$IN" | jq -r '.harden_round // 0 | tonumber')"
[ -n "$WS" ] || die "workspace not set"
H="$WS/harden"
STATE="$H/state.json"
mkdir -p "$H"
if [ "$MODE" = survivors ]; then
  [ -f "$STATE" ] || die "--survivors-only needs an earlier state.json at $STATE"
  OLD="$(cat "$STATE")"
else
  OLD='{"mutants":{},"approved_equivalents":[],"rounds":[]}'
fi

WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
RUN="$H/run"
rm -rf "$RUN"
mkdir -p "$RUN"
: > "$RUN/all.ndjson"
: > "$RUN/rescored.ndjson"
printf '%s' "$IN" | jq -c '(.detect.languages // [])[] | select(.status == "runnable")' > "$RUN/langs.ndjson"

while IFS= read -r entry; do
  L="$(printf '%s' "$entry" | jq -r .language)"
  TOOL="$(printf '%s' "$entry" | jq -r .tool)"
  CMD="$(printf '%s' "$entry" | jq -r '.test_command // ""')"
  ADAPTER="$HERE/mutate-$L.sh"
  [ -f "$ADAPTER" ] || die "no adapter for $L ($ADAPTER)"

  ONLY=null
  printf '%s' "$entry" | jq -c '.targets // []' > "$RUN/$L.targets.json"
  if [ "$MODE" = survivors ]; then
    ONLY="$(printf '%s' "$OLD" | jq -c --arg l "$L" '(.approved_equivalents // []) as $ap
      | [.mutants | to_entries[] | select(.value.language == $l)
         | select(.value.status == "survived" or .value.status == "no_coverage")
         | select(.key | IN($ap[]) | not) | .value.file] | unique')"
    if [ "$ONLY" = "[]" ]; then
      echo "mutate: $L has no survivors; not re-run" >&2
      continue
    fi
    [ "$L" = csharp ] || printf '%s' "$ONLY" > "$RUN/$L.targets.json"
    printf '%s\n' "$ONLY" >> "$RUN/rescored.ndjson"
  fi

  echo "mutate: running $TOOL for $L" >&2
  printf '%s' "$IN" | sh "$ADAPTER" "$RUN/$L.targets.json" "$CMD" "$RUN/$L.out.json" >&2 \
    || die "the $L adapter ($TOOL) failed"
  jq -e 'type == "array"' "$RUN/$L.out.json" >/dev/null 2>&1 || die "the $L adapter wrote no result list"
  # A repeated key within one tool's results is a different mutant (the same
  # replacement at another column, say), not a duplicate: number repeats in
  # report order (key, key#2, ...). The order is the tool's own, so a rescore
  # numbers each occurrence the same way.
  jq -c --arg l "$L" --arg t "$TOOL" '
    reduce .[] as $r ({seen: {}, out: []};
      "\($r.file):\($r.line):\($r.col // 0):\($r.mutator):\($r.replacement)" as $k
      | .seen[$k] += 1
      | .out += [{key: (if .seen[$k] == 1 then $k else "\($k)#\(.seen[$k])" end),
                  value: {status: $r.status, language: $l, tool: $t, file: $r.file, line: $r.line,
                          col: ($r.col // 0), mutator: $r.mutator, replacement: $r.replacement}}])
    | .out[]' "$RUN/$L.out.json" > "$RUN/$L.keyed.ndjson"
  n="$(jq -s '[.[] | select(.key | test("#[0-9]+$"))] | length' "$RUN/$L.keyed.ndjson")"
  [ "$n" = 0 ] || echo "mutate: $n key collision(s) in the $L results kept as distinct mutants (key#N)" >&2
  jq -c --argjson only "$ONLY" 'select($only == null or (.value.file | IN($only[])))' "$RUN/$L.keyed.ndjson" >> "$RUN/all.ndjson"
done < "$RUN/langs.ndjson"

DUP="$(jq -rs 'group_by(.key) | map(select(length > 1) | .[0].key) | join(", ")' "$RUN/all.ndjson")"
[ -z "$DUP" ] || die "the same mutant key came from more than one language: $DUP"

# The blob each scored file had at HEAD (adapters require a clean worktree, so
# HEAD is what was mutated): the proof that an approval's key still names the
# same mutant after a rescore.
RESCORED="$(jq -sc 'add // [] | unique' "$RUN/rescored.ndjson")"
{ jq -r '.value.file' "$RUN/all.ndjson"; printf '%s' "$RESCORED" | jq -r '.[]'; } | sort -u \
  | while IFS= read -r f; do
      [ -n "$f" ] || continue
      printf '%s\t%s\n' "$f" "$(git -C "$WORK" rev-parse -q --verify "HEAD:$f" 2>/dev/null || true)"
    done | jq -Rn '[inputs | split("\t") | {key: .[0], value: (.[1] // "")}] | from_entries' > "$RUN/blobs.json"

# An approval on a member of a collided key group in a rescored file is
# ambiguous: the #N ordinal is report order, so a reordered rescore would hand
# the approval to a different mutant. Such approvals are dropped (fail closed)
# whether the collision is in the earlier results or the fresh ones.
AMBIG='[]'
if [ "$MODE" = survivors ]; then
  AMBIG="$(printf '%s' "$OLD" | jq -c --slurpfile new "$RUN/all.ndjson" --argjson rs "$RESCORED" '
    def collided($m): . as $k
      | ($k | sub("#[0-9]+$"; "")) as $b
      | ($b != $k and $m[$b] != null) or $m["\($k)#2"] != null;
    .mutants as $old | ($new | from_entries) as $fresh
    | [(.approved_equivalents // [])[] | . as $k
       | select(($old[$k].file // null) as $f | $f != null and ($f | IN($rs[])))
       | select(collided($old) or collided($fresh))]')"
  [ "$AMBIG" = "[]" ] || echo "mutate: dropped approval(s) on collided mutant keys whose identity a rescore cannot prove: $(printf '%s' "$AMBIG" | jq -r 'join(", ")')" >&2
fi

printf '%s' "$OLD" | jq --slurpfile new "$RUN/all.ndjson" --slurpfile nb "$RUN/blobs.json" --argjson rs "$RESCORED" \
  --arg mode "$MODE" --argjson r "$ROUND" --argjson ambig "$AMBIG" '
  ($new | from_entries) as $fresh
  | if $mode == "full" then
      {mutants: $fresh, approved_equivalents: [], file_blobs: $nb[0], rounds: [{round: 0, voided: false, score: null}]}
    else
      .mutants as $old
      | (.file_blobs // {}) as $ob
      | (($old | with_entries(select(.value.file | IN($rs[]) | not))) + $fresh) as $mutants
      | .mutants = $mutants
      | .approved_equivalents = [(.approved_equivalents // [])[]
          | . as $k | ($old[$k].file // null) as $f
          | select($k | IN($ambig[]) | not)
          | select($f == null or ($f | IN($rs[]) | not)
                   or ($mutants[$k] != null and ($ob[$f] // "") != "" and $ob[$f] == ($nb[0][$f] // "")))]
      | .file_blobs = ($ob + $nb[0])
      | .rounds = ([(.rounds // [])[] | select(.round != $r + 1)] + [{round: ($r + 1), voided: false, score: null}])
    end' > "$STATE.tmp"
mv "$STATE.tmp" "$STATE"

jq --slurpfile langs "$RUN/langs.ndjson" '[.mutants[].status] as $s
  | def n($x): [$s[] | select(. == $x)] | length;
    {mutants: ($s | length), killed: n("killed"), timeout: n("timeout"), survived: n("survived"),
     no_coverage: n("no_coverage"), excluded: n("excluded"), languages: [$langs[].language]}' "$STATE"
