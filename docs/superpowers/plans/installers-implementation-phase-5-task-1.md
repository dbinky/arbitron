# Installers P5 — Task 1: Release build, cross-compile, artifact naming, SHA256SUMS + signature

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This is **Task 1 of 3** in P5 (release/distribution machinery + documentation). Design: `docs/superpowers/specs/2026-06-05-installers-design.md` §Distribution & versioning; phase doc `docs/superpowers/specs/installers-design-phase-5.md` Deliverable 1.

## Context

**Delivers:** the GitHub Releases artifact set the installer scripts (P1–P4) download and verify. Specifically:
- Cross-compiled `arbitron` binaries named `arbitron_<version>_<os>_<arch>[.exe]` for darwin/linux/windows × amd64/arm64 (6 artifacts).
- A `SHA256SUMS` file covering every artifact.
- A detached **signature** over `SHA256SUMS` (cosign keyless / Sigstore — see decision below), plus the public verification material so the trust root can be documented (Task 3).
- A `make release` / `make dist` target that produces the artifacts + `SHA256SUMS` locally and reproducibly.
- A `.github/workflows/release.yml` GitHub Actions workflow, triggered on a version tag, that runs the Makefile target, signs, and uploads everything to the GitHub Release.
- A `version` subcommand / `--version` flag on the binary so the installer scripts can read the installed version for upgrade comparison, and so artifacts embed their version via ldflags.

**Depends on:** P0 (merged) — the binary already cross-compiles with `CGO_ENABLED=0`, pure-Go SQLite (`modernc.org/sqlite`), embedded SPA via `go:embed`, no cgo. The existing `Makefile` has `ui`, `build`, `test`, `test-ui`, `test-all` targets; `build` is `CGO_ENABLED=0 go build -o arbitron ./cmd/arbitron`. The embedded dashboard lives at `adapter/dashboard/dist` (committed; `make ui` refreshes it). The Go module is `github.com/arbitron/arbitron`; the GitHub repo is `dbinky/arbitron`; Go 1.25.5.

**Locked decisions for this task:**
- **Artifact = the raw binary**, not a tar/zip archive. Name `arbitron_<version>_<os>_<arch>` (`.exe` on windows). `<version>` is the git tag **without** a leading `v` (tag `v1.2.3` → `arbitron_1.2.3_linux_amd64`). This matches the design's literal `arbitron_<version>_<os>_<arch>[.exe]` and keeps the P1–P4 download step a single GET with no unpack.
- **Signature = cosign keyless (Sigstore OIDC)**: `cosign sign-blob` over `SHA256SUMS` producing `SHA256SUMS.sig` + `SHA256SUMS.pem` (the ephemeral cert), verifiable with `cosign verify-blob` against the GitHub Actions OIDC identity. No long-lived private key to store. (Rationale: no secret-key management; trust root = the workflow's OIDC identity `https://github.com/dbinky/arbitron/.github/workflows/release.yml@refs/tags/*`, which Task 3 documents.) The scripts' **mandatory** check is the SHA256 match; signature verification is the optional defense-in-depth layer documented in Task 3.
- **The dashboard is rebuilt in CI** before cross-compiling (do not trust the committed `adapter/dashboard/dist` for a release), via the existing `make ui`, then each target is built from that tree.

## Test plan (write first)

These are the verifiable gates. Run them before wiring the live tag-triggered upload.

1. **Makefile dist target produces correctly named artifacts (local).**
   - `make dist VERSION=0.0.0-test` produces, under `dist/`, exactly:
     `arbitron_0.0.0-test_darwin_amd64`, `arbitron_0.0.0-test_darwin_arm64`,
     `arbitron_0.0.0-test_linux_amd64`, `arbitron_0.0.0-test_linux_arm64`,
     `arbitron_0.0.0-test_windows_amd64.exe`, `arbitron_0.0.0-test_windows_arm64.exe`,
     and `dist/SHA256SUMS`.
   - Assert (a bash test under `tests/release/dist_naming.bats`, shellcheck-clean):
     - all 6 binaries exist with the exact names;
     - `SHA256SUMS` has 6 lines, each `^[0-9a-f]{64}  arbitron_0.0.0-test_<os>_<arch>(\.exe)?$`, and the filenames in it are **basenames only** (no `dist/` path prefix — so `sha256sum -c` works from inside `dist/`);
     - `cd dist && shasum -a 256 -c SHA256SUMS` (or `sha256sum -c`) exits 0 (every checksum matches its file);
     - each non-`.exe` linux/darwin artifact is `CGO_ENABLED=0` (no dynamic libc): on the host-native artifact, `go version -m dist/arbitron_..._<hostos>_<hostarch>` succeeds and the file is a Go binary; `file` on linux artifacts reports "statically linked" / no interpreter where checkable.
   - **Determinism check:** running `make dist VERSION=0.0.0-test` twice produces byte-identical `SHA256SUMS` (build flags `-trimpath -ldflags "-s -w -buildid="` ⇒ reproducible). Assert the two SHA256SUMS files are identical.

2. **Version stamping round-trips.**
   - The host-native dist artifact, run as `./dist/arbitron_0.0.0-test_<hostos>_<hostarch> version`, prints `0.0.0-test` (and commit + date lines). Assert the printed version equals the `VERSION` passed to `make dist`. This is what the P1–P4 upgrade path reads.

3. **Release workflow dry-run / pre-release smoke (CI, no public release).**
   - The workflow can be exercised on a throwaway pre-release tag (e.g. `v0.0.0-rc.test`) **or** validated structurally with `act` where the runner matrix allows. Acceptance: a manually-dispatched run (`workflow_dispatch` with a `dry_run: true` input) builds all 6 artifacts, generates `SHA256SUMS`, signs it, and **uploads to a draft / pre-release** (not `latest`) — then a follow-up job re-downloads the uploaded `SHA256SUMS` + a binary and asserts `sha256sum -c` passes and `cosign verify-blob` passes against the expected OIDC identity. The job fails if any artifact is missing, misnamed, or fails verification.

4. **Workflow YAML lint.** `actionlint .github/workflows/release.yml` is clean (run in CI / locally).

## Implementation steps

- [ ] **Step 1: Add a `version` subcommand fed by ldflags (write test first).**
  - Test: `cmd/arbitron/version_test.go` — `newVersionCmd()` exists and is registered on `newRootCmd()`; with the package vars at their zero/`dev` defaults it prints a non-empty version string; the command writes to its `cmd.OutOrStdout()` so output is assertable.
  - Impl: `cmd/arbitron/version.go` — package-level `var (version = "dev"; commit = "none"; date = "unknown")` and `func newVersionCmd() *cobra.Command` that prints `arbitron <version> (commit <commit>, built <date>)`. Register it in `newRootCmd()` (after `newConnectionCmd()`), and add a root `--version`/`-v` shorthand via `rootCmd.Version = version` + `SetVersionTemplate`. These vars are overridden at build time with `-ldflags "-X main.version=… -X main.commit=… -X main.date=…"`.
  - Gate: `go test ./cmd/arbitron/ -run Version` PASS; `go build ./...` OK.

- [ ] **Step 2: Add `make dist` / `make release` to the Makefile.**
  - Add `.PHONY` entries `dist release` and a `VERSION ?= $(shell git describe --tags --always --dirty | sed 's/^v//')` default so a bare `make dist` works off the current tag.
  - `dist` target:
    1. Depends on `ui` only when `SKIP_UI` is unset is overkill — instead make CI call `make ui` first; the `dist` target itself just cross-compiles from the current `adapter/dashboard/dist`. Document this in a target comment.
    2. Loop over the 6 `GOOS/GOARCH` pairs. For each, build:
       `CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -buildid= -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)" -o dist/arbitron_$(VERSION)_$${os}_$${arch}$$ext ./cmd/arbitron`
       where `ext` is `.exe` for windows.
    3. `COMMIT ?= $(shell git rev-parse --short HEAD)` and `DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)`. For reproducibility prefer `DATE ?= $(shell git show -s --format=%cI HEAD)` (commit date) so two builds of the same commit match — use the commit date, not wall-clock.
    4. After building all 6: `cd dist && (sha256sum arbitron_* > SHA256SUMS 2>/dev/null || shasum -a 256 arbitron_* > SHA256SUMS)` — produce **basename-only** entries (run from inside `dist/`).
  - `release` target = `dist` plus the cosign signing step, gated so it only runs in CI (or with `COSIGN_EXPERIMENTAL`/OIDC available): `cosign sign-blob --yes dist/SHA256SUMS --output-signature dist/SHA256SUMS.sig --output-certificate dist/SHA256SUMS.pem`. Locally without OIDC this is a no-op skip with a printed notice; the live signing happens in the workflow (Step 4).
  - Add a `clean-dist` target (`rm -rf dist`).

- [ ] **Step 3: Add the release-naming bats test.**
  - `tests/release/dist_naming.bats` implementing test-plan checks 1 + 2. Keep it shellcheck/`bats`-clean and runnable on both macOS (`shasum`) and Linux (`sha256sum`) — detect which is present. Add it to whatever runs the install-test suite (a `make test-release` convenience target invoking `bats tests/release`).

- [ ] **Step 4: Create `.github/workflows/release.yml`.**
  - Triggers: `on: push: tags: ['v*']` plus `workflow_dispatch:` with a boolean `dry_run` input (for test-plan check 3).
  - `permissions:` — `contents: write` (to create/upload the release) and `id-token: write` (cosign keyless OIDC). Concurrency group keyed on the ref.
  - Job `build`:
    - `runs-on: ubuntu-latest` (cross-compilation is pure-Go, so one runner builds all 6 — no per-OS matrix needed since `CGO_ENABLED=0`).
    - `actions/checkout@v4` with `fetch-depth: 0` (tags for `git describe`).
    - `actions/setup-go@v5` pinned to Go 1.25.x; `actions/setup-node@v4` for `make ui`.
    - `make ui` (rebuild the SPA from source), then `make dist VERSION=${GITHUB_REF_NAME#v}` (strip leading `v`).
    - Install cosign (`sigstore/cosign-installer@v3`); `make release` (or inline the `cosign sign-blob` over `dist/SHA256SUMS`).
    - **Self-verify before upload:** `cd dist && (sha256sum -c SHA256SUMS || shasum -a 256 -c SHA256SUMS)` and `cosign verify-blob dist/SHA256SUMS --signature dist/SHA256SUMS.sig --certificate dist/SHA256SUMS.pem --certificate-identity-regexp "https://github.com/dbinky/arbitron/.github/workflows/release.yml@.*" --certificate-oidc-issuer https://token.actions.githubusercontent.com`. Fail the job if either fails.
    - Upload: `softprops/action-gh-release@v2` (or `gh release create`) attaching all `dist/arbitron_*`, `dist/SHA256SUMS`, `dist/SHA256SUMS.sig`, `dist/SHA256SUMS.pem`. On `workflow_dispatch` `dry_run`, set `prerelease: true` + `draft: true` and a throwaway tag so it never becomes `latest`.
  - Optional follow-up job `verify-release` (needs: build) that, on a real tag, re-downloads the published `SHA256SUMS` + one binary from the release and re-runs `sha256sum -c` + `cosign verify-blob` — proves the *published* artifacts (not just the build dir) verify, which is exactly what the install scripts will do.

- [ ] **Step 5: Lint + document the trust root inputs for Task 3.**
  - Run `actionlint` on the workflow.
  - Record (for Task 3 to document) the exact `cosign verify-blob` identity flags: `--certificate-identity-regexp 'https://github.com/dbinky/arbitron/\.github/workflows/release\.yml@refs/tags/v.*'` and `--certificate-oidc-issuer https://token.actions.githubusercontent.com`.

## Files

- `cmd/arbitron/version.go` (new) — `version`/`commit`/`date` ldflags vars + `newVersionCmd()`.
- `cmd/arbitron/version_test.go` (new) — version command/flag registration + output test.
- `cmd/arbitron/main.go` (modify) — register `newVersionCmd()`; set `rootCmd.Version` + version template.
- `Makefile` (modify) — add `dist`, `release`, `clean-dist`, `test-release` targets + `VERSION`/`COMMIT`/`DATE` vars; extend `.PHONY`.
- `.github/workflows/release.yml` (new) — tag-triggered build/name/checksum/sign/upload (+ `workflow_dispatch` dry-run + self-verify + optional `verify-release` job).
- `tests/release/dist_naming.bats` (new) — artifact naming, checksum-self-consistency, determinism, version round-trip.

## Acceptance criteria

- [ ] `make dist VERSION=0.0.0-test` emits exactly the 6 correctly-named binaries + `SHA256SUMS` under `dist/`, with `.exe` only on windows.
- [ ] `cd dist && sha256sum -c SHA256SUMS` (or `shasum -a 256 -c`) exits 0; `SHA256SUMS` entries are basenames.
- [ ] Two consecutive `make dist VERSION=…` runs of the same commit yield byte-identical `SHA256SUMS` (reproducible via `-trimpath -buildid=` + commit-date stamp).
- [ ] Host-native artifact `… version` prints the `VERSION` passed to the build.
- [ ] All binaries are `CGO_ENABLED=0` pure-Go (no cgo/libc dependency).
- [ ] `.github/workflows/release.yml` is `actionlint`-clean and has `contents: write` + `id-token: write` permissions.
- [ ] A `workflow_dispatch` dry-run builds + signs + uploads to a draft/pre-release and the in-workflow self-verify (`sha256sum -c` + `cosign verify-blob` against the OIDC identity) passes.
- [ ] `tests/release/dist_naming.bats` is shellcheck/bats-clean and passes on macOS + Linux.
- [ ] The cosign verification identity (workflow OIDC + issuer) is recorded for Task 3's trust-root docs.

## Dependencies

- **P0 (merged):** pure-Go cross-compile, embedded SPA, `--config` flag.
- **Feeds Task 2:** the artifact names + `SHA256SUMS`/signature locations are the exact strings the version-selection + verify contract (Task 2) and the P1–P4 download step consume.
- **Feeds Task 3:** the cosign trust root (OIDC identity + issuer) and the artifact-name scheme are documented there.
