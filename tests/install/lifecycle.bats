#!/usr/bin/env bats
#
# Task 4: health-check + upgrade / reconfigure / uninstall lifecycle.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64 USER=tester
  export ARBITRON_PORT=8080 ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no ARBITRON_DATA_DIR="$HOME/.arbitron/data"
  export ARBITRON_CLAUDE_PATH="$HOME/.arbitron/runtime/npm/bin/claude"
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake systemctl loginctl
}

teardown() {
  install_teardown_sandbox
}

# Stage a fully "installed" state on disk (config, secrets, binary, unit).
_stage_installed() {
  generate_master_key
  run_config_qa </dev/null
  write_config_yaml
  write_secrets_env
  mkdir -p "$(layout_bin)"
  printf '#!/bin/sh\n' >"$(layout_bin)/arbitron"
  chmod 0755 "$(layout_bin)/arbitron"
  render_systemd_unit
}

# --- health check ------------------------------------------------------------

@test "health_check succeeds on a mocked 200" {
  install_fake curl 'echo 200'   # our health_check reads the http code from curl -w
  run health_check 8080
  [ "$status" -eq 0 ]
}

@test "health_check fails with a log pointer on persistent non-200" {
  install_fake curl 'echo 000; exit 7'
  ARBITRON_HEALTH_RETRIES=2 ARBITRON_HEALTH_SLEEP=0 run health_check 8080
  [ "$status" -ne 0 ]
  [[ "$output" == *"$(layout_logs)"* ]]
}

# --- upgrade -----------------------------------------------------------------

@test "run_upgrade swaps the binary, restarts, and preserves config/data/secrets" {
  _stage_installed
  local cfg_before secrets_before
  cfg_before="$(cat "$(layout_config)")"
  secrets_before="$(cat "$(layout_secrets_env)")"

  # mock the binary download + health check.
  export ARBITRON_VERSION=9.9.9
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_9.9.9_linux_amd64\n" "$(printf NEW | sha256sum | cut -d" " -f1)" > "$dest" ;;
      *releases*)   printf "NEW" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
  run run_upgrade
  [ "$status" -eq 0 ]
  # binary restarted
  grep -q "systemctl --user restart arbitron" "$MOCK_LOG"
  # config + secrets unchanged
  [ "$(cat "$(layout_config)")" = "$cfg_before" ]
  [ "$(cat "$(layout_secrets_env)")" = "$secrets_before" ]
  # forward-only migration caveat surfaced
  [[ "$output" == *"migration"* || "$output" == *"down-migration"* || "$output" == *"forward-only"* ]]
}

@test "run_upgrade aborts when the download fails and leaves the existing binary unchanged" {
  # A checksum mismatch (tampered download) or network failure must not leave a
  # partial upgrade. The original binary must be intact and service untouched.
  _stage_installed
  local bin_before
  bin_before="$(cat "$(layout_bin)/arbitron")"

  # curl always fails for the release binary (simulates network error or
  # checksum mismatch via download_binary calling verify_download internally).
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "deadbeef  arbitron_9.9.9_linux_amd64\n" > "$dest" ;;  # mismatch
      *releases*)   printf "TAMPERED" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
  export ARBITRON_VERSION=9.9.9
  run run_upgrade
  [ "$status" -ne 0 ]
  # Original binary must be unchanged.
  [ "$(cat "$(layout_bin)/arbitron")" = "$bin_before" ]
  # Service must NOT have been restarted (no restart call in mock log).
  ! grep -q "systemctl --user restart arbitron" "$MOCK_LOG"
}

@test "run_upgrade on macOS kicks the launchd agent to restart" {
  # The macOS service_restart path (launchctl kickstart) is separate from the
  # Linux path; verify the upgrade lifecycle exercises it correctly.
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  install_mock recording-fake launchctl
  _stage_installed
  export ARBITRON_VERSION=9.9.9
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_9.9.9_darwin_arm64\n" "$(printf NEW | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *releases*)   printf "NEW" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
  run run_upgrade
  [ "$status" -eq 0 ]
  grep -q "kickstart" "$MOCK_LOG"
}

# --- reconfigure -------------------------------------------------------------

@test "run_reconfigure rewrites config + restarts without touching data or master key" {
  _stage_installed
  # seed a data file + capture the master key
  mkdir -p "$(layout_data)"
  printf 'rows' >"$(layout_data)/arbitron.db"
  local key_before
  key_before="$(grep '^ARBITRON_MASTER_KEY=' "$(layout_secrets_env)" | cut -d= -f2-)"

  export ARBITRON_PORT=9999
  install_fake curl 'echo 200'
  run run_reconfigure
  [ "$status" -eq 0 ]
  grep -qE 'port: 9999' "$(layout_config)"
  grep -q "systemctl --user restart arbitron" "$MOCK_LOG"
  # data preserved
  [ "$(cat "$(layout_data)/arbitron.db")" = "rows" ]
  # master key preserved
  local key_after
  key_after="$(grep '^ARBITRON_MASTER_KEY=' "$(layout_secrets_env)" | cut -d= -f2-)"
  [ "$key_after" = "$key_before" ]
}

@test "run_reconfigure on macOS kicks the launchd agent to restart" {
  # Parity: reconfigure must call launchctl kickstart on macOS, just as it
  # calls systemctl restart on Linux.
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  install_mock recording-fake launchctl
  _stage_installed
  export ARBITRON_PORT=7777
  install_fake curl 'echo 200'
  run run_reconfigure
  [ "$status" -eq 0 ]
  grep -qE 'port: 7777' "$(layout_config)"
  grep -q "kickstart" "$MOCK_LOG"
}

# --- uninstall ---------------------------------------------------------------

@test "run_uninstall removes service+binary+config and KEEPS data by default" {
  _stage_installed
  mkdir -p "$(layout_data)"
  printf 'rows' >"$(layout_data)/arbitron.db"

  ARBITRON_REMOVE_DATA=no run run_uninstall
  [ "$status" -eq 0 ]
  grep -q "systemctl --user disable --now arbitron" "$MOCK_LOG"
  [ ! -f "$(layout_bin)/arbitron" ]
  [ ! -f "$(layout_config)" ]
  [ ! -f "$HOME/.config/systemd/user/arbitron.service" ]
  # data kept
  [ -f "$(layout_data)/arbitron.db" ]
}

@test "run_uninstall removes data only on explicit yes" {
  _stage_installed
  mkdir -p "$(layout_data)"
  printf 'rows' >"$(layout_data)/arbitron.db"

  ARBITRON_REMOVE_DATA=yes run run_uninstall
  [ "$status" -eq 0 ]
  [ ! -d "$(layout_data)" ]
}
