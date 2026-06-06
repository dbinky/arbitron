# P3 — Task 5: Pester + PSScriptAnalyzer harness, CI wiring, end-to-end mocked run

> Phase **P3** (`install.ps1` local, Windows) — PowerShell mirror of P1's test
> harness task. Master design: `docs/superpowers/specs/2026-06-05-installers-design.md`.
> Phase design: `docs/superpowers/specs/installers-design-phase-3.md`.

## Context

**Delivers:** the consolidated test harness for `install.ps1` — a shared Pester
bootstrap/helpers module, an end-to-end **fully mocked** fresh-install test that
exercises the whole flow (deps → Q&A → binary → config/secrets → Scheduled Task →
health → summary) with zero real side effects, the PSScriptAnalyzer lint gate, and
the CI job that runs both on a Windows runner.

Tasks 1–4 each ship their own focused Pester suite (TDD, written first). This task
ties them together, adds the end-to-end happy-path + reconfigure + uninstall mocked
runs, and guarantees the lint gate is enforced in CI. It is the analogue of the
P1 `bats` + `shellcheck` CI wiring, on the PowerShell side.

**Mirrors P1:** same intent (mocked package manager / service / downloads; assert
config + service definition; lint clean), executed with `Pester` +
`PSScriptAnalyzer` instead of `bats` + `shellcheck`.

**Depends on:** Tasks 1–4 (all functions and their unit suites).

## Test plan (write first)

Shared helper module `tests/install/_Common.psm1` plus
`tests/install/Install.E2E.Tests.ps1`.

- **shared mock factory:** `_Common.psm1` exposes helpers to set up the standard
  mock environment (temp `$env:LOCALAPPDATA`, mocked `winget`, `npm`,
  `Register-ScheduledTask`/`Start-ScheduledTask`, `Invoke-WebRequest`,
  `Get-Command`, `Read-Host`, RNG seam) so each suite gets a consistent sandbox.
- **E2E fresh install (mocked):** with all deps reported missing, drives a full
  `Invoke-Main` (no existing install) answering: port 8080, subscription, skip-perms
  No, default data dir. Asserts, end to end:
  - winget invoked for each missing dependency;
  - `arbitron.exe` downloaded AND `Test-Sha256` passed before placement;
  - `config.yaml` written with absolute paths + `server.host: 127.0.0.1` +
    `llm.provider: claude-cli`;
  - `Register-ScheduledTask` called once with at-logon trigger and the
    `serve --config` action;
  - master key generated, stored in user env, and **absent from all captured
    output and from config.yaml**;
  - health-check invoked; summary printed with dashboard URL + task name + log path.
- **E2E API-key variant:** same flow choosing API key; asserts `llm.provider:
  anthropic`, key stored to secrets sink, key absent from config.yaml and logs.
- **E2E reconfigure (mocked):** with an existing install present, action
  `Reconfigure` re-runs Q&A pre-filled from a seeded config, rewrites config,
  restarts the task, and does NOT re-download the binary or regenerate the master
  key.
- **E2E uninstall (mocked):** action `Uninstall` unregisters the task, removes
  binary + config + user-env secrets, and keeps the data dir by default.
- **non-interactive `-Yes`:** a fresh install run with `-Yes` uses all defaults
  without prompting (assert `Read-Host` never called).
- **lint gate:** `Install.Lint.Tests.ps1` runs `Invoke-ScriptAnalyzer` over
  `install.ps1` and all `tests/install/*.ps1` with the repo settings; asserts zero
  Error/Warning diagnostics (fails the suite otherwise).

## Implementation steps

1. Add `tests/install/_Common.psm1`: sandbox setup (temp dirs, env overrides),
   standard `Mock` registrations, output-capture helper, RNG seam injector, and a
   teardown that restores env vars and removes temp dirs.
2. Write `tests/install/Install.E2E.Tests.ps1` with the fresh/api-key/reconfigure/
   uninstall/non-interactive scenarios above, using `_Common.psm1`.
3. Write `tests/install/Install.Lint.Tests.ps1` invoking PSScriptAnalyzer with
   `tests/install/PSScriptAnalyzerSettings.psd1` (from Task 1) across the script
   and tests.
4. Add a Pester config (`tests/install/PesterConfiguration.psd1` or inline in CI)
   selecting `tests/install`, enabling code-coverage on `install.ps1` if desired,
   and failing on any test failure.
5. CI: add a GitHub Actions job (Windows runner, e.g. `windows-latest`) that
   installs the pinned Pester 5.x + PSScriptAnalyzer, runs
   `Invoke-Pester -Configuration …`, and fails the build on any failure or lint
   diagnostic. Run it on changes to `install.ps1` / `tests/install/**`.
6. Document the local test command in the test files' headers
   (`Invoke-Pester ./tests/install`).

## Files

- `tests/install/_Common.psm1` (new) — shared mock/sandbox helpers.
- `tests/install/Install.E2E.Tests.ps1` (new) — end-to-end mocked flows.
- `tests/install/Install.Lint.Tests.ps1` (new) — PSScriptAnalyzer gate.
- `tests/install/PesterConfiguration.psd1` (new, optional) — Pester run config.
- CI workflow (new or modified, e.g. `.github/workflows/installers.yml`) — Windows
  Pester + analyzer job.
- `install.ps1` (modified only if seams are needed to make a step mockable).

## Acceptance criteria

- [ ] A fully mocked end-to-end fresh install passes, asserting deps via winget,
      verified binary download, correct `config.yaml`, at-logon Scheduled Task,
      master key generated + stored + never logged, health-check + summary.
- [ ] API-key, reconfigure, uninstall, and non-interactive `-Yes` E2E scenarios
      pass.
- [ ] No test performs a real install, download, registry/env write outside a
      sandbox, or scheduled-task registration.
- [ ] PSScriptAnalyzer reports zero errors/warnings across script + tests.
- [ ] CI runs Pester + analyzer on a Windows runner and gates the build.

## Dependencies

- **Tasks 1–4** (all `install.ps1` functions + their suites).
- Conceptual mirror of **P1**'s `bats` + `shellcheck` CI wiring.
- Feeds **P4** (Shared) which extends these suites with the system-level scenarios.
