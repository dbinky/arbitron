#!/bin/sh
# Deterministic tamper gate for a coding pass: compares <base> and <head> in
# work_dir and reports ways the tests may have been weakened.
#
#   tamper-gate.sh <base> <head> [--tests-only]   check mode
#   tamper-gate.sh --classify <base> <head>       classify mode (no checks)
#
# Reads the variable snapshot as JSON on stdin (work_dir, workspace) and writes
# exactly one JSON object to stdout; diagnostics go to stderr. Exit 2 means bad
# inputs only (work_dir unset or not a worktree, base/head not a commit) and
# nothing is emitted. A base that is not an ancestor of head (history rewritten
# inside the pass) is exit 0 with the non-judgeable finding history_rewritten.
# Check mode also writes <workspace>/tamper/findings.json, deletes a stale
# tamper/verdict.json and rotates tamper/must-fix.md to must-fix.prev.md.
#
# --tests-only additionally reports source_changed (and so force_void) when a
# source file, or a Rust source hunk, changed. A Rust file outside tests/ is
# split by hunk: a hunk wholly inside a #[cfg(test)] mod block (brace-matched,
# ignoring strings, char literals and comments) is a test hunk, anything else
# is a source hunk; the file's role is source if any hunk is a source hunk.
# Config, CI and baseline edits are file-level findings (line 0); a changed
# line inside a test or mutation section of a multi-purpose file is resolved by
# scanning up to the governing header (base side for deleted lines). The
# warning hardcoded_expected_value never affects clean or needs_judge.
set -eu

# ---------------------------------------------------------------------------
# Data tables. Regexes are Oniguruma (jq) except "assert", which are POSIX ERE
# (grep -E). "markers" are literal strings matched after a non-identifier left
# boundary (P7). "precedence" orders kinds for per-(file,line) dedupe (P6).
# ---------------------------------------------------------------------------
LANG_TABLE='{
  "precedence": ["history_rewritten","source_changed","baseline_edited","ci_config_changed","mutation_config_weakened","test_config_weakened","test_deleted","test_renamed","assertions_dropped","skip_marker_added"],
  "languages": {
    "go": {
      "src": "\\.go$",
      "test": ["_test\\.go$","(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["t\\.Error","t\\.Errorf","t\\.Fatal","t\\.Fatalf","assert\\.","require\\.","Expect\\("],
      "markers": ["t.Skip(","t.Skipf(","t.SkipNow(","T().Skip"],
      "markers_test": ["//go:build ignore","// +build ignore"]
    },
    "csharp": {
      "src": "\\.cs$",
      "test": ["Tests?\\.cs$","(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["Assert\\.","\\.Should\\(\\)","\\.Verify\\("],
      "markers": ["[Ignore","Skip =","Skip=","[Explicit"]
    },
    "js": {
      "src": "\\.(ts|tsx|js|jsx|mjs|cjs|mts|cts)$",
      "test": ["\\.(test|spec)\\.[^/]*$","(^|/)__tests__/","(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["expect\\(","assert\\(","assert\\."],
      "markers": ["it.skip","test.skip","describe.skip","it.only","test.only","describe.only","this.skip("],
      "markers_nodot": ["xit(","xtest(","xdescribe(","fit(","fdescribe("]
    },
    "python": {
      "src": "\\.py$",
      "test": ["(^|/)test_[^/]*\\.py$","_test\\.py$","(^|/)conftest\\.py$","(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["assert ","self\\.assert","pytest\\.raises"],
      "markers": ["pytest.mark.skip","pytest.mark.skipif","pytest.mark.xfail","@unittest.skip","pytest.skip(","self.skipTest("]
    },
    "java": {
      "src": "\\.java$",
      "test": ["Tests?\\.java$","IT\\.java$","(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["assert[A-Z][A-Za-z0-9_]*\\(","assertThat\\(","verify\\("],
      "markers": ["@Disabled","@Ignore","assumeTrue(","assumeFalse(","Assumptions.assume"]
    },
    "rust": {
      "src": "\\.rs$",
      "test": ["(^|/)tests?/","(?i)(^|/)([^/]*[_.-])?test_?(util|helper)s?[^/]*$","(^|/)[^/]*[a-z0-9]Test_?(Util|Helper)s?[^/]*$"],
      "assert": ["assert!","assert_eq!","assert_ne!","debug_assert"],
      "markers": ["#[ignore"]
    }
  },
  "config_test": ["(^|/)\\.gitattributes$","(^|/)jest\\.config\\.[^/]*$","(^|/)vitest\\.config\\.[^/]*$","(^|/)\\.mocharc[^/]*$","(^|/)karma\\.conf\\.[^/]*$","(^|/)(pytest|tox)\\.ini$","(^|/)\\.coveragerc$","\\.runsettings$","(^|/)nextest\\.toml$"],
  "config_mutation": ["(^|/)\\.gremlins\\.ya?ml$","(^|/)stryker-config\\.json$","(^|/)stryker\\.conf(ig)?\\.[^/]*$","(^|/)mutants\\.toml$"],
  "ci": ["^\\.github/workflows/","^\\.gitlab-ci\\.yml$","^azure-pipelines[^/]*\\.yml$","^\\.circleci/","^Jenkinsfile$","^\\.buildkite/","^bitbucket-pipelines\\.yml$","^\\.travis\\.yml$"],
  "tests_only_allow": ["^docs/.*\\.md$","^[^/]*\\.md$","^\\.arbitron/pipeline/[^/]*\\.(baseline|harden|survivors)\\.json$","^docs/reference/(focus-areas\\.md|test-baseline\\.json)$"],
  "baseline": ["\\.(baseline|harden|survivors)\\.json$","(^|/)test-baseline\\.json$"],
  "keyword": {
    "files": ["(^|/)package\\.json$","(^|/)pom\\.xml$","(^|/)build\\.gradle[^/]*$","(^|/)Cargo\\.toml$","(^|/)vite\\.config\\.[^/]*$"],
    "re": "exclude|ignore|skip|testPathIgnore|coveragePathIgnore|testMatch|threshold|omit|maven\\.test\\.skip|skipTests|testFailureIgnore|harness|test *= *false"
  },
  "line_rules": [
    {"files": "(^|/)conftest\\.py$", "re": "collect_ignore_glob|collect_ignore|pytest_collection_modifyitems", "kind": "test_config_weakened"},
    {"files": "Tests?\\.csproj$", "re": "<Compile[[:space:]]+Remove[[:space:]]*=", "kind": "test_config_weakened"}
  ],
  "sections": [
    {"files": "(^|/)pyproject\\.toml$", "scoper": "ini", "scope": "^tool\\.pytest\\.ini_options$|^tool\\.coverage(\\.|$)", "kind": "test_config_weakened"},
    {"files": "(^|/)pyproject\\.toml$", "scoper": "ini", "scope": "^tool\\.mutmut$", "kind": "mutation_config_weakened"},
    {"files": "(^|/)setup\\.cfg$", "scoper": "ini", "scope": "^tool:pytest$|^coverage:", "kind": "test_config_weakened"},
    {"files": "(^|/)setup\\.cfg$", "scoper": "ini", "scope": "^mutmut$", "kind": "mutation_config_weakened"},
    {"files": "(^|/)pom\\.xml$", "scoper": "pom", "scope": "^pitest$", "kind": "mutation_config_weakened"},
    {"files": "(^|/)build\\.gradle[^/]*$", "scoper": "gradle", "scope": "^pitest$", "kind": "mutation_config_weakened"},
    {"files": "(^|/)vite\\.config\\.[^/]*$", "scoper": "vite", "scope": "^test$", "kind": "test_config_weakened"}
  ]
}'

# shellcheck disable=SC2016 # jq programs, not shell expansions
# Shared jq definitions: $T is LANG_TABLE, $CS the directories holding a
# *Tests.csproj (their .cs files are C# tests).
JQ_LIB='
def esc: gsub("(?<c>[\\\\.^$*+?(){}|\\[\\]])"; "\\" + .c);
def lang($p): ([$T.languages | to_entries[] | select(.value.src as $r | $p | test($r)) | .key] | first) // "";
def csdir($p): $CS | any(.[]; . as $d | $p | startswith($d));
def role($p):
  lang($p) as $l
  | if ($T.ci | any(.[]; . as $r | $p | test($r))) then "ci"
    elif (($T.config_test + $T.config_mutation) | any(.[]; . as $r | $p | test($r))) then "config"
    elif $l != "" and (($T.languages[$l].test | any(.[]; . as $r | $p | test($r))) or ($l == "csharp" and csdir($p))) then "test"
    elif $l != "" then "source"
    else "other" end;
def frole: if has("rs_role") then .rs_role elif .status == "R" and role(.old) == "test" then "test" else role(.path) end;
def flang: lang(.path) as $l | if $l == "" and .status == "R" then lang(.old) else $l end;
def sword: {M: "changed", A: "added", D: "deleted", R: "renamed"}[.] // "changed";
def anyre($arr; $s): $arr | any(.[]; . as $r | $s | test($r));
def parse_ns:
  def go($t):
    if ($t | length) == 0 then empty
    else $t[0] as $s
      | if ($s | test("^[RC]")) then ({status: $s[0:1], old: $t[1], path: $t[2]}, go($t[3:]))
        else ({status: $s[0:1], old: $t[1], path: $t[1]}, go($t[2:])) end
    end;
  [go(map(select(length > 0)))];
'

# ---------------------------------------------------------------------------
# awk helpers. Programs avoid a literal single quote (octal \047 instead) so
# they can live in single-quoted shell strings.
#
# LEX_AWK (-v lang=...): blanks string, char and raw-string contents and removes
# comments (line, block, Python docstrings), keeping the line count, so tokens
# inside literals or comments are never counted and braces can be matched.
# ---------------------------------------------------------------------------
# shellcheck disable=SC2016 # awk programs, not shell expansions
LEX_AWK='
BEGIN {
  lc = (lang == "python") ? "#" : "//"
  rust = (lang == "rust"); blk = (lang != "python")
  bt = (lang == "go" || lang == "js"); tri = (lang == "python" || lang == "java")
  q = "\047"; dq = "\""; st = ""; tn = 0
}
{
  line = $0; n = length(line); out = ""; i = 1
  while (i <= n) {
    c = substr(line, i, 1)
    if (st == "B") {
      two = substr(line, i, 2)
      if (rust && two == "/*") { depth++; i += 2 }
      else if (two == "*/") { depth--; i += 2; if (depth == 0) st = "" }
      else i++
      continue
    }
    if (st == "S") {
      if (esc && c == "\\") { i += 2; continue }
      if (lang == "js" && term == "`" && c == "$" && substr(line, i + 1, 1) == "{") { tn++; tb[tn] = 0; st = ""; i += 2; continue }
      if (verb && c == dq && substr(line, i + 1, 1) == dq) { i += 2; continue }
      if (substr(line, i, tl) == term) { i += tl; st = ""; continue }
      i++; continue
    }
    two = substr(line, i, 2); three = substr(line, i, 3)
    if (substr(line, i, length(lc)) == lc) break
    if (blk && two == "/*") { st = "B"; depth = 1; i += 2; out = out " "; continue }
    if (tri && (three == dq dq dq || (lang == "python" && three == q q q))) {
      st = "S"; term = three; tl = 3; esc = 1; verb = 0; ml = 1; i += 3; out = out dq dq; continue }
    if (rust && c == "r" && (i == 1 || substr(line, i - 1, 1) !~ /[A-Za-z0-9_]/ ||
        (substr(line, i - 1, 1) ~ /[bc]/ && (i == 2 || substr(line, i - 2, 1) !~ /[A-Za-z0-9_]/)))) {
      j = i + 1; h = ""
      while (substr(line, j, 1) == "#") { h = h "#"; j++ }
      if (substr(line, j, 1) == dq) {
        st = "S"; term = dq h; tl = length(term); esc = 0; verb = 0; ml = 1; i = j + 1; out = out dq dq; continue }
    }
    if (bt && c == "`") { st = "S"; term = "`"; tl = 1; esc = (lang == "js"); verb = 0; ml = 1; i++; out = out dq dq; continue }
    if (lang == "csharp" && c == "@" && substr(line, i + 1, 1) == dq) {
      st = "S"; term = dq; tl = 1; esc = 0; verb = 1; ml = 1; i += 2; out = out dq dq; continue }
    if (c == dq) { st = "S"; term = dq; tl = 1; esc = 1; verb = 0; ml = rust; i++; out = out dq dq; continue }
    if (c == q) {
      if (rust) {
        nx = substr(line, i + 1, 1)
        if (nx == "\\") {
          k = index(substr(line, i + 2), q)
          if (k > 0) { i += 2 + k; out = out dq dq; continue }
        } else if (substr(line, i + 2, 1) == q && nx != q) { i += 3; out = out dq dq; continue }
        out = out c; i++; continue
      }
      st = "S"; term = q; tl = 1; esc = 1; verb = 0; ml = 0; i++; out = out dq dq; continue
    }
    if (tn > 0 && c == "{") tb[tn]++
    else if (tn > 0 && c == "}") {
      if (tb[tn] == 0) { tn--; st = "S"; term = "`"; tl = 1; esc = 1; verb = 0; ml = 1; i++; continue }
      tb[tn]--
    }
    out = out c; i++
  }
  if (st == "S" && !ml) st = ""
  print out
}'

# RSBLOCKS_AWK: reads LEX_AWK output of a Rust file, prints "start end" for each
# #[cfg(test)] mod block (start is the attribute line; the third column is the
# line of the "mod name {" opener; the fourth is the closing-brace line when code
# follows the brace on it, else 0, and such a line is never test code).
# shellcheck disable=SC2016
RSBLOCKS_AWK='
function scan(t,   k, ch) {
  for (k = 1; k <= length(t); k++) {
    ch = substr(t, k, 1)
    if (ch == "{") { depth++; opened = 1 }
    else if (ch == "}") { depth--; if (opened && depth == 0) { tr = substr(t, k + 1); gsub(/[ \t]+/, "", tr); print start, NR, openl, (tr == "" ? 0 : NR); inb = 0; return } }
  }
}
{
  t = $0; gsub(/^[ \t]+/, "", t); gsub(/[ \t]+$/, "", t)
  if (inb) { scan(t); next }
  if (t == "") next
  if (t ~ /^#\[cfg\(test\)\]/) {
    if (!pend) astart = NR
    pend = 1
    sub(/^#\[cfg\(test\)\][ \t]*/, "", t)
    if (t == "") next
  }
  if (!pend) next
  if (t ~ /^#\[/) next
  if (t ~ /^(pub(\([^)]*\))?[ \t]+)?mod[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*(\{.*)?$/) {
    inb = 1; start = astart; openl = NR; depth = 0; opened = 0; pend = 0
    scan(t); next
  }
  pend = 0
}
END { if (inb) print start, NR, openl, 0 }'

# RSFILTER_AWK (-v r="s e;s e"): keeps only the lines inside the given ranges.
# shellcheck disable=SC2016
RSFILTER_AWK='
BEGIN { n = split(r, R, ";") }
{ for (k = 1; k <= n; k++) { split(R[k], p, " "); if (p[1] != "" && NR >= p[1] + 0 && NR <= p[2] + 0) { print; next } } }'

# RSHUNK_AWK (-v rb=... -v rh=... as "s e;s e"): reads git diff -U0 output and
# prints "test" if every changed non-blank line lies inside a test block (base-side
# lines against rb, head-side against rh), else "source". A changed attribute or
# opener line of a block makes it a source change unless the whole block was
# added or deleted.
# shellcheck disable=SC2016
RSHUNK_AWK='
function inr(arr, cnt, l,   k, p) {
  for (k = 1; k <= cnt; k++) { split(arr[k], p, " "); if (p[1] != "" && l >= p[1] + 0 && l <= p[2] + 0 && l != p[4] + 0) return 1 }
  return 0
}
BEGIN { nb = split(rb, B, ";"); nh = split(rh, H, ";"); hunks = 0; src = 0; hdr = 1 }
/^diff --git / { hdr = 1; next }
/^@@ / {
  hdr = 0; hunks++
  a = $2; sub(/^-/, "", a); split(a, x, ","); bl = x[1] + 0
  c = $3; sub(/^\+/, "", c); split(c, y, ","); hl = y[1] + 0
  next
}
hdr { next }
/^-/ { CB[bl] = 1; if (substr($0, 2) !~ /^[ \t\r]*$/ && !inr(B, nb, bl)) src = 1; bl++; next }
/^\+/ { CH[hl] = 1; if (substr($0, 2) !~ /^[ \t\r]*$/ && !inr(H, nh, hl)) src = 1; hl++; next }
# A changed attribute-to-opener line of a block is a source change unless the
# whole block was added or deleted in the same diff.
function hdrchk(arr, cnt, C,   k, p, l) {
  for (k = 1; k <= cnt; k++) {
    split(arr[k], p, " ")
    if (p[1] == "") continue
    for (l = p[1] + 0; l <= p[3] + 0; l++) if (C[l]) {
      for (l = p[1] + 0; l <= p[2] + 0; l++) if (!C[l]) src = 1
      break
    }
  }
}
END { hdrchk(B, nb, CB); hdrchk(H, nh, CH); print (hunks == 0 || src) ? "source" : "test" }'

# CHG_AWK: git diff -U0 -> "side<TAB>path<TAB>line<TAB>text" per changed line;
# "-" lines carry the base path and base line, "+" lines the head ones.
# shellcheck disable=SC2016
CHG_AWK='
/^diff --git / { hdr = 1; ap = ""; bp = ""; next }
/^@@ /         { hdr = 0; split($2, x, ","); as = substr(x[1], 2) + 0; split($3, y, ","); cs = substr(y[1], 2) + 0; next }
hdr && /^--- / { ap = substr($0, 5); sub(/\t$/, "", ap); next }
hdr && /^\+\+\+ / { bp = substr($0, 5); sub(/\t$/, "", bp); next }
hdr            { next }
/^\+/          { print "+\t" bp "\t" cs "\t" substr($0, 2); cs++; next }
/^-/           { print "-\t" ap "\t" as "\t" substr($0, 2); as++; next }'

# Scope resolvers (-v want=",3,5," -v side=... -v file=...): print
# "side<TAB>file<TAB>line<TAB>scope" for each wanted line.
# INI/TOML: the governing [section] header, brackets removed.
# shellcheck disable=SC2016
SCOPE_INI_AWK='
{ t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t\r]+$/, "", t)
  if (t ~ /^\[+[A-Za-z_][^]]*\]+[ \t]*([#;].*)?$/) { cur = t; sub(/^\[+/, "", cur); sub(/\].*$/, "", cur) }
  if (index(want, "," NR ",")) print side "\t" file "\t" NR "\t" cur }'
# pom.xml: "pitest" for lines inside a <plugin> whose first artifactId contains pitest.
# shellcheck disable=SC2016
SCOPE_POM_AWK='
{ L[NR] = $0 }
END {
  for (i = 1; i <= NR; i++) {
    if (L[i] ~ /<plugin>/) { s = i; art = ""; seen = 0 }
    else if (s && !seen && L[i] ~ /<artifactId>/) { art = L[i]; seen = 1 }
    if (s && L[i] ~ /<\/plugin>/) { if (index(art, "pitest")) for (k = s; k <= i; k++) P[k] = 1; s = 0 }
  }
  for (i = 1; i <= NR; i++) if (index(want, "," i ",")) print side "\t" file "\t" i "\t" (P[i] ? "pitest" : "")
}'
# Brace block opened by a line matching -v open (on LEX_AWK output): label for lines inside it.
# shellcheck disable=SC2016
SCOPE_BLOCK_AWK='
{ if (!inb && $0 ~ open) { inb = 1; d = 0; o = 0 }
  f = inb
  if (inb) {
    for (k = 1; k <= length($0); k++) { ch = substr($0, k, 1); if (ch == "{") { d++; o = 1 } else if (ch == "}") d-- }
    if (o && d <= 0) inb = 0
  }
  if (index(want, "," NR ",")) print side "\t" file "\t" NR "\t" (f ? label : "") }'

die() { echo "tamper-gate: $*" >&2; exit 2; }

MODE=check
TESTS_ONLY=false
BASE=""
HEAD=""
while [ $# -gt 0 ]; do
  case "$1" in
    --classify) MODE=classify ;;
    --tests-only) TESTS_ONLY=true ;;
    -*) die "unknown option: $1" ;;
    *)
      if [ -z "$BASE" ]; then BASE="$1"
      elif [ -z "$HEAD" ]; then HEAD="$1"
      else die "too many arguments"
      fi
      ;;
  esac
  shift
done
[ -n "$BASE" ] && [ -n "$HEAD" ] || die "usage: tamper-gate.sh [--classify] <base> <head> [--tests-only]"

IN="$(cat)"
WORK="$(printf '%s' "$IN" | jq -r '.work_dir // ""' 2>/dev/null)" || die "stdin is not JSON"
WS="$(printf '%s' "$IN" | jq -r '.workspace // ""')"

[ -n "$WORK" ] || die "work_dir not set"
# The repository belongs to the agent being checked. A replace ref (refs/replace)
# would make every read below see an original blob instead of the committed one.
GIT_NO_REPLACE_OBJECTS=1
export GIT_NO_REPLACE_OBJECTS
git -C "$WORK" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not a git worktree: $WORK"
if [ "$MODE" = check ]; then [ -n "$WS" ] || die "workspace not set"; fi

BASE_SHA="$(git -C "$WORK" rev-parse --verify -q "$BASE^{commit}" 2>/dev/null)" || die "base is not a commit: $BASE"
HEAD_SHA="$(git -C "$WORK" rev-parse --verify -q "$HEAD^{commit}" 2>/dev/null)" || die "head is not a commit: $HEAD"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

g() { git -C "$WORK" -c core.quotepath=off "$@"; }
TAB="$(printf '\t')"
# Longest added line the hardcoded-literal warning scans (longer = generated).
MAX_LIT_LINE=2000

if [ "$MODE" = check ]; then
  mkdir -p "$WS/tamper"
  rm -f "$WS/tamper/verdict.json"
  if [ -f "$WS/tamper/must-fix.md" ]; then mv -f "$WS/tamper/must-fix.md" "$WS/tamper/must-fix.prev.md"; fi
fi

RW=false
if ! g merge-base --is-ancestor "$BASE_SHA" "$HEAD_SHA" 2>/dev/null; then
  RW=true
fi

COMMITS="$(g rev-list --count "$BASE_SHA..$HEAD_SHA")"

# Directories (with trailing slash) of C# test projects, at either end.
CSDIRS="$( { g ls-tree -r --name-only "$BASE_SHA"; g ls-tree -r --name-only "$HEAD_SHA"; } |
  jq -Rn '[inputs | select(test("Tests\\.csproj$")) | sub("[^/]*$"; "") | select(. != "")] | unique')"

# ranges_of <sha> <path>: "s e;s e;" of the cfg(test) mod blocks of a Rust file.
ranges_of() {
  g show "$1:$2" 2>/dev/null | LC_ALL=C awk -v lang=rust "$LEX_AWK" | LC_ALL=C awk "$RSBLOCKS_AWK" |
    awk '{ printf "%s %s %s %s;", $1, $2, $3, $4 }'
}

# count_asserts <sha> <path> <lang> <re> <blocks_only>: assertion tokens in code
# (not strings or comments); with blocks_only=1, only inside cfg(test) blocks.
count_asserts() {
  g show "$1:$2" 2>/dev/null | LC_ALL=C awk -v lang="$3" "$LEX_AWK" > "$TMP/lx"
  if [ "$5" = 1 ]; then
    rr="$(LC_ALL=C awk "$RSBLOCKS_AWK" "$TMP/lx" | awk '{ printf "%s %s %s %s;", $1, $2, $3, $4 }')"
    LC_ALL=C awk -v r="$rr" "$RSFILTER_AWK" "$TMP/lx" > "$TMP/lx2"
    mv -f "$TMP/lx2" "$TMP/lx"
  fi
  LC_ALL=C grep -aoE "$4" "$TMP/lx" | wc -l | tr -d ' '
}

if [ "$RW" = true ]; then
  echo '[]' > "$TMP/ns.json"
else
  g diff --no-ext-diff --no-textconv --no-color -M --name-status -z "$BASE_SHA" "$HEAD_SHA" -- > "$TMP/ns.raw"
  tr '\0' '\n' < "$TMP/ns.raw" | jq -Rn "$JQ_LIB"'[inputs] | parse_ns' --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" > "$TMP/ns0.json"

  # Rust files outside tests/ that were modified: role by hunk.
  jq -r --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'
    .[] | select(.status == "M" and lang(.path) == "rust" and role(.path) == "source") | .path' "$TMP/ns0.json" > "$TMP/rs.list"
  : > "$TMP/rs.tsv"
  while IFS= read -r p; do
    rb="$(ranges_of "$BASE_SHA" "$p")"
    rh="$(ranges_of "$HEAD_SHA" "$p")"
    r="$(g diff --no-ext-diff --no-textconv --text --no-color -U0 "$BASE_SHA" "$HEAD_SHA" -- "$p" | LC_ALL=C awk -v rb="$rb" -v rh="$rh" "$RSHUNK_AWK")"
    printf '%s\t%s\n' "$p" "$r" >> "$TMP/rs.tsv"
  done < "$TMP/rs.list"
  jq -Rn '[inputs | split("\t") | {key: .[0], value: .[1]}] | from_entries' "$TMP/rs.tsv" > "$TMP/rsmap.json"
  jq --slurpfile M "$TMP/rsmap.json" 'map(if $M[0][.path] then . + {rs_role: $M[0][.path]} else . end)' "$TMP/ns0.json" > "$TMP/ns.json"
fi

if [ "$MODE" = classify ]; then
  jq -n --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" --slurpfile NS "$TMP/ns.json" \
    --arg base "$BASE_SHA" --arg head "$HEAD_SHA" --argjson rw "$RW" "$JQ_LIB"'
    ($NS[0] | map(. + {language: flang, role: frole})) as $f
    | { base: $base, head: $head, history_rewritten: $rw,
        files: ($f | map({path, status, language, role})),
        source_files_by_language: ($f | map(select(.role == "source")) | group_by(.language)
                                    | map({key: .[0].language, value: map(.path)}) | from_entries),
        test_files: ($f | map(select(.role == "test") | .path)) }'
  exit 0
fi

: > "$TMP/f1.ndjson"
: > "$TMP/f2.ndjson"
: > "$TMP/f3.ndjson"
: > "$TMP/f4.ndjson"
: > "$TMP/f5.ndjson"
: > "$TMP/w.ndjson"

if [ "$RW" = false ]; then
  # test_deleted / test_renamed
  jq -c --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'
    .[] | select(frole == "test") | flang as $l
    | if .status == "D" then {kind: "test_deleted", file: .path, line: 0, language: $l, detail: "deleted", judgeable: true}
      elif .status == "R" then {kind: "test_renamed", file: .path, line: 0, language: $l, detail: ("renamed from " + .old), judgeable: true}
      else empty end' "$TMP/ns.json" > "$TMP/f1.ndjson"

  # assertions_dropped: test files present at both ends, plus Rust files with
  # cfg(test) blocks (counted inside the blocks only).
  jq -r --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'
    .[] | select(.status != "D" and .status != "A") | select(has("rs_role") or frole == "test")
    | flang as $l | select($l != "")
    | [.old, .path, $l, ("(^|[^A-Za-z0-9_$])(" + ($T.languages[$l].assert | join("|")) + ")"), (if has("rs_role") then "1" else "0" end)]
    | join("\t")' "$TMP/ns.json" > "$TMP/cand.tsv"
  while IFS="$TAB" read -r old new lang re blocks; do
    nb="$(count_asserts "$BASE_SHA" "$old" "$lang" "$re" "$blocks")"
    nh="$(count_asserts "$HEAD_SHA" "$new" "$lang" "$re" "$blocks")"
    if [ "$nh" -lt "$nb" ]; then
      jq -nc --arg file "$new" --arg lang "$lang" --arg d "${nb}→${nh}" \
        '{kind: "assertions_dropped", file: $file, line: 0, language: $lang, detail: $d, judgeable: true}' >> "$TMP/f2.ndjson"
    fi
  done < "$TMP/cand.tsv"

  # Changed lines (side, file, line, text) from the hunk headers.
  g diff --no-ext-diff --no-textconv --text --no-color -M -U0 --src-prefix=a/ --dst-prefix=b/ "$BASE_SHA" "$HEAD_SHA" -- |
    LC_ALL=C awk "$CHG_AWK" |
    jq -Rc '
      split("\t") as $c
      | { side: $c[0],
          file: ($c[1] | (if startswith("\"") then (try fromjson catch .) else . end) | ltrimstr("a/") | ltrimstr("b/")),
          line: ($c[2] | tonumber), text: ($c[3:] | join("\t")) }
      | select(.file != "/dev/null" and .file != "")' > "$TMP/changes.ndjson"

  # skip_marker_added: added lines.
  jq -c --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'
      select(.side == "+")
      | lang(.file) as $l | select($l != "")
      | . as $a
      | ( [$T.languages[$l].markers[] | select(. as $m | $a.text | test("(^|[^A-Za-z0-9_$])" + ($m | esc)))]
          + [($T.languages[$l].markers_nodot // [])[] | select(. as $m | $a.text | test("(^|[^A-Za-z0-9_$.])" + ($m | esc)))]
          + (if role($a.file) == "test"
             then [($T.languages[$l].markers_test // [])[] | select(. as $m | $a.text | test("(^|[^A-Za-z0-9_$])" + ($m | esc)))]
             else [] end) ) as $hit
      | select($hit | length > 0)
      | {kind: "skip_marker_added", file, line, language: $l, detail: ($hit | join(",")), judgeable: true}' "$TMP/changes.ndjson" > "$TMP/f3.ndjson"

  # Section scopes for changed lines of multi-purpose config files.
  jq -rs --argjson T "$LANG_TABLE" "$JQ_LIB"'
    def scoper($p): first($T.sections[] | select(.files as $r | $p | test($r)) | .scoper) // "";
    map(select(scoper(.file) != "")) | group_by([.side, .file])[]
    | [.[0].side, .[0].file, scoper(.[0].file), (map(.line | tostring) | join(","))] | join("\t")' "$TMP/changes.ndjson" > "$TMP/scoped.tsv"
  : > "$TMP/scopes.tsv"
  while IFS="$TAB" read -r side file scoper lines; do
    sha="$HEAD_SHA"
    if [ "$side" = "-" ]; then sha="$BASE_SHA"; fi
    want=",$lines,"
    case "$scoper" in
      ini) g show "$sha:$file" 2>/dev/null | LC_ALL=C awk -v want="$want" -v side="$side" -v file="$file" "$SCOPE_INI_AWK" ;;
      pom) g show "$sha:$file" 2>/dev/null | LC_ALL=C awk -v want="$want" -v side="$side" -v file="$file" "$SCOPE_POM_AWK" ;;
      gradle) g show "$sha:$file" 2>/dev/null | LC_ALL=C awk -v lang=java "$LEX_AWK" |
        LC_ALL=C awk -v want="$want" -v side="$side" -v file="$file" -v label=pitest -v open='^[ \t]*pitest[ \t]*[{]' "$SCOPE_BLOCK_AWK" ;;
      vite) g show "$sha:$file" 2>/dev/null | LC_ALL=C awk -v lang=js "$LEX_AWK" |
        LC_ALL=C awk -v want="$want" -v side="$side" -v file="$file" -v label=test -v open='(^|[^A-Za-z0-9_$.])test[ \t]*:[ \t]*[{]' "$SCOPE_BLOCK_AWK" ;;
    esac
  done < "$TMP/scoped.tsv" >> "$TMP/scopes.tsv"
  jq -Rc 'split("\t") | {side: .[0], file: .[1], line: (.[2] | tonumber), scope: .[3]}' "$TMP/scopes.tsv" > "$TMP/scopes.ndjson"

  # Config, CI and baseline findings (file-level, line 0).
  jq -nc --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" --slurpfile NS "$TMP/ns.json" \
    --slurpfile CH "$TMP/changes.ndjson" --slurpfile SC "$TMP/scopes.ndjson" "$JQ_LIB"'
    def mk($kind; $file; $d): {kind: $kind, file: $file, line: 0, language: lang($file), detail: $d, judgeable: true};
    ($NS[0] | map(select(.status == "R") | {key: .old, value: .path}) | from_entries) as $ren
    | ( $NS[0][] | . as $e | ([.path, .old] | unique) as $ps | (.status | sword) as $w
        | ( (if any($ps[]; anyre($T.config_mutation; .)) then mk("mutation_config_weakened"; $e.path; "mutation config " + $w)
             elif any($ps[]; anyre($T.config_test; .)) then mk("test_config_weakened"; $e.path; "test config " + $w)
             else empty end),
            (if any($ps[]; anyre($T.ci; .)) then mk("ci_config_changed"; $e.path; "CI config " + $w) else empty end),
            (if any($ps[]; anyre($T.baseline; .)) then mk("baseline_edited"; $e.path; "baseline " + $w) else empty end) ) ),
      ( $CH[] | . as $c
        | (if $c.side == "-" then ($ren[$c.file] // $c.file) else $c.file end) as $f
        | ( (select(anyre($T.keyword.files; $c.file)) | select($c.text | test($T.keyword.re; "i"))
             | mk("test_config_weakened"; $f; ($c.text | match($T.keyword.re; "i").string))),
            ($T.line_rules[] | . as $lr | select($c.file | test($lr.files)) | select($c.text | test($lr.re; "i"))
             | mk($lr.kind; $f; ($c.text | match($lr.re; "i").string))),
            ($T.sections[] | . as $s | select($c.file | test($s.files))
             | (first($SC[] | select(.side == $c.side and .file == $c.file and .line == $c.line)) // null) as $sc
             | select($sc != null and ($sc.scope | test($s.scope)))
             | mk($s.kind; $f; "[" + $sc.scope + "]")) ) )' > "$TMP/f4.ndjson"

  # source_changed (--tests-only): the round may change test files and the
  # prose/state this workflow owns (tests_only_allow), nothing else. Every other
  # path voids, whatever its language: SQL, templates, scripts and configuration
  # can carry application behavior the six-language table does not classify.
  # Rust files are judged by hunk (frole), and a rename must stay a test on both
  # sides. .gitattributes is never allowed.
  if [ "$TESTS_ONLY" = true ]; then
    jq -c --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'
      def allowed($p): anyre($T.tests_only_allow; $p);
      def bad_path($p; $r): $r != "test" and (allowed($p) | not);
      .[] | select(if .status == "R" then (bad_path(.path; role(.path)) or bad_path(.old; role(.old)))
                   else bad_path(.path; frole) end)
      | {kind: "source_changed", file: .path, line: 0, language: flang, detail: ("non-test file " + (.status | sword)), judgeable: false}' "$TMP/ns.json" > "$TMP/f5.ndjson"
  fi

  # Warning hardcoded_expected_value: a literal (string of 8+ chars, number of
  # 4+ digits) on an added source line that also appears in a test file at head.
  # Lines over MAX_LIT_LINE chars are generated (minified bundles) and skipped:
  # the regex scan over them takes minutes and timed the whole gate out.
  jq -r --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" --slurpfile NS "$TMP/ns.json" \
    --argjson max "$MAX_LIT_LINE" "$JQ_LIB"'
    ($NS[0] | map(select(.rs_role == "test") | .path)) as $rt
    | . as $c
    | select($c.side == "+" and ($c.text | length) <= $max)
    | select(role($c.file) == "source" and (($rt | index($c.file)) == null))
    | ( [ ($c.text | scan("\"((?:[^\"\\\\]|\\\\.)*)\"|\u0027((?:[^\u0027\\\\]|\\\\.)*)\u0027|`([^`]*)`") | map(select(. != null)) | .[0] | select(length >= 8)),
          ($c.text | scan("(?<![A-Za-z0-9_.])[0-9]{4,}(?![A-Za-z0-9_])")) ] | unique )[]
    | [$c.file, ($c.line | tostring), .] | join("\t")' "$TMP/changes.ndjson" > "$TMP/lits.tsv"
  if [ -s "$TMP/lits.tsv" ]; then
    g ls-tree -r --name-only "$HEAD_SHA" |
      jq -Rr --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" "$JQ_LIB"'select(role(.) == "test") | "'"$HEAD_SHA"':" + .' |
      g cat-file --batch > "$TMP/corpus"
    # cfg(test) blocks of Rust source files count as test code too.
    g grep -l -F '#[cfg(test)]' "$HEAD_SHA" -- '*.rs' 2>/dev/null | sed "s|^$HEAD_SHA:||" |
      while IFS= read -r rp; do
        rr="$(ranges_of "$HEAD_SHA" "$rp")"
        g show "$HEAD_SHA:$rp" 2>/dev/null | LC_ALL=C awk -v r="$rr" "$RSFILTER_AWK" >> "$TMP/corpus"
      done
    while IFS="$TAB" read -r file line lit; do
      if LC_ALL=C grep -qaF -e "$lit" "$TMP/corpus"; then
        jq -nc --arg file "$file" --argjson line "$line" --arg lit "$lit" \
          '{kind: "hardcoded_expected_value", file: $file, line: $line, detail: ("literal " + ($lit | tojson) + " also appears in a test file")}' >> "$TMP/w.ndjson"
      fi
    done < "$TMP/lits.tsv"
  fi
fi

OUT="$(jq -n --argjson T "$LANG_TABLE" --argjson CS "$CSDIRS" --slurpfile NS "$TMP/ns.json" \
  --slurpfile F1 "$TMP/f1.ndjson" --slurpfile F2 "$TMP/f2.ndjson" --slurpfile F3 "$TMP/f3.ndjson" \
  --slurpfile F4 "$TMP/f4.ndjson" --slurpfile F5 "$TMP/f5.ndjson" --slurpfile W "$TMP/w.ndjson" \
  --argjson rw "$RW" --arg base "$BASE_SHA" --arg head "$HEAD_SHA" --argjson commits "$COMMITS" "$JQ_LIB"'
  def uniq: reduce .[] as $x ([]; if any(.[]; . == $x) then . else . + [$x] end);
  def plural($n; $w): ($n | tostring) + " " + $w + (if $n == 1 then "" else "s" end);
  $T.precedence as $P
  | (if $rw then [{kind: "history_rewritten", file: "", line: 0, language: "", judgeable: false,
                   detail: "base is not an ancestor of head"}]
     else $F1 + $F2 + $F3 + $F4 + $F5 end) as $raw
  | ($raw | group_by([.file, .line])
     | map(sort_by(. as $k | $P | index($k.kind)) as $s
           | $s[0] as $w
           | ($s | map(.kind) | uniq | map(select(. != $w.kind))) as $also
           | $w + {detail: ($s | map(.detail) | uniq | join(","))}
             + (if ($also | length) > 0 then {also_kinds: $also} else {} end))
     | to_entries | map({id: ("T" + ((.key + 1) | tostring))} + .value)) as $f
  | ($W | group_by([.file, .line])
     | map({kind: "hardcoded_expected_value", file: .[0].file, line: .[0].line, detail: (map(.detail) | uniq | join(", "))})
     | to_entries | map({id: ("W" + ((.key + 1) | tostring))} + .value)) as $w
  | ($rw or ($f | any(.kind == "source_changed"))) as $force
  | { clean: ($f | length == 0),
      needs_judge: (($f | any(.judgeable)) and ($force | not)),
      force_void: $force,
      source_changed: ($f | any(.kind == "source_changed" or (.also_kinds // [] | index("source_changed") != null))),
      history_rewritten: $rw,
      findings: $f,
      warnings: $w,
      blocking_count: ($f | length),
      warn_count: ($w | length),
      base: $base, head: $head,
      test_files_changed: (if $rw then [] else ($NS[0] | map(select(frole == "test") | .path) | unique) end),
      summary: (plural($f | length; "finding") + ", " + plural($w | length; "warning") + " over " + plural($commits; "commit")) }')"

printf '%s\n' "$OUT" | jq . > "$WS/tamper/findings.json"
printf '%s\n' "$OUT"
