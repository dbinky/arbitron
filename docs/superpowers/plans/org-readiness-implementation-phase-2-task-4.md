# Phase 2 — Task 4: opencode LiteLLM mapping + ship both CLIs (image + installer)

**Objective:** Confirm/add the opencode `litellm` → `openai` provider alias so a
LiteLLM-family agent binding routes via `OPENAI_BASE_URL` + `-m openai/<model>`,
and make BOTH agent backends present in every deployment: the `Dockerfile`
installs `@anthropic-ai/claude-code` AND opencode (pinned), and the installers
(`install.sh` / `install.ps1`) install both (opencode is currently optional).

**Dependencies:**
- Task 2 (backend dispatch — `opencode` backend now reachable for a LiteLLM
  connection regardless of provider).
- Independent of Task 3 for the packaging half.

**Spec/design anchors:**
- `docs/specs/org-readiness-spec.md` — Phase 2 ("Both agent backends are present
  in the deployment … so either can be selected at runtime"); Cross-cutting
  ("Both agent backends installed."); acceptance ("A freshly provisioned
  deployment has both agent backends available without any manual post-install
  step.").
- `docs/superpowers/specs/org-readiness-design-phase-2.md` — "opencode (confirm
  only)" and "Deployment packaging".

---

## Part A — opencode LiteLLM alias (code)

### Files to modify
- `domain/provider/canonical.go` — add a `litellm` entry only if needed (today
  `litellm` canonicalizes to itself; see test below). The opencode env table is
  keyed by opencode ids, and `litellm` is NOT currently in
  `adapter/opencode/provider_id.go::providerIDMap`. Choose ONE:
  - **(preferred)** add `"litellm": "openai"` to `providerIDMap` so a LiteLLM
    agent binding maps to the opencode `openai` provider (which already has
    `OPENAI_API_KEY` + `OPENAI_BASE_URL` in `env_providers.go`), OR
  - canonicalize `litellm` → `openai` in `domain/provider/canonical.go` (broader
    blast radius: it would also rewrite the chat-tier provider id, which the chat
    factory currently lists `litellm` explicitly — avoid unless intended).
  - Decision: prefer the `providerIDMap` entry — it is symmetric with the chat
    tier (which already accepts `litellm` via `providerOpenAI`/`providerLiteLLM`
    in `factory.go:130`) and keeps the change to the agent path only.

### TDD plan (tests first) — `adapter/opencode`
Mirror the existing `adapter/opencode/args_test.go` and `env_test.go` styles.

1. **happy — `litellm` provider maps to opencode `openai`.**
   `mapProviderID("litellm")` → `("openai", true)` (red until the table entry is
   added).
2. **happy — `-m openai/<model>` for a litellm binding.** `buildArgs(mission,
   binding{Provider:"litellm", Model:"gpt-4o"}, posture)` → args contain
   `-m openai/gpt-4o`. Asserts the model leaf is not double-prefixed.
3. **happy — env carries `OPENAI_BASE_URL` + `OPENAI_API_KEY`.**
   `opencodeEnv(ctx, binding{Provider:"litellm", BaseURL:"https://litellm:4000",
   RevealKey:->"sk"})` → env contains `OPENAI_BASE_URL=https://litellm:4000` and
   `OPENAI_API_KEY=sk`.
4. **boundary — model already `openai/gpt-4o`.** `modelLeaf` does not double the
   prefix → `-m openai/gpt-4o` (one prefix).
5. **regression — `claude-cli` still unmapped.** `mapProviderID("claude-cli")`
   → `("", false)` (fail-closed; claude-cli must never route through opencode —
   guarded by `provider_id.go`'s deliberate omission).
6. **canonicalization unchanged.** Add/keep a case in
   `domain/provider/canonical_test.go` asserting `Canonicalize("litellm") ==
   "litellm"` (so the chat tier's explicit `litellm` arm is unaffected) — if the
   `providerIDMap` route is chosen, this stays green with no canonical.go change.

### Implementation steps
- Add `"litellm": "openai"` to `providerIDMap` → tests 1–5 green; test 6 stays
  green (no canonical.go change).

---

## Part B — Dockerfile ships both CLIs

### Files to modify
- `Dockerfile` (runtime stage, the `RUN apk add ... && npm install -g
  @anthropic-ai/claude-code` block at lines 33–39):
  - Pin the claude-code version (`@anthropic-ai/claude-code@<pinned>`), matching
    the version the installers pin (keep one source of truth in a comment).
  - Install opencode (pinned). Opencode ships as an npm package and/or a
    direct-download binary; use the same mechanism the installer settles on
    (Part C). If npm: `npm install -g opencode-ai@<pinned>` (confirm the package
    name against the installed modern binary — see the `TODO(opencode-binary)`
    note in `adapter/opencode/args.go:24`). Ensure the resulting binary is on
    `PATH` for the `arbitron` service user (the image already adds npm-global to
    PATH implicitly via the global prefix; verify `opencode` resolves).
  - Keep `bash`/`coreutils` (already present; opencode's Bash tool needs a real
    POSIX shell too).

### Verification (not a Go test — a build/smoke assertion)
- A CI/smoke step (document it; do not author CI here) runs `docker build` then
  `docker run --rm <img> sh -lc 'command -v claude && command -v opencode'` and
  expects both to resolve with exit 0. This is the image-level proof of the
  acceptance criterion "a freshly provisioned deployment has both agent backends
  available."

---

## Part C — Installers ship both CLIs

### `install.sh`
- Today `ensure_opencode` (lines 587–599) is OPTIONAL and prompt-gated
  (`prompt_yes_no INSTALL_OPENCODE ... no`), and `resolve_dependencies` (604–616)
  calls it as a soft step. Change so opencode is installed by default in the
  agent toolchain path:
  - Make opencode a REQUIRED step alongside `ensure_claude` when the install uses
    the agent toolchain (i.e. not the `api-key`-only skip branch at 606–609):
    `ensure_opencode || return 1` (promote from soft to required), OR keep a
    `--no-opencode` opt-out flag but default to install. Pin the version.
  - Keep the existing "already present → record and skip" fast path.
  - Update the `ARBITRON_*` env/flag docs and the dependency-version block near
    line 34–38 to include the opencode pin.

### `install.ps1`
- The dependency spec table (lines 582–589) marks opencode `Optional = $true`.
  Flip to `Optional = $false` (or add a default-on install) so the agent
  toolchain path installs it. Add an `Install-Opencode` function paralleling
  `Install-ClaudeCli` (lines 675–689) using the chosen mechanism (npm `-g`
  `--prefix` into the Arbitron npm prefix if it is an npm package, else
  `Install-ViaDirectDownload` with a pinned `DownloadUrl` + `DownloadSha256`).
  Wire it into the dependency-resolution flow (`Resolve-Dependency`) so a fresh
  install provisions both.

### TDD plan — installers (Pester / bats / shellcheck per existing conventions)
The repo already has installer tests (commit history shows Pester + a portable
`file_mode`/bats helper). Add:

7. **install.sh — agent path installs opencode.** Stub `require_cmd` to report
   opencode ABSENT and stub `pkg_install`/npm to record its argv; run
   `resolve_dependencies` with `ARBITRON_LLM_ACCESS=subscription` and assert the
   opencode install was invoked (and failure now propagates non-zero, proving it
   is required, not soft).
8. **install.sh — api-key path still skips the whole agent toolchain.** With
   `ARBITRON_LLM_ACCESS=api-key`, `resolve_dependencies` returns 0 and neither
   claude nor opencode install is invoked (preserve the existing 606–609 branch).
9. **install.sh — opencode already present → no reinstall.** `require_cmd
   opencode` true → recorded as present, installer not invoked.
10. **install.ps1 — opencode spec is required.** `Get-DependencySpec | where
    Name -eq opencode` has `Optional -eq $false`.
11. **install.ps1 — Install-Opencode invoked on a fresh box.** Mock
    `Test-DependencyPresent` → false and the install primitive (Invoke-Npm or
    Invoke-ArbitronDownload) → assert it is called with the pinned version, and a
    non-zero exit throws (required, fail-loud).
12. **install.ps1 — checksum gate (direct-download path only).** If opencode is
    installed via `Install-ViaDirectDownload`, a checksum mismatch aborts with no
    install (reuse `Test-Sha256` assertions already used for other deps).

### Implementation steps
- Pick the opencode install mechanism (npm vs direct download) consistent across
  Dockerfile + both installers; pin the version in one documented place.
- install.sh: promote `ensure_opencode` to required in the agent path; pin.
- install.ps1: flip `Optional`, add `Install-Opencode`, wire into resolution.

---

## Acceptance criteria (tied to spec Phase 2)

- "An agentic node configured to use opencode completes a task with its model
  calls routed through the gateway": a `litellm` binding maps to opencode
  `openai` with `OPENAI_BASE_URL` + `-m openai/<model>` (Part A tests 1–3).
- "Both agent backends are present in the deployment" / "A freshly provisioned
  deployment has both agent backends available without any manual post-install
  step": image smoke (`command -v claude && command -v opencode`) and installer
  tests 7,9,11 prove both are provisioned (Parts B, C).
- claude-cli must never route through opencode (Part A test 5; fail-closed).

---

## Security notes

- **Pin versions and verify integrity.** Both CLIs are pinned in the image and
  installers; the direct-download installer path verifies a SHA-256 before use
  (test 12), and npm installs pin an exact version — a supply-chain swap of an
  unpinned `latest` is the risk being closed.
- **opencode credential handling is unchanged** by Part A: the `litellm`→`openai`
  alias reuses the existing `OPENAI_*` env contract in
  `adapter/opencode/env.go`, where the key is revealed per call via
  `binding.RevealKey` and placed only in the subprocess env (never argv/logs).
- **No new allowlist entries.** `OPENAI_BASE_URL`/`OPENAI_API_KEY` are injected
  per call by `opencodeEnv`, not added to `procenv.baseAllowlist`; the existing
  `rejectBaseNames` guard still applies.
- **Fail-loud install.** Promoting opencode to a required step means a failed
  install aborts (tests 7,11) rather than silently shipping a half-provisioned
  box that would later fail closed at an opencode node's first run.
