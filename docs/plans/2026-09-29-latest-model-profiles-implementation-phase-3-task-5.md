# Latest Model Profiles — Phase 3 Task 5: Durable catalog, cycle journal and model events Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Durable catalog, cycle journal and model events.

**Architecture:** Domain ports own contracts; adapters produce real inputs; the shared `cmd/arbitron/main.go:initDomain` composition root connects them. Execution order follows the dependency graph, not numeric phase order.

**Tech Stack:** Go 1.25.5, SQLite/sqlc, MCP/HTTP; React/Vitest for dashboard boundaries.

**Spec:** `docs/specs/2026-09-29-latest-model-profiles-spec.md`; `docs/designs/2026-09-29-latest-model-profiles-design-phase-3.md`. Read the Phase 3 execution/coverage map and this task's upstream Interfaces before coding.

## Global constraints

- Refresh cadence is four hours; following provider/tier stays fixed and only eligible newer usable versions advance.
- New runs use published updates; post-migration active/retried/resumed runs retain specific starting versions. Pre-migration NULL-pin runs keep live resolution until completion (D1).
- Pinned profiles and last known working models survive discovery failure; no harness installation/update.
- Announcement webhook defaults empty; do not supply it during this work. Each newly usable canonical model gets at least one announcement when configured, including no-followers discoveries; rare duplicates after ambiguous sends are accepted (D7).
- No mandatory Claude/Sonnet normalizer. Unknown evidence is explicit, never fabricated success.

## Review focus

Required tests below cover missing production inputs, restart/concurrency, partial failure, authorization/secret handling and exact final sinks. A fake must reject missing load-bearing fields; unsupported external guarantees remain blockers.

## Dependencies and files

**Depends on:** P2T3 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-3.md`) and P3T1–P3T4 types; persistence can be tested using typed evidence without claiming external proof.

**Files:** Create `domain/modelcatalog/store.go`, `domain/modelcatalog/report.go`, `domain/modelcatalog/public.go`, `adapter/sqlite/model_catalog_repo.go`, tests, `adapter/sqlite/migrations/022_model_catalog.{up,down}.sql`. Modify profile advancement transaction helper only to reuse it, not duplicate CAS. On a fresh database migration 022 sorts before 023; on an existing database apply missing 022 independently even when 023/024/025 are already recorded. When applying 022 to a DB that already records 023, add the catalog/event schema without overwriting the existing model_state_meta flag; test this task-order migration path.

## Interfaces (owned here unless explicitly consumed)

Produce `RefreshReport{ID string; At time.Time; SliceStatus,PathStatus map[string]string; NewlyUsable []Observation; Changes []provider.Advancement; PublicationPending bool; AnnouncementError string}`. `Observation{Entry modelidentity.Entry; Paths []UsabilityEvidence; FirstConfirmedAt time.Time}`. `AnnouncementPayload{EventID,CanonicalID,Provider,Tier string; FirstConfirmedAt time.Time; Paths []UsabilityEvidence; Changes []provider.Advancement}`. `Event{Payload AnnouncementPayload; State string; ClaimID string; LeaseUntil time.Time}`; shared states `awaiting_setting`, `pending`, `in_flight`, `delivery_unknown`, `announced`, `suppressed`. `CycleCommit{Report RefreshReport; Catalog CatalogSnapshot; Evidence []UsabilityEvidence; Advances []provider.AdvanceRequest; WebhookConfigured *bool}`. `CatalogStore.CommitCycle(context.Context,CycleCommit) (RefreshReport,error)`, `Recover(context.Context) ([]RefreshReport,error)`, `Snapshot(context.Context) (CatalogView,error)`; `CatalogView{Catalog CatalogSnapshot; Evidence []UsabilityEvidence; Reports []RefreshReport}`.

## Data producers and composition-root wiring

P3T2 fetch/normalize, P3T4 evidence, P3T6 prepared advancements, clock and P1T1 raw webhook read feed CommitCycle. Nil WebhookConfigured means read fault (awaiting_setting), false empty (suppressed), true configured (pending). SQLite is the producer of historical first confirmation, immutable original event payload and journal for Phase 4. P3T7 constructs store on same DB as profile repo; root tests recover it after restart.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestCycleCommitsModelJournalAndEvent` asserts model CAS, original changes and first usability/event in one migrated SQLite transaction.
- Success: no-followers event has empty changes; multi-path confirmation yields one event; tier unknown still cataloged/usable.
- Failure: stale profile skips only its advance, duplicate discovery/reappearance never duplicates event.
- Error: injected event/report/journal write faults roll back CAS; crash/reopen recovers committed report/pending event, never synthesizes history from current profile.
- Edge: first configured/empty/read-error discovery classifications; retry awaiting_setting preserves observation/payload; repeated canonical identity across native paths; prior invalid slice retained.
- Input guard: empty successful evidence cannot create a first-usable event or advancement; frozen payload comes from actual SQL CAS returns, never planned proposals/fake ignored arguments.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Len(t, durableEvents, 1)
assert.Equal(t, committedChanges, durableEvents[0].Payload.Changes)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Within one transaction persist accepted slices/evidence and first-usable observations, invoke P2T3 commitAdvanceTx for each proposal, record exact actual changes (skip CAS conflicts with status), freeze new event payload after committed changes known, and commit report. Unique canonical identity enforces one event lifetime; request ID enforces one advancement. Discovery tier may be unknown and still announce; tier/order evidence is required only for following advancement. Failed cycle tx rolls back model/journal/events together. A journal change to previously observed model appears in report, but must not mutate its frozen announcement. Preserve historical observations on unknown paths and discovery errors; no first-refresh baseline flag.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

**Focused gate:** `go test ./adapter/sqlite ./domain/modelcatalog -run "Catalog|Cycle|AnnouncementEvent" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Provenance / release gate (resolved 2026-10-01)

None.

## Persistence feeds and redacted projections

Implement `CatalogStore.MarkPublished(context.Context,string) error` for a committed report ID, atomically acknowledging cache publication; Recover returns committed reports needing acknowledgement. CatalogView includes all current source entries plus retained historical identity/order metadata for previously selected/current models, persisted evidence indexed by canonical model/path/config revision, reports, and redacted unmatched-follow-target diagnostics populated by P3T6. Publication is an acknowledgement, never another CAS/journal write. P2T3/P3T6 consume this seam.

Add schema columns for CatalogModel release_date/canonical_model_id/SourceRef, Entry PolicyRevision, evidence ConfigRevision/CheckedAt/ProofRef; reserve the event extension seam for retry/part fields owned and implemented by P4T2, avoiding duplicate field/migration work; no TTL or opaque receiver receipt is required by D7. Persist confirmed evidence across DB reopen without time-based expiry. Public catalog DTOs are separately projected here/at P3T7: do not serialize internal ExecutionPath.Binding, credential closures, raw account paths, configured URLs, attempt destinations or raw proof files. Tests make missing persisted metadata alter equal-version ordering, missing evidence prevent an advance/event, and acknowledgement failure remain recoverable.

CycleCommit proposals carry the captured PathRevision; P3T3 gate rechecks it and P2T3 transaction code checks persistent DB/policy revisions before updates. WithCurrent covers commit, not network probes. Keep schema migration 022 independent of the already-applied 023/024 model_state_meta/connection revision schema; test both fresh install and a DB with 019/021/023/024 already recorded and 020/022 absent.

Produce `PublicCatalogEntry{CanonicalID,Provider,NativeID,Tier,SpecificModel,Usability string; Paths []PublicPath}` and `PublicPath{PathKey,ConfigRevision,State string; CheckedAt time.Time}`, plus `PublicCatalogView{Entries []PublicCatalogEntry; UnmatchedTargets []provider.FollowTarget; Reports []PublicRefreshReport}`. `PublicRefreshReport` includes ID/At/statuses/committed Changes/PublicationPending and only static classified diagnostic codes. `ProjectCatalog(CatalogView) PublicCatalogView` deep-copies safe fields and drops ProofRef, raw slices, bindings, filesystem/account locations and settings/credential data. P3T6 List returns this projection and P3T7 MCP serializes it; CatalogStore.Snapshot remains the trusted internal CatalogView. Add tests using secret-bearing internal proof/route fixtures and assert their values cannot reach registered catalog-tool responses.

## Retained model inputs and diagnostics producer

Persist CatalogSlice.Models, including CatalogProvider/NativeID/CanonicalModelID/ReleaseDate/SourceRef, in migration 022. Retain historical rows when a current offering disappears. P3T6 remaps current and candidate CatalogModels through one captured policy revision before comparing; do not compare a cached old-policy Entry against a newly classified candidate. Test a policy edit changing numeric capture groups while the current model is absent, plus a read/malformed-history failure that preserves the profile and reports unknown.

Add `UnmatchedTargets []provider.FollowTarget` to CycleCommit and CatalogView, produced by P3T6 from actual following targets with no verifiable matching/ordered/usable candidates and persisted with cycle diagnostics. ProjectCatalog consumes this explicit field. An internal view with no diagnostics producer cannot satisfy model_catalog_list's unmatched-target requirement. Tests drive a real unsupported target through refresh, DB reopen and registered admin list; empty source inputs must not produce a false all-clear.

CommitCycle calls the existing `commitAdvanceTx(ctx,tx,normalizedExpected,request)` helper and checks shared persistent revisions at transaction entry. A global token change aborts proposed advancements (or recollects before commit), while a per-profile ErrAdvanceConflict skips only that profile. Tests differentiate these outcomes and assert events include only actually committed changes.

P3T5 also owns `ProbeCursor map[string]string` on CatalogView and CycleCommit, persisted in migration 022 and returned after reopen. P3T6 supplies the updated per-path/config cursor through CycleCommit.ProbeCursor; CommitCycle stores it with completed evidence even for a degraded budget-limited scan. No cursor marks unprobed candidates confirmed. Add a reopen/rollback test with a non-empty cursor and evidence; empty/missing cursor injection must break the P3T7 bounded-scan progress sink.

Implement provider.PublicationJournal on ModelCatalogRepo: Unpublished selects original profile_advancement_journal rows without an acknowledgement, whether or not a report exists; Acknowledge atomically records IDs after confirmed publication. Migration 022 creates `model_publication_acks(advancement_id PRIMARY KEY REFERENCES profile_advancement_journal(id))`; preserve applied 021. CommitCycle journal rows use the same acknowledgement mechanism. MarkPublished(reportID) checks/acknowledges all the report's actual changes and clears PublicationPending; no-journal-change reports may be acknowledged directly. SQLite tests cover direct advancement with no report, acknowledgement errors, duplicates and reopen. This is the concrete producer for P2T3/P3T6 publication recovery, rather than an assumed report-only path.

## Owned persistence and direct-publication slice (2026-10-16)

- [x] Write `TestCycleCommitsModelJournalAndEvent` and real migrated-SQLite tests for classifications/no-followers/multiple paths, frozen payloads, conflicts versus shared revisions, injected write/ack faults, replay/concurrency, retained metadata/evidence/cursors/diagnostics, DB reopen and direct journal rows without reports. Add safe-projection tests and `TestCatalogPublicationRootAcknowledgesOnlyPublishedChanges` through the actual root/HTTP/executed-output/pin/ack sinks.
- [x] Run red: the exact focused gate first fails on missing contracts. Temporary empty API bodies then fail model/journal/event/report/recovery/projection assertions; remove them for implementation. The root initially fails compilation on missing construction, then (after correcting fixture YAML/capability inputs) behaviorally fails because successful publication has no acknowledgement and an injected acknowledgement fault is silently reported as success.
- [x] Implement migration 022, CatalogStore/RefreshReport/Observation/Event DTOs, the declared shared UsabilityEvidence DTO, historical source/entry retention, atomic cycle persistence using existing commitAdvanceTx, safe public projection and PublicationJournal. Implement the declared direct-acknowledgement prerequisite port/SetPublicationJournal and inject the actual repository at initDomain; never acknowledge before confirming cache publication. Preserve nanoseconds in the existing shared journal timestamp so returned and reopened original changes agree exactly. No probe, selection runtime, delivery transition or timer is invented here.
- [x] Run the focused gate, dependent full provider/cache/MCP/root packages, focused SQLite profile/advancement tests and race gates. Format Go and regenerate sqlc v1.31.1; retain only the ten new catalog table structs in generated/models.go, discarding unrelated regeneration drift in the pre-existing hand-curated generated files. No existing migration is edited; fresh install and genuine pre-020/pre-022 gap-fill with 023/024/025 recorded preserve the initialized secret flag.
- [x] Review spec compliance, then anti-green-theater, then code quality; rerun all gates and commit only this verified slice and its plan evidence. Full-task boxes remain unchecked for the producer-dependent acceptance above.

Ordered self-review evidence:

1. **Spec first:** persistence uses one transaction and the existing four-argument CAS helper, records only actual returned changes, never rewrites frozen first-event payloads, retains source release/canonical/policy inputs and confirmed evidence without TTL, and distinguishes nil/false/true webhook eligibility. Review adds a red test for invalid slices incorrectly advancing from retained membership, then skips only those proposals with `invalid_slice`; adds the required explicit persisted policy_revision column with an observed missing-column red. No network/probe/policy-edit/delivery feature or option is added. Public DTOs intentionally omit raw slices, source/proof/account locations and settings; arbitrary diagnostic text is classified, not serialized.
2. **Anti-green-theater second:** root HTTP refuses missing/wrong model, bearer credential, route, method, prompt or stream mode; assertions require durable original journal IDs, exact acknowledgement count, accepted node output, old X pins and fresh Y execution/pins. Temporarily remove acknowledgement and its missing-port guard: both unit guards and both root cases fail. Restore them, then remove only root SetPublicationJournal injection: both root cases fail closed. Restore wiring before verification. Store tests reject empty/incomplete/unknown/wrong-model evidence, use a second real DB writer for global policy/connection revisions, inject SQL trigger faults, and recover original rows after current-profile supersession; no fake ignores a CAS/proof/publication argument. Typed evidence is not a substitute for P3T4's genuine checks or P3T7's registered-tool/discovery sink.
3. **Quality third:** follow neighboring handwritten SQLite adapters, use transaction-local reads/revision checks, deterministic row ordering, owned maps/slices, static public codes and separately generated safe DTOs. Keep Phase 4 retry/part fields and transitions out of 022. Reuse existing cache publication and root gate; no duplicate CAS or refresh implementation. Final diff contains no temporary empty methods or disabled wiring.

Post-review gates (all exit 0):
- `go test ./adapter/sqlite ./domain/modelcatalog -run 'Catalog|Cycle|AnnouncementEvent' -count=1`
- `go test ./domain/provider ./adapter/provider ./adapter/mcp ./cmd/arbitron -count=1`
- `go test ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection|Catalog|Cycle|AnnouncementEvent' -count=1`
- `go test -race -p 1 ./domain/modelcatalog ./domain/provider ./adapter/sqlite ./cmd/arbitron -run 'Catalog|Cycle|AnnouncementEvent|AdvanceModel|AdvanceWiring' -count=1 -timeout=8m`
- `git diff --check`

Remaining work is the concrete P3T4/P3T6/P3T7 feed/recovery/registered-tool integration above, not an assumed no-op. No full-task completion, scheduled refresh, genuine usability, selected-model advancement or announcement-delivery claim is made by this slice.

## Current durable sinks and schema ownership

Migration 022, publication acknowledgements, current/historical Models, cursor/diagnostics and migration 026 retry/part storage already exist. Extend them in place. P3T6 owns all discovery/candidate CommitCycle feeds; P3T7 owns registered-tool/reopen/execution sinks and P4T3 receiver sinks. The D8 pin-route persistence schema is P1T3's new 031, not an edit to 020/022/026.

- [x] Add red/green persistence coverage for an exact-confirmed model with unknown tier/order and no Advances; assert original event, public usable state and metadata survive reopen. Reject empty/wrong-path/wrong-config proof. D8 unknown paths never create confirmed events.
- [x] Verify candidate bootstrap commits genuine observations/cursors without profile proposals through the same store, never a separate memory-only evidence sink. Add rollback/replay tests and ensure later following creation or automatic advancement cannot mutate the original frozen event. PublicationJournal.Unpublished includes direct rows with no report; P3T6 retries these on each cycle, not only restart.

> RESOLVED (2026-10-18, prospective-bootstrap feed, commits 75ed381/41ff3fa; acceptance below). Historical blocker: `CandidateDeps` still has only Store/Inventory/Probe/Clock; `CandidateService.read` reads the stored catalog and `prove` calls `remember`, retaining fresh confirmations only in its in-memory `proofs` map. `DiscoveryPipeline.Collect(ctx)` has no prospective profile input and `RefreshCoordinator` has no shared Do operation. P3T6 owns these missing producer/coordinator changes and recurring journal recovery. The storage-only and existing non-prospective root sinks below are verified, but cannot establish durable cold prospective confirmation or next-cycle publication repair. Do not re-fail this same missing feed on a later pass; move to independent P3T6 work. The first checkbox's complete wrong-path/config/D8-to-durable-event acceptance remains unchecked with the prospective feed; typed SQLite persistence cannot infer an expected route from an arbitrary nonempty PathKey/ConfigRevision.

### Independent native-evidence persistence slice (2026-10-18)

- [x] Write `TestCatalogNativeUnknownTierSurvivesReopen`, `TestCatalogNativeDiscoveryRejectsUnidentifiedProof`, and `TestCatalogNativeDiscoveryWriteFaultRollsBackAndReplays` using migrated SQLite, no initial profiles, empty tier/order/SpecificModel, actual cursor/metadata/event/public-view sinks and later following advancement. Add `TestCatalogDiscoveryWithoutAdvancesRoot` through real initDomain/newModelRefresh, guarded catalog transport, inventory, authenticated exact-model HTTP execution, coordinator, shared SQLite store and root reopen. Substitute only HTTP I/O; missing/wrong executed identity cannot create an event.
- [x] Observe behavioral red before implementation: the exact focused gate accepts unrelated NativeModel and a native spelling inconsistent with SpecificModel when no Advances exist. Correct a fixture-only nil Raw versus JSON null mismatch, then rerun to isolate those two intended failures. Positive unknown-tier persistence and rollback/replay controls already pass.
- [x] Add the minimal source-identity check for every incoming confirmed proof after the transaction's coherent persisted snapshot is available, before observation/event commit. Require its canonical identity and exact SpecificModel spelling, falling back to NativeID only when SpecificModel is absent. Do not require tier/order/immutability for discovery and do not reinterpret historical confirmations after later policy classification. The existing root's real pipeline feeds this same repository; no parallel evidence store or route API is introduced.
- [x] Complete spec-compliance, anti-green-theater and code-quality reviews in that order. Disable only the new guard: both intended negative tests fail. Restore it, then remove only `sel.Evidence` from the actual Collect-to-CommitCycle feed: the root positive fails at public usability and durable event count (not merely an intermediate call). Restore all mutations. Reuse source-backed historical entries and existing transaction rollback; no schema/query/UI changes or regeneration are needed.
- [x] Rerun focused, affected-root and race gates after review, check the diff, and commit only this verified persistence slice and its plan evidence. Leave full bootstrap/recovery acceptance unchecked rather than inventing a producer.

Fresh final verification (all exit 0):
- `go test ./adapter/sqlite ./domain/modelcatalog -run 'Catalog|Cycle|AnnouncementEvent' -count=1`
- `go test ./cmd/arbitron ./domain/modelcatalog -count=1 -timeout=8m`
- `go test -race -p 1 ./adapter/sqlite ./domain/modelcatalog ./cmd/arbitron -run 'Catalog|Cycle|AnnouncementEvent|UsabilityNativeUnknownTierRoot|UsabilityD8AndUnknownMappingRoot' -count=1 -timeout=8m`
- `git diff --check`

P2T2's existing cold-bootstrap blocker and P3T4's existing Codex tool-free source-contract blocker were inspected and not re-failed. This pass implements only P3T5's independent storage slice. Full-source unknown-tier scheduling, durable prospective bootstrap and recurring publication repair remain P3T6 work; registered-tool/execution and receiver release acceptance remain P3T7/P4T3 work.

## Durable bootstrap acceptance and completion (2026-10-18)

Selection: P3T4 retains its external Codex tool-free source-contract blocker; do not re-fail it. P3T6 now supplies coordinated prospective Collect, durable proof/cursor commits and recurring journal recovery. This pass closes P3T5's remaining storage/feed acceptance without claiming P3T4's external contract or P3T7/P4T3's complete release gates.

Strict TDD:
- Add `TestCatalogProjectionRequiresCompletePathProof`, run the exact focused gate and observe behavioral red: missing ProofRef, NativeModel or CheckedAt makes the entry unknown but incorrectly leaves its public path confirmed. The exact/unknown-tier positive control already passes. Add only the complete-proof guard to ProjectCatalog's per-path state, then rerun the focused gate green.
- Add `TestCatalogProspectiveBootstrapAtomicFrozenEventsRoot` through real initDomain/newModelRefresh, guarded HTTP fetch, persisted policy, actual credential-injected HTTP execution, shared coordinator/SQLite store and ProfileService. Event/report/cursor SQL faults roll back all source/history/evidence/observations/reports/cursors and leave profiles/journals empty. Retry must actually re-probe rolled-back checks; a successful repeat and reopen reuse persisted proof. No bootstrap profile proposals are committed. An unclassified model's native/canonical ID, release date, SourceRef, proof, cursor, usable public entry and original no-change event survive reopen. Later following creation and automatic advancement produce an actual journal/new-run output without rewriting any original payload.
- Use real persisted-proof faults to reject missing proof, wrong path and wrong config at prospective revalidation, with positive controls before/after. Add `TestCatalogDiscoveryRejectsChangedConfigRoot` and `TestCatalogOpaquePathsNeverPersistConfirmationRoot`: credential rotation during exact I/O and native Azure/Bedrock/unmapped routes persist explicit unknown evidence and no event. The repository is not asked to infer an expected route from arbitrary typed strings; its production inventory/router/selector supplies and validates that context.
- The additional root storage acceptance is already implemented by P3T6 and passes without another runtime. Correct only an initial fixture assumption that profile creation never adds a distinct normalized route; compare exact pre/post-reopen calls instead. Do not count that fixture failure as behavioral red. Existing named SQLite tests establish the earlier CAS/event/replay/migration red/green history above.

Ordered self-review:
1. **Spec compliance first:** only close this task's durable storage/feed/public-projection contract. Reuse the existing graph and transaction helper; no new schema, port, scheduler, mapping, delivery behavior, option or webhook value. No sqlc/UI regeneration is necessary. Existing direct no-report journal/recovery tests are freshly run below.
2. **Anti-green-theater second:** HTTP leaves reject missing/wrong method, route, model, bearer credential, prompt, mode or tool posture. Assertions cover exact SQL rollback, original frozen payloads, reports/acknowledgements, persisted metadata/cursors, restarted proof reuse, executed new-run model/output and actual registered model_catalog_list content. Temporarily remove the new projection guard: all three root cases expose confirmed paths for empty proof at the MCP sink, plus the three unit cases fail. Restore it. Temporarily remove path/config comparisons from the existing evidence matcher: all six SQL-corruption refusals fail by accepting the wrong proof. Restore that mutation too; candidates.go has no final diff. Strengthen source/canonical metadata and public-list membership assertions so missing inputs cannot pass vacuously.
3. **Code quality third:** follow neighboring migrated-root fixtures, reuse mcpToolCaller and the existing shared Confirmed predicate, isolate HOME, use mutex/atomic request counters, close stacks before reopening, and format Go. No memory proof cache, ignored load-bearing argument or temporary mutation remains.

Fresh final verification (exit 0):
- `go test ./adapter/sqlite ./domain/modelcatalog -run 'Catalog|Cycle|AnnouncementEvent' -count=1`
- `go test ./domain/modelcatalog ./adapter/sqlite -count=1 -timeout=5m` (both also passed in the broader command below).
- `go test ./cmd/arbitron -run 'Catalog|ProspectiveBootstrap|RefreshRepairsPublicationWithoutRestart|DiscoveryAllSourceCandidatesOnEveryPathRoot|UsabilityNativeUnknownTierRoot|UsabilityD8AndUnknownMappingRoot' -count=1 -timeout=5m`
- New three root test functions also pass three repeated runs.
- `go test ./adapter/mcp -run 'Catalog|IdentityPolicy' -count=1`
- `go test -race -p 1 ./domain/modelcatalog ./adapter/sqlite -run 'Catalog|Cycle|AnnouncementEvent' -count=1 -timeout=8m`
- `go test -race -p 1 ./cmd/arbitron -run 'Catalog|ProspectiveBootstrap|RefreshRepairsPublicationWithoutRestart|DiscoveryAllSourceCandidatesOnEveryPathRoot|UsabilityNativeUnknownTierRoot|UsabilityD8AndUnknownMappingRoot' -count=1 -timeout=8m`
- `git diff --check`

Broader-suite concerns, not this task's focused-gate failures: the additional full adapter/mcp run fails pre-existing profile fixtures in profile_authz_e2e_test.go and tools_profile_test.go because they omit the mandatory version resolver. An initially overbroad root race regex containing Cycle also includes TestRefreshDroppedProposalsDeferAnnouncementToCommittingCycle, whose separate refresh graph is not injected into ProfileService and returns no eligible candidate during profile_update. These files are unchanged. The task-specific root/race gates above are green; no full-suite/release pass is claimed. P3T7/P4T3 retain the broader integration/fixture gates.

## Current provenance and no-advance transaction acceptance (2026-10-19)

Preserve existing migration `032_model_evidence_provider` and `UsabilityEvidence.Provider`, populated by P3T6's selector from the checked catalog Entry. Persist/reload it as `model_usability_evidence.catalog_provider` and use existing `confirmingEntry` to choose the actual confirmed offering's provider/tier for the immutable event, rather than the first historical offering sharing its canonical ID. Migration-032 legacy empty provenance stays empty; preserve legacy evidence and frozen events without inventing a backfill. Applied 019–032 are immutable; earlier suggestions to extend 022/026 or reserve 031 are historical.

Also preserve `CycleCommit.PolicyRevision int64` and `DeferDiscovery []string` from `domain/modelcatalog/store.go`. P3T6 produces them; `checkCyclePolicyTx` rejects stale fresh classifications even in discovery-only commits, and CommitCycle defers affected first observations when all their proposals were dropped/conflicted. Evidence/cursors remain durable for retry. Unrelated/no-follower observations continue; a later eligible cycle freezes only actual returned changes. `AdvanceRequest.Evidence` consumes the exact P2T1 path refs. Reuse the existing methods/schema; no new store or event format is needed.

- [x] Extend existing `adapter/sqlite/model_catalog_provenance_test.go:TestCatalogEventNamesConfirmingProvider` only where assertions are missing; do not add a duplicate test: two source offerings share a canonical ID but have different providers/tiers/spellings; only the second is confirmed. Assert the second offering and exact proof persist after reopen and supply the frozen payload; later confirmation/order/policy edits never rewrite it. Failure controls reject forged provider/spelling; error controls roll back evidence/event together; edge controls cover 031→032 upgrade and legacy empty provenance. Run the focused gate red before fixing any missing feed, then green. Existing passing assertions need no invented red.
- [x] Extend existing `adapter/sqlite/model_catalog_repo_test.go:TestCatalogCycleDiscoveryOnlyRejectsStaleClassification` and `TestCatalogCycleDefersDiscoveryUntilAdvancementCommits` only for missing assertions: an external DB writer changes policy after classification, with Advances empty. Stale catalog/evidence/event/report writes all reject; retry under the current policy succeeds. Cover policy-read/storage faults and unchanged revision. Test dropped/all-conflicted proposals versus unrelated no-followers and the next successful committing cycle through existing cycle tests. P3T7/P4T3 add registered-tool/executed-model/receiver sinks; these local tests do not wait on them.

### Provenance and discovery-only acceptance completion (2026-10-19)

Selection: P3T4's recorded non-Codex inherited-tool/MCP/plugin isolation blocker remains unresolved. Per the no-spin rule, this pass completes only P3T5's independent unchecked acceptance, not another attempt at that blocker or any downstream task.

Extended the three existing named tests in place. File-backed reopen preserves both differently spelled/providered/tiered offerings and the exact second-offering proof. Original event and observation bytes remain unchanged after a later first-sorting offering confirms and classification/order/tier changes. Forged provider/spelling rejects atomically; evidence/event insertion faults roll back and replay successfully. Actual migrations through 031 plus historical SQL establish the upgrade fixture; 032 leaves legacy provenance empty, even when a subsequent cycle supplies today's provider, and preserves the frozen event bytes.

A separate SQLite connection edits policy after discovery-only classification. Stale/mixed revisions, missing policy rows, policy-table storage faults and catalog-write faults leave catalog/history/state/evidence/cursors/observations/events/reports/journal empty; the same report ID succeeds after correction under the current revision. Dropped and all-conflicted proposals preserve proof/cursors while an unrelated no-follower discovery commits immediately. The next cycle supplies no new evidence, advances using durable proof, and freezes only its actual journal changes; replay duplicates nothing.

TDD evidence: ran the specified focused gate after each test extension and before any production change. Existing production behavior satisfies the strengthened controls, so no missing feed or implementation red was invented. The first run exposed only a fixture slice-aliasing error: updating the later input also mutated the expected legacy payload. Give the expected payload its own proof slice; all assertions then pass. No production, schema, query, API or UI change was needed.

Ordered self-review:
1. **Spec compliance:** only extend this task's existing tests and completion record. Preserve applied migrations and empty legacy provenance. P3T7/P4T3 retain their registered-tool/executed-model/receiver obligations.
2. **Anti-green-theater:** tests assert exact reopened evidence, immutable SQL payload/observation bytes, rollback across durable tables, actual profile/journal changes and unrelated discovery progress. Temporarily drop persisted Provider, bypass the cycle policy input and ignore discovery deferral: all three named tests fail behaviorally (lost proof provenance, accepted stale classifications, premature observations). Restore every mutation; production repository has no final diff. Existing real-root bootstrap/discovery/publication tests below verify that the already-constructed shared store is fed by the production pipeline and reaches persisted events/new-run output; no new component or test-only wiring is introduced.
3. **Code quality:** follow neighboring migrated SQLite/root fixtures, reuse existing cycle/event helpers and historical migration files, keep expected proof independent of snapshot ordering, format changed Go files and run vet. No generated-artifact regeneration is required.

Fresh post-review verification (all exit 0):
- `go test ./adapter/sqlite ./domain/modelcatalog -run 'Catalog|Cycle|AnnouncementEvent' -count=1`
- `go test ./adapter/sqlite ./domain/modelcatalog -count=1 -timeout=5m`
- `go test ./cmd/arbitron -run 'CatalogProspectiveBootstrapAtomicFrozenEventsRoot|CatalogDiscoveryWithoutAdvancesRoot|CatalogPublicationRootAcknowledgesOnlyPublishedChanges|CatalogDiscoveryRejectsChangedConfigRoot|CatalogOpaquePathsNeverPersistConfirmationRoot' -count=1 -timeout=5m`
- `go test -race -p 1 ./adapter/sqlite ./domain/modelcatalog -run 'Catalog|Cycle|AnnouncementEvent' -count=1 -timeout=8m`
- `go vet ./adapter/sqlite ./domain/modelcatalog`
- `gofmt -l adapter/sqlite/model_catalog_provenance_test.go adapter/sqlite/model_catalog_repo_test.go` (empty output)
- `git diff --check` and `git diff --exit-code -- adapter/sqlite/model_catalog_repo.go`

All P3T5 checkboxes are now complete. No full-feature release or P3T4 source-contract completion is claimed.
