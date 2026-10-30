#!/bin/sh
# Decides which languages harden can mutate. The version probes execute
# repository-controlled programs (a project-local stryker, a dotnet local tool),
# so they run through sandbox.sh and detect needs {{workspace}} for its scratch
# space; without an isolation mechanism it exits 1 before probing anything. Reads the variable snapshot as JSON
# on stdin (work_dir, classify, resolve-inputs.inputs.baseline_path) and emits
# {languages:[{language, tool, version, version_status, version_warning,
# runner_version, status, reason, targets, test_command, test_command_source}],
# runnable_count, skip_reason, warnings}. The registry is
# mutation-tools.json beside this script.
#
# For each language with changed source files (classify.source_files_by_language,
# restricted to files still present in work_dir):
#   - no registry entry          -> skipped
#   - probe fails                -> collected; exit 1 listing every install_hint
#   - Java with no pom.xml       -> skipped (the PIT adapter is Maven-only)
#   - C# with no referencing
#     test project               -> skipped
#   - otherwise                  -> runnable
# Tool versions are checked against the registry's tested_version, on the
# executable the probe ran (a project-local tool in the changed files' package
# for JS, a local dotnet tool for C#). A version is supported when it is the
# tested one or newer within the same major (for 0.x tools, within the same
# minor). An older, newer-major or unparseable version stays runnable but is
# reported, never silently accepted: the language gets version_status
# "unsupported" / "unparseable" and a version_warning, the warning is printed to
# stderr, and the output lists every warning under `warnings`. PIT is invoked as
# org.pitest:pitest-maven:<pinned version>, so its version is the pinned
# tested_version (version_source "pinned"); the probe's output is Maven's and is
# reported separately as runner_version.
# The test command comes from the baseline suite when there is one, else the
# registry default (test_command_source "baseline" / "registry-default").
# Baseline suite choice, in order: a suite for the language whose paths cover the
# changed files; a covering suite whose command matches no known language (e.g.
# "make test"); the first suite for the language. So a covering unknown-command
# suite beats a non-covering language-matching one.
set -eu

HERE="$(dirname "$0")"
REG="$HERE/mutation-tools.json"
[ -f "$REG" ] || { echo "detect: registry not found: $REG" >&2; exit 1; }

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""')"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"
BASELINE_PATH="$(printf '%s' "$IN" | jq -r '."resolve-inputs".inputs.baseline_path // ""')"
[ -n "$WORK" ] && [ -d "$WORK" ] || { echo "detect: work_dir not set or not a directory" >&2; exit 1; }
SBX="$HERE/sandbox.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
: > "$TMP/langs.ndjson"
: > "$TMP/hints"
: > "$TMP/warnings"

# version_status <found> <tested>: supported | unsupported | unparseable.
# Supported: same major as tested and not older (for a 0.x tested version: same
# minor too). An empty tested version means the registry pins nothing to check.
version_status() {
  [ -n "$2" ] || { echo supported; return; }
  case "$1" in [0-9]*) ;; *) echo unparseable; return ;; esac
  fmaj="${1%%.*}"; tmaj="${2%%.*}"
  frest="${1#*.}"; fmin="${frest%%.*}"
  trest="${2#*.}"; tmin="${trest%%.*}"
  if [ "$fmaj" != "$tmaj" ]; then echo unsupported
  elif [ "$tmaj" = 0 ] && [ "$fmin" != "$tmin" ]; then echo unsupported
  elif [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" != "$2" ]; then echo unsupported
  else echo supported; fi
}

# Collapses ., .. and empty components in a slash-separated relative path.
norm() {
  printf '%s\n' "$1" | awk -F/ '{
    n = 0
    for (i = 1; i <= NF; i++) {
      if ($i == "" || $i == ".") continue
      if ($i == ".." && n > 0 && st[n] != "..") { n--; continue }
      st[++n] = $i
    }
    out = ""
    for (i = 1; i <= n; i++) out = out (i > 1 ? "/" : "") st[i]
    print out
  }'
}

# Nearest directory at or above $1's directory (inside work_dir) holding a file
# of the given name; prints it relative to work_dir, "." for the root.
nearest_dir_with() {
  d="$(dirname "$1")"
  while :; do
    [ -f "$WORK/$d/$2" ] && { printf '%s\n' "$d"; return 0; }
    [ "$d" = "." ] && return 1
    d="$(dirname "$d")"
  done
}

# Test command from the baseline: $1 language, $2 JSON array of changed files.
# Prints the command, or nothing when the baseline offers none.
baseline_command() {
  [ -n "$BASELINE_PATH" ] || return 0
  case "$BASELINE_PATH" in
    /*) bf="$BASELINE_PATH" ;;
    *) bf="$WORK/$BASELINE_PATH" ;;
  esac
  [ -f "$bf" ] || return 0
  if ! jq -e . "$bf" >/dev/null 2>&1; then
    echo "detect: baseline $BASELINE_PATH is not valid JSON; using the registry default" >&2
    return 0
  fi
  jq -r --arg L "$1" --argjson files "$2" '
    {go: "(^|[ ;&|])go +test", csharp: "dotnet", js: "npm|npx|yarn|pnpm|jest|vitest|mocha|node",
     python: "pytest|python|tox|unittest", java: "mvn|gradle", rust: "cargo"} as $re
    | def norm: ltrimstr("./") | sub("/+$"; "") | sub("/?\\*+$"; "");
      def covers: (.paths // []) as $ps
        | any($ps[]; (. | norm) as $p
            | ($p == "" or $p == "." or any($files[]; . == $p or startswith($p + "/"))));
      def mine: (.language // "") == $L
        or ((.language // "") == "" and (.command | test($re[$L])));
      def unknown: (.language // "") == ""
        and ([$re[] as $r | select(.command | test($r))] | length == 0);
      if type == "object" and (.suites | type) == "array" then
        [.suites[] | select((.command | type) == "string" and (.command | length) > 0)] as $s
        | ( [$s[] | select(mine and covers)][0]
            // [$s[] | select(unknown and covers)][0]
            // [$s[] | select(mine)][0] ).command // empty
      else empty end' "$bf" 2>/dev/null || true
}

# Registry default test command for $1 language; $2 = a changed file (for java).
default_command() {
  kind="$(jq -r --arg l "$1" '.tools[] | select(.language == $l) | .default_test_command | type' "$REG")"
  if [ "$kind" = string ]; then
    jq -r --arg l "$1" '.tools[] | select(.language == $l) | .default_test_command' "$REG"
    return
  fi
  for m in $(jq -r --arg l "$1" '.tools[] | select(.language == $l) | .default_test_command | keys_unsorted[]' "$REG"); do
    if nearest_dir_with "$2" "$m" >/dev/null; then
      jq -r --arg l "$1" --arg m "$m" '.tools[] | select(.language == $l) | .default_test_command[$m]' "$REG"
      return
    fi
  done
  jq -r --arg l "$1" '.tools[] | select(.language == $l) | .default_test_command | to_entries[0].value' "$REG"
}

# C# targets: test projects that ProjectReference a project owning a changed file.
csharp_targets() {
  : > "$TMP/cs.changed"
  while IFS= read -r f; do
    d="$(dirname "$f")"
    while :; do
      found=""
      for p in "$WORK/$d"/*.csproj; do
        [ -f "$p" ] && { found="$(norm "$d/$(basename "$p")")"; break; }
      done
      if [ -n "$found" ]; then printf '%s\n' "$found" >> "$TMP/cs.changed"; break; fi
      [ "$d" = "." ] && break
      d="$(dirname "$d")"
    done
  done
  : > "$TMP/cs.targets"
  [ -s "$TMP/cs.changed" ] || return 0
  find "$WORK" \( -name node_modules -o -name .git -o -name bin -o -name obj \) -prune -o -name '*.csproj' -print | sort |
  while IFS= read -r abs; do
    rel="${abs#"$WORK"/}"
    grep -Eq 'Microsoft\.NET\.Test\.Sdk|<IsTestProject>[[:space:]]*true' "$abs" || continue
    grep '<ProjectReference' "$abs" | sed -n 's/.*Include="\([^"]*\)".*/\1/p' | tr "\\\\" '/' |
    while IFS= read -r inc; do
      if grep -Fxq "$(norm "$(dirname "$rel")/$inc")" "$TMP/cs.changed"; then
        printf '%s\n' "$rel" >> "$TMP/cs.targets"
        break
      fi
    done
  done
  return 0
}

# Probing runs repository code: refuse up front, with the real reason, when no
# isolation can be built (rather than reporting every tool as missing).
if [ "$(printf '%s' "$IN" | jq -r '(.classify.source_files_by_language // {}) | length')" != 0 ]; then
  [ -n "$WS" ] && [ -d "$WS" ] || { echo "detect: workspace not set or not a directory" >&2; exit 1; }
  sh "$SBX" --check || exit 1
fi

for L in $(printf '%s' "$IN" | jq -r '.classify.source_files_by_language // {} | keys[]'); do
  # Only files still on disk can be mutated; a deleted source has nothing to test.
  : > "$TMP/files"
  printf '%s' "$IN" | jq -r --arg l "$L" '.classify.source_files_by_language[$l][]' |
  while IFS= read -r f; do
    if [ -f "$WORK/$f" ]; then printf '%s\n' "$f" >> "$TMP/files"; fi
  done
  [ -s "$TMP/files" ] || continue
  FILES="$(jq -Rn '[inputs]' < "$TMP/files")"

  TOOL="$(jq -r --arg l "$L" '.tools[] | select(.language == $l) | .tool' "$REG")"
  if [ -z "$TOOL" ]; then
    jq -nc --arg l "$L" --argjson f "$FILES" '{language:$l, tool:"", version:"", status:"skipped",
      reason:("no mutation tool registered for " + $l), targets:[], test_command:"", test_command_source:""}' >> "$TMP/langs.ndjson"
    continue
  fi

  # The PIT adapter drives Maven only: Java files with no pom.xml above them are
  # dropped, and with none left the language is skipped before any probe.
  if [ "$L" = java ]; then
    : > "$TMP/files.mvn"
    while IFS= read -r f; do
      if nearest_dir_with "$f" pom.xml >/dev/null; then printf '%s\n' "$f" >> "$TMP/files.mvn"; fi
    done < "$TMP/files"
    if [ ! -s "$TMP/files.mvn" ]; then
      jq -nc --arg t "$TOOL" '{language:"java", tool:$t, version:"", status:"skipped",
        reason:"PIT adapter supports Maven projects only; Gradle not supported", targets:[], test_command:"", test_command_source:""}' >> "$TMP/langs.ndjson"
      continue
    fi
    cat "$TMP/files.mvn" > "$TMP/files"
    FILES="$(jq -Rn '[inputs]' < "$TMP/files")"
  fi

  # The probe runs where the adapter will: in work_dir, or for JS in the changed
  # files' package, so a repo-local tool (npx --no-install, a dotnet local tool)
  # is found. The probe is the command the adapter runs, not a listing.
  PROBE="$(jq -r --arg l "$L" '.tools[] | select(.language == $l) | .probe' "$REG")"
  PDIR=.
  if [ "$L" = js ]; then PDIR="$(nearest_dir_with "$(head -1 "$TMP/files")" package.json || echo .)"; fi
  prc=0
  POUT="$(sh "$SBX" --run-dir "$WS" --cwd "$WORK/$PDIR" -- sh -c "$PROBE" 2>&1 < /dev/null)" || prc=$?
  if [ "$prc" -eq 125 ]; then
    # The sandbox itself failed (not the tool): say so rather than "tool missing".
    printf '%s\n' "$POUT" >&2
    exit 1
  fi
  if [ "$prc" -ne 0 ]; then
    jq -r --arg l "$L" '.tools[] | select(.language == $l) | "\(.tool) (\(.language)): \(.install_hint)"' "$REG" >> "$TMP/hints"
    continue
  fi
  VERSION="$(printf '%s\n' "$POUT" | grep -Eo '[0-9]+\.[0-9]+(\.[0-9]+)*' | head -1 || true)"
  TESTED="$(jq -r --arg l "$L" '.tools[] | select(.language == $l) | .tested_version // ""' "$REG")"
  RUNNER_VERSION=""
  if [ "$(jq -r --arg l "$L" '.tools[] | select(.language == $l) | .version_source // "probe"' "$REG")" = pinned ]; then
    # The probe printed the runner's version (Maven's), not the tool's.
    RUNNER_VERSION="$VERSION"
    VERSION="$TESTED"
  fi
  VSTATUS="$(version_status "$VERSION" "$TESTED")"
  VWARN=""
  case "$VSTATUS" in
    unsupported) VWARN="$TOOL $VERSION is outside the supported range (tested $TESTED; supported: $TESTED or newer within the same $( [ "${TESTED%%.*}" = 0 ] && echo minor || echo major )); its reports may not match what the adapter expects" ;;
    unparseable) VWARN="$TOOL's version could not be read from its probe output; it is not the tested $TESTED, or at least not verified to be" ;;
  esac
  if [ -n "$VWARN" ]; then
    echo "detect: WARNING: $VWARN" >&2
    printf '%s\n' "$VWARN" >> "$TMP/warnings"
  fi

  TARGETS="$FILES"
  if [ "$L" = csharp ]; then
    csharp_targets < "$TMP/files"
    TARGETS="$(jq -Rn '[inputs] | unique' < "$TMP/cs.targets")"
    if [ "$TARGETS" = "[]" ]; then
      jq -nc --arg t "$TOOL" --arg v "$VERSION" '{language:"csharp", tool:$t, version:$v, status:"skipped",
        reason:"no test project references changed code", targets:[], test_command:"", test_command_source:""}' >> "$TMP/langs.ndjson"
      continue
    fi
  fi

  CMD="$(baseline_command "$L" "$FILES")"
  if [ -n "$CMD" ]; then SRC=baseline; else
    CMD="$(default_command "$L" "$(head -1 "$TMP/files")")"
    SRC=registry-default
  fi
  jq -nc --arg l "$L" --arg t "$TOOL" --arg v "$VERSION" --arg vs "$VSTATUS" --arg vw "$VWARN" --arg rv "$RUNNER_VERSION" \
    --argjson targets "$TARGETS" --arg c "$CMD" --arg s "$SRC" \
    '{language:$l, tool:$t, version:$v, version_status:$vs, version_warning:$vw, runner_version:$rv, status:"runnable", reason:"", targets:$targets, test_command:$c, test_command_source:$s}' >> "$TMP/langs.ndjson"
done

if [ -s "$TMP/hints" ]; then
  echo "detect: mutation tools missing for changed languages; install them:" >&2
  sed 's/^/  /' "$TMP/hints" >&2
  exit 1
fi

jq -s --rawfile w "$TMP/warnings" '{languages: ., runnable_count: (map(select(.status == "runnable")) | length),
    warnings: ($w | split("\n") | map(select(length > 0)))}
  | .skip_reason = (if .runnable_count > 0 then ""
                    elif (.languages | length) == 0 then "no changed source files"
                    else ([.languages[].reason] | join("; ")) end)' "$TMP/langs.ndjson"
