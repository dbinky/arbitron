#!/usr/bin/env bats
#
# P2 Task 4: auth x TLS permutation matrix + P0-guard negatives + secrets-leak
# checks. Each positive row drives a full run_shared_install (mocked) and asserts
# the COMBINED config.yaml + arbitron.env + systemd-unit are mutually consistent
# and would pass P0's loader validation.

load test_helper
load helpers/shared

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  shared_sandbox_paths
  shared_stage_mocks
  # Elevated; account already present; binary downloads succeed; health passes.
  export ARBITRON_UID=0
  export MOCK_GETENT_RC=0
  export ARBITRON_HEALTH_RETRIES=1 ARBITRON_HEALTH_SLEEP=0
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_SVC_USER=arbitron
  export ARBITRON_ACTION=fresh
  export ARBITRON_SKIP_CLAUDE_LOGIN=1
  # Bypass the network: install_binary_system drops a fake binary directly so the
  # matrix exercises the config/env/unit wiring, not the download chain (covered
  # by the skeleton tests).
  install_binary_system() {
    printf 'install -m 0755 -o root -g root <bin> %s/arbitron\n' "$ARB_PREFIX_BIN" >>"$MOCK_LOG"
  }
  resolve_dependencies() { :; }
}

teardown() {
  install_teardown_sandbox
}

_common_oidc() {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=arb-client
  export ARBITRON_OIDC_CLIENT_SECRET=row-secret
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_RBAC=no
}

# --- Row 1: edge+local x tunnel ---------------------------------------------

@test "matrix row1: edge+local + tunnel" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'mode: "?local"?'
  read_cfg | grep -qE 'host: "?127\.0\.0\.1"?'
  read_cfg | grep -qE 'enabled: false'
  read_cfg | grep -qF 'https://arbitron.example.com'
  read_cfg | grep -qE 'cookiesecure: true'
  ! read_unit | grep -qF 'CAP_NET_BIND_SERVICE'
}

# --- Row 2: edge+local x direct cert/key ------------------------------------

@test "matrix row2: edge+local + direct cert/key (loopback, no allowunauth)" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=direct
  export ARBITRON_TLS_CERT=/etc/ssl/arb.crt
  export ARBITRON_TLS_KEY=/etc/ssl/arb.key
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'mode: "?local"?'
  read_cfg | grep -qE 'host: "?127\.0\.0\.1"?'
  read_cfg | grep -qE 'enabled: true'
  read_cfg | grep -qF '/etc/ssl/arb.crt'
  # loopback => allowunauthenticated not required (stays false/absent)
  ! read_cfg | grep -qE 'allowunauthenticated: true'
}

# --- Row 3: OIDC org x tunnel -----------------------------------------------

@test "matrix row3: OIDC org + tunnel" {
  _common_oidc
  export ARBITRON_TLS_MODEL=tunnel
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'mode: "?org"?'
  read_cfg | grep -qF 'https://idp.example.com'
  read_cfg | grep -qE 'enabled: false'
  read_cfg | grep -qE 'cookiesecure: true'
  read_env | grep -qE '^ARBITRON_AUTH_OIDC_CLIENTSECRET=row-secret$'
  ! read_cfg | grep -qF 'row-secret'
}

# --- Row 4: OIDC org x direct cert/key --------------------------------------

@test "matrix row4: OIDC org + direct cert/key (public bind allowed)" {
  _common_oidc
  export ARBITRON_BIND_HOST=0.0.0.0
  export ARBITRON_TLS_MODEL=direct
  export ARBITRON_TLS_CERT=/etc/ssl/arb.crt
  export ARBITRON_TLS_KEY=/etc/ssl/arb.key
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'mode: "?org"?'
  read_cfg | grep -qE 'enabled: true'
  read_cfg | grep -qF '/etc/ssl/arb.key'
}

# --- Row 5: OIDC org x autocert ---------------------------------------------

@test "matrix row5: OIDC org + autocert (port 443 + CAP_NET_BIND_SERVICE)" {
  _common_oidc
  export ARBITRON_TLS_MODEL=autocert
  export ARBITRON_AUTOCERT_DOMAIN=arbitron.example.com
  export ARBITRON_PORT=8080   # must be forced to 443
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'mode: "?org"?'
  read_cfg | grep -qE 'enabled: true'
  read_cfg | grep -qF 'autocertdomain: "arbitron.example.com"'
  read_cfg | grep -qE 'autocertcachedir: "?/'
  read_cfg | grep -qE 'port: 443'
  read_unit | grep -qF 'AmbientCapabilities=CAP_NET_BIND_SERVICE'
}

# --- Row 6: OIDC org + RBAC --------------------------------------------------

@test "matrix row6: OIDC org + RBAC" {
  _common_oidc
  export ARBITRON_TLS_MODEL=tunnel
  export ARBITRON_RBAC=yes
  export ARBITRON_RBAC_GROUP_CLAIM=groups
  export ARBITRON_RBAC_DEFAULT_GROUP=default
  export ARBITRON_RBAC_LEADS="engineering=carol@example.com"
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'groupclaim: "?groups"?'
  read_cfg | grep -qE 'defaultgroup: "?default"?'
  read_cfg | grep -qF 'carol@example.com'
}

# --- env mode/owner on every row --------------------------------------------

@test "matrix: arbitron.env is requested 0600 root:arbitron" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run run_shared_install
  [ "$status" -eq 0 ]
  grep -qE "chmod 0?600 .*arbitron.env" "$MOCK_LOG"
  grep -qE "chown root:arbitron .*arbitron.env" "$MOCK_LOG"
}

# --- P0-guard negatives ------------------------------------------------------

@test "guard: autocert with a non-443 port is auto-corrected to 443" {
  _common_oidc
  export ARBITRON_TLS_MODEL=autocert
  export ARBITRON_AUTOCERT_DOMAIN=arbitron.example.com
  export ARBITRON_PORT=8080
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'port: 443'
}

@test "guard: direct-TLS with a cert but no key is refused" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=direct
  export ARBITRON_TLS_CERT=/etc/ssl/arb.crt
  export ARBITRON_TLS_KEY=""     # missing key
  run run_shared_install
  [ "$status" -ne 0 ]
  shopt -s nocasematch
  [[ "$output" == *"key"* ]]
  shopt -u nocasematch
}

@test "guard: local+TLS on a public bind needs explicit allowunauthenticated" {
  export ARBITRON_AUTH_MODEL=local-public   # local mode, operator forces public bind
  export ARBITRON_BIND_HOST=0.0.0.0
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=direct
  export ARBITRON_TLS_CERT=/etc/ssl/arb.crt
  export ARBITRON_TLS_KEY=/etc/ssl/arb.key
  # Without confirmation the installer refuses.
  export ARBITRON_ALLOW_UNAUTH=no
  run run_shared_install
  [ "$status" -ne 0 ]

  # With explicit confirmation the key is set.
  export ARBITRON_ALLOW_UNAUTH=yes
  run run_shared_install
  [ "$status" -eq 0 ]
  read_cfg | grep -qE 'allowunauthenticated: true'
}

# --- secrets-leak ------------------------------------------------------------

@test "secrets: master key + client secret + api key never appear in output" {
  _common_oidc
  export ARBITRON_TLS_MODEL=tunnel
  export ARBITRON_LLM_ACCESS=api-key
  export ARBITRON_API_KEY=sk-ant-matrix-leak-check
  run run_shared_install
  [ "$status" -eq 0 ]
  [[ "$output" != *"row-secret"* ]]
  [[ "$output" != *"sk-ant-matrix-leak-check"* ]]
  local mk; mk="$(grep '^ARBITRON_MASTER_KEY=' "$ARB_ETC/arbitron.env" | cut -d= -f2-)"
  [ -n "$mk" ]
  [[ "$output" != *"$mk"* ]]
}
