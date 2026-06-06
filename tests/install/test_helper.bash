# Common bats setup for the install.sh suite.
#
# Provides a sandboxed HOME, a mock-bin dir prepended to PATH where recording
# fakes (tests/install/mocks/*) are installed per-test, and a $MOCK_LOG file the
# fakes append their argv to so assertions can read what was invoked.
#
# Sourcing install.sh: tests set ARBITRON_INSTALL_LIB_ONLY=1 so install.sh
# defines its functions and returns WITHOUT running main (the canonical guard
# from Task 1's alignment notes).

# Absolute path to the repo root (this file lives at tests/install/).
INSTALL_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export INSTALL_REPO_ROOT
export INSTALL_SH="$INSTALL_REPO_ROOT/install.sh"

# install_setup_sandbox creates a fresh per-test sandbox: HOME, a mock-bin dir on
# PATH, and an empty $MOCK_LOG. Call from each test file's setup().
install_setup_sandbox() {
  # Remember the inbound PATH so teardown can restore it (a test may isolate PATH
  # to a sandbox-internal coreutils dir that gets deleted on teardown).
  ORIG_PATH="$PATH"
  export ORIG_PATH

  TEST_SANDBOX="$(mktemp -d "${BATS_TMPDIR:-/tmp}/arbitron-install.XXXXXX")"
  export TEST_SANDBOX
  export HOME="$TEST_SANDBOX/home"
  mkdir -p "$HOME"

  export MOCK_BIN="$TEST_SANDBOX/bin"
  mkdir -p "$MOCK_BIN"
  export PATH="$MOCK_BIN:$PATH"

  export MOCK_LOG="$TEST_SANDBOX/mock.log"
  : >"$MOCK_LOG"

  # USER is referenced by the systemd/loginctl path; pin it deterministically.
  export USER="${USER:-tester}"
}

install_teardown_sandbox() {
  # Restore the inbound PATH first so neither this rm nor bats's own post-test
  # cleanup depends on a sandbox-internal coreutils dir we are about to delete.
  [ -n "${ORIG_PATH:-}" ] && export PATH="$ORIG_PATH"
  if [ -n "${TEST_SANDBOX:-}" ] && [ -d "$TEST_SANDBOX" ]; then
    rm -rf "$TEST_SANDBOX"
  fi
}

# install_mock <name> [as <othername>...] copies a recording fake from
# tests/install/mocks/ onto the mock PATH. With extra args, also symlinks the
# same fake under those alternate names (e.g. install_mock pkg apt-get dnf).
install_mock() {
  local src="$INSTALL_REPO_ROOT/tests/install/mocks/$1"
  shift || true
  cp "$src" "$MOCK_BIN/$(basename "$src")"
  chmod +x "$MOCK_BIN/$(basename "$src")"
  local extra
  for extra in "$@"; do
    cp "$src" "$MOCK_BIN/$extra"
    chmod +x "$MOCK_BIN/$extra"
  done
}

# install_fake <name> <body...> writes an ad-hoc recording fake to the mock PATH.
# The body is the script after the recording shebang; it can read "$@".
install_fake() {
  local name="$1"
  shift
  {
    echo '#!/usr/bin/env bash'
    echo 'printf "%s" "$(basename "$0")" >>"$MOCK_LOG"'
    echo 'for a in "$@"; do printf " %s" "$a" >>"$MOCK_LOG"; done'
    echo 'printf "\n" >>"$MOCK_LOG"'
    printf '%s\n' "$*"
  } >"$MOCK_BIN/$name"
  chmod +x "$MOCK_BIN/$name"
}

# install_remove_cmd shadows a command so require_cmd reports it absent: we point
# PATH at only the mock bin and a minimal coreutils set. Simpler: create a
# wrapper dir without the command. Here we just ensure it is not in MOCK_BIN and
# rely on tests controlling PATH; provided as a no-op placeholder for clarity.

# mock_log_has <substring> greps $MOCK_LOG for a recorded invocation.
mock_log_has() {
  grep -qF -- "$1" "$MOCK_LOG"
}

# install_isolate_path restricts PATH to ONLY the mock-bin dir plus a curated
# directory of coreutils symlinks install.sh itself needs. This guarantees any
# tool NOT explicitly mocked (node, git, brew, ...) is genuinely absent, so deps
# tests can exercise the missing-dependency branches even on a host that has the
# real tools installed. Call AFTER install_setup_sandbox.
install_isolate_path() {
  local coreutils="$TEST_SANDBOX/coreutils"
  mkdir -p "$coreutils"
  local tool src
  for tool in bash sh env printf cat grep egrep awk sed tr head tail cut sort \
              uniq wc id uname mkdir rm mv cp chmod stat ln test true false \
              dirname basename mktemp date sleep tee xargs find sha256sum shasum \
              base64 openssl python3 plutil; do
    src="$(command -v "$tool" 2>/dev/null || true)"
    [ -n "$src" ] && ln -sf "$src" "$coreutils/$tool"
  done
  export PATH="$MOCK_BIN:$coreutils"
}
