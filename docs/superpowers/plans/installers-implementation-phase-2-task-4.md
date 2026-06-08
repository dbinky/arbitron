# Installers P2 — Task 4: bats permutation matrix + shellcheck CI for the shared path

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.
> This task is itself test-authoring; the "implementation" is the shared test harness +
> CI wiring. Steps use `- [ ]` checkboxes.

Design refs: `docs/superpowers/specs/2026-06-05-installers-design.md` (§"Testing
strategy") and `docs/superpowers/specs/installers-design-phase-2.md` (§"Testing").

## Context

Tasks 1–3 each ship focused bats files. This task adds the **cross-cutting
permutation coverage** and the **CI wiring** that the design's testing strategy calls
for: an end-to-end fresh-install run per shared auth×TLS permutation asserting the
*combined* `config.yaml` + `arbitron.env` + systemd-unit outputs are mutually
consistent, plus a single shared test-helper library and a CI job that runs all
`tests/install/*.bats` in a Linux container and `shellcheck install.sh`.

Fold this into Task 3 if a separate task is undesirable; it is split out because the
permutation matrix is broad and benefits from a shared fixture harness.

## The permutation matrix (each = one end-to-end fresh shared install)

Auth × TLS combinations that must produce a coherent, P0-valid config:

| # | Auth | TLS | Key assertions (combined) |
|---|------|-----|---------------------------|
| 1 | edge+local | tunnel/proxy | `auth.mode=local`, `server.host=127.0.0.1`, `server.tls.enabled=false`, `publicbaseurl` set, `cookiesecure=true`; unit has no `CAP_NET_BIND_SERVICE` |
| 2 | edge+local | direct cert/key | local + loopback bind, `tls.enabled=true`, cert/key set; **no** `allowunauthenticated` needed (loopback) |
| 3 | OIDC org | tunnel/proxy | `auth.mode=org`, oidc.* set, `tls.enabled=false`, `publicbaseurl`+`cookiesecure=true`, secret in env |
| 4 | OIDC org | direct cert/key | `auth.mode=org`, `tls.enabled=true`, cert/key set, public bind allowed (org has auth) |
| 5 | OIDC org | autocert | `auth.mode=org`, `tls.enabled=true`, `autocertdomain` set, `autocertcachedir` set, `server.port=443`, unit has `CAP_NET_BIND_SERVICE` |
| 6 | OIDC org + RBAC | any | `auth.rbac.groupclaim`/`defaultgroup`/`leads.<dept>` present |

For each: assert (a) `config.yaml` keys, (b) `arbitron.env` is 0600 root:arbitron with
master key (+ secrets), (c) the generated unit's `User`/hardening/`--config` are present,
and (d) the config would **pass P0 validation** (see Step 4).

## Test plan (write first)

New bats file `tests/install/shared_matrix.bats` (the permutation runs) and a shared
helper `tests/install/helpers/shared.bash`.

1. One `@test` per matrix row (1–6) driving a full `run_shared_install` with scripted
   answers and the mock-bin PATH, asserting the combined outputs in the table.
2. **Negative / P0-guard rows** (installer must refuse or auto-correct *before* invoking
   the binary, so the operator never hits a load-time failure):
   - autocert chosen with a non-443 port → installer forces 443 or aborts with guidance.
   - direct-TLS with only a cert and no key → installer re-prompts / refuses (mirrors
     P0 "cert without key" rejection).
   - local mode + public (non-loopback) bind + TLS → requires explicit confirm then sets
     `allowunauthenticated=true` (assert both: refusal without confirm; key set with).
3. **Real-binary validation (optional but recommended):** if a built `arbitron` binary
   is available in CI, run `arbitron serve --config <generated> --help`-style dry
   validation (or a config-load smoke that exits after validation) for each generated
   config to prove P0's loader accepts it. Gate behind a `[ -x ./arbitron ]` skip so the
   pure-bats suite still runs without a binary.
4. **Secrets never leak:** across all matrix runs, assert the generated master key and
   any client secret/API key never appear in captured stdout/stderr.

## Implementation steps

- [ ] **Step 1:** Create `tests/install/helpers/shared.bash`: a `mkmock` helper to stamp
  fake `sudo`/`useradd`/`getent`/`systemctl`/`curl`/`install`/`chown`/`chmod` into a
  per-test PATH dir that record argv; an `answers()` helper to feed scripted Q&A; a
  `read_unit`/`read_config`/`read_env` accessor set. Reuse P1's helper if one exists;
  extend rather than fork.
- [ ] **Step 2:** Write `tests/install/shared_matrix.bats` rows 1–6 (positive) — fail
  first (skeleton functions from Tasks 1–3 must be wired before they pass).
- [ ] **Step 3:** Add the negative/P0-guard `@test`s.
- [ ] **Step 4:** Add the optional real-binary validation `@test`s behind a binary-present
  skip guard.
- [ ] **Step 5:** Add a secrets-leak `@test` scanning combined captured output.
- [ ] **Step 6:** CI wiring — add/extend a GitHub Actions job (or the repo's existing CI
  config) that, on a Linux container with `bats` + `shellcheck` installed, runs
  `shellcheck install.sh` and `bats tests/install/`. Ensure the job covers all P2 bats
  files (skeleton, config, service, matrix). Keep the macOS launchd job from P1 untouched.
- [ ] **Step 7:** Run the full suite; fix until green and `shellcheck` clean.

## Files

- Created: `tests/install/shared_matrix.bats`.
- Created/extended: `tests/install/helpers/shared.bash` (and mock-bin fixtures).
- Modified: CI workflow file (e.g. `.github/workflows/*.yml`) to run shellcheck + bats
  over `tests/install/` in a Linux container — extend P1's job rather than duplicating.

## Acceptance criteria

- [ ] All six auth×TLS permutations produce mutually consistent, P0-valid
  `config.yaml` + `arbitron.env` (0600 root:arbitron) + systemd unit.
- [ ] Negative guards (autocert non-443, cert-without-key, local+TLS+public-bind)
  are enforced by the installer before any binary invocation.
- [ ] No secret (master key, client secret, API key) appears in installer output.
- [ ] CI runs `shellcheck install.sh` (clean) and the full `tests/install/` bats suite
  in a Linux container; macOS launchd coverage from P1 is preserved.

## Dependencies

- **Tasks 1–3 (this phase):** the full shared install flow under test.
- **P0 (merged):** the loader validations the matrix asserts against (optionally via the
  real binary).
- **P1 tasks:** the existing CI job + shared test-helper conventions this task extends.
