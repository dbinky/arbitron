#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# notify-teams.sh local fallback: with no webhook configured, a macOS host gets
# a desktop notification through osascript (stubbed here on PATH), so a failed
# or blocked run is never silent. The webhook path is unchanged, the fallback is
# opt-out with ARBITRON_NOTIFY_LOCAL=0, and nothing in it can fail the node.

setup() {
  SCRIPT="$BATS_TEST_DIRNAME/notify-teams.sh"
  T="$BATS_TEST_TMPDIR"
  STUBS="$T/stubs"
  mkdir -p "$STUBS"
  unset ARBITRON_TEAMS_WEBHOOK_URL ARBITRON_NOTIFY_LOCAL
  # osascript stub: one argument per line, then a separator.
  cat > "$STUBS/osascript" <<EOF
#!/bin/sh
for a in "\$@"; do printf '%s\n' "\$a"; done >> '$T/osascript.calls'
echo '--' >> '$T/osascript.calls'
exit \${OSASCRIPT_RC:-0}
EOF
  cat > "$STUBS/uname" <<'EOF'
#!/bin/sh
echo "${FAKE_UNAME:-Darwin}"
EOF
  cat > "$STUBS/curl" <<EOF
#!/bin/sh
cat > /dev/null
echo curl >> '$T/curl.calls'
printf 200
EOF
  chmod +x "$STUBS/osascript" "$STUBS/uname" "$STUBS/curl"
  export PATH="$STUBS:$PATH"
}

# notify <stdin json> <args...>
notify() {
  printf '%s' "$1" > "$T/in.json"; shift
  run --separate-stderr sh "$SCRIPT" "$@" < "$T/in.json"
}

calls() { cat "$T/osascript.calls" 2>/dev/null || true; }

@test "no webhook on macOS posts a local notification with the stage, outcome and reason" {
  notify '{}' "feature-pipeline stopped at plan — my-feature" "child workflow ended failed" "error" "Stage=plan" "Repo=r"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .skipped)" = true ]
  [ "$(printf '%s' "$output" | jq -r .local_notified)" = true ]
  c="$(calls)"
  [[ "$c" == *"display notification"* ]]
  [[ "$c" == *"feature-pipeline stopped at plan — my-feature"* ]]
  [[ "$c" == *"failed"* ]]
  [[ "$c" == *"plan"* ]]
  [[ "$c" == *"child workflow ended failed"* ]]
}

@test "the outcome follows the status: good is done, warning is blocked" {
  notify '{}' "T" "S" "good"
  [[ "$(calls)" == *"done"* ]]
  rm -f "$T/osascript.calls"
  notify '{}' "T" "S" "warning"
  [[ "$(calls)" == *"blocked"* ]]
}

@test "an Outcome fact overrides the status-derived outcome" {
  notify '{}' "T" "unresolved plan dependencies" "error" "Outcome=blocked" "Stage=plan"
  [ "$status" -eq 0 ]
  [[ "$(calls)" == *"blocked"* ]]
  [[ "$(calls)" != *"failed"* ]]
}

@test "a long reason is truncated" {
  long="$(printf 'x%.0s' $(seq 1 2000))"
  notify '{}' "T" "$long" "error"
  [ "$status" -eq 0 ]
  max="$(calls | awk '{ if (length($0) > m) m = length($0) } END { print m }')"
  [ "$max" -le 300 ]
}

@test "ARBITRON_NOTIFY_LOCAL=0 disables the fallback" {
  ARBITRON_NOTIFY_LOCAL=0 notify '{}' "T" "S" "error"
  [ "$status" -eq 0 ]
  [ -z "$(calls)" ]
  [ "$(printf '%s' "$output" | jq -r .local_notified)" = false ]
}

@test "a configured webhook posts there and does not notify locally" {
  notify '{"teams_webhook_url":"https://example.invalid/hook"}' "T" "S" "error"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .posted)" = true ]
  [ -s "$T/curl.calls" ]
  [ -z "$(calls)" ]
}

@test "a non-macOS host skips the fallback cleanly" {
  FAKE_UNAME=Linux notify '{}' "T" "S" "error"
  [ "$status" -eq 0 ]
  [ -z "$(calls)" ]
  [ "$(printf '%s' "$output" | jq -r .skipped)" = true ]
}

@test "no osascript on PATH skips the fallback cleanly" {
  rm "$STUBS/osascript"
  mkdir -p "$T/bin"
  for b in sh jq cat printf head uname; do
    p="$(PATH="$STUBS:/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin" command -v "$b")" && ln -sf "$p" "$T/bin/$b"
  done
  run --separate-stderr env PATH="$T/bin" sh -c 'printf "{}" | sh "$1" T S error' _ "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .local_notified)" = false ]
}

@test "a failing osascript never fails the node" {
  OSASCRIPT_RC=1 notify '{}' "T" "S" "error"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .local_notified)" = false ]
}

@test "the legacy positional call with no stdin still works" {
  run --separate-stderr sh -c 'sh "$1" "T" "S" "error" < /dev/null' _ "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(printf '%s' "$output" | jq -r .skipped)" = true ]
}
