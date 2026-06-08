# Installers P1 — Task 2: dependency resolution

> Phase 1 (P1) of the Arbitron installers feature. Source design:
> `docs/superpowers/specs/2026-06-05-installers-design.md` (§Dependency
> resolution) and `docs/superpowers/specs/installers-design-phase-1.md`
> (§Dependency resolution). Builds on Task 1's helpers.

## Context

This task delivers the dependency-resolution engine for the local install: for
each agent-feature dependency it checks presence + minimum version, then escalates
through OS package manager → bootstrap-the-package-manager → direct-download, and
checksum-verifies every download before use. Arbitron itself has **zero** runtime
deps (self-contained `CGO_ENABLED=0` binary); all of this is for the **agent**
features (the `claude-cli` provider path), so a deployment that only ever uses the
`anthropic` HTTP provider can decline the agent toolchain.

Dependencies (per the design): `node`, the `claude` CLI, `git`, `gh` (required for
the agent path); `opencode` (optional). On macOS the package manager is `brew`
(bootstrap Homebrew if absent); on Linux it is whichever of `apt`/`dnf`/`pacman`/
`zypper` is present. `claude` installs via `npm -g` into an **Arbitron-owned npm
prefix** (`~/.arbitron/runtime/npm`) so it never fights a system Node. The
package-manager abstraction and the download/verify path are factored so P2's
shared/system install reuses them (with sudo).

Depends on: Task 1 (helpers: `require_cmd`, `command_version`, `verify_checksum`,
logging, prompts, layout). Feeds: Task 3 (binary download reuses the verified-
download helper), Task 5 (tests), P2.

## Test plan (write first)

Create `tests/install/deps.bats`. Mock every package manager, `npm`, `curl`, and
the version probes by putting fake executables on `PATH` (mock-bin dir from Task 1's
test_helper) that record their argv to a log file the assertions read. Network is
never touched.

Presence + version:
- When `node` is present and `node --version` ≥ the minimum → resolver is a no-op
  (asserts NO package-manager invocation recorded).
- When `node` is present but BELOW minimum → resolver attempts an upgrade/install
  via the package manager (asserts install command recorded).
- When `git`/`gh` are already present → no install attempted for them.

Package-manager selection:
- On macOS (mocked `uname`), an absent `node` triggers `brew install` (asserts the
  recorded argv is `brew install node` or the chosen formula).
- On Linux with only `apt` present → `apt-get install -y …` recorded.
- On Linux with only `dnf` present → `dnf install -y …` recorded.
- On Linux with only `pacman` → `pacman -S --noconfirm …`; with only `zypper` →
  `zypper install -y …`.
- Package-manager precedence is deterministic when several are mocked present
  (documented order, e.g. apt > dnf > pacman > zypper) — assert the chosen one.

Bootstrap:
- On macOS with NO `brew` on PATH and the bootstrap prompt accepted → the Homebrew
  install command is invoked (mocked), then `brew install node` proceeds.
- On macOS with NO `brew` and bootstrap DECLINED → falls through to direct-download
  of node into `~/.arbitron/runtime/` (asserts download helper called with the node
  artifact, not the package manager).

`claude` CLI via Arbitron-owned npm prefix:
- Installing `claude` runs `npm install -g @anthropic-ai/claude-code` with the npm
  prefix pointed at `~/.arbitron/runtime/npm` (asserts `npm_config_prefix` / `npm
  --prefix` and that the resulting `claude` bin path under that prefix is recorded
  for later use as `llm.binarypath`).
- When a system `claude` already satisfies the version → no npm install attempted.

Checksum enforcement (mandatory):
- A direct-download with a MATCHING sha256 succeeds and the artifact is used.
- A direct-download with a MISMATCHED sha256 aborts non-zero and the artifact is
  NOT executed/installed (asserts `verify_checksum` failure short-circuits).

opencode (optional):
- When the user declines the optional `opencode` prompt → resolver records no
  opencode install and continues successfully.

shellcheck:
- `install.sh` still passes `shellcheck` clean after this task.

## Implementation steps

1. Add minimum-version constants (e.g. `MIN_NODE_MAJOR`) near the top of
   `install.sh`, sourced by the version checks.
2. Add `detect_pkg_mgr`: on macOS returns `brew`; on Linux probes
   `apt-get`/`dnf`/`pacman`/`zypper` in a documented precedence and returns the
   first found, or empty.
3. Add `pkg_install <pkg...>`: dispatches to the detected manager with its correct
   non-interactive flags (`brew install`, `apt-get install -y`, `dnf install -y`,
   `pacman -S --noconfirm`, `zypper install -y`). Local install runs these as the
   user (no sudo); leave a documented seam where P2 prefixes `sudo`.
4. Add `bootstrap_pkg_mgr`: macOS Homebrew bootstrap (prompted), returning success/
   failure so the caller can fall back.
5. Add `download_verified <url> <sha256> <dest>`: `curl -fsSL` (with redirects) →
   temp file → `verify_checksum` (Task 1) → move into place; abort on mismatch.
   This is the shared download primitive Task 3 reuses for the binary.
6. Add `ensure_node`: presence+version check → `pkg_install node` → bootstrap →
   `download_verified` node into `~/.arbitron/runtime/` and PATH-shim it.
7. Add `ensure_claude`: if a satisfying `claude` exists, record its path; else
   `npm install -g @anthropic-ai/claude-code` with `npm_config_prefix`/`--prefix`
   set to `$(layout_runtime)/npm`; record the resolved `claude` path for Task 3 to
   write as `llm.binarypath`.
8. Add `ensure_git`, `ensure_gh` (package-manager-first, download fallback).
9. Add `ensure_opencode` (optional; prompted; skipped cleanly on decline).
10. Add `resolve_dependencies`: orchestrates the above in order; only runs the
    agent-toolchain deps when the install will use the `claude-cli`/subscription
    path (Task 3 sets a flag based on the LLM-access answer) — an API-key-only
    install may skip node/claude.
11. Run `shellcheck`; write `deps.bats`; make green.

## Files

- `install.sh` (modified) — dependency-resolution functions.
- `tests/install/deps.bats` (new).
- `tests/install/test_helper.bash` (modified, if extra mock scaffolding needed).

## Acceptance criteria

- [ ] Each dependency follows presence/version → pkg-mgr → bootstrap →
      direct-download, with the correct command per platform/manager (tested).
- [ ] `claude` installs into the Arbitron-owned npm prefix and its path is captured
      for `llm.binarypath`; a satisfying system `claude` is reused.
- [ ] Every download is checksum-verified; a mismatch aborts and nothing unverified
      is executed/installed.
- [ ] `opencode` is optional and a decline is non-fatal.
- [ ] API-key-only installs can skip the agent toolchain.
- [ ] No sudo on the local path; the pkg-install seam is factored for P2 to add sudo.
- [ ] `shellcheck install.sh` clean.

## Dependencies

- Depends on: Task 1 (helpers, layout, checksum, prompts).
- Required by: Task 3 (download primitive), Task 5 (tests), P2.
