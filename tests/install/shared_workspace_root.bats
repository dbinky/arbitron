#!/usr/bin/env bats
#
# Remote Runners Phase 2 Task 11 / RR-07: the shared (Linux) server's workspace
# root is the cross-OS canonical /opt/arbitron/workspaces for NEW installs;
# existing servers keep theirs.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake install
  install_mock recording-fake chown
  export ARBITRON_INSTALL_TYPE=shared
}

teardown() {
  install_teardown_sandbox
}

@test "new shared installs default workspace.path to /opt/arbitron/workspaces" {
  [ "$ARB_WORKSPACES" = "/opt/arbitron/workspaces" ]
  prompt_shared_workspace_location </dev/null
  [ "$SCFG_WORKSPACE_DIR" = "/opt/arbitron/workspaces" ]
  [ "$(layout_workspaces)" = "/opt/arbitron/workspaces" ]
}

@test "create_system_layout creates /opt/arbitron 0755 root and workspaces 0750 for the service user" {
  export ARB_SVC_USER=arbitron SCFG_WORKSPACE_DIR=/opt/arbitron/workspaces
  create_system_layout
  grep -q "install -d -m 0755 -o root -g root /opt/arbitron$" "$MOCK_LOG"
  grep -q "install -d -m 0750 -o arbitron -g arbitron /opt/arbitron/workspaces$" "$MOCK_LOG"
}

@test "write_shared_config writes the chosen workspace root" {
  export ARB_ETC="$TEST_SANDBOX/etc" SCFG_WORKSPACE_DIR=/opt/arbitron/workspaces
  mkdir -p "$ARB_ETC"
  write_shared_config
  grep -qx '  path: "/opt/arbitron/workspaces"' "$ARB_ETC/config.yaml"
}

@test "the systemd unit can write the workspace root (ProtectSystem=strict)" {
  export SCFG_WORKSPACE_DIR=/opt/arbitron/workspaces
  export ARB_SYSTEMD_UNIT_PATH="$TEST_SANDBOX/arbitron.service"
  write_systemd_unit
  grep -qxF "ReadWritePaths=$ARB_LIB $ARB_LOG /opt/arbitron/workspaces" "$ARB_SYSTEMD_UNIT_PATH"
}

@test "a workspace root under the data root is not listed twice" {
  export SCFG_WORKSPACE_DIR="$ARB_LIB/workspaces"
  export ARB_SYSTEMD_UNIT_PATH="$TEST_SANDBOX/arbitron.service"
  write_systemd_unit
  grep -qxF "ReadWritePaths=$ARB_LIB $ARB_LOG" "$ARB_SYSTEMD_UNIT_PATH"
}

@test "reconfigure keeps an existing server workspace.path" {
  export ARB_ETC="$TEST_SANDBOX/etc"
  mkdir -p "$ARB_ETC"
  printf 'workspace:\n  path: "/var/lib/arbitron/workspaces"\n' >"$ARB_ETC/config.yaml"
  prompt_shared_workspace_location </dev/null
  [ "$SCFG_WORKSPACE_DIR" = "/var/lib/arbitron/workspaces" ]
}

@test "per-user (Local) installs keep ~/.arbitron/workspaces" {
  export ARBITRON_INSTALL_TYPE=local
  [ "$(layout_workspaces)" = "$HOME/.arbitron/workspaces" ]
}
