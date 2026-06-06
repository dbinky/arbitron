# Installers P4 — Task 4: Pester integration suite + PSScriptAnalyzer + CI (shared permutations)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: these are the cross-cutting end-to-end assertions; per-feature unit tests live in Tasks 1–3.

**Goal:** Lock in the full Windows-shared behavior with an end-to-end Pester suite that walks each meaningful permutation (auth × TLS × run-as) through skeleton → Q&A → config write → service registration → health-check, plus a guard that the shared Q&A stays parallel to P2, a PSScriptAnalyzer gate, and a Windows CI runner wiring.

**Design refs:** `installers-design-phase-4.md` (§Testing); master design §Testing strategy (`install.ps1` → Pester + PSScriptAnalyzer); `installers-design-phase-2.md` (§Testing) for the parallel-question invariant.

---

## Context

**Delivers:**
- An end-to-end Pester suite exercising the Shared path with mocked elevation / `New-Service` / `sc.exe` / account cmdlets / downloads / DPAPI / HTTP, asserting the **combined** result (config.yaml contents + service registration + secrets handling) for each permutation rather than one unit at a time.
- A **parity test**: the shared Q&A prompt sequence in `install.ps1` matches the P2 (`install.sh`) shared question set/order. (If P2's prompt strings are not extractable from bash for cross-language comparison, encode the canonical ordered list once and assert both installers against it; reference the P2 plan's question list as the source.)
- A `PSScriptAnalyzer` invocation (repo ruleset) as a test/CI gate, zero errors for `install.ps1` and the test files.
- Windows CI job wiring (GitHub Actions `windows-latest`) running `Invoke-Pester` + `Invoke-ScriptAnalyzer` for the install scripts.

**Depends on:** P4 **Tasks 1–3** (the behavior under test). P3 test harness conventions (dot-source guard, mock patterns) — reuse them. **Parallel to P2** test suite (mirror the shared permutation coverage).

---

## Test plan (write first)

End-to-end suite `tests/install/Shared.E2E.Tests.ps1` (plus a parity test, optionally same file). Each case scripts the full answer set and asserts the aggregate outcome.

- [ ] **Permutation: edge-auth + tunnel/proxy TLS + LocalSystem:** loopback bind, `PublicBaseURL` + Secure cookies, no `server.tls.enabled`; service as LocalSystem, auto-start; health-check over `http`.
- [ ] **Permutation: edge-auth + direct app-TLS + dedicated account:** `server.tls.enabled` with cert/key; `allowunauthenticated` set when host is non-loopback; service as `arbitron` account; health-check over `https`.
- [ ] **Permutation: OIDC + autocert + LocalSystem:** OIDC block populated; `server.tls.autocertdomain` set with `server.port: 443`; service LocalSystem; health-check `https`.
- [ ] **Permutation: OIDC + RBAC + direct app-TLS + dedicated account:** OIDC + RBAC (group claim/default group/department leads) in config; cert/key TLS; dedicated account run-as.
- [ ] **Shared Q&A parallel to P2:** assert the ordered shared question list equals the canonical list (host/port → auth model → OIDC sub-questions → TLS model → skip-permissions → service account → data location). Fails if P4 drifts from P2.
- [ ] **Master key auto-generated & never logged (e2e):** across permutations, master key is created and absent from transcript.
- [ ] **Non-elevated aborts before any mutation (e2e):** end-to-end run with elevation `$false` performs no service/config/account side effects.
- [ ] **Upgrade e2e:** existing install → stop/swap+verify/restart, config+data untouched.
- [ ] **Uninstall e2e:** service+binary+config removed, data kept by default.
- [ ] **PSScriptAnalyzer gate:** `Invoke-ScriptAnalyzer install.ps1` and the test files return zero error-severity findings.

---

## Implementation steps

- [ ] **Step 1:** Build a permutation harness/helper that feeds a full scripted answer set and a mock environment (elevated, ProgramFiles/ProgramData, mocked cmdlets) and returns the captured config object + recorded cmdlet invocations for assertions.
- [ ] **Step 2:** Write the four auth×TLS×run-as permutation cases asserting the aggregate outcome (config + service + secrets) each.
- [ ] **Step 3:** Define the canonical ordered shared-question list (single source) and write the parity test asserting `install.ps1`'s shared Q&A order against it; cross-link the P2 plan's list in a comment.
- [ ] **Step 4:** Add the master-key-not-logged and non-elevated-no-mutation e2e cases; add Upgrade and Uninstall e2e cases (reusing Task 3 mocks).
- [ ] **Step 5:** Add a PSScriptAnalyzer gate (a Pester case or a CI step) over `install.ps1` + `tests/install/*`.
- [ ] **Step 6:** Add/extend the Windows CI workflow (`.github/workflows/*`) with a `windows-latest` job installing Pester + PSScriptAnalyzer and running both; ensure the Linux `install.sh` (bats/shellcheck) job is unaffected.
- [ ] **Step 7:** Run the full suite green locally (or in CI); PSScriptAnalyzer clean.

---

## Files

- **Create:** `tests/install/Shared.E2E.Tests.ps1` (permutations + parity + e2e flows).
- **Modify/Create:** `.github/workflows/` Windows CI job for `Invoke-Pester` + `Invoke-ScriptAnalyzer`.
- **Modify (only if needed for testability):** `install.ps1` — expose the canonical shared-question list / hooks for the parity and permutation harness.

---

## Acceptance criteria

- [ ] All four auth×TLS×run-as permutations pass end-to-end (config + service + secrets asserted together).
- [ ] Shared Q&A parity-with-P2 test passes (drift fails the build).
- [ ] Master key never logged; non-elevated run mutates nothing; Upgrade/Uninstall e2e behave per spec.
- [ ] `PSScriptAnalyzer` zero error-severity findings for `install.ps1` + tests.
- [ ] Windows CI job runs Pester + PSScriptAnalyzer and is green; Linux job unaffected.

---

## Dependencies

- **P4 Tasks 1–3** (behavior under test).
- **P3** test harness conventions (dot-source guard, mocks).
- **Parallel to P2** shared test suite — keep permutation coverage and the canonical question list aligned across both installers.
