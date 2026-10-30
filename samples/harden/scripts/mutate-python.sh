#!/bin/sh
# Python mutation adapter (mutmut 3).
#
#   mutate-python.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace). Targets are
# changed .py files relative to work_dir. Writes the normalized list [{file,
# line, col, mutator, replacement, status}] to <out json> and emits {language, tool,
# mutants}.
#
# mutmut has no diff mode and reads its config only from ./pyproject.toml
# [tool.mutmut] or ./setup.cfg [mutmut] in its cwd, so it runs in a copy of HEAD
# (git archive) under {{workspace}}/harden/python. In the copy, each project root
# (nearest pyproject.toml, setup.py, setup.cfg or requirements.txt; else the
# repository root) gets a generated [mutmut] section in setup.cfg (source_paths =
# the targets' top-level packages, only_mutate = the targets) that replaces any
# project [mutmut] / [tool.mutmut] section. Sources must import as top-level
# packages. mutmut runs pytest itself, so the test command cannot be injected.
#
# Results come from mutants/<path>.py.meta exit_code_by_key: 0 survived; 5 or 33
# no_coverage (no tests); 24, 36, 152, 255 or -24 timeout; null (not checked)
# or 34 (skipped) excluded; 1 or 3 killed. Any other code is excluded with a
# warning, never counted as a kill. `mutmut show <key>` gives the
# diff: replacement is its first added line, and the line is where the removed
# line sits in the source, searched from the mutated function's def. mutmut has
# no mutator names, so mutator is "mutmut".
#
# Fails closed (exit 1) on report format drift: a malformed .meta, a `mutmut
# show` diff with no removed line (so no line number), or mutants reported none
# of which belong to a target file.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x).
set -eu

die() { echo "mutate-python: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-python.sh <targets json file> <test command> <out json>"
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

if command -v mutmut >/dev/null 2>&1; then
  MM="mutmut"
elif command -v python3 >/dev/null 2>&1 && sbx --cwd "$WS" -- python3 -c 'import mutmut' >/dev/null 2>&1; then
  MM="python3 -m mutmut"
else
  die "mutmut not found on PATH (nor as python3 -m mutmut)"
fi
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

case "$CMD" in ''|pytest*|'python -m pytest'*|'python3 -m pytest'*) ;;
  *) echo "mutate-python: mutmut always runs pytest; ignoring test command '$CMD'" >&2 ;; esac

RUN="$WS/harden/python"
rm -rf "$RUN"
mkdir -p "$RUN/tree"
git -C "$WORK" archive HEAD | tar -x -C "$RUN/tree"

is_root() {
  for m in pyproject.toml setup.py setup.cfg requirements.txt; do [ -f "$WORK/$1/$m" ] && return 0; done
  return 1
}

# Group targets by project root: "<root>\t<file relative to root>" lines.
jq -r '.[]' "$TARGETS" | while IFS= read -r f; do
  d="$(dirname "$f")"
  while ! is_root "$d" && [ "$d" != . ]; do d="$(dirname "$d")"; done
  if [ "$d" = . ]; then printf '.\t%s\n' "$f"; else printf '%s\t%s\n' "$d" "${f#"$d"/}"; fi
done > "$RUN/groups.tsv"
cut -f1 "$RUN/groups.tsv" | sort -u > "$RUN/roots.txt"

# drop_section <file> <header> : remove an INI/TOML section (header line up to the next [section]).
drop_section() {
  [ -f "$1" ] || return 0
  awk -v h="$2" '/^[[:space:]]*\[/ { skip = ($0 ~ "^[[:space:]]*\\[" h "\\][[:space:]]*$") } !skip' "$1" > "$1.tmp"
  mv "$1.tmp" "$1"
}

# locate <source file> <function> <removed text> <hunk-relative line> : prints the absolute line.
locate() {
  awk -v fn="$2" -v want="$3" -v rel="$4" '
    def == 0 && $0 ~ ("^[ \t]*(async[ \t]+)?def[ \t]+" fn "[ \t]*\\(") { def = NR }
    def > 0 && $0 == want { print NR; found = 1; exit }
    END { if (!found) print (def > 0 ? def + rel - 1 : rel) }' "$1"
}

US="$(printf '\037')"
: > "$RUN/all.ndjson"
while IFS= read -r root; do
  c="$RUN/tree/$root"
  awk -F '\t' -v r="$root" '$1 == r { print $2 }' "$RUN/groups.tsv" > "$RUN/files.txt"
  drop_section "$c/setup.cfg" mutmut
  drop_section "$c/pyproject.toml" tool.mutmut
  sep=""
  [ -s "$c/setup.cfg" ] && sep=yes
  {
    [ -z "$sep" ] || echo
    echo '[mutmut]'
    echo 'source_paths='
    cut -d/ -f1 "$RUN/files.txt" | sort -u | sed 's/^/    /'
    echo 'only_mutate='
    sed 's/^/    /' "$RUN/files.txt"
  } >> "$c/setup.cfg"

  # shellcheck disable=SC2086 # MM is "mutmut" or "python3 -m mutmut"
  sbx --rw "$c" --cwd "$c" -- $MM run < /dev/null >&2 || echo "mutate-python: mutmut exited non-zero in $root" >&2
  [ -d "$c/mutants" ] || die "mutmut wrote no mutants directory for $root"

  find "$c/mutants" -name '*.py.meta' | sort > "$RUN/metas.txt"
  [ -s "$RUN/metas.txt" ] || die "mutmut wrote no .meta results for $root"
  raw=0
  mapped=0
  while IFS= read -r meta; do
    jq -e '(.exit_code_by_key | type) == "object"' "$meta" >/dev/null 2>&1 || die "malformed mutmut result: $meta"
    raw=$((raw + $(jq '.exit_code_by_key | length' "$meta")))
    rel="${meta#"$c/mutants/"}"
    rel="${rel%.meta}"
    grep -Fxq "$rel" "$RUN/files.txt" || continue
    mapped=$((mapped + $(jq '.exit_code_by_key | length' "$meta")))
    if [ "$root" = . ]; then file="$rel"; else file="$root/$rel"; fi
    jq -r '.exit_code_by_key | to_entries[]
      | [.key, (.value as $c
          | if $c == null or $c == 34 then "excluded"
            elif $c == 0 then "survived"
            elif $c == 5 or $c == 33 then "no_coverage"
            elif $c == 24 or $c == 36 or $c == 152 or $c == 255 or $c == -24 then "timeout"
            elif $c == 1 or $c == 3 then "killed"
            else "?\($c)" end)] | @tsv' "$meta" > "$RUN/keys.tsv"
    while IFS="$(printf '\t')" read -r key st; do
      case "$st" in
        \?*) echo "mutate-python: unknown mutmut exit code ${st#?} for $key; counting it as excluded" >&2; st=excluded ;;
      esac
      # shellcheck disable=SC2086
      sbx --rw "$c" --cwd "$c" -- $MM show "$key" < /dev/null > "$RUN/show.txt" 2>/dev/null || die "mutmut show failed for $key"
      fn="${key%__mutmut_*}"
      fn="${fn##*.}"
      fn="${fn##*ǁ}"
      fn="${fn#x_}"
      parsed="$(awk '
        /^@@/ { split($2, a, ","); n = 0; inhunk = 1; next }
        inhunk && /^-/ && minus == "" { minus = substr($0, 2); line = n + 1 }
        inhunk && /^\+/ && plus == "" { plus = substr($0, 2) }
        inhunk && /^[ -]/ { n++ }
        END { printf "%s\037%s\037%s\n", line, minus, plus }' "$RUN/show.txt")"
      relline="$(printf '%s' "$parsed" | cut -d "$US" -f1)"
      [ -n "$relline" ] || die "no removed line in 'mutmut show $key', so its line is unknown (report format drift?)"
      minus="$(printf '%s' "$parsed" | cut -d "$US" -f2)"
      plus="$(printf '%s' "$parsed" | cut -d "$US" -f3)"
      line="$(locate "$c/$rel" "$fn" "$minus" "$relline")"
      jq -nc --arg f "$file" --argjson l "$line" --arg r "$plus" --arg s "$st" \
        '{file: $f, line: $l, col: 0, mutator: "mutmut", replacement: ($r | sub("^\\s+"; "") | sub("\\s+$"; "")), status: $s}' >> "$RUN/all.ndjson"
    done < "$RUN/keys.tsv"
  done < "$RUN/metas.txt"
  [ "$raw" -eq 0 ] || [ "$mapped" -gt 0 ] \
    || die "$raw mutant(s) reported under $root but none map to a target file (report format drift?)"
done < "$RUN/roots.txt"

jq -s . "$RUN/all.ndjson" > "$OUT"
jq -n --slurpfile l "$OUT" '{language: "python", tool: "mutmut", mutants: ($l[0] | length)}'
