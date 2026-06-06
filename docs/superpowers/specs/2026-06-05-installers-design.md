# Arbitron Installers — Design

> Implementation design for cross-platform installers covering five scenarios:
> (a) macOS local, (b) Windows local (PowerShell), (c) Linux local,
> (d) Linux shared server, (e) Windows shared server (PowerShell). Goal: a new
> install, an upgrade, or a config change where everything "just works" —
> dependencies resolved with no assumptions about the machine, and config
> questions that are easy to understand and answer.

## Purpose

Make Arbitron installable on a bare machine with a single command. The installer
detects and resolves every runtime dependency, asks a short set of plain-language
configuration questions, writes a correct config, sets up secrets, and registers
an OS service that survives reboot. The same entry point handles fresh installs,
upgrades, and reconfiguration.

## Scope

In scope:
- `install.sh` (bash) covering macOS + Linux, local + shared.
- `install.ps1` (PowerShell) covering Windows, local + shared.
- Dependency detection + auto-install (Node, `claude` CLI, git, gh; opencode optional).
- Interactive config Q&A → `config.yaml` (absolute paths) + protected secrets env.
- Native service registration (launchd / systemd / Windows Service / Scheduled Task).
- Upgrade / reconfigure / uninstall flows.
- Two supporting Go changes (configurable config path; direct TLS serving) and one
  optional Go change (native Windows-service awareness).
- GitHub Releases distribution + checksum verification.
- `bats` / `Pester` / Go tests for the above.

Out of scope (non-goals):
- Target-project language toolchains (go/python/rust/… that the *agent* uses to
  build a user's project) — operator responsibility.
- Air-gapped / fully offline install; bundling runtimes in the package.
- Container / Kubernetes deployment (`docker-compose.yaml` already exists).
- Provisioning the cloudflared tunnel or any reverse proxy.
- HA / multi-node.

## Background: what is actually being installed

The `arbitron` binary is already self-contained: it builds with `CGO_ENABLED=0`,
uses pure-Go SQLite (`modernc.org/sqlite` — no libc/cgo), and embeds the dashboard
SPA via `go:embed`. It cross-compiles to darwin/linux/windows × amd64/arm64 and has
**zero hard runtime dependencies**. One binary, subcommands `serve` and `mcp`.

All dependency complexity is about the **agent features**, not Arbitron itself:
- `anthropic` provider (HTTP) → API key only, no external binary.
- `claude-cli` provider (the "real work on local projects" path) → the `claude`
  binary (`npm i -g @anthropic-ai/claude-code`) → **Node.js**, plus **git**, **gh**,
  and a POSIX shell on PATH; plus per-language toolchains for whatever the agent
  builds (out of scope).
- `opencode` provider → the `opencode` binary (optional).
- Azure / OpenRouter → HTTP + a **master key** (`ARBITRON_MASTER_KEY`) to encrypt
  stored connection secrets.

Config auto-creates at `~/.arbitron/config.yaml` on first run, but DB/workspace/log
paths default CWD-relative (`./arbitron.db`, `./workspaces`, `arbitron.log`). The
installer always writes **absolute** paths, so no app change is needed for that.

Auth has two modes that map onto the two install classes: **local** (single
auto-admin, no login, refuses non-loopback bind) for a/b/c, and **org** (OIDC +
server-side sessions) for the multi-user path in d/e.

## Decisions

| Area | Decision |
|---|---|
| Dependency strategy | Detect + auto-install via OS package manager; bootstrap the package manager if absent, else direct-download that one dependency. Bundle nothing but the binary. |
| Entry point | `curl … \| bash` → `install.sh` (mac + linux); `irm … \| iex` → `install.ps1` (windows). **All logic lives in the scripts**; the binary is downloaded and run. |
| Service | Native per platform, auto-start, reboot-persistent. |
| Privilege | **Local** = user-level (no sudo/admin; `~/.arbitron`; user service). **Shared** = system-level (sudo/admin; system paths; dedicated `arbitron` account). |
| LLM credentials | Offer subscription (`claude /login`) **and** API key; default subscription on local, API key on shared. |
| Master key | Auto-generate 32 random bytes → base64 → protected service-env file. |
| Shared TLS | Support **both**: (A) HTTP origin behind a tunnel/proxy with `PublicBaseURL` + Secure cookies (zero app change); (B) direct app-TLS with BYO/internal-CA cert **or** autocert. |
| Tunnel/proxy | Operator runs their own (cloudflared the likely initial path). Installer sets `PublicBaseURL` + Secure cookies and prints guidance; never touches the tunnel. |
| Skip-permissions | Asked explicitly on every install, with the RCE-equivalent warning. |
| Shared auth | Offer **edge-auth + local mode** (loopback behind an authenticated tunnel; single shared admin) **and** **OIDC org mode** (per-user identity + RBAC). |
| Spec/impl shape | One design spec; phased implementation (each phase independently shippable). |

## Required Go changes

These get strict TDD (the bash/PowerShell can't be unit-tested the same way).

### 1. Configurable config path (load-bearing)
`cmd/arbitron/main.go` hardcodes `filepath.Join(homeDir, ".arbitron", "config.yaml")`.
A system service running as the `arbitron` account, and Windows system installs,
both need this overridable.
- Add a persistent `--config` flag and an `ARBITRON_CONFIG` env var.
- Precedence: `--config` > `ARBITRON_CONFIG` > current default (`~/.arbitron/config.yaml`).
- Resolution lives in a small pure function: `(flag, env, homeDir) → path`,
  table-tested across all precedence permutations and empty/absent cases.

### 2. Direct TLS serving (load-bearing for scenario B)
`main.go` only calls `srv.ListenAndServe()` (plain HTTP), and `ServerConfig` has no
TLS fields.
- Add a `server.tls` block to `domain/config`: `enabled bool`, `certfile string`,
  `keyfile string`, `autocertdomain string`, `autocertcachedir string`.
- In `runServe`: when `tls.enabled` and cert/key set → `ListenAndServeTLS`; when
  `autocertdomain` set → `golang.org/x/crypto/acme/autocert` (new dependency,
  managed cert + `:80` HTTP-01 redirect handler); otherwise unchanged plain HTTP.
- Validation in the config loader (fail-fast, mirroring `validateAuth`): `enabled`
  requires either cert+key **or** an autocert domain; cert without key (or vice
  versa) is rejected.
- Tests: `httptest`/`crypto/tls` round-trip with a generated self-signed cert;
  loader validation table tests. Autocert wiring covered by a construction test
  (no live ACME).

### 3. Native Windows-service awareness (optional)
To register a real Windows Service with no third-party shim, make the binary
service-aware via `golang.org/x/sys/windows/svc` (detect "am I running under the
SCM?" and dispatch accordingly; a no-op on non-Windows). **Decision: native.**
Fallbacks if deferred: a downloaded WinSW/nssm wrapper, or a Scheduled Task. The
user-level local-Windows path (scenario b) uses a **Scheduled Task** regardless
(no elevation); the shared-Windows path (scenario e) uses the native Service.

No app change is needed for the CWD-relative path defaults — the installer writes
absolute paths into config.

## Scenario → layout matrix

| | a) mac local | b) win local | c) linux local | d) linux shared | e) win shared |
|---|---|---|---|---|---|
| Script | install.sh | install.ps1 | install.sh | install.sh (sudo) | install.ps1 (admin) |
| Service | launchd LaunchAgent | Scheduled Task (user) | systemd `--user` (+linger) | systemd system unit | Windows Service |
| Run as | invoking user | invoking user | invoking user | dedicated `arbitron` | LocalSystem / svc acct |
| Binary | `~/.arbitron/bin/arbitron` | `%LOCALAPPDATA%\Arbitron\bin\` | `~/.arbitron/bin/arbitron` | `/usr/local/bin/arbitron` | `%ProgramFiles%\Arbitron\` |
| Config | `~/.arbitron/config.yaml` | `%LOCALAPPDATA%\Arbitron\config.yaml` | `~/.arbitron/config.yaml` | `/etc/arbitron/config.yaml` | `%ProgramData%\Arbitron\config.yaml` |
| Data/db | `~/.arbitron/data/` | `%LOCALAPPDATA%\Arbitron\data\` | `~/.arbitron/data/` | `/var/lib/arbitron/` | `%ProgramData%\Arbitron\data\` |
| Workspaces | `~/.arbitron/workspaces/` | `…\Arbitron\workspaces\` | `~/.arbitron/workspaces/` | `/var/lib/arbitron/workspaces/` | `…\Arbitron\workspaces\` |
| Logs | `~/.arbitron/logs/` | `…\Arbitron\logs\` | `~/.arbitron/logs/` | `/var/log/arbitron/` (or journald) | `…\Arbitron\logs\` |
| Secrets env | `~/.arbitron/arbitron.env` (0600) | user env / DPAPI | `~/.arbitron/arbitron.env` (0600) | `/etc/arbitron/arbitron.env` (0600 root:arbitron) | service env / DPAPI |
| Auth | local / loopback | local / loopback | local / loopback | edge+local **or** OIDC | edge+local **or** OIDC |

## Installer flow (identical shape in bash and PowerShell)

```
detect OS / arch / elevation
  → install type: Local | Shared            (macOS = local only)
detect existing install (config + service present)?
  → Fresh | Upgrade | Reconfigure | Uninstall
[Fresh/Reconfigure]
  dependency resolution (see below)
  config Q&A (pre-filled from existing config on reconfigure)
  download + checksum-verify binary
  write config.yaml (absolute paths) + secrets env (0600)
  register + start service
  health-check http(s)://host:port
  print summary: dashboard URL, service name, log path, next steps
```

### Dependency resolution

Per dependency (`node`, `claude` CLI, `git`, `gh`; `opencode` optional):
1. Check presence and minimum version.
2. If missing → try the platform package manager (brew / winget / apt / dnf /
   pacman / zypper).
3. If no package manager → offer to bootstrap one (Homebrew / App Installer).
4. If declined/impossible → direct-download that dependency into an Arbitron-owned
   `…/runtime/` directory.

`claude` installs via `npm -g` into an Arbitron-owned npm prefix so it does not
fight a system Node. Every download is checksum-verified before use.

### Config Q&A (the "easy questions")

**Local (a/b/c):**
- Port (default 8080).
- LLM access: Claude subscription (`claude /login`) or Anthropic API key (paste).
- "Enable unattended agent tools?" — the `--dangerously-skip-permissions` prompt,
  shown with its RCE-equivalent warning.
- Data location (default shown).

Everything else defaults; local mode binds loopback.

**Shared (d/e):**
- Bind host / port.
- Auth model: **edge-auth + local mode** (installer enforces loopback bind so
  Arbitron is reachable only via the authenticated tunnel) **or** **OIDC org mode**.
- If OIDC: issuer URL, client ID, client secret, redirect URL, bootstrap admins,
  and optional RBAC (group claim, default group, department leads).
- TLS model: behind tunnel/proxy → set `PublicBaseURL` (+ Secure cookies) and print
  guidance; **or** direct app-TLS → cert + key paths, **or** autocert domain.
- "Enable unattended agent tools?" — the same explicit prompt as local, shown with
  its RCE-equivalent warning (asked, not silently defaulted).
- Service account name (default `arbitron`).
- Data location (default shown).
- Master key auto-generated silently into the secrets env.

## Secrets handling

- **API key / master key** → written to the secrets env file (mode 0600; on shared
  Linux owned `root:arbitron` and referenced via systemd `EnvironmentFile=`; on
  Windows via the service environment / DPAPI).
- **Subscription login** → `claude /login` (interactive) writes to
  `CLAUDE_CONFIG_DIR`. For a shared service account this directory lives under the
  data dir and login runs as that account (or via device login on a headless box).
- The master key is generated as 32 random bytes, base64-encoded, and never logged.
  A Claude-only / API-key-only deployment simply never uses it; it is generated up
  front so that adding an Azure/OpenRouter connection later "just works."

## Service definitions

- **launchd** (mac local): `~/Library/LaunchAgents/dev.arbitron.plist`, `RunAtLoad`
  + `KeepAlive`, `EnvironmentVariables` for the config path and secrets.
- **systemd `--user`** (linux local): `~/.config/systemd/user/arbitron.service`;
  `loginctl enable-linger <user>` so it starts without an interactive login.
- **systemd system** (linux shared): `/etc/systemd/system/arbitron.service`,
  `User=arbitron`, `EnvironmentFile=/etc/arbitron/arbitron.env`,
  `WorkingDirectory=/var/lib/arbitron`, hardening (`NoNewPrivileges=yes`,
  `ProtectSystem=strict`, `ProtectHome=yes`, `ReadWritePaths=/var/lib/arbitron`).
- **Scheduled Task** (windows local): at-logon task running as the user.
- **Windows Service** (windows shared): native service (Go `svc`), auto-start.

Each definition passes `--config <path>` (or `ARBITRON_CONFIG`) so the service
finds the right config regardless of the run-as account's home directory.

## Upgrade / reconfigure / uninstall

Re-running the script detects an existing install (config + service) and offers:
- **Upgrade** — download the new binary, checksum-verify, swap it in, restart the
  service. DB migrations run automatically on `serve` start
  (`adaptsqlite.RunMigrations`). Migrations are forward-only — surface the
  documented one-way down-migration caveat in the upgrade notes.
- **Reconfigure** — re-run the Q&A pre-filled from the existing config; rewrite
  `config.yaml`; preserve data and existing secrets.
- **Uninstall** — stop and remove the service, binary, and config; prompt whether
  to remove data (default **keep**).

## Distribution & versioning

GitHub Releases artifacts: `arbitron_<version>_<os>_<arch>[.exe]` plus
`SHA256SUMS` (and a signature). Scripts fetch the latest release by default,
pinnable via `ARBITRON_VERSION`. `install.sh` / `install.ps1` are hosted at a
stable URL. Checksum verification is **mandatory** before executing any downloaded
artifact.

## Testing strategy

- **Go changes:** table tests for config-path resolution; TLS serving via
  `httptest`/`crypto/tls` with a generated self-signed cert; loader validation
  tests; autocert construction test.
- **`install.sh`:** `bats` with mocked package managers and downloads, asserting the
  written `config.yaml`, secrets env, and service unit; `shellcheck` lint. Linux
  paths exercised in CI containers; a macOS runner covers the launchd path.
- **`install.ps1`:** `Pester` with mocked `winget` / `New-Service` /
  `Register-ScheduledTask`, asserting config + service registration;
  `PSScriptAnalyzer` lint.

## Phasing (each independently shippable)

- **P0** — Go prerequisites: configurable config path; direct TLS serving; native
  Windows-service awareness.
- **P1** — `install.sh` local (scenarios a, c).
- **P2** — `install.sh` shared (scenario d).
- **P3** — `install.ps1` local (scenario b).
- **P4** — `install.ps1` shared (scenario e).
- **P5** — release/distribution machinery + documentation.

## Open questions

None blocking. The Windows-service mechanism (§Required Go changes #3) is decided as
native Go service support, with Scheduled Task for the user-level local path; this
can be revisited in P0/P3 if native `svc` integration proves costly.
