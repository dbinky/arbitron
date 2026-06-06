# Installers P5 — Task 2: Script hosting, version selection, and the fail-closed checksum-verification contract

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. This is **Task 2 of 3** in P5. Design: `docs/superpowers/specs/2026-06-05-installers-design.md` §Distribution & versioning; phase doc `docs/superpowers/specs/installers-design-phase-5.md` Deliverable 2.

## Context

**Delivers:**
- A **stable URL** for `install.sh` and `install.ps1` (entry points `curl … | bash` / `irm … | iex`).
- **Latest-by-default** release selection with **`ARBITRON_VERSION` pinning** in both scripts.
- The **mandatory, fail-closed checksum-verification contract** the P1–P4 scripts call: a shared `verify_download` (bash) / `Test-Download` (PowerShell) helper that downloads `SHA256SUMS`, looks up the artifact's expected hash, computes the actual hash, and **aborts the install** (non-zero exit, no execution of the artifact) on any mismatch, missing entry, or missing/unfetchable `SHA256SUMS`.
- Tests proving verification **fails closed** on tampered checksum, tampered binary, missing `SHA256SUMS`, and missing-entry cases — extending the P1 `bats` and P3 `Pester` suites.

**Depends on:** **Task 1** (artifact names `arbitron_<version>_<os>_<arch>[.exe]`, the `SHA256SUMS` file, the cosign signature, the `version` subcommand). **P1–P4** (the scripts exist and already call a verify step — this task finalizes the contract those steps implement and re-points any provisional download URL to the stable release/script locations). **P0** (merged).

**Locked decisions for this task:**
- **Stable script URL = the GitHub raw URL on a stable ref.** Primary documented form (Task 3) is `https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh` (and `install.ps1`). The scripts live at **repo root** (per P1/P3). If/when a vanity domain is added it is a CNAME/redirect to the same content — out of scope here; the raw URL is the contract.
- **Release endpoints (no API token needed):**
  - Latest binary: `https://github.com/dbinky/arbitron/releases/latest/download/arbitron_<version>_<os>_<arch>[.exe]` — but the `latest/download/` path requires the literal filename incl. `<version>`, which latest doesn't know. So **resolve the version first**:
    - latest version via the redirect of `https://github.com/dbinky/arbitron/releases/latest` → final URL ends `/tag/v<version>`; parse `<version>`. (No API, no rate-limit token.) Fallback: GitHub API `releases/latest` `.tag_name` if the redirect parse fails.
    - pinned: `ARBITRON_VERSION` set ⇒ use it verbatim (accept both `1.2.3` and `v1.2.3`; normalize to no-leading-`v` for the filename, keep `v` for the tag path).
  - Download base for a resolved version: `https://github.com/dbinky/arbitron/releases/download/v<version>/<artifact>` and `…/v<version>/SHA256SUMS` (+ `.sig` + `.pem`).
- **Mandatory gate = SHA256 match.** Signature (`cosign verify-blob`) is **optional defense-in-depth**: verify it when `cosign` is present on PATH (or when `ARBITRON_VERIFY_SIGNATURE=1`), otherwise skip with a printed notice — but the SHA256 check is **never** skippable. There is **no** `--skip-verify`/env bypass for the checksum.
- **Fail-closed semantics:** any of {`SHA256SUMS` fetch fails, artifact not listed in `SHA256SUMS`, computed≠expected, hash tool absent} ⇒ print a clear error, delete the partial download, exit non-zero, and **never `chmod +x` / run / install** the artifact.

## Test plan (write first)

These extend `tests/install/` (P1 bats, P3 Pester). The verify helper must be factored so tests can drive it with local fixture files (a `file://` or local-path download shim), independent of the network.

**Bash (`tests/install/verify.bats`, shellcheck-clean):**
1. **Happy path:** given a fixture binary + a correct `SHA256SUMS` listing it, `verify_download <file> <expected-name> <sumsfile>` exits 0.
2. **Tampered binary (FAIL CLOSED):** corrupt one byte of the binary; same `SHA256SUMS` ⇒ helper exits non-zero, prints a mismatch error, and the binary is **not** marked executable / not moved into place.
3. **Tampered checksum (FAIL CLOSED):** flip a hex digit in the `SHA256SUMS` entry ⇒ non-zero exit (computed≠expected).
4. **Missing `SHA256SUMS` (FAIL CLOSED):** point at a non-existent sums file ⇒ non-zero exit, explicit "could not fetch/read SHA256SUMS" message; install aborts.
5. **Missing entry (FAIL CLOSED):** `SHA256SUMS` exists but does not list `<expected-name>` ⇒ non-zero exit, "no checksum for <name>" message.
6. **Version selection:** with `ARBITRON_VERSION=1.2.3`, the URL builder yields `…/releases/download/v1.2.3/arbitron_1.2.3_<os>_<arch>…` and `…/v1.2.3/SHA256SUMS`; with `ARBITRON_VERSION=v1.2.3` it normalizes identically; unset ⇒ the version-resolver function is invoked (mock the redirect to return `tag/v9.9.9` and assert the builder uses `9.9.9`).
7. **No-bypass:** there is no flag/env that makes `verify_download` return 0 on a mismatch (grep the script: no `skip.*verif`/`--insecure` checksum bypass path; the only optional skip is the *signature* step).

**PowerShell (`tests/install/Verify.Tests.ps1`, PSScriptAnalyzer-clean):** mirror cases 1–6 against `Test-Download` / `Get-ArbitronVersion` / `Get-ArtifactUrl`, mocking `Invoke-WebRequest` to return fixture content and using `Get-FileHash -Algorithm SHA256`. Assert that on mismatch/missing the function throws (terminating error) and the artifact is removed.

**Integration (optional, gated):** a `bats` case that, against a real published pre-release tag from Task 1's dry-run, downloads the real `SHA256SUMS` + binary and verifies end-to-end (skipped when offline).

## Implementation steps

> The P1/P3 scripts already contain a verify step. This task **factors it into one well-defined helper** with the contract above, points it at the Task-1 artifact layout, and adds the version resolver. If P1/P3 implemented an ad-hoc verify, replace it with this helper and keep the call sites.

- [ ] **Step 1 (bash): version resolver + URL builder.**
  - Add to `install.sh`:
    - `arbitron_resolve_version()` — if `$ARBITRON_VERSION` set, normalize (strip leading `v`) and echo it; else follow the `releases/latest` redirect (`curl -fsSLI -o /dev/null -w '%{url_effective}'`), parse the trailing `tag/v<ver>`, echo `<ver>`; on parse failure fall back to the API `tag_name`. Fail closed (non-zero) if no version can be determined.
    - `arbitron_artifact_name(version, os, arch)` → `arbitron_<version>_<os>_<arch>` (+ `.exe` on windows — bash path won't hit windows but keep the helper total).
    - `arbitron_release_base(version)` → `https://github.com/dbinky/arbitron/releases/download/v<version>`.
  - Keep all three pure (string in/out) so the bats tests drive them without network; the network call is isolated in `arbitron_resolve_version` behind a `curl` wrapper the tests can stub.

- [ ] **Step 2 (bash): the fail-closed `verify_download` helper.**
  - Signature: `verify_download <downloaded_file> <artifact_basename> <sha256sums_file>`.
  - Logic: read the expected hash for `<artifact_basename>` from `<sha256sums_file>` (grep `"  <name>$"`); if absent → error + return 1. Compute actual (`sha256sum` or `shasum -a 256`, whichever exists; if neither → error + return 1). Compare case-insensitively; mismatch → error + `rm -f` the download + return 1. Match → return 0. **No bypass path.**
  - A separate `verify_signature <sha256sums_file>` (optional): if `cosign` present or `ARBITRON_VERIFY_SIGNATURE=1`, run `cosign verify-blob` with the Task-1 identity flags; on failure return 1 (so when explicitly requested it is also fail-closed); when cosign absent and not requested, print a one-line notice and return 0.
  - Wire the download flow: fetch `SHA256SUMS` (+ `.sig`/`.pem` if verifying signature) → fetch the binary to a temp file → `verify_download` (abort on non-zero) → optional `verify_signature` → only then `chmod +x` + move into the install location. Ensure every early-return leaves nothing executable in place.

- [ ] **Step 3 (PowerShell): mirror Steps 1–2 in `install.ps1`.**
  - `Get-ArbitronVersion` (env pin or `releases/latest` redirect via `Invoke-WebRequest -MaximumRedirection 0`/`-Method Head`, parse tag; API fallback).
  - `Get-ArtifactUrl` / `Get-ArtifactName` builders (`.exe` suffix).
  - `Test-Download -Path -Name -SumsFile` using `Get-FileHash -Algorithm SHA256`; throw a terminating error on mismatch/missing/missing-entry and `Remove-Item` the partial download; no bypass for the checksum. Optional `Test-Signature` gated on `cosign` presence / `$env:ARBITRON_VERIFY_SIGNATURE`.

- [ ] **Step 4: Re-point provisional URLs.**
  - Replace any placeholder/`TODO` download base or version logic left in P1–P4 with the resolver + `arbitron_release_base` / `Get-ArtifactUrl`. Grep both scripts for hard-coded `releases/` URLs and ensure they all flow through the new builders.

- [ ] **Step 5: Write the tests (Step "test plan" above) and make them pass.**
  - `tests/install/verify.bats` + `tests/install/Verify.Tests.ps1`. Provide a download shim: the helpers should accept already-downloaded local files for the verify functions (they do — they take file paths), and the resolver's `curl`/`Invoke-WebRequest` is the only thing stubbed.
  - Gate: `bats tests/install/verify.bats` PASS; `shellcheck install.sh` clean; `Invoke-Pester tests/install/Verify.Tests.ps1` PASS; `PSScriptAnalyzer` clean. Add to the existing `make test-release` / install-test CI step.

- [ ] **Step 6: Stable-URL self-consistency check.**
  - Add a doc-fixture assertion (used again in Task 3): the one-line install commands in the docs reference exactly `https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh` and `…/install.ps1`, and those files exist at repo root. A tiny bats/CI check greps the docs and `test -f install.sh install.ps1`.

## Files

- `install.sh` (modify) — add `arbitron_resolve_version`, `arbitron_artifact_name`, `arbitron_release_base`, `verify_download`, `verify_signature`; route the download flow through them; remove any provisional URL.
- `install.ps1` (modify) — add `Get-ArbitronVersion`, `Get-ArtifactUrl`/`Get-ArtifactName`, `Test-Download`, `Test-Signature`; route the download flow; remove any provisional URL.
- `tests/install/verify.bats` (new) — fail-closed checksum cases + version-selection + no-bypass.
- `tests/install/Verify.Tests.ps1` (new) — PowerShell mirror.
- (CI) the existing install-test workflow/`make` step (modify) — include the new test files + the stable-URL self-consistency check.

## Acceptance criteria

- [ ] `ARBITRON_VERSION` unset ⇒ scripts resolve and download the **latest** release; set ⇒ they pin to it (accepting `1.2.3` and `v1.2.3`).
- [ ] Both scripts download `SHA256SUMS` and verify the artifact's SHA256 **before** making it executable / installing it.
- [ ] Verification **fails closed** (non-zero exit / terminating error, artifact removed, install aborted) on: tampered binary, tampered checksum, missing `SHA256SUMS`, artifact-not-listed, and no-hash-tool.
- [ ] There is **no** checksum bypass flag/env; the only optional layer is the cosign **signature** check (defense-in-depth, gated on cosign presence / `ARBITRON_VERIFY_SIGNATURE`).
- [ ] `tests/install/verify.bats` + `tests/install/Verify.Tests.ps1` pass; `shellcheck`/`PSScriptAnalyzer` clean.
- [ ] All release download URLs in both scripts flow through the version resolver + URL builders (no hard-coded/provisional URLs remain).
- [ ] The documented stable script URLs resolve to the repo-root `install.sh` / `install.ps1`.

## Dependencies

- **Task 1:** artifact naming, `SHA256SUMS`, signature files, and their release-download URLs; the `version` subcommand (for upgrade comparison in P1–P4).
- **P1 + P3:** the scripts and their `bats`/`Pester` suites this task extends; P2/P4 reuse the same helpers via the shared script.
- **P0 (merged).**
- **Feeds Task 3:** the stable script URLs, the `ARBITRON_VERSION` usage, and the verification/trust model documented there.
