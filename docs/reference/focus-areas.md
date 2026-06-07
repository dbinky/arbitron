# Focus Areas Review Tracking

This document tracks the systematic review of the **Arbitron cross-platform installers** (`install.sh`, `install.ps1`, the supporting Go changes, release machinery, and docs) for correctness, security (fail-closed verification + credential secrecy), and cross-platform parity.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | install.sh — skeleton & shared helpers (`install.sh` — OS/arch/elevation detection, install-type select, Fresh/Upgrade/Reconfigure/Uninstall dispatch, logging/prompt/checksum helpers, `ARBITRON_INSTALL_LIB_ONLY` guard, `layout_*` resolvers) | [x] | [x] | Done |
| 2 | install.sh — dependency resolution (`install.sh` — node/claude/git/gh/opencode: presence+version → brew/apt/dnf/pacman/zypper → bootstrap → `download_verified` direct-download; Arbitron-owned npm prefix) | [x] | [x] | Done |
| 3 | install.sh — local config Q&A + config.yaml + secrets env (`install.sh` — port/LLM/skip-perms/data-location prompts, absolute-path loopback config, `~/.arbitron/arbitron.env` 0600 + generated master key) | [x] | [x] | Done |
| 4 | install.sh — local service & lifecycle (`install.sh` — launchd plist, systemd `--user` + linger, health-check, upgrade/reconfigure/uninstall keep-data-default) | [x] | [x] | Done |
| 5 | install.sh — shared skeleton (`install.sh` — `require_root`, dedicated `arbitron` account, `/etc` + `/var/lib` + `/var/log` layout + ownership/modes, `/usr/local/bin/arbitron`) | [x] | [x] | Done |
| 6 | install.sh — shared config Q&A (`install.sh` — bind host/port, edge+local vs OIDC[+RBAC], 3 TLS models w/ P0 guards, service account; `/etc/arbitron/config.yaml` + `/etc/arbitron/arbitron.env` 0600 root:arbitron) | [x] | [x] | Done |
| 7 | install.sh — shared systemd system unit & lifecycle (`install.sh` — `User=`/`EnvironmentFile=`/`WorkingDirectory=`, hardening, `CAP_NET_BIND_SERVICE`, shared upgrade/reconfigure/uninstall + optional account removal) | [x] | [x] | Done |
| 8 | install.ps1 — skeleton & shared helpers (`install.ps1` — OS/arch detection, `Get-InstallType`, `%LOCALAPPDATA%` resolver, dispatch, NOEXEC guard, mockable seams) | [x] | [x] | Done |
| 9 | install.ps1 — dependency resolution (`install.ps1` — winget + App Installer bootstrap + checksum-verified direct download; Arbitron-owned npm prefix) | [x] | [x] | Done |
| 10 | install.ps1 — local config Q&A + config + secrets (`install.ps1` — same Q&A as P1, absolute-path loopback config, master key + API key via user-env/DPAPI, never logged) | [x] | [x] | Done |
| 11 | install.ps1 — local Scheduled Task & lifecycle (`install.ps1` — at-logon `Register-ScheduledTask`, health-check, upgrade/reconfigure/uninstall) | [x] | [x] | Done |
| 12 | install.ps1 — shared skeleton (`install.ps1` — `Test-IsElevated` fail-fast, `%ProgramFiles%`/`%ProgramData%` layout, LocalSystem vs dedicated account + ACLs) | [x] | [x] | Done |
| 13 | install.ps1 — shared config Q&A + secrets (`install.ps1` — shared Q&A parallel to P2, `%ProgramData%\Arbitron\config.yaml`, DPAPI machine-scope secrets) | [x] | [x] | Done |
| 14 | install.ps1 — shared Windows Service & lifecycle (`install.ps1` — `New-Service` auto-start, LocalSystem/dedicated `-Credential` + logon-as-service, no shim, shared upgrade/reconfigure/uninstall) | [x] | [x] | Done |
| 15 | Go — `arbitron version` subcommand (`cmd/arbitron/version.go` — ldflags-stamped version/commit/date, dev sentinels, root `--version`) | [x] | [x] | Done |
| 16 | Go — native Windows service awareness (`cmd/arbitron/service_windows.go` + `service_other.go` — build-tagged SCM detect/dispatch + no-op twin, `runUnderServiceManager` wired into serve) | [x] | [x] | Done |
| 17 | Release machinery (`Makefile` dist/release/clean-dist/test-release + `.github/workflows/release.yml` — artifact naming, `SHA256SUMS`, cosign keyless, reproducibility, self-verify) | [x] | [x] | Done |
| 18 | Fail-closed checksum verification (both scripts — `verify_download` / `Test-Download`: missing/tampered/unlisted SHA256SUMS aborts; no bypass; optional cosign) | [x] | [x] | Done |
| 19 | Hosting & version selection (both scripts — `releases/latest` redirect default, `ARBITRON_VERSION` pin, `dbinky/arbitron` URL constant flow-through) | [ ] | [ ] | |
| 20 | Documentation (`docs/install/README.md` + `upgrading.md` + `security.md` + `tunnels-and-tls.md` — all 5 scenarios accurate to scripts, migration/security/cosign notes) | [ ] | [ ] | |
| 21 | Test harness integrity (`tests/install/` bats + Pester mocks, `tests/release/`, recording fakes, lib/NOEXEC guards, CI in `.github/workflows/installers.yml`) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 22 | install.sh local + install.ps1 local (cross-platform parity: same Q&A order/semantics, equivalent config + summary output) | [ ] | |
| 23 | install.sh shared + install.ps1 shared (parity of the shared auth/TLS/OIDC question set and the P0 invariant guards) | [ ] | |
| 24 | Fail-closed verification + Release machinery (the `SHA256SUMS`/cosign the workflow produces are exactly what the scripts consume; naming + format match) | [ ] | |
| 25 | Hosting/version selection + version subcommand (`releases/latest`/`ARBITRON_VERSION` resolution vs the binary's stamped `arbitron version`; upgrade comparison degrades gracefully) | [ ] | |
| 26 | Shared config Q&A + Secrets handling (OIDC client secret + master key route to the env file/DPAPI via `ARBITRON_AUTH_OIDC_CLIENTSECRET`, never into config.yaml) | [ ] | |
| 27 | install.ps1 shared service + Go windows/svc (the registered service runs the service-aware binary; BinaryPathName + serve `--config` contract matches the SCM dispatch) | [ ] | |
| 28 | Shared config Q&A + P0 TLS invariants (installer prompt guards exactly mirror the loader's fail-fast: cert XOR autocert, autocert⇒443, local+TLS+non-loopback⇒allowunauthenticated) | [ ] | |
| 29 | Documentation + scripts (every documented command/path/Q&A/service in `docs/install/` matches actual script behavior; `docs_commands.bats` covers the URLs) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- Every behavior has test coverage (happy / success / failure / error / edge), with package managers, downloads, and service tooling mocked.
- The code does what the phase design doc says it should.
- Error handling is thorough — no silent failures; fail-closed where security-relevant.
- Helpers/seams are clean (local and shared reuse one set; no divergent duplicates).

**Pass 2 (Robustness & Parity):**
- Code aligns to the spirit of the design, not just surface conformance.
- Secrets stay sealed on every path; file modes are correct.
- Test assertions test real risks (tamper cases, missing tools, non-root/non-admin), not just happy-path ceremony.
- Edge cases handled (existing install, partial download, declined dependency, headless box).

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- The contract flows cleanly across the boundary (artifact naming, URL constants, Q&A order, config keys).
- Parity assumptions hold end-to-end (sh ↔ ps1, local ↔ shared, script ↔ binary, script ↔ docs).
- No assumption in one component the other doesn't satisfy.
- Integration/parity tests cover the seam, not just unit-level happy paths.
