# Latest Model Profiles — Phase 1 Task 1: Persisted system settings Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Persisted system settings.

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

**Depends on:** None; independent of run pins.

**Files:** Create `domain/settings/settings.go`, `domain/settings/service.go`, `domain/settings/service_test.go`, `adapter/sqlite/settings_repo.go`, `adapter/sqlite/settings_repo_test.go`, and `adapter/sqlite/migrations/019_system_settings.{up,down}.sql`.

## Interfaces (owned here unless explicitly consumed)

Produce in `domain/settings`: `Values{CatalogURL string; TeamsWebhookURL string}`, `Patch{CatalogURL *string; TeamsWebhookURL *string}`, `PublicValues{CatalogURL string; TeamsWebhookURL string; TeamsConfigured bool}`. `SystemSettings` has `Read(context.Context) (Values,error)` and `Update(context.Context,Patch) (Values,error)`; updates merge atomically. `Service.Get(context.Context) (PublicValues,error)`, `Update(context.Context,Patch) (PublicValues,error)` require admin; `ReadInternal(context.Context) (Values,error)` is a separate trusted internal port. `NewService(SystemSettings) *Service`; `NewSettingsRepo(*sql.DB) *SettingsRepo`.

## Data producers and composition-root wiring

Persist submitted MCP/HTTP values from P1T2. Seed only absent keys to catalog URL `https://models.dev/api.json` and empty announcement webhook in migration 019; restart reads SQLite, never YAML notification settings. P1T2 constructs this repository/service at `initDomain`.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

- Happy: `TestSettingsPersistRestart` writes both settings, reopens migrated SQLite and reads the exact internal destination.
- Success: `TestSettingsPublicRedacts` returns configured=true and a redacted destination while `ReadInternal` returns the submitted value; defaults contain no webhook.
- Failure: `TestSettingsRejectsNonAdminAndInvalidURLs` asserts no writes for absent/non-admin identity, invalid URLs, or a redacted placeholder.
- Error: `TestSettingsReadAndWriteFaults` propagates DB failures; atomic patch failure retains both values.
- Edge: `TestSettingsPatchOmissionAndClear` leaves webhook unchanged when updating only URL, explicitly clears it with an empty pointer, and checks concurrent independent patches do not lose either value.
- Input guard: nil store is rejected at construction; tests read a non-empty submitted webhook through the real repository so a fake/default empty read cannot pass.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, submittedWebhook, internal.TeamsWebhookURL)
assert.NotEqual(t, submittedWebhook, public.TeamsWebhookURL)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

Reuse `domain/config/teams.go:ValidateTeamsWebhookURL` and `RedactWebhookURL`; catalog URL must be absolute HTTP(S), without credentials. Reject the redacted placeholder as input. Do not coerce a repository read error to an empty setting. Defaults belong to this service/schema, not to callers. Keep existing `config.SettingsStore` for run notifications separate.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

Verification: focused gate, race-enabled focused gate, full affected-package tests, and `go test ./cmd/arbitron -count=1` passed. Repository-wide `sqlc generate` hits an existing parser error in `queries/instances.sql:86`; settings queries were regenerated with sqlc v1.31.1 using a scoped query input and the existing migrations schema, without modifying unrelated generated code. Production service/repository construction and submitted-value boundary producers are owned by P1T2, as specified above.

**Focused gate:** `go test ./domain/settings ./adapter/sqlite -run Settings -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Provenance / release gate (resolved 2026-10-01)

None.

## Internal reader contract for planned consumers

P3T6 adds `settings.InternalReader` with `ReadInternal(context.Context) (Values,error)`; the existing Service implements it. P3T6 DiscoveryPipeline and P4T2 AnnouncementService consume this exact domain port. P3T7/P4T3 inject the persisted Service, not a public DTO reader or run-notification store. This interface addition belongs to P3T6 and must not be represented as already verified by the earlier execution evidence.
