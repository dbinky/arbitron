#!/usr/bin/env bats
#
# Task 3: config Q&A, config.yaml writer, secrets env writer.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  # Non-interactive Q&A defaults for most tests (subscription mode).
  export ARBITRON_PORT=8080
  export ARBITRON_LLM_ACCESS=subscription
  export ARBITRON_SKIP_PERMS=no
  export ARBITRON_DATA_DIR="$HOME/.arbitron/data"
  export ARBITRON_CLAUDE_PATH="$HOME/.arbitron/runtime/npm/bin/claude"
}

teardown() {
  install_teardown_sandbox
}

# --- config.yaml -------------------------------------------------------------

@test "write_config_yaml writes absolute database.path under the data dir" {
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  [ -f "$cfg" ]
  grep -qE '^  path: "/' "$cfg"
  grep -qF "$HOME/.arbitron/data/arbitron.db" "$cfg"
}

@test "write_config_yaml uses absolute workspace.path and logging.file" {
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  grep -qF "$HOME/.arbitron/workspaces" "$cfg"
  grep -qF "$HOME/.arbitron/logs/arbitron.log" "$cfg"
}

@test "write_config_yaml binds loopback and honours the chosen port" {
  export ARBITRON_PORT=9123
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  grep -qE 'host: "127\.0\.0\.1"' "$cfg"
  grep -qE 'port: 9123' "$cfg"
}

@test "default port is 8080 when unanswered" {
  unset ARBITRON_PORT
  run_config_qa </dev/null
  write_config_yaml
  grep -qE 'port: 8080' "$(layout_config)"
}

@test "subscription mode writes claude-cli provider and binarypath" {
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  grep -qE 'provider: "claude-cli"' "$cfg"
  grep -qF "binarypath: \"$HOME/.arbitron/runtime/npm/bin/claude\"" "$cfg"
}

@test "api-key mode writes anthropic provider and keeps key out of config.yaml" {
  export ARBITRON_LLM_ACCESS=api-key
  export ARBITRON_API_KEY="sk-ant-secret-DO-NOT-LEAK"
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  grep -qE 'provider: "anthropic"' "$cfg"
  ! grep -qF "sk-ant-secret-DO-NOT-LEAK" "$cfg"
}

@test "skippermissions true only when explicitly answered yes" {
  export ARBITRON_SKIP_PERMS=yes
  run_config_qa
  write_config_yaml
  grep -qE 'skippermissions: true' "$(layout_config)"
}

@test "skippermissions defaults to false" {
  run_config_qa
  write_config_yaml
  grep -qE 'skippermissions: false' "$(layout_config)"
}

@test "reconfigure pre-fills the port from an existing config" {
  mkdir -p "$(arbitron_home)"
  cat >"$(layout_config)" <<EOF
server:
  host: "127.0.0.1"
  port: 9555
EOF
  run read_existing_config_value "server.port"
  [ "$output" = "9555" ]
}

@test "config.yaml is mode 0600 (skippermissions kept private)" {
  run_config_qa
  write_config_yaml
  local cfg perms
  cfg="$(layout_config)"
  [ -f "$cfg" ]
  perms="$(file_mode "$cfg")"
  [ "$perms" = "600" ]
}

@test "api-key mode with empty key errors rather than producing a broken install" {
  # If a user chooses api-key mode but provides no key (headless / forgot to
  # set ARBITRON_API_KEY), the installer must abort early instead of silently
  # writing provider=anthropic without an API key in the env file.
  export ARBITRON_LLM_ACCESS=api-key
  unset ARBITRON_API_KEY
  run run_config_qa </dev/null
  [ "$status" -ne 0 ]
  [[ "$output" == *"api-key"* || "$output" == *"API key"* ]]
}

@test "custom data directory flows into config.yaml database.path" {
  local custom="$TEST_SANDBOX/custom-data"
  export ARBITRON_DATA_DIR="$custom"
  run_config_qa
  write_config_yaml
  local cfg="$(layout_config)"
  grep -qF "$custom/arbitron.db" "$cfg"
}

# --- secrets env -------------------------------------------------------------

@test "write_secrets_env creates arbitron.env mode 0600" {
  generate_master_key
  write_secrets_env
  local env="$(layout_secrets_env)"
  [ -f "$env" ]
  local perms
  perms="$(file_mode "$env")"
  [ "$perms" = "600" ]
}

@test "secrets env contains a base64 32-byte master key" {
  generate_master_key
  write_secrets_env
  local env="$(layout_secrets_env)"
  grep -qE '^ARBITRON_MASTER_KEY=' "$env"
  local val
  val="$(grep '^ARBITRON_MASTER_KEY=' "$env" | cut -d= -f2-)"
  local nbytes
  nbytes="$(printf '%s' "$val" | base64 --decode 2>/dev/null | wc -c | tr -d ' ')"
  [ "$nbytes" = "32" ]
}

@test "api-key mode adds ANTHROPIC_API_KEY; subscription does not" {
  generate_master_key
  export ARBITRON_LLM_ACCESS=api-key
  export ARBITRON_API_KEY="sk-ant-xyz"
  run_config_qa
  write_secrets_env
  grep -qE '^ANTHROPIC_API_KEY=' "$(layout_secrets_env)"

  # subscription run in a fresh secrets file
  rm -f "$(layout_secrets_env)"
  export ARBITRON_LLM_ACCESS=subscription
  run_config_qa
  generate_master_key
  write_secrets_env
  ! grep -qE '^ANTHROPIC_API_KEY=' "$(layout_secrets_env)"
}

@test "the master key never appears in stdout/stderr" {
  run bash -c "
    install_isolate_path() { :; }
    export ARBITRON_PORT=8080 ARBITRON_LLM_ACCESS=subscription ARBITRON_SKIP_PERMS=no
    export ARBITRON_DATA_DIR='$HOME/.arbitron/data'
    ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'
    generate_master_key
    write_secrets_env
    echo done
  "
  [ "$status" -eq 0 ]
  local key
  key="$(grep '^ARBITRON_MASTER_KEY=' "$(layout_secrets_env)" | cut -d= -f2-)"
  [ -n "$key" ]
  [[ "$output" != *"$key"* ]]
}

@test "the Anthropic API key never appears in stdout/stderr" {
  # Symmetric to the master-key test: the API key is a real secret and must
  # never be echoed, even during run_config_qa or write_secrets_env.
  run bash -c "
    install_isolate_path() { :; }
    export ARBITRON_PORT=8080 ARBITRON_LLM_ACCESS=api-key ARBITRON_SKIP_PERMS=no
    export ARBITRON_DATA_DIR='$HOME/.arbitron/data'
    export ARBITRON_API_KEY='sk-ant-SUPER-SECRET-XYZ789'
    ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'
    generate_master_key
    run_config_qa
    write_secrets_env
    echo done
  "
  [ "$status" -eq 0 ]
  [[ "$output" != *"sk-ant-SUPER-SECRET-XYZ789"* ]]
}

@test "unknown LLM_ACCESS value silently falls back to subscription mode" {
  # Documents that a typo ('SUBSCRIPTION', 'apikey', etc.) is treated as
  # subscription, not api-key. This is the safe default — no secret is
  # required — but operators should know about it.
  export ARBITRON_LLM_ACCESS=SUBSCRIPTION  # wrong case
  run_config_qa
  write_config_yaml
  grep -qE 'provider: "claude-cli"' "$(layout_config)"
}

@test "write_config_yaml sets arbitron home to mode 0700" {
  # The home directory should not be group/world-readable; the binary, config,
  # and secrets env all live beneath it.
  run_config_qa
  write_config_yaml
  local perms
  perms="$(file_mode "$(arbitron_home)")"
  [ "$perms" = "700" ]
}

@test "reconfigure preserves the existing master key" {
  mkdir -p "$(arbitron_home)"
  printf 'ARBITRON_MASTER_KEY=PRESERVED_KEY_VALUE_BASE64==\n' >"$(layout_secrets_env)"
  chmod 600 "$(layout_secrets_env)"
  generate_master_key   # should detect + reuse the existing key
  write_secrets_env
  grep -qF "PRESERVED_KEY_VALUE_BASE64==" "$(layout_secrets_env)"
}
