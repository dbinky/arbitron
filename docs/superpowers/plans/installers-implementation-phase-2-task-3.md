# Installers P2 — Task 3: systemd system unit + health check + shared upgrade/reconfigure/uninstall

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.
> Write the bats tests FIRST, watch them fail, then implement. `shellcheck` clean.
> Extends `install.sh` from P1 and Tasks 1–2. Steps use `- [ ]` checkboxes.

Design refs: `docs/superpowers/specs/2026-06-05-installers-design.md` (§"Service
definitions" / §"Upgrade / reconfigure / uninstall") and
`docs/superpowers/specs/installers-design-phase-2.md`.

## Context

Delivers the run-as-a-service layer for the shared install:

- The **systemd system unit** at `/etc/systemd/system/arbitron.service` with
  `User=arbitron`, `EnvironmentFile=/etc/arbitron/arbitron.env`,
  `WorkingDirectory=/var/lib/arbitron`, the P0 `--config /etc/arbitron/config.yaml`
  flag, and the security hardening directives.
- `systemctl daemon-reload` + `enable --now` to start at boot, then a **health check**
  against the configured `http(s)://host:port` (honoring TLS choice from Task 2).
- The three lifecycle flows at system scope: **Upgrade** (swap binary, restart),
  **Reconfigure** (rewrite config, restart — Q&A reuse from Task 2), **Uninstall**
  (stop/disable/remove unit + binary + config; prompt before removing
  `/var/lib/arbitron` data, default keep; optional `arbitron` account removal).

This completes the Task 1 `run_shared_install` orchestrator (the unit + health-check
TODOs) and adds the shared lifecycle dispatch alongside P1's local lifecycle.

## Service unit — exact required directives

```ini
[Unit]
Description=Arbitron
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=arbitron
Group=arbitron
EnvironmentFile=/etc/arbitron/arbitron.env
WorkingDirectory=/var/lib/arbitron
ExecStart=/usr/local/bin/arbitron serve --config /etc/arbitron/config.yaml
Restart=on-failure
# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/arbitron /var/log/arbitron

[Install]
WantedBy=multi-user.target
```

Notes:
- `User`/`Group` use the chosen service-account name (`ARB_SVC_USER`), not hardcoded
  `arbitron`, when a custom name was selected in Task 2.
- `ReadWritePaths` must include `/var/log/arbitron` (logs) in addition to
  `/var/lib/arbitron` — `ProtectSystem=strict` makes the rest of the FS read-only.
- If autocert (Task 2) is in use, `ReadWritePaths` must also include the autocert cache
  dir (default `/var/lib/arbitron/autocert`, already covered if under `/var/lib/arbitron`).
- Binding to port 443 (autocert/direct-TLS) as the `arbitron` (non-root) user requires a
  privileged-port capability: add `AmbientCapabilities=CAP_NET_BIND_SERVICE` to the unit
  whenever the chosen port < 1024. Cover this in tests.

## Test plan (write first)

New bats file `tests/install/shared_service.bats`. Mock `systemctl` (record argv +
return success), `curl`/`wget` (health check), `chown`/`chmod`/`rm`/`userdel`.

**Unit generation:**
1. `arbitron.service` contains `User=arbitron`, `Group=arbitron`.
2. Contains `EnvironmentFile=/etc/arbitron/arbitron.env`.
3. Contains `WorkingDirectory=/var/lib/arbitron`.
4. `ExecStart` includes `/usr/local/bin/arbitron serve` and
   `--config /etc/arbitron/config.yaml` (P0 flag).
5. Hardening present: `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`,
   and `ReadWritePaths=` listing `/var/lib/arbitron` and `/var/log/arbitron`.
6. Custom service account: when `ARB_SVC_USER=svc-arb`, the unit uses `User=svc-arb` /
   `Group=svc-arb`.
7. Privileged port: when port is 443, the unit includes
   `AmbientCapabilities=CAP_NET_BIND_SERVICE`; when port is 8080 it does NOT.
8. Unit file is written to `/etc/systemd/system/arbitron.service` mode 0644 root:root.

**Service activation:**
9. After writing the unit, `systemctl daemon-reload` then `systemctl enable --now
   arbitron` are invoked (assert argv order via recorded call log).

**Health check:**
10. Health check targets the configured scheme/host/port: HTTP `8080` →
    `http://127.0.0.1:8080/...`; direct-TLS/autocert → `https://...` (assert the URL the
    mocked curl was called with; for TLS the check may use `-k`/`--insecure` for a
    self-signed/autocert-pending cert — assert that flag is present on the TLS path).
11. Health check failure (mocked curl non-zero) surfaces a clear error + points at
    `journalctl -u arbitron` and the log dir; exit non-zero.

**Upgrade:**
12. Upgrade swaps `/usr/local/bin/arbitron` via `download_verified` + `install`, then
    `systemctl restart arbitron` (assert no config rewrite, no account changes).
13. Upgrade surfaces the forward-only migration caveat in printed notes (assert stdout
    contains the one-way down-migration warning).

**Reconfigure:**
14. Reconfigure re-runs Task 2's Q&A (pre-filled), rewrites `/etc/arbitron/config.yaml`,
    preserves `arbitron.env` master key + `/var/lib/arbitron` data, then
    `systemctl restart arbitron`.

**Uninstall:**
15. Uninstall: `systemctl disable --now arbitron`, removes
    `/etc/systemd/system/arbitron.service`, `systemctl daemon-reload`, removes
    `/usr/local/bin/arbitron` and `/etc/arbitron/*`.
16. Uninstall prompts before removing `/var/lib/arbitron`; **default keep** (assert data
    dir not removed when prompt declined/defaulted).
17. Uninstall optionally removes the service account: when confirmed, `userdel
    arbitron` is invoked; when declined, it is not.

18. **shellcheck clean.**

## Implementation steps

- [ ] **Step 1:** `write_systemd_unit()` — heredoc the unit using `ARB_SVC_USER`,
  `ARB_ETC`, `ARB_LIB`, `ARB_LOG`, the resolved `ExecStart`, and conditional
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` when port < 1024. Write to
  `/etc/systemd/system/arbitron.service` (0644 root:root via `install`).
- [ ] **Step 2:** `enable_systemd_unit()` — `systemctl daemon-reload` then
  `systemctl enable --now arbitron`.
- [ ] **Step 3:** `health_check_shared()` — derive scheme from `TLS_ENABLED`, build URL
  from host/port, curl with a retry/backoff loop; on TLS use `-k` (cert may be
  self-signed/autocert-pending); on failure print `journalctl -u arbitron` + log-dir
  guidance and return non-zero.
- [ ] **Step 4:** Complete `run_shared_install` (Task 1 TODOs): after config/env are
  written → `write_systemd_unit` → `enable_systemd_unit` → `health_check_shared` →
  print summary (dashboard URL, service name `arbitron`, log location, next steps incl.
  tunnel guidance when edge mode).
- [ ] **Step 5:** `upgrade_shared()` — `download_verified` → `install` new binary →
  `systemctl restart arbitron`; print forward-only-migration caveat.
- [ ] **Step 6:** `reconfigure_shared()` — re-run Task 2 Q&A (pre-filled) →
  `write_shared_config` (preserve env/data) → `systemctl restart arbitron`.
- [ ] **Step 7:** `uninstall_shared()` — `systemctl disable --now arbitron` → remove
  unit → `daemon-reload` → remove binary + `/etc/arbitron` → prompt to remove
  `/var/lib/arbitron` (default keep) → prompt to `userdel` the service account
  (default keep).
- [ ] **Step 8:** Dispatch: wire `upgrade_shared` / `reconfigure_shared` /
  `uninstall_shared` into the install-state dispatcher for the Shared path (mirrors
  P1's local dispatch). Iterate until bats pass + `shellcheck` clean.

## Files

- Modified: `install.sh` — `write_systemd_unit`, `enable_systemd_unit`,
  `health_check_shared`, completed `run_shared_install`, `upgrade_shared`,
  `reconfigure_shared`, `uninstall_shared`, shared lifecycle dispatch.
- Created: `tests/install/shared_service.bats`.
- Reuse: P1 `download_verified`, install-state dispatcher, summary/printing helpers;
  Task 2 Q&A + `write_shared_config`/`write_shared_env`.

## Acceptance criteria

- [ ] systemd unit at `/etc/systemd/system/arbitron.service` has `User`/`Group` =
  service account, `EnvironmentFile=/etc/arbitron/arbitron.env`,
  `WorkingDirectory=/var/lib/arbitron`, `--config /etc/arbitron/config.yaml`, and the
  hardening directives (`NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`,
  `ReadWritePaths` covering lib + log).
- [ ] `CAP_NET_BIND_SERVICE` added only when binding a privileged port (443).
- [ ] `daemon-reload` + `enable --now` run; health check honors TLS scheme and reports
  failures with journalctl/log guidance.
- [ ] Upgrade swaps binary + restarts (no config/account churn) and prints the
  forward-only migration caveat.
- [ ] Reconfigure rewrites config + restarts, preserving master key + data.
- [ ] Uninstall removes unit/binary/config, keeps data by default, optionally removes
  the service account.
- [ ] bats green; `shellcheck install.sh` clean.

## Dependencies

- **Task 1 (this phase):** `run_shared_install`, path globals, `ARB_SVC_USER`, account.
- **Task 2 (this phase):** config/env writers + Q&A (for reconfigure) and the
  `TLS_ENABLED`/host/port variables that drive the unit + health check.
- **P0 (merged):** the `--config` flag the unit passes; `server.tls` behavior the health
  check scheme depends on.
- **P1 tasks:** `download_verified` (upgrade), the install-state dispatcher pattern,
  and summary-printing helpers.
