#!/bin/sh
# Go mutation adapter (gremlins).
#
#   mutate-go.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace,
# scope.merge_base). Targets are changed .go files relative to work_dir. Writes
# the normalized list [{file, line, col, mutator, replacement, status}] to <out json>
# and emits {language, tool, mutants}.
#
# Scoping is native: `gremlins unleash --diff <merge-base sha>` from each
# target's module root (nearest go.mod). gremlins reports out-of-diff mutants as
# SKIPPED; those and files outside the targets are dropped. gremlins records no
# replacement text, so the mutant key degrades to (file, line, column, type):
# replacement is "col:<column>". gremlins always runs `go test`, so the test
# command cannot be injected. Status: KILLED killed, LIVED survived, NOT COVERED
# no_coverage, TIMED OUT timeout, NOT VIABLE excluded.
#
# Fails closed (exit 1) on report format drift: a missing or non-numeric line,
# an unknown status in a target file, mutants reported (other than SKIPPED)
# none of which map to a target, or no target with a go.mod above it.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x). Scratch lives under
# {{workspace}}/harden/go.
set -eu

die() { echo "mutate-go: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-go.sh <targets json file> <test command> <out json>"
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

command -v gremlins >/dev/null 2>&1 || die "gremlins not found on PATH"
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
SHA="$(git -C "$WORK" rev-parse --verify -q "$MB^{commit}")" || die "merge-base is not a commit: '$MB'"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

case "$CMD" in ''|'go test ./...') ;; *) echo "mutate-go: gremlins always runs 'go test'; ignoring test command '$CMD'" >&2 ;; esac

RUN="$WS/harden/go"
rm -rf "$RUN"
mkdir -p "$RUN"

# Group targets by module root: "<module dir>\t<file>" lines.
jq -r '.[]' "$TARGETS" | while IFS= read -r f; do
  d="$(dirname "$f")"
  while [ ! -f "$WORK/$d/go.mod" ] && [ "$d" != . ]; do d="$(dirname "$d")"; done
  [ -f "$WORK/$d/go.mod" ] || { echo "mutate-go: no go.mod above $f; skipping it" >&2; continue; }
  printf '%s\t%s\n' "$d" "$f"
done > "$RUN/groups.tsv"
[ -s "$RUN/groups.tsv" ] || die "no target has a go.mod above it"

# drift <records ndjson> <raw mutant count> <where> : fail closed when the report
# no longer matches the format this adapter knows.
drift() {
  nl="$(jq -s '[.[] | select((.line | type) != "number")] | length' "$1")"
  [ "$nl" = 0 ] || die "$nl mutant(s) with a missing or non-numeric line in $3 (report format drift?)"
  bad="$(jq -r 'select(.status | startswith("?")) | .status[1:]' "$1" | sort -u | tr '\n' ' ')"
  [ -z "$bad" ] || die "unknown status(es) in $3: $bad(report format drift?)"
  [ "$2" -eq 0 ] || [ -s "$1" ] || die "$2 mutant(s) reported in $3 but none map to a target file (report format drift?)"
}

: > "$RUN/all.ndjson"
cut -f1 "$RUN/groups.tsv" | sort -u > "$RUN/modules.txt"
i=0
while IFS= read -r mod; do
  i=$((i + 1))
  rep="$RUN/report-$i.json"
  sbx --rw "$WORK" --rw "$RUN" --cwd "$WORK/$mod" -- gremlins unleash --diff "$SHA" -o "$rep" < /dev/null >&2 \
    || echo "mutate-go: gremlins exited non-zero in $mod" >&2
  jq -e '(.files | type) == "array"' "$rep" >/dev/null 2>&1 || die "gremlins wrote no usable report for module $mod"
  awk -F '\t' -v m="$mod" '$1 == m { print $2 }' "$RUN/groups.tsv" | jq -R . | jq -s . > "$RUN/files-$i.json"
  jq -c --arg m "$mod" --slurpfile want "$RUN/files-$i.json" '
    {"KILLED": "killed", "LIVED": "survived", "NOT COVERED": "no_coverage", "TIMED OUT": "timeout", "NOT VIABLE": "excluded"} as $map
    | .files[] | (if $m == "." then .file_name else "\($m)/\(.file_name)" end) as $f
    | select($f | IN($want[0][]))
    | .mutations[] | select(.status != "SKIPPED")
    | {file: $f, line, col: (.column | if type == "number" then . else 0 end), mutator: .type, replacement: "col:\(.column)", status: ($map[.status] // "?\(.status)")}' "$rep" > "$RUN/mapped-$i.ndjson"
  drift "$RUN/mapped-$i.ndjson" "$(jq '[.files[].mutations[] | select(.status != "SKIPPED")] | length' "$rep")" "module $mod"
  cat "$RUN/mapped-$i.ndjson" >> "$RUN/all.ndjson"
done < "$RUN/modules.txt"

jq -s . "$RUN/all.ndjson" > "$OUT"
jq -n --slurpfile l "$OUT" '{language: "go", tool: "gremlins", mutants: ($l[0] | length)}'
