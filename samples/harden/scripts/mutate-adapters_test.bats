#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Each mutate-<lang>.sh adapter runs against a PATH stub of its tool that writes
# a report literal shaped as the spike recorded it (docs/superpowers/notes/
# 2026-10-23-mutation-tooling-spike.md). PATH holds only the stub dir plus a
# symlink farm of plain utilities, so no real mutation tool can leak in. The
# last test runs the real gremlins when it is installed.

setup() {
  D="$BATS_TEST_DIRNAME"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WS="$T/ws"
  WORK="$WS/work"
  STUBS="$T/stubs"
  SYS="$T/sys"
  FX="$T/fx"
  mkdir -p "$WORK" "$STUBS" "$SYS" "$FX"
  # The tool stubs record what they saw beside the run (not inside it) and read
  # their fixtures from there; grant the test directory, as an operator could.
  export HARDEN_SANDBOX_RW="$T"
  for u in sh jq git grep sed awk find sort tr head tail cat dirname basename mkdir rm mktemp ls cp mv tar wc cut uniq date env xargs ln uname readlink sandbox-exec bwrap; do
    p="$(command -v "$u")" && ln -sf "$p" "$SYS/$u"
  done
  git init -q -b main "$WORK"
  echo base > "$WORK/README"
  git -C "$WORK" add README
  git -C "$WORK" commit -q -m base
  BASE_SHA="$(git -C "$WORK" rev-parse HEAD)"
}

# put <relpath> <content> : write a file in WORK
put() {
  mkdir -p "$WORK/$(dirname "$1")"
  printf '%s\n' "$2" > "$WORK/$1"
}

feature() {
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m feature
}

# stub <name> <body> [dirty] : a /bin/sh tool on the stub PATH. With dirty, it
# first modifies a tracked file and drops an untracked config into WORK.
stub() {
  {
    echo '#!/bin/sh'
    # HOSTILE_PRE, when set, is what a malicious dependency or repository test
    # would do first; see the isolation tests at the end of this file.
    [ -z "${HOSTILE_PRE-}" ] || printf '%s\n' "$HOSTILE_PRE"
    if [ -n "${3-}" ]; then
      echo "echo tampered >> '$WORK/README'"
      echo "echo '{}' > '$WORK/stryker-config.json'"
    fi
    printf '%s\n' "$2"
  } > "$STUBS/$1"
  chmod +x "$STUBS/$1"
}

# run_adapter <lang> <targets-json> [test-command]
run_adapter() {
  printf '%s' "$2" > "$T/targets.json"
  snap="$(jq -nc --arg w "$WORK" --arg ws "$WS" --arg mb "$BASE_SHA" '{work_dir:$w, workspace:$ws, scope:{merge_base:$mb}}')"
  run --separate-stderr env PATH="$STUBS:$SYS" sh -c 'printf "%s" "$1" | sh "$2" "$3" "$4" "$5"' _ \
    "$snap" "$D/mutate-$1.sh" "$T/targets.json" "${3-}" "$T/out.json"
}

out() { jq -c "$1" "$T/out.json"; }

# --- Go (gremlins) -------------------------------------------------------------

go_project() {
  put svc/go.mod 'module toy'
  put svc/pkg/a.go 'package pkg'
  put svc/pkg/b.go 'package pkg'
  feature
}

gremlins_stub() {
  cat > "$FX/gremlins.json" <<'EOF'
{"go_module":"toy","files":[
  {"file_name":"pkg/a.go","mutations":[
    {"type":"CONDITIONALS_NEGATION","status":"SKIPPED","line":4,"column":7}]},
  {"file_name":"pkg/b.go","mutations":[
    {"type":"INVERT_NEGATIVES","status":"KILLED","line":4,"column":11},
    {"type":"ARITHMETIC_BASE","status":"LIVED","line":4,"column":15},
    {"type":"CONDITIONALS_BOUNDARY","status":"NOT COVERED","line":6,"column":3},
    {"type":"CONDITIONALS_NEGATION","status":"TIMED OUT","line":7,"column":3},
    {"type":"INCREMENT_DECREMENT","status":"NOT VIABLE","line":8,"column":2}]},
  {"file_name":"pkg/c.go","mutations":[
    {"type":"ARITHMETIC_BASE","status":"LIVED","line":1,"column":1}]}],
 "mutants_total":5,"mutants_killed":1,"mutants_lived":2}
EOF
  stub gremlins "pwd > '$T/gremlins.cwd'
echo \"\$*\" > '$T/gremlins.args'
while [ \$# -gt 0 ]; do [ \"\$1\" = -o ] && o=\"\$2\"; shift; done
cp '$FX/gremlins.json' \"\$o\"" "${1-}"
}

@test "normalizes gremlins report" {
  go_project
  gremlins_stub
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  [ "$status" -eq 0 ]
  # SKIPPED (out of diff) and files outside the targets are dropped; gremlins
  # reports no replacement, so the column stands in for it.
  [ "$(out '.')" = '[{"file":"svc/pkg/b.go","line":4,"col":11,"mutator":"INVERT_NEGATIVES","replacement":"col:11","status":"killed"},{"file":"svc/pkg/b.go","line":4,"col":15,"mutator":"ARITHMETIC_BASE","replacement":"col:15","status":"survived"},{"file":"svc/pkg/b.go","line":6,"col":3,"mutator":"CONDITIONALS_BOUNDARY","replacement":"col:3","status":"no_coverage"},{"file":"svc/pkg/b.go","line":7,"col":3,"mutator":"CONDITIONALS_NEGATION","replacement":"col:3","status":"timeout"},{"file":"svc/pkg/b.go","line":8,"col":2,"mutator":"INCREMENT_DECREMENT","replacement":"col:2","status":"excluded"}]' ]
  # Native diff scoping against the merge-base, from the module root.
  [ "$(cat "$T/gremlins.args")" = "unleash --diff $BASE_SHA -o $WS/harden/go/report-1.json" ]
  [ "$(cd "$(cat "$T/gremlins.cwd")" && pwd -P)" = "$(cd "$WORK/svc" && pwd -P)" ]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"language":"go","tool":"gremlins","mutants":5}' ]
}

# --- C# (Stryker.NET) ----------------------------------------------------------

cs_project() {
  put src/Lib/Lib.csproj '<Project />'
  put src/Lib/B.cs 'class B {}'
  put tests/Lib.Tests/Lib.Tests.csproj '<Project />'
  feature
}

stryker_net_stub() {
  # @W@ stands for the physical worktree path: Stryker.NET keys files absolutely.
  cat > "$FX/stryker-net.json" <<'EOF'
{"schemaVersion":"2","files":{"@W@/src/Lib/B.cs":{"language":"cs","mutants":[
  {"id":"1","mutatorName":"Arithmetic operator","replacement":"a - b","status":"Killed","location":{"start":{"line":4,"column":16},"end":{"line":4,"column":21}}},
  {"id":"2","mutatorName":"Equality mutation","replacement":"a <= b","status":"Survived","location":{"start":{"line":6,"column":16},"end":{"line":6,"column":21}}},
  {"id":"3","mutatorName":"Boolean mutation","replacement":"false","status":"NoCoverage","location":{"start":{"line":8,"column":16},"end":{"line":8,"column":20}}},
  {"id":"4","mutatorName":"Block removal","replacement":"{}","status":"Timeout","location":{"start":{"line":9,"column":1},"end":{"line":9,"column":2}}},
  {"id":"5","mutatorName":"String mutation","replacement":"\"\"","status":"Ignored","statusReason":"Removed by since filter","location":{"start":{"line":10,"column":1},"end":{"line":10,"column":2}}},
  {"id":"7","mutatorName":"Statement mutation","replacement":";","status":"Ignored","statusReason":"Ignored by comment","location":{"start":{"line":12,"column":1},"end":{"line":12,"column":2}}},
  {"id":"6","mutatorName":"Linq method mutation","replacement":"First()","status":"CompileError","location":{"start":{"line":11,"column":1},"end":{"line":11,"column":2}}}]}}}
EOF
  # Like Stryker.NET (LibGit2Sharp), the stub keys files by the repository's MAIN
  # working tree, so inside a linked worktree the --since diff names files that
  # are never the ones mutated and every mutant is "Removed by since filter".
  stub dotnet-stryker "pwd > '$T/stryker.cwd'
echo \"\$*\" > '$T/stryker.args'
git rev-parse HEAD > '$T/stryker.head'
n=\$(( \$(cat '$T/stryker.count' 2>/dev/null || echo 0) + 1 )); echo \"\$n\" > '$T/stryker.count'
for a in \"\$@\"; do case \"\$a\" in --output) next=1 ;; *) [ -n \"\${next-}\" ] && o=\"\$a\"; next= ;; esac; done
mkdir -p \"\$o/reports\"
fx='$FX/stryker-net.json'; [ -f '$FX/stryker-net.'\$n'.json' ] && fx='$FX/stryker-net.'\$n'.json'
gd=\"\$(cd \"\$(git rev-parse --git-dir)\" && pwd -P)\"; cd=\"\$(cd \"\$(git rev-parse --git-common-dir)\" && pwd -P)\"
sed \"s|@W@|\$(dirname \"\$cd\")|g\" \"\$fx\" > \"\$o/reports/mutation-report.json\"
if [ \"\$gd\" != \"\$cd\" ]; then
  jq '.files[].mutants |= map(.status = \"Ignored\" | .statusReason = \"Removed by since filter\")' \"\$o/reports/mutation-report.json\" > \"\$o/r.json\"
  mv \"\$o/r.json\" \"\$o/reports/mutation-report.json\"
fi" "${1-}"
}

@test "normalizes Stryker.NET report" {
  cs_project
  stryker_net_stub
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .mutator, .replacement, .status] | join("|")] | join(";")')" = '"src/Lib/B.cs|4|Arithmetic operator|a - b|killed;src/Lib/B.cs|6|Equality mutation|a <= b|survived;src/Lib/B.cs|8|Boolean mutation|false|no_coverage;src/Lib/B.cs|9|Block removal|{}|timeout;src/Lib/B.cs|12|Statement mutation|;|excluded;src/Lib/B.cs|11|Linq method mutation|First()|excluded"' ]
  [ "$(out '[.[] | .col]')" = '[16,16,16,1,1,1]' ]
  # Only the since-filtered mutant is dropped, and the drop is reported.
  [[ "$stderr" == *"dropped 1 mutant(s) removed by the --since filter"* ]]
  # --since takes a resolved sha; the output root is under the harden workspace.
  [ "$(cat "$T/stryker.args")" = "--since:$BASE_SHA --reporter json --output $WS/harden/csharp/run-1" ]
  # Stryker.NET runs in a plain clone of WORK's HEAD under the harden workspace,
  # from the test project directory, never inside WORK itself.
  [ "$(cat "$T/stryker.head")" = "$(git -C "$WORK" rev-parse HEAD)" ]
  # (the sandbox hands the tool its physical cwd)
  case "$(cat "$T/stryker.cwd")" in
    "$(cd "$WS/harden/csharp" && pwd -P)/"*/tests/Lib.Tests) ;;
    *) echo "stryker ran in $(cat "$T/stryker.cwd")"; return 1 ;;
  esac
}

@test "Stryker.NET scopes the diff when WORK is a linked worktree" {
  cs_project
  # init-workspace makes WORK a linked worktree of the clone in src/.
  git clone -q "$WORK" "$WS/src"
  rm -rf "$WORK"
  git -C "$WS/src" worktree add -q --detach "$WORK" HEAD
  stryker_net_stub
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .status] | join("|")] | join(";")')" = '"src/Lib/B.cs|4|killed;src/Lib/B.cs|6|survived;src/Lib/B.cs|8|no_coverage;src/Lib/B.cs|9|timeout;src/Lib/B.cs|12|excluded;src/Lib/B.cs|11|excluded"' ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- JS (StrykerJS) ------------------------------------------------------------

js_project() {
  put web/package.json '{"name":"w"}'
  put web/src/x.js 'x'
  put web/src/y.js 'y'
  feature
}

strykerjs_stub() {
  cat > "$FX/strykerjs.json" <<'EOF'
{"schemaVersion":"1","files":{
  "src/x.js":{"language":"javascript","mutants":[
    {"id":"0","mutatorName":"ArithmeticOperator","replacement":"a - b","status":"Killed","location":{"start":{"line":1,"column":10},"end":{"line":1,"column":15}}},
    {"id":"1","mutatorName":"ConditionalExpression","replacement":"true","status":"Survived","location":{"start":{"line":2,"column":5},"end":{"line":2,"column":9}}},
    {"id":"2","mutatorName":"BlockStatement","replacement":"{}","status":"NoCoverage","location":{"start":{"line":3,"column":1},"end":{"line":3,"column":2}}}]},
  "src/y.js":{"language":"javascript","mutants":[
    {"id":"3","mutatorName":"StringLiteral","replacement":"\"\"","status":"Timeout","location":{"start":{"line":1,"column":1},"end":{"line":1,"column":2}}},
    {"id":"4","mutatorName":"EqualityOperator","replacement":"a != b","status":"CompileError","location":{"start":{"line":2,"column":1},"end":{"line":2,"column":2}}},
    {"id":"5","mutatorName":"UpdateOperator","replacement":"i--","status":"RuntimeError","location":{"start":{"line":3,"column":1},"end":{"line":3,"column":2}}},
    {"id":"6","mutatorName":"BooleanLiteral","replacement":"false","status":"Ignored","statusReason":"Ignored because of excluded mutation \"BooleanLiteral\"","location":{"start":{"line":4,"column":1},"end":{"line":4,"column":2}}}]}}}
EOF
  stub stryker "pwd > '$T/strykerjs.cwd'
[ \"\$1\" = run ] || exit 9
cp \"\$2\" '$T/strykerjs.cfg'
cp '$FX/strykerjs.json' \"\$(jq -r .jsonReporter.fileName \"\$2\")\"" "${1-}"
}

@test "normalizes StrykerJS report" {
  js_project
  strykerjs_stub
  run_adapter js '["web/src/x.js","web/src/y.js"]' 'npm run test:unit'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .mutator, .replacement, .status] | join("|")] | join(";")')" = '"web/src/x.js|1|ArithmeticOperator|a - b|killed;web/src/x.js|2|ConditionalExpression|true|survived;web/src/x.js|3|BlockStatement|{}|no_coverage;web/src/y.js|1|StringLiteral|\"\"|timeout;web/src/y.js|2|EqualityOperator|a != b|excluded;web/src/y.js|3|UpdateOperator|i--|excluded;web/src/y.js|4|BooleanLiteral|false|excluded"' ]
  [[ "$stderr" == *"dropped 0 mutant(s)"* ]]
  [ "$(out '[.[] | .col]')" = '[10,5,1,1,1,1,1]' ]
  C="$T/strykerjs.cfg"
  # Target list relative to the package, the injected test command, and every
  # output outside the worktree.
  [ "$(jq -c .mutate "$C")" = '["src/x.js","src/y.js"]' ]
  [ "$(jq -r .testRunner "$C")" = "command" ]
  [ "$(jq -r .commandRunner.command "$C")" = "npm run test:unit" ]
  [ "$(jq -r .coverageAnalysis "$C")" = "off" ]
  [ "$(jq -c .reporters "$C")" = '["json"]' ]
  [[ "$(jq -r .jsonReporter.fileName "$C")" == "$WS/harden/js/"* ]]
  [[ "$(jq -r .tempDirName "$C")" == "$WS/harden/js/"* ]]
  [ "$(cd "$(cat "$T/strykerjs.cwd")" && pwd -P)" = "$(cd "$WORK/web" && pwd -P)" ]
}

# --- StrykerJS runs the configured command from the sandbox cwd -------------------

# A stryker stub that does what StrykerJS does with the command runner: run
# commandRunner.command, from its own cwd (the package root of the sandbox), and
# abort without a report when that initial test run fails.
strykerjs_exec_stub() {
  strykerjs_stub
  stub stryker "[ \"\$1\" = run ] || exit 9
pwd -P >> '$T/stryker.cwds'
cmd=\"\$(jq -r .commandRunner.command \"\$2\")\"
printf '%s\n' \"\$cmd\" >> '$T/stryker.cmds'
sh -c \"\$cmd\" >> '$T/stryker.testrun' 2>&1 || { echo 'initial test run failed' >&2; exit 1; }
cp '$FX/strykerjs.json' \"\$(jq -r .jsonReporter.fileName \"\$2\")\""
}

js_two_packages() {
  js_project
  put web/test.sh 'echo web >> '"'$T/ran.web'"
  put admin/package.json '{"name":"a"}'
  put admin/src/x.js 'x'
  put admin/test.sh 'echo admin >> '"'$T/ran.admin'"
  feature
}

@test "StrykerJS: a root-relative 'cd <package> && ...' baseline command runs in the package" {
  js_project; put web/test.sh 'echo ran >> '"'$T/ran.web'"; feature
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js"]' 'cd web && sh test.sh'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/stryker.cmds")" = "sh test.sh" ]
  [ "$(cat "$T/ran.web")" = "ran" ]
}

@test "StrykerJS: 'cd ./<package>/ && ...' is recognised too" {
  js_project; put web/test.sh 'echo ran >> '"'$T/ran.web'"; feature
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js"]' 'cd ./web/ && sh test.sh'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/stryker.cmds")" = "sh test.sh" ]
}

@test "StrykerJS: a package-local command is used unchanged" {
  js_project; put web/test.unit.sh 'echo unit >> '"'$T/ran.web'"; feature
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js"]' 'sh test.unit.sh'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/stryker.cmds")" = "sh test.unit.sh" ]
  [ "$(cat "$T/ran.web")" = "unit" ]
}

@test "StrykerJS: two packages run a package-local command each from its own root" {
  js_two_packages
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js","admin/src/x.js"]' 'sh test.sh'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/ran.web")" = "web" ]
  [ "$(cat "$T/ran.admin")" = "admin" ]
  [ "$(wc -l < "$T/stryker.cwds" | tr -d ' ')" = "2" ]
}

@test "StrykerJS: a command aimed at one package is rejected when another package changed" {
  js_two_packages
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js","admin/src/x.js"]' 'cd web && sh test.sh'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"root-relative"* && "$stderr" == *"admin"* && "$stderr" == *"per-package"* ]]
  # Nothing was substituted and nothing ran for the package it does not fit.
  [ ! -e "$T/ran.admin" ]
}

@test "StrykerJS: a cd into some other directory is rejected, never replaced by npm test" {
  js_project; put web/test.sh 'echo ran >> '"'$T/ran.web'"; feature
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js"]' 'cd elsewhere && sh test.sh'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"root-relative"* ]]
  [ ! -e "$T/stryker.cmds" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

@test "StrykerJS: a second cd after the package cd is ambiguous and rejected" {
  js_project; put web/test.sh 'echo ran'; feature
  strykerjs_exec_stub
  run_adapter js '["web/src/x.js"]' 'cd web && cd src && sh ../test.sh'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"changes directory"* ]]
  [ ! -e "$T/stryker.cmds" ]
}

@test "StrykerJS: at the repository root the command is used as given" {
  put package.json '{"name":"root"}'
  put src/x.js 'x'
  put dashboard/test.sh 'echo root-ran >> '"'$T/ran.root'"
  feature
  strykerjs_exec_stub
  run_adapter js '["src/x.js"]' 'cd dashboard && sh test.sh'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(cat "$T/stryker.cmds")" = "cd dashboard && sh test.sh" ]
  [ "$(cat "$T/ran.root")" = "root-ran" ]
}

# --- Python (mutmut) -----------------------------------------------------------

py_project() {
  put pyproject.toml '[project]
name = "toypkg"'
  put setup.cfg '[tool:pytest]
addopts = -q

[mutmut]
source_paths = elsewhere'
  put toypkg/__init__.py ''
  put toypkg/b.py 'def add(a, b):
    return a + b


def sub(a, b):
    return a - b'
  feature
}

show() {
  mkdir -p "$FX/show"
  printf '%s\n' "$2" > "$FX/show/$1"
}

mutmut_stub() {
  show toypkg.b.x_add__mutmut_1 '# toypkg.b.x_add__mutmut_1: killed
--- toypkg/b.py
+++ toypkg/b.py
@@ -1,2 +1,2 @@
 def add(a, b):
-    return a + b
+    return a - b'
  show toypkg.b.x_add__mutmut_2 '# toypkg.b.x_add__mutmut_2: survived
--- toypkg/b.py
+++ toypkg/b.py
@@ -1,2 +1,2 @@
 def add(a, b):
-    return a + b
+    return a + b + 1'
  show toypkg.b.x_sub__mutmut_1 '# toypkg.b.x_sub__mutmut_1: no tests
--- toypkg/b.py
+++ toypkg/b.py
@@ -1,2 +1,2 @@
 def sub(a, b):
-    return a - b
+    return a + b'
  show toypkg.b.x_sub__mutmut_2 '# toypkg.b.x_sub__mutmut_2: timeout
--- toypkg/b.py
+++ toypkg/b.py
@@ -1,2 +1,2 @@
 def sub(a, b):
-    return a - b
+    return None'
  show toypkg.b.x_sub__mutmut_3 '# toypkg.b.x_sub__mutmut_3: not checked
--- toypkg/b.py
+++ toypkg/b.py
@@ -1,2 +1,2 @@
 def sub(a, b):
-    return a - b
+    return a'
  printf 'toypkg/b.py.meta' > "$FX/meta.path"
  printf '%s' '{"exit_code_by_key": {"toypkg.b.x_add__mutmut_1": 1, "toypkg.b.x_add__mutmut_2": 0, "toypkg.b.x_sub__mutmut_1": 33, "toypkg.b.x_sub__mutmut_2": 36, "toypkg.b.x_sub__mutmut_3": null}, "durations_by_key": {}}' > "$FX/meta.json"
  stub mutmut "case \"\$1\" in
  run)
    pwd > '$T/mutmut.cwd'
    cp setup.cfg '$T/mutmut.cfg'
    m=\"mutants/\$(cat '$FX/meta.path')\"
    mkdir -p \"\$(dirname \"\$m\")\"
    cp '$FX/meta.json' \"\$m\" ;;
  show) cat '$FX/show/'\"\$2\" ;;
  *) exit 9 ;;
esac" "${1-}"
}

@test "normalizes mutmut report" {
  py_project
  mutmut_stub
  run_adapter python '["toypkg/b.py"]' 'pytest'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .mutator, .replacement, .status] | join("|")] | join(";")')" = '"toypkg/b.py|2|mutmut|return a - b|killed;toypkg/b.py|2|mutmut|return a + b + 1|survived;toypkg/b.py|6|mutmut|return a + b|no_coverage;toypkg/b.py|6|mutmut|return None|timeout;toypkg/b.py|6|mutmut|return a|excluded"' ]
  # mutmut ran in a copy under the harden workspace with a generated [mutmut]
  # section that replaces the project's own and keeps its other sections.
  [[ "$(cd "$(cat "$T/mutmut.cwd")" && pwd -P)" == "$(cd "$WS/harden" && pwd -P)/python/"* ]]
  grep -q '^\[tool:pytest\]' "$T/mutmut.cfg"
  ! grep -q 'elsewhere' "$T/mutmut.cfg"
  [ "$(sed -n '/^\[mutmut\]/,$p' "$T/mutmut.cfg" | tr -d ' ' | tr '\n' '|')" = '[mutmut]|source_paths=|toypkg|only_mutate=|toypkg/b.py|' ]
}

# --- Java (PIT) ----------------------------------------------------------------

java_project() {
  put pom.xml '<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>toy</groupId>
  <artifactId>toy</artifactId>
  <version>1</version>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.10.0</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>'
  put src/main/java/toy/B.java 'package toy;

public class B {}'
  feature
}

mvn_stub() {
  cat > "$FX/mutations.xml" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<mutations partial="true">
<mutation detected='true' status='KILLED' numberOfTestsRun='1'><sourceFile>B.java</sourceFile><mutatedClass>toy.B</mutatedClass><mutatedMethod>add</mutatedMethod><methodDescription>(II)I</methodDescription><lineNumber>4</lineNumber><mutator>org.pitest.mutationtest.engine.gregor.mutators.MathMutator</mutator><indexes><index>6</index></indexes><blocks><block>0</block></blocks><killingTest>toy.BTest.[engine:junit-jupiter]/[class:toy.BTest]/[method:add()]</killingTest><description>Replaced integer addition with subtraction</description></mutation>
<mutation detected='false' status='SURVIVED' numberOfTestsRun='1'><sourceFile>B.java</sourceFile><mutatedClass>toy.B</mutatedClass><mutatedMethod>lt</mutatedMethod><methodDescription>(II)Z</methodDescription><lineNumber>8</lineNumber><mutator>org.pitest.mutationtest.engine.gregor.mutators.ConditionalsBoundaryMutator</mutator><indexes><index>2</index></indexes><blocks><block>0</block></blocks><killingTest/><description>changed conditional boundary</description></mutation>
<mutation detected='false' status='NO_COVERAGE' numberOfTestsRun='0'><sourceFile>B.java</sourceFile><mutatedClass>toy.B$Inner</mutatedClass><mutatedMethod>name</mutatedMethod><methodDescription>()Ljava/lang/String;</methodDescription><lineNumber>12</lineNumber><mutator>org.pitest.mutationtest.engine.gregor.mutators.returns.EmptyObjectReturnValsMutator</mutator><indexes><index>1</index></indexes><blocks><block>0</block></blocks><killingTest/><description>replaced return value with &quot;&quot; for toy/B$Inner::name &amp; more</description></mutation>
<mutation detected='true' status='TIMED_OUT' numberOfTestsRun='1'><sourceFile>B.java</sourceFile><mutatedClass>toy.B</mutatedClass><mutatedMethod>loop</mutatedMethod><methodDescription>()V</methodDescription><lineNumber>9</lineNumber><mutator>org.pitest.mutationtest.engine.gregor.mutators.IncrementsMutator</mutator><indexes><index>3</index></indexes><blocks><block>1</block></blocks><killingTest/><description>Changed increment from 1 to -1</description></mutation>
<mutation detected='false' status='RUN_ERROR' numberOfTestsRun='0'><sourceFile>B.java</sourceFile><mutatedClass>toy.B</mutatedClass><mutatedMethod>x</mutatedMethod><methodDescription>()V</methodDescription><lineNumber>10</lineNumber><mutator>org.pitest.mutationtest.engine.gregor.mutators.VoidMethodCallMutator</mutator><indexes><index>3</index></indexes><blocks><block>1</block></blocks><killingTest/><description>removed call to x</description></mutation>
</mutations>
EOF
  stub mvn "pwd > '$T/mvn.cwd'
echo \"\$*\" > '$T/mvn.args'
cp pom.xml '$T/mvn.pom'
for a in \"\$@\"; do case \"\$a\" in -DreportsDirectory=*) d=\"\${a#-DreportsDirectory=}\" ;; esac; done
mkdir -p \"\$d\"
cp '$FX/mutations.xml' \"\$d/mutations.xml\"" "${1-}"
}

@test "normalizes PIT report" {
  java_project
  mvn_stub
  run_adapter java '["src/main/java/toy/B.java"]' 'mvn test'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .mutator, .replacement, .status] | join("|")] | join(";")')" = '"src/main/java/toy/B.java|4|MathMutator|Replaced integer addition with subtraction|killed;src/main/java/toy/B.java|8|ConditionalsBoundaryMutator|changed conditional boundary|survived;src/main/java/toy/B.java|12|EmptyObjectReturnValsMutator|replaced return value with \"\" for toy/B$Inner::name & more|no_coverage;src/main/java/toy/B.java|9|IncrementsMutator|Changed increment from 1 to -1|timeout;src/main/java/toy/B.java|10|VoidMethodCallMutator|removed call to x|excluded"' ]
  A="$(cat "$T/mvn.args")"
  [[ "$A" == *"-DtargetClasses=toy.B,toy.B\$* "* ]]
  [[ "$A" == *"org.pitest:pitest-maven:mutationCoverage"* ]]
  [[ "$A" == *"-DreportsDirectory=$WS/harden/java/"* ]]
  [[ "$A" == *"-DoutputFormats=XML"* ]]
  [[ "$A" == *"-DtimestampedReports=false"* ]]
  # JUnit 5 needs pitest-junit5-plugin: declared in the copy's pom only.
  grep -q 'pitest-junit5-plugin' "$T/mvn.pom"
  grep -q '<artifactId>pitest-maven</artifactId>' "$T/mvn.pom"
  ! grep -q 'pitest' "$WORK/pom.xml"
  [[ "$(cd "$(cat "$T/mvn.cwd")" && pwd -P)" == "$(cd "$WS/harden" && pwd -P)/java/"* ]]
}

@test "PIT plugin goes into an existing top-level plugins list, not pluginManagement" {
  put pom.xml '<project>
  <dependencies><dependency><artifactId>junit-jupiter</artifactId></dependency></dependencies>
  <build>
    <pluginManagement>
      <plugins>
        <plugin><artifactId>managed</artifactId></plugin>
      </plugins>
    </pluginManagement>
    <plugins>
      <plugin><artifactId>compiler</artifactId></plugin>
    </plugins>
  </build>
</project>'
  put src/main/java/toy/B.java 'package toy;'
  feature
  mvn_stub
  run_adapter java '["src/main/java/toy/B.java"]' 'mvn test'
  [ "$status" -eq 0 ]
  # The plugin sits after compiler, inside the second (top-level) plugins list.
  [ "$(grep -n 'pitest-maven\|compiler\|managed' "$T/mvn.pom" | cut -d: -f2- | tr -d ' ' | sed 's/<[^>]*>//g' | tr '\n' ',')" = "managed,compiler,pitest-maven," ]
  [ "$(grep -c '<plugins>' "$T/mvn.pom")" = "2" ]
}

@test "a Gradle-only Java target list exits 1" {
  put build.gradle 'plugins { id "java" }'
  put src/main/java/toy/B.java 'package toy;'
  feature
  mvn_stub
  run_adapter java '["src/main/java/toy/B.java"]' 'gradle test'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"Maven"* ]]
}

# --- Rust (cargo-mutants) ------------------------------------------------------

rust_project() {
  put Cargo.toml '[package]
name = "toy"'
  put src/lib.rs 'pub fn add() {}'
  put src/other.rs 'pub fn other() {}'
  feature
}

cargo_stub() {
  cat > "$FX/outcomes.json" <<'EOF'
{"outcomes":[
  {"scenario":"Baseline","summary":"Success"},
  {"scenario":{"Mutant":{"package":"toy","file":"src/lib.rs","function":{"function_name":"add"},"span":{"start":{"line":2,"column":5},"end":{"line":2,"column":10}},"replacement":"0","genre":"FnValue"}},"summary":"CaughtMutant"},
  {"scenario":{"Mutant":{"package":"toy","file":"src/lib.rs","function":{"function_name":"add"},"span":{"start":{"line":2,"column":5},"end":{"line":2,"column":10}},"replacement":"1","genre":"FnValue"}},"summary":"MissedMutant"},
  {"scenario":{"Mutant":{"package":"toy","file":"src/lib.rs","function":{"function_name":"sub"},"span":{"start":{"line":6,"column":7},"end":{"line":6,"column":8}},"replacement":"+","genre":"BinaryOperator"}},"summary":"Timeout"},
  {"scenario":{"Mutant":{"package":"toy","file":"src/lib.rs","function":{"function_name":"sub"},"span":{"start":{"line":6,"column":5},"end":{"line":6,"column":10}},"replacement":"Default::default()","genre":"FnValue"}},"summary":"Unviable"}]}
EOF
  stub cargo "[ \"\$1\" = mutants ] || exit 9
pwd > '$T/cargo.cwd'
echo \"\$*\" > '$T/cargo.args'
while [ \$# -gt 0 ]; do
  case \"\$1\" in --in-diff) cp \"\$2\" '$T/cargo.diff' ;; --output) o=\"\$2\" ;; esac
  shift
done
mkdir -p \"\$o/mutants.out\"
cp '$FX/outcomes.json' \"\$o/mutants.out/outcomes.json\"" "${1-}"
}

@test "normalizes cargo-mutants report" {
  rust_project
  cargo_stub
  run_adapter rust '["src/lib.rs"]' 'cargo test --release'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | [.file, .line, .mutator, .replacement, .status] | join("|")] | join(";")')" = '"src/lib.rs|2|FnValue|0|killed;src/lib.rs|2|FnValue|1|survived;src/lib.rs|6|BinaryOperator|+|timeout;src/lib.rs|6|FnValue|Default::default()|excluded"' ]
  # Native diff scoping, restricted to the targets, and the test arguments.
  grep -q '^+++ b/src/lib.rs' "$T/cargo.diff"
  ! grep -q 'other.rs' "$T/cargo.diff"
  A="$(cat "$T/cargo.args")"
  [[ "$A" == *"--output $WS/harden/rust/"* ]]
  [[ "$A" == *"--cargo-test-arg --release"* ]]
}

# --- Format drift: the tool ran mutants, but the adapter cannot trust them ------

# fails_closed <lang> <targets> [stderr fragment]
fails_closed() {
  run_adapter "$1" "$2" "${4:-x}"
  [ "$status" -eq 1 ] || { echo "exit $status"; return 1; }
  [ ! -e "$T/out.json" ] || { echo "wrote $T/out.json"; return 1; }
  [ -z "${3-}" ] || [[ "$stderr" == *"$3"* ]] || { echo "stderr: $stderr"; return 1; }
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

@test "gremlins drift: an unknown status in a target exits 1" {
  go_project; gremlins_stub
  jq '.files[1].mutations[0].status = "EXPLODED"' "$FX/gremlins.json" > "$T/x" && mv "$T/x" "$FX/gremlins.json"
  fails_closed go '["svc/pkg/b.go"]' "EXPLODED"
}

@test "gremlins drift: mutants that map to no target exit 1" {
  go_project; gremlins_stub
  jq '.files |= map(.file_name |= "moved/" + .)' "$FX/gremlins.json" > "$T/x" && mv "$T/x" "$FX/gremlins.json"
  fails_closed go '["svc/pkg/b.go"]' "map"
}

@test "gremlins: only out-of-diff mutants is an empty result, not drift" {
  go_project; gremlins_stub
  printf '{"files":[{"file_name":"pkg/a.go","mutations":[{"type":"X","status":"SKIPPED","line":1,"column":1}]}]}' > "$FX/gremlins.json"
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  [ "$status" -eq 0 ]
  [ "$(out .)" = "[]" ]
}

@test "go: no go.mod above any target exits 1" {
  put pkg/b.go 'package pkg'; feature; gremlins_stub
  fails_closed go '["pkg/b.go"]' "go.mod"
}

@test "Stryker.NET drift: an unknown status exits 1" {
  cs_project; stryker_net_stub
  sed 's/"Survived"/"Pending"/' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  fails_closed csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' "Pending"
}

@test "Stryker.NET drift: report paths outside the worktree exit 1" {
  cs_project; stryker_net_stub
  sed 's|@W@|/elsewhere|' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  fails_closed csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' "map"
}

@test "Stryker.NET: two test projects reporting one mutant merge to its best status" {
  cs_project
  put tests/Other.Tests/Other.Tests.csproj '<Project />'
  feature
  stryker_net_stub
  jq '.files[].mutants |= map(if .id == "2" then .status = "Killed" else . end)' "$FX/stryker-net.json" > "$FX/stryker-net.2.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out 'length')" = "6" ]
  [ "$(out '[.[] | select(.line == 6) | .status]')" = '["killed"]' ]
}

@test "Stryker.NET: a key collision inside one project is kept, not merged" {
  cs_project; stryker_net_stub
  jq '.files[].mutants += [{"id":"7","mutatorName":"Equality mutation","replacement":"a <= b","status":"Killed","location":{"start":{"line":6,"column":30}}}]' \
    "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out '[.[] | select(.line == 6) | .status]')" = '["survived","killed"]' ]
}

# Two mutations of one line with the same mutator and replacement ("a - b" twice,
# at columns 5 and 9). Identity across projects is (file, line, start column,
# mutator, replacement); report order is not identity.
two_mutant_report() { # <first-status> <second-status> <order: ab|ba> <out>
  jq -n --arg a "$1" --arg b "$2" --arg o "$3" '
    def m($id; $col; $st): {id: $id, mutatorName: "Arithmetic operator", replacement: "a - b", status: $st,
                            location: {start: {line: 4, column: $col}, end: {line: 4, column: ($col + 4)}}};
    {schemaVersion: "2", files: {"@W@/src/Lib/B.cs": {language: "cs",
      mutants: (if $o == "ab" then [m("1"; 5; $a), m("2"; 9; $b)] else [m("2"; 9; $b), m("1"; 5; $a)] end)}}}' > "$4"
}

two_project_setup() {
  cs_project
  put tests/Other.Tests/Other.Tests.csproj '<Project />'
  feature
  stryker_net_stub
}

@test "Stryker.NET: reordered two-project reports keep the survivor, keyed by start column" {
  two_project_setup
  two_mutant_report Killed Survived ab "$FX/stryker-net.json"
  two_mutant_report Killed Survived ba "$FX/stryker-net.2.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  # A at col 5 stays killed, B at col 9 stays survived: matching by ordinal would
  # pair the report-1 survivor with the report-2 kill and fabricate a second kill.
  [ "$(out '[.[] | [.col, .status] | join(":")]')" = '["5:killed","9:survived"]' ]
}

@test "Stryker.NET: the reordered survivor reaches state.json and the score" {
  two_project_setup
  two_mutant_report Killed Survived ab "$FX/stryker-net.json"
  two_mutant_report Killed Survived ba "$FX/stryker-net.2.json"
  snap="$(jq -nc --arg w "$WORK" --arg ws "$WS" --arg mb "$BASE_SHA" \
    '{work_dir:$w, workspace:$ws, harden_round:0, gate_threshold:80, scope:{merge_base:$mb},
      detect:{languages:[{language:"csharp", tool:"Stryker.NET", status:"runnable",
        targets:["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"], test_command:"dotnet test"}]} }')"
  run --separate-stderr env PATH="$STUBS:$SYS" sh -c 'printf "%s" "$1" | sh "$2" >/dev/null && printf "%s" "$1" | sh "$3"' _ \
    "$snap" "$D/mutate.sh" "$D/score.sh"
  [ "$status" -eq 0 ] || { echo "exit $status: $stderr"; return 1; }
  [ "$(jq -r '.mutants["src/Lib/B.cs:4:5:Arithmetic operator:a - b"].status' "$WS/harden/state.json")" = killed ]
  [ "$(jq -r '.mutants["src/Lib/B.cs:4:9:Arithmetic operator:a - b"].status' "$WS/harden/state.json")" = survived ]
  [ "$(printf '%s' "$output" | jq -c '[.killed, .survived, .score]')" = '[1,1,50]' ]
  [ "$(jq -c '[.survivors[].mutant_id]' "$WS/harden/survivors-round.json")" = '["src/Lib/B.cs:4:9:Arithmetic operator:a - b"]' ]
}

@test "Stryker.NET: an unresolved collision never earns a kill from ordinal matching" {
  two_project_setup
  # Project 1 holds the same identity twice (col 5, killed then survived); project
  # 2 holds it once, killed. The three reports cannot be paired up, so both
  # mutants take the pessimistic status instead of a kill.
  jq -n '{schemaVersion:"2", files:{"@W@/src/Lib/B.cs":{language:"cs", mutants:[
    {id:"1",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Killed",location:{start:{line:4,column:5}}},
    {id:"2",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Survived",location:{start:{line:4,column:5}}}]}}}' > "$FX/stryker-net.json"
  jq -n '{schemaVersion:"2", files:{"@W@/src/Lib/B.cs":{language:"cs", mutants:[
    {id:"1",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Killed",location:{start:{line:4,column:5}}}]}}}' > "$FX/stryker-net.2.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(out '[.[] | .status]')" = '["survived","survived"]' ]
  # Report order does not change the outcome.
  jq -n '{schemaVersion:"2", files:{"@W@/src/Lib/B.cs":{language:"cs", mutants:[
    {id:"2",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Survived",location:{start:{line:4,column:5}}},
    {id:"1",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Killed",location:{start:{line:4,column:5}}}]}}}' > "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"]' 'dotnet test'
  [ "$(out '[.[] | .status]')" = '["survived","survived"]' ]
}

@test "Stryker.NET: a collision whose every report says killed stays killed" {
  two_project_setup
  jq -n '{schemaVersion:"2", files:{"@W@/src/Lib/B.cs":{language:"cs", mutants:[
    {id:"1",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Killed",location:{start:{line:4,column:5}}},
    {id:"2",mutatorName:"Arithmetic operator",replacement:"a - b",status:"Timeout",location:{start:{line:4,column:5}}}]}}}' > "$FX/stryker-net.json"
  cp "$FX/stryker-net.json" "$FX/stryker-net.2.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Other.Tests/Other.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(out '[.[] | .status]')" = '["timeout","timeout"]' ]
}

@test "StrykerJS drift: an unknown status exits 1" {
  js_project; strykerjs_stub
  sed 's/"Survived"/"Pending"/' "$FX/strykerjs.json" > "$T/x" && mv "$T/x" "$FX/strykerjs.json"
  fails_closed js '["web/src/x.js"]' "Pending"
}

@test "js: no package.json above any target exits 1" {
  put src/x.js 'x'; feature; strykerjs_stub
  fails_closed js '["src/x.js"]' "package.json"
}

@test "mutmut: an unknown exit code is excluded with a warning, never killed" {
  py_project; mutmut_stub
  printf '%s' '{"exit_code_by_key": {"toypkg.b.x_add__mutmut_1": 77}}' > "$FX/meta.json"
  run_adapter python '["toypkg/b.py"]' 'pytest'
  [ "$status" -eq 0 ]
  [ "$(out '[.[].status]')" = '["excluded"]' ]
  [[ "$stderr" == *"77"* ]]
}

@test "mutmut drift: results only for files that are not targets exit 1" {
  py_project; mutmut_stub
  printf 'toypkg/other.py.meta' > "$FX/meta.path"
  fails_closed python '["toypkg/b.py"]' "map"
}

@test "PIT drift: an unknown status exits 1" {
  java_project; mvn_stub
  sed "s/status='SURVIVED'/status='EXPLODED'/" "$FX/mutations.xml" > "$T/x" && mv "$T/x" "$FX/mutations.xml"
  fails_closed java '["src/main/java/toy/B.java"]' "EXPLODED"
}

@test "PIT drift: mutated classes that map to no target exit 1" {
  java_project; mvn_stub
  sed 's/<mutatedClass>toy\.B/<mutatedClass>moved.B/' "$FX/mutations.xml" > "$T/x" && mv "$T/x" "$FX/mutations.xml"
  fails_closed java '["src/main/java/toy/B.java"]' "map"
}

@test "cargo-mutants drift: an unknown outcome exits 1" {
  rust_project; cargo_stub
  sed 's/"MissedMutant"/"WeirdMutant"/' "$FX/outcomes.json" > "$T/x" && mv "$T/x" "$FX/outcomes.json"
  fails_closed rust '["src/lib.rs"]' "WeirdMutant"
}

@test "rust: no Cargo.toml above any target exits 1" {
  put src/lib.rs 'pub fn add() {}'; feature; cargo_stub
  fails_closed rust '["src/lib.rs"]' "Cargo.toml"
}

# --- Missing or non-numeric line is drift ---------------------------------------

@test "gremlins drift: a non-numeric line exits 1" {
  go_project; gremlins_stub
  jq '.files[1].mutations[0].line = "4"' "$FX/gremlins.json" > "$T/x" && mv "$T/x" "$FX/gremlins.json"
  fails_closed go '["svc/pkg/b.go"]' "line"
}

@test "Stryker.NET drift: a missing line exits 1" {
  cs_project; stryker_net_stub
  jq '.files[].mutants[0].location.start |= del(.line)' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  fails_closed csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' "line"
}

@test "StrykerJS drift: a missing location exits 1" {
  js_project; strykerjs_stub
  jq '.files["src/x.js"].mutants[1] |= del(.location)' "$FX/strykerjs.json" > "$T/x" && mv "$T/x" "$FX/strykerjs.json"
  fails_closed js '["web/src/x.js"]' "line"
}

@test "mutmut drift: a diff with no removed line exits 1" {
  py_project; mutmut_stub
  show toypkg.b.x_add__mutmut_2 '# toypkg.b.x_add__mutmut_2: survived'
  fails_closed python '["toypkg/b.py"]' "line"
}

@test "PIT drift: a non-numeric lineNumber exits 1" {
  java_project; mvn_stub
  sed 's|<lineNumber>8</lineNumber>|<lineNumber>eight</lineNumber>|' "$FX/mutations.xml" > "$T/x" && mv "$T/x" "$FX/mutations.xml"
  fails_closed java '["src/main/java/toy/B.java"]' "line"
}

@test "cargo-mutants drift: a missing span line exits 1" {
  rust_project; cargo_stub
  jq '.outcomes[1].scenario.Mutant.span.start.line = null' "$FX/outcomes.json" > "$T/x" && mv "$T/x" "$FX/outcomes.json"
  fails_closed rust '["src/lib.rs"]' "line"
}

# --- A broken mutation build fails the gate --------------------------------------

# gate_after <lang> <tool> <targets-json> : mutate.sh then score.sh over the stubs
gate_after() {
  snap="$(jq -nc --arg w "$WORK" --arg ws "$WS" --arg mb "$BASE_SHA" --arg l "$1" --arg t "$2" --argjson tg "$3" \
    '{work_dir:$w, workspace:$ws, harden_round:0, gate_threshold:80, scope:{merge_base:$mb},
      detect:{languages:[{language:$l, tool:$t, status:"runnable", targets:$tg, test_command:"npm test"}]} }')"
  run --separate-stderr env PATH="$STUBS:$SYS" sh -c 'printf "%s" "$1" | sh "$2" >/dev/null && printf "%s" "$1" | sh "$3"' _ \
    "$snap" "$D/mutate.sh" "$D/score.sh"
  [ "$status" -eq 0 ] || { echo "exit $status: $stderr"; return 1; }
  [ "$(printf '%s' "$output" | jq -r .score)" = "null" ]
  [ "$(printf '%s' "$output" | jq -r .gate_passed)" = "false" ]
  [[ "$(printf '%s' "$output" | jq -r .reason)" == "all "*" mutants excluded"* ]]
}

@test "PIT with every mutant RUN_ERROR fails the gate" {
  java_project; mvn_stub
  sed "s/status='[A-Z_]*'/status='RUN_ERROR'/" "$FX/mutations.xml" > "$T/x" && mv "$T/x" "$FX/mutations.xml"
  gate_after java PIT '["src/main/java/toy/B.java"]'
}

@test "Stryker.NET with every mutant CompileError fails the gate" {
  cs_project; stryker_net_stub
  jq '.files[].mutants[].status = "CompileError"' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  gate_after csharp Stryker.NET '["tests/Lib.Tests/Lib.Tests.csproj"]'
}

@test "mutmut with every exit code unknown fails the gate" {
  py_project; mutmut_stub
  jq '.exit_code_by_key |= map_values(77)' "$FX/meta.json" > "$T/x" && mv "$T/x" "$FX/meta.json"
  gate_after python mutmut '["toypkg/b.py"]'
}

@test "Stryker.NET since-filtered Ignored mutants are dropped; other Ignored are excluded" {
  cs_project; stryker_net_stub
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Removed by since filter")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out .)" = "[]" ]
  [[ "$stderr" == *"dropped 7 mutant(s) removed by the --since filter"* ]]
  jq '.files[].mutants |= map(.statusReason = "Ignored by comment")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(out '[.[].status] | unique')" = '["excluded"]' ]
  [ "$(out length)" = "7" ]
}

@test "Stryker.NET warns when every mutant is since-filtered" {
  cs_project; stryker_net_stub
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Removed by since filter")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [[ "$stderr" == *"warning: all mutants since-filtered"* ]]
  [ "$(printf '%s' "$output" | jq -c .)" = '{"language":"csharp","tool":"Stryker.NET","mutants":0,"warning":"all mutants since-filtered"}' ]
}

@test "Stryker.NET does not warn when some mutants survive the since filter" {
  cs_project; stryker_net_stub
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [[ "$stderr" != *"all mutants since-filtered"* ]]
  [ "$(printf '%s' "$output" | jq -c 'has("warning")')" = "false" ]
}

@test "Stryker.NET does not warn when the report has no mutants at all" {
  cs_project; stryker_net_stub
  jq '.files[].mutants = []' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [[ "$stderr" != *"all mutants since-filtered"* ]]
  [ "$(printf '%s' "$output" | jq -c 'has("warning")')" = "false" ]
}

@test "Stryker.NET does not warn when the rest are Ignored for another reason" {
  cs_project; stryker_net_stub
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Ignored by comment") | .files[].mutants[0].statusReason = "Removed by since filter"' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c 'has("warning")')" = "false" ]
}

@test "Stryker.NET warns only when every project is all since-filtered" {
  cs_project; stryker_net_stub
  put tests/Two.Tests/Two.Tests.csproj '<Project />'
  feature
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Removed by since filter")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Two.Tests/Two.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .warning)" = "all mutants since-filtered" ]
}

@test "Stryker.NET does not warn when one of two projects keeps mutants" {
  cs_project; stryker_net_stub
  put tests/Two.Tests/Two.Tests.csproj '<Project />'
  feature
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Removed by since filter")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.1.json"
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj","tests/Two.Tests/Two.Tests.csproj"]' 'dotnet test'
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -c 'has("warning")')" = "false" ]
}

@test "Stryker.NET with every mutant Ignored by config fails the gate" {
  cs_project; stryker_net_stub
  jq '.files[].mutants |= map(.status = "Ignored" | .statusReason = "Removed by mutator filter")' "$FX/stryker-net.json" > "$T/x" && mv "$T/x" "$FX/stryker-net.json"
  gate_after csharp Stryker.NET '["tests/Lib.Tests/Lib.Tests.csproj"]'
}

@test "StrykerJS with every mutant Ignored fails the gate" {
  js_project; strykerjs_stub
  jq '.files[].mutants |= map(.status = "Ignored")' "$FX/strykerjs.json" > "$T/x" && mv "$T/x" "$FX/strykerjs.json"
  gate_after js StrykerJS '["web/src/x.js","web/src/y.js"]'
}

# --- Every adapter ---------------------------------------------------------------

@test "tool missing stub fails" {
  for l in go csharp js python java rust; do
    run_adapter "$l" '["a"]' 'x'
    [ "$status" -eq 1 ] || { echo "$l exited $status"; return 1; }
    [ ! -e "$T/out.json" ] || { echo "$l wrote output"; return 1; }
    [[ "$stderr" == *"not found"* ]] || { echo "$l: $stderr"; return 1; }
  done
}

@test "a dirty worktree is refused and left as it was" {
  go_project
  gremlins_stub
  echo wip >> "$WORK/README"
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"not clean"* ]]
  [ "$(git -C "$WORK" status --porcelain)" = " M README" ]
  [ ! -e "$T/gremlins.args" ]
}

@test "a tool that writes no report fails and still restores the worktree" {
  js_project
  stub stryker 'exit 1' dirty
  run_adapter js '["web/src/x.js"]' 'npm test'
  [ "$status" -eq 1 ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- Cleanliness (P11): each stub deliberately writes into the worktree ---------

clean_after() {
  [ "$status" -eq 0 ] || { echo "exit $status: $stderr"; return 1; }
  [ -z "$(git -C "$WORK" status --porcelain)" ] || { git -C "$WORK" status --porcelain; return 1; }
  [ "$(cat "$WORK/README")" = "base" ]
}

@test "go adapter leaves the worktree clean" {
  go_project; gremlins_stub dirty
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  clean_after
}

@test "csharp adapter leaves the worktree clean" {
  cs_project; stryker_net_stub dirty
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  clean_after
}

@test "js adapter leaves the worktree clean" {
  js_project; strykerjs_stub dirty
  run_adapter js '["web/src/x.js"]' 'npm test'
  clean_after
}

@test "python adapter leaves the worktree clean" {
  py_project; mutmut_stub dirty
  run_adapter python '["toypkg/b.py"]' 'pytest'
  clean_after
}

@test "java adapter leaves the worktree clean" {
  java_project; mvn_stub dirty
  run_adapter java '["src/main/java/toy/B.java"]' 'mvn test'
  clean_after
}

@test "rust adapter leaves the worktree clean" {
  rust_project; cargo_stub dirty
  run_adapter rust '["src/lib.rs"]' 'cargo test'
  clean_after
}

@test "ignored build output survives the restore" {
  printf 'bin/\n' > "$WORK/.gitignore"
  git -C "$WORK" add .gitignore
  git -C "$WORK" commit -q -m ignore
  go_project
  mkdir -p "$WORK/bin" && echo keep > "$WORK/bin/cache"
  gremlins_stub dirty
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  clean_after
  [ "$(cat "$WORK/bin/cache")" = "keep" ]
}

# --- Real gremlins -----------------------------------------------------------------

@test "real gremlins on a tiny Go module" {
  G="$(command -v gremlins || true)"
  if [ -z "$G" ] && command -v go >/dev/null 2>&1; then
    G="$(go env GOPATH)/bin/gremlins"
  fi
  [ -x "$G" ] || skip "gremlins not installed"
  put go.mod 'module toy

go 1.22'
  put pkg/a.go 'package pkg

func A(x int) bool { return x > 2 }'
  put pkg/a_test.go 'package pkg

import "testing"

func TestA(t *testing.T) {
	if !A(5) {
		t.Fatal()
	}
}'
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m a
  BASE_SHA="$(git -C "$WORK" rev-parse HEAD)"
  put pkg/b.go 'package pkg

func B(x, y int) int { return x + y }

func C(x int) bool { return x < 10 }'
  put pkg/b_test.go 'package pkg

import "testing"

func TestB(t *testing.T) {
	if B(1, 2) != 3 {
		t.Fatal()
	}
}'
  feature
  printf '["pkg/b.go"]' > "$T/targets.json"
  snap="$(jq -nc --arg w "$WORK" --arg ws "$WS" --arg mb "$BASE_SHA" '{work_dir:$w, workspace:$ws, scope:{merge_base:$mb}}')"
  run --separate-stderr env PATH="$(dirname "$G"):$PATH" GOFLAGS=-mod=mod sh -c 'printf "%s" "$1" | sh "$2" "$3" "$4" "$5"' _ \
    "$snap" "$D/mutate-go.sh" "$T/targets.json" "go test ./..." "$T/out.json"
  [ "$status" -eq 0 ] || { echo "$stderr"; return 1; }
  [ "$(out '[.[] | "\(.line):\(.mutator):\(.status)"] | sort | join(",")')" = '"3:ARITHMETIC_BASE:killed,5:CONDITIONALS_BOUNDARY:no_coverage,5:CONDITIONALS_NEGATION:no_coverage"' ]
  [ "$(out '[.[].file] | unique')" = '["pkg/b.go"]' ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}

# --- Isolation: every adapter runs repository code behind the sandbox ---------------
#
# Each tool stub first behaves like hostile repository code: it dumps its
# environment, tries to write outside its run (a sibling run, the host HOME) and
# tries to read the host HOME's credentials. The adapter must still succeed, the
# attempts must fail, and the dummy credentials of the service's environment must
# never be visible to the tool.

hostile_setup() {
  DUMMY=dummy-token-5f3a9c
  export GH_TOKEN="$DUMMY" GITHUB_TOKEN="$DUMMY" ANTHROPIC_API_KEY="$DUMMY" AWS_SECRET_ACCESS_KEY="$DUMMY" \
    CLAUDE_CONFIG_DIR="$T/fakehome/.claude" SSH_AUTH_SOCK="$T/agent.sock" NPM_TOKEN="$DUMMY" CARGO_REGISTRY_TOKEN="$DUMMY"
  FAKEHOME="$T/fakehome"
  SIBLING="$T/siblings/inst2"
  mkdir -p "$FAKEHOME/.ssh" "$FAKEHOME/.config/gh" "$SIBLING"
  echo "ssh-secret" > "$FAKEHOME/.ssh/id_ed25519"
  echo "gh-secret" > "$FAKEHOME/.config/gh/hosts.yml"
  echo "sibling-secret" > "$SIBLING/secret"
  export HOME="$FAKEHOME"
  # The run's own tree is the only thing granted: no operator extensions here
  # beyond reading the fixtures the stubs copy from.
  unset HARDEN_SANDBOX_RW
  export HARDEN_SANDBOX_RO="$FX"
  HOSTILE_PRE="{ echo HOSTILE-ENV-BEGIN; env; echo HOSTILE-ENV-END; } >&2
for t in '$SIBLING/planted' '$FAKEHOME/planted' '$T/planted' '/tmp/arbitron-hostile-planted'; do
  if ( : > \"\$t\" ) 2>/dev/null; then echo \"HOSTILE-WRITE:\$t:WROTE\" >&2; else echo \"HOSTILE-WRITE:\$t:denied\" >&2; fi
done
for t in '$SIBLING/secret' '$FAKEHOME/.ssh/id_ed25519' '$FAKEHOME/.config/gh/hosts.yml'; do
  if cat \"\$t\" >/dev/null 2>&1; then echo \"HOSTILE-READ:\$t:READ\" >&2; else echo \"HOSTILE-READ:\$t:denied\" >&2; fi
done"
  export HOSTILE_PRE
}

# assert_isolated: the tool ran (its hostile preamble reported), saw no credential,
# could write nowhere outside its run and read none of the host's secrets.
assert_isolated() {
  [ "$status" -eq 0 ] || { echo "exit $status: $stderr"; return 1; }
  [[ "$stderr" == *HOSTILE-ENV-BEGIN* ]] || { echo "the tool never ran: $stderr"; return 1; }
  env_section="$(printf '%s\n' "$stderr" | sed -n '/HOSTILE-ENV-BEGIN/,/HOSTILE-ENV-END/p')"
  [[ "$env_section" != *"$DUMMY"* ]] || { echo "token leaked into the tool's environment"; return 1; }
  for name in GH_TOKEN GITHUB_TOKEN ANTHROPIC_API_KEY AWS_SECRET_ACCESS_KEY CLAUDE_CONFIG_DIR SSH_AUTH_SOCK NPM_TOKEN CARGO_REGISTRY_TOKEN; do
    [[ "$env_section" != *"$name="* ]] || { echo "$name reached the tool"; return 1; }
  done
  [[ "$env_section" != *"HOME=$FAKEHOME"* ]] || { echo "the host HOME reached the tool"; return 1; }
  # What matters is the host: nothing was planted outside the run (on Linux the
  # sandbox has its own private /tmp, where a "write" lands in memory and is gone).
  [[ "$stderr" != *":READ"* ]] || { echo "a host secret was readable: $stderr"; return 1; }
  [[ "$stderr" == *"HOSTILE-WRITE:$SIBLING/planted:denied"* ]]
  [[ "$stderr" == *"HOSTILE-WRITE:$FAKEHOME/planted:denied"* ]]
  [[ "$stderr" == *"HOSTILE-READ:$FAKEHOME/.ssh/id_ed25519:denied"* ]]
  [ ! -e "$SIBLING/planted" ] && [ ! -e "$FAKEHOME/planted" ] && [ ! -e "$T/planted" ] && [ ! -e /tmp/arbitron-hostile-planted ]
}

@test "isolation: gremlins runs sandboxed" {
  hostile_setup; go_project; gremlins_stub
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  assert_isolated
}

@test "isolation: Stryker.NET runs sandboxed" {
  hostile_setup; cs_project; stryker_net_stub
  run_adapter csharp '["tests/Lib.Tests/Lib.Tests.csproj"]' 'dotnet test'
  assert_isolated
}

@test "isolation: StrykerJS runs sandboxed" {
  hostile_setup; js_project; strykerjs_stub
  run_adapter js '["web/src/x.js","web/src/y.js"]' 'npm test'
  assert_isolated
}

@test "isolation: mutmut runs sandboxed" {
  hostile_setup; py_project; mutmut_stub
  run_adapter python '["toypkg/b.py"]' 'pytest'
  assert_isolated
}

@test "isolation: PIT (Maven) runs sandboxed" {
  hostile_setup; java_project; mvn_stub
  run_adapter java '["src/main/java/toy/B.java"]' 'mvn test'
  assert_isolated
}

@test "isolation: cargo-mutants runs sandboxed" {
  hostile_setup; rust_project; cargo_stub
  run_adapter rust '["src/lib.rs"]' 'cargo test'
  assert_isolated
}

@test "isolation: an adapter refuses to run repository code when no sandbox can be built" {
  hostile_setup; go_project; gremlins_stub
  # A PATH with neither bwrap nor sandbox-exec.
  rm -f "$SYS/sandbox-exec" "$SYS/bwrap"
  run_adapter go '["svc/pkg/b.go"]' 'go test ./...'
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"no isolation available"* ]]
  [[ "$stderr" != *HOSTILE-ENV-BEGIN* ]]
  [ ! -e "$T/gremlins.args" ]
  [ -z "$(git -C "$WORK" status --porcelain)" ]
}
