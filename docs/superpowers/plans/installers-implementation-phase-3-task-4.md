# P3 — Task 4: Scheduled Task, health-check, upgrade/reconfigure/uninstall

> Phase **P3** (`install.ps1` local, Windows) — PowerShell mirror of P1 Task 4.
> Master design: `docs/superpowers/specs/2026-06-05-installers-design.md`.
> Phase design: `docs/superpowers/specs/installers-design-phase-3.md`.

## Context

**Delivers:** the service-registration step (a per-user at-logon **Scheduled
Task**, no elevation), the post-install health-check against the loopback
dashboard, and the full lifecycle branches — **Upgrade**, **Reconfigure**,
**Uninstall** — that the Task 1 dispatch routes to. Wires together Tasks 1–3 into
`Invoke-FreshInstall` and the summary print.

**Mirrors P1 Task 4:** same lifecycle semantics (upgrade = swap binary + restart;
reconfigure = re-run Q&A pre-filled + rewrite config, preserve data + secrets;
uninstall = remove service/binary/config, prompt before deleting data, default
**keep**). The Windows service mechanism is a **Scheduled Task** (P1 uses
launchd/systemd-user). **The native Go Windows Service from P0 is NOT used here** —
that is P4 (shared). Local needs no elevation, so a per-user Scheduled Task is the
right mechanism.

**Depends on:** Task 1 (paths, helpers, dispatch stubs), Task 2 (deps), Task 3
(config + secrets + binary).

**Feeds P4:** factor `Register-ArbitronService` behind an install-type switch so
P4 can register the native Windows Service for the Shared branch while local keeps
the Scheduled Task; keep upgrade/uninstall service operations behind the same seam.

## Service definition (Scheduled Task)

At-logon task named `Arbitron`, running as the invoking user, action:
`arbitron.exe serve --config %LOCALAPPDATA%\Arbitron\config.yaml`. Registered via
`Register-ScheduledTask` with `New-ScheduledTaskTrigger -AtLogOn`,
`New-ScheduledTaskAction -Execute <bin> -Argument 'serve --config <cfg>'`,
`New-ScheduledTaskPrincipal` for the current user (interactive logon, no admin),
and `New-ScheduledTaskSettingsSet` (restart on failure, run only when network
available off). Started immediately via `Start-ScheduledTask` after registration.

## Test plan (write first)

Pester suite `tests/install/Install.Service.Tests.ps1`. Mock
`Register-ScheduledTask`, `Start-ScheduledTask`, `Stop-ScheduledTask`,
`Unregister-ScheduledTask`, `Get-ScheduledTask`, `New-ScheduledTask*` builders,
`Invoke-WebRequest` (health), and Tasks 1–3 functions.

- **at-logon trigger:** `Register-ArbitronService -InstallType Local -Paths`
  builds a trigger via `New-ScheduledTaskTrigger -AtLogOn` (assert the trigger
  builder called with at-logon).
- **action command line:** the registered action's `Execute` is the
  `…\bin\arbitron.exe` path and `Argument` contains
  `serve --config <…\config.yaml>` (assert exact `--config` path passed — P0 flag).
- **principal is current user, no elevation:** the principal uses the invoking
  user with interactive logon type and **not** `RunLevel Highest` / SYSTEM (assert
  no admin/SYSTEM principal — local path).
- **task name:** registered with `-TaskName 'Arbitron'`.
- **started after register:** `Start-ScheduledTask -TaskName 'Arbitron'` called
  after a successful `Register-ScheduledTask`.
- **health-check success:** `Test-ArbitronHealth` polls
  `http://127.0.0.1:<port>/` (or the documented health path) and returns success
  when the mocked request returns 200 within the retry window.
- **health-check timeout:** when the mock never returns 200, `Test-ArbitronHealth`
  fails after the bounded retries and the installer surfaces a clear error +
  log-path hint (assert non-zero/throw, message references the log path).
- **fresh install orchestration:** `Invoke-FreshInstall` calls, in order, deps →
  Q&A → binary download+verify → write config+secrets → register+start service →
  health-check → summary (assert call order via mocks).
- **summary output parallel to P1:** the printed summary includes dashboard URL
  (`http://127.0.0.1:<port>`), service/task name (`Arbitron`), log path, and next
  steps; assert it contains those fields (keep wording parallel to install.sh).
- **upgrade:** `Invoke-Upgrade` downloads + verifies the new exe, stops the task,
  swaps the binary, restarts the task; does NOT touch config/data/secrets (assert
  config writer + secret setter NOT called).
- **upgrade migration caveat surfaced:** upgrade prints the forward-only
  down-migration caveat (assert message present).
- **reconfigure:** `Invoke-Reconfigure` loads the existing `config.yaml`, runs the
  Q&A pre-filled (Task 3 `-Defaults`), rewrites config, preserves data + secrets,
  restarts the task (assert binary download NOT called; data dir untouched;
  existing master key not regenerated).
- **uninstall keeps data by default:** `Invoke-Uninstall` stops +
  `Unregister-ScheduledTask`, removes binary + config; prompts before data removal;
  with default answer the data dir is preserved (assert data dir NOT removed).
- **uninstall removes data on opt-in:** when the user opts to remove data, the data
  dir is deleted (assert removed).
- **uninstall clears secrets:** user-env secrets (`ARBITRON_MASTER_KEY`,
  `ARBITRON_LLM_APIKEY`) are removed (assert `SetEnvironmentVariable(..., $null,
  'User')` or removal called).

## Implementation steps

1. `Register-ArbitronService -InstallType -Paths -Port`: for `Local` build action
   (`-Execute $Paths.BinaryExe -Argument "serve --config `"$($Paths.Config)`""`),
   `-AtLogOn` trigger, current-user principal (interactive, no elevation),
   settings set (restart-on-failure, allow start on battery, no idle constraints);
   `Register-ScheduledTask -TaskName 'Arbitron' -Force`; then `Start-ScheduledTask`.
   Keep the install-type switch so P4 plugs in the native service.
2. `Test-ArbitronHealth -Port [-TimeoutSeconds]`: poll `http://127.0.0.1:<port>/`
   with bounded retries + backoff; return success on 200; on timeout throw with a
   message pointing at `$Paths.Logs\arbitron.log`.
3. `Write-InstallSummary -Paths -Port`: print dashboard URL, task name, log path,
   and next steps (subscription login reminder if applicable). Keep wording
   parallel to the P1 summary.
4. `Invoke-FreshInstall`: orchestrate Tasks 2→3→service→health→summary in order.
5. `Invoke-Upgrade`: `Get-ArbitronBinary` (download+verify) → `Stop-ScheduledTask`
   → swap exe → `Start-ScheduledTask` → print migration caveat + summary. Leave
   config/data/secrets untouched.
6. `Invoke-Reconfigure`: load existing config (parse YAML to a defaults object) →
   `Invoke-ConfigQA -Defaults` → `Write-ArbitronConfig` → restart task → summary.
   Preserve data + existing secrets (do not regenerate the master key).
7. `Invoke-Uninstall`: `Stop-ScheduledTask` + `Unregister-ScheduledTask` → remove
   `$Paths.Bin` + `$Paths.Config` → remove user-env secrets → prompt for data
   removal (default **keep**) → conditionally remove `$Paths.Data`.

## Files

- `install.ps1` (modified) — service registration, health-check, summary, and the
  Fresh/Upgrade/Reconfigure/Uninstall flow bodies.
- `tests/install/Install.Service.Tests.ps1` (new).

## Acceptance criteria

- [ ] Scheduled Task registered at-logon, current user, no elevation, action runs
      `arbitron.exe serve --config <config.yaml>`, then started.
- [ ] Health-check polls loopback and fails clearly (with log-path hint) on
      timeout.
- [ ] Fresh install runs deps → Q&A → binary → config/secrets → service → health →
      summary in order.
- [ ] Summary prints dashboard URL, task name, log path, next steps (parallel to
      P1).
- [ ] Upgrade swaps + restarts only, surfaces the forward-only migration caveat,
      preserves config/data/secrets.
- [ ] Reconfigure re-runs pre-filled Q&A, rewrites config, preserves data +
      secrets (master key not regenerated).
- [ ] Uninstall removes task/binary/config + secrets; data kept by default, removed
      on opt-in.
- [ ] Service registration is behind an install-type seam for P4 reuse.
- [ ] All Pester tests green; PSScriptAnalyzer clean.

## Dependencies

- **Tasks 1–3** (helpers/paths, deps, config+secrets+binary).
- **P0** (merged): `--config` flag passed in the task action. (P0's native Windows
  Service is **not** used here — it is P4.)
- Conceptual mirror of **P1 Task 4**; keep lifecycle semantics + summary parallel.
