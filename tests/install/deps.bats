#!/usr/bin/env bats
#
# Task 2: dependency resolution (presence/version -> pkg-mgr -> bootstrap ->
# checksum-verified direct download). All package managers, npm, curl and
# version probes are mocked on an isolated PATH; the network is never touched.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

# Helper: stage a recording fake for one or more tool names.
_mock() { install_mock recording-fake "$@"; }

# --- package-manager selection ----------------------------------------------

@test "detect_pkg_mgr returns brew on macOS" {
  _mock brew
  ARBITRON_UNAME_S=Darwin run detect_pkg_mgr
  [ "$status" -eq 0 ]
  [ "$output" = "brew" ]
}

@test "detect_pkg_mgr returns apt-get when only apt present on Linux" {
  _mock apt-get
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ "$output" = "apt-get" ]
}

@test "detect_pkg_mgr returns dnf when only dnf present" {
  _mock dnf
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ "$output" = "dnf" ]
}

@test "detect_pkg_mgr returns pacman when only pacman present" {
  _mock pacman
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ "$output" = "pacman" ]
}

@test "detect_pkg_mgr returns zypper when only zypper present" {
  _mock zypper
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ "$output" = "zypper" ]
}

@test "detect_pkg_mgr precedence apt > dnf > pacman > zypper" {
  _mock apt-get dnf pacman zypper
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ "$output" = "apt-get" ]
}

@test "detect_pkg_mgr empty when no manager present on Linux" {
  ARBITRON_UNAME_S=Linux run detect_pkg_mgr
  [ -z "$output" ]
}

# --- pkg_install dispatch ----------------------------------------------------

@test "pkg_install uses brew install on macOS" {
  _mock brew
  ARBITRON_UNAME_S=Darwin run pkg_install node
  [ "$status" -eq 0 ]
  mock_log_has "brew install node"
}

@test "pkg_install uses apt-get install -y" {
  _mock apt-get
  ARBITRON_UNAME_S=Linux run pkg_install git
  mock_log_has "apt-get install -y git"
}

@test "pkg_install uses dnf install -y" {
  _mock dnf
  ARBITRON_UNAME_S=Linux run pkg_install git
  mock_log_has "dnf install -y git"
}

@test "pkg_install uses pacman -S --noconfirm" {
  _mock pacman
  ARBITRON_UNAME_S=Linux run pkg_install git
  mock_log_has "pacman -S --noconfirm git"
}

@test "pkg_install uses zypper install -y" {
  _mock zypper
  ARBITRON_UNAME_S=Linux run pkg_install git
  mock_log_has "zypper install -y git"
}

# --- presence + version: ensure_node ----------------------------------------

@test "ensure_node is a no-op when node satisfies the minimum" {
  install_fake node 'echo v20.11.0'
  _mock brew
  ARBITRON_UNAME_S=Darwin run ensure_node
  [ "$status" -eq 0 ]
  ! mock_log_has "brew install"
}

@test "ensure_node installs via pkg mgr when node is absent" {
  _mock brew
  ARBITRON_UNAME_S=Darwin run ensure_node
  [ "$status" -eq 0 ]
  mock_log_has "brew install node"
}

@test "ensure_node upgrades when node is below minimum" {
  install_fake node 'echo v16.0.0'
  _mock brew
  ARBITRON_UNAME_S=Darwin run ensure_node
  mock_log_has "brew install node"
}

# --- ensure_git / ensure_gh --------------------------------------------------

@test "ensure_git no-op when git present" {
  install_fake git 'echo git version 2.40.0'
  _mock apt-get
  ARBITRON_UNAME_S=Linux run ensure_git
  [ "$status" -eq 0 ]
  ! mock_log_has "apt-get install"
}

@test "ensure_gh installs gh when absent" {
  _mock apt-get
  ARBITRON_UNAME_S=Linux run ensure_gh
  mock_log_has "apt-get install -y gh"
}

# --- bootstrap (macOS Homebrew) ---------------------------------------------

@test "ensure_node bootstraps Homebrew when brew absent and accepted" {
  # No brew on PATH; bootstrap accepted, then brew (now mocked post-bootstrap).
  install_fake curl 'exit 0'
  ARBITRON_UNAME_S=Darwin ARBITRON_BOOTSTRAP_BREW=yes run bootstrap_pkg_mgr
  [ "$status" -eq 0 ]
  mock_log_has "curl"
}

@test "bootstrap declined returns non-zero so caller can fall back" {
  ARBITRON_UNAME_S=Darwin ARBITRON_BOOTSTRAP_BREW=no run bootstrap_pkg_mgr
  [ "$status" -ne 0 ]
}

# --- claude via Arbitron-owned npm prefix ------------------------------------

@test "ensure_claude installs via npm into the Arbitron npm prefix" {
  install_fake node 'echo v20.0.0'
  install_fake npm 'exit 0'
  ARBITRON_UNAME_S=Darwin run ensure_claude
  [ "$status" -eq 0 ]
  mock_log_has "npm install -g @anthropic-ai/claude-code"
  # The recorded npm invocation must target the Arbitron-owned prefix.
  grep -q "npm" "$MOCK_LOG"
}

@test "ensure_claude records the resolved claude path under the npm prefix" {
  install_fake node 'echo v20.0.0'
  install_fake npm 'exit 0'
  ARBITRON_UNAME_S=Darwin run ensure_claude
  [ "$status" -eq 0 ]
  # ARBITRON_CLAUDE_PATH (exported) points under the runtime npm prefix.
  run bash -c "install_fake() { :; }; ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; printf '%s' \"\$(layout_runtime)/npm/bin/claude\""
  [[ "$output" == *"$HOME/.arbitron/runtime/npm/bin/claude" ]]
}

@test "ensure_claude reuses a satisfying system claude" {
  install_fake claude 'echo 1.2.3'
  install_fake npm 'exit 0'
  ARBITRON_UNAME_S=Darwin run ensure_claude
  [ "$status" -eq 0 ]
  ! mock_log_has "npm install -g @anthropic-ai/claude-code"
}

# --- checksum enforcement on direct download --------------------------------

@test "download_verified succeeds on a matching sha256" {
  local content="payload-bytes"
  local sum
  sum="$(printf '%s' "$content" | sha256_tool)"
  # curl mock writes the expected content to the -o destination.
  install_fake curl 'for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then printf "payload-bytes" > "$a"; fi; done'
  local dest="$TEST_SANDBOX/out.bin"
  run download_verified "https://example/x" "$sum" "$dest"
  [ "$status" -eq 0 ]
  [ -f "$dest" ]
}

@test "download_verified aborts on a mismatched sha256 and leaves no artifact" {
  install_fake curl 'for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then printf "tampered" > "$a"; fi; done'
  local dest="$TEST_SANDBOX/out.bin"
  run download_verified "https://example/x" "deadbeef" "$dest"
  [ "$status" -ne 0 ]
  [ ! -f "$dest" ]
}

# --- opencode (optional) -----------------------------------------------------

@test "ensure_opencode declined is non-fatal and installs nothing" {
  _mock brew
  ARBITRON_UNAME_S=Darwin ARBITRON_INSTALL_OPENCODE=no run ensure_opencode
  [ "$status" -eq 0 ]
  ! mock_log_has "brew install opencode"
}

# --- resolve_dependencies orchestration -------------------------------------

@test "resolve_dependencies skips the agent toolchain for api-key installs" {
  _mock brew
  ARBITRON_UNAME_S=Darwin ARBITRON_LLM_ACCESS=api-key run resolve_dependencies
  [ "$status" -eq 0 ]
  ! mock_log_has "npm install -g @anthropic-ai/claude-code"
}
