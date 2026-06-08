# P3 — Task 2: dependency resolution (winget + App Installer bootstrap + fallback)

> Phase **P3** (`install.ps1` local, Windows) — PowerShell mirror of P1 Task 2.
> Master design: `docs/superpowers/specs/2026-06-05-installers-design.md`.
> Phase design: `docs/superpowers/specs/installers-design-phase-3.md`.

## Context

**Delivers:** the dependency-resolution engine for `install.ps1`. Per dependency
(`node`, `claude` CLI, `git`, `gh`; `opencode` optional): check presence + min
version → install via `winget` → if winget absent, offer to bootstrap **App
Installer** → if declined/impossible, direct-download that one dependency into an
Arbitron-owned prefix. `claude` installs via `npm -g` into an **Arbitron-owned npm
prefix** so it never fights a system Node. Every download is **checksum-verified**
(via Task 1's `Test-Sha256`) before use.

**Mirrors P1 Task 2:** same dependency list, same min-version checks, same
four-step resolution ladder, same Arbitron-owned npm prefix for `claude`. Only the
package manager differs (`winget` instead of `brew`/`apt`).

**Depends on:** Task 1 (helpers: logging, prompts, `Test-Sha256`, paths).

**Feeds P4:** the resolver must take the dependency set + target prefix as input so
the Shared branch can resolve into machine-wide prefixes; do not hardcode the
local paths inside the resolver.

## Test plan (write first)

Pester suite `tests/install/Install.Deps.Tests.ps1`. Mock `Get-Command`,
`winget`, `npm`, `Invoke-WebRequest`/`Start-BitsTransfer`, and `Test-Sha256`.

- **Presence check, present:** when `Get-Command node` resolves and
  `node --version` ≥ min, `Resolve-Dependency -Name node` does nothing (no winget
  call) — assert winget mock not invoked.
- **Presence check, too old:** `node --version` below min → resolver proceeds to
  install (assert winget invoked).
- **winget install path:** with `node` missing and `winget` present,
  `Resolve-Dependency -Name node` invokes
  `winget install --id OpenJS.NodeJS … --accept-source-agreements --accept-package-agreements`
  (assert the mock received the correct package id and silent flags).
- **Package id mapping:** asserts the id table — node → `OpenJS.NodeJS`,
  git → `Git.Git`, gh → `GitHub.cli` — each routed correctly.
- **winget absent → bootstrap prompt:** when `Get-Command winget` fails,
  `Resolve-Dependency` calls the App Installer bootstrap helper (mock
  `Install-AppInstaller`); on accept it retries winget.
- **bootstrap declined → direct download:** when winget absent and the user
  declines bootstrap (mock prompt → No), the resolver direct-downloads the
  dependency into the Arbitron-owned prefix and calls `Test-Sha256` before use —
  assert download cmdlet + checksum gate both invoked, in that order.
- **checksum failure aborts:** when `Test-Sha256` returns `$false` for a
  downloaded artifact, the resolver throws and does NOT install/extract it
  (assert no extract/Move-Item after a failed verify).
- **claude via npm prefix:** `Resolve-Dependency -Name claude` runs `npm` with the
  Arbitron-owned prefix set (`npm config set prefix <…\Arbitron\npm>` or
  `npm install -g --prefix …`) and installs `@anthropic-ai/claude-code`; assert the
  prefix points under the Arbitron paths and NOT a system location.
- **claude requires node:** `claude` resolution first ensures `node` is resolved
  (assert node resolved before the npm -g call).
- **opencode optional:** when `opencode` is missing, the resolver logs that it is
  optional and continues without failing (assert no throw, returns "skipped").
- **PATH augmentation:** after installing into the Arbitron npm prefix, the
  resolver prepends that prefix's bin to the process `PATH` for the rest of the run
  (assert `$env:PATH` updated) — so the freshly installed `claude` is callable.

## Implementation steps

1. `Get-DependencySpec`: table of `{ Name; Command; MinVersion; WingetId;
   VersionArgs; VersionРegEx; Optional }` for node/claude/git/gh/opencode. Keep it
   data-driven so P4 reuses it.
2. `Test-DependencyPresent -Spec`: `Get-Command $Spec.Command`; if found, parse
   `& $cmd $Spec.VersionArgs` and compare to `MinVersion` via `[version]`.
3. `Install-AppInstaller`: prompt (Task 1 yes/no); on accept, download the App
   Installer (`Microsoft.DesktopAppInstaller`) MSIX bundle, `Test-Sha256`, then
   `Add-AppxPackage`. On decline return `$false` so the caller falls back.
4. `Install-ViaWinget -Spec`: invoke
   `winget install --id <id> --silent --accept-source-agreements --accept-package-agreements`;
   surface non-zero exit as an error.
5. `Install-ViaDirectDownload -Spec -Prefix`: download the official archive/installer
   for the dependency into `<Prefix>\runtime\<name>`, `Test-Sha256` (mandatory),
   extract/place, return the resolved binary path.
6. `Install-ClaudeCli -Paths`: ensure node resolved; set/derive an Arbitron-owned
   npm prefix (`Join-Path $Paths.Bin '..\npm'` or a dedicated `NpmPrefix` path from
   Task 1); `npm install -g --prefix <prefix> @anthropic-ai/claude-code`; prepend
   `<prefix>` (and its bin) to the process `PATH`.
7. `Resolve-Dependency -Name -Paths`: orchestrate the ladder
   present? → winget → bootstrap App Installer → direct download; route `claude`
   through `Install-ClaudeCli`; treat `opencode` as optional (skip, do not fail).
8. `Resolve-AllDependencies -Paths`: iterate the spec list; aggregate results;
   return a summary object (resolved/skipped) for the final summary print.
9. Every download path funnels through `Test-Sha256` from Task 1; no artifact is
   executed/extracted before verification.

## Files

- `install.ps1` (modified) — dependency-resolution functions.
- `tests/install/Install.Deps.Tests.ps1` (new).

## Acceptance criteria

- [ ] Each dependency: presence + min-version check before any install.
- [ ] Missing dependency installs via `winget` with silent/agreement flags and the
      correct package id.
- [ ] winget absent → App Installer bootstrap offered; accept retries winget,
      decline falls back to direct download.
- [ ] Direct downloads are checksum-verified before use; a mismatch aborts.
- [ ] `claude` installs via `npm -g` into an Arbitron-owned prefix (not system);
      node ensured first; prefix bin added to PATH.
- [ ] `opencode` is optional and never fails the install.
- [ ] Resolver is data-driven and prefix-parameterized for P4 reuse.
- [ ] All Pester tests green; PSScriptAnalyzer clean.

## Dependencies

- **Task 1** (helpers, paths, `Test-Sha256`).
- **P0** (merged).
- Conceptual mirror of **P1 Task 2**; keep the dependency set + ladder parallel.
