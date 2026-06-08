# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of **Arbitron org-readiness** (edge auth, pluggable agent backends, scheduled triggers). Add new findings to `## Open Issues`; move resolved ones to `## Fixed Previously`; log genuinely out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

These carry-forward items were flagged during the draft implementation and should be reviewed/closed by the loop:

1. **sqlc workaround for case-insensitive email lookup.** `GetUserByEmail` uses `WHERE lower(email) = ?` with the argument lowercased in Go (sqlc v1.31.1 SQLite parser corrupts the query set on `lower(?)` / trailing `COLLATE NOCASE LIMIT 1`). Verify correctness (incl. unicode-fold edge cases vs intent) and that `generated/users.sql.go` is purely additive. Area: #3.
2. **Multi-audience edge verification uses `SkipClientIDCheck: true`.** Audience membership is checked manually in `Verify`. Confirm it's airtight: missing/empty `aud` rejected; only configured audiences accepted; signature/iss/exp/nbf still enforced by go-oidc. Area: #1.
3. **opencode package + version unpinned.** `opencode-ai` is installed via npm in the Dockerfile and both installers with the version intentionally editable in one place each (ties to `TODO(opencode-binary)` in `adapter/opencode/args.go`). Pin once confirmed, consistently across all three; verify the binary name/CLI contract matches `adapter/opencode/args.go`. Area: #8.
4. **procenv allowlist vs opencode base-url guard.** Adding `ANTHROPIC_BASE_URL` to the procenv allowlist required scoping opencode's `rejectBaseNames` collision guard to the key var only. Verify no provider's base-url is wrongly rejected and no secret can leak via the allowlist. Areas: #7, #8, #20.
5. **Migration 013 rebuilds the notifications table.** Makes `notifications.instance_id` nullable, widens the type CHECK for `schedule_skipped`/`schedule_missed`, adds denormalized `workflow_name`. Verify existing notification flows (needs_interaction/failed/completed) are unaffected, the dashboard/repo handle a null instance id, and the down migration faithfully restores the prior shape. Area: #10.
6. **`schedule_repo.go` is hand-written (sqlc unavailable in the build env).** It mirrors `notification_repo.go`; `queries/schedules.sql` exists for a future `sqlc generate`. Verify the hand-written repo and the `.sql` stay in sync and the row mapping (JSON variables, nullable `last_fired_at`, TEXT-uuid FKs) is correct. Area: #10.
7. **Live/integration paths unverified.** No automated test exercises: (a) claude-code or opencode against a real LiteLLM endpoint (Anthropic `/v1/messages` + OpenAI `/v1/chat/completions`) → Azure AI Foundry; (b) `edge` mode against a real Cloudflare Access deployment (real JWKS + AUD); (c) the evaluator under real wall-clock and a real container restart. Add a smoke test or CI/VM run, or document the manual verification procedure. Areas: #14, #15, #16.

---

## Fixed Previously

(none yet)

---

## Won't Fix (Beyond Current Scope)

These are explicitly deferred by the spec (`docs/specs/org-readiness-spec.md` non-goals). Do NOT implement them in this loop; listed so they aren't re-raised as gaps:

- Mapping SSO group/department claims to RBAC (everyone is in the default group until a claim is available).
- A scheduling dashboard UI (Phase 3 ships REST + MCP management only).
- Multi-instance / distributed scheduling and the datastore changes it implies (single in-process evaluator + embedded SQLite).
- Schedule "run now" / manual fire convenience.
- Any new provider adapter beyond the existing families (LiteLLM is reached via the `openai`/`litellm` ids).
