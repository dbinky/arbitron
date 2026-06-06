#!/usr/bin/env bats
#
# Task 5: end-to-end "fresh local install" smoke tests with everything mocked,
# plus an idempotent re-run check.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  # Non-interactive Q&A.
  export ARBITRON_PORT=8080
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_DATA_DIR="$HOME/.arbitron/data"
  export ARBITRON_INSTALL_OPENCODE=no
  export ARBITRON_BOOTSTRAP_BREW=yes
  export ARBITRON_VERSION=1.0.0
  export ARBITRON_SKIP_CLAUDE_LOGIN=1
  export ARBITRON_HEALTH_RETRIES=2 ARBITRON_HEALTH_SLEEP=0
}

teardown() {
  install_teardown_sandbox
}

# Stage all external tools as recording fakes that satisfy the install. A shared
# curl fake serves the binary, SHA256SUMS and the health endpoint.
_stage_externals() {
  install_mock recording-fake brew npm git gh launchctl systemctl loginctl
  install_fake node 'echo v20.0.0'
  install_fake claude 'echo 1.2.3'
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.0.0_%s_%s\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" "$OS_TOKEN" "$ARCH_TOKEN" > "$dest" ;;
      *releases*)   printf "BIN" > "$dest" ;;
      *)            echo 200 ;;   # health check
    esac
  '
}

_assert_artifacts() {
  local port="$1"
  # config.yaml: absolute paths, loopback
  grep -qE 'host: "127\.0\.0\.1"' "$(layout_config)"
  grep -qE "port: ${port}" "$(layout_config)"
  grep -qF "$HOME/.arbitron/data/arbitron.db" "$(layout_config)"
  # secrets env 0600 + 32-byte base64 master key
  local perms
  perms="$(stat -f '%Lp' "$(layout_secrets_env)" 2>/dev/null || stat -c '%a' "$(layout_secrets_env)")"
  [ "$perms" = "600" ]
  local key nbytes
  key="$(grep '^ARBITRON_MASTER_KEY=' "$(layout_secrets_env)" | cut -d= -f2-)"
  nbytes="$(printf '%s' "$key" | base64 --decode 2>/dev/null | wc -c | tr -d ' ')"
  [ "$nbytes" = "32" ]
  # binary installed + executable
  [ -x "$(layout_bin)/arbitron" ]
}

@test "fresh macOS install produces all artifacts and passes health check" {
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  export OS_TOKEN=darwin ARCH_TOKEN=arm64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  _stage_externals
  run run_fresh
  [ "$status" -eq 0 ]
  _assert_artifacts 8080
  # LaunchAgent loaded
  [ -f "$HOME/Library/LaunchAgents/dev.arbitron.plist" ]
  mock_log_has "launchctl"
  [[ "$output" == *"http://127.0.0.1:8080"* ]]
}

@test "fresh Linux install produces all artifacts and registers systemd --user" {
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64 USER=tester
  export OS_TOKEN=linux ARCH_TOKEN=amd64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  _stage_externals
  run run_fresh
  [ "$status" -eq 0 ]
  _assert_artifacts 8080
  [ -f "$HOME/.config/systemd/user/arbitron.service" ]
  mock_log_has "systemctl --user enable --now arbitron"
  mock_log_has "loginctl enable-linger tester"
}

@test "idempotent re-run detects the existing install and routes to upgrade" {
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64 USER=tester
  export OS_TOKEN=linux ARCH_TOKEN=amd64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  _stage_externals
  run_fresh >/dev/null 2>&1
  # second run: existing config + service present, default action upgrade.
  run detect_state </dev/null
  [ "$status" -eq 0 ]
  [ "$output" = "Upgrade" ]
}
