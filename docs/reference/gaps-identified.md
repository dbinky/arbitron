# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of **Arbitron org-readiness** (edge auth, pluggable agent backends, scheduled triggers). Add new findings to `## Open Issues`; move resolved ones to `## Fixed Previously`; log genuinely out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

*(none — all carry-forward items resolved or deferred below)*

---

## Fixed Previously

- **sqlc workaround for case-insensitive email lookup — verified correct.** `WHERE lower(email) = ?` with Go-side `strings.ToLower` is correct for ASCII email addresses (the intended scope for CF Access JWTs); SQLite's `lower()` and Go's `strings.ToLower` agree on all ASCII input. `generated/users.sql.go` is purely additive. Area: #3 Pass 2.

- **`ResolveByEmail` did not trim the email before use.** The guard checked `strings.TrimSpace(email) == ""` but didn't assign the trimmed value, so a whitespace-padded email would be stored with padding in `Email`/`Username` and a subsequent trimmed-form call would create a duplicate user. Fixed by assigning `email = strings.TrimSpace(email)` before the empty check. Area: #3 Pass 2.

- **`resolve()` in edge mode mapped empty email from verifier to 500 instead of 401.** If an `EdgeVerifier` returned `("", nil)` (contract violation), `ResolveByEmail("")` returned `ErrInvalidClaims`, which wrapped as a non-`errNoCredential` error → 500. Fixed by adding a post-`Verify` guard: `if email == ""` → `errNoCredential` → 401. Area: #4 Pass 2.

- **`reject()` redirected browser navigations to `/auth/login` in edge mode.** In edge mode, `/auth/login` is not mounted (`authHandler` is nil), so the redirect created an infinite redirect loop for any browser GET to a non-API path without a CF token. Fixed: `reject()` now skips the redirect when `m.mode == identity.ModeEdge`, returning 401 directly. Area: #4 Pass 1.

- **Both `agentEnv` (claudecli) and `opencodeEnv` produced duplicate base-url entries when operator env set + binding overrides.** `procenv.BaseEnv()` passes through an operator-set allowlisted var (e.g. `ANTHROPIC_BASE_URL`); when the binding also provides a `BaseURL`, the old code appended a second entry — which one wins is implementation-defined. Fixed by moving `RemoveVar` to `procenv` (shared utility) and calling `procenv.RemoveVar(env, baseURLVar)` before the append in both adapters. Test: `TestAgentEnv_BindingBaseURLOverridesOperatorEnv` (claudecli), `TestOpencodeEnv_AnthropicBindingBaseURLDeduplicatesOperatorEnv` (opencode). Areas: #7 Pass 1, #8 Pass 1.

- **Multi-audience edge verification uses `SkipClientIDCheck: true` — confirmed airtight.** Tests added for: missing `aud` claim in token (go-oidc's SkipClientIDCheck does not bypass `audienceMatches`); empty-string `aud` in token; whitespace-only email claim. Root fix: `adapter/edge.New` now returns an error when the configured audience set is empty after whitespace-stripping, surfacing a misconfiguration at boot rather than silently producing a Verifier that always rejects. All rejection paths confirmed: signature/iss/exp/nbf enforced by go-oidc; manual aud-membership check is the only thing `SkipClientIDCheck` bypasses, and it is fail-closed. Area: #1.

- **Migration 013 rebuilds the notifications table — verified, admin-only schedule alert visibility documented.** Existing flows (needs_interaction/failed/completed) unaffected. Schedule alerts persist with NULL instance_id and denormalized workflow_name. Admin List (nil ownerID) includes schedule alerts; user-scoped List/CountUnread/MarkAllRead/Dismiss exclude them — this is by design (the notifications table has no creator_id for schedule alerts; "operators" per spec). Down migration faithfully restores prior shape (drops schedule-type rows, restores NOT NULL instance_id and 3-value CHECK). Tests in `notification_repo_test.go`. Area: #10.

- **`schedule_repo.go` vs `queries/schedules.sql` — verified in sync.** All six queries match (INSERT/SELECT/UPDATE/DELETE column lists and parameter binding). Row mapping (JSON variables, nullable last_fired_at, TEXT-uuid FKs) correct. Area: #10.

- **procenv allowlist vs opencode base-url guard — verified correct.** `rejectBaseNames` in `adapter/opencode/env.go` is correctly scoped to key vars only (not base-url vars). The key var (e.g. `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) must never be on the base allowlist — this is asserted by `TestOpencodeEnv_AnthropicBaseURLNotGuardedKeyIs` and `TestOpencodeEnv_BaseNameCollisionRejected`. The base-url guard uses `RemoveVar` before appending a per-binding override, which correctly strips any operator-inherited allowlisted value before the binding value is set. No secret leaks via the allowlist path. Area: #20.

- **opencode package name and minimum version confirmed.** Package is `npm install -g opencode-ai`; minimum compatible version is `0.4.0` (modern SST `run -m` contract, `--allow-all-tools` / `--allowed-tools` flags). This is the runtime gate in `adapter/opencode/version.go minOpencodeVersion` and now also the installer gate in `install.ps1 MinVersion`. Removed `TODO(opencode-binary)` placeholders from `args.go` and `version.go`. All three install surfaces (Dockerfile, install.sh, install.ps1) use `opencode-ai`; to pin an exact release append `@<version>` (e.g. `opencode-ai@0.4.0`). Area: #8.

---

## Won't Fix (Beyond Current Scope)

These are explicitly deferred by the spec (`docs/specs/org-readiness-spec.md` non-goals). Do NOT implement them in this loop; listed so they aren't re-raised as gaps:

- **Deferred scope (future org work).** SSO group/department→RBAC mapping, a scheduling dashboard UI, multi-instance/distributed scheduling, schedule "run now", and additional provider adapters. Documented for follow-up in [`docs/specs/final-org-gaps.md`](../specs/final-org-gaps.md).
- **Live/integration paths against real cloud endpoints.** Three paths can only be verified against the real work environment (LiteLLM→AIF model routing, `edge` mode against a real Cloudflare Access tenant, scheduler no-backfill across a real container restart). Moved to a dedicated deployment-verification doc: see [`docs/setup/org-setup-gaps.md`](../setup/org-setup-gaps.md). Areas: #14, #15, #16.
