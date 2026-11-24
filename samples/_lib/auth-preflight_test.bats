#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# auth-preflight.sh checks that the agent CLIs a stage uses are logged in, so an
# expired login stops the pipeline with a named reason instead of surfacing as an
# opaque "harness process failed" after hours of retries (run 68225980). The
# CLIs are stubs on a PATH that holds nothing else of theirs.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/auth-preflight.sh"
  T="$BATS_TEST_TMPDIR"
  STUBS="$T/stubs"
  mkdir -p "$STUBS"
  # Only the tools the script needs, so a real claude/codex/pi is never found.
  for b in sh env jq cat sleep kill date mktemp rm printf tr head sed grep cut chmod mkdir ls awk; do
    p="$(command -v "$b")" && ln -sf "$p" "$STUBS/$b"
  done
  export PATH="$STUBS"
  unset ARBITRON_AUTH_PREFLIGHT ARBITRON_AUTH_PREFLIGHT_TIMEOUT
  FUTURE="2099-01-01T00:00:00.000Z"
  PAST="2001-01-01T00:00:00.000Z"
}

# stub_claude <loggedIn> <loginExpiresAt|null> [rc]
stub_claude() {
  cat > "$STUBS/claude" <<EOF
#!/bin/sh
printf '%s\n' "\$*" > '$T/claude.args'
printf '%s' "\${ANTHROPIC_API_KEY-unset}" > '$T/claude.apikey'
cat <<JSON
{"loggedIn": $1, "authMethod": "claude.ai", "loginExpiresAt": $( [ "$2" = null ] && echo null || echo "\"$2\"" ), "email": "someone@example.com", "token": "sk-ant-SECRET123"}
JSON
exit ${3:-0}
EOF
  chmod +x "$STUBS/claude"
}

# stub_codex <rc> <text>
stub_codex() {
  cat > "$STUBS/codex" <<EOF
#!/bin/sh
printf '%s\n' "\$*" > '$T/codex.args'
echo "$2"
exit $1
EOF
  chmod +x "$STUBS/codex"
}

# stub_pi <rc> <text>
stub_pi() {
  cat > "$STUBS/pi" <<EOF
#!/bin/sh
printf '%s\n' "\$*" > '$T/pi.args'
echo "$2"
exit $1
EOF
  chmod +x "$STUBS/pi"
}

preflight() { run --separate-stderr sh "$SCRIPT" "$@" < /dev/null; }
field() { printf '%s' "$output" | jq -r "$1"; }

@test "every CLI logged in is ok" {
  stub_claude true "$FUTURE"
  stub_codex 0 "Logged in using ChatGPT"
  stub_pi 0 ready
  preflight claude codex pi:openai-codex
  [ "$status" -eq 0 ]
  [ "$(field .ok)" = true ]
  [ "$(field '.failures | length')" = 0 ]
  [ "$(field '.checked | join(",")')" = "claude,codex,pi:openai-codex" ]
  [ "$(cat "$T/claude.args")" = "auth status --json" ]
  [ "$(cat "$T/codex.args")" = "login status" ]
  [[ "$(cat "$T/pi.args")" == *"auth check --provider openai-codex"* ]]
}

@test "claude logged out is a failure, and the run still exits 0" {
  stub_claude false null 1
  preflight claude
  [ "$status" -eq 0 ]
  [ "$(field .ok)" = false ]
  [ "$(field '.failures[0].cli')" = claude ]
  [[ "$(field '.failures[0].detail')" == *"not logged in"* ]]
  [[ "$(field .summary)" == *claude* ]]
}

@test "a claude login past its expiry is a failure" {
  stub_claude true "$PAST"
  preflight claude
  [ "$(field .ok)" = false ]
  [[ "$(field '.failures[0].detail')" == *"expired"* ]]
}

@test "claude is probed without ANTHROPIC_API_KEY so the OAuth login is what is checked" {
  stub_claude true "$FUTURE"
  ANTHROPIC_API_KEY=sk-should-not-reach preflight claude
  [ "$(cat "$T/claude.apikey")" = unset ]
}

@test "unreadable claude status output is a failure" {
  cat > "$STUBS/claude" <<'EOF'
#!/bin/sh
echo "not json"
EOF
  chmod +x "$STUBS/claude"
  preflight claude
  [ "$(field .ok)" = false ]
  [ "$(field '.failures[0].cli')" = claude ]
}

@test "codex not logged in is a failure" {
  stub_codex 1 "Not logged in"
  preflight codex
  [ "$(field .ok)" = false ]
  [ "$(field '.failures[0].cli')" = codex ]
}

@test "pi not ready for its provider is a failure" {
  stub_pi 1 not_ready
  preflight pi:openai-codex
  [ "$(field .ok)" = false ]
  [ "$(field '.failures[0].cli')" = "pi:openai-codex" ]
  [[ "$(field '.failures[0].detail')" == *"not_ready"* ]]
}

@test "pi with no provider is skipped with a note" {
  stub_pi 0 ready
  preflight pi
  [ "$(field .ok)" = true ]
  [ "$(field '.skipped[0].cli')" = pi ]
  [ ! -e "$T/pi.args" ]
}

@test "a CLI that is not installed is skipped, not a failure" {
  preflight claude codex
  [ "$status" -eq 0 ]
  [ "$(field .ok)" = true ]
  [ "$(field '.skipped | length')" = 2 ]
}

@test "a hanging probe times out as a failure" {
  cat > "$STUBS/codex" <<'EOF'
#!/bin/sh
sleep 30
EOF
  chmod +x "$STUBS/codex"
  start=$(date +%s)
  ARBITRON_AUTH_PREFLIGHT_TIMEOUT=1 preflight codex
  [ $(( $(date +%s) - start )) -lt 10 ]
  [ "$(field .ok)" = false ]
  [[ "$(field '.failures[0].detail')" == *"timed out"* ]]
}

@test "no probe output is ever echoed, so no secret is printed" {
  stub_claude false null 1
  stub_codex 1 "token=sk-codex-SECRET999"
  preflight claude codex
  [[ "$output$stderr" != *SECRET* ]]
  [[ "$output$stderr" != *"someone@example.com"* ]]
}

@test "an unknown CLI name fails closed" {
  preflight clade
  [ "$(field .ok)" = false ]
  [ "$(field '.failures[0].cli')" = clade ]
}

@test "ARBITRON_AUTH_PREFLIGHT=0 skips every probe" {
  stub_claude false null 1
  ARBITRON_AUTH_PREFLIGHT=0 preflight claude
  [ "$(field .ok)" = true ]
  [ ! -e "$T/claude.args" ]
}

@test "no CLI named is a usage error" {
  preflight
  [ "$status" -eq 2 ]
}
