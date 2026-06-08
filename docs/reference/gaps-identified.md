# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of **Arbitron org-readiness** (edge auth, pluggable agent backends, scheduled triggers). Add new findings to `## Open Issues`; move resolved ones to `## Fixed Previously`; log genuinely out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

These carry-forward items were flagged during the draft implementation and should be reviewed/closed by the loop:

1. **opencode package + version unpinned.** `opencode-ai` is installed via npm in the Dockerfile and both installers with the version intentionally editable in one place each (ties to `TODO(opencode-binary)` in `adapter/opencode/args.go`). Pin once confirmed, consistently across all three; verify the binary name/CLI contract matches `adapter/opencode/args.go`. Area: #8.
3. **procenv allowlist vs opencode base-url guard.** Adding `ANTHROPIC_BASE_URL` to the procenv allowlist required scoping opencode's `rejectBaseNames` collision guard to the key var only. Verify no provider's base-url is wrongly rejected and no secret can leak via the allowlist. Area: #20.
4. **Migration 013 rebuilds the notifications table — VERIFIED, admin-only schedule alert visibility documented.** Existing flows (needs_interaction/failed/completed) unaffected. Schedule alerts persist with NULL instance_id and denormalized workflow_name. Admin List (nil ownerID) includes schedule alerts; user-scoped List/CountUnread/MarkAllRead/Dismiss exclude them — this is by design (the notifications table has no creator_id for schedule alerts; "operators" per spec). Down migration faithfully restores prior shape (drops schedule-type rows, restores NOT NULL instance_id and 3-value CHECK). Tests in `notification_repo_test.go`. Area: #10.
5. **`schedule_repo.go` vs `queries/schedules.sql` — VERIFIED IN SYNC.** All six queries match (INSERT/SELECT/UPDATE/DELETE column lists and parameter binding). Row mapping (JSON variables, nullable last_fired_at, TEXT-uuid FKs) correct. Area: #10.
6. **Live/integration paths unverified.** No automated test exercises: (a) claude-code or opencode against a real LiteLLM endpoint (Anthropic `/v1/messages` + OpenAI `/v1/chat/completions`) → Azure AI Foundry; (b) `edge` mode against a real Cloudflare Access deployment (real JWKS + AUD); (c) the evaluator under real wall-clock and a real container restart. Add a smoke test or CI/VM run, or document the manual verification procedure. Areas: #14, #15, #16.

---

## Fixed Previously

- **sqlc workaround for case-insensitive email lookup — verified correct.** `WHERE lower(email) = ?` with Go-side `strings.ToLower` is correct for ASCII email addresses (the intended scope for CF Access JWTs); SQLite's `lower()` and Go's `strings.ToLower` agree on all ASCII input. `generated/users.sql.go` is purely additive. Area: #3 Pass 2.

- **`ResolveByEmail` did not trim the email before use.** The guard checked `strings.TrimSpace(email) == ""` but didn't assign the trimmed value, so a whitespace-padded email would be stored with padding in `Email`/`Username` and a subsequent trimmed-form call would create a duplicate user. Fixed by assigning `email = strings.TrimSpace(email)` before the empty check. Area: #3 Pass 2.

- **`resolve()` in edge mode mapped empty email from verifier to 500 instead of 401.** If an `EdgeVerifier` returned `("", nil)` (contract violation), `ResolveByEmail("")` returned `ErrInvalidClaims`, which wrapped as a non-`errNoCredential` error → 500. Fixed by adding a post-`Verify` guard: `if email == ""` → `errNoCredential` → 401. Area: #4 Pass 2.

- **`reject()` redirected browser navigations to `/auth/login` in edge mode.** In edge mode, `/auth/login` is not mounted (`authHandler` is nil), so the redirect created an infinite redirect loop for any browser GET to a non-API path without a CF token. Fixed: `reject()` now skips the redirect when `m.mode == identity.ModeEdge`, returning 401 directly. Area: #4 Pass 1.

- **Both `agentEnv` (claudecli) and `opencodeEnv` produced duplicate base-url entries when operator env set + binding overrides.** `procenv.BaseEnv()` passes through an operator-set allowlisted var (e.g. `ANTHROPIC_BASE_URL`); when the binding also provides a `BaseURL`, the old code appended a second entry — which one wins is implementation-defined. Fixed by moving `RemoveVar` to `procenv` (shared utility) and calling `procenv.RemoveVar(env, baseURLVar)` before the append in both adapters. Test: `TestAgentEnv_BindingBaseURLOverridesOperatorEnv` (claudecli), `TestOpencodeEnv_AnthropicBindingBaseURLDeduplicatesOperatorEnv` (opencode). Areas: #7 Pass 1, #8 Pass 1.


- **Multi-audience edge verification uses `SkipClientIDCheck: true` — confirmed airtight.** Tests added for: missing `aud` claim in token (go-oidc's SkipClientIDCheck does not bypass `audienceMatches`); empty-string `aud` in token; whitespace-only email claim. Root fix: `adapter/edge.New` now returns an error when the configured audience set is empty after whitespace-stripping, surfacing a misconfiguration at boot rather than silently producing a Verifier that always rejects. All rejection paths confirmed: signature/iss/exp/nbf enforced by go-oidc; manual aud-membership check is the only thing `SkipClientIDCheck` bypasses, and it is fail-closed. Area: #1.

---

## Won't Fix (Beyond Current Scope)

These are explicitly deferred by the spec (`docs/specs/org-readiness-spec.md` non-goals). Do NOT implement them in this loop; listed so they aren't re-raised as gaps:

- Mapping SSO group/department claims to RBAC (everyone is in the default group until a claim is available).
- A scheduling dashboard UI (Phase 3 ships REST + MCP management only).
- Multi-instance / distributed scheduling and the datastore changes it implies (single in-process evaluator + embedded SQLite).
- Schedule "run now" / manual fire convenience.
- Any new provider adapter beyond the existing families (LiteLLM is reached via the `openai`/`litellm` ids).
