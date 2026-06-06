# Installers — Phase 3 (P3): `install.ps1` local (Windows)

> Decomposed from `docs/superpowers/specs/2026-06-05-installers-design.md`. Phase
> numbering matches the master: this is **P3**. It is the PowerShell mirror of P1.
> P0 (Go prerequisites, incl. native Windows-service awareness) is merged.

## Scope

Deliver `install.ps1` (PowerShell) covering **(b) Windows local**. Local Windows
installs are **user-level**: no elevation, everything under `%LOCALAPPDATA%`, and a
per-user **Scheduled Task** (at-logon) as the service mechanism (no admin needed).
Entry point: `irm … | iex`.

The installer flow mirrors P1's shape exactly (detect → existing-install branch →
deps → Q&A → download+verify → write config + secrets → register service →
health-check → summary). Windows shared (scenario e) is **P4** and extends this
script.

Out of scope: Unix (`install.sh` → P1/P2), Windows shared (P4), release machinery (P5).

## Layout (from master matrix, windows local column)

| | Windows local |
|---|---|
| Script | `install.ps1` |
| Service | Scheduled Task (user, at-logon) |
| Run as | invoking user |
| Binary | `%LOCALAPPDATA%\Arbitron\bin\arbitron.exe` |
| Config | `%LOCALAPPDATA%\Arbitron\config.yaml` |
| Data/db | `%LOCALAPPDATA%\Arbitron\data\` |
| Workspaces | `%LOCALAPPDATA%\Arbitron\workspaces\` |
| Logs | `%LOCALAPPDATA%\Arbitron\logs\` |
| Secrets env | user env / DPAPI |
| Auth | local / loopback |

Absolute paths are written into `config.yaml` via the P0 `--config` mechanism.

## Dependency resolution

Per dependency (`node`, `claude` CLI, `git`, `gh`; `opencode` optional):
1. Check presence + min version.
2. If missing → `winget`.
3. If no package manager → offer to bootstrap **App Installer** (winget).
4. If declined/impossible → direct-download into an Arbitron-owned prefix (`claude`
   via `npm -g` into an Arbitron-owned npm prefix).
5. Every download **checksum-verified** before use.

## Config Q&A (local — same questions as P1)

- Port (default 8080).
- LLM access: Claude subscription (`claude /login`) or Anthropic API key (paste);
  default subscription on local.
- "Enable unattended agent tools?" — explicit `--dangerously-skip-permissions`
  prompt with the RCE-equivalent warning.
- Data location (default shown).

Local mode binds loopback. Master key auto-generated (32 bytes → base64) into the
secrets store; never logged.

## Service definition (Scheduled Task)

At-logon Scheduled Task running as the invoking user, launching
`arbitron.exe serve --config %LOCALAPPDATA%\Arbitron\config.yaml`. Registered via
`Register-ScheduledTask`. (The native Go Windows **Service** is used only by the
shared path in P4; local uses a Scheduled Task regardless, since it needs no
elevation.)

## Secrets handling (Windows)

API key / master key stored via the user environment / **DPAPI**. Subscription
login (`claude /login`) writes to `CLAUDE_CONFIG_DIR` under the data dir.

## Upgrade / reconfigure / uninstall (local)

- **Upgrade** — download new `arbitron.exe`, checksum-verify, swap, restart the
  Scheduled Task. Migrations run on `serve` start (forward-only caveat noted).
- **Reconfigure** — re-run Q&A pre-filled from existing config; rewrite config;
  preserve data + secrets.
- **Uninstall** — remove the Scheduled Task, binary, config; prompt before removing
  data (default keep).

## Testing

- `Pester` with mocked `winget` / `Register-ScheduledTask` / download cmdlets,
  asserting the written `config.yaml`, secrets registration (DPAPI/user env), and
  Scheduled Task definition.
- `PSScriptAnalyzer` lint, clean. Run on a Windows CI runner.

## Dependencies on other phases

- **Mirrors P1**: same flow, same local Q&A; keep the question set and summary
  output parallel to `install.sh` so both installers behave identically.
- **Consumes P0**: `--config`/`ARBITRON_CONFIG`; the native Windows-service
  awareness shipped in P0 is *not* exercised here (local uses Scheduled Task) but is
  used by P4.
- **Feeds P4**: install-type detection, dependency resolution, download/verify, and
  the Q&A engine must be factored so P4's Shared branch reuses them.
- **Feeds P5**: binary download + checksum-verify depends on the P5 release artifact
  naming + `SHA256SUMS`.
