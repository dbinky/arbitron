# Installers — Phase 2 (P2): `install.sh` shared (Linux server)

> Decomposed from `docs/superpowers/specs/2026-06-05-installers-design.md`. Phase
> numbering matches the master: this is **P2**. It **extends the `install.sh` built
> in P1** with the shared/system-level path. P0 (Go prerequisites) is merged.

## Scope

Add the **(d) Linux shared server** scenario to `install.sh`. Shared installs are
**system-level**: require sudo/admin, use system paths, run under a dedicated
`arbitron` service account, and support multi-user auth + public exposure.

This is a branch within the same script, selected at the "install type:
Local | Shared" decision. macOS is local-only (no shared path). Windows shared is
**P4**.

Out of scope: the local path itself (P1), Windows (P3/P4), release machinery (P5).

## Layout (from master matrix, linux shared column)

| | Linux shared |
|---|---|
| Script | `install.sh` (sudo) |
| Service | systemd **system** unit |
| Run as | dedicated `arbitron` account |
| Binary | `/usr/local/bin/arbitron` |
| Config | `/etc/arbitron/config.yaml` |
| Data/db | `/var/lib/arbitron/` |
| Workspaces | `/var/lib/arbitron/workspaces/` |
| Logs | `/var/log/arbitron/` (or journald) |
| Secrets env | `/etc/arbitron/arbitron.env` (0600, `root:arbitron`) |
| Auth | edge+local **or** OIDC |

## Privilege + account

- Detect elevation; require sudo for the Shared path (fail fast with guidance if not
  elevated).
- Create the dedicated `arbitron` system account (no login shell) if absent; own the
  data/log dirs appropriately; secrets env is `0600 root:arbitron`.

## Config Q&A (shared)

- Bind host / port.
- **Auth model**: **edge-auth + local mode** (installer enforces loopback bind so
  Arbitron is reachable only via an authenticated tunnel; single shared admin)
  **or** **OIDC org mode**.
- If OIDC: issuer URL, client ID, client secret, redirect URL, bootstrap admins,
  and optional RBAC (group claim, default group, department leads).
- **TLS model**: behind tunnel/proxy → set `PublicBaseURL` (+ Secure cookies) and
  print guidance; **or** direct app-TLS → cert + key paths (P0 `server.tls`); **or**
  autocert domain (P0 autocert path). Note P0's fail-fast validations apply
  (autocert requires `server.port == 443`; local-mode + TLS on a non-loopback host
  requires the explicit `allowunauthenticated` escape hatch).
- "Enable unattended agent tools?" — the same explicit RCE-warning prompt as local.
- Service account name (default `arbitron`).
- Data location (default shown).
- Master key auto-generated silently into the secrets env.

## Service definition (systemd system)

`/etc/systemd/system/arbitron.service`:
- `User=arbitron`, `EnvironmentFile=/etc/arbitron/arbitron.env`,
  `WorkingDirectory=/var/lib/arbitron`.
- Hardening: `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`,
  `ReadWritePaths=/var/lib/arbitron`.
- Passes `--config /etc/arbitron/config.yaml`.

## Tunnel / proxy + TLS

Operator runs their own tunnel/proxy (cloudflared the likely initial path). The
installer **never touches the tunnel** — for the edge path it sets `PublicBaseURL` +
Secure cookies and prints guidance. The direct-TLS path uses P0's `server.tls`
(BYO cert/key or autocert).

## Secrets handling (shared)

- API key / master key → `/etc/arbitron/arbitron.env` (0600, `root:arbitron`),
  referenced via systemd `EnvironmentFile=`.
- Subscription login (`claude /login`) writes to `CLAUDE_CONFIG_DIR` under the data
  dir, run as the `arbitron` account (or device login on a headless box).

## Upgrade / reconfigure / uninstall (shared)

Same three flows as P1 but system-level: swap `/usr/local/bin/arbitron`, restart
the system unit; reconfigure rewrites `/etc/arbitron/config.yaml`; uninstall removes
the unit/binary/config and prompts before removing `/var/lib/arbitron` data
(default keep). Optionally remove the `arbitron` account on uninstall.

## Testing

- `bats` with mocked `sudo`/`useradd`/`systemctl` and downloads, asserting the
  system-unit contents, ownership/mode of `/etc/arbitron/arbitron.env`, and the
  written shared `config.yaml` (including the auth/TLS permutations).
- `shellcheck` clean. Exercised in CI containers.

## Dependencies on other phases

- **Builds on P1**: reuses install-type detection, dependency resolution,
  download/verify, and the Q&A engine; adds the Shared branch.
- **Consumes P0**: `server.tls` (cert/key + autocert) and the `--config` flag for
  the system unit; relies on P0's validation behavior for the shared TLS choices.
- **Relates to P4**: the shared auth/TLS Q&A and OIDC question set should be kept
  parallel to the Windows shared (`install.ps1`) path so both installers ask the
  same questions.
