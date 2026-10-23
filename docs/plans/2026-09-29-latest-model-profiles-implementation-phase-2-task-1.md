# Latest Model Profiles — Phase 2 Task 1: Profile selection fields, migrations and shared advancement contracts Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Profile selection fields, migrations and shared advancement contracts.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-2.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** None.

Execute this producer before the identity and instance-pin consumers despite phase numbering.

**Files:** Modify `domain/provider/provider.go`, `domain/provider/profile_patch.go`, `domain/provider/profile_store.go`, `adapter/sqlite/profile_repo.go`, `adapter/sqlite/queries/profiles.sql`, generated types, `domain/config/config.go`, `adapter/config/profiles.go`. Create `domain/provider/advance.go`, `adapter/sqlite/migrations/021_following_profiles.{up,down}.sql`, `adapter/sqlite/profile_following_test.go`, domain/config tests.

## Interfaces (owned here unless explicitly consumed)

Produce `SelectionMode string` values `pinned`, `following`; `FollowTarget{Provider string; Tier string}`; `Profile.SelectionMode SelectionMode`, `FollowTarget *FollowTarget`, `Revision int64`. Sparse `ProfilePatch.SelectionMode *string`, `FollowTarget **FollowTarget` distinguish omission and explicit null clear. `AdvanceRequest{ID string; RefreshID string; Expected Profile; Model string; CanonicalID string; PathRevision string; At time.Time}`; `Advancement{ID,RefreshID,Alias,FromModel,ToModel,CanonicalID string; Revision int64; At time.Time}`; `AdvanceStore.CommitAdvance(context.Context,AdvanceRequest) (Advancement,error)` returns `ErrAdvanceConflict` on expected-field mismatch. `EligibleCandidate{Model,CanonicalID,PathRevision string}` (this task extends both with the existing ExecutionRevision token for P3T3 to populate), `EligibleCandidateSource` has `Newest(context.Context,Profile) (EligibleCandidate,error)` and `ValidateStart(context.Context,Profile) (EligibleCandidate,error)`, `VersionRequests(context.Context,Profile) ([]VersionRequest,error)`, and `RevalidateStart(context.Context,Profile,EligibleCandidate) error`.

## Data producers and composition-root wiring

Admin/YAML inputs feed mode/target, SQLite supplies revision starting at 1; omitted old configurations/rows default pinned. Migration 021 also creates `profile_advancement_journal` with unique request ID to support atomic CAS from its first use (P2T3). No caller may supply stored revision. P2T2 validates; P2T3 writes CAS; P3T6 supplies eligible candidates.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

- Happy: `TestFollowingProfileRoundTrip` persists target/model/revision through reopen and cache reload.
- Success: old rows/configs default pinned; config parses both selection forms.
- Failure: unknown mode/malformed target patch fails parsing; no invalid persisted shape.
- Error: migration/query faults retain prior schema/data; revision update does not report success on missing row.
- Edge: omitted versus null target; nested target/slice copies; ABA edits increment revision even if model string returns to original.
- Input guard: tests feed a non-empty target through actual config conversion and SQLite read, so a dropped field fails; sqlc round-trip test covers regenerated and handwritten paths.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, provider.SelectionMode("following"), reloaded.SelectionMode)
assert.Equal(t, target, *reloaded.FollowTarget)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

Keep existing `ProfileStore` CRUD signatures; adapter updates increment revision atomically, including all admin edits and ABA changes. Preserve model spelling; normalize provider/tier slugs only. Add explicit YAML selection/target fields and sample comments in task P2T2. A profile model remains one spelling usable by all its paths. Do not extend `ResolvedBinding` or conflate follow tier with axes/backend/connection provider.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./domain/provider ./domain/config ./adapter/config ./adapter/sqlite -run "Following|Profile" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Execution evidence

- Red: the specified focused gate failed on absent selection APIs. After adding the API shape, reran to observe behavioral failures for patch parsing/ownership, YAML mapping, cache copies, root persistence, revision assignment and migrated legacy reads before implementing those behaviors.
- Green: every task-owned test passes. `go test ./domain/provider ./domain/config ./adapter/config ./adapter/sqlite -run "Following|Profile" -skip TestSampleFeaturePipeline_ValidatesWithDefaultProfiles -count=1` exits 0; `go test ./adapter/provider ./cmd/arbitron -run "Following|Profile" -count=1` exits 0. `go test -race ./domain/provider ./adapter/provider ./adapter/sqlite -run Following -count=1` exits 0.
- Broad-gate concern: the exact specified gate exits 1 solely for `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (committed sample aliases missing from default registry). Reproduced the same failure in a clean `git archive HEAD` checkout. This unrelated existing red is tolerated by the mission; no sample/default-registry changes are included.
- Regenerated profile queries with sqlc v1.31.1 using an isolated profile-only generation config; copied affected generated schema types without overwriting unrelated hand-curated generated queries. Temporary generation files removed.
- Self-review, in order: (1) selection/shape/revision/ports only; no candidate runtime, CAS, new flags, model identity claims or ResolvedBinding changes; (2) real `initDomain` YAML -> seed -> SQLite -> restart assertions and real SQLite -> cache target/revision assertions, plus generated-query reads and raw SQL migration/constraint checks; (3) neighboring patterns, owned nested fields, atomic increment/rows-affected handling, reversible transactional migration and gofmt/diff checks. P2T2/P2T3/P3T6/P3T7 own the later contract consumers as planned.

## Provenance / release gate (resolved 2026-10-01)

None for this completed field task. The earlier skipped pin tasks remain unimplemented; D1/D2 now settle their policy prerequisites, and P3T1/P1T3/P1T4 own their production implementation.

## Selection proof contract extension (alignment review 2026-10-16)

- [x] Implement and test the shared proof-returning candidate port and PreparedSelection extension below; update existing checking fakes and compile-time contracts.

The shared port/type extension is implemented. P2T2 still owns preparation/proof retention and revalidation orchestration; P2T3 owns validated SQL writes; P3T6/P3T7 own the real candidate producer and root injection. This contract-only completion does not certify those downstream execution sinks.

Extend the existing EligibleCandidate with `ExecutionRevision ExecutionRevision` (the type already exists in domain/provider/execution_gate.go). Both Newest and ValidateStart return the exact selected/supplied Model, canonical identity and non-empty prospective PathRevision/token; ValidateStart never substitutes a newer model. VersionRequests returns non-empty requests built from actual prospective routes, including ConnectionStore.Get's routing provider and effective backend. It performs no fetch/probe. RevalidateStart performs read-only inventory/config/policy/evidence checks against the exact prospective Profile and returned candidate; it does no network work, takes no gate/coordinator lock and never mutates a profile.

Produce `PreparedSelection{Profile Profile; Candidate *EligibleCandidate}` in domain/provider/selection.go, with deep copies. Candidate is required for a following selection needing validation; pinned version validation has nil Candidate. P2T1 owns the shared type, P2T2 produces populated values, P3T6 implements the candidate port, P2T3 persists it after proof revalidation, and P3T7 wires the same source into service and seed. Update existing domain fakes and compile-time interface checks for these intentional port extensions; an error-only ValidateStart cannot prove a later write safe.

### Current advancement evidence contract (2026-10-19)

The existing `domain/provider/advance.go` also defines `PathEvidenceRef{PathKey,ConfigRevision string}`, `AdvanceRequest.Evidence []PathEvidenceRef`, and `AdvanceStore.ReplayAdvance(context.Context,AdvanceRequest) (Advancement,bool,error)`. Include them in the shared contract above. P3T6 produces one reference for every consuming path in the captured current inventory; P2T3/P3T5 verify matching confirmed model/path/config evidence inside the transaction. A digest alone is insufficient. P2T3 preserves original-result replay before new-write discovery validation. These are existing APIs, not new competing interfaces; P2T3/P3T5 own their persistence tests and P3T7 the executed-model sink.

### Extension execution evidence

- Red: `go test ./domain/provider -run 'EligibleCandidate|PreparedSelection' -count=1` first fails compilation for the absent APIs. With API stubs, it fails behaviorally on shared candidate maps, shared profile tools and the shared candidate pointer. Deep-copy implementation makes all three tests green.
- Contract checks require proof-returning ValidateStart, VersionRequestSource compatibility and RevalidateStart; the old error-only source cannot compile. Existing service fakes now return owned candidate proof, reject a missing starting model and error on unexpected route/revalidation calls rather than pretending success. Existing ValidateSelection is signature-adapted only; retaining and revalidating its result is P2T2 work.
- Ordered review: (1) scope is shared contracts/copy ownership and necessary caller/fake adaptations only, with no candidate runtime, new configuration or routing guesses; (2) non-empty nested profile/proof fixtures assert exact copies and mutation isolation, including nil pinned candidates—these are type-contract tests, not claims of final runtime integration; existing real-root profile tests remain green; (3) reuse Profile.Clone and ExecutionRevision.Clone, retain nil maps/pointers, copy recorded fake inputs and format Go.
- Verification: all packages compile with `go test ./... -run '^$'`. The exact focused gate still fails only on the previously recorded `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` missing sample aliases. The same gate excluding that unrelated test and including `EligibleCandidate|PreparedSelection` passes; dependent `adapter/provider` and `cmd/arbitron` Following/Profile tests pass. Post-review `go test ./domain/provider -count=1` and `go test -race ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'EligibleCandidate|PreparedSelection|Following' -count=1` both pass, as do the filtered focused and dependent-root gates rerun after review.
