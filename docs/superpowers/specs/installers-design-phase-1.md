# Installers — Phase 1 (P1): `install.sh` local (macOS + Linux)

> Decomposed from `docs/superpowers/specs/2026-06-05-installers-design.md` (the
> master design). Phase numbering matches the master: this is **P1**. The Go
> prerequisites (**P0** — configurable config path, direct TLS serving, native
> Windows-service awareness) are **already implemented and merged**; this phase
> consumes them and does not modify them.

## Scope

Deliver `install.sh` (bash) covering the two **local** Unix scenarios from the
master matrix:

- **(a) macOS local** — launchd LaunchAgent, run as invoking user.
- **(c) Linux local** — systemd `--user` unit (+ linger), run as invoking user.

Local installs are **user-level**: no sudo, everything under `~/.arbitron`, a
user service that survives reboot. This phase establishes the script skeleton and
the full local install/upgrade/reconfigure/uninstall lifecycle. Shared/system
installs (scenario d) are **P2** and extend this same script.

Out of scope for P1: Windows (`install.ps1` → P3/P4), Linux shared server (P2),
release/distribution machinery (P5).

## Deliverables

- `install.sh` at **repo root** (hosted at a stable URL in P5). Entry point:
  `curl … | bash`. (`install.ps1`, also at repo root, is P3/P4.) Test suites live
  under `tests/install/`.
- The common installer flow from the master design, implemented for local:
  ```
  detect OS / arch / elevation → install type = Local
  detect existing install (config + service)?  → Fresh | Upgrade | Reconfigure | Uninstall
  [Fresh/Reconfigure] dependency resolution → config Q&A → download + checksum-verify
    → write config.yaml (absolute paths) + secrets env (0600) → register + start service
    → health-check → print summary
  ```
- `bats` test suite + `shellcheck` lint wired into CI (Linux container; macOS runner
  for the launchd path).

## Layout (from master matrix, local columns)

| | macOS local | Linux local |
|---|---|---|
| Binary | `~/.arbitron/bin/arbitron` | `~/.arbitron/bin/arbitron` |
| Config | `~/.arbitron/config.yaml` | `~/.arbitron/config.yaml` |
| Data/db | `~/.arbitron/data/` | `~/.arbitron/data/` |
| Workspaces | `~/.arbitron/workspaces/` | `~/.arbitron/workspaces/` |
| Logs | `~/.arbitron/logs/` | `~/.arbitron/logs/` |
| Secrets env | `~/.arbitron/arbitron.env` (0600) | `~/.arbitron/arbitron.env` (0600) |
| Service | `~/Library/LaunchAgents/dev.arbitron.plist` | `~/.config/systemd/user/arbitron.service` |
| Auth | local / loopback | local / loopback |

The installer always writes **absolute** paths into `config.yaml` (the app's
CWD-relative defaults are never relied on).

## Dependency resolution

Per dependency (`node`, `claude` CLI, `git`, `gh`; `opencode` optional):
1. Check presence + minimum version.
2. If missing → platform package manager (`brew` on mac; `apt`/`dnf`/`pacman`/
   `zypper` on linux).
3. If no package manager → offer to bootstrap one (Homebrew on mac).
4. If declined/impossible → direct-download that one dependency into an
   Arbitron-owned prefix (`claude` via `npm -g` into an Arbitron-owned npm prefix so
   it does not fight a system Node).
5. Every download is **checksum-verified** before use.

## Config Q&A (local — the "easy questions")

- Port (default 8080).
- LLM access: Claude subscription (`claude /login`) **or** Anthropic API key (paste).
  Default **subscription** on local.
- "Enable unattended agent tools?" — the `--dangerously-skip-permissions` prompt,
  shown with its RCE-equivalent warning. Asked explicitly, never silently defaulted.
- Data location (default shown).

Everything else defaults; **local mode binds loopback**. Master key is generated
(32 random bytes → base64) into the secrets env even for Claude-only deployments,
so adding an Azure/OpenRouter connection later "just works"; never logged.

## Service definitions

- **launchd** (mac): `~/Library/LaunchAgents/dev.arbitron.plist`, `RunAtLoad` +
  `KeepAlive`, `EnvironmentVariables` carrying the config path + secrets.
- **systemd `--user`** (linux): `~/.config/systemd/user/arbitron.service`;
  `loginctl enable-linger <user>` so it starts without an interactive login.

Each unit passes `--config <path>` (or `ARBITRON_CONFIG`) — the P0 flag — so the
service finds the right config regardless of run-as home directory.

## Upgrade / reconfigure / uninstall (local)

- **Upgrade** — download new binary, checksum-verify, swap, restart service. DB
  migrations run on `serve` start (forward-only; surface the one-way down-migration
  caveat in upgrade notes).
- **Reconfigure** — re-run Q&A pre-filled from existing `config.yaml`; rewrite
  config; preserve data + existing secrets.
- **Uninstall** — stop + remove service, binary, config; prompt whether to remove
  data (default **keep**).

## Testing

- `bats` with mocked package managers and downloads, asserting the written
  `config.yaml`, the secrets env (mode 0600), and the service unit contents.
- `shellcheck` lint, clean.
- Linux paths exercised in CI containers; a macOS runner covers the launchd path.

## Dependencies on other phases

- **Consumes P0** (merged): `--config`/`ARBITRON_CONFIG`, the `server.tls` block
  (unused on loopback-only local), and the binary's zero-runtime-dep nature.
- **Feeds P2**: P2 extends this script with the shared/system-level branch, so the
  install-type detection, dependency resolution, download/verify, and Q&A engine
  built here must be factored to allow a Shared path to reuse them.
- **Feeds P5**: the binary download + checksum-verify step depends on the release
  artifact naming + `SHA256SUMS` that P5 formalizes; P1 may target the existing
  release layout and be re-pointed in P5.
