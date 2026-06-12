# Focus Areas Review Tracking

This document tracks the systematic review of the **profile-editing** feature (database-driven, admin-managed model providers & connections, with context-window + effort axes) for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Axis enums (`domain/provider/axes.go` — `ContextWindow`/`Effort` validity, parse, empty→default, effort ordering/`GreaterThan`) | [x] | [x] | Done |
| 2 | Capability validation (`domain/provider/capability.go` — `Validate`, model-override resolution, effort floor, inapplicable-axis rejection, unknown-backend) | [x] | [x] | Done |
| 3 | Capability loader (`adapter/config/capabilities.go` + `capabilities.default.yaml` — embedded default, per-backend override merge, malformed-enum fail-fast, nil-override inherit) | [x] | [x] | Done |
| 4 | Profile persistence (`adapter/sqlite/profile_repo.go`, `migrations/015_profiles.*`, `queries/profiles.sql` — CRUD, dup→`ErrProfileExists`, missing→`ErrProfileNotFound`, JSON allowed_tools + axes round-trip) | [x] | [x] | Done |
| 5 | Cache + resolver (`adapter/provider/cache.go`, `dynamic.go` — store-bound `Load`/`Reload`, axes on binding, lazy `RevealKey`, zero-I/O `Kind`, `-race`) | [x] | [x] | Done |
| 6 | Startup seeding (`cmd/arbitron/profile_seed.go` + `initDomain` wiring — empty-table seed, validate+skip invalid, non-empty no-op, DB-wins) | [x] | [x] | Done |
| 7 | Runtime mapping (`domain/provider/capability.go` `Emit`/`Emission` — claude env vars, codex `-c`, none-disables, defaults emit nothing, floor parity with `Validate`) | [x] | [x] | Done |
| 8 | claude-cli emission (`adapter/claudecli/env.go`, `shared.go`, chat/agent paths — `applyCapabilityEnv`, credential env preserved, effective-backend keying) | [x] | [x] | Done |
| 9 | codex emission (`adapter/codex/args.go`, `stream_adapter.go` — `buildExecArgs` extra args, `model_reasoning_effort`, batch+stream parity, model-required fail-closed) | [x] | [ ] | |
| 10 | ProfileService (`domain/provider/profile_service.go`, `authz.go` — admin gate on writes, read-open list/get, validate-before-persist, recache, effective echo) | [ ] | [ ] | |
| 11 | ConnectionService (`domain/provider/connection_service.go` — admin gate on all ops, `Reveal` not exposed) | [ ] | [ ] | |
| 12 | MCP profile tools (`adapter/mcp/tools_profile.go` — `profile_*` + `provider_capabilities`, arg parsing, `profileErrorResult` mapping, effective echo) | [ ] | [ ] | |
| 13 | Connection tools retrofit (`adapter/mcp/tools_connection.go` — admin-only routing via `ConnectionService`, `connErrorResult` authz cases, no api_key echoed) | [ ] | [ ] | |
| 14 | Composition + e2e (`cmd/arbitron/main.go` wiring, `adapter/mcp/deps.go`, `adapter/mcp/profile_authz_e2e_test.go` — service construction, deps injection, admin/non-admin coverage) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 15 | Validate + Emit (write-time validity and runtime emission share one table and agree on the effective tier/effort — `xhigh`→`high`, `none`→disable, defaults→nothing) | [ ] | |
| 16 | ProfileStore + Cache + Resolver (a stored profile's axes flow store → cache → `ResolvedBinding` intact; connection-backed path still reveals lazily) | [ ] | |
| 17 | ProfileService + Cache recache (a successful write reloads the cache so the next `Resolve` reflects it; reload failure leaves the durable write intact) | [ ] | |
| 18 | MCP profile tools + ProfileService (sentinel→tool-error mapping is faithful; admin gate enforced end-to-end; effective values echoed; capabilities serialized without secrets) | [ ] | |
| 19 | ConnectionService + connection tools (admin gate end-to-end on every connection op; no api_key in any response or log; `Reveal` unreachable from the tool surface) | [ ] | |
| 20 | Seeding + ProfileStore + config (`ProfilesFromConfig` ∪ defaults seed once; invalid entries skipped; `config.yaml` ignored thereafter; non-profile config untouched) | [ ] | |
| 21 | Emission + claude/codex adapters (effective-backend keying correct across both adapters; chat vs agent paths; opencode emits nothing) | [ ] | |
| 22 | Capability loader + ProfileService validation (the embedded/override ruleset actually drives write rejection + flooring; a narrowed override changes what writes are accepted) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy / success / failure / error / edge)
- The code does what the design doc says it should
- Error handling is thorough — no silent failures; sentinels are specific and matched with `errors.Is`
- Module boundaries are clean (no adapter import in `domain`; no secret leaking across a boundary)

**Pass 2 (Robustness & Security):**
- Code aligns to the spirit of the design, not just surface conformance
- Authz and secret-handling invariants hold on every path (not just the happy one)
- Test assertions test real risks (a non-admin refused, an inapplicable axis rejected with nothing stored, a key never echoed), not happy-path ceremony
- Edge cases handled (empty/unset axes, unknown alias/backend/model, concurrent cache read/reload, reload failure, duplicate seed)

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (axes survive store→cache→binding→emission)
- Authz and validation decisions made in the service are honored by the tool surface
- No assumptions in one component that the other doesn't satisfy (Validate's flooring == Emit's flooring)
- Integration tests cover the seams (recache visibility, admin-vs-non-admin e2e, seed-once idempotency), not just unit-level happy paths
