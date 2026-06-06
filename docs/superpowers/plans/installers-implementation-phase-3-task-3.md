# P3 — Task 3: config Q&A, config.yaml, secrets, binary download+verify

> Phase **P3** (`install.ps1` local, Windows) — PowerShell mirror of P1 Task 3.
> Master design: `docs/superpowers/specs/2026-06-05-installers-design.md`.
> Phase design: `docs/superpowers/specs/installers-design-phase-3.md`.

## Context

**Delivers:** the interactive config Q&A (identical question set to P1 local), the
`config.yaml` writer (absolute paths, loopback bind), Windows secrets handling
(generated master key + optional API key via user environment / DPAPI), and the
`arbitron.exe` binary download + mandatory checksum verification.

**Mirrors P1 Task 3** exactly on the question set and the resulting config shape.
Differences are Windows-only: secrets go to user env / DPAPI instead of a 0600
`arbitron.env` file; paths are `%LOCALAPPDATA%\Arbitron\…`.

**Config Q&A (same as P1 local):**
- **Port** (default `8080`).
- **LLM access:** Claude subscription (`claude /login`) **or** Anthropic API key
  (paste). **Default subscription on local.**
- **"Enable unattended agent tools?"** — the `--dangerously-skip-permissions`
  prompt, shown with its RCE-equivalent warning; asked explicitly, never silently
  defaulted (default **No**).
- **Data location** (default `%LOCALAPPDATA%\Arbitron\data`, shown).

Local mode binds **loopback** (`server.host: 127.0.0.1`). Master key is generated
(32 random bytes → base64) and stored in the secrets store even for a Claude-only
deployment; **never logged**.

**Depends on:** Task 1 (paths, prompts, logging, `Test-Sha256`). On reconfigure,
the answers are pre-filled from the existing `config.yaml` (Task 4 supplies the
loaded values; this task's writer/Q&A accept a `-Defaults` object).

**Feeds P4:** keep the Q&A engine and the config writer parameterized (host,
auth-mode, secrets sink) so the Shared branch can pass non-loopback host, org auth,
and a file-based secrets sink.

## Config / secret key reference (from the Go config loader)

YAML keys are lowercased & nested. Relevant keys this task writes:
- `server.host`, `server.port`
- `database.path`, `workspace.path`, `logging.file`
- `llm.provider` (`"claude-cli"` for subscription, `"anthropic"` for API key),
  `llm.apikey` (only if API-key mode AND not using env), `llm.skippermissions`
- Master key is the **`ARBITRON_MASTER_KEY`** env var (not a YAML field).
- API key may also be supplied via `ARBITRON_LLM_APIKEY` env rather than written
  into `config.yaml` (preferred — keeps the secret out of the config file).

## Test plan (write first)

Pester suite `tests/install/Install.Config.Tests.ps1`. Mock prompts, the secret
generator's RNG seam, `[Environment]::SetEnvironmentVariable`, DPAPI calls,
download cmdlets, and `Test-Sha256`.

- **config.yaml absolute data path:** after `Write-ArbitronConfig` with answers,
  the written YAML contains `database.path`, `workspace.path`, `logging.file` as
  **absolute** paths under the chosen data dir (assert `IsPathRooted`).
- **loopback bind:** written `server.host` is `127.0.0.1` and `server.port` is the
  chosen port (default `8080`).
- **subscription mode:** when LLM answer = subscription, YAML sets
  `llm.provider: claude-cli` and contains **no** `llm.apikey`.
- **API-key mode:** when answer = API key, provider is `anthropic` and the key is
  routed to the secrets sink (env/DPAPI), NOT written as plaintext in
  `config.yaml` — assert the config file does not contain the key string.
- **skip-permissions default No:** when the user accepts the default,
  `llm.skippermissions: false`; when they explicitly opt in, `true`. Assert the
  RCE warning text is shown before the prompt.
- **master key generated:** `New-MasterKey` produces 32 bytes base64 (assert
  decoded length == 32, valid base64); stored in the secrets sink.
- **master key never logged:** capture all `Write-ArbitronLog`/`Write-Host` output
  during a full Q&A + write run; assert the base64 master key value never appears.
- **API key never logged:** same capture; the pasted API key never appears in
  output or in `config.yaml`.
- **secrets sink — env:** `Set-ArbitronSecret -Name ARBITRON_MASTER_KEY -Value …`
  calls `[Environment]::SetEnvironmentVariable(..., 'User')` (assert User scope,
  not Machine — local install has no elevation).
- **secrets sink — DPAPI option:** when DPAPI storage is selected, the secret is
  protected via `ConvertTo-SecureString`/`ProtectedData` and written under the
  data dir; assert the on-disk blob does not contain the plaintext.
- **binary download + verify:** `Get-ArbitronBinary` downloads
  `arbitron_<version>_windows_<arch>.exe`, fetches `SHA256SUMS`, and calls
  `Test-Sha256`; on match the exe is placed at `…\bin\arbitron.exe`; on mismatch it
  throws and leaves no binary in place.
- **reconfigure prefill:** `Invoke-ConfigQA -Defaults <obj>` uses the provided
  values as prompt defaults (assert a mocked existing port appears as the default).
- **CLAUDE_CONFIG_DIR for subscription:** subscription mode sets
  `CLAUDE_CONFIG_DIR` under the data dir before invoking `claude /login` (assert
  env set; the actual interactive login is out of unit scope / mocked).

## Implementation steps

1. `Invoke-ConfigQA -Paths -Defaults`: ask the four questions via Task 1 prompt
   helpers; return an answers object `{ Port; LlmMode; ApiKey; SkipPermissions;
   DataDir }`. Show the RCE-equivalent warning text immediately before the
   skip-permissions prompt; default No.
2. `New-MasterKey`: 32 bytes from `[RNGCryptoServiceProvider]` /
   `RandomNumberGenerator` → `[Convert]::ToBase64String`. Keep the RNG behind a
   seam for test determinism.
3. `Set-ArbitronSecret -Name -Value [-Method Env|Dpapi] -Paths`: Env →
   `[Environment]::SetEnvironmentVariable($Name,$Value,'User')`; Dpapi →
   `ConvertFrom-SecureString` / `[ProtectedData]::Protect(..., CurrentUser)` to a
   file under `$Paths.Data`. Never log the value (pass `-Secret` to the logger).
4. `Write-ArbitronConfig -Paths -Answers`: build the YAML with absolute paths
   (`database.path = $DataDir\arbitron.db`, `workspace.path = $DataDir\workspaces`,
   `logging.file = $Paths.Logs\arbitron.log`), `server.host: 127.0.0.1`,
   `server.port`, provider per LLM mode, `llm.skippermissions`. Do **not** write
   secrets into the file. Write via `Set-Content -Encoding utf8`.
5. Secrets wiring: always generate + store `ARBITRON_MASTER_KEY`; in API-key mode
   also store the key as `ARBITRON_LLM_APIKEY` (env/DPAPI) rather than in YAML. In
   subscription mode set `CLAUDE_CONFIG_DIR` and run `claude /login` (interactive;
   guarded/mocked in tests).
6. `Get-ArbitronBinary -Paths -Version -Arch`: resolve the release URL for
   `arbitron_<version>_windows_<arch>.exe`; download to a temp path; download
   `SHA256SUMS`; `Test-Sha256` against the matching line; on success move to
   `$Paths.BinaryExe`; on failure throw and clean up the temp file.
7. Ensure all created directories exist (`New-Item -ItemType Directory -Force`).

## Files

- `install.ps1` (modified) — Q&A, config writer, secrets, binary fetch.
- `tests/install/Install.Config.Tests.ps1` (new).

## Acceptance criteria

- [ ] Q&A asks exactly the four local questions, parallel to P1; subscription is
      the local default; skip-permissions defaults No with the RCE warning shown.
- [ ] `config.yaml` written with absolute paths and loopback `server.host`.
- [ ] Secrets (master key always; API key in API mode) go to user env / DPAPI,
      never into `config.yaml`.
- [ ] Master key is 32 bytes → base64 and never appears in any log or the config.
- [ ] `arbitron.exe` is downloaded and checksum-verified before placement; a
      mismatch aborts with no binary left behind.
- [ ] Q&A accepts pre-filled defaults for reconfigure.
- [ ] Writer/Q&A/secrets sink are parameterized for P4 reuse.
- [ ] All Pester tests green; PSScriptAnalyzer clean.

## Dependencies

- **Task 1** (paths, prompts, logging, `Test-Sha256`).
- **P0** (merged): `--config` mechanism consumes the written absolute config.
- **P5** (later): release artifact naming (`arbitron_<version>_windows_<arch>.exe`
  + `SHA256SUMS`) — may target current layout, re-pointed in P5.
- Conceptual mirror of **P1 Task 3**; keep the question set + config shape parallel.
