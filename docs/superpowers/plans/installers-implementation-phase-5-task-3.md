# Installers P5 — Task 3: User-facing documentation (install, upgrade/reconfigure/uninstall, security, tunnel/TLS)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. This is **Task 3 of 3** in P5. Design: `docs/superpowers/specs/2026-06-05-installers-design.md`; phase doc `docs/superpowers/specs/installers-design-phase-5.md` Deliverable 3.

## Context

**Delivers:** the complete user-facing installer documentation under `docs/install/`, covering:
- All five install scenarios (a–e): the one-line command, what gets installed and where (the layout matrix), the config questions, the resulting service, and the post-install summary.
- Upgrade / reconfigure / uninstall, including the **forward-only migration** caveat (migrations run on `serve` start and are one-way; downgrading a binary across a migration boundary is unsupported).
- Security notes: the `--dangerously-skip-permissions` RCE-equivalent warning; the shared-TLS + local-mode exposure caveat (P0 `allowunauthenticated` escape hatch); secrets-file modes (0600, `root:arbitron` on shared Linux; DPAPI/service-env on Windows); the verification trust root (SHA256 mandatory; cosign signature optional defense-in-depth).
- Tunnel/proxy guidance (operator-run; cloudflared the likely initial path) and the `PublicBaseURL` + Secure-cookies edge path.

**Depends on:** **Task 1** (artifact names + cosign trust root to document the verify story) and **Task 2** (stable script URLs, `ARBITRON_VERSION`, the fail-closed checksum contract). Reflects the behavior of the **P1–P4** scripts (layouts, Q&A, services, upgrade/reconfigure/uninstall flows) and **P0** config (`server.tls`, `PublicBaseURL`, `allowunauthenticated`, `--config`). Docs must match the master design's **Scenario → layout matrix** verbatim.

**Locked decisions for this task:**
- Docs live under `docs/install/` (new dir): an `index.md` overview + per-scenario sections, an `upgrading.md`, a `security.md`, and a `tunnels-and-tls.md`. Keep example commands copy-pasteable and exactly matching the scripts' real URLs/env vars.
- Documentation only — **no code, no script behavior changes** in this task. Where docs and script behavior disagree, that is a P1–P4 bug to file, not to paper over.

## Test plan (write first)

1. **Markdown link-check.** Every internal link (cross-doc + to repo files like `docs/superpowers/specs/2026-06-05-installers-design.md`) and external link resolves. Wire `markdown-link-check` (or `lychee`) over `docs/install/**` into CI (or a `make docs-linkcheck` target); the gate is zero broken links. External links may be allowlisted/skipped if rate-limited, but the GitHub raw script URLs and the releases URLs MUST be checked for shape.
2. **Example-command lint / self-consistency.** A small check (bats or a script under `tests/release/docs_commands.bats`) asserts:
   - the one-line install commands in `docs/install/*.md` reference exactly `https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh` (curl|bash) and `…/install.ps1` (irm|iex) — the same strings Task 2 fixed;
   - any `ARBITRON_VERSION=` example uses the documented form;
   - shell fences tagged `bash`/`powershell` containing install/upgrade/uninstall commands pass `shellcheck` (extract bash fences) where they are standalone runnable snippets (best-effort; mark non-runnable illustrative fences with `text`).
3. **Matrix fidelity.** A check that the paths quoted in `docs/install/index.md` for each scenario match the master design's matrix (grep the 6 path rows per column against the design doc, or at minimum a manual reviewer checklist item — automate the binary/config/data path strings comparison).
4. **Coverage checklist (reviewer gate, in the PR):** all five scenarios a–e present; upgrade/reconfigure/uninstall present with the forward-only caveat; all three security notes present; tunnel + PublicBaseURL guidance present.

## Implementation steps

- [ ] **Step 1: `docs/install/index.md` — overview + the five scenarios.**
  - Intro: one binary, `serve`/`mcp` subcommands, zero hard runtime deps; agent features pull `node`/`claude`/`git`/`gh` (link the master design's Background section).
  - A **"pick your scenario"** table mapping a–e to OS/class and the one-line command.
  - The **one-line commands** (verbatim, matching Task 2):
    - macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash`
    - Windows (PowerShell): `irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex`
    - Pinning: `ARBITRON_VERSION=1.2.3 curl -fsSL …/install.sh | bash` and `$env:ARBITRON_VERSION='1.2.3'; irm …/install.ps1 | iex`.
  - Per scenario (a mac local, b win local, c linux local, d linux shared, e win shared): reproduce the master matrix row (binary/config/data/workspaces/logs/secrets/service/auth paths), the config questions asked, the service that gets created, and a sample post-install summary (dashboard URL, service name, log path, next steps). Note macOS is local-only and that shared (d/e) needs sudo/admin.

- [ ] **Step 2: `docs/install/upgrading.md` — upgrade / reconfigure / uninstall.**
  - **Upgrade:** re-run the same one-line command; the script detects the existing install and offers Upgrade (download new binary, checksum-verify, swap, restart service). Note version pinning via `ARBITRON_VERSION` and how to check the current version (`arbitron version`, from Task 1).
  - **Forward-only migration caveat (prominent callout):** DB migrations run automatically on `serve` start (`adaptsqlite.RunMigrations`) and are **forward-only**; there is no supported down-migration. Downgrading to an older binary after an upgrade that ran a migration is unsupported — back up the DB before upgrading if a rollback path is required, and roll back by restoring that DB copy alongside the old binary.
  - **Reconfigure:** re-run → choose Reconfigure → the Q&A is pre-filled from the existing `config.yaml`; data + existing secrets are preserved.
  - **Uninstall:** re-run → Uninstall → removes service/binary/config; prompts before removing data (default **keep**); on shared, optionally removes the `arbitron` account. Document per-OS what is removed (paths from the matrix).

- [ ] **Step 3: `docs/install/security.md` — security notes.**
  - **`--dangerously-skip-permissions` (RCE-equivalent warning):** explain that enabling unattended agent tools lets the agent run shell/file/network actions without per-action approval — equivalent to granting remote code execution to whoever can submit work; only enable on a machine/account you accept that risk for; it is asked explicitly on every install and never silently defaulted.
  - **Shared-TLS + local-mode exposure caveat:** local/edge-auth mode trusts an authenticated tunnel and binds loopback; binding a public interface in local mode (or terminating TLS at the app while in local mode on a non-loopback host) requires the explicit P0 `allowunauthenticated` escape hatch — and doing so exposes an unauthenticated admin. Document when that escape hatch is acceptable (only behind a separately-authenticated edge) and the safer default (OIDC org mode for direct public exposure).
  - **Secrets file modes:** `~/.arbitron/arbitron.env` is `0600`; shared Linux `/etc/arbitron/arbitron.env` is `0600 root:arbitron` referenced via systemd `EnvironmentFile=`; Windows uses the service environment / DPAPI. The master key (32 random bytes → base64) is generated up front and never logged; a Claude-only/API-key-only deployment simply never uses it.
  - **Download trust root (from Tasks 1–2):** every downloaded artifact is SHA256-verified against the release `SHA256SUMS` **before** execution — this is mandatory and not bypassable. For defense-in-depth, `SHA256SUMS` is signed with cosign keyless; verify it with:
    `cosign verify-blob SHA256SUMS --signature SHA256SUMS.sig --certificate SHA256SUMS.pem --certificate-identity-regexp 'https://github.com/dbinky/arbitron/\.github/workflows/release\.yml@refs/tags/v.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.
    Explain the trust root = the release workflow's GitHub OIDC identity.

- [ ] **Step 4: `docs/install/tunnels-and-tls.md` — tunnel/proxy + TLS guidance.**
  - **Operator-runs-the-tunnel:** the installer never provisions or touches a tunnel/proxy; cloudflared is the likely initial path. For the edge path the installer sets `PublicBaseURL` + Secure cookies and prints guidance; you point your tunnel at the loopback origin.
  - **Edge path (recommended for public exposure):** HTTP origin behind an authenticating tunnel/proxy; set `PublicBaseURL` to the public https URL so cookies/redirects are correct and Secure cookies are enabled; keep the app bound to loopback. Include a minimal cloudflared example pointing at `http://127.0.0.1:8080`.
  - **Direct app-TLS path (P0 `server.tls`):** BYO cert/key (`certfile`/`keyfile`) or autocert (`autocertdomain` + `autocertcachedir`); note P0 fail-fast rules (cert requires key and vice-versa; autocert vs cert/key are mutually exclusive; enabled requires one of them). Mention autocert needs public DNS + reachable :80/:443.
  - Cross-link `security.md` for the local-mode + public-exposure caveat.

- [ ] **Step 5: Cross-link from the repo's primary docs entry.**
  - Add a short "Installation" pointer to `docs/install/index.md` from the top-level README / docs index (whichever is the canonical entry) so the install docs are discoverable. Do not duplicate content — link.

- [ ] **Step 6: Wire the doc checks (test plan).**
  - Add `make docs-linkcheck` (markdown-link-check/lychee over `docs/install/**`) and `tests/release/docs_commands.bats` (URL/command self-consistency + shellcheck of runnable bash fences). Add both to the CI docs/install job.

## Files

- `docs/install/index.md` (new) — overview + five scenarios + one-line commands + per-scenario matrix/Q&A/service/summary.
- `docs/install/upgrading.md` (new) — upgrade / reconfigure / uninstall + forward-only migration caveat.
- `docs/install/security.md` (new) — skip-permissions RCE warning, local-mode/TLS exposure + `allowunauthenticated`, secrets modes, download trust root + cosign verify.
- `docs/install/tunnels-and-tls.md` (new) — tunnel/proxy + `PublicBaseURL`/Secure-cookies edge path + direct `server.tls`/autocert.
- `README.md` / top-level docs index (modify) — "Installation" pointer to `docs/install/index.md`.
- `Makefile` (modify) — `docs-linkcheck` target (and fold `tests/release/docs_commands.bats` into `test-release`).
- `tests/release/docs_commands.bats` (new) — example-command / URL self-consistency + bash-fence shellcheck.

## Acceptance criteria

- [ ] All five scenarios (a–e) documented with the correct one-line command, the layout-matrix paths, the config questions, and the resulting service — matching the master design matrix verbatim.
- [ ] Upgrade, reconfigure, and uninstall documented, with a prominent **forward-only migration** caveat (no supported down-migration; back up DB before upgrade for rollback).
- [ ] Security notes cover: `--dangerously-skip-permissions` RCE-equivalent warning; shared-TLS + local-mode exposure caveat (`allowunauthenticated`); secrets file modes (0600 / root:arbitron / DPAPI); and the mandatory SHA256 + optional cosign trust root.
- [ ] Tunnel/proxy guidance present (operator-run; cloudflared example) with the `PublicBaseURL` + Secure-cookies edge path and the direct `server.tls`/autocert path.
- [ ] One-line install commands in the docs match the Task-2 stable URLs exactly; `ARBITRON_VERSION` pin example present for both shells.
- [ ] `make docs-linkcheck` passes (zero broken internal links); `tests/release/docs_commands.bats` passes; runnable bash fences are shellcheck-clean.
- [ ] Install docs are linked from the canonical repo docs entry point.

## Dependencies

- **Task 1:** artifact names + cosign trust root (for the verify/security section).
- **Task 2:** stable script URLs, `ARBITRON_VERSION` behavior, fail-closed checksum contract.
- **P1–P4:** the actual layouts, Q&A, services, and upgrade/reconfigure/uninstall flows being documented.
- **P0 (merged):** `server.tls`, `PublicBaseURL`, `allowunauthenticated`, `--config`, `arbitron version`.
