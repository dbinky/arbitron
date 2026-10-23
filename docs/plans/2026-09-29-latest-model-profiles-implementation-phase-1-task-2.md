# Latest Model Profiles — Phase 1 Task 2: Settings boundaries, dashboard and root wiring Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Settings boundaries, dashboard and root wiring.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-1.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P1T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md`)

**Files:** Modify `adapter/mcp/tools_system.go`, `adapter/mcp/deps.go`, `adapter/http/server.go`, `cmd/arbitron/main.go`, `dashboard/src/api/client.ts`, `dashboard/src/routes/Settings.tsx` and its CSS/tests. Create `adapter/mcp/tools_system_settings_test.go`, `adapter/http/system_settings_test.go`, `cmd/arbitron/model_settings_wiring_test.go`.

## Interfaces (owned here unless explicitly consumed)

Consume P1T1 `settings.Service`. Add `SystemSettings *settings.Service` to MCP `Deps`, HTTP `RouterDeps` and `domainStack`. Register `system_settings_get`, `system_settings_update`, `GET /system/settings`, `PUT /system/settings`; camelCase HTTP/public fields `catalogUrl`, `teamsWebhookUrl`, `teamsConfigured`; MCP update arguments `catalog_url`, `teams_webhook_url`. Missing field means omitted, explicit empty webhook clears. Dashboard `api.system.getSettings()` / `updateSettings(patch)` share this HTTP shape.

## Data producers and composition-root wiring

Admin input is the producer: dashboard card -> authenticated HTTP route -> service -> SQLite; MCP uses the same service. `cmd/arbitron/main.go:initDomain` constructs `SettingsRepo` then `Service`, retains it in `domainStack`, and passes it through `mcpDeps()` and the actual HTTP `httpDeps()` projection used by runServe (one production router site in this checkout). Use existing local/admin identity middleware; public redaction is never the background sender's input.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

- Happy: `TestModelSettingsWiringRoundTrip` constructs the actual `initDomain` stack with temporary config/DB, updates via authenticated HTTP and reads via registered MCP plus internal service after restart.
- Success: `SettingsModelAnnouncements` dashboard test edits catalog URL and webhook and verifies outgoing patch and configured status.
- Failure: non-admin/missing identity rejected on each public surface, unknown keys and redaction rejected without DB mutation.
- Error: service fault reaches HTTP/MCP/dashboard error state; no secret in payload/logs.
- Edge: saving only catalog URL omits webhook, clearing sends explicit empty, refresh never fills replacement input with secret/redaction.
- Input guard: remove `SystemSettings` from either real deps projection and round-trip test must fail at the persisted sink; handlers cannot return success when service is nil.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.True(t, public.TeamsConfigured)
assert.Equal(t, submittedCatalogURL, reopened.CatalogURL)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

Show catalog URL, configured status and a blank replacement input for the webhook; omit untouched secret on save. Service authz remains authoritative. Surface validation/storage errors without exposing raw webhook. Do not supply a real webhook, merge into run-notification settings, or save displayed redaction.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./adapter/http ./adapter/mcp ./cmd/arbitron -run ModelSettings -count=1; cd dashboard && npx tsc --noEmit && npx vitest run src/routes/Settings.test.tsx src/api/client.test.ts && npm run build`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Execution evidence

- Red: Go initially failed on the missing dependency APIs, then failed behaviorally on absent HTTP routes/MCP tools and empty persisted values. Dashboard tests failed on the missing card/client APIs. Dependencies were installed with `npm ci` before observing dashboard red.
- Green: the focused Go gate, TypeScript, all 23 selected Vitest tests, dashboard build and `make ui` passed. Full HTTP, MCP, composition-root, settings-domain and SQLite package tests also passed. MCP registration expectations include the two new tools.
- Review order: spec compliance, anti-green-theater, then code quality. Removing either production settings projection made the real round-trip test fail; assertions cover SQLite writes/reopen, public redaction and the raw internal sink. Storage triggers carrying a secret verify safe boundary errors and HTTP logs.
- Root inspection found one current production HTTP router site (`runServe`), now using the tested `httpDeps()` projection; standalone MCP uses `mcpDeps()`. There is no second production HTTP site in this checkout.
- Minimal supporting domain changes provide camelCase public JSON and a validation sentinel for HTTP 400 versus storage 500. No schema/query changes required sqlc regeneration. Embedded dashboard assets were rebuilt.
- Nonblocking existing tooling notices: npm reports 8 dependency vulnerabilities; Vite reports a large bundle. Dependency upgrades/bundle restructuring are outside this task.

## Provenance / release gate (resolved 2026-10-01)

None.
