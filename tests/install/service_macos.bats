#!/usr/bin/env bats
#
# Task 4: macOS launchd LaunchAgent registration.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  export ARBITRON_PORT=8080
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake launchctl
}

teardown() {
  install_teardown_sandbox
}

_plist() { printf '%s/Library/LaunchAgents/dev.arbitron.plist\n' "$HOME"; }

@test "render_launchd_plist writes a plist invoking the absolute binary + --config" {
  render_launchd_plist
  local p
  p="$(_plist)"
  [ -f "$p" ]
  grep -qF "$(layout_bin)/arbitron" "$p"
  grep -qF "serve" "$p"
  grep -qF -- "--config" "$p"
  grep -qF "$(layout_config)" "$p"
}

@test "plist sets RunAtLoad and KeepAlive true" {
  render_launchd_plist
  local p
  p="$(_plist)"
  grep -qF "RunAtLoad" "$p"
  grep -qF "KeepAlive" "$p"
  # the <true/> markers follow those keys
  grep -q "<true/>" "$p"
}

@test "plist EnvironmentVariables include ARBITRON_CONFIG with the absolute config path" {
  render_launchd_plist
  local p
  p="$(_plist)"
  grep -qF "ARBITRON_CONFIG" "$p"
  grep -qF "$(layout_config)" "$p"
}

@test "plist is valid XML via plutil when available" {
  render_launchd_plist
  if command -v plutil >/dev/null 2>&1; then
    run plutil -lint "$(_plist)"
    [ "$status" -eq 0 ]
  else
    skip "plutil not available"
  fi
}

@test "register_service_macos loads the agent via launchctl" {
  register_service_macos
  mock_log_has "launchctl"
  grep -q "bootstrap\|load" "$MOCK_LOG"
}
