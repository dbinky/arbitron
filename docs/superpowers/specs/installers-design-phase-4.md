# Installers — Phase 4 (P4): `install.ps1` shared (Windows server)

> Decomposed from `docs/superpowers/specs/2026-06-05-installers-design.md`. Phase
> numbering matches the master: this is **P4**. It **extends the `install.ps1` built
> in P3** with the shared/system-level path, and is the Windows mirror of P2. P0
> (incl. native Windows-service awareness via `golang.org/x/sys/windows/svc`) is
> merged.

## Scope

Add the **(e) Windows shared server** scenario to `install.ps1`. Shared Windows
installs are **system-level**: require admin elevation, use `%ProgramFiles%` /
`%ProgramData%`, run under LocalSystem (or a dedicated service account), register a
native **Windows Service**, and support multi-user auth + public exposure.

Selected at the "install type: Local | Shared" decision within the same script.

Out of scope: the local path itself (P3), Unix (P1/P2), release machinery (P5).

## Layout (from master matrix, windows shared column)

| | Windows shared |
|---|---|
| Script | `install.ps1` (admin) |
| Service | Windows Service (native Go `svc`) |
| Run as | LocalSystem / service account |
| Binary | `%ProgramFiles%\Arbitron\arbitron.exe` |
| Config | `%ProgramData%\Arbitron\config.yaml` |
| Data/db | `%ProgramData%\Arbitron\data\` |
| Workspaces | `%ProgramData%\Arbitron\workspaces\` |
| Logs | `%ProgramData%\Arbitron\logs\` |
| Secrets env | service env / DPAPI |
| Auth | edge+local **or** OIDC |

## Privilege + service account

- Detect elevation; require admin for the Shared path (fail fast with guidance).
- Run as **LocalSystem** by default, or a dedicated service account if chosen
  (default name `arbitron`).

## Config Q&A (shared — parallel to P2)

- Bind host / port.
- **Auth model**: edge-auth + local mode (loopback behind an authenticated tunnel;
  single shared admin) **or** OIDC org mode.
- If OIDC: issuer URL, client ID, client secret, redirect URL, bootstrap admins,
  optional RBAC (group claim, default group, department leads).
- **TLS model**: behind tunnel/proxy → `PublicBaseURL` + Secure cookies + guidance;
  **or** direct app-TLS → cert + key paths; **or** autocert domain. P0 fail-fast
  validations apply (autocert ⇒ port 443; local-mode + public-host TLS ⇒ explicit
  `allowunauthenticated`).
- "Enable unattended agent tools?" — explicit RCE-warning prompt.
- Service account name (default `arbitron`).
- Data location (default shown).
- Master key auto-generated silently into the secrets store.

The shared Q&A must stay parallel to P2 (Linux shared) so both installers ask the
same questions in the same order.

## Service definition (native Windows Service)

Register a native auto-start Windows Service that runs
`arbitron.exe serve --config %ProgramData%\Arbitron\config.yaml`. The binary is
service-aware (P0: detects the SCM and dispatches via `windows/svc`), so no
third-party shim (WinSW/nssm) is required. Registered via `New-Service` (or `sc.exe`).

## Secrets handling (Windows shared)

API key / master key via the service environment / **DPAPI** (machine scope).
Subscription login (`claude /login`) writes to `CLAUDE_CONFIG_DIR` under the data
dir, run as the service account (device login on a headless box).

## Upgrade / reconfigure / uninstall (shared)

- **Upgrade** — stop service, swap `%ProgramFiles%\Arbitron\arbitron.exe`,
  checksum-verify, restart. Forward-only migration caveat noted.
- **Reconfigure** — re-run Q&A pre-filled from existing config; rewrite
  `%ProgramData%\Arbitron\config.yaml`; preserve data + secrets.
- **Uninstall** — stop + remove the Windows Service, binary, config; prompt before
  removing `%ProgramData%\Arbitron\data` (default keep).

## Testing

- `Pester` with mocked `New-Service` / `sc.exe` / elevation checks / downloads,
  asserting the service registration, config in `%ProgramData%`, secrets handling,
  and the auth/TLS permutations.
- `PSScriptAnalyzer` clean. Windows CI runner.

## Dependencies on other phases

- **Builds on P3**: reuses install-type detection, dependency resolution,
  download/verify, and the Q&A engine; adds the Shared branch.
- **Consumes P0**: native Windows-service awareness (`windows/svc`) and `server.tls`
  + `--config`.
- **Mirrors P2**: keep the shared auth/TLS/OIDC question set identical across the
  Linux and Windows shared installers.
