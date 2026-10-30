#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Every fixture repo is built in setup(): one source file and one test file per
# language, committed as BASE. Each test then mutates the tree and commits.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/tamper-gate.sh"
  T="$BATS_TEST_TMPDIR"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  WORK="$T/work"
  WS="$T/ws"
  mkdir -p "$WS"
  mkrepo
}

mkrepo() {
  git init -q -b main "$WORK"
  mkdir -p "$WORK/pkg" "$WORK/App" "$WORK/App.Tests" "$WORK/src/main/java" "$WORK/src/test/java" "$WORK/tests" "$WORK/src"
  printf 'package pkg\n\nfunc A() int { return 1 }\n' > "$WORK/pkg/a.go"
  printf 'package pkg\n\nfunc TestA(t *testing.T) {\n\tt.Errorf("a")\n\tt.Errorf("b")\n}\n' > "$WORK/pkg/a_test.go"
  printf 'class Program { }\n' > "$WORK/App/Program.cs"
  printf '<Project></Project>\n' > "$WORK/App.Tests/App.Tests.csproj"
  printf 'class FooTests {\n  void T() {\n    Assert.Equal(1, 1);\n    Assert.Equal(2, 2);\n  }\n}\n' > "$WORK/App.Tests/FooTests.cs"
  printf 'module.exports = function a() { return 1 }\n' > "$WORK/src/a.js"
  printf 'test("a", () => {\n  expect(1).toBe(1)\n  expect(2).toBe(2)\n})\n' > "$WORK/src/a.test.js"
  printf 'def a():\n    return 1\n' > "$WORK/app.py"
  printf 'def test_a():\n    assert 1 == 1\n    assert 2 == 2\n' > "$WORK/tests/test_app.py"
  printf 'class A { }\n' > "$WORK/src/main/java/A.java"
  printf 'class ATest {\n  void t() {\n    assertEquals(1, 1);\n    assertEquals(2, 2);\n  }\n}\n' > "$WORK/src/test/java/ATest.java"
  printf 'pub fn a() -> i32 { 1 }\n' > "$WORK/src/lib.rs"
  printf 'fn t() {\n    assert_eq!(1, 1);\n    assert_eq!(2, 2);\n}\n' > "$WORK/tests/it.rs"
  git -C "$WORK" add -A
  git -C "$WORK" commit -q -m base
  BASE="$(git -C "$WORK" rev-parse HEAD)"
}

snap() {
  jq -n --arg w "$WORK" --arg ws "$WS" '{work_dir:$w, workspace:$ws}'
}

# gate_raw <args...>: runs the script with the standard snapshot.
gate_raw() {
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "${@:3}"' _ "$(snap)" "$SCRIPT" "$@"
}

# gate: BASE..HEAD check mode.
gate() { gate_raw "$BASE" HEAD "$@"; }

j() { printf '%s' "$output" | jq -r "$1"; }

change() { git -C "$WORK" add -A; git -C "$WORK" commit -q -m "${1:-change}"; }

# add_line <file> <text>: append a line and commit.
add_line() { printf '%s\n' "$2" >> "$WORK/$1"; change; }

# drop_line <file> <literal>: delete the first line containing the literal.
drop_line() {
  awk -v p="$2" '!d && index($0,p){d=1;next}1' "$WORK/$1" > "$WORK/$1.new"
  mv "$WORK/$1.new" "$WORK/$1"
  change
}

# one_finding <kind>: gate, then expect exactly one finding of that kind.
one_finding() {
  gate
  [ "$status" -eq 0 ]
  [ "$(j '.findings|length')" = "1" ]
  [ "$(j '.findings[0].kind')" = "$1" ]
}

# detail_has <marker>: the first finding's detail names the intended marker.
detail_has() { [[ "$(j '.findings[0].detail')" == *"$1"* ]]; }

# comment_out <file> <literal> <prefix>: comment out the first line containing it.
comment_out() {
  awk -v p="$2" -v c="$3" '!d && index($0,p){d=1;print c $0;next}1' "$WORK/$1" > "$WORK/$1.new"
  mv "$WORK/$1.new" "$WORK/$1"
  change
}

no_finding() {
  gate
  [ "$status" -eq 0 ]
  [ "$(j '.findings|length')" = "0" ]
  [ "$(j '.clean')" = "true" ]
}

# --- Happy path

@test "adding a test file is clean" {
  printf 'package pkg\n\nfunc TestB(t *testing.T) {\n\tt.Errorf("x")\n}\n' > "$WORK/pkg/b_test.go"
  change
  no_finding
}

@test "adding assertions is clean" {
  add_line pkg/a_test.go '	t.Errorf("c")'
  no_finding
}

@test "source-only change is clean" {
  add_line pkg/a.go '// more'
  no_finding
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.needs_judge')" = "false" ]
  [ "$(j '.force_void')" = "false" ]
}

@test "clean output shape and findings.json" {
  add_line pkg/a.go '// more'
  gate
  [ "$(j '.base')" = "$BASE" ]
  [ "$(j '.head')" = "$(git -C "$WORK" rev-parse HEAD)" ]
  [ "$(j '.blocking_count')" = "0" ]
  [ "$(j '.warn_count')" = "0" ]
  [ "$(j '.warnings|length')" = "0" ]
  [ "$(j '.history_rewritten')" = "false" ]
  [ "$(jq -S . "$WS/tamper/findings.json")" = "$(printf '%s' "$output" | jq -S .)" ]
}

# --- Skip and focus markers

@test "marker t.Skip in go" { add_line pkg/a_test.go '	t.Skip("x")'; one_finding skip_marker_added; detail_has "t.Skip"; }
@test "marker t.Skipf in go" { add_line pkg/a_test.go '	t.Skipf("x")'; one_finding skip_marker_added; detail_has "t.Skipf"; }
@test "marker t.SkipNow in go" { add_line pkg/a_test.go '	t.SkipNow()'; one_finding skip_marker_added; detail_has "t.SkipNow"; }

@test "marker [Ignore in csharp" { add_line App.Tests/FooTests.cs '[Ignore]'; one_finding skip_marker_added; detail_has "[Ignore"; }
@test "marker Skip = in csharp" { add_line App.Tests/FooTests.cs '[Fact(Skip = "x")]'; one_finding skip_marker_added; detail_has "Skip ="; }
@test "marker Skip= in csharp" { add_line App.Tests/FooTests.cs '[Fact(Skip="x")]'; one_finding skip_marker_added; detail_has "Skip="; }
@test "marker [Explicit in csharp" { add_line App.Tests/FooTests.cs '[Explicit]'; one_finding skip_marker_added; detail_has "[Explicit"; }

@test "marker it.skip in js" { add_line src/a.test.js "it.skip('x')"; one_finding skip_marker_added; detail_has "it.skip"; }
@test "marker test.skip in js" { add_line src/a.test.js "test.skip('x')"; one_finding skip_marker_added; detail_has "test.skip"; }
@test "marker describe.skip in js" { add_line src/a.test.js "describe.skip('x')"; one_finding skip_marker_added; detail_has "describe.skip"; }
@test "marker xit in js" { add_line src/a.test.js "xit('x')"; one_finding skip_marker_added; detail_has "xit"; }
@test "marker xtest in js" { add_line src/a.test.js "xtest('x')"; one_finding skip_marker_added; detail_has "xtest"; }
@test "marker xdescribe in js" { add_line src/a.test.js "xdescribe('x')"; one_finding skip_marker_added; detail_has "xdescribe"; }
@test "marker it.only in js" { add_line src/a.test.js "it.only('x')"; one_finding skip_marker_added; detail_has "it.only"; }
@test "marker test.only in js" { add_line src/a.test.js "test.only('x')"; one_finding skip_marker_added; detail_has "test.only"; }
@test "marker describe.only in js" { add_line src/a.test.js "describe.only('x')"; one_finding skip_marker_added; detail_has "describe.only"; }
@test "marker fit in js" { add_line src/a.test.js "fit('x')"; one_finding skip_marker_added; detail_has "fit"; }
@test "marker fdescribe in js" { add_line src/a.test.js "fdescribe('x')"; one_finding skip_marker_added; detail_has "fdescribe"; }

@test "marker @pytest.mark.skip in python" { add_line tests/test_app.py '@pytest.mark.skip'; one_finding skip_marker_added; detail_has "pytest.mark.skip"; }
@test "marker @pytest.mark.skipif in python" { add_line tests/test_app.py '@pytest.mark.skipif(True, reason="x")'; one_finding skip_marker_added; detail_has "pytest.mark.skipif"; }
@test "marker @pytest.mark.xfail in python" { add_line tests/test_app.py '@pytest.mark.xfail'; one_finding skip_marker_added; detail_has "pytest.mark.xfail"; }
@test "marker @unittest.skip in python" { add_line tests/test_app.py '@unittest.skip("x")'; one_finding skip_marker_added; detail_has "unittest.skip"; }
@test "marker pytest.skip in python" { add_line tests/test_app.py '    pytest.skip("x")'; one_finding skip_marker_added; detail_has "pytest.skip"; }
@test "marker self.skipTest in python" { add_line tests/test_app.py '    self.skipTest("x")'; one_finding skip_marker_added; detail_has "self.skipTest"; }

@test "marker @Disabled in java" { add_line src/test/java/ATest.java '@Disabled'; one_finding skip_marker_added; detail_has "Disabled"; }
@test "marker @Ignore in java" { add_line src/test/java/ATest.java '@Ignore'; one_finding skip_marker_added; detail_has "Ignore"; }
@test "marker assumeTrue in java" { add_line src/test/java/ATest.java '    assumeTrue(false);'; one_finding skip_marker_added; detail_has "assumeTrue"; }
@test "marker Assumptions.assumeTrue in java" { add_line src/test/java/ATest.java '    Assumptions.assumeTrue(false);'; one_finding skip_marker_added; detail_has "Assumptions.assume"; }

@test "marker #[ignore in rust tests/it.rs" { add_line tests/it.rs '#[ignore]'; one_finding skip_marker_added; detail_has "#[ignore"; }

# --- Marker anchoring (P7), negatives

@test "process.exit in a JS file" { add_line src/a.js 'process.exit(1)'; no_finding; }
@test "benefit in a JS file" { add_line src/a.js 'benefit(1)'; no_finding; }
@test "myxit and profit in a JS file" { add_line src/a.js 'myxit(1); profit(2)'; no_finding; }
@test ".only on a non-test identifier" { add_line src/a.js 'cache.only(1); unit.only(2)'; no_finding; }

@test "marker after a boundary character still matches" {
  add_line src/a.test.js "  (xit('x'))"
  one_finding skip_marker_added
}

# --- De-duplication

@test "skipif yields one finding listing both markers" {
  add_line tests/test_app.py '@pytest.mark.skipif(True, reason="x")'
  one_finding skip_marker_added
  [[ "$(j '.findings[0].detail')" == *"pytest.mark.skipif"* ]]
  [[ "$(j '.findings[0].detail')" == *"pytest.mark.skip,"* || "$(j '.findings[0].detail')" == *",pytest.mark.skip" ]]
}

@test "Assumptions.assumeTrue yields one" {
  add_line src/test/java/ATest.java '    Assumptions.assumeTrue(false);'
  one_finding skip_marker_added
  [[ "$(j '.findings[0].detail')" == *"assumeTrue("* ]]
  [[ "$(j '.findings[0].detail')" == *"Assumptions.assume"* ]]
}

@test "[FactSkip = x] yields one" {
  add_line App.Tests/FooTests.cs '[Fact(Skip = "x")]'
  one_finding skip_marker_added
}

@test "two markers on two lines yield two" {
  add_line pkg/a_test.go '	t.Skip("x")'
  add_line pkg/a_test.go '	t.SkipNow()'
  gate
  [ "$(j '.findings|length')" = "2" ]
  [ "$(j '.findings[0].line')" != "$(j '.findings[1].line')" ]
  [ "$(j '[.findings[].id]|join(",")')" = "T1,T2" ]
}

@test "deleted test file yields only test_deleted" {
  git -C "$WORK" rm -q pkg/a_test.go
  change
  one_finding test_deleted
  [ "$(j '.findings[0] | has("also_kinds")')" = "false" ]
}

@test "kinds colliding on one file, line keep the most specific" {
  # rename + dropped assertion both land on line 0 of the new path.
  git -C "$WORK" mv pkg/a_test.go pkg/b_test.go
  drop_line pkg/b_test.go 't.Errorf("b")'
  one_finding test_renamed
  [ "$(j '.findings[0].also_kinds|join(",")')" = "assertions_dropped" ]
}

# --- Assertion drop

@test "assertions dropped in go t.Errorf" { drop_line pkg/a_test.go 't.Errorf("b")'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; [ "$(j '.findings[0].detail')" = "2→1" ]; }
@test "assertions dropped in csharp Assert.Equal" { drop_line App.Tests/FooTests.cs 'Assert.Equal(2, 2)'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; }
@test "assertions dropped in js expect" { drop_line src/a.test.js 'expect(2)'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; }
@test "assertions dropped in python assert" { drop_line tests/test_app.py 'assert 2'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; }
@test "assertions dropped in java assertEquals" { drop_line src/test/java/ATest.java 'assertEquals(2'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; }
@test "assertions dropped in rust assert_eq! in tests/it.rs" { drop_line tests/it.rs 'assert_eq!(2'; one_finding assertions_dropped; [[ "$(j '.findings[0].detail')" =~ ^[0-9]+→[0-9]+$ ]]; }

# --- Deletion and rename

@test "deleting the test file in go" { git -C "$WORK" rm -q pkg/a_test.go; change; one_finding test_deleted; }
@test "deleting the test file in csharp" { git -C "$WORK" rm -q App.Tests/FooTests.cs; change; one_finding test_deleted; }
@test "deleting the test file in js" { git -C "$WORK" rm -q src/a.test.js; change; one_finding test_deleted; }
@test "deleting the test file in python" { git -C "$WORK" rm -q tests/test_app.py; change; one_finding test_deleted; }
@test "deleting the test file in java" { git -C "$WORK" rm -q src/test/java/ATest.java; change; one_finding test_deleted; }
@test "deleting the test file in rust" { git -C "$WORK" rm -q tests/it.rs; change; one_finding test_deleted; }

@test "R100 rename gives test_renamed" {
  git -C "$WORK" mv pkg/a_test.go pkg/b_test.go
  change
  one_finding test_renamed
  [ "$(j '.findings[0].file')" = "pkg/b_test.go" ]
}

@test "rename with dropped asserts gives both kinds" {
  git -C "$WORK" mv pkg/a_test.go pkg/b_test.go
  drop_line pkg/b_test.go 't.Errorf("b")'
  gate
  [ "$(j '[.findings[] | .kind, (.also_kinds // [])[]] | sort | join(",")')" = "assertions_dropped,test_renamed" ]
}

# --- History rewritten

history_rewritten_asserts() {
  [ "$status" -eq 0 ]
  [ "$(j '.history_rewritten')" = "true" ]
  [ "$(j '.force_void')" = "true" ]
  [ "$(j '.needs_judge')" = "false" ]
  [ "$(j '.clean')" = "false" ]
  [ "$(j '.findings|length')" = "1" ]
  [ "$(j '.findings[0].kind')" = "history_rewritten" ]
  [ "$(j '.findings[0].judgeable')" = "false" ]
}

@test "history rewritten: amend of base" {
  add_line pkg/a.go '// one'
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" commit -q --amend -m amended
  gate
  history_rewritten_asserts
}

@test "history rewritten: reset below base then recommit" {
  add_line pkg/a.go '// one'
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" reset -q --hard HEAD~1
  add_line pkg/a.go '// two'
  gate
  history_rewritten_asserts
}

@test "history rewritten: rebase onto another commit" {
  git -C "$WORK" checkout -q -b side
  echo s > "$WORK/side.txt"
  change side
  git -C "$WORK" checkout -q main
  add_line pkg/a.go '// one'
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" rebase -q --onto side main~1 main
  gate
  history_rewritten_asserts
}

@test "history rewritten still rotates must-fix and removes the verdict" {
  mkdir -p "$WS/tamper"
  echo old > "$WS/tamper/verdict.json"
  add_line pkg/a.go '// one'
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" commit -q --amend -m amended
  gate
  [ ! -e "$WS/tamper/verdict.json" ]
  [ -f "$WS/tamper/findings.json" ]
}

# --- Edge cases

@test "multiple commits in range" {
  add_line pkg/a_test.go '	t.Skip("x")'
  drop_line tests/it.rs 'assert_eq!(2'
  gate
  [ "$(j '.findings|length')" = "2" ]
  [ "$(j '[.findings[].kind]|sort|join(",")')" = "assertions_dropped,skip_marker_added" ]
  [[ "$(j '.summary')" == *"2 commits"* ]]
}

@test "filename with spaces" {
  printf 'package pkg\n\nfunc TestS(t *testing.T) {\n\tt.Errorf("x")\n}\n' > "$WORK/pkg/my file_test.go"
  change
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  add_line "pkg/my file_test.go" '	t.Skip("x")'
  one_finding skip_marker_added
  [ "$(j '.findings[0].file')" = "pkg/my file_test.go" ]
  [ "$(j '.findings[0].language')" = "go" ]
}

@test "binary files ignored" {
  head -c 512 /dev/urandom > "$WORK/data.bin"
  printf '\211PNG\r\n\032\n\000\000\000' > "$WORK/img.png"
  change
  no_finding
}

@test "merge commit in range" {
  git -C "$WORK" checkout -q -b side
  printf '%s\n' '	t.Skip("x")' >> "$WORK/pkg/a_test.go"
  change side
  git -C "$WORK" checkout -q main
  add_line pkg/a.go '// main'
  git -C "$WORK" merge -q --no-ff side -m merge
  one_finding skip_marker_added
}

@test "stale verdict.json removed and must-fix.md rotated to .prev" {
  mkdir -p "$WS/tamper"
  echo stale > "$WS/tamper/verdict.json"
  echo fixme > "$WS/tamper/must-fix.md"
  add_line pkg/a.go '// x'
  gate
  [ "$status" -eq 0 ]
  [ ! -e "$WS/tamper/verdict.json" ]
  [ ! -e "$WS/tamper/must-fix.md" ]
  [ "$(cat "$WS/tamper/must-fix.prev.md")" = "fixme" ]
  [ -f "$WS/tamper/findings.json" ]
}

@test "rotation overwrites an older must-fix.prev.md" {
  mkdir -p "$WS/tamper"
  echo older > "$WS/tamper/must-fix.prev.md"
  echo newer > "$WS/tamper/must-fix.md"
  gate_raw "$BASE" HEAD
  [ "$(cat "$WS/tamper/must-fix.prev.md")" = "newer" ]
}

# --- Errors

@test "error: unset work_dir" {
  run --separate-stderr bash -c 'printf "%s" "{\"workspace\":\"$1\"}" | sh "$2" "$3" HEAD' _ "$WS" "$SCRIPT" "$BASE"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "error: work_dir not a repo" {
  mkdir -p "$T/plain"
  run --separate-stderr bash -c 'printf "%s" "$1" | sh "$2" "$3" HEAD' _ "$(jq -n --arg w "$T/plain" --arg ws "$WS" '{work_dir:$w,workspace:$ws}')" "$SCRIPT" "$BASE"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "error: base not a commit" {
  gate_raw deadbeefdeadbeefdeadbeefdeadbeefdeadbeef HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "error: head not a commit" {
  gate_raw "$BASE" no-such-ref
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "error: a tree-ish that is not a commit is rejected" {
  gate_raw "$BASE" "$(git -C "$WORK" rev-parse 'HEAD^{tree}')"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "error: option-like base is rejected" {
  gate_raw --bogus HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

# --- Boundary

@test "base == head is clean" {
  gate_raw "$BASE" "$BASE"
  [ "$status" -eq 0 ]
  [ "$(j '.clean')" = "true" ]
  [ "$(j '.findings|length')" = "0" ]
}

@test "equal assertion counts: no finding" {
  drop_line pkg/a_test.go 't.Errorf("b")'
  add_line pkg/a_test.go '	t.Errorf("other")'
  no_finding
}

@test "one fewer assertion: one finding" {
  drop_line pkg/a_test.go 't.Errorf("b")'
  one_finding assertions_dropped
}

# --- Classify

@test "classify roles and languages for a mixed set" {
  add_line pkg/a.go '// s'
  add_line pkg/a_test.go '// t'
  add_line App.Tests/FooTests.cs '// t'
  printf 'x\n' > "$WORK/App.Tests/Helper.cs"
  mkdir -p "$WORK/.github/workflows"
  echo 'on: push' > "$WORK/.github/workflows/ci.yml"
  echo 'module.exports = {}' > "$WORK/jest.config.js"
  echo hi > "$WORK/README.md"
  echo '# t' >> "$WORK/tests/test_app.py"
  change mixed
  gate_raw --classify "$BASE" HEAD
  [ "$status" -eq 0 ]
  role() { j ".files[] | select(.path==\"$1\") | .role"; }
  lang() { j ".files[] | select(.path==\"$1\") | .language"; }
  [ "$(role pkg/a.go)" = "source" ]
  [ "$(lang pkg/a.go)" = "go" ]
  [ "$(role pkg/a_test.go)" = "test" ]
  [ "$(role App.Tests/FooTests.cs)" = "test" ]
  [ "$(lang App.Tests/FooTests.cs)" = "csharp" ]
  [ "$(role App.Tests/Helper.cs)" = "test" ]
  [ "$(role .github/workflows/ci.yml)" = "ci" ]
  [ "$(role jest.config.js)" = "config" ]
  [ "$(role README.md)" = "other" ]
  [ "$(role tests/test_app.py)" = "test" ]
  [ "$(lang tests/test_app.py)" = "python" ]
  [ "$(j '.files[] | select(.path=="pkg/a.go") | .status')" = "M" ]
  [ "$(j '.base')" = "$BASE" ]
  [ "$(j '.source_files_by_language.go|join(",")')" = "pkg/a.go" ]
  [ "$(j '.test_files|sort|join(",")')" = "App.Tests/FooTests.cs,App.Tests/Helper.cs,pkg/a_test.go,tests/test_app.py" ]
}

@test "classify performs no checks and writes no findings" {
  add_line pkg/a_test.go '	t.Skip("x")'
  gate_raw --classify "$BASE" HEAD
  [ "$status" -eq 0 ]
  [ "$(j 'has("findings")')" = "false" ]
  [ ! -e "$WS/tamper/findings.json" ]
}

@test "classify test-file set equals check mode's" {
  add_line pkg/a.go '// s'
  add_line pkg/a_test.go '	t.Skip("x")'
  add_line src/a.test.js "// t"
  git -C "$WORK" rm -q tests/it.rs
  change
  gate_raw --classify "$BASE" HEAD
  classify_set="$(j '.test_files|sort|join(",")')"
  gate
  [ "$classify_set" = "$(j '.test_files_changed|sort|join(",")')" ]
  [ -n "$classify_set" ]
}

@test "classify errors exit 2 on a bad commit" {
  gate_raw --classify nope HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "classify in a rewritten history exits 0 with history_rewritten and no files" {
  add_line pkg/a.go '// one'
  BASE="$(git -C "$WORK" rev-parse HEAD)"
  git -C "$WORK" commit -q --amend -m amended
  gate_raw --classify "$BASE" HEAD
  [ "$status" -eq 0 ]
  [ "$(j '.history_rewritten')" = "true" ]
  [ "$(j '.files|length')" = "0" ]
  [ "$(j '.test_files|length')" = "0" ]
}

# --- Commented-out and masked assertions

@test "commented-out assertion in go counts as dropped" { comment_out pkg/a_test.go 't.Errorf("b")' '// '; one_finding assertions_dropped; }
@test "commented-out assertion in csharp counts as dropped" { comment_out App.Tests/FooTests.cs 'Assert.Equal(2' '// '; one_finding assertions_dropped; }
@test "commented-out assertion in js counts as dropped" { comment_out src/a.test.js 'expect(2)' '// '; one_finding assertions_dropped; }
@test "commented-out assertion in python counts as dropped" { comment_out tests/test_app.py 'assert 2' '# '; one_finding assertions_dropped; }
@test "commented-out assertion in java counts as dropped" { comment_out src/test/java/ATest.java 'assertEquals(2' '// '; one_finding assertions_dropped; }
@test "commented-out assertion in rust counts as dropped" { comment_out tests/it.rs 'assert_eq!(2' '// '; one_finding assertions_dropped; }

@test "padding a comment with a token does not mask a drop" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js '// expect( noop'
  one_finding assertions_dropped
}

@test "a token inside a string literal does not mask a drop" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js 'const s = "expect( x"; const t = '"'"'expect( y'"'"';'
  one_finding assertions_dropped
}

@test "trailing comment after a real assertion still counts it" {
  add_line pkg/a_test.go '	t.Errorf("c") // note'
  drop_line pkg/a_test.go 't.Errorf("b")'
  no_finding
}

# --- Extended markers

@test "marker pytestmark = pytest.mark.skip without an at-sign" {
  add_line tests/test_app.py 'pytestmark = pytest.mark.skip'
  one_finding skip_marker_added
  detail_has "pytest.mark.skip"
}

@test "marker go build ignore in a test file" {
  add_line pkg/a_test.go '//go:build ignore'
  one_finding skip_marker_added
  detail_has "//go:build ignore"
}

@test "marker plus-build ignore in a test file" {
  add_line pkg/a_test.go '// +build ignore'
  one_finding skip_marker_added
}

@test "go build ignore in a non-test go file is not a finding" {
  add_line pkg/a.go '//go:build ignore'
  no_finding
}

@test "marker testify T().Skip" {
  add_line pkg/a_test.go '	s.T().Skip("x")'
  one_finding skip_marker_added
  detail_has "T().Skip"
}

@test "marker assumeFalse in java" {
  add_line src/test/java/ATest.java '    assumeFalse(true);'
  one_finding skip_marker_added
  detail_has "assumeFalse("
}

@test "marker Assume.assumeTrue keeps the dot boundary" {
  add_line src/test/java/ATest.java '    Assume.assumeTrue(false);'
  one_finding skip_marker_added
  detail_has "assumeTrue("
}

@test "marker this.skip in js" {
  add_line src/a.test.js '  this.skip();'
  one_finding skip_marker_added
  detail_has "this.skip("
}

@test "model.fit( and fitAddon.fit() are not findings" {
  add_line src/a.js 'model.fit(x); fitAddon.fit(); obj.xit(1); a.fdescribe(2)'
  no_finding
}

@test "bare xit( after a space or paren still matches" {
  add_line src/a.test.js 'x = xit(1)'
  one_finding skip_marker_added
}

# --- Extended classification

@test "classify mts cts test and helper patterns" {
  mkdir -p "$WORK/test" "$WORK/internal"
  echo x > "$WORK/src/b.mts"
  echo x > "$WORK/src/b.test.mts"
  echo x > "$WORK/src/c.cts"
  echo x > "$WORK/test/helper.js"
  echo x > "$WORK/internal/testutil.go"
  echo x > "$WORK/internal/TestHelpers.java"
  change
  gate_raw --classify "$BASE" HEAD
  role() { j ".files[] | select(.path==\"$1\") | .role"; }
  [ "$(role src/b.mts)" = "source" ]
  [ "$(j '.files[] | select(.path=="src/b.mts") | .language')" = "js" ]
  [ "$(role src/b.test.mts)" = "test" ]
  [ "$(role src/c.cts)" = "source" ]
  [ "$(role test/helper.js)" = "test" ]
  [ "$(role internal/testutil.go)" = "test" ]
  [ "$(role internal/TestHelpers.java)" = "test" ]
}

@test "classify root-level Tests.csproj does not make every cs file a test" {
  echo '<Project></Project>' > "$WORK/Root.Tests.csproj"
  echo x > "$WORK/Program2.cs"
  echo x > "$WORK/RootTests.cs"
  change
  gate_raw --classify "$BASE" HEAD
  [ "$(j '.files[] | select(.path=="Program2.cs") | .role')" = "source" ]
  [ "$(j '.files[] | select(.path=="RootTests.cs") | .role')" = "test" ]
}

# ===========================================================================
# Task 8: config, CI, baseline, --tests-only, Rust hunks, warnings
# ===========================================================================

# put <path> <content>: write a file (creating directories), no commit.
put() { mkdir -p "$WORK/$(dirname "$1")"; printf '%s\n' "$2" > "$WORK/$1"; }

# commit_base: commit the prepared tree and make it the new BASE.
commit_base() {
  git -C "$WORK" add -A
  git -C "$WORK" commit -q --allow-empty -m prep
  BASE="$(git -C "$WORK" rev-parse HEAD)"
}

# cfg_case <kind> <file> <before> <after>: before is the base, after the head.
cfg_case() {
  put "$2" "$3"; commit_base
  put "$2" "$4"; change
  one_finding "$1"
  [ "$(j '.findings[0].file')" = "$2" ]
}

# cfg_none <file> <before> <after>: a change that must not be a finding.
cfg_none() {
  put "$1" "$2"; commit_base
  put "$1" "$3"; change
  no_finding
}

# --- Config: test_config_weakened

@test "config jest.config.js testPathIgnorePatterns" {
  cfg_case test_config_weakened jest.config.js 'module.exports = {}' "module.exports = { testPathIgnorePatterns: ['/a/'] }"
}
@test "config vitest.config.ts exclude" {
  cfg_case test_config_weakened vitest.config.ts 'export default {}' "export default { test: { exclude: ['a'] } }"
}
@test "config .mocharc.yml ignore" {
  cfg_case test_config_weakened .mocharc.yml 'spec: a' $'spec: a\nignore: b'
}
@test "config .mocharc.json spec" {
  cfg_case test_config_weakened .mocharc.json '{"spec":"test/**"}' '{"spec":"test/one.js"}'
}
@test "config karma.conf.js exclude" {
  cfg_case test_config_weakened karma.conf.js 'module.exports = {}' "module.exports = { exclude: ['a.js'] }"
}
@test "config vite.config.ts test.exclude section" {
  cfg_case test_config_weakened vite.config.ts $'export default {\n  test: {\n    globals: true,\n  },\n}' $'export default {\n  test: {\n    globals: true,\n    include: ["a"],\n  },\n}'
}
@test "config package.json jest.testPathIgnorePatterns" {
  cfg_case test_config_weakened package.json $'{\n  "jest": {\n  }\n}' $'{\n  "jest": {\n    "testPathIgnorePatterns": ["/a/"]\n  }\n}'
}
@test "config pytest.ini --ignore" {
  cfg_case test_config_weakened pytest.ini $'[pytest]\naddopts = -q' $'[pytest]\naddopts = -q --ignore=tests/slow'
}
@test "config pyproject pytest section" {
  cfg_case test_config_weakened pyproject.toml $'[tool.pytest.ini_options]\naddopts = "-q"' $'[tool.pytest.ini_options]\naddopts = "-q -x"'
}
@test "config pyproject coverage omit" {
  cfg_case test_config_weakened pyproject.toml $'[tool.coverage.run]\nbranch = true' $'[tool.coverage.run]\nbranch = true\nomit = ["a.py"]'
}
@test "config setup.cfg tool:pytest" {
  cfg_case test_config_weakened setup.cfg $'[tool:pytest]\naddopts = -q' $'[tool:pytest]\naddopts = -q -x'
}
@test "config setup.cfg coverage:run omit" {
  cfg_case test_config_weakened setup.cfg $'[coverage:run]\nbranch = True' $'[coverage:run]\nbranch = True\nomit = a.py'
}
@test "config tox.ini" {
  cfg_case test_config_weakened tox.ini $'[tox]\nenvlist = py3' $'[tox]\nenvlist = py3\n'
}
@test "config .coveragerc omit" {
  cfg_case test_config_weakened .coveragerc $'[run]\nbranch = True' $'[run]\nbranch = True\nomit = a.py'
}
@test "config conftest collect_ignore" {
  cfg_case test_config_weakened tests/conftest.py 'import pytest' $'import pytest\ncollect_ignore = ["test_app.py"]'
}
@test "config .runsettings" {
  cfg_case test_config_weakened a.runsettings '<RunSettings/>' $'<RunSettings>\n</RunSettings>'
}
@test "config nextest.toml" {
  cfg_case test_config_weakened .config/nextest.toml $'[profile.default]\nretries = 0' $'[profile.default]\nretries = 2'
}
@test "config pom skipTests" {
  cfg_case test_config_weakened pom.xml $'<project>\n  <properties>\n  </properties>\n</project>' $'<project>\n  <properties>\n    <skipTests>true</skipTests>\n  </properties>\n</project>'
}
@test "config gradle test exclude" {
  cfg_case test_config_weakened build.gradle $'test {\n  useJUnitPlatform()\n}' $'test {\n  useJUnitPlatform()\n  exclude "**/Slow*"\n}'
}
@test "config Cargo test = false" {
  cfg_case test_config_weakened Cargo.toml $'[lib]\nname = "a"' $'[lib]\nname = "a"\ntest = false'
}
@test "config test csproj Compile Remove" {
  cfg_case test_config_weakened App.Tests/App.Tests.csproj '<Project></Project>' $'<Project>\n  <ItemGroup><Compile Remove="FooTests.cs" /></ItemGroup>\n</Project>'
}

# --- Config: mutation_config_weakened

@test "config stryker-config.json thresholds" {
  cfg_case mutation_config_weakened stryker-config.json '{"thresholds":{"break":80}}' '{"thresholds":{"break":10}}'
}
@test "config stryker.conf.mjs mutate" {
  cfg_case mutation_config_weakened stryker.conf.mjs 'export default { mutate: ["src/**"] }' 'export default { mutate: ["src/a.js"] }'
}
@test "config stryker.config.json thresholds.break" {
  cfg_case mutation_config_weakened stryker.config.json '{"thresholds":{"break":80}}' '{"thresholds":{"break":0}}'
}
@test "config .gremlins.yaml" {
  cfg_case mutation_config_weakened .gremlins.yaml 'threshold: 80' 'threshold: 10'
}
@test "config .gremlins.yml" {
  cfg_case mutation_config_weakened .gremlins.yml 'threshold: 80' 'threshold: 10'
}
@test "config .cargo/mutants.toml" {
  cfg_case mutation_config_weakened .cargo/mutants.toml 'exclude_globs = []' 'exclude_globs = ["a"]'
}
@test "config mutants.toml" {
  cfg_case mutation_config_weakened mutants.toml 'exclude_globs = []' 'exclude_globs = ["a"]'
}
@test "config pyproject tool.mutmut" {
  cfg_case mutation_config_weakened pyproject.toml $'[tool.mutmut]\npaths_to_mutate = "src/"' $'[tool.mutmut]\npaths_to_mutate = "src/a.py"'
}
@test "config setup.cfg mutmut" {
  cfg_case mutation_config_weakened setup.cfg $'[mutmut]\npaths_to_mutate = src/' $'[mutmut]\npaths_to_mutate = src/a.py'
}
@test "config gradle pitest block" {
  cfg_case mutation_config_weakened build.gradle $'pitest {\n  threads = 4\n}' $'pitest {\n  threads = 4\n  mutators = ["DEFAULTS"]\n}'
}

# --- Config: negatives

@test "config negative: package.json dependency bump" {
  cfg_none package.json $'{\n  "dependencies": {\n    "left-pad": "1.0.0"\n  }\n}' $'{\n  "dependencies": {\n    "left-pad": "1.1.0"\n  }\n}'
}
@test "config negative: pyproject project.version bump" {
  cfg_none pyproject.toml $'[project]\nversion = "1.0.0"' $'[project]\nversion = "1.0.1"'
}
@test "config negative: gradle dependencies edit beside pitest block" {
  cfg_none build.gradle $'dependencies {\n  testImplementation "a:b:1"\n}\npitest {\n  threads = 4\n}' $'dependencies {\n  testImplementation "a:b:2"\n}\npitest {\n  threads = 4\n}'
}
@test "config negative: edit after a pytest section header in another section" {
  cfg_none pyproject.toml $'[tool.pytest.ini_options]\naddopts = "-q"\n\n[tool.black]\nline-length = 88' $'[tool.pytest.ini_options]\naddopts = "-q"\n\n[tool.black]\nline-length = 100'
}
@test "config negative: setup.cfg metadata edit" {
  cfg_none setup.cfg $'[metadata]\nname = a\n\n[tool:pytest]\naddopts = -q' $'[metadata]\nname = b\n\n[tool:pytest]\naddopts = -q'
}
@test "config negative: vite.config edit outside the test block" {
  cfg_none vite.config.ts $'export default {\n  server: { port: 3000 },\n  test: {\n    globals: true,\n  },\n}' $'export default {\n  server: { port: 4000 },\n  test: {\n    globals: true,\n  },\n}'
}
@test "config negative: pom edit in a non-pitest plugin" {
  cfg_none pom.xml $'<project><build><plugins>\n<plugin>\n  <artifactId>maven-compiler-plugin</artifactId>\n  <version>3.1</version>\n</plugin>\n<plugin>\n  <artifactId>pitest-maven</artifactId>\n  <version>1.0</version>\n</plugin>\n</plugins></build></project>' $'<project><build><plugins>\n<plugin>\n  <artifactId>maven-compiler-plugin</artifactId>\n  <version>3.2</version>\n</plugin>\n<plugin>\n  <artifactId>pitest-maven</artifactId>\n  <version>1.0</version>\n</plugin>\n</plugins></build></project>'
}
@test "config negative: conftest unrelated edit" {
  cfg_none tests/conftest.py 'import pytest' $'import pytest\nimport os'
}
@test "config negative: csproj PackageReference edit" {
  cfg_none App.Tests/App.Tests.csproj $'<Project>\n  <ItemGroup><PackageReference Include="xunit" Version="1" /></ItemGroup>\n</Project>' $'<Project>\n  <ItemGroup><PackageReference Include="xunit" Version="2" /></ItemGroup>\n</Project>'
}
@test "config negative: files that only resemble config names" {
  put docs/mocharc-notes.md a; put gremlins.yml a; put mutants.toml.bak a; put stryker.txt a; put karma-notes.md a
  commit_base
  put docs/mocharc-notes.md b; put gremlins.yml b; put mutants.toml.bak b; put stryker.txt b; put karma-notes.md b
  change
  no_finding
}
@test "config negative: pyproject without mutmut or pytest sections" {
  cfg_none pyproject.toml $'[tool.mutmut]\npaths = "a"\n\n[tool.ruff]\nline-length = 88' $'[tool.mutmut]\npaths = "a"\n\n[tool.ruff]\nline-length = 90'
}

# --- Config: scoping and de-duplication

@test "deleted lines are scoped on the base side" {
  put pyproject.toml $'[tool.pytest.ini_options]\naddopts = "-q"\n[tool.black]\nline-length = 88'
  commit_base
  put pyproject.toml $'[tool.black]\nline-length = 88'
  change
  one_finding test_config_weakened
}
@test "config matching keyword and section rules in one kind yields one" {
  cfg_case test_config_weakened vite.config.ts $'export default {\n  test: {\n    globals: true,\n  },\n}' $'export default {\n  test: {\n    globals: true,\n    exclude: ["a"],\n  },\n}'
  [ "$(j '.findings[0].also_kinds // []|length')" = "0" ]
}
@test "pom pitest mutationThreshold yields one mutation_config_weakened with also_kinds test_config_weakened" {
  put pom.xml $'<project><build><plugins>\n<plugin>\n  <groupId>org.pitest</groupId>\n  <artifactId>pitest-maven</artifactId>\n  <configuration>\n    <mutationThreshold>80</mutationThreshold>\n  </configuration>\n</plugin>\n</plugins></build></project>'
  commit_base
  put pom.xml $'<project><build><plugins>\n<plugin>\n  <groupId>org.pitest</groupId>\n  <artifactId>pitest-maven</artifactId>\n  <configuration>\n    <mutationThreshold>10</mutationThreshold>\n  </configuration>\n</plugin>\n</plugins></build></project>'
  change
  one_finding mutation_config_weakened
  [ "$(j '.findings[0].also_kinds|tojson')" = '["test_config_weakened"]' ]
}
@test "gradle pitest block exclude yields one mutation_config_weakened with also_kinds test_config_weakened" {
  put build.gradle $'pitest {\n  threads = 4\n}'
  commit_base
  put build.gradle $'pitest {\n  threads = 4\n  excludedClasses = ["a.*"]\n}'
  change
  one_finding mutation_config_weakened
  [ "$(j '.findings[0].also_kinds|tojson')" = '["test_config_weakened"]' ]
}
@test "gradle pitest block with a brace in a string stays one block" {
  put build.gradle $'pitest {\n  jvmArgs = ["-Dx={"]\n  threads = 4\n}\nother {\n  a = 1\n}'
  commit_base
  put build.gradle $'pitest {\n  jvmArgs = ["-Dx={"]\n  threads = 4\n}\nother {\n  a = 2\n}'
  change
  no_finding
}

# --- CI

@test "ci .github/workflows" { put .github/workflows/ci.yml 'on: push'; change; one_finding ci_config_changed; }
@test "ci .gitlab-ci.yml" { put .gitlab-ci.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci azure-pipelines" { put azure-pipelines-main.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci .circleci" { put .circleci/config.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci Jenkinsfile" { put Jenkinsfile 'pipeline'; change; one_finding ci_config_changed; }
@test "ci .buildkite" { put .buildkite/pipeline.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci bitbucket-pipelines.yml" { put bitbucket-pipelines.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci .travis.yml" { put .travis.yml 'a: 1'; change; one_finding ci_config_changed; }
@test "ci lookalike path is not CI" { put docs/ci.yml 'a: 1'; put src/Jenkinsfile.md 'a'; change; no_finding; }

# --- Baseline

@test "baseline x.baseline.json edit" {
  put .arbitron/pipeline/x.baseline.json '{"a":1}'; commit_base
  put .arbitron/pipeline/x.baseline.json '{"a":2}'; change
  one_finding baseline_edited
}
@test "baseline docs/reference/test-baseline.json edit" {
  put docs/reference/test-baseline.json '{"a":1}'; commit_base
  put docs/reference/test-baseline.json '{"a":2}'; change
  one_finding baseline_edited
}
@test "baseline x.harden.json edit" {
  put .arbitron/pipeline/x.harden.json '{"a":1}'; commit_base
  put .arbitron/pipeline/x.harden.json '{"a":2}'; change
  one_finding baseline_edited
}
@test "baseline x.survivors.json edit" {
  put .arbitron/pipeline/x.survivors.json '{"a":1}'; commit_base
  put .arbitron/pipeline/x.survivors.json '{"a":2}'; change
  one_finding baseline_edited
}
@test "baseline *.baseline.json outside .arbitron/pipeline is flagged" {
  put docs/y.baseline.json '{"a":1}'; commit_base
  put docs/y.baseline.json '{"a":2}'; change
  one_finding baseline_edited
}
@test "baseline deletion is flagged" {
  put .arbitron/pipeline/x.baseline.json '{"a":1}'; commit_base
  git -C "$WORK" rm -q .arbitron/pipeline/x.baseline.json; change
  one_finding baseline_edited
}
@test "baseline committed before pass-mark, untouched in the pass: no finding" {
  put .arbitron/pipeline/x.baseline.json '{"a":1}'
  put docs/reference/test-baseline.json '{"a":1}'
  commit_base
  add_line pkg/a.go '// more'
  no_finding
}

# --- --tests-only

ts_only() { gate --tests-only; }

expect_source_changed() {
  ts_only
  [ "$status" -eq 0 ]
  [ "$(j '.source_changed')" = "true" ]
  [ "$(j '.force_void')" = "true" ]
  [ "$(j '.needs_judge')" = "false" ]
  [ "$(j '[.findings[]|select(.kind=="source_changed")]|length')" = "1" ]
  [ "$(j '.findings[]|select(.kind=="source_changed")|.judgeable')" = "false" ]
}

@test "tests-only source change in go" { add_line pkg/a.go '// more'; expect_source_changed; }
@test "tests-only source change in csharp" { add_line App/Program.cs '// more'; expect_source_changed; }
@test "tests-only source change in js" { add_line src/a.js '// more'; expect_source_changed; }
@test "tests-only source change in python" { add_line app.py '# more'; expect_source_changed; }
@test "tests-only source change in java" { add_line src/main/java/A.java '// more'; expect_source_changed; }
@test "tests-only source change in rust" { add_line src/lib.rs '// more'; expect_source_changed; }

@test "tests-only source deletion and addition are source_changed" {
  git -C "$WORK" rm -q pkg/a.go
  put pkg/b.go 'package pkg'
  change
  ts_only
  [ "$(j '.source_changed')" = "true" ]
  [ "$(j '[.findings[]|select(.kind=="source_changed")]|length')" = "2" ]
}

@test "tests-only test-only change gives no source_changed" {
  add_line pkg/a_test.go '	t.Errorf("c")'
  add_line src/a.test.js 'expect(3).toBe(3)'
  ts_only
  [ "$status" -eq 0 ]
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.force_void')" = "false" ]
  [ "$(j '.clean')" = "true" ]
}

@test "source change without --tests-only is not source_changed" {
  add_line pkg/a.go '// more'
  gate
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.force_void')" = "false" ]
}

@test "tests-only source change plus a judgeable finding is still not judged" {
  add_line pkg/a.go '// more'
  add_line pkg/a_test.go '	t.Skip("x")'
  ts_only
  [ "$(j '.needs_judge')" = "false" ]
  [ "$(j '.force_void')" = "true" ]
  [ "$(j '.findings|length')" = "2" ]
}

# --- Rust cfg(test) hunks

RS_LIB=$'pub fn a() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n\n    #[test]\n    fn t() {\n        assert_eq!(a(), 1);\n        assert_eq!(a() + 1, 2);\n    }\n}'

rs_base() { put src/lib.rs "$RS_LIB"; commit_base; }

@test "marker #[ignore] inside the cfg(test) mod in src/lib.rs" {
  rs_base
  put src/lib.rs "${RS_LIB/"    #[test]"/"    #[test]
    #[ignore]"}"
  change
  one_finding skip_marker_added
  detail_has "#[ignore"
  [ "$(j '.findings[0].file')" = "src/lib.rs" ]
}

@test "assert_eq! dropped inside the cfg(test) mod gives assertions_dropped" {
  rs_base
  put src/lib.rs "${RS_LIB/"        assert_eq!(a() + 1, 2);
"/}"
  change
  one_finding assertions_dropped
  [ "$(j '.findings[0].detail')" = "2→1" ]
  [ "$(j '.findings[0].file')" = "src/lib.rs" ]
}

@test "assertions outside the cfg(test) mod are not counted" {
  put src/lib.rs "${RS_LIB/"pub fn a() -> i32 { 1 }"/"pub fn a() -> i32 { assert!(true); assert!(true); 1 }"}"
  commit_base
  put src/lib.rs "$RS_LIB"
  change
  no_finding
}

@test "edit inside cfg(test) mod is not source" {
  rs_base
  put src/lib.rs "${RS_LIB/"use super::*;"/"use super::a;"}"
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.clean')" = "true" ]
}

@test "edit outside the mod is source" {
  rs_base
  put src/lib.rs "${RS_LIB/"{ 1 }"/"{ 2 }"}"
  change
  expect_source_changed
}

@test "commit touching both is source" {
  rs_base
  put src/lib.rs "${RS_LIB/"{ 1 }"/"{ 2 }"}"
  put src/lib.rs "$(cat "$WORK/src/lib.rs")"$'\n// tail'
  sed_replace_tests_line
  change
  expect_source_changed
}

sed_replace_tests_line() {
  awk '{ if (index($0, "use super::*;")) print "    use super::a;"; else print }' "$WORK/src/lib.rs" > "$WORK/src/lib.rs.new"
  mv "$WORK/src/lib.rs.new" "$WORK/src/lib.rs"
}

@test "tests/it.rs edit is not source" {
  add_line tests/it.rs '// more'
  ts_only
  [ "$(j '.source_changed')" = "false" ]
}

@test "brace in a string literal does not close the block" {
  put src/lib.rs $'pub fn a<\'x>(s: &\'x str) -> &\'x str { s }\n\n#[cfg(test)]\nmod tests {\n    use super::*;\n    // a stray } in a comment\n    const S: &str = "}";\n    const C: char = \'}\';\n    const R: &str = r#"}"#;\n    /* } */\n\n    #[test]\n    fn t() {\n        assert_eq!(1, 1);\n    }\n}'
  commit_base
  awk '{ if (index($0, "assert_eq!(1, 1);")) print "        assert_eq!(1, 1);\n        assert_eq!(2, 2);"; else print }' "$WORK/src/lib.rs" > "$WORK/src/lib.rs.new"
  mv "$WORK/src/lib.rs.new" "$WORK/src/lib.rs"
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.clean')" = "true" ]
}

@test "deleted lines inside the block count as a test hunk" {
  rs_base
  put src/lib.rs 'pub fn a() -> i32 { 1 }'
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '[.findings[]|select(.kind=="source_changed")]|length')" = "0" ]
}

@test "deleting a rust source file is source" {
  rs_base
  git -C "$WORK" rm -q src/lib.rs
  change
  expect_source_changed
}

@test "classify gives rust role by hunk" {
  rs_base
  put src/other.rs "$RS_LIB"
  commit_base
  put src/lib.rs "${RS_LIB/"use super::*;"/"use super::a;"}"
  put src/other.rs "${RS_LIB/"{ 1 }"/"{ 2 }"}"
  change
  gate_raw --classify "$BASE" HEAD
  [ "$(j '.files[]|select(.path=="src/lib.rs")|.role')" = "test" ]
  [ "$(j '.files[]|select(.path=="src/other.rs")|.role')" = "source" ]
  [ "$(j '.test_files|index("src/lib.rs")!=null')" = "true" ]
  [ "$(j '.source_files_by_language.rust|index("src/other.rs")!=null')" = "true" ]
}

# --- Warning hardcoded_expected_value

@test "10-char literal shared with a test warns; clean stays true; no judge" {
  printf '%s\n' 'var K = "abcdefghij"' >> "$WORK/pkg/a.go"
  printf '%s\n' '	_ = "abcdefghij"' >> "$WORK/pkg/a_test.go"
  change
  gate
  [ "$status" -eq 0 ]
  [ "$(j '.clean')" = "true" ]
  [ "$(j '.needs_judge')" = "false" ]
  [ "$(j '.findings|length')" = "0" ]
  [ "$(j '.warnings|length')" = "1" ]
  [ "$(j '.warn_count')" = "1" ]
  [ "$(j '.warnings[0].kind')" = "hardcoded_expected_value" ]
  [ "$(j '.warnings[0].id')" = "W1" ]
  [ "$(j '.warnings[0].file')" = "pkg/a.go" ]
  [ "$(j '.warnings[0].line')" = "4" ]
  [[ "$(j '.warnings[0].detail')" == *abcdefghij* ]]
  [[ "$(j '.summary')" == *"1 warning"* ]]
}

@test "7-char literal or 3-digit number does not warn" {
  printf '%s\n' 'var K = "abcdefg"; var N = 123' >> "$WORK/pkg/a.go"
  printf '%s\n' '	_ = "abcdefg"; _ = 123' >> "$WORK/pkg/a_test.go"
  change
  gate
  [ "$(j '.warnings|length')" = "0" ]
  [ "$(j '.warn_count')" = "0" ]
}

@test "4-digit number shared with a test warns" {
  printf '%s\n' 'var N = 4242' >> "$WORK/pkg/a.go"
  printf '%s\n' '	_ = 4242' >> "$WORK/pkg/a_test.go"
  change
  gate
  [ "$(j '.warnings|length')" = "1" ]
  [[ "$(j '.warnings[0].detail')" == *4242* ]]
}

@test "literal not present in any test file does not warn" {
  printf '%s\n' 'var K = "abcdefghij"' >> "$WORK/pkg/a.go"
  change
  gate
  [ "$(j '.warnings|length')" = "0" ]
}

@test "literal added on a test line does not warn" {
  printf '%s\n' '	_ = "abcdefghij"' >> "$WORK/pkg/a_test.go"
  change
  gate
  [ "$(j '.warnings|length')" = "0" ]
}

@test "warnings do not make a finding set judgeable" {
  printf '%s\n' 'var K = "abcdefghij"' >> "$WORK/pkg/a.go"
  printf '%s\n' '	t.Skip("abcdefghij")' >> "$WORK/pkg/a_test.go"
  change
  gate
  [ "$(j '.findings|length')" = "1" ]
  [ "$(j '.warnings|length')" = "1" ]
  [ "$(j '.needs_judge')" = "true" ]
}

# --- Carried from the Task 7 re-review

@test "backtick template literal containing a token does not mask a drop (js)" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js 'const s = `expect( x`;'
  one_finding assertions_dropped
}
@test "multi-line template literal containing tokens does not mask a drop (js)" {
  drop_line src/a.test.js 'expect(2)'
  printf '%s\n' 'const s = `' 'expect(2)' 'expect(3)' '`;' >> "$WORK/src/a.test.js"
  change
  one_finding assertions_dropped
}
@test "go raw string containing a token does not mask a drop" {
  drop_line pkg/a_test.go 't.Errorf("b")'
  printf '%s\n' '	_ = `' '	t.Errorf("b")' '	`' >> "$WORK/pkg/a_test.go"
  change
  one_finding assertions_dropped
}
@test "token after a template literal on the same line still counts" {
  add_line src/a.test.js 'const s = `x`; expect(3).toBe(3)'
  no_finding
}
@test "block comment padding vector /* expect( */ does not mask a drop" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js '/* expect( */'
  one_finding assertions_dropped
}
@test "multi-line block comment containing assertions counts as dropped (js)" {
  printf '%s\n' 'test("a", () => {' '  expect(1).toBe(1)' '  /*' '  expect(2).toBe(2)' '  */' '})' > "$WORK/src/a.test.js"
  change
  one_finding assertions_dropped
}
@test "block-commented assertion counts as dropped in go" {
  comment_out pkg/a_test.go 't.Errorf("b")' '/* '
  gate; [ "$(j '.findings[0].kind')" = "assertions_dropped" ]
}
@test "block-commented assertion counts as dropped in csharp" {
  awk '{ if (index($0, "Assert.Equal(2")) print "/* " $0 " */"; else print }' "$WORK/App.Tests/FooTests.cs" > "$WORK/x.new"
  mv "$WORK/x.new" "$WORK/App.Tests/FooTests.cs"
  change
  one_finding assertions_dropped
}
@test "block-commented assertion counts as dropped in java" {
  awk '{ if (index($0, "assertEquals(2")) print "/* " $0 " */"; else print }' "$WORK/src/test/java/ATest.java" > "$WORK/x.new"
  mv "$WORK/x.new" "$WORK/src/test/java/ATest.java"
  change
  one_finding assertions_dropped
}
@test "block-commented assertion counts as dropped in rust" {
  awk '{ if (index($0, "assert_eq!(2")) print "/* " $0 " */"; else print }' "$WORK/tests/it.rs" > "$WORK/x.new"
  mv "$WORK/x.new" "$WORK/tests/it.rs"
  change
  one_finding assertions_dropped
}
@test "assertion after a closing block comment on the same line counts" {
  awk '{ if (index($0, "expect(2)")) print "/* c */ " $0; else print }' "$WORK/src/a.test.js" > "$WORK/x.new"
  mv "$WORK/x.new" "$WORK/src/a.test.js"
  change
  no_finding
}
@test "python docstring containing assertions does not count" {
  printf '%s\n' 'def test_a():' '    assert 1 == 1' '    """' '    assert 2 == 2' '    """' > "$WORK/tests/test_app.py"
  change
  one_finding assertions_dropped
}
@test "python single-line triple-quoted string does not count" {
  printf '%s\n' 'def test_a():' '    assert 1 == 1' "    x = '''assert 2 == 2'''" > "$WORK/tests/test_app.py"
  change
  one_finding assertions_dropped
}
@test "python assertion after a docstring still counts" {
  printf '%s\n' 'def test_a():' '    """doc"""' '    assert 1 == 1' '    assert 2 == 2' > "$WORK/tests/test_app.py"
  change
  no_finding
}

@test "classify anchors the helper-file pattern on a word start" {
  mkdir -p "$WORK/internal"
  echo x > "$WORK/internal/latest_helpers.go"
  echo x > "$WORK/internal/contestutils.py"
  echo x > "$WORK/internal/test_helpers.go"
  echo x > "$WORK/internal/testutil.go"
  echo x > "$WORK/internal/FooTestHelper.cs"
  echo x > "$WORK/internal/my-testutil.js"
  change
  gate_raw --classify "$BASE" HEAD
  role() { j ".files[] | select(.path==\"internal/$1\") | .role"; }
  [ "$(role latest_helpers.go)" = "source" ]
  [ "$(role contestutils.py)" = "source" ]
  [ "$(role test_helpers.go)" = "test" ]
  [ "$(role testutil.go)" = "test" ]
  [ "$(role FooTestHelper.cs)" = "test" ]
  [ "$(role my-testutil.js)" = "test" ]
}

@test "classify roles for config, ci and baseline files" {
  put jest.config.js a
  put .github/workflows/ci.yml a
  put stryker.conf.mjs a
  put README.md a
  change
  gate_raw --classify "$BASE" HEAD
  [ "$(j '.files[]|select(.path=="jest.config.js")|.role')" = "config" ]
  [ "$(j '.files[]|select(.path=="stryker.conf.mjs")|.role')" = "config" ]
  [ "$(j '.files[]|select(.path==".github/workflows/ci.yml")|.role')" = "ci" ]
  [ "$(j '.files[]|select(.path=="README.md")|.role')" = "other" ]
}


# ===========================================================================
# Task 8 fix round 1
# ===========================================================================

@test "adding cfg(test) to a prod mod is source" {
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n\nmod helpers {\n    pub fn h() {}\n}'
  commit_base
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n\n#[cfg(test)]\nmod helpers {\n    pub fn h() {}\n}'
  change
  expect_source_changed
}

@test "removing cfg(test) from a test mod is source" {
  rs_base
  put src/lib.rs "${RS_LIB/"#[cfg(test)]
"/}"
  change
  expect_source_changed
}

@test "renaming the opener of a test mod is source" {
  rs_base
  put src/lib.rs "${RS_LIB/"mod tests {"/"mod tests2 {"}"
  change
  expect_source_changed
}

@test "adding a whole new cfg(test) mod is a test hunk" {
  put src/lib.rs 'pub fn a() -> i32 { 1 }'
  commit_base
  put src/lib.rs "$RS_LIB"
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
}

@test "br raw string with a trailing backslash does not swallow the file" {
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    const S: &[u8] = br"\\";\n    #[test]\n    fn t() {}\n}'
  commit_base
  printf '%s\n' 'fn b() {}' >> "$WORK/src/lib.rs"
  change
  expect_source_changed
}

@test "cr raw string with a trailing backslash does not swallow the file" {
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    const S: &std::ffi::CStr = cr"\\";\n    #[test]\n    fn t() {}\n}'
  commit_base
  printf '%s\n' 'fn b() {}' >> "$WORK/src/lib.rs"
  change
  expect_source_changed
}

@test "identifier ending in r before a quote is not a raw string" {
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n\n#[cfg(test)]\nmod tests {\n    fn t() { let s = foo_r("x"); }\n}'
  commit_base
  printf '%s\n' 'fn b() {}' >> "$WORK/src/lib.rs"
  change
  expect_source_changed
}

@test "nested template literal inside a template does not mask a drop" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js 'const s = `a ${ `expect( x` } b ${ {a: 1}.a } expect( y`;'
  one_finding assertions_dropped
}

@test "assertion after a nested template literal line still counts" {
  drop_line src/a.test.js 'expect(2)'
  add_line src/a.test.js 'const s = `a ${ `x` } b`; expect(3).toBe(3)'
  no_finding
}

@test "ci modify" {
  put .github/workflows/ci.yml 'on: push'; commit_base
  put .github/workflows/ci.yml 'on: pull_request'; change
  one_finding ci_config_changed
}
@test "ci delete" {
  put .github/workflows/ci.yml 'on: push'; commit_base
  git -C "$WORK" rm -q .github/workflows/ci.yml; change
  one_finding ci_config_changed
}
@test "ci plain azure-pipelines.yml" { put azure-pipelines.yml 'a: 1'; change; one_finding ci_config_changed; }

@test "config negative: Cargo.toml version bump" {
  cfg_none Cargo.toml $'[package]\nname = "a"\nversion = "0.1.0"' $'[package]\nname = "a"\nversion = "0.1.1"'
}

@test "warning corpus includes cfg(test) blocks in src rs files" {
  rs_base
  put src/lib.rs "${RS_LIB/"assert_eq!(a(), 1);"/"assert_eq!(a(), 1); let _k = \"abcdefghij\";"}"
  printf '%s\n' 'pub fn b() -> &'"'"'static str { "abcdefghij" }' >> "$WORK/src/lib.rs"
  change
  gate
  [ "$(j '.warnings|length')" -ge 1 ]
  [[ "$(j '[.warnings[].detail]|join(" ")')" == *abcdefghij* ]]
}


# ===========================================================================
# Task 8 fix round 2: code sharing a line with a block's closing brace
# ===========================================================================

@test "code after the closing brace of an existing test mod is source" {
  rs_base
  put src/lib.rs "$RS_LIB"' pub fn evil() -> i32 { 42 }'
  change
  expect_source_changed
}

@test "code after the closing brace inside a whole new test mod is source" {
  put src/lib.rs 'pub fn a() -> i32 { 1 }'
  commit_base
  put src/lib.rs "$RS_LIB"' pub fn evil() -> i32 { 42 }'
  change
  expect_source_changed
}

@test "code after a one-line test mod is source" {
  put src/lib.rs 'pub fn a() -> i32 { 1 }'
  commit_base
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n#[cfg(test)]\nmod tests { } pub fn evil() -> i32 { 42 }'
  change
  expect_source_changed
}

@test "code before mod on the opener line is source" {
  put src/lib.rs 'pub fn a() -> i32 { 1 }'
  commit_base
  put src/lib.rs $'pub fn a() -> i32 { 1 }\n#[cfg(test)]\npub fn evil() -> i32 { 42 } mod tests { }'
  change
  expect_source_changed
}

@test "closing brace followed only by a comment stays a test hunk" {
  rs_base
  put src/lib.rs "$RS_LIB"' // end of tests'
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
}

@test "closing brace followed by a block comment stays a test hunk" {
  rs_base
  put src/lib.rs "$RS_LIB"' /* end */'
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
}

# --- tests-only enforces an allowed set, not a source classifier

@test "tests-only: an SQL asset change forces a void" {
  put db/policy.sql 'select 1;'
  commit_base
  put db/policy.sql 'select 2;'
  change
  expect_source_changed
}

@test "tests-only: a template, a script and a yaml change each force a void" {
  put tpl/page.tmpl 'a'; put bin/run.sh 'a'; put deploy/app.yaml 'a: 1'
  commit_base
  put tpl/page.tmpl 'b'; put bin/run.sh 'b'; put deploy/app.yaml 'a: 2'
  change
  ts_only
  [ "$(j '.force_void')" = "true" ]
  [ "$(j '[.findings[]|select(.kind=="source_changed")]|length')" = "3" ]
}

@test "tests-only: a config file change forces a void" {
  put jest.config.js 'module.exports = {}'
  commit_base
  put jest.config.js 'module.exports = { testPathIgnorePatterns: ["a"] }'
  change
  expect_source_changed
}

@test "tests-only: .gitattributes is never exempt, at any depth" {
  add_line pkg/a_test.go '	t.Errorf("c")'
  put pkg/.gitattributes '*_test.go -diff'
  change
  ts_only
  [ "$(j '.force_void')" = "true" ]
  [ "$(j '.source_changed')" = "true" ]
}

@test "tests-only: the unrelated .arbitron directory is not exempt" {
  put .arbitron/workflow.json '{}'
  change
  expect_source_changed
}

@test "tests-only: owned prose and pipeline state stay allowed" {
  put docs/notes.md 'x'
  put README.md 'x'
  put .arbitron/pipeline/one.harden.json '{}'
  change
  ts_only
  [ "$(j '.source_changed')" = "false" ]
  [ "$(j '.force_void')" = "false" ]
}

@test "tests-only: renaming a test file onto a source path forces a void" {
  git -C "$WORK" mv pkg/a_test.go pkg/b.go
  change
  ts_only
  [ "$(j '.force_void')" = "true" ]
}

# --- git metadata the checked agent controls cannot blind the gate

@test "a committed .gitattributes with -diff cannot hide a skip marker" {
  add_line pkg/a_test.go '	t.Skip("x")'
  put .gitattributes '*_test.go -diff'
  change
  gate
  [ "$status" -eq 0 ]
  [ "$(j '.clean')" = "false" ]
  [ "$(j '[.findings[].kind]|(index("skip_marker_added")!=null) or (index("test_config_weakened")!=null)')" = "true" ]
  [ "$(j '[.findings[]|select(.file==".gitattributes")]|length')" = "1" ]
}

@test "a nested .gitattributes change is a finding of its own" {
  put pkg/.gitattributes '* -diff'
  change
  gate
  [ "$(j '.clean')" = "false" ]
  [ "$(j '.findings[0].file')" = "pkg/.gitattributes" ]
  [ "$(j '.findings[0].kind')" = "test_config_weakened" ]
}

@test ".git/info/attributes with -diff cannot hide a skip marker" {
  mkdir -p "$WORK/.git/info"
  printf '* -diff\n' > "$WORK/.git/info/attributes"
  add_line pkg/a_test.go '	t.Skip("x")'
  one_finding skip_marker_added
}

@test "a replace ref cannot make a tampered test blob look original" {
  add_line pkg/a_test.go '	t.Skip("x")'
  tampered="$(git -C "$WORK" rev-parse HEAD:pkg/a_test.go)"
  original="$(git -C "$WORK" rev-parse "$BASE:pkg/a_test.go")"
  git -C "$WORK" replace "$tampered" "$original"
  gate
  [ "$status" -eq 0 ]
  [ "$(j '.clean')" = "false" ]
  [ "$(j '[.findings[].kind]|index("skip_marker_added")!=null')" = "true" ]
}

@test "a replace ref cannot hide dropped assertions" {
  drop_line pkg/a_test.go 't.Errorf("b")'
  tampered="$(git -C "$WORK" rev-parse HEAD:pkg/a_test.go)"
  original="$(git -C "$WORK" rev-parse "$BASE:pkg/a_test.go")"
  git -C "$WORK" replace "$tampered" "$original"
  gate
  [ "$(j '.clean')" = "false" ]
  [ "$(j '[.findings[].kind]|index("assertions_dropped")!=null')" = "true" ]
}
