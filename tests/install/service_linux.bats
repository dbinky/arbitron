#!/usr/bin/env bats
#
# Task 4: Linux systemd --user unit registration.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  export ARBITRON_PORT=8080
  export USER=tester
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  install_mock recording-fake systemctl loginctl
}

teardown() {
  install_teardown_sandbox
}

_unit() { printf '%s/.config/systemd/user/arbitron.service\n' "$HOME"; }

@test "render_systemd_unit ExecStart is the absolute binary serve --config" {
  render_systemd_unit
  local u
  u="$(_unit)"
  [ -f "$u" ]
  grep -qF "ExecStart=$(layout_bin)/arbitron serve --config $(layout_config)" "$u"
}

@test "unit EnvironmentFile points at the absolute secrets env" {
  render_systemd_unit
  grep -qF "EnvironmentFile=$(layout_secrets_env)" "$(_unit)"
}

@test "unit has Restart and WantedBy=default.target" {
  render_systemd_unit
  local u
  u="$(_unit)"
  grep -qE "Restart=(on-failure|always)" "$u"
  grep -qF "WantedBy=default.target" "$u"
}

@test "register_service_linux daemon-reloads, enables --now, and enables linger" {
  register_service_linux
  mock_log_has "systemctl --user daemon-reload"
  grep -q "systemctl --user enable --now arbitron" "$MOCK_LOG"
  grep -q "loginctl enable-linger tester" "$MOCK_LOG"
}
