#!/usr/bin/env bats
#
# Task 5: cross-cutting security scenarios (0600 secrets, no-secret-logging,
# checksum enforcement, loopback-only bind).

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_PORT=8080 ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no ARBITRON_DATA_DIR="$HOME/.arbitron/data"
  export ARBITRON_INSTALL_OPENCODE=no ARBITRON_VERSION=1.0.0
  export ARBITRON_SKIP_CLAUDE_LOGIN=1
  export ARBITRON_HEALTH_RETRIES=2 ARBITRON_HEALTH_SLEEP=0
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64 USER=tester
  export OS_TOKEN=linux ARCH_TOKEN=amd64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

_good_curl() {
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.0.0_linux_amd64\n" "$(printf BIN | sha256sum | cut -d" " -f1)" > "$dest" ;;
      *releases*)   printf "BIN" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
}

@test "secrets env is 0600 and the master key never appears in run output" {
  install_mock recording-fake brew npm git gh systemctl loginctl
  install_fake node 'echo v20.0.0'
  install_fake claude 'echo 1.2.3'
  _good_curl
  run run_fresh
  [ "$status" -eq 0 ]
  local perms
  perms="$(stat -f '%Lp' "$(layout_secrets_env)" 2>/dev/null || stat -c '%a' "$(layout_secrets_env)")"
  [ "$perms" = "600" ]
  local key
  key="$(grep '^ARBITRON_MASTER_KEY=' "$(layout_secrets_env)" | cut -d= -f2-)"
  [ -n "$key" ]
  [[ "$output" != *"$key"* ]]
}

@test "a binary checksum mismatch aborts and leaves no binary or service" {
  install_mock recording-fake brew npm git gh systemctl loginctl
  install_fake node 'echo v20.0.0'
  install_fake claude 'echo 1.2.3'
  # SHA256SUMS advertises a bogus digest -> download_verified must abort.
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  arbitron_1.0.0_linux_amd64\n" > "$dest" ;;
      *releases*)   printf "BIN" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
  run run_fresh
  [ "$status" -ne 0 ]
  [ ! -e "$(layout_bin)/arbitron" ]
  [ ! -f "$HOME/.config/systemd/user/arbitron.service" ]
}

@test "local mode always writes a loopback server.host" {
  write_config_yaml
  grep -qE 'host: "127\.0\.0\.1"' "$(layout_config)"
  ! grep -qE 'host: "0\.0\.0\.0"' "$(layout_config)"
}

@test "skip-permissions defaults closed (false) when not explicitly enabled" {
  run_config_qa </dev/null
  write_config_yaml
  grep -qE 'skippermissions: false' "$(layout_config)"
}

@test "unsupported arch aborts (input validation)" {
  ARBITRON_UNAME_M=mips run detect_arch
  [ "$status" -ne 0 ]
}
