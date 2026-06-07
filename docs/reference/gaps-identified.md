# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

These are carry-forward items seeded at loop setup — known limitations of the draft implementation that the review loop should confirm, close, or reclassify. They are not bugs found in review yet; verify each against the code and either resolve or document.

### INST-001: Live-binary contracts are mock-only
**Area**: install.sh / install.ps1 dependency resolution + service registration (Focus Areas 2, 4, 7, 9, 11, 14)
**Detail**: bats and Pester mock every package manager (brew/apt/dnf/pacman/zypper/winget), every download, and all service tooling (systemctl/launchctl/Register-ScheduledTask/New-Service). No test exercises a real dependency install, a real `claude` npm install into the Arbitron-owned prefix, or a real OS service start/stop. A per-scenario live smoke test (CI container/VM) is outstanding. Confirm whether the loop should add one or document it as a release gate.

### INST-002: Release workflow unproven on a real tag
**Area**: Release machinery + hosting/version selection (Focus Areas 17, 19; Paired 24, 25)
**Detail**: `make dist` is tested for artifact naming, checksum self-consistency, and reproducibility, and `release.yml` passes `actionlint`, but cosign keyless signing, the `releases/latest` redirect, and the in-workflow self-verify have not run end-to-end against GitHub. The scripts' `releases/latest` / `ARBITRON_VERSION` resolution is unit-tested against mocked redirects only. Verify the redirect-parsing and artifact-URL construction match GitHub's real behavior.

### INST-003: Native windows/svc path is compile/no-op tested only
**Area**: Go native Windows service awareness + install.ps1 shared service (Focus Areas 16, 14; Paired 27)
**Detail**: `service_windows.go` is covered only via the non-Windows no-op twin, the windows-tagged unit tests, and `GOOS=windows go build`. The StartPending→Running→Stop handler has not run under a real Windows SCM, and the installed service's BinaryPathName + `serve --config` dispatch has not been exercised live. A Windows live-service smoke test is outstanding.

### INST-004: Node direct-download fallback intentionally absent
**Area**: install.sh / install.ps1 dependency resolution (Focus Areas 2, 9)
**Detail**: The implementation does package-manager + bootstrap only; the pinned node tarball URL/checksum direct-download fallback from the design's step 6 was deferred (no pinned URL in P1; deferred to P5, which also did not pin it). Confirm whether the bare-machine-with-no-package-manager case actually needs the tarball fallback, or whether package-manager bootstrap (Homebrew / App Installer) is sufficient. If needed, the verified-download primitive already exists — only the pinned source is missing.

## Fixed Previously

### INST-017: PS1 Invoke-SharedConfigQA OIDC sub-questions order mismatch + missing redirect URL derivation (FA23)
**Area**: install.ps1 shared config Q&A / OIDC parity (Paired area 23)
**Fixed**: This iteration
**Detail**: `Invoke-SharedConfigQA` in OIDC mode asked redirect URL BEFORE bootstrap admins and never prompted for "Public base URL" inside the OIDC block (it appeared only in the tunnel TLS branch later). Bash `prompt_oidc` asks: public base URL → bootstrap admins → OIDC redirect URL (with default derived as `publicbaseurl + /auth/callback`). Without the derivation, PS1 operators must type the full callback URL manually with no auto-suggest — a common source of misconfiguration that causes the OIDC token exchange to fail silently at runtime. Fixed by: (1) adding a "Public base URL" prompt in the OIDC block before bootstrap admins; (2) deriving the redirect URL default from it; (3) moving the redirect URL prompt after bootstrap admins; (4) skipping the TLS tunnel re-ask when public base URL was already set by the OIDC block (matches bash's `[ -z "$SCFG_PUBLIC_BASE_URL" ]` guard). `Get-SharedQuestionOrder` updated to reflect the corrected canonical order. Two Pester tests added: order test (`Public base URL before Bootstrap admins before OIDC redirect URL in OIDC block`) and derivation test (redirect URL `$Default` = `publicbaseurl/auth/callback`). 214 bats + 192 Pester, all green; PSScriptAnalyzer: 0 diagnostics.

### INST-016: PS1 Invoke-ConfigQA local Q&A order mismatch (data-location before skip-permissions, FA22)
**Area**: install.ps1 local config Q&A / cross-platform parity (Paired area 22)
**Fixed**: This iteration
**Detail**: `Invoke-ConfigQA` asked data-location (step 3) then skip-permissions (step 4), but bash `run_config_qa` and the P1/P3 task plan both specify the canonical order as port→LLM→skip-permissions→data-location. Operators switching between OS platforms would see different question sequences. Reordered PS1 to match bash. Added a Pester Q&A order test (port→LLM→skip-perms→data-loc, index-based, mirroring the existing shared order test pattern) that fails before the fix and passes after. 214 bats + 190 Pester, all green; PSScriptAnalyzer: 0 diagnostics.

### INST-015: ARBITRON_INSTALL_LIB_ONLY and ARBITRON_INSTALL_NOEXEC guards had no explicit tests (FA21 Pass 2)
**Area**: Test harness integrity (Focus Area 21)
**Fixed**: This iteration
**Detail**: Both installer guards (`ARBITRON_INSTALL_LIB_ONLY` for bash, `ARBITRON_INSTALL_NOEXEC` for PS1) were relied upon by every single test file's setup/BeforeAll, but neither had an explicit test. If either guard were accidentally removed, the failure mode would be hundreds of confusing cascade failures rather than a clear diagnostic. Added a behavioral bats test (`ARBITRON_INSTALL_LIB_ONLY=1 sources install.sh without invoking main (guard integrity)`) that mocks `uname`, sources with the guard, and asserts `MOCK_LOG` is empty (main() was never called). Added two Pester tests to `Install.Skeleton.Tests.ps1`: a structural test verifying the guard pattern is in install.ps1 and gates Invoke-Main, and a behavioral test verifying Invoke-Main is defined (functions loaded) but the suite passed its BeforeAll without Invoke-Main running. 214 bats + 189 Pester, all green.

### INST-014: tests/release/ not exercised in CI (FA21 Pass 1)
**Area**: Test harness integrity (Focus Area 21)
**Fixed**: This iteration
**Detail**: `installers.yml` only ran `make test-install`; `make test-release` (artifact naming, SHA256SUMS self-consistency, version round-trip, docs URL accuracy via `docs_commands.bats` and `dist_naming.bats`) was never executed in CI. A regression in the Makefile, Go sources, or docs could ship without being caught. Added a `release-tests` job (ubuntu-latest, Go + bats + `make test-release`) and extended the path triggers to include `tests/release/**`, `cmd/arbitron/**`, `go.mod`, `go.sum`, and `docs/install/**`. Added a bats assertion `installers.yml exercises make test-release in CI` (in `docs_commands.bats`) as the failing-first TDD anchor; all 17 release tests pass.

### INST-013: PS1 Invoke-SharedConfigQA missing direct-TLS cert+key both-or-neither guard (parity with bash)
**Area**: install.ps1 shared config Q&A / TLS models (Focus Area 20 / paired 23, 28)
**Fixed**: This iteration
**Detail**: `prompt_shared_tls` in bash (line 1084) aborts if either `TLS_CERT` or `TLS_KEY` is empty when `direct` mode is chosen — parity with the binary's P0 loader requirement that cert and key are both-or-neither. `Invoke-SharedConfigQA` in PS1 had no equivalent guard: an operator who provided a cert path but left the key blank (or vice versa) would get through install and find their Windows Service unable to start with a cryptic TLS-config error. Added the guard: throw if `TlsCertFile` or `TlsKeyFile` is empty after prompting. Added two Pester tests (`throws when direct TLS certificate is given without a key` and `key without cert`). Two bats tests added to lock the documented bash behavior. `tunnels-and-tls.md` already documented "The installer aborts if you give only one" — now both scripts honour this invariant.

### INST-012: PS1 Invoke-SharedConfigQA missing LLM access prompt (parity with bash run_shared_qa)
**Area**: install.ps1 shared config Q&A (Focus Area 20)
**Fixed**: This iteration
**Detail**: `Invoke-SharedConfigQA` silently defaulted `LlmMode` to 'subscription' without prompting. Bash `run_shared_qa` asks LLM access as step 8 (after data location). Windows shared operators had no way to configure api-key mode during install. Added the LLM access prompt (step 8) and API key sub-prompt (step 8a) matching the bash order and the same empty-key abort guard. `Get-SharedQuestionOrder` updated. `docs/install/README.md` scenario (e) updated to list LLM access as the final shared Q&A step. Three Pester tests added; question-order test extended; 185 Pester + 211 bats, 0 failures.

### INST-011: Invoke-SharedConfigQA missing empty-OIDC-client-secret guard (parity with INST-009)
**Area**: install.ps1 shared config Q&A (Focus Area 13)
**Fixed**: This iteration
**Detail**: `Invoke-SharedConfigQA` accepted an empty OIDC client secret without complaint. Pressing Enter at the client-secret prompt would proceed, write `mode: org` with all OIDC URLs to config.yaml, but store no `ARBITRON_AUTH_OIDC_CLIENTSECRET` in DPAPI — every OIDC token exchange would fail at runtime. Exact parity gap with INST-009 (install.sh `prompt_oidc` fix). Added the guard: throw if `OidcClientSecret` is empty after the prompt. Added a Pester test `throws when OIDC mode is chosen but the client secret is left empty` to lock the invariant.

### INST-010: upgrade_shared health check probes wrong port (ignored argument)
**Area**: install.sh shared systemd unit & lifecycle (Focus Area 7)
**Fixed**: This iteration
**Detail**: `upgrade_shared` called `health_check_shared "$(read_existing_config_value server.port || echo 8080)"`, passing the
existing-config port as a positional argument. But `health_check_shared` ignores positional parameters and reads the port only
from `SCFG_PORT` or `PORT` env vars. On a non-default port (e.g., 9090), the upgrade health check silently probed port 8080,
giving a false pass (with `|| true`) or a spurious miss. Fixed by setting `PORT` as an env-var prefix:
`PORT="${_up_port:-8080}" health_check_shared || true` where `_up_port` is read from the existing config.
Added bats test `upgrade health check uses the port from the existing config not the env default` to lock the invariant.

### INST-009: prompt_oidc missing empty-client-secret guard (parity with INST-006/INST-008)
**Area**: install.sh shared config Q&A / prompt_oidc (Focus Area 6)
**Fixed**: This iteration
**Detail**: `prompt_oidc` accepted an empty OIDC client secret without complaint. A headless
install (or an operator who pressed Enter at the prompt) would proceed, `write_shared_env`
would silently omit `ARBITRON_AUTH_OIDC_CLIENTSECRET`, and the OIDC IdP would reject every
token exchange at runtime — a silently broken shared install. The same class of bug was fixed
for the API key in INST-006 (P1) and INST-008 (P2). Added the identical guard in `prompt_oidc`:
`die` if `SCFG_OIDC_CLIENT_SECRET` is empty. Added a bats test
`OIDC mode with empty client secret is refused` to lock the invariant.

### INST-008: run_shared_qa missing empty-API-key guard (parity with P1/INST-006)
**Area**: install.sh shared config Q&A (Focus Area 6)
**Fixed**: This iteration
**Detail**: `run_shared_qa` accepted `LLM_ACCESS=api-key` with an empty `ARBITRON_API_KEY`, silently writing `provider: "anthropic"` to config.yaml without an `ANTHROPIC_API_KEY` in arbitron.env — a broken install with no warning. The same bug was fixed in the local path (INST-006, FA3 Pass 1) but not applied to the shared path. Added the identical guard: `die` if `CFG_LLM_ACCESS=api-key` and `CFG_API_KEY` is empty. Added a bats test `api-key mode with empty key fails in run_shared_qa` to lock the invariant.

### INST-005: launchd plist launched binary directly, bypassing the secrets-env wrapper
**Area**: install.sh local service & lifecycle (Focus Area 4 / skeleton review, Focus Area 1)
**Fixed**: Iteration 1
**Detail**: `render_launchd_plist` created a wrapper script (`arbitron-service`) that sources `~/.arbitron/arbitron.env` before exec-ing the binary, but the plist's `ProgramArguments` referenced the raw binary (`${bin}`) instead of the wrapper (`${wrapper}`). On macOS, `ARBITRON_MASTER_KEY` and `ANTHROPIC_API_KEY` were never in the service environment. Fixed by changing ProgramArguments to reference only `${wrapper}`; the wrapper already passes `serve --config`. Updated service_macos.bats: removed the stale assertion that `serve`/`--config` appear in the plist, added two new tests asserting (a) ProgramArguments uses the wrapper and (b) the wrapper sources the secrets env and exec's the binary with serve --config.

<!--
Example of a fixed issue entry:

### INST-005: Secrets env created world-readable
**Area**: install.sh local config (`install.sh`)
**Fixed**: Iteration 3
**Detail**: arbitron.env was written then chmod'd, leaving a brief 0644 window. Switched to umask 077 around the write so it is never group/world-readable. Added a bats assertion on the resulting mode.
-->

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Example of a won't-fix entry:

### INST-W01: Air-gapped / fully offline install
**Detail**: Bundling runtimes and an offline install path is an explicit non-goal in the master design. Out of scope for this review.
-->
