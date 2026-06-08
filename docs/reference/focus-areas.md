# Focus Areas Review Tracking

This document tracks the systematic review of **Arbitron org-readiness** (edge-trusted SSO, pluggable agent backends + universal provider routing, scheduled/recurring triggers) for correctness, fail-closed auth, credential secrecy, backend/provider routing, and scheduling integrity.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Edge JWT verifier (`adapter/edge/verifier.go` — go-oidc discovery, signature/iss/aud-set/exp/nbf, email extraction, JWKS cache, fail-fast) | [x] | [x] | Done |
| 2 | Edge auth config + validation (`domain/config/config.go`, `adapter/config/loader.go` — `auth.edge.{teamdomain,audiences}`, `validateAuth`, env overlay, YAML example) | [x] | [x] | Done |
| 3 | Email provisioning (`domain/instance/provisioner.go` `ResolveByEmail`, `UserRepository.GetByEmail`, `adapter/sqlite/user_repo.go` + query — create-on-first-sight, bootstrap admin vs default group, case-insensitivity) | [x] | [x] | Done |
| 4 | Edge auth middleware + wiring (`adapter/http/auth_middleware.go` edge branch, `NewEdgeAuthMiddleware`, `cmd/arbitron/main.go` — stateless, fail-closed 401 vs 500, no login routes) | [x] | [x] | Done |
| 5 | Backend axis (`domain/workflow/workflow.go` `NodeConfig.Backend` + `validation.go`; `domain/provider/provider.go` Profile/ResolvedBinding; `adapter/provider/static.go`; `domain/engine/executor_agent.go` precedence) | [x] | [x] | Done |
| 6 | Factory backend dispatch (`adapter/provider/factory.go` `AgentProviderFor`, `Deps.DefaultAgentBackend`, `ErrUnknownBackend`) | [x] | [ ] | |
| 7 | claude-cli endpoint/key injection (`adapter/claudecli/env.go`, `agent_adapter.go`, `NewAgentAdapterForBinding`; `adapter/procenv/procenv.go` allowlist) | [ ] | [ ] | |
| 8 | opencode LiteLLM mapping + CLI install (`adapter/opencode/provider_id.go`, `env.go` guard; `Dockerfile`, `install.sh`, `install.ps1` install both CLIs) | [ ] | [ ] | |
| 9 | Schedule domain (`domain/schedule/` schedule.go/validation.go/ports.go — entity, cron `NextFire`, timezone normalize, validation) | [ ] | [ ] | |
| 10 | Schedule persistence (`adapter/sqlite/schedule_repo.go`, `migrations/013_schedules.*`, `queries/schedules.sql`, instance `HasActiveRun`, notifications schema change) | [ ] | [ ] | |
| 11 | Schedule evaluator (`domain/schedule/evaluator.go`, `alerter.go` — injected clock, fire-as-creator, collision skip, no-backfill recovery, main.go lifecycle) | [ ] | [ ] | |
| 12 | Schedule management surface (`domain/schedule/service.go` authz, `adapter/http/schedule_handler.go`, `adapter/mcp/tools_schedule.go` — CRUD/enable-disable, own-vs-admin, can-trigger gate) | [ ] | [ ] | |
| 13 | Auth mode coherence (`domain/identity` + `adapter/http/auth_middleware.go` — local/org/edge selection; org & local remain unchanged and green) | [ ] | [ ] | |
| 14 | Vertical slice: edge request → identity (header → verify → provision → enriched identity → authorized request) | [ ] | [ ] | |
| 15 | Vertical slice: agent node → backend → provider → LiteLLM (node/profile backend → resolver → factory → adapter → endpoint+key) | [ ] | [ ] | |
| 16 | Vertical slice: schedule due → fire-as-creator → stamped instance → queue (cron tick → evaluator → TriggerWithDepartment → enqueue) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 17 | Edge verifier + Email provisioning (verified email flows correctly into create/admin/default-group; case + normalization) | [ ] | |
| 18 | Edge config + Middleware/wiring (config drives verifier+middleware construction; fail-fast on missing teamdomain/audiences; nil session/token repos) | [ ] | |
| 19 | Backend axis + Factory dispatch (Backend field set in executor reaches `AgentProviderFor`; precedence node>profile>default end-to-end; unknown fails closed) | [ ] | |
| 20 | claude-cli injection + opencode mapping (both backends reach LiteLLM; credential secrecy parity; base-url allowlist vs reject-guard interaction) | [ ] | |
| 21 | Schedule domain + Persistence (entity ↔ repo round-trip; cron/timezone/LastFiredAt persisted; hand-written repo matches `schedules.sql`) | [ ] | |
| 22 | Schedule evaluator + Trigger-stamp (fire-as-creator reuses `domain/engine` `resolveStamp`; owner+department correct; department override) | [ ] | |
| 23 | Schedule evaluator + Persistence/recovery (`LastFiredAt`-driven no-backfill is deterministic across restart; skip/miss alerts persisted) | [ ] | |
| 24 | Schedule management + Authz (own-vs-admin + can-trigger gate consistent across REST and MCP surfaces) | [ ] | |
| 25 | Edge identity + Schedule ownership (a scheduled run's creator attribution ties to an edge-provisioned user; department stamping coherent) | [ ] | |
| 26 | Credential secrecy + Config/logging (provider keys + master key never in logs/argv/allowlist/`config.yaml`; non-secret base-urls only where intended) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All behaviors have test coverage across the five scenario categories (happy / success / failure / error / edge).
- The code does what the design doc says it should (in spirit).
- Error handling is thorough — no silent failures; fail-closed where the constraint demands it.
- Module boundaries are clean (domain ports vs adapters; no leaking concrete types across seams).

**Pass 2 (Robustness & Extensibility):**
- Aligns to the spirit of the spec/design, not just surface conformance.
- Time/network/subprocess dependencies are injected/mocked; tests are deterministic (injected clock; httptest JWKS; mock runners).
- Assertions test real risks (auth bypass, key leakage, mis-routing, backfill storms, DST), not happy-path ceremony.
- Edge cases handled (empty/missing claims, unknown backend, invalid cron/tz, restart, collision, DB error).

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (claims → identity; node → binding → adapter; schedule → trigger → instance).
- Error propagation works end-to-end (verify failure → 401; provision DB error → 500; unknown backend → fail closed).
- No assumptions in one component the other doesn't satisfy (e.g. the evaluator's creator context is what trigger-stamp expects).
- Integration tests cover the seams between the pair, not just unit-level happy paths.
