#!/usr/bin/env bats
#
# P2 Task 2: Shared config Q&A + /etc/arbitron/config.yaml + /etc/arbitron/arbitron.env.
# Drives the Q&A via the ARBITRON_* answer-injection env vars (same mechanism as
# P1's prompt_default/prompt_yes_no). chown/chmod/install are recording fakes so
# the writers run against the sandbox while the privileged commands are inert.

load test_helper

# Redirect the system paths into the sandbox so the writers can actually create
# files (the real /etc is not writable in CI). The layout_* resolvers honour
# ARB_ETC/ARB_LIB/ARB_LOG overrides.
_sandbox_system_paths() {
  export ARB_ETC="$TEST_SANDBOX/etc/arbitron"
  export ARB_LIB="$TEST_SANDBOX/var/lib/arbitron"
  export ARB_LOG="$TEST_SANDBOX/var/log/arbitron"
  mkdir -p "$ARB_ETC" "$ARB_LIB" "$ARB_LOG"
}

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  export ARB_INSTALL_MODE=shared
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake chown chmod install
  hash -r   # drop any cached path to the real chmod/chown so the mocks win
  _sandbox_system_paths
  # Common non-interactive answers; individual tests override.
  export ARBITRON_BIND_HOST=0.0.0.0
  export ARBITRON_PORT=8080
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_SVC_USER=arbitron
}

teardown() {
  install_teardown_sandbox
}

_cfg() { printf '%s/config.yaml\n' "$ARB_ETC"; }
_env() { printf '%s/arbitron.env\n' "$ARB_ETC"; }

# --- Auth: edge + local ------------------------------------------------------

@test "edge+local writes auth.mode local and forces loopback bind" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_BIND_HOST=10.0.0.5          # operator typed a public host
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qE 'mode: "?local"?' "$cfg"
  grep -qE 'host: "?127\.0\.0\.1"?' "$cfg"
  ! grep -qE 'issuerurl: "?http' "$cfg"
}

@test "edge+local sets publicbaseurl and cookiesecure true" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qF 'https://arbitron.example.com' "$cfg"
  grep -qE 'cookiesecure: true' "$cfg"
}

# --- Auth: OIDC org ----------------------------------------------------------

@test "OIDC org writes auth.mode org with issuer/clientid/redirect" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=arb-client
  export ARBITRON_OIDC_CLIENT_SECRET=top-secret-value
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com,bob@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qE 'mode: "?org"?' "$cfg"
  grep -qF 'https://idp.example.com' "$cfg"
  grep -qF 'arb-client' "$cfg"
  grep -qF 'https://arbitron.example.com/auth/callback' "$cfg"
}

@test "OIDC client secret goes to arbitron.env not config.yaml" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=arb-client
  export ARBITRON_OIDC_CLIENT_SECRET=top-secret-value
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  generate_master_key
  write_shared_env
  local cfg env; cfg="$(_cfg)"; env="$(_env)"
  ! grep -qF 'top-secret-value' "$cfg"
  grep -qE '^ARBITRON_AUTH_OIDC_CLIENTSECRET=top-secret-value$' "$env"
  grep -qF 'alice@example.com' "$cfg"   # bootstrap admins land in config
}

@test "optional RBAC writes groupclaim, defaultgroup and a department lead" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=arb-client
  export ARBITRON_OIDC_CLIENT_SECRET=s
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=yes
  export ARBITRON_RBAC_GROUP_CLAIM=groups
  export ARBITRON_RBAC_DEFAULT_GROUP=default
  export ARBITRON_RBAC_LEADS="engineering=carol@example.com"
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qE 'groupclaim: "?groups"?' "$cfg"
  grep -qE 'defaultgroup: "?default"?' "$cfg"
  grep -qF 'carol@example.com' "$cfg"
}

@test "RBAC declined leaves rbac lead keys absent" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=arb-client
  export ARBITRON_OIDC_CLIENT_SECRET=s
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  ! grep -qE 'leads:' "$(_cfg)"
}

# --- TLS: three models -------------------------------------------------------

@test "tunnel/proxy TLS keeps tls disabled, sets publicbaseurl, prints guidance" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=c
  export ARBITRON_OIDC_CLIENT_SECRET=s
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=tunnel
  run run_shared_qa
  [ "$status" -eq 0 ]
  [[ "$output" == *"tunnel"* ]] || [[ "$output" == *"proxy"* ]]
  write_shared_config
  grep -qE 'enabled: false' "$(_cfg)"
}

@test "direct app-TLS sets cert and key, autocertdomain empty" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=c
  export ARBITRON_OIDC_CLIENT_SECRET=s
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=direct
  export ARBITRON_TLS_CERT=/etc/ssl/arb.crt
  export ARBITRON_TLS_KEY=/etc/ssl/arb.key
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qE 'enabled: true' "$cfg"
  grep -qF '/etc/ssl/arb.crt' "$cfg"
  grep -qF '/etc/ssl/arb.key' "$cfg"
  grep -qE 'autocertdomain: "?"?$' "$cfg"
}

@test "autocert sets domain + cachedir and forces port 443" {
  export ARBITRON_AUTH_MODEL=oidc
  export ARBITRON_OIDC_ISSUER=https://idp.example.com
  export ARBITRON_OIDC_CLIENT_ID=c
  export ARBITRON_OIDC_CLIENT_SECRET=s
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_OIDC_BOOTSTRAP_ADMINS="alice@example.com"
  export ARBITRON_RBAC=no
  export ARBITRON_TLS_MODEL=autocert
  export ARBITRON_AUTOCERT_DOMAIN=arbitron.example.com
  export ARBITRON_PORT=8080   # operator typed non-443; installer must force 443
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qE 'enabled: true' "$cfg"
  grep -qF 'autocertdomain: "arbitron.example.com"' "$cfg"
  grep -qE 'autocertcachedir: "?/' "$cfg"
  grep -qE 'port: 443' "$cfg"
}

# --- skip-permissions / RCE prompt ------------------------------------------

@test "skip-permissions prompt carries an RCE warning and is recorded" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  export ARBITRON_SKIP_PERMS=yes
  # Call directly (so SCFG_* persists) but tee stderr to a file for the warning
  # assertion — a `run` subshell would discard the collected state.
  run_shared_qa 2>"$TEST_SANDBOX/qa.err"
  shopt -s nocasematch
  grep -qi 'RCE\|remote code execution' "$TEST_SANDBOX/qa.err"
  shopt -u nocasematch
  write_shared_config
  grep -qE 'skippermissions: true' "$(_cfg)"
}

# --- secrets env -------------------------------------------------------------

@test "arbitron.env chmod 0600 and chown root:arbitron" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  generate_master_key
  write_shared_env
  grep -qE "chmod 0?600 .*$(_env)" "$MOCK_LOG"
  grep -qE "chown root:arbitron .*$(_env)" "$MOCK_LOG"
}

@test "master key written to env, never echoed" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run bash -c "
    install_isolate_path() { :; }
    export ARB_INSTALL_MODE=shared
    export ARB_ETC='$ARB_ETC' ARB_LIB='$ARB_LIB' ARB_LOG='$ARB_LOG'
    export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
    export ARBITRON_AUTH_MODEL=edge-local ARBITRON_TLS_MODEL=tunnel
    export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
    export ARBITRON_SKIP_PERMS=no ARBITRON_LLM_ACCESS=subscription
    PATH='$MOCK_BIN:$PATH'
    ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'
    run_shared_qa >/dev/null
    generate_master_key
    write_shared_env
    echo done
  "
  [ "$status" -eq 0 ]
  local key; key="$(grep '^ARBITRON_MASTER_KEY=' "$(_env)" | cut -d= -f2-)"
  [ -n "$key" ]
  [[ "$output" != *"$key"* ]]
}

@test "anthropic api key lands in env not config" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  export ARBITRON_LLM_ACCESS=api-key
  export ARBITRON_API_KEY=sk-ant-shared-secret
  run_shared_qa
  write_shared_config
  generate_master_key
  write_shared_env
  grep -qE '^ANTHROPIC_API_KEY=sk-ant-shared-secret$' "$(_env)"
  ! grep -qF 'sk-ant-shared-secret' "$(_cfg)"
}

# --- paths -------------------------------------------------------------------

@test "config.yaml uses absolute /var/lib + /var/log system paths" {
  export ARBITRON_AUTH_MODEL=edge-local
  export ARBITRON_PUBLIC_BASE_URL=https://arbitron.example.com
  export ARBITRON_TLS_MODEL=tunnel
  run_shared_qa
  write_shared_config
  local cfg; cfg="$(_cfg)"
  grep -qF "$ARB_LIB/arbitron.db" "$cfg"
  grep -qF "$ARB_LIB/workspaces" "$cfg"
  grep -qF "$ARB_LOG/arbitron.log" "$cfg"
}

# --- reconfigure pre-fill ----------------------------------------------------

@test "reconfigure pre-fills host/port from an existing config" {
  cat >"$(_cfg)" <<EOF
server:
  host: "0.0.0.0"
  port: 9700
EOF
  run read_existing_config_value server.port
  [ "$output" = "9700" ]
}

@test "reconfigure preserves an existing master key" {
  printf 'ARBITRON_MASTER_KEY=SHARED_PRESERVED_KEY==\n' >"$(_env)"
  generate_master_key
  grep -qF 'SHARED_PRESERVED_KEY==' "$(_env)" || true
  [ "$MASTER_KEY" = "SHARED_PRESERVED_KEY==" ]
}
