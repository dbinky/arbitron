#!/usr/bin/env bats
#
# Task 1: OS/arch/elevation detection, install-type selection, existing-install
# detection and state dispatch.

load test_helper

setup() {
  install_setup_sandbox
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

# uname is mocked via an env override the detectors honour so tests do not depend
# on the real platform.

@test "detect_os returns macos for Darwin" {
  ARBITRON_UNAME_S=Darwin run detect_os
  [ "$status" -eq 0 ]
  [ "$output" = "macos" ]
}

@test "detect_os returns linux for Linux" {
  ARBITRON_UNAME_S=Linux run detect_os
  [ "$status" -eq 0 ]
  [ "$output" = "linux" ]
}

@test "detect_os errors on unsupported OS" {
  ARBITRON_UNAME_S=Plan9 run detect_os
  [ "$status" -ne 0 ]
}

@test "detect_arch normalises x86_64/amd64 to amd64" {
  ARBITRON_UNAME_M=x86_64 run detect_arch
  [ "$output" = "amd64" ]
  ARBITRON_UNAME_M=amd64 run detect_arch
  [ "$output" = "amd64" ]
}

@test "detect_arch normalises aarch64/arm64 to arm64" {
  ARBITRON_UNAME_M=aarch64 run detect_arch
  [ "$output" = "arm64" ]
  ARBITRON_UNAME_M=arm64 run detect_arch
  [ "$output" = "arm64" ]
}

@test "detect_arch errors on unsupported arch" {
  ARBITRON_UNAME_M=mips run detect_arch
  [ "$status" -ne 0 ]
}

@test "is_elevated false when uid != 0 (the local path)" {
  ARBITRON_UID=1000 run is_elevated
  [ "$status" -ne 0 ]
}

@test "is_elevated true when uid == 0" {
  ARBITRON_UID=0 run is_elevated
  [ "$status" -eq 0 ]
}

@test "select_install_type yields Local on macOS" {
  ARBITRON_UNAME_S=Darwin run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Local" ]
}

@test "select_install_type yields Local on Linux in P1" {
  ARBITRON_UNAME_S=Linux run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Local" ]
}

@test "select_install_type ARBITRON_INSTALL_TYPE=shared yields Shared on Linux" {
  ARBITRON_UNAME_S=Linux ARBITRON_INSTALL_TYPE=shared run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Shared" ]
}

@test "select_install_type ARBITRON_INSTALL_TYPE=local yields Local on Linux" {
  ARBITRON_UNAME_S=Linux ARBITRON_INSTALL_TYPE=local run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Local" ]
}

@test "select_install_type ARBITRON_INSTALL_TYPE=unknown errors on Linux" {
  ARBITRON_UNAME_S=Linux ARBITRON_INSTALL_TYPE=unknown run select_install_type
  [ "$status" -ne 0 ]
}

@test "select_install_type ignores ARBITRON_INSTALL_TYPE on macOS (always Local)" {
  ARBITRON_UNAME_S=Darwin ARBITRON_INSTALL_TYPE=shared run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Local" ]
}

# --- existing-install detection & dispatch -----------------------------------

@test "detect_state is Fresh with no config and no service" {
  run detect_state
  [ "$status" -eq 0 ]
  [ "$output" = "Fresh" ]
}

@test "detect_state honours ARBITRON_ACTION=upgrade when install present" {
  mkdir -p "$(arbitron_home)"
  : >"$(layout_config)"
  ARBITRON_ACTION=upgrade run detect_state
  [ "$status" -eq 0 ]
  [ "$output" = "Upgrade" ]
}

@test "detect_state routes reconfigure via ARBITRON_ACTION" {
  mkdir -p "$(arbitron_home)"
  : >"$(layout_config)"
  ARBITRON_ACTION=reconfigure run detect_state
  [ "$output" = "Reconfigure" ]
}

@test "detect_state routes uninstall via ARBITRON_ACTION" {
  mkdir -p "$(arbitron_home)"
  : >"$(layout_config)"
  ARBITRON_ACTION=uninstall run detect_state
  [ "$output" = "Uninstall" ]
}

@test "detect_state errors on unknown ARBITRON_ACTION" {
  ARBITRON_ACTION=frobnicate run detect_state
  [ "$status" -ne 0 ]
}

@test "detect_state with existing install on headless stdin defaults to Upgrade" {
  # Re-running the installer piped (curl | bash) on an existing install must not hang;
  # it should return the safe default 'upgrade' when stdin is /dev/null.
  mkdir -p "$(arbitron_home)"
  : >"$(layout_config)"
  run detect_state </dev/null
  [ "$status" -eq 0 ]
  [ "$output" = "Upgrade" ]
}

@test "detect_state routes Fresh via ARBITRON_ACTION=fresh" {
  ARBITRON_ACTION=fresh run detect_state
  [ "$status" -eq 0 ]
  [ "$output" = "Fresh" ]
}

@test "has_existing_config true only when config.yaml present" {
  run has_existing_config
  [ "$status" -ne 0 ]
  mkdir -p "$(arbitron_home)"
  : >"$(layout_config)"
  run has_existing_config
  [ "$status" -eq 0 ]
}
