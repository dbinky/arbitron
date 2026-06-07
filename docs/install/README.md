# Installing Arbitron

Arbitron ships as a **single self-contained binary** with two subcommands —
`arbitron serve` (the orchestration server + dashboard) and `arbitron mcp` (the
MCP server). The binary builds with `CGO_ENABLED=0`, uses pure-Go SQLite, and
embeds the dashboard SPA, so it has **zero hard runtime dependencies**.

The only external tools the installer resolves are for the **agent features**
(the `claude-cli` provider path): `node`, the `claude` CLI, `git`, and `gh`
(`opencode` is optional). An **API-key-only** deployment (the `anthropic`
provider) needs none of them — the installer skips the whole toolchain. See the
master design's
[Background section](../superpowers/specs/2026-06-05-installers-design.md#background-what-is-actually-being-installed)
for the full rationale.

## One-line install

| Platform | Command |
|---|---|
| macOS / Linux | `curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh \| bash` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 \| iex` |

The script detects your OS/arch, asks whether you want a **Local** (per-user) or
**Shared** (system-wide) install, resolves dependencies, runs a short
plain-language Q&A, downloads and **checksum-verifies** the binary, writes the
config and secrets, registers a reboot-persistent service, health-checks it, and
prints a summary.

### Pin a version

By default the script installs the **latest** release. Pin a specific version
with `ARBITRON_VERSION`:

```bash
ARBITRON_VERSION=1.2.3 curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

```powershell
$env:ARBITRON_VERSION='1.2.3'; irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex
```

Check what is installed with `arbitron version` (prints version + commit + build
date).

## Pick your scenario

| | Scenario | OS / class | Script | Privilege |
|---|---|---|---|---|
| **a** | [macOS local](#a-macos-local) | macOS, single user | `install.sh` | no sudo |
| **b** | [Windows local](#b-windows-local) | Windows, single user | `install.ps1` | no admin |
| **c** | [Linux local](#c-linux-local) | Linux, single user | `install.sh` | no sudo |
| **d** | [Linux shared server](#d-linux-shared-server) | Linux, multi-user | `install.sh` | **sudo / root** |
| **e** | [Windows shared server](#e-windows-shared-server) | Windows, multi-user | `install.ps1` | **administrator** |

macOS is **local-only** (there is no Shared path). On Linux the script defaults
to Shared when run as root, Local otherwise; on Windows it defaults to Local.

After install, see:

- [Upgrade / reconfigure / uninstall](upgrading.md) — including the
  **forward-only migration** caveat.
- [Security notes](security.md) — skip-permissions, local-mode exposure,
  secrets, and download verification.
- [Tunnels & TLS](tunnels-and-tls.md) — exposing a shared server publicly.

---

## a) macOS local

```bash
curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

User-level, loopback-only, no sudo. The service is a launchd LaunchAgent that
starts at login and is kept alive.

**Layout**

| | Path |
|---|---|
| Binary | `~/.arbitron/bin/arbitron` |
| Config | `~/.arbitron/config.yaml` |
| Data / DB | `~/.arbitron/data/` (`arbitron.db`) |
| Workspaces | `~/.arbitron/workspaces/` |
| Logs | `~/.arbitron/logs/` |
| Secrets env | `~/.arbitron/arbitron.env` (mode `0600`) |
| Service | launchd LaunchAgent `~/Library/LaunchAgents/dev.arbitron.plist` |
| Auth | `local` / loopback bind (`127.0.0.1`) |

**Config questions asked**

1. **Dashboard port** (default `8080`).
2. **LLM access** — `subscription` (runs `claude /login`) or `api-key` (paste an
   Anthropic key).
3. **Enable unattended agent tools (skip permissions)?** — the
   `--dangerously-skip-permissions` question, shown with its RCE-equivalent
   warning. Default **No**. See [security.md](security.md#dangerously-skip-permissions).
4. **Data location** (default `~/.arbitron/data`).

For subscription mode the installer then launches `claude /login` interactively
(`CLAUDE_CONFIG_DIR` lives under the data dir).

**Resulting service & summary**

The plist runs a small wrapper that sources `arbitron.env` (keeping the master
key out of the plist) then execs `arbitron serve --config ~/.arbitron/config.yaml`.
The summary prints:

```text
Arbitron is ready.
  Dashboard: http://127.0.0.1:8080
  Service:   launchd agent dev.arbitron (launchctl)
  Logs:      ~/.arbitron/logs
  Config:    ~/.arbitron/config.yaml
```

---

## b) Windows local

```powershell
irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex
```

Per-user, loopback-only, **no admin**. The service mechanism is an **at-logon
Scheduled Task** (`Arbitron`) running as the current user.

**Layout**

| | Path |
|---|---|
| Binary | `%LOCALAPPDATA%\Arbitron\bin\arbitron.exe` |
| Config | `%LOCALAPPDATA%\Arbitron\config.yaml` |
| Data / DB | `%LOCALAPPDATA%\Arbitron\data\` (`arbitron.db`) |
| Workspaces | `%LOCALAPPDATA%\Arbitron\workspaces\` |
| Logs | `%LOCALAPPDATA%\Arbitron\logs\` |
| Secrets | User environment variables / DPAPI (no env file) |
| Service | Scheduled Task `Arbitron` (at-logon, runs as you) |
| Auth | `local` / loopback bind (`127.0.0.1`) |

**Config questions asked**

1. **Dashboard port** (default `8080`).
2. **LLM access** — `subscription` (runs `claude /login`) or `apikey` (paste an
   Anthropic key).
3. **Data location** (default `%LOCALAPPDATA%\Arbitron\data`).
4. **Enable unattended agent tools (`--dangerously-skip-permissions`)?** — shown
   with its RCE-equivalent warning. Default **No**.

Secrets are stored as **User-scope environment variables** (`ARBITRON_MASTER_KEY`,
and `ARBITRON_LLM_APIKEY` in api-key mode), never written into `config.yaml`.

**Resulting service & summary**

```text
Arbitron installation complete.
  Dashboard:    http://127.0.0.1:8080
  Service task: Arbitron (at-logon Scheduled Task)
  Config:       %LOCALAPPDATA%\Arbitron\config.yaml
  Log file:     %LOCALAPPDATA%\Arbitron\logs\arbitron.log
  Next step:    run `claude /login` if you have not already authenticated.
```

---

## c) Linux local

```bash
curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

Per-user, loopback-only, no sudo. The service is a **systemd `--user` unit** with
**linger** enabled so it starts at boot without an interactive login. (When run
on Linux, choose `local` at the install-type prompt — it is the default unless
you are root.)

**Layout**

| | Path |
|---|---|
| Binary | `~/.arbitron/bin/arbitron` |
| Config | `~/.arbitron/config.yaml` |
| Data / DB | `~/.arbitron/data/` (`arbitron.db`) |
| Workspaces | `~/.arbitron/workspaces/` |
| Logs | `~/.arbitron/logs/` |
| Secrets env | `~/.arbitron/arbitron.env` (mode `0600`) |
| Service | systemd `--user` unit `~/.config/systemd/user/arbitron.service` (+ linger) |
| Auth | `local` / loopback bind (`127.0.0.1`) |

**Config questions asked** — identical to macOS local (port, LLM access,
skip-permissions with the RCE warning, data location).

**Resulting service & summary**

The unit runs `arbitron serve --config ~/.arbitron/config.yaml` with
`EnvironmentFile=~/.arbitron/arbitron.env`. The summary prints:

```text
Arbitron is ready.
  Dashboard: http://127.0.0.1:8080
  Service:   systemd --user unit arbitron (systemctl --user)
  Logs:      ~/.arbitron/logs
  Config:    ~/.arbitron/config.yaml
```

---

## d) Linux shared server

```bash
curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

Run as **root** (or via `sudo`); choose **shared** at the install-type prompt
(it is the default when running as root). The shared path is system-wide, runs
under a dedicated **`arbitron`** service account, and supports multi-user auth
and public TLS exposure.

**Layout**

| | Path |
|---|---|
| Binary | `/usr/local/bin/arbitron` (`0755 root:root`) |
| Config | `/etc/arbitron/config.yaml` (`0644 root:root`, **secret-free**) |
| Data / DB | `/var/lib/arbitron/` (`0750`, owned by the service account) — `arbitron.db` |
| Workspaces | `/var/lib/arbitron/workspaces/` |
| Logs | `/var/log/arbitron/` (or journald) |
| Secrets env | `/etc/arbitron/arbitron.env` (`0600 root:arbitron`, via systemd `EnvironmentFile=`) |
| Service | systemd **system** unit `/etc/systemd/system/arbitron.service` |
| Run as | dedicated `arbitron` account |
| Auth | `edge+local` **or** OIDC `org` mode |

**Config questions asked** (canonical shared order)

1. **Bind host** (default `0.0.0.0`) and **bind port** (default `8080`).
2. **Auth model** — `edge-local` (edge auth + local mode: the installer **forces
   a loopback bind** so Arbitron is reachable only via your authenticated
   tunnel/proxy) or `oidc` (OIDC org mode).
3. **If OIDC:** issuer URL, client ID, client secret (→ `arbitron.env`),
   public base URL, bootstrap admins, derived redirect URL
   (`<publicbaseurl>/auth/callback`), and optional RBAC (group claim, default
   group, department leads).
4. **TLS model** — `tunnel` (your proxy terminates TLS; sets `publicbaseurl` +
   Secure cookies), `direct` (BYO `certfile`/`keyfile`), or `autocert`
   (Let's Encrypt; forces port **443**). See [tunnels-and-tls.md](tunnels-and-tls.md).
5. **Enable unattended agent tools (`--dangerously-skip-permissions`)?** — RCE
   warning, default **No**.
6. **Service account name** (default `arbitron`).
7. **Data location** (default `/var/lib/arbitron`).
8. **LLM access** — `subscription` or `api-key`.

The **master key** is generated silently into `arbitron.env`; it is never
prompted and never logged.

**Resulting service & summary**

The hardened system unit (`NoNewPrivileges=yes`, `ProtectSystem=strict`,
`ProtectHome=yes`, `ReadWritePaths=/var/lib/arbitron /var/log/arbitron`;
`CAP_NET_BIND_SERVICE` only when binding a port < 1024) runs
`arbitron serve --config /etc/arbitron/config.yaml` as `User=arbitron`. The
summary prints:

```text
Arbitron (shared) is ready.
  Public URL: https://arbitron.example.com   (when set)
  Local bind: 0.0.0.0:8080
  Service:    systemd unit 'arbitron' (systemctl status arbitron)
  Logs:       /var/log/arbitron (journalctl -u arbitron)
  Config:     /etc/arbitron/config.yaml
```

---

## e) Windows shared server

```powershell
irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex
```

Run from an **elevated (Administrator) PowerShell** and choose **Shared** at the
install-type prompt. The shared path registers a **native auto-start Windows
Service** (`arbitron`) running as **LocalSystem** or a dedicated local account.

**Layout**

| | Path |
|---|---|
| Binary | `%ProgramFiles%\Arbitron\arbitron.exe` |
| Config | `%ProgramData%\Arbitron\config.yaml` |
| Data / DB | `%ProgramData%\Arbitron\data\` (`arbitron.db`) |
| Workspaces | `%ProgramData%\Arbitron\workspaces\` |
| Logs | `%ProgramData%\Arbitron\logs\` |
| Secrets | Service environment / **DPAPI (LocalMachine scope)** — `<name>.secret` under the data dir |
| Service | native Windows Service `arbitron` (auto-start) |
| Run as | LocalSystem **or** dedicated local account |
| Auth | `edge+local` **or** OIDC `org` mode |

**Config questions asked**

First the installer asks how the service should run — **LocalSystem** (default)
or a dedicated **Account** (default name `arbitron`, created with a random,
machine-managed, never-expiring password). Then the canonical shared Q&A runs in
the same order as Linux shared: bind host/port → auth model → (OIDC sub-questions
+ optional RBAC) → TLS model → skip-permissions (RCE warning) → service account
name → data location → LLM access (`subscription` or `apikey`).

Secrets (master key, OIDC client secret, API key) are stored with **DPAPI at
machine scope** so the service can read them; they never enter `config.yaml`.

**Resulting service & summary**

```text
Arbitron installation complete.
  Dashboard:    https://arbitron.example.com   (scheme/host follow your TLS choice)
  Service:      arbitron (native Windows Service, auto-start)
  Config:       %ProgramData%\Arbitron\config.yaml
  Log file:     %ProgramData%\Arbitron\logs\arbitron.log
```
