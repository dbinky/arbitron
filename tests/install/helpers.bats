#!/usr/bin/env bats
#
# Task 1: shared helper library (logging, prompts, layout, checksum, require_cmd).

load test_helper

setup() {
  install_setup_sandbox
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

# --- logging -----------------------------------------------------------------

@test "log_info writes to stderr not stdout" {
  run bash -c "ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; log_info hello 2>/dev/null"
  [ "$status" -eq 0 ]
  [ -z "$output" ]   # nothing on stdout
}

@test "log_error writes to stderr and does NOT exit" {
  run bash -c "ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; log_error boom; echo survived"
  [ "$status" -eq 0 ]
  [[ "$output" == *"survived"* ]]
}

@test "die prints to stderr and exits non-zero" {
  run bash -c "ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; die 'fatal thing'"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fatal thing"* ]]
}

# --- prompts -----------------------------------------------------------------

@test "prompt_default returns the default on empty stdin" {
  run bash -c "ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_default PORT 'Port?' '8080' </dev/null"
  [ "$status" -eq 0 ]
  [ "$output" = "8080" ]
}

@test "prompt_default returns typed value from stdin" {
  run bash -c "printf '9090\n' | { ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_default PORT 'Port?' '8080'; }"
  [ "$status" -eq 0 ]
  [ "$output" = "9090" ]
}

@test "prompt_default honours ARBITRON_<VAR> env override without prompting" {
  export ARBITRON_PORT=7777
  run prompt_default PORT 'Port?' '8080' </dev/null
  [ "$status" -eq 0 ]
  [ "$output" = "7777" ]
}

@test "prompt_secret honours ARBITRON_<VAR> env override without prompting" {
  export ARBITRON_API_KEY="sk-ant-secret"
  run prompt_secret API_KEY 'Paste your Anthropic API key' '' </dev/null
  [ "$status" -eq 0 ]
  [ "$output" = "sk-ant-secret" ]
}

@test "prompt_secret returns the default on empty stdin" {
  run bash -c "ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_secret API_KEY 'Key?' '' </dev/null"
  [ "$status" -eq 0 ]
  [ "$output" = "" ]
}

@test "prompt_secret reads non-interactive stdin (no tty) without echo machinery" {
  # When stdin is not a tty (piped/CI), prompt_secret falls back to a plain read
  # so automated runs still work; the no-echo read -rs path is the interactive one.
  run bash -c "printf 'piped-secret\n' | { ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_secret API_KEY 'Key?' ''; }"
  [ "$status" -eq 0 ]
  [ "$output" = "piped-secret" ]
}

@test "prompt_secret uses read -rs (no-echo) for interactive entry" {
  # Source-level guard: the secret prompt must use the silent read flag so typed
  # secrets are not echoed to the terminal / scrollback.
  run grep -A1 'prompt_secret()' "$INSTALL_SH"
  grep -qE 'read -rs' "$INSTALL_SH"
}

@test "the API key and OIDC client secret prompts route through prompt_secret" {
  # No-echo entry must actually be wired to the secret prompts, not just defined.
  grep -qE 'prompt_secret API_KEY "Paste your Anthropic API key"' "$INSTALL_SH"
  grep -qE 'prompt_secret OIDC_CLIENT_SECRET' "$INSTALL_SH"
  # And the old echoing prompt_default must no longer be used for these secrets.
  ! grep -qE 'prompt_default API_KEY "Paste your Anthropic API key"' "$INSTALL_SH"
  ! grep -qE 'prompt_default OIDC_CLIENT_SECRET' "$INSTALL_SH"
}

@test "prompt_yes_no parses yes/no and respects default" {
  run bash -c "printf 'y\n'  | { ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_yes_no CONFIRM 'ok?' no; }"
  [ "$status" -eq 0 ]
  run bash -c "printf 'n\n'  | { ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_yes_no CONFIRM 'ok?' yes; }"
  [ "$status" -ne 0 ]
  # empty -> default yes
  run bash -c "{ ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_yes_no CONFIRM 'ok?' yes </dev/null; }"
  [ "$status" -eq 0 ]
  # empty -> default no
  run bash -c "{ ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'; prompt_yes_no CONFIRM 'ok?' no </dev/null; }"
  [ "$status" -ne 0 ]
}

@test "prompt_yes_no respects env override" {
  ARBITRON_CONFIRM=yes run prompt_yes_no CONFIRM 'ok?' no </dev/null
  [ "$status" -eq 0 ]
  ARBITRON_CONFIRM=no run prompt_yes_no CONFIRM 'ok?' yes </dev/null
  [ "$status" -ne 0 ]
}

# --- layout ------------------------------------------------------------------

@test "arbitron_home is HOME/.arbitron" {
  [ "$(arbitron_home)" = "$HOME/.arbitron" ]
}

@test "layout helpers return the exact absolute P1 paths" {
  [ "$(layout_bin)"         = "$HOME/.arbitron/bin" ]
  [ "$(layout_config)"      = "$HOME/.arbitron/config.yaml" ]
  [ "$(layout_data)"        = "$HOME/.arbitron/data" ]
  [ "$(layout_db)"          = "$HOME/.arbitron/data/arbitron.db" ]
  [ "$(layout_workspaces)"  = "$HOME/.arbitron/workspaces" ]
  [ "$(layout_logs)"        = "$HOME/.arbitron/logs" ]
  [ "$(layout_secrets_env)" = "$HOME/.arbitron/arbitron.env" ]
  [ "$(layout_runtime)"     = "$HOME/.arbitron/runtime" ]
}

@test "layout helpers return system paths when ARB_INSTALL_MODE=shared" {
  ARB_INSTALL_MODE=shared
  [ "$(layout_bin)"         = "/usr/local/bin" ]
  [ "$(layout_config)"      = "/etc/arbitron/config.yaml" ]
  [ "$(layout_data)"        = "/var/lib/arbitron" ]
  [ "$(layout_db)"          = "/var/lib/arbitron/arbitron.db" ]
  [ "$(layout_workspaces)"  = "/var/lib/arbitron/workspaces" ]
  [ "$(layout_logs)"        = "/var/log/arbitron" ]
  [ "$(layout_secrets_env)" = "/etc/arbitron/arbitron.env" ]
  # runtime: shared mode reuses ARB_LIB/runtime
  [ "$(layout_runtime)"     = "/var/lib/arbitron/runtime" ]
  ARB_INSTALL_MODE=local
}

# --- checksum ----------------------------------------------------------------

@test "verify_checksum succeeds on match and fails on mismatch" {
  local f="$TEST_SANDBOX/artifact.bin"
  printf 'hello arbitron' >"$f"
  local good
  good="$(sha256_tool <"$f")"
  run verify_checksum "$f" "$good"
  [ "$status" -eq 0 ]
  run verify_checksum "$f" "0000000000000000000000000000000000000000000000000000000000000000"
  [ "$status" -ne 0 ]
}

@test "sha256_tool produces a 64-hex digest" {
  local d
  d="$(printf 'x' | sha256_tool)"
  [[ "$d" =~ ^[0-9a-f]{64}$ ]]
}

# --- require_cmd -------------------------------------------------------------

@test "require_cmd returns non-zero (does not exit) when command absent" {
  run require_cmd definitely-not-a-real-cmd-xyz
  [ "$status" -ne 0 ]
}

@test "require_cmd returns zero when command present" {
  install_fake mytool 'exit 0'
  run require_cmd mytool
  [ "$status" -eq 0 ]
}

# --- library guard -----------------------------------------------------------

@test "ARBITRON_INSTALL_LIB_ONLY=1 sources install.sh without invoking main (guard integrity)" {
  # Every test setup() calls 'ARBITRON_INSTALL_LIB_ONLY=1 source install.sh'.
  # If the guard breaks, main() runs and calls detect_os -> uname, leaving a
  # trace in MOCK_LOG. This test makes that failure obvious rather than a
  # cascade of confusing errors across the entire suite.
  install_mock recording-fake uname
  run bash -c "
    unset ARBITRON_UNAME_S ARBITRON_UNAME_M
    ARBITRON_INSTALL_LIB_ONLY=1 source '$INSTALL_SH'
    echo sourced-ok
  " </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"sourced-ok"* ]]
  # main() always calls detect_os which calls uname; empty log = guard fired.
  [ ! -s "$MOCK_LOG" ]
}
