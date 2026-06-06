# Installers P1 — Task 5: bats suite hardening, shellcheck, CI wiring & end-to-end

> Phase 1 (P1) of the Arbitron installers feature. Source design:
> `docs/superpowers/specs/2026-06-05-installers-design.md` (§Testing strategy) and
> `docs/superpowers/specs/installers-design-phase-1.md` (§Testing). Consolidates and
> hardens the tests written test-first in Tasks 1–4 and wires them into CI.

## Context

Tasks 1–4 each ship their own bats files written test-first. This task consolidates
that suite, fills coverage gaps, adds an end-to-end "fresh local install" smoke test
that runs the whole script with everything mocked, and wires `shellcheck` +
`bats` into CI (Linux container for the apt/dnf/systemd paths; a macOS runner for
the launchd/brew path). It also factors the mock scaffolding so P2 can reuse it.

The five `bats` scenario categories from the project's strict-TDD convention are
covered across the suite: happy path, input validation, error/failure handling,
idempotency/re-run, and security/permissions (0600 secrets, no-secret-logging,
checksum enforcement). This task ensures each category is represented and green.

Depends on: Tasks 1–4 (all functions + their per-task bats files). Feeds: P2/P5
(shared mock harness + CI job).

## Test plan (write first)

This task IS the test task; the "write first" discipline here means defining the
end-to-end and cross-cutting scenarios before wiring CI, then making them pass.

End-to-end (`tests/install/e2e.bats`):
- A full `Fresh` macOS install with all externals mocked (uname=Darwin, brew/npm/
  claude/launchctl/curl recording fakes, Q&A via `ARBITRON_*` env overrides) →
  produces config.yaml (absolute paths, loopback), `arbitron.env` (0600, base64
  32-byte master key), installs the binary, loads the LaunchAgent, and passes the
  mocked health check; final summary prints the dashboard URL.
- The same for a full `Fresh` Linux install (uname=Linux, apt + systemctl/loginctl
  mocked).
- Idempotency: running the script a second time detects the existing install and
  routes to the Upgrade/Reconfigure prompt rather than clobbering (re-run safety).

Cross-cutting security (`tests/install/security.bats`):
- The secrets env is `0600` and the master key never appears in captured stdout/
  stderr across a full run.
- Any checksum mismatch (binary OR a dependency download) aborts the entire install
  non-zero and leaves no service registered.
- Local mode always writes a loopback `server.host` (asserts non-loopback is never
  produced by the local path).

Lint:
- `shellcheck install.sh` is clean (no disables without justification).
- bats files themselves pass shellcheck where applicable (or are excluded with a
  documented rationale).

## Implementation steps

1. Create `tests/install/mocks/` with reusable recording-fake scripts (brew, apt-get,
   dnf, pacman, zypper, npm, claude, git, gh, launchctl, systemctl, loginctl, curl,
   openssl) that append their argv to `$MOCK_LOG`. Refactor Tasks 1–4 tests to use
   these instead of ad-hoc inline mocks.
2. Add `tests/install/e2e.bats` and `tests/install/security.bats` per the test plan.
3. Add a `Makefile` target (e.g. `test-install`) that runs `shellcheck install.sh`
   then `bats tests/install`, and document it.
4. Wire CI: add a job/step (matching the repo's existing CI mechanism) that on Linux
   installs `bats` + `shellcheck` and runs `make test-install`; add a macOS runner
   job that exercises `e2e.bats` for the launchd/brew path. (If the repo has no CI
   workflow yet, add the workflow file; otherwise extend the existing one.)
5. Verify all five strict-TDD scenario categories are represented across the suite;
   add any missing input-validation/error cases (e.g. unsupported arch, declined
   bootstrap with no fallback, malformed existing config on reconfigure).
6. Run the full suite locally (or in the container) until green; confirm shellcheck
   clean.

## Files

- `tests/install/mocks/*` (new) — shared recording fakes.
- `tests/install/e2e.bats` (new).
- `tests/install/security.bats` (new).
- `tests/install/*.bats` (modified) — refactor onto shared mocks.
- `Makefile` (modified) — `test-install` target.
- CI workflow file (new or modified) — Linux + macOS install-test jobs.

## Acceptance criteria

- [ ] `make test-install` runs `shellcheck` + the full `bats` suite and passes.
- [ ] End-to-end Fresh installs (macOS + Linux) are green with everything mocked and
      assert the full set of artifacts (config, secrets, binary, service, health).
- [ ] Idempotent re-run routes to Upgrade/Reconfigure, never clobbers.
- [ ] Security scenarios pass: 0600 secrets, no key in logs, checksum-mismatch abort,
      loopback-only bind.
- [ ] All five strict-TDD scenario categories are represented in the suite.
- [ ] CI runs the suite on a Linux container and a macOS runner; both green.
- [ ] Shared mock harness is factored for reuse by P2.

## Dependencies

- Depends on: Tasks 1–4 (all implementation + their per-task bats files).
- Required by: P2 (reuses mock harness + CI job) and P5 (release-pin re-point).
