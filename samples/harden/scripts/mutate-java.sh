#!/bin/sh
# Java mutation adapter (PIT via Maven).
#
#   mutate-java.sh <targets json file> <test command> <out json>
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace). Targets are
# changed .java files relative to work_dir. Writes the normalized list [{file,
# line, col, mutator, replacement, status}] to <out json> and emits {language, tool,
# mutants}.
#
# PIT has no free diff mode, so scoping is the target list: each target maps to
# its fully qualified class (package declaration + file name) and goes into
# -DtargetClasses as "<class>,<class>$*", so inner and anonymous classes are
# mutated too (a bare "<class>*" would also take unrelated classes such as
# <class>Test or <class>Builder). PIT with JUnit 5 needs pitest-junit5-plugin
# declared in the pom, and the worktree must not be edited, so PIT runs in a
# copy of HEAD (git archive) under {{workspace}}/harden/java. Where the copy's
# pom has no pitest-maven plugin, one is added to the top-level <build><plugins>
# (never pluginManagement or a profile), with the JUnit 5 plugin when the pom
# uses junit-jupiter. Each target's module is its nearest pom.xml; targets with
# no pom.xml above them are skipped (Gradle is not supported, and detect.sh
# skips Gradle-only Java). PIT runs the build's own test framework, so the
# test command cannot be injected. Reports: -DoutputFormats=XML
# -DreportsDirectory=<dir> -DtimestampedReports=false → <dir>/mutations.xml.
# Status: KILLED and MEMORY_ERROR killed, SURVIVED survived, NO_COVERAGE
# no_coverage, TIMED_OUT timeout; RUN_ERROR, NON_VIABLE, STARTED and NOT_STARTED
# excluded. mutator is the mutator's class name; replacement is PIT's
# description.
#
# Fails closed (exit 1) on report format drift: a missing or non-numeric line,
# an unknown status in a target class, mutants reported none of which map to a
# target class, or no target with a pom.xml above it.
#
# The worktree must be clean on entry; on exit it is restored with
# `git checkout -- .` and `git clean -fd` (never -x).
set -eu

die() { echo "mutate-java: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: mutate-java.sh <targets json file> <test command> <out json>"
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

PIT_VERSION=1.30.1
JUNIT5_PLUGIN_VERSION=1.2.2

command -v mvn >/dev/null 2>&1 || die "mvn not found on PATH (PIT runs through Maven)"
[ -n "$WORK" ] && [ -n "$WS" ] || die "work_dir and workspace must be set"
jq -e 'type == "array"' "$TARGETS" >/dev/null 2>&1 || die "targets file is not a JSON array: $TARGETS"
[ -z "$(git -C "$WORK" status --porcelain)" ] || die "worktree not clean: $WORK"

restore() { git -C "$WORK" reset -q; git -C "$WORK" checkout -q -- .; git -C "$WORK" clean -fdq; }
trap restore EXIT
trap 'exit 1' HUP INT TERM

case "$CMD" in ''|'mvn test') ;; *) echo "mutate-java: PIT runs the build's own tests; ignoring test command '$CMD'" >&2 ;; esac

RUN="$WS/harden/java"
rm -rf "$RUN"
mkdir -p "$RUN/tree"

# Group targets by Maven module: "<module dir>\t<file>\t<fqcn>" lines.
jq -r '.[]' "$TARGETS" > "$RUN/targets.txt"
: > "$RUN/groups.tsv"
while IFS= read -r f; do
  d="$(dirname "$f")"
  while [ ! -f "$WORK/$d/pom.xml" ] && [ "$d" != . ]; do d="$(dirname "$d")"; done
  [ -f "$WORK/$d/pom.xml" ] || { echo "mutate-java: no pom.xml above $f; skipping it" >&2; continue; }
  pkg="$(sed -n 's/^[[:space:]]*package[[:space:]]*\([A-Za-z0-9_.]*\)[[:space:]]*;.*/\1/p' "$WORK/$f" | head -1)"
  cls="$(basename "$f" .java)"
  printf '%s\t%s\t%s\n' "$d" "$f" "${pkg:+$pkg.}$cls" >> "$RUN/groups.tsv"
done < "$RUN/targets.txt"
[ -s "$RUN/groups.tsv" ] || die "no target has a pom.xml above it: the PIT adapter supports Maven projects only (Gradle is not supported)"
cut -f1 "$RUN/groups.tsv" | sort -u > "$RUN/modules.txt"

# drift <records ndjson> <raw mutant count> <where> : fail closed when the report
# no longer matches the format this adapter knows.
drift() {
  nl="$(jq -s '[.[] | select((.line | type) != "number")] | length' "$1")"
  [ "$nl" = 0 ] || die "$nl mutant(s) with a missing or non-numeric line in $3 (report format drift?)"
  bad="$(jq -r 'select(.status | startswith("?")) | .status[1:]' "$1" | sort -u | tr '\n' ' ')"
  [ -z "$bad" ] || die "unknown status(es) in $3: $bad(report format drift?)"
  [ "$2" -eq 0 ] || [ -s "$1" ] || die "$2 mutant(s) reported in $3 but none map to a target class (report format drift?)"
}

git -C "$WORK" archive HEAD | tar -x -C "$RUN/tree"

# ensure_pit <pom> : declare pitest-maven (and the JUnit 5 plugin when needed) in
# the top-level build plugins of <pom>, unless pitest-maven is already there.
ensure_pit() {
  grep -q '<artifactId>pitest-maven</artifactId>' "$1" && return 0
  junit5=0
  grep -q 'junit-jupiter' "$1" && junit5=1
  awk -v v="$PIT_VERSION" -v jv="$JUNIT5_PLUGIN_VERSION" -v j5="$junit5" '
    function plugin(ind) {
      print ind "<plugin>"
      print ind "  <groupId>org.pitest</groupId>"
      print ind "  <artifactId>pitest-maven</artifactId>"
      print ind "  <version>" v "</version>"
      if (j5) {
        print ind "  <dependencies>"
        print ind "    <dependency>"
        print ind "      <groupId>org.pitest</groupId>"
        print ind "      <artifactId>pitest-junit5-plugin</artifactId>"
        print ind "      <version>" jv "</version>"
        print ind "    </dependency>"
        print ind "  </dependencies>"
      }
      print ind "</plugin>"
    }
    { lines[NR] = $0 }
    END {
      pm = 0; pr = 0; build = 0; at = 0; mode = ""
      for (i = 1; i <= NR; i++) {
        l = lines[i]
        if (l ~ /<pluginManagement>/) pm++
        if (l ~ /<profiles>/) pr++
        if (pm == 0 && pr == 0) {
          if (l ~ /<build>/) build = 1
          if (build && l ~ /<\/plugins>/ && at == 0) { at = i; mode = "plugins" }
          if (build && l ~ /<\/build>/ && at == 0) { at = i; mode = "build" }
        }
        if (l ~ /<\/pluginManagement>/) pm--
        if (l ~ /<\/profiles>/) pr--
      }
      if (at == 0) {
        for (i = NR; i >= 1; i--) if (lines[i] ~ /<\/project>/) { at = i; mode = "project"; break }
      }
      for (i = 1; i <= NR; i++) {
        if (i == at) {
          if (mode == "plugins") plugin("      ")
          else if (mode == "build") { print "    <plugins>"; plugin("      "); print "    </plugins>" }
          else { print "  <build>"; print "    <plugins>"; plugin("      "); print "    </plugins>"; print "  </build>" }
        }
        print lines[i]
      }
    }' "$1" > "$1.tmp"
  mv "$1.tmp" "$1"
}

: > "$RUN/all.ndjson"
i=0
while IFS= read -r mod; do
  i=$((i + 1))
  c="$RUN/tree/$mod"
  rep="$RUN/report-$i"
  ensure_pit "$c/pom.xml"
  awk -F '\t' -v m="$mod" '$1 == m { print $3 "\t" $2 }' "$RUN/groups.tsv" > "$RUN/classes-$i.tsv"
  # Each class plus its inner and anonymous classes (Outer$Inner, Outer$1).
  classes="$(cut -f1 "$RUN/classes-$i.tsv" | sed 's/.*/&,&$*/' | tr '\n' ',' | sed 's/,$//')"
  sbx --rw "$RUN" --cwd "$c" -- mvn -q -B test-compile org.pitest:pitest-maven:mutationCoverage "-DtargetClasses=$classes" \
      -DoutputFormats=XML "-DreportsDirectory=$rep" -DtimestampedReports=false < /dev/null >&2 \
    || echo "mutate-java: Maven exited non-zero in $mod" >&2
  [ -f "$rep/mutations.xml" ] || die "PIT wrote no mutations.xml for module $mod"
  # One record per <mutation>: status, mutatedClass, lineNumber, mutator, description.
  awk '
    function tag(s, t,   a, b) {
      a = index(s, "<" t ">"); if (a == 0) return ""
      s = substr(s, a + length(t) + 2); b = index(s, "</" t ">")
      return b ? substr(s, 1, b - 1) : ""
    }
    function unxml(s) {
      gsub(/&lt;/, "<", s); gsub(/&gt;/, ">", s); gsub(/&quot;/, "\"", s); gsub(/&apos;/, "\047", s); gsub(/&amp;/, "\\&", s)
      return s
    }
    { buf = buf $0 " " }
    END {
      while ((a = index(buf, "<mutation ")) > 0) {
        buf = substr(buf, a); b = index(buf, "</mutation>")
        if (b == 0) break
        rec = substr(buf, 1, b - 1); buf = substr(buf, b + 11)
        st = substr(rec, index(rec, "status=") + 8); sub(/[\047"].*$/, "", st)
        printf "%s\037%s\037%s\037%s\037%s\n", st, tag(rec, "mutatedClass"), tag(rec, "lineNumber"), tag(rec, "mutator"), unxml(tag(rec, "description"))
      }
    }' "$rep/mutations.xml" > "$RUN/raw-$i.txt"
  jq -R -c --rawfile classes "$RUN/classes-$i.tsv" '
      ($classes | split("\n") | map(select(length > 0) | split("\t") | {key: .[0], value: .[1]}) | from_entries) as $files
      | {"KILLED": "killed", "MEMORY_ERROR": "killed", "SURVIVED": "survived", "NO_COVERAGE": "no_coverage", "TIMED_OUT": "timeout"} as $map
      | split("\u001f") as [$st, $cls, $line, $mut, $desc]
      | ($files[$cls | sub("\\$.*$"; "")]) as $f
      | select($f != null)
      | {file: $f, line: ($line | tonumber? // null), col: 0, mutator: ($mut | split(".") | last), replacement: $desc,
         status: ($map[$st] // (if $st | IN("RUN_ERROR", "NON_VIABLE", "STARTED", "NOT_STARTED") then "excluded" else "?\($st)" end))}' \
    "$RUN/raw-$i.txt" > "$RUN/mapped-$i.ndjson"
  drift "$RUN/mapped-$i.ndjson" "$(wc -l < "$RUN/raw-$i.txt" | tr -d ' ')" "module $mod"
  cat "$RUN/mapped-$i.ndjson" >> "$RUN/all.ndjson"
done < "$RUN/modules.txt"

jq -s . "$RUN/all.ndjson" > "$OUT"
jq -n --slurpfile l "$OUT" '{language: "java", tool: "PIT", mutants: ($l[0] | length)}'
