#!/usr/bin/env bats
#
# P2 Task 1: Shared (Linux system) install skeleton — elevation gate, dedicated
# service account, /etc + /var/lib + /var/log layout with ownership/modes, and
# /usr/local/bin/arbitron. Tests source install.sh with the P1 lib-only guard
# and mock every privileged command via the recording fake on the PATH.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  # Shared path uses the system layout.
  export ARB_INSTALL_MODE=shared
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  # Privileged commands are recording fakes; getent/id behaviour is per-test.
  # NB: do NOT mock chmod here — install_fake (used by test 10 to stage a curl
  # fake) shells out to `chmod +x`, so chmod must stay the real coreutils binary.
  # create_system_layout uses `install -d` (mocked) rather than chmod in shared
  # mode, so a real chmod does not weaken the layout assertions.
  install_mock recording-fake sudo useradd userdel install chown getent systemctl
  hash -r   # drop cached paths to the real install/chown so the mocks win
}

teardown() {
  install_teardown_sandbox
}

# --- elevation gate ----------------------------------------------------------

@test "require_root dies when not elevated and sudo is unavailable" {
  export ARBITRON_UID=1000
  # Remove sudo from the mock bin so it is genuinely absent.
  rm -f "$MOCK_BIN/sudo"
  run require_root
  [ "$status" -ne 0 ]
  [[ "$output" == *"requires root"* ]] || [[ "$output" == *"sudo"* ]]
}

@test "require_root dies with sudo guidance when not elevated but sudo present" {
  export ARBITRON_UID=1000
  run require_root
  [ "$status" -ne 0 ]
  [[ "$output" == *"sudo"* ]]
}

@test "require_root passes when elevated (uid 0)" {
  export ARBITRON_UID=0
  run require_root
  [ "$status" -eq 0 ]
}

# --- service account ---------------------------------------------------------

@test "ensure_service_account creates a system, no-login account when absent" {
  export MOCK_GETENT_RC=2   # getent passwd <user> -> absent
  run ensure_service_account
  [ "$status" -eq 0 ]
  grep -q "useradd" "$MOCK_LOG"
  grep -q -- "--system" "$MOCK_LOG"
  grep -qE -- "--shell (/usr/sbin/nologin|/bin/false)" "$MOCK_LOG"
  grep -q -- "--home-dir /var/lib/arbitron" "$MOCK_LOG"
  grep -qE "useradd .*\barbitron\b" "$MOCK_LOG"
}

@test "ensure_service_account reuses an existing account (no useradd)" {
  export MOCK_GETENT_RC=0   # getent passwd <user> -> present
  run ensure_service_account
  [ "$status" -eq 0 ]
  ! grep -q "useradd" "$MOCK_LOG"
  [[ "$output" == *"already exists"* ]]
}

@test "ensure_service_account honours a custom service account name" {
  export ARB_SVC_USER=svc-arb
  export MOCK_GETENT_RC=2
  run ensure_service_account
  [ "$status" -eq 0 ]
  grep -qE "getent passwd svc-arb" "$MOCK_LOG"
  grep -qE "useradd .*\bsvc-arb\b" "$MOCK_LOG"
}

# --- system layout -----------------------------------------------------------

@test "create_system_layout creates etc/lib/log/workspaces/samples dirs" {
  run create_system_layout
  [ "$status" -eq 0 ]
  # install -d (or mkdir) was called for each system dir.
  grep -qF "/etc/arbitron" "$MOCK_LOG"
  grep -qF "/var/lib/arbitron" "$MOCK_LOG"
  grep -qF "/var/lib/arbitron/workspaces" "$MOCK_LOG"
  grep -qF "/var/lib/arbitron/samples" "$MOCK_LOG"
  grep -qF "/var/log/arbitron" "$MOCK_LOG"
}

@test "create_system_layout places samples under SCFG_DATA_DIR when overridden" {
  export SCFG_DATA_DIR="$TEST_SANDBOX/data/custom"
  run create_system_layout
  [ "$status" -eq 0 ]
  grep -qF "$TEST_SANDBOX/data/custom/samples" "$MOCK_LOG"
  grep -qF "$TEST_SANDBOX/data/custom/workspaces" "$MOCK_LOG"
}

# G4: the database and its <db>.model-state.key live directly under the data
# root, so a custom root must itself be service-owned and private — not merely
# the parent that `install -d` leaves root-owned while creating workspaces.
@test "create_system_layout makes a custom SCFG_DATA_DIR itself service-owned 0750" {
  export SCFG_DATA_DIR="$TEST_SANDBOX/data/custom"
  run create_system_layout
  [ "$status" -eq 0 ]
  grep -qxF "install -d -m 0750 -o arbitron -g arbitron $TEST_SANDBOX/data/custom" "$MOCK_LOG"
}

@test "create_system_layout makes data + log dirs service-account owned, etc root" {
  run create_system_layout
  [ "$status" -eq 0 ]
  # /var/lib + /var/log owned by the service account.
  grep -qE "(chown|install) .*arbitron(:arbitron)? .*/var/lib/arbitron" "$MOCK_LOG"
  grep -qE "(chown|install) .*arbitron(:arbitron)? .*/var/log/arbitron" "$MOCK_LOG"
  # /etc/arbitron stays root-owned (no chown to the service user on it).
  ! grep -qE "(chown|install) .*-o arbitron .*/etc/arbitron(\b| )" "$MOCK_LOG"
}

@test "create_system_layout applies 0755 to etc and 0750 to lib" {
  run create_system_layout
  [ "$status" -eq 0 ]
  grep -qE "(chmod 0?755|install .*-m 0?755) .*/etc/arbitron" "$MOCK_LOG"
  grep -qE "(chmod 0?750|install .*-m 0?750) .*/var/lib/arbitron" "$MOCK_LOG"
}

@test "create_system_layout falls back to mkdir+chmod+chown when install is absent" {
  # On minimal systems (Alpine / BusyBox containers) GNU install may be absent.
  # _make_dir must fall back to mkdir + chmod + chown rather than silently
  # failing. Redirect system paths into the sandbox so mkdir can actually create
  # dirs, then remove the install mock to force the fallback.
  export ARB_ETC="$TEST_SANDBOX/etc/arbitron"
  export ARB_LIB="$TEST_SANDBOX/var/lib/arbitron"
  export ARB_LOG="$TEST_SANDBOX/var/log/arbitron"
  rm -f "$MOCK_BIN/install"
  hash -r   # clear cached path so require_cmd install re-searches and fails
  run create_system_layout
  [ "$status" -eq 0 ]
  # Directories must actually have been created on disk.
  [ -d "$ARB_ETC" ]
  [ -d "$ARB_LIB" ]
  [ -d "$ARB_LOG" ]
  [ -d "$ARB_LIB/workspaces" ]
  [ -d "$ARB_LIB/samples" ]
  # The fallback path calls chown; install was NOT called.
  grep -q "chown" "$MOCK_LOG"
  ! grep -q "^install " "$MOCK_LOG"
}

# --- binary install ----------------------------------------------------------

@test "install_binary_system aborts without calling install when SHA256 verification fails" {
  # Fail-closed contract: a tampered binary (hash mismatch) must cause
  # install_binary_system to return non-zero and must NEVER invoke `install`
  # for the system binary path. This guards the most critical security
  # invariant of the shared install.
  export ARBITRON_VERSION=1.0.0
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "deadbeef  arbitron_1.0.0_linux_amd64\n" > "$dest" ;;
      *arbitron_1.0.0_linux_amd64*) printf "TAMPERED" > "$dest" ;;
    esac
  '
  run install_binary_system
  [ "$status" -ne 0 ]
  # The mock `install` must NOT have been called for the system binary path.
  ! grep -qF "$ARB_PREFIX_BIN" "$MOCK_LOG"
}

@test "install_binary_system places binary at /usr/local/bin 0755 root" {
  export ARBITRON_VERSION=1.0.0 OS_TOKEN=linux ARCH_TOKEN=amd64
  # curl serves a matching SHA256SUMS + the binary; the canonical fail-closed
  # verify_download gate runs inside fetch_binary_to before placement.
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.0.0_linux_amd64\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *arbitron_1.0.0_linux_amd64*) printf "BIN" > "$dest" ;;
    esac
  '
  run install_binary_system
  [ "$status" -eq 0 ]
  grep -qE "install .*-m 0?755 .*/usr/local/bin/arbitron" "$MOCK_LOG"
  grep -qE "install .*(-o root|root:root) " "$MOCK_LOG"
}

# --- helper reuse + OS guard -------------------------------------------------

@test "run_shared_install reuses P1 detect_os/detect_arch/download_verified" {
  export ARBITRON_UID=0
  export MOCK_GETENT_RC=0
  : >"$TEST_SANDBOX/calls"
  detect_os()          { echo "linux"; echo detect_os >>"$TEST_SANDBOX/calls"; }
  detect_arch()        { echo "amd64"; echo detect_arch >>"$TEST_SANDBOX/calls"; }
  resolve_dependencies(){ echo resolve_dependencies >>"$TEST_SANDBOX/calls"; }
  download_verified()  { echo download_verified >>"$TEST_SANDBOX/calls"; printf 'BIN' >"$3"; }
  # install_binary_system fetches via curl; short-circuit it to the verify call
  # so this test proves reuse without staging the full download mock chain.
  install_binary_system() { local t; t="$(mktemp)"; download_verified url sha "$t"; rm -f "$t"; }
  # Stop after the skeleton steps so Task 2/3 stages do not run here.
  write_shared_config(){ echo write_shared_config >>"$TEST_SANDBOX/calls"; }
  write_shared_env()   { :; }
  run_shared_qa()      { :; }
  write_systemd_unit() { :; }
  enable_systemd_unit(){ :; }
  health_check_shared(){ :; }
  print_shared_summary(){ :; }
  detect_state()       { echo Fresh; }
  run run_shared_install
  [ "$status" -eq 0 ]
  grep -q "detect_arch" "$TEST_SANDBOX/calls"
  grep -q "resolve_dependencies" "$TEST_SANDBOX/calls"
  grep -q "download_verified" "$TEST_SANDBOX/calls"
}

@test "macOS can never enter the Shared install path" {
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  run select_install_type
  [ "$status" -eq 0 ]
  [ "$output" = "Local" ]
}
