#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# suite-summary.sh turns a mutation tool's log into a short failure summary
# (failing test names, then the first failure's first lines) that fits in the
# first 4KB of a node error, ahead of a log the engine truncates at 1MB.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/suite-summary.sh"
  T="$BATS_TEST_TMPDIR"
  LOG="$T/tool.log"
}

summarize() { run --separate-stderr sh "$SCRIPT" "$LOG"; }

noise() { i=0; while [ "$i" -lt "$1" ]; do echo "ok  	noise/pkg$i	0.01s"; i=$((i + 1)); done; }

@test "go: lists every failing test and package, then the first failure" {
  {
    noise 50
    printf -- '--- FAIL: TestProxyTLS (0.02s)\n'
    printf '    proxy_test.go:41: handshake: remote error: tls: bad certificate\n'
    printf '    proxy_test.go:42: want 200\n'
    printf 'FAIL\n'
    printf 'FAIL\tgithub.com/x/y/adapter/proxy\t0.31s\n'
    noise 20
    printf -- '--- FAIL: TestMakeCache (0.00s)\n'
    printf '    make_test.go:9: cannot write cache\n'
    printf 'FAIL\tgithub.com/x/y/tools\t0.10s\n'
  } > "$LOG"
  summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"--- FAIL: TestProxyTLS"* ]]
  [[ "$output" == *"--- FAIL: TestMakeCache"* ]]
  [[ "$output" == *"FAIL	github.com/x/y/adapter/proxy"* ]]
  [[ "$output" == *"bad certificate"* ]]
  # The names come before the first-failure excerpt.
  names="${output%%first failure*}"
  [[ "$names" == *TestMakeCache* ]]
  [[ "$output" != *"noise/pkg"* ]]
}

@test "the first-failure excerpt is at most 40 lines" {
  { printf -- '--- FAIL: TestBig (0.00s)\n'; i=0; while [ "$i" -lt 200 ]; do echo "    big_test.go:$i: line $i"; i=$((i + 1)); done; } > "$LOG"
  summarize
  excerpt="${output#*first failure*$'\n'}"
  [ "$(printf '%s\n' "$excerpt" | grep -c 'big_test.go')" -le 40 ]
  [[ "$output" == *"line 0"* ]]
  [[ "$output" != *"line 100"* ]]
}

@test "the summary stays under 3.5KB however much failed" {
  { i=0; while [ "$i" -lt 2000 ]; do printf -- '--- FAIL: TestN%d_%s (0.00s)\n' "$i" "$(printf 'x%.0s' $(seq 1 100))"; i=$((i + 1)); done; } > "$LOG"
  summarize
  [ "${#output}" -le 3500 ]
  [[ "$output" == *"TestN0_"* ]]
}

@test "a go panic with no FAIL line is the first failure" {
  { noise 5; printf 'panic: runtime error: index out of range\n\ngoroutine 1 [running]:\nmain.f()\n'; } > "$LOG"
  summarize
  [[ "$output" == *"panic: runtime error"* ]]
}

@test "pytest FAILED lines are listed" {
  printf '____ test_a ____\n    assert 1 == 2\nE   AssertionError\nFAILED tests/test_a.py::test_a - AssertionError\n' > "$LOG"
  summarize
  [[ "$output" == *"FAILED tests/test_a.py::test_a"* ]]
  [[ "$output" == *"AssertionError"* ]]
}

@test "cargo failing tests are listed" {
  printf 'test it::works ... ok\ntest it::breaks ... FAILED\n\n---- it::breaks stdout ----\nthread panicked at src/lib.rs:3\n' > "$LOG"
  summarize
  [[ "$output" == *"test it::breaks ... FAILED"* ]]
  [[ "$output" != *"it::works"* ]]
  [[ "$output" == *"thread panicked"* ]]
}

@test "with no failure markers the log tail is shown" {
  { noise 100; echo "error: sandbox: operation not permitted"; } > "$LOG"
  summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"no test failure lines"* ]]
  [[ "$output" == *"operation not permitted"* ]]
  [ "${#output}" -le 3500 ]
}

@test "a missing log is reported, not an error" {
  summarize
  [ "$status" -eq 0 ]
  [[ "$output" == *"no log"* ]]
}
