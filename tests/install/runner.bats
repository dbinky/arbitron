#!/usr/bin/env bats
#
# Remote Runners Phase 2 Task 11: the macOS runner install scenario.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake launchctl
  # RR-07: macOS TMPDIR is under /var -> /private/var; roots built from the
  # sandbox must use the physical path or the symlink refusal would trip.
  TEST_SANDBOX="$(cd "$TEST_SANDBOX" && pwd -P)"
}

teardown() {
  install_teardown_sandbox
}

_runner_plist() { printf '%s/Library/LaunchAgents/dev.arbitron.runner.plist\n' "$HOME"; }

# curl fake serving a runner artifact + matching SHA256SUMS for version $1, os $2, arch $3.
_stage_runner_release() {
  local v="$1" os="$2" arch="$3" sum
  sum="$(printf RUNNERBIN | shasum -a 256 | cut -d' ' -f1)"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf '%s  arbitron-runner_${v}_${os}_${arch}\n' '${sum}' > \"\$dest\" ;;
      *) printf RUNNERBIN > \"\$dest\" ;;
    esac
  "
}

@test "select_install_type yields Runner on macOS when ARBITRON_INSTALL_TYPE=runner" {
  ARBITRON_INSTALL_TYPE=runner run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Runner" ]
}

@test "select_install_type keeps Local on macOS for anything else" {
  ARBITRON_INSTALL_TYPE=shared run select_install_type
  [ "$output" = "Local" ]
  run select_install_type
  [ "$output" = "Local" ]
}

@test "select_install_type refuses a runner on Linux (remote runners are macOS-only)" {
  ARBITRON_UNAME_S=Linux ARBITRON_INSTALL_TYPE=runner run select_install_type
  [ "$status" -ne 0 ]
  [[ "$output" == *"macOS-only"* ]]
}

@test "parse_args --runner selects the runner install type" {
  unset ARBITRON_INSTALL_TYPE
  parse_args --runner
  [ "$ARBITRON_INSTALL_TYPE" = "runner" ]
  run select_install_type
  [ "$output" = "Runner" ]
}

@test "parse_args rejects unknown flags" {
  run parse_args --bogus
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown argument: --bogus"* ]]
}

@test "arbitron_runner_artifact_name is arbitron-runner_<v>_<os>_<arch>" {
  run arbitron_runner_artifact_name 1.2.3 darwin arm64
  [ "$output" = "arbitron-runner_1.2.3_darwin_arm64" ]
}

@test "download_runner_binary fetches the runner artifact, verifies it and installs 0755" {
  export ARBITRON_VERSION="1.2.3"
  _stage_runner_release 1.2.3 darwin arm64
  run download_runner_binary
  [ "$status" -eq 0 ]
  mock_log_has "arbitron-runner_1.2.3_darwin_arm64"
  [ -x "$(runner_bin)/arbitron-runner" ]
  [ "$(file_mode "$(runner_bin)/arbitron-runner")" = "755" ]
  [ ! -e "$HOME/.arbitron/bin/arbitron" ]
}

@test "download_runner_binary aborts on checksum mismatch and installs nothing" {
  export ARBITRON_VERSION="1.2.3"
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf "%s  arbitron-runner_1.2.3_darwin_arm64\n" deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef > "$dest" ;;
      *) printf TAMPERED > "$dest" ;;
    esac
  '
  run download_runner_binary
  [ "$status" -ne 0 ]
  [[ "$output" == *"verification failed"* ]]
  [ ! -e "$(runner_bin)/arbitron-runner" ]
}

@test "the server download path still fetches the server artifact" {
  export ARBITRON_VERSION="1.2.3"
  local sum
  sum="$(printf BIN | shasum -a 256 | cut -d' ' -f1)"
  install_fake curl "
    for a in \"\$@\"; do prev=\"\$last\"; last=\"\$a\"; if [ \"\$prev\" = \"-o\" ]; then dest=\"\$a\"; fi; done
    case \"\$*\" in
      *SHA256SUMS.sig*|*SHA256SUMS.pem*) exit 22 ;;
      *SHA256SUMS*) printf '%s  arbitron_1.2.3_darwin_arm64\n' '${sum}' > \"\$dest\" ;;
      *) printf BIN > \"\$dest\" ;;
    esac
  "
  run fetch_binary_to "$TEST_SANDBOX/srv/arbitron"
  [ "$status" -eq 0 ]
  mock_log_has "arbitron_1.2.3_darwin_arm64"
}

@test "the runner workspace root prompt defaults to /opt/arbitron/workspaces" {
  [ "$ARBITRON_RUNNER_DEFAULT_WORKSPACE_ROOT" = "/opt/arbitron/workspaces" ]
  # Side-effect free: never touch the real /opt from a test.
  grep -q 'prompt_default RUNNER_WORKSPACE_ROOT .*"\$ARBITRON_RUNNER_DEFAULT_WORKSPACE_ROOT")' "$INSTALL_SH"
}

@test "ensure_runner_workspace_root uses an existing or user-creatable absolute root without sudo" {
  install_fake sudo 'printf "sudo %s\n" "$*" >>"$MOCK_LOG"; exit 1'
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/opt/arbitron/workspaces"
  ensure_runner_workspace_root
  [ "$CFG_RUNNER_WORKSPACE_ROOT" = "$TEST_SANDBOX/opt/arbitron/workspaces" ]
  [ -d "$TEST_SANDBOX/opt/arbitron/workspaces" ]
  ! grep -q "^sudo" "$MOCK_LOG"
}

@test "ensure_runner_workspace_root rejects a relative root" {
  export ARBITRON_RUNNER_WORKSPACE_ROOT="relative/ws"
  run ensure_runner_workspace_root
  [ "$status" -ne 0 ]
  [[ "$output" == *"must be absolute"* ]]
}

@test "ensure_runner_workspace_root refuses a root with a symlinked path component" {
  mkdir -p "$TEST_SANDBOX/real"
  ln -s "$TEST_SANDBOX/real" "$TEST_SANDBOX/link"
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/link/arbitron/workspaces"
  run ensure_runner_workspace_root
  [ "$status" -ne 0 ]
  [[ "$output" == *"symlink"* ]]
  [[ "$output" == *"$TEST_SANDBOX/link"* ]]
  [ ! -e "$TEST_SANDBOX/real/arbitron" ]
}

@test "ensure_runner_workspace_root creates the root with one approved sudo install -d" {
  if [ "$(id -u)" -eq 0 ]; then skip "root can create any directory"; fi
  mkdir -p "$TEST_SANDBOX/locked"
  chmod 0500 "$TEST_SANDBOX/locked"
  # Fake sudo: record the argv, then perform the directory creation it asked for.
  install_fake sudo 'printf "sudo %s\n" "$*" >>"$MOCK_LOG"; chmod 0700 "'"$TEST_SANDBOX"'/locked"; for a in "$@"; do last="$a"; done; mkdir -p "$last"'
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/locked/opt/arbitron/workspaces"
  export ARBITRON_RUNNER_ROOT_SUDO=yes
  ensure_runner_workspace_root
  [ "$CFG_RUNNER_WORKSPACE_ROOT" = "$TEST_SANDBOX/locked/opt/arbitron/workspaces" ]
  [ "$(grep -c '^sudo ' "$MOCK_LOG")" -eq 1 ]
  grep -q "^sudo install -d -o $(id -un) -g staff -m 0750 $TEST_SANDBOX/locked/opt/arbitron/workspaces$" "$MOCK_LOG"
}

@test "ensure_runner_workspace_root falls back to the runner root (chat-only) when sudo is declined" {
  if [ "$(id -u)" -eq 0 ]; then skip "root can create any directory"; fi
  mkdir -p "$TEST_SANDBOX/locked"
  chmod 0500 "$TEST_SANDBOX/locked"
  install_fake sudo 'printf "sudo %s\n" "$*" >>"$MOCK_LOG"; exit 0'
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/locked/opt/arbitron/workspaces"
  export ARBITRON_RUNNER_ROOT_SUDO=no
  ensure_runner_workspace_root 2>"$TEST_SANDBOX/err"
  chmod 0700 "$TEST_SANDBOX/locked"
  [ "$CFG_RUNNER_WORKSPACE_ROOT" = "$HOME/.arbitron-runner/workspaces" ]
  [ -d "$HOME/.arbitron-runner/workspaces" ]
  ! grep -q "^sudo" "$MOCK_LOG"
  grep -q "chat_only" "$TEST_SANDBOX/err"
  grep -q "RR-07" "$TEST_SANDBOX/err"
}

@test "ensure_runner_workspace_root falls back to the runner root (chat-only) when sudo fails" {
  if [ "$(id -u)" -eq 0 ]; then skip "root can create any directory"; fi
  mkdir -p "$TEST_SANDBOX/locked"
  chmod 0500 "$TEST_SANDBOX/locked"
  install_fake sudo 'printf "sudo %s\n" "$*" >>"$MOCK_LOG"; exit 1'
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/locked/opt/arbitron/workspaces"
  export ARBITRON_RUNNER_ROOT_SUDO=yes
  ensure_runner_workspace_root 2>"$TEST_SANDBOX/err"
  chmod 0700 "$TEST_SANDBOX/locked"
  [ "$CFG_RUNNER_WORKSPACE_ROOT" = "$HOME/.arbitron-runner/workspaces" ]
  [ "$(grep -c '^sudo ' "$MOCK_LOG")" -eq 1 ]
  grep -q "chat_only" "$TEST_SANDBOX/err"
}

@test "write_runner_config writes the skeleton the runner reads, mode 0600" {
  CFG_RUNNER_WORKSPACE_ROOT="/opt/arbitron/workspaces"
  write_runner_config
  local cfg
  cfg="$(runner_config)"
  [ "$(file_mode "$cfg")" = "600" ]
  grep -qx 'workspace_root: "/opt/arbitron/workspaces"' "$cfg"
  grep -qx "scratch_root: \"$HOME/.arbitron-runner/scratch\"" "$cfg"
  grep -qx "spool_dir: \"$HOME/.arbitron-runner/spool\"" "$cfg"
  grep -qx 'heartbeat_seconds: 5' "$cfg"
  grep -qx 'probe_interval_seconds: 60' "$cfg"
  grep -qx 'output_buffer_frames: 4096' "$cfg"
  grep -qx 'claude_binary: ""' "$cfg"
  grep -qx '  connection_ref: ""' "$cfg"
  grep -qx '  families: \[\]' "$cfg"
}

@test "write_runner_config preserves an existing config" {
  mkdir -p "$(runner_home)"
  printf 'workspace_root: "/custom"\n' >"$(runner_config)"
  CFG_RUNNER_WORKSPACE_ROOT="/other"
  write_runner_config
  grep -qx 'workspace_root: "/custom"' "$(runner_config)"
}

@test "write_runner_env writes server URL and key at 0600 with commented optional keys" {
  export ARBITRON_RUNNER_SERVER_URL="https://arbitron.example.com"
  export ARBITRON_RUNNER_KEY="ark_testsecret"
  write_runner_env
  local envf
  envf="$(runner_env)"
  [ "$(file_mode "$envf")" = "600" ]
  grep -qx 'ARBITRON_SERVER_URL=https://arbitron.example.com' "$envf"
  grep -qx 'ARBITRON_RUNNER_KEY=ark_testsecret' "$envf"
  grep -q '^# LUNAROUTE_API_KEY=' "$envf"
  grep -q '^# ARBITRON_EXTRA_HEADERS=' "$envf"
}

@test "write_runner_env never overwrites an existing runner.env and re-tightens it to 0600" {
  mkdir -p "$(runner_home)"
  printf 'ARBITRON_RUNNER_KEY=ark_keep\n' >"$(runner_env)"
  chmod 0644 "$(runner_env)"
  export ARBITRON_RUNNER_KEY="ark_other"
  write_runner_env
  grep -qx 'ARBITRON_RUNNER_KEY=ark_keep' "$(runner_env)"
  [ "$(file_mode "$(runner_env)")" = "600" ]
}

@test "render_runner_launchd_plist runs arbitron-runner run --config with RunAtLoad and KeepAlive" {
  render_runner_launchd_plist
  local p
  p="$(_runner_plist)"
  [ -f "$p" ]
  grep -qF "<string>dev.arbitron.runner</string>" "$p"
  grep -qF "<string>$(runner_bin)/arbitron-runner</string>" "$p"
  grep -qF "<string>run</string>" "$p"
  grep -qF "<string>--config</string>" "$p"
  grep -qF "<string>$(runner_config)</string>" "$p"
  grep -A1 "<key>RunAtLoad</key>" "$p" | grep -qF "<true/>"
  grep -A1 "<key>KeepAlive</key>" "$p" | grep -qF "<true/>"
  grep -A1 "StandardOutPath"  "$p" | grep '<string>' | grep -qE '<string>/'
  grep -A1 "StandardErrorPath" "$p" | grep '<string>' | grep -qE '<string>/'
}

@test "the runner plist never contains secrets" {
  export ARBITRON_RUNNER_KEY="ark_neverinplist"
  write_runner_env
  render_runner_launchd_plist
  ! grep -q "ark_neverinplist" "$(_runner_plist)"
  ! grep -q "ARBITRON_RUNNER_KEY" "$(_runner_plist)"
}

@test "the runner plist is valid XML via plutil when available" {
  render_runner_launchd_plist
  if command -v plutil >/dev/null 2>&1; then
    run plutil -lint "$(_runner_plist)"
    [ "$status" -eq 0 ]
  else
    skip "plutil not available"
  fi
}

@test "register_runner_service_macos bootstraps the agent in the user's gui domain" {
  export ARBITRON_UID=501
  register_runner_service_macos
  grep -q "launchctl bootout gui/501/dev.arbitron.runner" "$MOCK_LOG"
  grep -q "launchctl bootstrap gui/501 $(_runner_plist)" "$MOCK_LOG"
  grep -q "launchctl kickstart -k gui/501/dev.arbitron.runner" "$MOCK_LOG"
}

@test "print_runner_guidance explains runner.env, the workspace root and the login-session start (RR-05)" {
  CFG_RUNNER_WORKSPACE_ROOT="/opt/arbitron/workspaces"
  run print_runner_guidance
  [ "$status" -eq 0 ]
  [[ "$output" == *"runner.env"* ]]
  [[ "$output" == *"ARBITRON_RUNNER_KEY"* ]]
  [[ "$output" == *"/opt/arbitron/workspaces"* ]]
  [[ "$output" == *"workspace.path"* ]]
  [[ "$output" == *"FileVault"* ]]
  [[ "$output" == *"fdesetup authrestart"* ]]
  [[ "$output" == *"offline"* ]]
  [[ "$output" != *"automatic login"* ]]
  [[ "$output" != *"auto-login"* ]]
}

@test "run_runner_install installs binary, config, env and agent without touching the server layout" {
  export ARBITRON_VERSION="1.2.3"
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/srv/ws"
  export ARBITRON_RUNNER_SERVER_URL="https://arbitron.example.com"
  export ARBITRON_RUNNER_KEY="ark_install"
  _stage_runner_release 1.2.3 darwin arm64
  run run_runner_install
  [ "$status" -eq 0 ]
  [ -x "$(runner_bin)/arbitron-runner" ]
  [ -f "$(runner_config)" ]
  [ "$(file_mode "$(runner_env)")" = "600" ]
  [ -f "$(_runner_plist)" ]
  [ -d "$(runner_home)/spool" ]
  [ -d "$(runner_home)/scratch" ]
  [ "$(file_mode "$(runner_home)")" = "700" ]
  grep -qx "workspace_root: \"$TEST_SANDBOX/srv/ws\"" "$(runner_config)"
  grep -q "launchctl bootstrap" "$MOCK_LOG"
  [ ! -e "$HOME/Library/LaunchAgents/dev.arbitron.plist" ]
  [ ! -e "$HOME/.arbitron" ]
}

@test "detect_runner_state is Fresh without an install and honours ARBITRON_ACTION" {
  run detect_runner_state
  [ "$output" = "Fresh" ]
  ARBITRON_ACTION=upgrade run detect_runner_state
  [ "$output" = "Upgrade" ]
  ARBITRON_ACTION=uninstall run detect_runner_state
  [ "$output" = "Uninstall" ]
  ARBITRON_ACTION=reconfigure run detect_runner_state
  [ "$status" -ne 0 ]
}

@test "run_runner_upgrade swaps the binary and restarts, preserving config and env" {
  export ARBITRON_VERSION="1.2.3"
  mkdir -p "$(runner_home)"
  printf 'workspace_root: "/keep"\n' >"$(runner_config)"
  printf 'ARBITRON_RUNNER_KEY=ark_keep\n' >"$(runner_env)"
  _stage_runner_release 1.2.3 darwin arm64
  run run_runner_upgrade
  [ "$status" -eq 0 ]
  [ -x "$(runner_bin)/arbitron-runner" ]
  grep -qx 'workspace_root: "/keep"' "$(runner_config)"
  grep -qx 'ARBITRON_RUNNER_KEY=ark_keep' "$(runner_env)"
  grep -q "launchctl kickstart -k gui/.*/dev.arbitron.runner" "$MOCK_LOG"
}

@test "run_runner_uninstall removes the agent and binary and keeps secrets by default" {
  render_runner_launchd_plist
  mkdir -p "$(runner_bin)"
  : >"$(runner_bin)/arbitron-runner"
  printf 'ARBITRON_RUNNER_KEY=ark_keep\n' >"$(runner_env)"
  export ARBITRON_REMOVE_DATA=no
  run run_runner_uninstall
  [ "$status" -eq 0 ]
  [ ! -f "$(_runner_plist)" ]
  [ ! -e "$(runner_bin)/arbitron-runner" ]
  [ -f "$(runner_env)" ]
  grep -q "bootout gui/.*/dev.arbitron.runner" "$MOCK_LOG"
}

@test "run_runner_uninstall with REMOVE_DATA=yes deletes ~/.arbitron-runner" {
  render_runner_launchd_plist
  printf 'x\n' >"$(runner_env)"
  export ARBITRON_REMOVE_DATA=yes
  run run_runner_uninstall
  [ "$status" -eq 0 ]
  [ ! -e "$(runner_home)" ]
}

@test "main --runner installs operator inputs through the real dispatcher" {
  export ARBITRON_VERSION="1.2.3"
  export ARBITRON_RUNNER_WORKSPACE_ROOT="$TEST_SANDBOX/server/workspaces"
  export ARBITRON_RUNNER_SERVER_URL="https://server.example.com"
  export ARBITRON_RUNNER_KEY="ark_from_operator"
  _stage_runner_release 1.2.3 darwin arm64
  run env -u ARBITRON_INSTALL_LIB_ONLY bash "$INSTALL_SH" --runner
  [ "$status" -eq 0 ]
  grep -qx "workspace_root: \"$TEST_SANDBOX/server/workspaces\"" "$(runner_config)"
  grep -qx 'ARBITRON_SERVER_URL=https://server.example.com' "$(runner_env)"
  grep -qx 'ARBITRON_RUNNER_KEY=ark_from_operator' "$(runner_env)"
  grep -q "launchctl bootstrap gui/.* $(_runner_plist)" "$MOCK_LOG"
  [ -x "$(runner_bin)/arbitron-runner" ]
  [ ! -e "$HOME/.arbitron" ]
}

@test "main --runner on Linux dies before writing anything" {
  run env -u ARBITRON_INSTALL_LIB_ONLY ARBITRON_UNAME_S=Linux bash "$INSTALL_SH" --runner
  [ "$status" -ne 0 ]
  [[ "$output" == *"macOS-only"* ]]
  [ ! -e "$HOME/.arbitron-runner" ]
}
