# Installers P2 — Task 1: Shared-path skeleton (elevation, account, system paths)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.
> Write the bats tests FIRST, watch them fail, then implement. `shellcheck` must stay
> clean. This task EXTENDS the `install.sh` produced in **P1**; it does not create a new
> script. Steps use checkbox (`- [ ]`) syntax for tracking.

Design refs: `docs/superpowers/specs/2026-06-05-installers-design.md` and
`docs/superpowers/specs/installers-design-phase-2.md`.

## Context

Delivers the **(d) Linux shared server** entry into the existing `install.sh`: the
"Shared" branch of the install-type decision. This task wires up everything that must
be in place *before* config Q&A and service registration:

- Elevation detection — the Shared path requires root/sudo; fail fast with guidance if
  not elevated.
- Dedicated `arbitron` system account creation (no-login shell) if absent.
- System path layout under `/etc/arbitron`, `/var/lib/arbitron`, `/var/log/arbitron`,
  binary at `/usr/local/bin/arbitron`, with correct ownership and modes.
- Reuse of P1 helpers (OS/arch detection, dependency resolution, download + checksum
  verify, logging/prompt helpers, the existing-install detector) — invoked from the
  Shared branch rather than re-implemented.

It deliberately stops short of writing config (Task 2) and the systemd unit (Task 3).

### What it depends on

- **P0 (merged):** `--config` / `ARBITRON_CONFIG`. Not exercised here directly, but the
  system paths chosen here (`/etc/arbitron/config.yaml`) are what Task 3's unit passes
  via `--config`.
- **P1 tasks (the local `install.sh`):** this task assumes P1 has already factored the
  script into reusable functions. Concretely it reuses, by name (rename to match P1's
  actual helper names if they differ):
  - `detect_os_arch` — OS/arch detection.
  - `detect_install_state` — Fresh | Upgrade | Reconfigure | Uninstall detection
    (this task passes it the **system** config/service locations).
  - `resolve_dependencies` — node/claude/git/gh resolution.
  - `download_and_verify` — release artifact download + SHA256 verification.
  - `log_info` / `log_warn` / `die` / `prompt_default` / `confirm` — UI helpers.
  - The install-type selection point where Local vs Shared is chosen.

## Test plan (write first)

New bats file `tests/install/shared_skeleton.bats`. Tests source `install.sh` with a
guard so it does not auto-run `main` (P1 must expose an `ARBITRON_INSTALL_NO_MAIN=1`
sourcing guard or equivalent `[[ "${BASH_SOURCE[0]}" == "${0}" ]]` main guard — assume
P1 provides it; if not, add it as the first step here). All privileged commands are
mocked via a `PATH`-prepended fake-bin dir (`sudo`, `useradd`, `getent`, `id`,
`install`, `chown`, `chmod`, `mkdir`) that log their argv to `$BATS_TEST_TMPDIR`.

1. **Shared path requires elevation.** With the `id -u` mock returning non-zero (UID
   1000) and no `sudo` available, invoking the Shared branch `die`s with a message
   containing "requires root" / "re-run with sudo"; exit status non-zero.
2. **Elevated run proceeds.** With `id -u` mock returning `0`, the Shared branch passes
   the elevation gate (no `die`).
3. **Account created when absent.** `getent passwd arbitron` mock returns non-zero
   (absent) → `useradd` is invoked with a system-account, no-login profile:
   assert the recorded `useradd` argv contains `--system`, `--shell /usr/sbin/nologin`
   (or `/bin/false`), `--home-dir /var/lib/arbitron`, and the account name `arbitron`.
4. **Account reused when present.** `getent passwd arbitron` mock returns `0` →
   `useradd` is **not** invoked (assert no useradd argv file written); a `log_info`
   "already exists" path is taken.
5. **Custom service account name honored.** When the service-account variable is set to
   `svc-arb` (Task 2 sources it from Q&A; here it is injected via env/global), account
   creation + ownership target `svc-arb`, not `arbitron`.
6. **System directory layout + ownership.** After the layout step, assert `mkdir`/`install`
   were called for `/etc/arbitron`, `/var/lib/arbitron`, `/var/lib/arbitron/workspaces`,
   `/var/log/arbitron`; and `chown` was called making `/var/lib/arbitron` and
   `/var/log/arbitron` owned by the service account; `/etc/arbitron` stays `root`-owned.
7. **Directory modes.** `/etc/arbitron` is `0755` root-owned; `/var/lib/arbitron` is
   `0750` service-account-owned (data not world-readable); assert the `chmod` argv.
8. **Binary install location.** `download_and_verify` output is installed to
   `/usr/local/bin/arbitron` mode `0755` root-owned (assert `install` argv).
9. **Reuses P1 detection helpers.** Stub `detect_os_arch` and `download_and_verify` and
   assert the Shared branch calls them (call-count file), proving no duplicate logic.
10. **shellcheck clean.** A bats test (or CI step) runs `shellcheck install.sh` and
    asserts exit 0.

## Implementation steps

- [ ] **Step 1:** Confirm/add a sourcing guard in `install.sh` so bats can source it
  without executing `main`. Run the new bats file; all tests fail (functions undefined).
- [ ] **Step 2:** Add Shared-path globals near P1's path constants: `ARB_PREFIX_BIN=/usr/local/bin`,
  `ARB_ETC=/etc/arbitron`, `ARB_LIB=/var/lib/arbitron`, `ARB_LOG=/var/log/arbitron`,
  `ARB_SVC_USER` (default `arbitron`, overridable; Task 2 sets it from Q&A).
- [ ] **Step 3:** `require_root()` — detect elevation (`[ "$(id -u)" -eq 0 ]`). If not
  root and `sudo` is present, `die` instructing the operator to re-run under `sudo`
  (do NOT silently self-`sudo`; keep the privilege boundary explicit). If not root and
  no `sudo`, `die`.
- [ ] **Step 4:** `ensure_service_account()` — `getent passwd "$ARB_SVC_USER"`; if
  absent, `useradd --system --no-create-home --home-dir "$ARB_LIB" --shell /usr/sbin/nologin "$ARB_SVC_USER"`
  (fall back to `/bin/false` if nologin missing). If present, `log_info` and return.
- [ ] **Step 5:** `create_system_layout()` — create `$ARB_ETC` (0755 root:root),
  `$ARB_LIB` + `$ARB_LIB/workspaces` (0750, `chown $ARB_SVC_USER:$ARB_SVC_USER`),
  `$ARB_LOG` (0750, service-account-owned). Use `install -d -m MODE -o OWNER -g GROUP`
  where available, else `mkdir -p` + `chmod` + `chown`.
- [ ] **Step 6:** `install_binary_system()` — call P1's `download_and_verify`, then
  `install -m 0755 -o root -g root <downloaded> /usr/local/bin/arbitron`.
- [ ] **Step 7:** `run_shared_install()` orchestrator (skeleton): `require_root` →
  `detect_install_state` (system locations) → `resolve_dependencies` →
  `ensure_service_account` → `create_system_layout` → `install_binary_system`. Leave
  TODO markers where Task 2 (config) and Task 3 (unit + health check) plug in.
- [ ] **Step 8:** Hook `run_shared_install` into P1's install-type selection (Shared
  branch). macOS must never reach it (guard: shared is Linux-only).
- [ ] **Step 9:** Iterate until all bats tests pass and `shellcheck install.sh` is clean.

## Files

- Modified: `install.sh` (repo root) — Shared-path globals + `require_root`,
  `ensure_service_account`, `create_system_layout`, `install_binary_system`,
  `run_shared_install` skeleton; wired into install-type selection.
- Created: `tests/install/shared_skeleton.bats`.
- Possibly created: `tests/install/helpers/mock_bin/` fakes (`sudo`, `useradd`,
  `getent`, `id`, `install`, `chown`, `chmod`, `mkdir`) + a `tests/install/helpers/load.bash`
  if P1 hasn't already established one (reuse P1's if present).

## Acceptance criteria

- [ ] Shared branch fails fast and clearly when not root.
- [ ] `arbitron` system account is created (no-login, system, home `/var/lib/arbitron`)
  only when absent; reused otherwise; honors a custom service-account name.
- [ ] System layout created with correct ownership/modes: `/etc/arbitron` 0755 root;
  `/var/lib/arbitron` (+ `workspaces`) 0750 service-account; `/var/log/arbitron` 0750
  service-account; binary at `/usr/local/bin/arbitron` 0755 root.
- [ ] Reuses P1 helpers (`detect_os_arch`, `detect_install_state`,
  `resolve_dependencies`, `download_and_verify`, UI helpers) rather than duplicating.
- [ ] macOS cannot enter the Shared path.
- [ ] All `tests/install/shared_skeleton.bats` pass; `shellcheck install.sh` clean.

## Dependencies

- **P0 (merged):** system config path is the target of `--config` used downstream.
- **P1 tasks:** the local `install.sh` skeleton — OS/arch detection, install-state
  detection, dependency resolution, download/verify, UI helpers, and the install-type
  selection point. This task assumes those exist and are factored as reusable
  functions (per P1's "Feeds P2" note). If a sourcing/main guard is missing, add it here.
