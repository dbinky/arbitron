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

@test "render_launchd_plist writes a plist that references the wrapper and config path" {
  render_launchd_plist
  local p
  p="$(_plist)"
  [ -f "$p" ]
  # wrapper path (which contains the binary path as a prefix) appears in the plist.
  grep -qF "$(layout_bin)/arbitron-service" "$p"
  # config path appears in the EnvironmentVariables dict.
  grep -qF "$(layout_config)" "$p"
}

@test "plist ProgramArguments uses the wrapper script not the binary directly" {
  render_launchd_plist
  local p wrapper
  p="$(_plist)"
  wrapper="$(layout_bin)/arbitron-service"
  # Wrapper path must appear as the launch target.
  grep -qF "$wrapper" "$p"
  # The bare binary invocation with serve sub-command must NOT be the target
  # (serve + --config come from the wrapper, not the plist).
  ! grep -qF "<string>serve</string>" "$p"
}

@test "the launchd wrapper script sources the secrets env before execing the binary" {
  render_launchd_plist
  local wrapper
  wrapper="$(layout_bin)/arbitron-service"
  [ -f "$wrapper" ]
  [ -x "$wrapper" ]
  # Wrapper must source the secrets env file.
  grep -qF "$(layout_secrets_env)" "$wrapper"
  # Wrapper must exec the real binary.
  grep -qF "exec" "$wrapper"
  # serve --config must come from the wrapper, not the plist.
  grep -qF "serve" "$wrapper"
  grep -qF -- "--config" "$wrapper"
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

@test "plist StandardOutPath and StandardErrorPath are absolute paths" {
  # launchd requires absolute log paths — relative paths silently produce no
  # log output, making debugging a failed start impossible.
  render_launchd_plist
  local p
  p="$(_plist)"
  # Extract the two path strings that follow the Std{Out,Err}Path keys.
  # Both must begin with '/' (absolute path marker).
  grep -A1 "StandardOutPath"  "$p" | grep '<string>' | grep -qE '<string>/'
  grep -A1 "StandardErrorPath" "$p" | grep '<string>' | grep -qE '<string>/'
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

@test "service_remove on macOS removes the plist and the secrets-env wrapper" {
  # After uninstall the plist and the arbitron-service wrapper (which holds the
  # binary path) must both be gone so no stale service definition remains.
  render_launchd_plist
  local plist wrapper
  plist="$(_plist)"
  wrapper="$(layout_bin)/arbitron-service"
  [ -f "$plist" ]
  [ -f "$wrapper" ]
  run service_remove
  [ "$status" -eq 0 ]
  [ ! -f "$plist" ]
  [ ! -f "$wrapper" ]
  grep -q "bootout" "$MOCK_LOG"
}
