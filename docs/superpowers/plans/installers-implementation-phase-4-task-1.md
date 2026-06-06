# Installers P4 — Task 1: Shared-path skeleton (Windows server)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. STRICT TDD: write the Pester test first, watch it fail, then implement.

**Goal:** Add the system-level **Shared** branch entry point to `install.ps1` (built in P3): admin-elevation requirement/check, the `%ProgramFiles%` / `%ProgramData%` directory layout, the LocalSystem-vs-dedicated-service-account choice, and reuse of the P3 install-type detection / dependency-resolution / download-verify / Q&A-engine helpers. No config write, no service registration yet (Tasks 2 and 3).

**Design refs:** `docs/superpowers/specs/installers-design-phase-4.md` (§Scope, §Layout, §Privilege + service account, §Dependencies); master `docs/superpowers/specs/2026-06-05-installers-design.md` (matrix col *e*). Linux mirror: `docs/superpowers/specs/installers-design-phase-2.md` Task-equivalent (P2 privilege/account skeleton).

---

## Context

**Delivers:**
- The "install type: Local | Shared" branch selecting the Windows **Shared** path inside the existing `install.ps1`.
- A fail-fast admin-elevation check (Shared requires elevation; print actionable guidance and exit non-zero when not elevated).
- The shared path layout constants/resolver: binary under `%ProgramFiles%\Arbitron\arbitron.exe`; config/data/workspaces/logs under `%ProgramData%\Arbitron\` (`config.yaml`, `data\`, `workspaces\`, `logs\`).
- The **run-as** choice: LocalSystem (default) **or** a dedicated service account (default name `arbitron`); for the dedicated-account branch, create the local account if absent (placeholder for Task 3 to consume; account name captured here).
- Creation of the `%ProgramData%\Arbitron\` tree with appropriate ACLs (admin/SYSTEM full; service account read/write on data/logs).

**Depends on (P3 — name them):**
- P3 **install-type detection** helper (the `Local | Shared` prompt/parse).
- P3 **dependency resolution** (`node`, `claude`, `git`, `gh`; `opencode` optional) — reused unchanged; runs after the Shared branch is chosen.
- P3 **download + checksum-verify** helper for the binary.
- P3 **Q&A engine** primitives (`Read-Answer`/prompt helpers, default rendering) — reused by Task 2.
- P3 helper factoring requirement ("Feeds P4"): these helpers must be parameterizable by install class (Local vs Shared) so paths/elevation differ without duplicating logic.

**Consumes P0:** native Windows-service awareness (`windows/svc`) and `--config` — exercised in Task 3, declared here for the layout.

**Parallel to P2:** mirror the Linux shared privilege/account skeleton (sudo→admin; dedicated `arbitron` account; system paths).

---

## Test plan (write first)

Pester suite `tests/install/Shared.Skeleton.Tests.ps1`. Mock elevation checks, account/dir cmdlets, and any download. Dot-source `install.ps1` with a guard so it exposes functions without running the entry flow (P3 already establishes this pattern; reuse it).

- [ ] **Elevation required (fail fast):** with `Test-IsElevated` mocked to `$false`, invoking the Shared branch throws/exits non-zero and the message names how to elevate (e.g. "Run from an elevated PowerShell / 'Run as Administrator'"). Assert no directory-creation or download cmdlet was called.
- [ ] **Elevation present → proceeds:** `Test-IsElevated` → `$true`; the Shared branch proceeds to layout/account setup without error.
- [ ] **Layout constants (Shared):** the resolved paths equal `%ProgramFiles%\Arbitron\arbitron.exe` (binary) and `%ProgramData%\Arbitron\{config.yaml,data,workspaces,logs}`. Assert via the path resolver using mocked `$env:ProgramFiles` / `$env:ProgramData`.
- [ ] **Layout constants (Local unchanged):** same resolver with class=Local still returns the P3 `%LOCALAPPDATA%` paths (no regression).
- [ ] **Directory tree creation:** `New-Item`/`New-Directory` mocked; assert `%ProgramData%\Arbitron\{data,workspaces,logs}` each created.
- [ ] **Run-as default LocalSystem:** when the run-as prompt answer is empty/default, the captured run-as is `LocalSystem` and **no** local account is created (`New-LocalUser` not called).
- [ ] **Run-as dedicated account:** when the operator chooses a service account with default name, captured account is `arbitron`; if absent (`Get-LocalUser` mocked to throw/NotFound), `New-LocalUser` is called once with that name and a no-interactive-logon posture.
- [ ] **Service account name override:** a non-default name is captured and used for `New-LocalUser`.
- [ ] **ACLs applied for dedicated account:** when a dedicated account is chosen, `Set-Acl`/`icacls` is invoked granting that account read/write on `data\` and `logs\` (assert called with the account + the two paths).
- [ ] **Install-type detection reused:** the Shared branch is only entered when the P3 install-type prompt yields `Shared` (assert dispatch table routes correctly; assert P3 helper is invoked, not re-implemented).
- [ ] **PSScriptAnalyzer:** new/changed code passes `Invoke-ScriptAnalyzer` with the repo ruleset, zero errors.

---

## Implementation steps

- [ ] **Step 1:** Write the failing Pester cases above in `tests/install/Shared.Skeleton.Tests.ps1`; run `Invoke-Pester` and confirm red.
- [ ] **Step 2:** Add `Test-IsElevated` (returns `$true` when the current principal is in the built-in Administrators role via `[Security.Principal.WindowsPrincipal]`). Keep it as a thin, mockable function.
- [ ] **Step 3:** Extend the path resolver (P3) to take an install class. For `Shared`, resolve `$BinDir = Join-Path $env:ProgramFiles 'Arbitron'`, `$DataRoot = Join-Path $env:ProgramData 'Arbitron'`, and derive `ConfigPath`, `DataDir`, `WorkspacesDir`, `LogsDir` under `$DataRoot`. Return an object/hashtable both branches consume.
- [ ] **Step 4:** In the Shared branch (entered from the P3 install-type dispatch): call `Test-IsElevated`; on `$false`, `Write-Error` with elevation guidance and `exit 1` (do nothing else).
- [ ] **Step 5:** Create the `%ProgramData%\Arbitron\` tree (`data`, `workspaces`, `logs`) via the shared `New-Directory` helper (idempotent: `-Force`).
- [ ] **Step 6:** Add `Get-RunAsChoice` — prompt "Run the service as LocalSystem (default) or a dedicated account?"; on dedicated, prompt for account name (default `arbitron`). Return `{ Kind = 'LocalSystem' | 'Account'; Name }`.
- [ ] **Step 7:** For `Kind = 'Account'`: `Get-LocalUser -Name $Name`; if absent, `New-LocalUser` with a generated password, `-PasswordNeverExpires`, no interactive logon, and grant "Log on as a service" (defer the actual `secedit`/privilege grant detail to Task 3 if cleaner — capture the account here). Apply data/log ACLs via `Set-Acl`/`icacls`.
- [ ] **Step 8:** Reuse P3 dependency resolution and download+verify after the skeleton is set up (call the existing helpers; the binary is placed at `$BinDir\arbitron.exe`).
- [ ] **Step 9:** Run Pester (green) and `Invoke-ScriptAnalyzer` (clean).

---

## Files

- **Modify:** `install.ps1` (repo root) — add Shared branch, `Test-IsElevated`, install-class-aware path resolver, `Get-RunAsChoice`, ProgramData tree + ACLs, dedicated-account creation.
- **Create:** `tests/install/Shared.Skeleton.Tests.ps1`.

---

## Acceptance criteria

- [ ] Choosing **Shared** routes into the system-level branch via the P3 install-type detection (not a fork/duplicate).
- [ ] Non-elevated Shared run fails fast with actionable guidance and performs no filesystem/download side effects.
- [ ] Shared layout resolves to `%ProgramFiles%\Arbitron\arbitron.exe` + `%ProgramData%\Arbitron\{config.yaml,data,workspaces,logs}`; Local layout unchanged.
- [ ] Run-as defaults to LocalSystem; dedicated-account path creates `arbitron` (or override) when absent and ACLs data/logs for it.
- [ ] P3 dependency-resolution, download/verify, and Q&A helpers are reused, not reimplemented.
- [ ] Pester suite green; `PSScriptAnalyzer` clean.

---

## Dependencies

- **P3:** install-type detection, dependency resolution, download+checksum-verify, Q&A-engine primitives, install-class-parameterizable helpers ("Feeds P4").
- **P0:** layout assumes the service-aware binary + `--config` (consumed in Task 3).
- **Parallel to P2** (Linux shared) Task-equivalent privilege/account skeleton — keep the run-as/account semantics aligned.
