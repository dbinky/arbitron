#!/usr/bin/env bats
#
# P2 Task 3: systemd SYSTEM unit + hardening + CAP_NET_BIND_SERVICE + health
# check + shared upgrade/reconfigure/uninstall (incl. optional account removal).

load test_helper

_sandbox_system_paths() {
  export ARB_ETC="$TEST_SANDBOX/etc/arbitron"
  export ARB_LIB="$TEST_SANDBOX/var/lib/arbitron"
  export ARB_LOG="$TEST_SANDBOX/var/log/arbitron"
  export ARB_SYSTEMD_UNIT_PATH="$TEST_SANDBOX/etc/systemd/system/arbitron.service"
  mkdir -p "$ARB_ETC" "$ARB_LIB" "$ARB_LOG" "$(dirname "$ARB_SYSTEMD_UNIT_PATH")"
}

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  export ARB_INSTALL_MODE=shared
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake systemctl userdel
  install_mock recording-install install
  install_mock recording-rm rm
  hash -r   # drop cached paths to the real install/rm so the mocks win
  _sandbox_system_paths
  export ARB_SVC_USER=arbitron
  export BIND_HOST=127.0.0.1
  export PORT=8080
  export TLS_ENABLED=false
  export ARBITRON_UID=0   # lifecycle handlers call require_root
}

teardown() {
  install_teardown_sandbox
}

_unit() { printf '%s\n' "$ARB_SYSTEMD_UNIT_PATH"; }

# --- unit generation ---------------------------------------------------------

@test "unit has User/Group = service account" {
  write_systemd_unit
  grep -qE '^User=arbitron$' "$(_unit)"
  grep -qE '^Group=arbitron$' "$(_unit)"
}

@test "unit references the system EnvironmentFile and WorkingDirectory" {
  write_systemd_unit
  grep -qF "EnvironmentFile=$ARB_ETC/arbitron.env" "$(_unit)"
  grep -qF "WorkingDirectory=$ARB_LIB" "$(_unit)"
}

@test "unit ExecStart passes serve --config the system config" {
  write_systemd_unit
  grep -qF "ExecStart=/usr/local/bin/arbitron serve --config $ARB_ETC/config.yaml" "$(_unit)"
}

@test "unit contains the hardening directives incl ReadWritePaths lib+log" {
  write_systemd_unit
  local u; u="$(_unit)"
  grep -qE '^NoNewPrivileges=yes$' "$u"
  grep -qE '^ProtectSystem=strict$' "$u"
  grep -qE '^ProtectHome=yes$' "$u"
  grep -qF "ReadWritePaths=$ARB_LIB $ARB_LOG" "$u"
}

@test "custom service account renders User/Group=svc-arb" {
  export ARB_SVC_USER=svc-arb
  write_systemd_unit
  grep -qE '^User=svc-arb$' "$(_unit)"
  grep -qE '^Group=svc-arb$' "$(_unit)"
}

@test "privileged port 443 adds CAP_NET_BIND_SERVICE" {
  export PORT=443
  write_systemd_unit
  grep -qF 'AmbientCapabilities=CAP_NET_BIND_SERVICE' "$(_unit)"
}

@test "unprivileged port 8080 omits CAP_NET_BIND_SERVICE" {
  export PORT=8080
  write_systemd_unit
  ! grep -qF 'AmbientCapabilities=CAP_NET_BIND_SERVICE' "$(_unit)"
}

@test "unit written to /etc/systemd/system path 0644 root via install" {
  write_systemd_unit
  grep -qE "install .*-m 0?644 .*arbitron.service" "$MOCK_LOG"
  grep -qE "install .*(-o root|root:root)" "$MOCK_LOG"
}

# --- activation --------------------------------------------------------------

@test "enable_systemd_unit daemon-reloads then enable --now arbitron" {
  enable_systemd_unit
  grep -q "systemctl daemon-reload" "$MOCK_LOG"
  grep -q "systemctl enable --now arbitron" "$MOCK_LOG"
  # order: daemon-reload precedes enable
  local dr en
  dr="$(grep -n 'systemctl daemon-reload' "$MOCK_LOG" | head -n1 | cut -d: -f1)"
  en="$(grep -n 'systemctl enable --now arbitron' "$MOCK_LOG" | head -n1 | cut -d: -f1)"
  [ "$dr" -lt "$en" ]
}

# --- health check ------------------------------------------------------------

@test "health check uses http on a plain port" {
  export TLS_ENABLED=false PORT=8080 BIND_HOST=127.0.0.1
  install_fake curl 'echo 200'
  run health_check_shared
  [ "$status" -eq 0 ]
  grep -qE 'curl .*http://127.0.0.1:8080' "$MOCK_LOG"
}

@test "health check uses https + insecure on the TLS path" {
  export TLS_ENABLED=true PORT=443 BIND_HOST=arbitron.example.com
  install_fake curl 'echo 200'
  run health_check_shared
  [ "$status" -eq 0 ]
  grep -qE 'curl .*https://' "$MOCK_LOG"
  grep -qE 'curl .*(-k|--insecure)' "$MOCK_LOG"
}

@test "health check failure points at journalctl and the log dir" {
  export ARBITRON_HEALTH_RETRIES=2 ARBITRON_HEALTH_SLEEP=0
  install_fake curl 'echo 000'
  run health_check_shared
  [ "$status" -ne 0 ]
  [[ "$output" == *"journalctl -u arbitron"* ]]
}

# --- upgrade -----------------------------------------------------------------

# curl that serves SHA256SUMS, the release artifact, and a 200 health response.
_stage_upgrade_curl() {
  export ARBITRON_VERSION=1.0.0 OS_TOKEN=linux ARCH_TOKEN=amd64
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.0.0_linux_amd64\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *releases*)   printf "BIN" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
}

@test "upgrade swaps the binary, restarts, no config rewrite" {
  write_shared_config() { echo "write_shared_config" >>"$MOCK_LOG"; }
  _stage_upgrade_curl
  export ARBITRON_HEALTH_RETRIES=1 ARBITRON_HEALTH_SLEEP=0
  run upgrade_shared
  [ "$status" -eq 0 ]
  grep -qE "install .*-m 0?755 .*/usr/local/bin/arbitron" "$MOCK_LOG"
  grep -q "systemctl restart arbitron" "$MOCK_LOG"
  ! grep -q "write_shared_config" "$MOCK_LOG"
}

@test "upgrade health check uses the port from the existing config not the env default" {
  # upgrade_shared passes server.port to health_check_shared so the health
  # probe lands on the right port even when PORT env var is set to a stale value.
  _stage_upgrade_curl
  cat >"$ARB_ETC/config.yaml" <<EOF
server:
  host: "127.0.0.1"
  port: 9090
EOF
  export PORT=8080   # stale default — must NOT be the probe port
  export ARBITRON_HEALTH_RETRIES=1 ARBITRON_HEALTH_SLEEP=0
  run upgrade_shared
  [ "$status" -eq 0 ]
  grep -qE 'curl .*:9090' "$MOCK_LOG"
}

@test "upgrade prints the forward-only migration caveat" {
  _stage_upgrade_curl
  export ARBITRON_HEALTH_RETRIES=1 ARBITRON_HEALTH_SLEEP=0
  run upgrade_shared
  shopt -s nocasematch
  [[ "$output" == *"forward-only"* ]] || [[ "$output" == *"down-migration"* ]] || [[ "$output" == *"downgrad"* ]]
  shopt -u nocasematch
}

@test "upgrade is fail-closed — a tampered binary aborts without calling install" {
  # SHA256SUMS is computed over 'BIN', but curl delivers 'TAMPERED'.
  # verify_download must detect the mismatch and abort upgrade_shared before
  # recording-install is ever called for the binary — i.e. the existing binary
  # is never touched.
  export ARBITRON_VERSION=1.0.0 OS_TOKEN=linux ARCH_TOKEN=amd64
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.0.0_linux_amd64\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *releases*)   printf "TAMPERED" > "$dest" ;;
      *)            echo 200 ;;
    esac
  '
  run upgrade_shared
  [ "$status" -ne 0 ]
  ! grep -qE "install .*/arbitron" "$MOCK_LOG"
}

# --- reconfigure -------------------------------------------------------------

@test "reconfigure rewrites config and restarts, preserving data" {
  cat >"$ARB_ETC/config.yaml" <<EOF
server:
  host: "127.0.0.1"
  port: 8080
EOF
  printf 'ARBITRON_MASTER_KEY=KEEPME==\n' >"$ARB_ETC/arbitron.env"
  export ARBITRON_AUTH_MODEL=edge-local ARBITRON_TLS_MODEL=tunnel
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_LLM_ACCESS=subscription ARBITRON_SKIP_PERMS=no
  install_fake curl 'echo 200'
  export ARBITRON_HEALTH_RETRIES=1 ARBITRON_HEALTH_SLEEP=0
  run reconfigure_shared
  [ "$status" -eq 0 ]
  grep -q "systemctl restart arbitron" "$MOCK_LOG"
  grep -qF 'KEEPME==' "$ARB_ETC/arbitron.env"
}

# --- uninstall ---------------------------------------------------------------

@test "uninstall disables unit, removes unit/binary/config, daemon-reloads" {
  : >"$(_unit)"
  run uninstall_shared
  [ "$status" -eq 0 ]
  grep -q "systemctl disable --now arbitron" "$MOCK_LOG"
  grep -q "systemctl daemon-reload" "$MOCK_LOG"
  grep -qF "rm" "$MOCK_LOG"
}

@test "uninstall keeps data by default" {
  : >"$(_unit)"
  printf 'db' >"$ARB_LIB/arbitron.db"
  export ARBITRON_REMOVE_DATA=no
  run uninstall_shared
  [ "$status" -eq 0 ]
  [ -f "$ARB_LIB/arbitron.db" ]
}

@test "uninstall removes data dir when ARBITRON_REMOVE_DATA=yes" {
  # The REMOVE_DATA=yes path calls rm -rf $ARB_LIB. This is irreversible and
  # must actually be exercised — a silent code path for destructive ops is a risk.
  # recording-rm removes paths within $TEST_SANDBOX, so the assertion is real.
  : >"$(_unit)"
  printf 'db' >"$ARB_LIB/arbitron.db"
  export ARBITRON_REMOVE_DATA=yes
  export ARBITRON_REMOVE_ACCOUNT=no
  run uninstall_shared
  [ "$status" -eq 0 ]
  grep -qE "rm .*-rf .*$ARB_LIB" "$MOCK_LOG"
  [ ! -f "$ARB_LIB/arbitron.db" ]
}

@test "uninstall removes the service account only when confirmed" {
  : >"$(_unit)"
  export ARBITRON_REMOVE_DATA=no
  export ARBITRON_REMOVE_ACCOUNT=yes
  run uninstall_shared
  [ "$status" -eq 0 ]
  grep -qE "userdel .*arbitron" "$MOCK_LOG"
}

@test "uninstall keeps the service account by default" {
  : >"$(_unit)"
  export ARBITRON_REMOVE_DATA=no
  export ARBITRON_REMOVE_ACCOUNT=no
  run uninstall_shared
  [ "$status" -eq 0 ]
  ! grep -q "userdel" "$MOCK_LOG"
}

@test "shared_service_remove disables the unit, removes the unit file, then daemon-reloads in order" {
  # The extracted teardown seam (mirrors the local service_remove). A single
  # function the suite can assert "teardown removed the unit" against, so the
  # local and shared uninstall paths cannot silently drift.
  : >"$(_unit)"
  [ -f "$(_unit)" ]
  run shared_service_remove
  [ "$status" -eq 0 ]
  # Unit file is gone (recording-rm operates on real sandbox paths).
  [ ! -f "$(_unit)" ]
  # Both systemctl calls happened, against the centralized unit constant.
  grep -q "systemctl disable --now arbitron" "$MOCK_LOG"
  grep -q "systemctl daemon-reload" "$MOCK_LOG"
  # Ordering: disable --now precedes daemon-reload.
  local dis rel
  dis="$(grep -n 'systemctl disable --now arbitron' "$MOCK_LOG" | head -n1 | cut -d: -f1)"
  rel="$(grep -n 'systemctl daemon-reload' "$MOCK_LOG" | head -n1 | cut -d: -f1)"
  [ "$dis" -lt "$rel" ]
}

@test "uninstall_shared routes service teardown through shared_service_remove" {
  # uninstall_shared must delegate the unit teardown to the seam (not inline it),
  # so the bats suite has one assertable teardown function.
  : >"$(_unit)"
  shared_service_remove() { echo "shared_service_remove called" >>"$MOCK_LOG"; }
  export ARBITRON_REMOVE_DATA=no
  export ARBITRON_REMOVE_ACCOUNT=no
  run uninstall_shared
  [ "$status" -eq 0 ]
  grep -q "shared_service_remove called" "$MOCK_LOG"
}
