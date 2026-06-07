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
