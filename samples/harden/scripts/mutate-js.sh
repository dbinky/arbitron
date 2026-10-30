#!/bin/sh
# JS/TS mutation adapter (StrykerJS).
#
#   mutate-js.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace). Targets are
# changed source files relative to work_dir. Writes the normalized list [{file,
# line, col, mutator, replacement, status}] to <out json> and emits {language, tool,
# mutants}.
#
# StrykerJS has no diff mode, so scoping is the target list: for each target's
# package root (nearest package.json) a config under {{workspace}}/harden/js
# names the targets in `mutate`, injects the test command through the command
# test runner (coverageAnalysis off), and points jsonReporter.fileName and
# tempDirName outside the worktree; `stryker run <config>` runs from the package
# root, and the command runs from the package root of Stryker's sandbox copy.
#
# The baseline records its command relative to the REPOSITORY root (what the
# baseline agent ran from ./work), so for a package below the root it is
# translated, never injected as is: exactly one recognised leading
# `cd <package> &&` (or `cd ./<package>/ &&`) naming THIS package is stripped; a
# command with no cd is package-local (`npm run test:unit`) and used unchanged;
# any other cd (a different directory, a second cd) is ambiguous and exits 1
# with an actionable error rather than substituting some other test command, as
# is a command aimed at one package when several packages changed. For the root
# package the command is used as given. The binary is the package's node_modules/.bin/stryker, else stryker on
# PATH, else `npx --no-install stryker`. Status: Killed killed, Survived
# survived, NoCoverage no_coverage, Timeout timeout; CompileError, RuntimeError
# and Ignored excluded (StrykerJS has no --since, so Ignored always means a
# config or comment opt-out). Only mutants outside the targets are dropped; the
# count goes to stderr.
#
# Fails closed (exit 1) on report format drift: a missing or non-numeric line,
# an unknown status in a target file, mutants reported none of which map to a
# target, or no target with a package.json above it.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x).
set -eu

die() { echo "mutate-js: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-js.sh <targets json file> <test command> <out json>"
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

command -v stryker >/dev/null 2>&1 || command -v npx >/dev/null 2>&1 || die "stryker not found on PATH (nor npx)"
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
[ -n "$CMD" ] || die "no test command"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

RUN="$WS/harden/js"
rm -rf "$RUN"
mkdir -p "$RUN"

# pkg_command <package dir>: the baseline command as it must run from that
# package's root (see the header). Prints it, or exits 1 when it cannot be
# translated without guessing.
pkg_command() {
  [ "$1" != . ] || { printf '%s' "$CMD"; return 0; }
  lead="$(printf '%s' "$CMD" | sed -nE 's/^[[:space:]]*cd[[:space:]]+([^[:space:];&|]+)[[:space:]]*&&.*$/\1/p')"
  rest="$CMD"
  if [ -n "$lead" ]; then
    dir="${lead#./}"; dir="${dir%/}"
    [ "$dir" = "$1" ] || die "baseline command is root-relative (cd $lead) and does not belong to package $1; record a per-package command for $1"
    rest="$(printf '%s' "$CMD" | sed -E 's/^[[:space:]]*cd[[:space:]]+[^[:space:];&|]+[[:space:]]*&&[[:space:]]*//')"
    [ -n "$rest" ] || die "baseline command for package $1 is only a cd"
  fi
  if printf '%s' "$rest" | grep -Eq '(^|[;&|(])[[:space:]]*cd([[:space:]]|$)'; then
    die "baseline command for js is root-relative (it changes directory: $CMD); record a per-package command for $1"
  fi
  printf '%s' "$rest"
}

# Group targets by package root: "<package dir>\t<file>" lines.
jq -r '.[]' "$TARGETS" | while IFS= read -r f; do
  d="$(dirname "$f")"
  while [ ! -f "$WORK/$d/package.json" ] && [ "$d" != . ]; do d="$(dirname "$d")"; done
  [ -f "$WORK/$d/package.json" ] || { echo "mutate-js: no package.json above $f; skipping it" >&2; continue; }
  printf '%s\t%s\n' "$d" "$f"
done > "$RUN/groups.tsv"
[ -s "$RUN/groups.tsv" ] || die "no target has a package.json above it"
cut -f1 "$RUN/groups.tsv" | sort -u > "$RUN/packages.txt"
jq -c . "$TARGETS" > "$RUN/targets.json"

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
i=0
while IFS= read -r pkg; do
  i=$((i + 1))
  g="$RUN/run-$i"
  mkdir -p "$g"
  awk -F '\t' -v p="$pkg" '$1 == p { f = $2; if (p != ".") f = substr(f, length(p) + 2); print f }' "$RUN/groups.tsv" \
    | jq -R . | jq -s . > "$g/mutate.json"
  PKG_CMD="$(pkg_command "$pkg")" || exit 1
  jq -n --slurpfile m "$g/mutate.json" --arg cmd "$PKG_CMD" --arg rep "$g/mutation.json" --arg tmp "$g/tmp" \
    '{mutate: $m[0], testRunner: "command", commandRunner: {command: $cmd}, coverageAnalysis: "off",
      reporters: ["json"], jsonReporter: {fileName: $rep}, tempDirName: $tmp}' > "$g/stryker.conf.json"
  if [ -x "$WORK/$pkg/node_modules/.bin/stryker" ]; then
    set -- "$WORK/$pkg/node_modules/.bin/stryker"
  elif command -v stryker >/dev/null 2>&1; then
    set -- stryker
  else
    set -- npx --no-install stryker
  fi
  sbx --rw "$g" --cwd "$WORK/$pkg" -- "$@" run "$g/stryker.conf.json" < /dev/null >&2 \
    || echo "mutate-js: StrykerJS exited non-zero in $pkg" >&2
  jq -e '(.files | type) == "object"' "$g/mutation.json" >/dev/null 2>&1 || die "StrykerJS wrote no usable report for package $pkg"
  jq -c --arg p "$pkg" --arg w "$(cd "$WORK/$pkg" && pwd -P)/" --slurpfile want "$RUN/targets.json" '
    {"Killed": "killed", "Survived": "survived", "NoCoverage": "no_coverage", "Timeout": "timeout",
     "CompileError": "excluded", "RuntimeError": "excluded", "Ignored": "excluded"} as $map
    | .files | to_entries[]
    | (.key | if startswith($w) then .[($w | length):] else ltrimstr("./") end) as $rel
    | (if $p == "." then $rel else "\($p)/\($rel)" end) as $f
    | select($f | IN($want[0][]))
    | .value.mutants[]
    | {file: $f, line: .location.start.line, col: (.location.start.column | if type == "number" then . else 0 end), mutator: .mutatorName, replacement, status: ($map[.status] // "?\(.status)")}' \
    "$g/mutation.json" > "$g/mapped.ndjson"
  raw="$(jq '[.files[].mutants[]] | length' "$g/mutation.json")"
  echo "mutate-js: dropped $((raw - $(wc -l < "$g/mapped.ndjson"))) mutant(s) outside the targets in package $pkg" >&2
  drift "$g/mapped.ndjson" "$raw" "package $pkg"
  cat "$g/mapped.ndjson" >> "$RUN/all.ndjson"
done < "$RUN/packages.txt"

jq -s . "$RUN/all.ndjson" > "$OUT"
jq -n --slurpfile l "$OUT" '{language: "js", tool: "StrykerJS", mutants: ($l[0] | length)}'
