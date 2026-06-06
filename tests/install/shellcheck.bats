#!/usr/bin/env bats
#
# Lint gate: install.sh must pass shellcheck clean.

load test_helper

@test "shellcheck install.sh is clean" {
  if ! command -v shellcheck >/dev/null 2>&1; then
    skip "shellcheck not installed"
  fi
  run shellcheck "$INSTALL_SH"
  [ "$status" -eq 0 ]
}
