# P3 — Task 1: `install.ps1` skeleton, OS/arch detection, install-type dispatch

> Phase **P3** (`install.ps1` local, Windows) — PowerShell mirror of P1 Task 1.
> Master design: `docs/superpowers/specs/2026-06-05-installers-design.md`.
> Phase design: `docs/superpowers/specs/installers-design-phase-3.md`.

## Context

**Delivers:** the `install.ps1` entry-point skeleton at repo root — platform/arch
detection, install-type pinned to `Local`, existing-install detection, and the
`Fresh | Upgrade | Reconfigure | Uninstall` dispatch. Plus the shared helper
library every later task reuses: structured logging (with secret redaction),
interactive prompt helpers, and a checksum-verify helper. Establishes the Pester +
PSScriptAnalyzer test harness.

**Mirrors P1 Task 1** (`install.sh` skeleton): identical control flow shape —
`detect OS/arch → install type = Local → detect existing install → branch`. Keep
function names and the dispatch table parallel to the bash version so both
installers read the same.

**Depends on:** P0 (merged) — the `arbitron.exe` binary honors `--config` /
`ARBITRON_CONFIG`. Nothing else.

**Feeds P4:** factor every helper and the detection/dispatch so a future `Shared`
install-type can reuse them. Specifically: `Get-InstallType` must return an enum
(`Local`/`Shared`) even though P3 only ever yields `Local`; path resolution must
go through a single `Get-ArbitronPaths -InstallType` function so P4 can supply the
`%ProgramFiles%` / `%ProgramData%` layout without touching call sites.

## Test plan (write first)

Pester suite `tests/install/Install.Skeleton.Tests.ps1`. Dot-source `install.ps1`
with a guard so sourcing defines functions without running `Invoke-Main` (e.g.
`install.ps1` ends with `if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }`,
or gate on a `$env:ARBITRON_INSTALL_NOEXEC` flag the tests set).

- **OS guard:** `Assert-SupportedPlatform` throws a clear error when
  `$IsWindows` is `$false` (mock `$IsWindows`); succeeds on Windows.
- **Arch detection:** `Get-Architecture` returns `amd64` for `AMD64` and `arm64`
  for `ARM64` (mock `$env:PROCESSOR_ARCHITECTURE` / `RuntimeInformation`); throws
  on an unknown arch.
- **Install type:** `Get-InstallType` returns `Local` (no elevation probed —
  local needs none).
- **Paths:** `Get-ArbitronPaths -InstallType Local` returns absolute paths rooted
  at a mocked `$env:LOCALAPPDATA\Arbitron` for `Bin`, `Config`, `Data`,
  `Workspaces`, `Logs`; asserts `Config` ends in `config.yaml` and all paths are
  rooted (`[System.IO.Path]::IsPathRooted`).
- **Existing-install detection:** `Test-ExistingInstall` returns `$false` when
  config absent; `$true` when a mocked `config.yaml` exists AND the scheduled task
  is registered (mock `Get-ScheduledTask`).
- **Dispatch — fresh:** with no existing install, `Invoke-Main` calls
  `Invoke-FreshInstall` (mock all four flow functions; assert exactly one called).
- **Dispatch — existing:** with an existing install, the action prompt drives the
  branch — mock `Read-Host`/prompt helper to return `Upgrade`, assert
  `Invoke-Upgrade` called; repeat for `Reconfigure` → `Invoke-Reconfigure`,
  `Uninstall` → `Invoke-Uninstall`.
- **Logging redaction:** `Write-Log` (or `Write-ArbitronLog`) given a message
  containing a value tagged secret never emits the secret to the transcript —
  assert via captured `Write-Host`/stream that the master-key / API-key value is
  absent. (Concrete redaction proven in Task 3; here assert the helper exists and
  masks a `-Secret` argument.)
- **Checksum helper:** `Test-Sha256` returns `$true` when a temp file's computed
  `Get-FileHash -Algorithm SHA256` matches the expected hash (case-insensitive),
  `$false` on mismatch; throws/returns false on a missing file. Use a real temp
  file with a known SHA-256.
- **PSScriptAnalyzer:** a meta-test runs `Invoke-ScriptAnalyzer -Path install.ps1`
  with the repo settings and asserts zero `Error`/`Warning` diagnostics.

## Implementation steps

1. Create `install.ps1` at repo root. Header: `#Requires -Version 5.1`,
   `[CmdletBinding()]` param block, `Set-StrictMode -Version Latest`,
   `$ErrorActionPreference = 'Stop'`. Accept overridable inputs as params with env
   fallbacks: `-Version` (`$env:ARBITRON_VERSION`), `-Config`
   (`$env:ARBITRON_CONFIG`), non-interactive/`-Yes` switch.
2. `Assert-SupportedPlatform`: throw unless running on Windows (`$IsWindows` on
   PS7, or `[Environment]::OSVersion.Platform` on PS5.1).
3. `Get-Architecture`: map `$env:PROCESSOR_ARCHITECTURE` /
   `[RuntimeInformation]::OSArchitecture` → `amd64` | `arm64`; throw on others.
4. `Get-InstallType`: return `'Local'` (constant for P3; the function exists so P4
   can extend it to detect/choose `Shared`).
5. `Get-ArbitronPaths -InstallType`: switch on type; for `Local` build a hashtable
   of absolute paths under `Join-Path $env:LOCALAPPDATA 'Arbitron'`
   (`Bin = …\bin`, `BinaryExe = …\bin\arbitron.exe`, `Config = …\config.yaml`,
   `Data = …\data`, `Workspaces = …\workspaces`, `Logs = …\logs`,
   `ClaudeConfigDir = …\claude`). Single source of truth for all later tasks.
6. Shared helpers:
   - `Write-ArbitronLog -Level <Info|Warn|Error> -Message [-Secret]`: timestamped
     console output; when `-Secret` given, never include its value. Color via
     `Write-Host` foreground.
   - Prompt helpers: `Read-PromptDefault -Prompt -Default`,
     `Read-PromptChoice -Prompt -Choices -Default`,
     `Read-PromptYesNo -Prompt -Default`. All honor the `-Yes`/non-interactive
     switch (return defaults without prompting).
   - `Test-Sha256 -Path -ExpectedHash`: `Get-FileHash -Algorithm SHA256`,
     case-insensitive compare; the single gate every download must pass.
7. `Test-ExistingInstall`: true iff config file exists AND
   `Get-ScheduledTask -TaskName 'Arbitron'` resolves (wrap in try/catch).
8. Flow stubs (filled in later tasks): `Invoke-FreshInstall`, `Invoke-Upgrade`,
   `Invoke-Reconfigure`, `Invoke-Uninstall` — each logs "not yet implemented" for
   now so the dispatch is testable.
9. `Invoke-Main`: orchestrate detect → existing-install check → if none →
   `Invoke-FreshInstall`; else prompt the action (default `Upgrade`) and dispatch.
   Guard the bottom-of-file auto-invoke so dot-sourcing in tests does not run it.
10. Add `tests/install/PSScriptAnalyzerSettings.psd1` (severity, excluded rules if
    any — keep none excluded unless justified) and wire the analyzer meta-test.

## Files

- `install.ps1` (new) — skeleton, detection, dispatch, shared helpers.
- `tests/install/Install.Skeleton.Tests.ps1` (new).
- `tests/install/PSScriptAnalyzerSettings.psd1` (new).

## Acceptance criteria

- [ ] `install.ps1` exists at repo root and is dot-sourceable without side effects.
- [ ] OS/arch detection: rejects non-Windows; maps amd64/arm64; throws on unknown.
- [ ] `Get-ArbitronPaths -InstallType Local` yields absolute `%LOCALAPPDATA%\Arbitron`
      paths covering bin/config/data/workspaces/logs.
- [ ] Existing-install detection requires both config and scheduled task.
- [ ] Dispatch routes Fresh/Upgrade/Reconfigure/Uninstall to the right function.
- [ ] Logging, prompt, and checksum helpers exist and pass their unit tests.
- [ ] Helpers/detection are factored (enum install-type, single path function) so
      P4 can add a `Shared` branch without editing call sites.
- [ ] All Pester tests green; PSScriptAnalyzer reports zero errors/warnings.

## Dependencies

- **P0** (merged): `--config` / `ARBITRON_CONFIG`.
- Conceptual mirror of **P1 Task 1**; keep function names + dispatch parallel.
- Feeds every later P3 task and the P4 Shared branch.
