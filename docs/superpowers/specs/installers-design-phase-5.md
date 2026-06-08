# Installers — Phase 5 (P5): release / distribution machinery + documentation

> Decomposed from `docs/superpowers/specs/2026-06-05-installers-design.md`. Phase
> numbering matches the master: this is **P5**. It formalizes how the binary and
> the installer scripts are published and verified, and writes the user-facing docs.
> P0–P4 are the prerequisites.

## Scope

Stand up the distribution + versioning machinery the installer scripts (P1–P4)
depend on, and document the whole installer surface. This is the phase that makes
`curl … | bash` / `irm … | iex` resolve to real, checksum-verified artifacts.

Out of scope: the installer scripts themselves (P1–P4); the app (P0).

## Deliverables

### 1. Release artifacts (GitHub Releases)

- Cross-compiled binaries named `arbitron_<version>_<os>_<arch>[.exe]` for
  darwin/linux/windows × amd64/arm64 (the binary already cross-compiles with
  `CGO_ENABLED=0`, pure-Go SQLite, embedded SPA).
- A `SHA256SUMS` file covering every artifact, plus a **signature** over it.
- A release workflow (GitHub Actions) that builds, names, checksums, signs, and
  uploads on tag.

### 2. Script hosting + version selection

- `install.sh` and `install.ps1` hosted at a **stable URL**.
- Scripts fetch the **latest** release by default; pinnable via `ARBITRON_VERSION`.
- **Checksum verification is mandatory** before executing any downloaded artifact
  (the scripts already call a verify step in P1–P4; P5 guarantees the `SHA256SUMS`
  + signature exist and documents the trust root).

### 3. Documentation

- Install docs for all five scenarios (a–e): the one-line command, what gets
  installed and where (the layout matrix), the config questions, and the resulting
  service.
- Upgrade / reconfigure / uninstall instructions, including the **forward-only
  migration** caveat (one-way down-migration documented).
- Security notes: the `--dangerously-skip-permissions` RCE warning, the shared-TLS +
  local-mode exposure caveat (P0 `allowunauthenticated`), secrets file modes.
- Tunnel/proxy guidance (operator-run; cloudflared the likely initial path) and the
  `PublicBaseURL` + Secure-cookies edge path.

## Versioning

- `ARBITRON_VERSION` env var pins a release; absent → latest.
- The release tag is the source of truth for the `<version>` in artifact names.

## Testing

- Validate the release workflow produces correctly named artifacts + a verifiable
  `SHA256SUMS` (+ signature) — e.g. a dry-run/`act` or a tagged pre-release smoke.
- A script-level test that checksum verification **fails closed** on a tampered or
  missing checksum (extends the P1/P3 `bats`/`Pester` suites).
- Docs link-check / example-command lint where practical.

## Dependencies on other phases

- **Depends on P1–P4**: the scripts must exist and call a verify step; P5 finalizes
  the artifacts/checksums those steps consume and re-points any provisional download
  URLs to the stable release/script locations.
- **Depends on P0**: artifact set matches the binary's supported os/arch targets.
