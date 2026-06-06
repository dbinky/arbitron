# Installers P1 — Task 4: service registration & lifecycle (launchd / systemd --user)

> Phase 1 (P1) of the Arbitron installers feature. Source design:
> `docs/superpowers/specs/2026-06-05-installers-design.md` (§Service definitions,
> §Upgrade / reconfigure / uninstall) and
> `docs/superpowers/specs/installers-design-phase-1.md` (§Service definitions,
> §Upgrade / reconfigure / uninstall). Builds on Tasks 1–3.

## Context

This task delivers native, reboot-persistent **user-level** service registration
for the two local platforms and the full install/upgrade/reconfigure/uninstall
lifecycle, plus a post-start health check.

- **macOS** → a launchd LaunchAgent at `~/Library/LaunchAgents/dev.arbitron.plist`
  with `RunAtLoad` + `KeepAlive` and `EnvironmentVariables` carrying the config path
  (`ARBITRON_CONFIG`) and the secrets env values; loaded via `launchctl`.
- **Linux** → a systemd `--user` unit at `~/.config/systemd/user/arbitron.service`
  with `EnvironmentFile=` pointing at the secrets env, started/enabled via
  `systemctl --user`, plus `loginctl enable-linger <user>` so it survives logout/
  reboot without an interactive session.

Every unit invokes `arbitron serve --config <abs path>` (the P0 `--config` flag) so
the service finds the right config regardless of the run-as account's home. The unit
runs as the invoking user (no sudo, no dedicated account — that's P2). The
upgrade/reconfigure/uninstall handlers (stubbed in Task 1) are completed here.

Depends on: Tasks 1–3 (layout, config + secrets on disk, binary in place). Feeds:
Task 5 (tests), P2 (system-unit branch reuses the unit-rendering + lifecycle seams).

## Test plan (write first)

Create `tests/install/service_macos.bats`, `tests/install/service_linux.bats`, and
`tests/install/lifecycle.bats`. Mock `launchctl`, `systemctl`, `loginctl`, and
`curl` (health check) as recording fakes on `PATH`; render units into the sandbox
`HOME`. Select platform via the mocked `uname`.

launchd (`service_macos.bats`):
- After registration, `~/Library/LaunchAgents/dev.arbitron.plist` exists and is
  valid plist XML (assert via `plutil -lint` when available, else an XML parse).
- The plist `ProgramArguments` invoke `$(layout_bin)/arbitron serve --config
  $(layout_config)` (asserts the absolute binary path + `--config` + absolute config
  path are present).
- `RunAtLoad` and `KeepAlive` are `true`.
- `EnvironmentVariables` includes `ARBITRON_CONFIG` set to the absolute config path
  (and the master key / api key are sourced from the secrets env, not duplicated in
  plaintext where avoidable — assert the chosen secret-delivery approach).
- `launchctl load`/`bootstrap` is invoked for the agent (asserts recorded argv).

systemd --user (`service_linux.bats`):
- `~/.config/systemd/user/arbitron.service` exists.
- `ExecStart` = `$(layout_bin)/arbitron serve --config $(layout_config)` (absolute).
- `EnvironmentFile=` = the absolute `arbitron.env` path.
- `Restart=on-failure` (or `always`) and `WantedBy=default.target` present.
- `systemctl --user daemon-reload`, `enable --now arbitron`, and
  `loginctl enable-linger <user>` are all invoked (asserts recorded argv).

health check (`lifecycle.bats`):
- After start, the installer polls `http://127.0.0.1:<port>/` (or a known health
  path) with a bounded retry loop; a mocked 200 → success summary; a persistent
  non-200/timeout → non-zero exit with a diagnostic pointing at the log path.

Upgrade (`lifecycle.bats`):
- Upgrade downloads + verifies the new binary (reuses Task 3 path), swaps it in,
  restarts the service (asserts `launchctl kickstart`/`systemctl --user restart`
  recorded), and PRESERVES config + data + secrets (asserts files unchanged).
- Upgrade notes surface the forward-only migration / one-way down-migration caveat
  (asserts the caveat text is printed).

Reconfigure (`lifecycle.bats`):
- Reconfigure re-runs the Q&A pre-filled (Task 3), rewrites `config.yaml`, restarts
  the service, and does NOT touch the DB/data or regenerate the master key.

Uninstall (`lifecycle.bats`):
- Uninstall stops + removes the service unit (asserts `launchctl bootout` /
  `systemctl --user disable --now` + unit file removed), removes the binary + config.
- Data removal is PROMPTED and defaults to KEEP (asserts data dir still present when
  the prompt is declined/defaulted; removed only on explicit yes).

shellcheck: `install.sh` clean.

## Implementation steps

1. Add `render_launchd_plist`: heredoc the plist with absolute `ProgramArguments`
   (`arbitron serve --config <cfg>`), `RunAtLoad`/`KeepAlive` true,
   `EnvironmentVariables` (`ARBITRON_CONFIG`, log redirect paths), `StandardOut/
   ErrorPath` under the logs dir. Decide secret delivery: prefer having launchd load
   the secrets env (e.g. a wrapper that sources `arbitron.env`) over duplicating the
   master key in the plist; document the choice.
2. Add `register_service_macos`: write the plist, `launchctl bootout` any prior, then
   `launchctl bootstrap gui/$UID` (or `load -w`), then `kickstart`.
3. Add `render_systemd_unit`: heredoc `[Unit]/[Service]/[Install]` with absolute
   `ExecStart`, `EnvironmentFile=<arbitron.env>`, `Restart=on-failure`,
   `WorkingDirectory=<data dir>`, `WantedBy=default.target`.
4. Add `register_service_linux`: `mkdir -p ~/.config/systemd/user`, write the unit,
   `systemctl --user daemon-reload`, `systemctl --user enable --now arbitron`,
   `loginctl enable-linger "$USER"`.
5. Add `register_service`: dispatch on OS; implement `service_present`,
   `service_stop`, `service_restart`, `service_remove` as per-OS helpers (replacing
   Task 1's `has_existing_service` stub).
6. Add `health_check <port>`: bounded retry loop curling the local URL; on success
   continue to summary, on failure `die` with a pointer to `$(layout_logs)`.
7. Complete `run_upgrade`: download+verify new binary (Task 3), stop, swap, restart,
   health-check, print the migration caveat.
8. Complete `run_reconfigure`: Q&A (pre-filled) → rewrite config → restart →
   health-check (data + master key untouched).
9. Complete `run_uninstall`: stop + remove service, binary, config; prompt for data
   removal (default keep).
10. Complete `print_summary`: dashboard URL (`http://127.0.0.1:<port>`), service name,
    log path, next steps.
11. Run `shellcheck`; write the three bats files; make green.

## Files

- `install.sh` (modified) — service rendering/registration, health check, lifecycle
  handlers, summary.
- `tests/install/service_macos.bats` (new).
- `tests/install/service_linux.bats` (new).
- `tests/install/lifecycle.bats` (new).

## Acceptance criteria

- [ ] macOS: a valid LaunchAgent plist is written and loaded; `RunAtLoad`+`KeepAlive`
      set; invokes the absolute binary with `--config <abs>`.
- [ ] Linux: a systemd `--user` unit is written, `EnvironmentFile` points at the
      secrets env, the service is enabled+started, and linger is enabled.
- [ ] Both units invoke `arbitron serve --config <abs path>` (P0 flag) and survive
      reboot (RunAtLoad / linger).
- [ ] Health check polls the local URL and fails loudly with a log pointer on timeout.
- [ ] Upgrade swaps the binary + restarts, preserving config/data/secrets, and prints
      the forward-only migration caveat.
- [ ] Reconfigure rewrites config + restarts without touching data or the master key.
- [ ] Uninstall removes service/binary/config and keeps data by default (removes only
      on explicit confirmation).
- [ ] No sudo on the local path; unit-rendering + lifecycle seams are factored for P2.
- [ ] `shellcheck install.sh` clean.

## Dependencies

- Depends on: Tasks 1–3 (helpers/layout, config + secrets on disk, binary installed).
- Required by: Task 5 (tests); P2 reuses the unit-render + lifecycle seams.
