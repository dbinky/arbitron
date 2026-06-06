# Installers P4 — Task 2: Shared config Q&A + config.yaml + secrets (Windows server)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: write the Pester test first, watch it fail, then implement.

**Goal:** Drive the **shared** configuration Q&A (kept byte-for-byte parallel to the P2 Linux shared question set, in the same order — the single source of truth is the **"CANONICAL shared Q&A order"** block in `installers-implementation-phase-2-task-2.md`; mirror it exactly), render `%ProgramData%\Arbitron\config.yaml` (absolute paths, P0 `--config`-compatible), and write secrets (API/master key) via the service environment / **DPAPI machine scope**. The master key is auto-generated silently.

**Design refs:** `installers-design-phase-4.md` (§Config Q&A (shared — parallel to P2), §Secrets handling); `installers-design-phase-2.md` (§Config Q&A (shared), §Secrets handling). **Source of truth for the question order = the "CANONICAL shared Q&A order" block in the P2 plan `installers-implementation-phase-2-task-2.md`** (mirror it 1:1, including the P0 TLS invariants). Master design §Config Q&A (Shared), §Secrets handling.

---

## Context

**Delivers:**
- The shared Q&A flow, identical in wording/order to P2:
  1. Bind host / port.
  2. **Auth model**: edge-auth + local mode (installer enforces loopback bind; single shared admin) **or** OIDC org mode.
  3. If OIDC: issuer URL, client ID, client secret, redirect URL, bootstrap admins, optional RBAC (group claim, default group, department leads).
  4. **TLS model**: behind tunnel/proxy → `PublicBaseURL` + Secure cookies + printed guidance; **or** direct app-TLS → cert + key paths (P0 `server.tls`); **or** autocert domain.
  5. "Enable unattended agent tools?" — explicit `--dangerously-skip-permissions` RCE-warning prompt.
  6. Service account name (default `arbitron`) — already captured in Task 1; surfaced/confirmed here.
  7. Data location (default `%ProgramData%\Arbitron\data` shown).
- Rendering of `%ProgramData%\Arbitron\config.yaml` with absolute paths and the answers mapped to the real config keys (`server.host`, `server.port`, auth block, `server.tls` block, `PublicBaseURL`, skip-permissions, data/workspace/log paths).
- Respect for P0 fail-fast validations when composing the TLS block: autocert ⇒ `server.port == 443`; local-mode + TLS on a non-loopback host ⇒ explicit `server.tls.allowunauthenticated`. The installer should pre-validate and either set the right values or prompt, so the binary does not later fail-fast unexpectedly.
- Secrets: master key = 32 random bytes → base64, generated silently; API key (if pasted) + master key written via DPAPI machine scope and/or the Windows service environment, never logged.
- **Reconfigure** support: when invoked in reconfigure mode, pre-fill answers from the existing `config.yaml`.

**Depends on:** P4 **Task 1** (Shared skeleton: layout paths, run-as/account, elevation). P3 **Q&A engine** primitives (prompt/default/secret-input helpers). P0 `server.tls` schema + `--config`.

**Parallel to P2** (Linux shared) config-Q&A task — same questions, same order; only the secrets/path mechanics differ (DPAPI/ProgramData vs `arbitron.env`/`/etc`).

---

## Test plan (write first)

Pester suite `tests/install/Shared.Config.Tests.ps1`. Mock the Q&A input helper to feed scripted answers, mock DPAPI/`Set-ItemProperty` env writes, mock random-byte generation to a fixed value for deterministic assertions.

- [ ] **Question order matches P2:** record the sequence of prompts; assert it equals the P2 order (host/port → auth model → [OIDC sub-questions] → TLS model → skip-permissions → service account → data location). Pin the exact prompt strings so drift from P2 is caught.
- [ ] **Edge-auth + local mode:** answers select edge-auth; assert config sets local-mode auth, **loopback bind** (host forced to `127.0.0.1`), `PublicBaseURL` when a TLS-behind-proxy answer is given, and Secure cookies.
- [ ] **OIDC answers → expected config:** OIDC selected with issuer/client id/secret/redirect/bootstrap admins; assert the rendered config contains the OIDC block with those exact values and bootstrap admins list.
- [ ] **OIDC + RBAC:** optional RBAC answers (group claim, default group, department leads) appear in the config when provided; absent when skipped.
- [ ] **TLS: tunnel/proxy:** sets `PublicBaseURL`, Secure cookies, and prints guidance; **no** `server.tls.enabled`.
- [ ] **TLS: direct app-TLS:** cert + key paths produce `server.tls.enabled: true` with `certfile`/`keyfile`; no autocert domain.
- [ ] **TLS: autocert:** autocert domain produces `server.tls.autocertdomain`; assert installer sets/forces `server.port: 443` (P0 rule), or refuses with guidance if the operator set a conflicting port.
- [ ] **Local-mode + non-loopback TLS guard:** when edge/local-mode is combined with TLS on a non-loopback host, assert `server.tls.allowunauthenticated` is set (explicit escape hatch) — matching P0 validation.
- [ ] **Skip-permissions prompt:** the RCE warning is shown; "yes" sets the skip-permissions flag in config, "no" leaves it disabled. Assert the warning text is present.
- [ ] **config.yaml under %ProgramData%:** the file is written to `%ProgramData%\Arbitron\config.yaml` with **absolute** data/workspace/log paths under `%ProgramData%\Arbitron\`.
- [ ] **Master key auto-generated + never logged:** generation invoked once; key written to the secrets store (DPAPI machine scope / service env); assert it never appears in transcript/`Write-Host` output.
- [ ] **API key via DPAPI machine scope:** a pasted API key is stored via DPAPI machine scope (mock asserts the protect call with machine scope) and/or the service environment; not written into `config.yaml`.
- [ ] **Reconfigure pre-fill:** given an existing `config.yaml`, prompts default to its current values; preserved keys round-trip.
- [ ] **PSScriptAnalyzer:** clean.

---

## Implementation steps

- [ ] **Step 1:** Write the failing Pester cases in `tests/install/Shared.Config.Tests.ps1`; confirm red.
- [ ] **Step 2:** Implement `Invoke-SharedConfigQA` calling the P3 prompt helpers in the **P2 order**. Centralize prompt strings in one place so they can be asserted/compared to P2 (consider a shared constants block both installers conceptually mirror).
- [ ] **Step 3:** Auth-model branch. Edge-auth → force `host = 127.0.0.1`, set local-mode admin, Secure cookies. OIDC → collect issuer/client id/secret/redirect/bootstrap admins; then optional RBAC sub-questions. Route the OIDC **client secret** to the secrets store as **`ARBITRON_AUTH_OIDC_CLIENTSECRET`** (the loader binds this env name → `auth.oidc.clientsecret`; same as P2) — never into `config.yaml`.
- [ ] **Step 4:** TLS-model branch with three sub-paths (tunnel/proxy, direct cert+key, autocert). Apply P0 invariants: autocert ⇒ set `server.port = 443`; local-mode + non-loopback TLS ⇒ set `allowunauthenticated`. Print tunnel/proxy guidance text for the proxy path.
- [ ] **Step 5:** Skip-permissions prompt with the explicit RCE-equivalent warning; map answer to the config flag.
- [ ] **Step 6:** Compose the config object and render YAML via P3's config-writer helper **`Write-ArbitronConfig`** (P3 Task 3's canonical name — reuse it, parameterized for the ProgramData path + shared host/auth) to `%ProgramData%\Arbitron\config.yaml`, all paths absolute.
- [ ] **Step 7:** Secrets: reuse P3's **`New-MasterKey`** (32 random bytes → base64, RNG behind a seam — do NOT introduce a separate `Get-RandomBytes` helper; P3 already owns master-key generation) for the master key (silent). Store master key + any pasted API key via P3's **`Set-ArbitronSecret`** with the DPAPI **machine** scope variant (`ProtectedData.Protect` with `DataProtectionScope.LocalMachine`) — the shared analogue of P3's User-scope call — and/or set the Windows **service environment** variables consumed by the service (wired into the service definition in Task 3). Use the same secret env-var **names** as P3/P2: `ARBITRON_MASTER_KEY`, `ARBITRON_LLM_APIKEY` (and the OIDC client secret per Task 2's auth section). Never echo secret values.
- [ ] **Step 8:** Reconfigure mode: load existing `config.yaml`, feed values as prompt defaults; preserve untouched keys and existing secrets.
- [ ] **Step 9:** Pester green; `Invoke-ScriptAnalyzer` clean.

---

## Files

- **Modify:** `install.ps1` — `Invoke-SharedConfigQA`, TLS/auth composition, secrets (DPAPI machine scope / service env), shared config writer for the ProgramData path, reconfigure pre-fill.
- **Create:** `tests/install/Shared.Config.Tests.ps1`.

---

## Acceptance criteria

- [ ] Shared Q&A order and prompt wording match P2 exactly (asserted).
- [ ] Edge-auth forces loopback bind; OIDC renders the full OIDC (+ optional RBAC) block.
- [ ] All three TLS paths render correctly and honor P0 fail-fast invariants (autocert⇒443; local-mode+non-loopback TLS⇒allowunauthenticated).
- [ ] Skip-permissions prompt shows the RCE warning and maps the answer.
- [ ] `config.yaml` written under `%ProgramData%\Arbitron\` with absolute paths; secrets via DPAPI machine scope / service env; master key auto-generated and never logged.
- [ ] Reconfigure pre-fills from existing config and preserves data/secrets.
- [ ] Pester green; PSScriptAnalyzer clean.

---

## Dependencies

- **P4 Task 1** (skeleton: paths, run-as, elevation).
- **P3:** Q&A-engine prompt helpers, config writer.
- **P0:** `server.tls` schema, `--config`, fail-fast validation semantics.
- **Parallel to P2** shared config-Q&A task — same question set/order.
