# Installing Arbitron

Arbitron ships as a **single self-contained binary** with two subcommands —
`arbitron serve` (the orchestration server + dashboard) and `arbitron mcp` (the
MCP server). The binary builds with `CGO_ENABLED=0`, uses pure-Go SQLite, and
embeds the dashboard SPA. Core workflow saving and execution do not require a browser;
rendered workflow auto-arrangement has the additional runtime prerequisite below.

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

Check what is installed with `arbitron version` (prints version + commit + build
date).

## Unattended (non-interactive) install

Both installers run without a human at the keyboard, for CI and fleet
provisioning. The two scripts use **different** override models.

### macOS / Linux (`install.sh`)

Every interactive prompt resolves a matching **`ARBITRON_<VAR>`** environment
variable *before* it would read from the terminal. Set the variables for the
answers you want; any you leave unset fall back to the printed default. This
gives you full control over a Local *or* Shared unattended install.

> **Invoke it as a file, not a pipe.** `curl … | bash` puts the script on
> stdin, which the prompts would otherwise consume. Download first, then run:
>
> ```bash
> curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh -o install.sh
> ARBITRON_INSTALL_TYPE=local \
> ARBITRON_LLM_ACCESS=api-key ARBITRON_API_KEY=sk-ant-... \
> ARBITRON_SKIP_PERMS=no ARBITRON_SKIP_CLAUDE_LOGIN=1 \
>   bash install.sh
> ```

The interactive `claude /login` step is **auto-skipped** whenever stdin is not a
TTY (or set `ARBITRON_SKIP_CLAUDE_LOGIN=1` explicitly); authenticate later with
`claude /login`.

**Common variables (Local and Shared)**

| Variable | Prompt | Default | Notes |
|---|---|---|---|
| `ARBITRON_INSTALL_TYPE` | Install type | `shared` if root on Linux, else `local` | `local` \| `shared` (macOS is local-only) |
| `ARBITRON_ACTION` | Existing-install action | `upgrade` if installed, else `fresh` | `fresh` \| `upgrade` \| `reconfigure` \| `uninstall` |
| `ARBITRON_VERSION` | — | latest release | Pin a release, e.g. `1.2.3` |
| `ARBITRON_PORT` | Dashboard / bind port | `8080` | |
| `ARBITRON_LLM_ACCESS` | LLM access | `subscription` | `subscription` \| `api-key` |
| `ARBITRON_API_KEY` | Anthropic API key | — | Required in `api-key` mode (secret) |
| `ARBITRON_SKIP_PERMS` | Skip agent permissions | `no` | `yes` enables `--dangerously-skip-permissions` (see [security.md](security.md#dangerously-skip-permissions)) |
| `ARBITRON_DATA_DIR` | Data location | `~/.arbitron/data` (local), `/var/lib/arbitron` (shared) | |
| `ARBITRON_INSTALL_OPENCODE` | Install opencode backend | `no` | `yes` \| `no` |
| `ARBITRON_BOOTSTRAP_BREW` | Install Homebrew if missing (macOS) | prompt | `yes` \| `no` |
| `ARBITRON_SKIP_CLAUDE_LOGIN` | — | unset | `1` skips the interactive `claude /login` |

**Shared-server variables (`ARBITRON_INSTALL_TYPE=shared`)**

| Variable | Prompt | Default | Notes |
|---|---|---|---|
| `ARBITRON_BIND_HOST` | Bind host | `0.0.0.0` | |
| `ARBITRON_AUTH_MODEL` | Auth model | `edge-local` | `edge-local` \| `oidc` |
| `ARBITRON_OIDC_ISSUER` | OIDC issuer URL | — | OIDC mode |
| `ARBITRON_OIDC_CLIENT_ID` | OIDC client ID | — | OIDC mode |
| `ARBITRON_OIDC_CLIENT_SECRET` | OIDC client secret | — | OIDC mode (secret → `arbitron.env`) |
| `ARBITRON_PUBLIC_BASE_URL` | Public base URL | — | OIDC mode; redirect derives as `<url>/auth/callback` |
| `ARBITRON_OIDC_BOOTSTRAP_ADMINS` | Bootstrap admins | — | OIDC mode |
| `ARBITRON_OIDC_REDIRECT` | Redirect URL | derived | Override the derived callback |
| `ARBITRON_RBAC` | Enable RBAC | `no` | `yes` exposes the next three |
| `ARBITRON_RBAC_GROUP_CLAIM` | Group claim | `groups` | |
| `ARBITRON_RBAC_DEFAULT_GROUP` | Default group | `default` | |
| `ARBITRON_RBAC_LEADS` | Department leads | — | `dept=a,b;dept2=c` |
| `ARBITRON_TLS_MODEL` | TLS model | — | `tunnel` \| `direct` \| `autocert` (see [tunnels-and-tls.md](tunnels-and-tls.md)) |
| `ARBITRON_TLS_CERT` / `ARBITRON_TLS_KEY` | Cert / key paths | — | `direct` mode |
| `ARBITRON_AUTOCERT_DOMAIN` / `ARBITRON_AUTOCERT_CACHEDIR` | Autocert domain / cache | — | `autocert` mode (forces port 443) |
| `ARBITRON_SVC_USER` | Service account name | `arbitron` | |
| `ARBITRON_MASTER_KEY` | — | generated | Provide to reuse an existing key; otherwise generated silently |

**Uninstall (`ARBITRON_ACTION=uninstall`)**

| Variable | Prompt | Default |
|---|---|---|
| `ARBITRON_REMOVE_DATA` | Also remove all data (irreversible) | `no` |
| `ARBITRON_REMOVE_ACCOUNT` | Also remove the service account (shared) | `no` |

**CI / advanced knobs:** `ARBITRON_REPO` (fork), `ARBITRON_VERIFY_SIGNATURE=1`
(require a cosign signature), `ARBITRON_HEALTH_RETRIES` / `ARBITRON_HEALTH_SLEEP`
(post-install health-check tuning), `ARBITRON_CLAUDE_PATH` (pre-resolved `claude`
binary), `ARBITRON_PKG_SUDO`.

## Browser for workflow auto-arrangement

Rendered layout review needs a local **Chrome or Chromium** executable, including
when running `arbitron mcp` without an open dashboard. Install it on the server
host; the installers do not install a browser. Arbitron auto-detects standard
Chrome/Chromium locations, or configure an explicit executable:

```yaml
layout:
  browserpath: /usr/bin/chromium
  browsernosandbox: false
  artifactdir: /var/lib/arbitron/layout-artifacts
```

The equivalent environment variables are `ARBITRON_LAYOUT_BROWSERPATH`,
`ARBITRON_LAYOUT_BROWSERNOSANDBOX`, and `ARBITRON_LAYOUT_ARTIFACTDIR`.

**macOS:** the full `Google Chrome.app` registers with the Dock on every launch,
even headless, so a server arranging workflows makes the Dock flicker. Use the
Chrome for Testing headless shell instead, which has no Dock icon. Install it
(for example `npx @puppeteer/browsers install chrome-headless-shell@stable`) and
point `layout.browserpath` (or `ARBITRON_LAYOUT_BROWSERPATH`) at the **absolute**
path of its `chrome-headless-shell` executable. Auto-detection prefers a
`chrome-headless-shell` found on the server's `PATH` over `Chrome.app`, but nothing
puts it there by default, and an absolute path keeps the server from executing
whatever binary of that name a writable `PATH` directory happens to hold.
The artifact directory defaults to `layout-artifacts` beside `database.path`;
the service account needs permission to write overview and tile PNG evidence there.
Captures are written owner-only (directories `0700`, files `0600`) because
screenshots show workflow text. They are review inputs, not an archive: a
discarded or replaced capture is deleted at once, a revision keeps at most its
selected capture until a newer save supersedes it, and startup removes leftover
captures that no stored result references once they are older than two hours.
The age threshold means several processes can share one data directory.

**Sandbox.** Keep Chromium's sandbox enabled (`browsernosandbox: false`, the
default) on hosts and in containers. The browser always starts with a minimal
environment (no `ARBITRON_MASTER_KEY` or provider API keys), but without the
sandbox a compromised renderer runs as the arbitron service user, with access to
the database, workflow files and any credentials mounted into the container
(for example `claude-config` and `gh-config`). Arbitron logs a startup warning
whenever the sandbox is disabled.

- **`docker compose` (supported):** `docker-compose.yaml` sets
  `ARBITRON_LAYOUT_BROWSERNOSANDBOX=false` and loads
  `docker/seccomp-chromium.json` with `security_opt: seccomp=...`. The profile is
  Docker's default profile plus `clone`, `unshare`, `setns` and `chroot`,
  which Chromium's unprivileged user-namespace sandbox needs. The container gets
  no added capabilities. If your host kernel disables unprivileged user
  namespaces (`kernel.unprivileged_userns_clone=0`, `user.max_user_namespaces=0`,
  or an AppArmor userns restriction), arrangement fails at the `rendering`
  stage. Allow user namespaces, or accept the fallback below.
- **Bare `docker run` (fallback):** the image sets
  `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true` only so that a plain `docker run` works
  under Docker's default seccomp profile, which blocks the sandbox. The container
  is then the only isolation boundary around the renderer. To keep the sandbox,
  run with `--security-opt seccomp=docker/seccomp-chromium.json -e
  ARBITRON_LAYOUT_BROWSERNOSANDBOX=false`. Rendering in a separate restricted
  container is planned but not yet available.

A missing browser logs a boot warning but does **not** prevent startup or saving
workflows. Arrangement fails visibly at the `rendering` stage, retains the saved
positions, and remains retryable after installing/configuring the browser. A build
missing `render.html` instead fails startup; rebuild embedded assets with `make ui`.
For development, `make test-render` requires a real browser and fails rather than
skips when none is available; `make test-all` includes that gate.

## Codex backend (optional, user-provided)

The **codex** agent backend lets agent nodes (`agent_call`, `interactive_agent`)
run on OpenAI's Codex CLI driving your **ChatGPT subscription** instead of
claude-cli or opencode. It is **not** installed by `install.sh`
— codex is a **user-provided prerequisite** you set up once on the host. (This is
the only backend the installer does not manage; claude-cli and opencode are
handled by the toolchain step above.)

To use it:

1. **Install the Codex CLI** on the host (per OpenAI's instructions). Arbitron
   targets **codex-cli 0.137.0+** and resolves the `codex` binary from `PATH`
   and the standard install dirs.
2. **Sign in:** run `codex login` and complete **Sign in with ChatGPT** so a
   valid subscription session is written to `~/.codex` (or `$CODEX_HOME`). The
   engine sets **no `OPENAI_API_KEY`** — the on-disk session is codex's only
   credential surface.
3. **Point a profile at it:** set a profile's `backend: "codex"` and a codex
   `model:` (e.g. the feature-pipeline sample ships `interviewer` on
   `backend: codex` / `model: gpt-5.5`). The backend axis is independent of the
   provider/connection.

Arbitron surfaces a **readiness check** on `system_config` (HTTP
`GET /system/config` and the MCP `system_config` tool) reporting
`ready` / `not-installed` / `not-logged-in` for the codex backend, so an
unusable backend is caught **before** a run rather than failing mid-mission.

> **Security — `--dangerously-bypass-approvals-and-sandbox`.** The codex backend
> runs `codex exec --dangerously-bypass-approvals-and-sandbox`, which bypasses
> both approvals and codex's own sandbox — the same fully-open posture as
> claude-cli's `--dangerously-skip-permissions` and opencode's allow-all. On
> model-influenced prompts this is **RCE-equivalent**, accepted only for
> **local, single-user, unattended** use where every prompt input is trusted,
> and contained solely by the OS/container the agent runs in. See
> [the agent security model](../agent-security-model.md) for the full posture
> and the container boundary that is the real isolation surface.

> **Scope.** codex is **subscription-only and agent-tier only**. API-key OpenAI
> access (and chat-tier use) remains available via the **opencode** backend.

## Pick your scenario

| | Scenario | OS / class | Script | Privilege |
|---|---|---|---|---|
| **a** | [macOS local](#a-macos-local) | macOS, single user | `install.sh` | no sudo |
| **b** | [Linux local](#b-linux-local) | Linux, single user | `install.sh` | no sudo |
| **c** | [Linux shared server](#c-linux-shared-server) | Linux, multi-user | `install.sh` | **sudo / root** |

macOS is **local-only** (there is no Shared path). On Linux the script defaults
to Shared when run as root, Local otherwise.

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

## b) Linux local

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

## c) Linux shared server

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
