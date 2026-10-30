#!/bin/sh
# Rust mutation adapter (cargo-mutants).
#
#   mutate-rust.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace,
# scope.merge_base). Targets are changed .rs files relative to work_dir. Writes
# the normalized list [{file, line, col, mutator, replacement, status}] to <out json>
# and emits {language, tool, mutants}.
#
# Scoping is native: `git diff --relative <merge-base> -- <targets>` into a file
# under {{workspace}}/harden/rust, then `cargo mutants --in-diff <file> --output
# <dir>` from each target's topmost Cargo.toml (the workspace or crate root).
# The test command is injected when it is `cargo test ...` (each further word
# becomes --cargo-test-arg) or `cargo nextest ...` (--test-tool nextest);
# anything else is ignored with a warning. Report: <dir>/mutants.out/
# outcomes.json; the Baseline scenario is skipped. mutator is the mutant genre.
# Status: CaughtMutant killed, MissedMutant survived, Timeout timeout, Unviable
# excluded.
#
# Fails closed (exit 1) on report format drift: a missing or non-numeric line,
# an unknown outcome in a target file, mutants reported none of which map to a
# target, or no target with a Cargo.toml above it.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x).
set -eu

die() { echo "mutate-rust: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-rust.sh <targets json file> <test command> <out json>"
TARGETS="$1"; CMD="$2"; OUT="$3"
IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"

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
MB="$(printf '%s' "$IN" | jq -r '.scope.merge_base // ""')"

command -v cargo >/dev/null 2>&1 || die "cargo not found on PATH (cargo-mutants runs as cargo mutants)"
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
SHA="$(git -C "$WORK" rev-parse --verify -q "$MB^{commit}")" || die "merge-base is not a commit: '$MB'"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

RUN="$WS/harden/rust"
rm -rf "$RUN"
mkdir -p "$RUN"

# Test-command injection, one flag per line.
: > "$RUN/test-args.txt"
case "$CMD" in
  'cargo nextest'*) printf '%s\n' --test-tool nextest > "$RUN/test-args.txt" ;;
  'cargo test'*)
    for w in ${CMD#cargo test}; do printf '%s\n%s\n' --cargo-test-arg "$w"; done > "$RUN/test-args.txt" ;;
  '') ;;
  *) echo "mutate-rust: cannot inject test command '$CMD' into cargo-mutants; using cargo test" >&2 ;;
esac

# Group targets by topmost Cargo.toml: "<root>\t<file relative to root>" lines.
jq -r '.[]' "$TARGETS" | while IFS= read -r f; do
  d="$(dirname "$f")"
  top=""
  while :; do
    [ -f "$WORK/$d/Cargo.toml" ] && top="$d"
    [ "$d" = . ] && break
    d="$(dirname "$d")"
  done
  [ -n "$top" ] || { echo "mutate-rust: no Cargo.toml above $f; skipping it" >&2; continue; }
  if [ "$top" = . ]; then printf '.\t%s\n' "$f"; else printf '%s\t%s\n' "$top" "${f#"$top"/}"; fi
done > "$RUN/groups.tsv"
[ -s "$RUN/groups.tsv" ] || die "no target has a Cargo.toml above it"
cut -f1 "$RUN/groups.tsv" | sort -u > "$RUN/roots.txt"

# drift <records ndjson> <raw mutant count> <where> : fail closed when the report
# no longer matches the format this adapter knows.
drift() {
  nl="$(jq -s '[.[] | select((.line | type) != "number")] | length' "$1")"
  [ "$nl" = 0 ] || die "$nl mutant(s) with a missing or non-numeric line in $3 (report format drift?)"
  bad="$(jq -r 'select(.status | startswith("?")) | .status[1:]' "$1" | sort -u | tr '\n' ' ')"
  [ -z "$bad" ] || die "unknown outcome(s) in $3: $bad(report format drift?)"
  [ "$2" -eq 0 ] || [ -s "$1" ] || die "$2 mutant(s) reported in $3 but none map to a target file (report format drift?)"
}

: > "$RUN/all.ndjson"
i=0
while IFS= read -r root; do
  i=$((i + 1))
  diff="$RUN/in-$i.diff"
  out="$RUN/run-$i"
  awk -F '\t' -v r="$root" '$1 == r { print $2 }' "$RUN/groups.tsv" > "$RUN/files-$i.txt"
  # shellcheck disable=SC2046 # one pathspec per target line
  (cd "$WORK/$root" && git diff --relative "$SHA" -- $(cat "$RUN/files-$i.txt")) > "$diff"
  if [ ! -s "$diff" ]; then
    echo "mutate-rust: no diff for the targets under $root; nothing to mutate there" >&2
    continue
  fi
  # shellcheck disable=SC2046 # one flag or value per line
  sbx --rw "$RUN" --cwd "$WORK/$root" -- cargo mutants --in-diff "$diff" --output "$out" $(cat "$RUN/test-args.txt") < /dev/null >&2 \
    || echo "mutate-rust: cargo-mutants exited non-zero under $root" >&2
  rep="$out/mutants.out/outcomes.json"
  jq -e '(.outcomes | type) == "array"' "$rep" >/dev/null 2>&1 || die "cargo-mutants wrote no usable outcomes.json under $root"
  awk -F '\t' -v r="$root" '$1 == r { print (r == "." ? $2 : r "/" $2) }' "$RUN/groups.tsv" | jq -R . | jq -s . > "$RUN/want-$i.json"
  jq -c --arg r "$root" --slurpfile want "$RUN/want-$i.json" '
    {"CaughtMutant": "killed", "MissedMutant": "survived", "Timeout": "timeout", "Unviable": "excluded"} as $map
    | .outcomes[] | select((.scenario | type) == "object" and .scenario.Mutant != null)
    | .summary as $s | .scenario.Mutant
    | (if $r == "." then .file else "\($r)/\(.file)" end) as $f
    | select($f | IN($want[0][]))
    | {file: $f, line: .span.start.line, col: (.span.start.column | if type == "number" then . else 0 end), mutator: .genre, replacement, status: ($map[$s] // "?\($s)")}' "$rep" > "$RUN/mapped-$i.ndjson"
  drift "$RUN/mapped-$i.ndjson" "$(jq '[.outcomes[] | select((.scenario | type) == "object" and .scenario.Mutant != null)] | length' "$rep")" "the outcomes under $root"
  cat "$RUN/mapped-$i.ndjson" >> "$RUN/all.ndjson"
done < "$RUN/roots.txt"

jq -s . "$RUN/all.ndjson" > "$OUT"
jq -n --slurpfile l "$OUT" '{language: "rust", tool: "cargo-mutants", mutants: ($l[0] | length)}'
