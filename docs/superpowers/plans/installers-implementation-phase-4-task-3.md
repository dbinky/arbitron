# Installers P4 — Task 3: Windows Service registration + health-check + Upgrade/Reconfigure/Uninstall (shared)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: write the Pester test first, watch it fail, then implement.

**Goal:** Register a native auto-start **Windows Service** (no third-party shim) running `arbitron.exe serve --config %ProgramData%\Arbitron\config.yaml` as LocalSystem or the dedicated service account; perform a post-start health-check against `http(s)://host:port`; and implement the shared **Upgrade**, **Reconfigure**, and **Uninstall** flows. The binary is service-aware via **P4 Task 0** (the Go change in this phase that adds `golang.org/x/sys/windows/svc` dispatch — NOT shipped by P0), so it dispatches under the SCM directly. **This task hard-depends on P4 Task 0 landing first** (the registered service only works because the binary detects the SCM). If Task 0 fell back to a Scheduled Task / WinSW wrapper, register via that mechanism instead (see Task 0 "Fallback").

**Design refs:** `installers-design-phase-4.md` (§Service definition, §Secrets handling, §Upgrade / reconfigure / uninstall); master design §Service definitions (Windows Service), §Upgrade/reconfigure/uninstall; Linux mirror in `installers-design-phase-2.md` (systemd system unit + shared flows).

---

## Context

**Delivers:**
- Native Windows Service registration via `New-Service` (fallback `sc.exe`):
  - `BinaryPathName` = `"%ProgramFiles%\Arbitron\arbitron.exe" serve --config "%ProgramData%\Arbitron\config.yaml"` (quoted, absolute).
  - `StartupType = Automatic` (auto-start, reboot-persistent).
  - Run-as: `LocalSystem` (default) **or** the dedicated account from Task 1 (`-Credential` / `sc.exe config obj=`), with "Log on as a service" granted.
  - Service name `arbitron` (or chosen).
- Service environment wiring so the secrets from Task 2 (master key / API key via DPAPI machine scope or service env) reach the running service.
- Start the service and **health-check** `http(s)://<host>:<port>` (scheme from the TLS choice) with bounded retries; fail with diagnostics (point at `%ProgramData%\Arbitron\logs\`) if it never comes healthy.
- Summary output: dashboard URL, service name, log path, next steps (and tunnel/proxy guidance when applicable).
- **Upgrade:** stop service → swap `%ProgramFiles%\Arbitron\arbitron.exe` → checksum-verify the new binary → restart; surface the forward-only-migration caveat.
- **Reconfigure:** re-run Task 2 Q&A pre-filled from existing config → rewrite `%ProgramData%\Arbitron\config.yaml` → restart service; preserve data + secrets.
- **Uninstall:** stop + remove the Windows Service → remove binary + config → prompt before removing `%ProgramData%\Arbitron\data` (**default keep**); optionally remove the dedicated `arbitron` account.

**Depends on:** **P4 Task 0** (native `windows/svc` awareness in the binary — the Go change this phase delivers; MUST land before this task). P4 **Task 1** (run-as/account, layout, elevation) and **Task 2** (config.yaml + secrets). **P0 (merged):** `--config` only. P3 download+checksum-verify (reused by Upgrade).

**Parallel to P2:** the shared Upgrade/Reconfigure/Uninstall flows mirror the systemd-system versions; keep behavior (default-keep data, forward-only caveat, optional account removal) aligned.

---

## Test plan (write first)

Pester suite `tests/install/Shared.Service.Tests.ps1`. Mock `New-Service`, `sc.exe`, `Get-Service`/`Start-Service`/`Stop-Service`/`Remove-Service`, the health-check HTTP call (`Invoke-WebRequest`), the download/checksum helper, and DPAPI/env writes.

- [ ] **New-Service called with auto-start:** assert `New-Service` (or `sc.exe create`) invoked once with `StartupType Automatic` and `Name = arbitron`.
- [ ] **BinaryPathName passes serve --config:** assert the binary path is the quoted `%ProgramFiles%\Arbitron\arbitron.exe` followed by `serve --config "%ProgramData%\Arbitron\config.yaml"`.
- [ ] **Run-as LocalSystem (default):** when Task 1 chose LocalSystem, the service is registered as LocalSystem (no credential / `obj= LocalSystem`).
- [ ] **Run-as dedicated account:** when a dedicated account was chosen, `New-Service -Credential` (or `sc.exe config obj=.\arbitron`) uses that account; "Log on as a service" right is granted (assert the grant call).
- [ ] **Service-aware binary (P4 Task 0) — no shim:** assert no WinSW/nssm download or wrapper is referenced anywhere in the Shared service path (relies on the Task-0 binary; only applies when Task 0 shipped the native path rather than a documented fallback).
- [ ] **Start + health-check success:** `Start-Service` then `Invoke-WebRequest` to the correct scheme/host/port returns healthy → success path; URL scheme is `https` for direct-TLS/autocert, `http` otherwise.
- [ ] **Health-check failure diagnostics:** health check never healthy → non-zero exit and message pointing at `%ProgramData%\Arbitron\logs\`.
- [ ] **Upgrade flow:** existing install detected; assert order Stop-Service → download+checksum-verify → replace `arbitron.exe` → Start-Service; forward-only caveat printed. Assert the swap does not touch `config.yaml` or data.
- [ ] **Upgrade checksum mismatch aborts:** if checksum-verify fails, the old binary is left in place and the service is restarted (or never stopped); non-zero exit.
- [ ] **Reconfigure flow:** Q&A pre-filled → `config.yaml` rewritten under `%ProgramData%` → service restarted; data + secrets untouched (no DPAPI re-generate of master key).
- [ ] **Uninstall removes service + binary + config:** `Stop-Service` + `Remove-Service` (or `sc.exe delete`) + binary + config removed; **data kept by default** (assert `data\` not removed unless confirmed).
- [ ] **Uninstall data removal on confirm:** when the operator confirms, `data\` is removed.
- [ ] **Uninstall optional account removal:** dedicated account is removed only when confirmed (`Remove-LocalUser` called/ not called accordingly).
- [ ] **Summary output:** dashboard URL, service name, log path present on success.
- [ ] **PSScriptAnalyzer:** clean.

---

## Implementation steps

- [ ] **Step 1:** Write the failing Pester cases in `tests/install/Shared.Service.Tests.ps1`; confirm red.
- [ ] **Step 2:** Implement `Register-ArbitronService`: build the quoted `BinaryPathName`; call `New-Service -Name arbitron -StartupType Automatic -BinaryPathName ...`; for the dedicated account add `-Credential`. Fall back to `sc.exe create`/`config` where `New-Service` lacks an option. Grant "Log on as a service" for the dedicated account (`secedit`/`ntrights`-equivalent or LSA policy via P/Invoke helper) — keep it mockable.
- [ ] **Step 3:** Wire secrets into the service environment (or rely on DPAPI machine-scope reads performed by the binary). Ensure no secret is passed on the command line.
- [ ] **Step 4:** `Start-Service arbitron`; reuse P3's health-check function **`Test-ArbitronHealth`** (P3 Task 4's canonical name — extend it to take a scheme/host so the shared `https`/non-loopback case works; do NOT introduce a separate `Wait-HealthCheck`) with bounded retry/backoff; on failure, emit guidance pointing to `%ProgramData%\Arbitron\logs\` and exit non-zero.
- [ ] **Step 5:** Reuse P3's summary function **`Write-InstallSummary`** (P3 Task 4's canonical name — NOT a new `Write-Summary`): dashboard URL, service name, log path, next steps (+ tunnel/proxy guidance when the proxy TLS path was chosen).
- [ ] **Step 6:** **Upgrade** flow (entered from existing-install detection): `Stop-Service` → reuse P3 download+checksum-verify → replace `arbitron.exe` → `Start-Service`; abort and restore on checksum mismatch; print forward-only-migration caveat.
- [ ] **Step 7:** **Reconfigure** flow: invoke Task 2 Q&A in reconfigure mode → rewrite `config.yaml` → `Restart-Service`; preserve data + existing secrets (do not regenerate the master key).
- [ ] **Step 8:** **Uninstall** flow: `Stop-Service` + `Remove-Service`/`sc.exe delete` → remove binary + config → prompt for data removal (default keep) → optional dedicated-account removal.
- [ ] **Step 9:** Pester green; `Invoke-ScriptAnalyzer` clean.

---

## Files

- **Modify:** `install.ps1` — `Register-ArbitronService` (install-type switch: native Windows Service for Shared), "Log on as a service" grant, secrets/service-env wiring, the extended `Test-ArbitronHealth` + `Write-InstallSummary` (P3's names, parameterized for the shared scheme/paths), shared Upgrade/Reconfigure/Uninstall flows.
- **Create:** `tests/install/Shared.Service.Tests.ps1`.

---

## Acceptance criteria

- [ ] Native auto-start Windows Service registered (`New-Service`/`sc.exe`) running `arbitron.exe serve --config %ProgramData%\Arbitron\config.yaml`; **no** third-party shim.
- [ ] Runs as LocalSystem (default) or the dedicated account with "Log on as a service".
- [ ] Health-check uses the correct scheme/host/port; failure produces actionable diagnostics.
- [ ] Upgrade swaps the binary with checksum verification and restarts; config/data untouched; forward-only caveat surfaced.
- [ ] Reconfigure rewrites config and restarts, preserving data + secrets.
- [ ] Uninstall removes service/binary/config, keeps data by default, and optionally removes the account.
- [ ] Pester green; PSScriptAnalyzer clean.

---

## Dependencies

- **P4 Task 0** (REQUIRED FIRST): native `windows/svc` service awareness in the binary.
- **P4 Task 1** (run-as/account, layout, elevation) and **Task 2** (config + secrets).
- **P0 (merged):** `--config` (the service-awareness is P4 Task 0, not P0).
- **P3:** download + checksum-verify (reused by Upgrade).
- **Parallel to P2** shared service/Upgrade/Reconfigure/Uninstall flows.
