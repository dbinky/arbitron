# Latest Model Profiles — Phase 3 Task 6: Eligibility, candidate producer and single-flight refresh Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Eligibility, candidate producer and single-flight refresh.

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

**Depends on:** P1T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-1-task-1.md`), P2T2–P2T3 and P3T1–P3T5.

**Files:** Modify `domain/settings/settings.go` to add the P1T1-described InternalReader interface. Create `domain/modelcatalog/candidates.go`, `domain/modelcatalog/refresh.go`, `domain/modelcatalog/loop.go`, tests; update `domain/provider/advance_service.go` validation to use evidence without adapter imports.

## Interfaces (owned here unless explicitly consumed)

Preserve `CandidateService.Newest(ctx,profile)` / `ValidateStart(ctx,profile)` returning `(provider.EligibleCandidate,error)`, `VersionRequests` and read-only `RevalidateStart`. Extend the existing runtime using the exact dependency/Collect/coordinator contracts in “Current orchestration repairs and exact shared contracts” below. Internal `RefreshService.Refresh(ctx) (RefreshReport,error)` is distinct from authorized `AdminService.List` / `RefreshNow`; P3T7 injects *AdminService into MCP deps. Preserve existing RefreshLoop.Run(ctx) (no result), RefreshLoop.NewTicker and RefreshCoordinator.Refresh/Close, adding the shared Do admission path. Announcer.Announce(ctx,RefreshReport) error is mandatory in production; staged DeferredAnnouncer preserves durable events.

## Data producers and composition-root wiring

Settings.ReadInternal supplies URL/webhook classification; fetcher/normalizer supplies all slices; inventory supplies every actual configured path; probes supply confirmed evidence for the unchanged path/configuration revision; ListInternal supplies expected profiles; CandidateService supplies explicit/newest versions for admin creation and refresh selection. RefreshService acquires one lock shared with manual/scheduled requests; prepare validated CAS requests, recheck inventory revision inside shared execution lease, then CatalogStore.CommitCycle -> PublishCommitted -> report -> Announcer. Recover pending cache publication from journals at startup and before successful report.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestRefreshAdvancesBySuccessfulFourHourTick` fetches newer same-target usable candidate, commits/publishes Y and new run sink uses Y while old sink uses X.
- Success: no followers still produce event; no Anthropic path works; admin following creation without model selects persisted candidate.
- Failure: pinned/wrong provider/tier/unknown current/tied order/unavailable path/incompatible spellings cannot advance; no downgrade when current disappears.
- Error: fetch/slice/probe/CAS/persistence/publication failures are isolated appropriately; failed publication marks pending and repair returns Y without duplicate change; announcer error cannot undo model.
- Edge: startup/restart, concurrent manual tick, path edit between probe/CAS, equal dates, unchanged confirmation reuse across four-hour ticks and credential revision change, event eligibility read fault and cancelled 3h cycle.
- Input guard: test fakes check URL, exact candidate, inventory revision, all aliases and AdvanceRequest expected fields. P3T7 integration removes each collaborator/data input in turn and asserts the final sink test fails, not merely report shape.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, "version-Y", publishedModel)
assert.Equal(t, "version-X", activeInvocation.Model)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

Newest for initialization requires same provider/tier and confirmed fixed version; automatic advancement separately requires a known immutable current version and strictly newer order; all consuming paths need confirmed evidence and one common executable model spelling. Never alter provider/target/routing/axes/tools/pins. Discovery traverses all configured candidates/paths, independent of followers. No paths/incomplete relevant inventory cannot imply vacuous all-path success. Initialize an absent starting model from newest eligible without needing a current version; ValidateStart checks membership and exact route usability. Fetch failure retains working models/catalog and still drains announcement retry through Announcer. Awaiting-setting classification retries even when discovery fails. Notification errors recorded separately, do not undo model success. Scheduled and manual overlap returns explicit already-running, never duplicate work. Bound a cycle to 3h, per-check bounds and 4 workers so ticker stays meaningful; incomplete scan reports degraded and prevents affected advances, never claims full successful coverage.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./domain/modelcatalog ./domain/provider -run "Refresh|Candidate|Follow|Loop" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Provenance / release gate (resolved 2026-10-01)

Provenance gates resolved 2026-10-01 (D2, D3–D6 in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`): implement and test the chosen rules exactly as recorded there; do not substitute synthetic evidence for the real probe/catalog/delivery behavior they define.

## Bootstrap, probing and cycle results

Use a shared `DiscoveryPipeline` produced here with bounded fetch/normalize/inventory/probe collection and a separate CatalogStore commit boundary. CandidateService receives this pipeline, RefreshCoordinator and CatalogStore; RefreshService receives the same pipeline plus profile advancer/publisher. Extend `DiscoveryPipeline.Collect(context.Context,*provider.Profile) (CycleCommit,error)`; reuse the existing CycleCommit fields, SelectionInput and Selection rather than create DiscoveryInputs. Optional prospective Profile supplies seed/create routes. Collect does not commit or call mutation/seed/candidate methods; its read-only AdvanceValidator may be implemented by ProfileService. Refresh selects advancements from these inputs and then calls CommitCycle once so the event freezes actual committed changes; candidate bootstrap calls CommitCycle with no proposed advances because no profile has yet been created. No nested coordinator acquisition is allowed. Refresh selects candidates from a read-only merged view of freshly collected inputs and historical CatalogStore.Snapshot through an internal unlocked eligibility helper (newly fetched evidence participates before the atomic commit); public Newest/ValidateStart acquire the coordinator only when they need discovery. Explicitly test a cold empty DB seed and simultaneous manual refresh/creation to detect recursive lock/constructor cycles.

For each `(CanonicalID, PathKey, ConfigRevision)`, load P3T5 persisted evidence before deciding to probe: skip confirmed entries without time expiry, retry failed/unknown later, and check new models/new path revisions. Probing all configured models without followers remains required. Newest means newest eligible for initialization; comparison with current model and strictly newer ordering is a separate advancement check, so ValidateStart can validate an older still-working model. A complete fresh scan with no eligible replacement is successful without change; incomplete slices/paths retain data and inhibit affected advances. Fetch/normalization/global inventory failure does not prevent Announcer draining durable pending events.

Persist and return the actual post-publication report: P3T5 MarkPublished clears PublicationPending only after authoritative cache verification. Never return success based on a proposed advance or on a durable report still marked pending. Cycle setting-read errors create awaiting_setting events; P4T2 retries classification at every drain, even after discovery failure. Manual and scheduled calls return an explicit busy result on coordinator overlap; callers can retry without recursively taking the lock.

Use `RefreshService.SetAnnouncer(Announcer) error` with nil/start-state rejection and the existing loop's Now/NewTicker root seams, as specified below. AnnouncementService consumes the same Now via its existing narrow clock interface. No competing Clock/Ticker hierarchy is introduced.

DiscoveryPipeline passes the real settings CatalogURL into both Fetch(ctx,url) and Normalize(ctx,url,raw), matching P3T2's exact signature; it does not supply a hardcoded SourceRef. Normalizer's time callback comes from the same root Clock.Now so catalog and claim/event timestamps share the test clock.

## Selection proof, route-only validation and current policy feeds

Implement every extended P2T1 EligibleCandidateSource method on CandidateService. Newest/ValidateStart return non-empty canonical/model identity and the exact prospective PathsForProfile revision/ExecutionRevision. RevalidateStart recomputes that prospective snapshot and verifies all proof/config/model/target fields against persisted confirmation while already under the caller's mutation lease; it never acquires coordinator/gate or fetches/probes. VersionRequests delegates to P3T3's route-only method without policy/catalog access. ValidateStart returns proof for the supplied model even when a newer one exists. Update checking fakes to reject zero/mismatched proof; never erase proof in ValidateSelection.

For existing-profile automatic advancement, build requests using the current installation Snapshot token, not a prospective token. An internal eligibility helper may share matching logic but preserves this revision distinction. Read retained CatalogModels from P3T5 and re-map current and candidate through one policy revision (check all mapped PolicyRevision values agree before selection; a concurrent edit discards/retries selection). Populate CycleCommit.UnmatchedTargets and visible classified reasons for unknown current, unavailable paths and ambiguous identity.

Add red/green tests for new profile/edited route whose prospective digest differs from current, stale proof after credentials/policy/workflow change, DB change from another process, preserved raw metadata across policy edits, axes/tool edits requiring new config proof, and pinned explicit-version validation during policy/catalog faults. P3T7 root tests execute the resulting selected model, rather than just observing a successful ValidateStart call.

Bounded scans must make progress across ticks. P3T5 persists already completed per-model/path checks; schedule missing/unknown candidates fairly before repeatedly retrying unavailable ones, and retain per-path rotating scan position in catalog state when a 3h budget prevents full coverage (P3T5 owns `CatalogView.ProbeCursor map[string]string` and CycleCommit's same field; key is path/config revision, value last considered canonical ID). P3T6 produces/consumes the cursor with deterministic wraparound, prioritizes prospective requested candidates without starving discovery, and preserves completed results on budget exhaustion via a degraded CommitCycle with no unsupported advances. New path/config starts its own cursor. Add a tiny-budget many-candidate test across restart/ticks: a newly usable tail model eventually reaches persisted usability/event and execution, even with a permanently unavailable head. Cancellation/shutdown cannot report a successful full scan. Root clock tests cover the cursor feed; no fake may supply an empty cursor forever while claiming all models were scanned.

Reserve 30s of the 3h cycle bound for commit/publication: stop collection by 3h-30s and return partial CycleCommit plus degraded statuses/cursor, then persist completed evidence with the still-live cycle context. Do not try to commit using an expired scan context or discard all progress on every budget timeout. Parent shutdown cancellation stops promptly; uncommitted work may repeat next start, without falsely reported confirmation. Happy/success/failure/error/edge tests exercise this time budget with a fake clock and bounded I/O, not real multi-hour waits.

Before startup/refresh success, consume PublicationJournal.Unpublished for direct and cycle advances, call the shared publisher on the original rows, acknowledge only after confirmed cache revisions, then MarkPublished on recovered reports whose changes are acknowledged. Missing report rows do not hide pending direct advances. Journal read/ack/reload errors remain publication-pending and cannot trigger another CAS. P3T7 injects the same actual ModelCatalogRepo journal port into ProfileService and refresh recovery and asserts the new-run execution sink.

Read internal settings for CatalogURL before fetching; after collecting observations, read internal settings again for discovery announcement eligibility. A first URL-read fault aborts fetching and preserves catalog/profiles; a later eligibility-read fault retains collected observations as awaiting_setting via nil WebhookConfigured. Tests distinguish these faults. No default/hardcoded catalog URL or empty webhook is substituted for a read error. P4T2 independently retries classification even if later discovery fails.

## Current orchestration repairs and exact shared contracts

**Files:** extend existing `domain/modelcatalog/{refresh,candidates,candidate_source,probe_schedule,loop,admin}.go`, tests and `domain/provider/advance_service.go`. Do not add parallel refresh/candidate implementations. Existing internal entry is `RefreshService.Refresh(ctx) (RefreshReport,error)`; authenticated operations are `AdminService.List(ctx) (PublicCatalogView,error)` and `RefreshNow(ctx) (PublicRefreshReport,error)`. P3T7 exposes *AdminService in MCP deps. Preserve this service-level authorization boundary rather than put transport-only checks on an internal Refresh method.

**Interfaces owned here:** extend `DiscoveryPipeline.Collect(ctx context.Context, prospective *provider.Profile) (CycleCommit,error)` from its existing one-argument method. A nil prospective is scheduled/manual installation discovery; a nonnil profile overlays actual prospective routes and prioritizes its target, with no profile proposals or mutation. Reuse existing CycleCommit/SelectionInput/Selection and CatalogStore; no second DiscoveryInputs DTO. Add `RefreshCoordinator.Do(context.Context,func(context.Context) error) error` sharing admission/cancellation/join with existing `Refresh(ctx)` and `Close()`. Busy remains ErrRefreshBusy. No operation calls Do/Refresh recursively.

Extend existing CandidateDeps to `{Store CatalogStore; Inventory CandidateInventory; Pipeline *DiscoveryPipeline; Coordinator *RefreshCoordinator; Gate Gate; Mapper EntryMapper; Clock func() time.Time}`. CandidateService uses persisted evidence only for local validation; if a current/prospective route needs discovery, run Collect(ctx,&profile) → Gate.WithRead → CommitCycle (no Advances) → MarkPublished inside the shared coordinator, then reload/remap the stored CatalogModels and return proof for that exact prospective snapshot. This replaces direct uncoordinated candidate probing and memory-only `proofs`. ValidateStart never substitutes another model. RevalidateStart performs local read-only rechecks of full target/model/canonical identity, policy/connection token, prospective digest and every persisted path/config proof under the caller's existing mutation lease; it never fetches, probes or acquires the coordinator/gate. Remap historical/current candidate metadata at one policy revision even before the first refresh after a policy edit.

Add `AdvanceValidator` with `ValidateAdvance(context.Context,provider.AdvanceRequest) error` to DiscoveryDeps, mandatory, supplied by P2T3 ProfileService. Before proposing each automatic replacement call it outside the commit lease, then revalidate persistent/external revisions and complete expected row at commit as already designed. Capability-invalid candidates are skipped with classified diagnostics; continue to an older/newer compatible eligible candidate rather than commit an incompatible Model or block unrelated profiles. Validator failure cannot manufacture usable evidence, journal rows or successful publication.

Retain existing Clock.Now structural use in AnnouncementService. Extend existing RefreshLoop.NewTicker seam and root clock wiring rather than add a competing Clock/Ticker hierarchy: injected `Now func() time.Time` plus `NewTicker func(time.Duration) (<-chan time.Time,func())` feed persisted times and the actual 4h timer. Make Announcer mandatory for production RefreshService (concrete durable service at final root; DeferredAnnouncer only for explicitly staged Phase 3 tests). `SetAnnouncer(Announcer) error` rejects nil and may run only before lifecycle start. Delivery errors populate classified AnnouncementError without reversing model success.

### Discovery independent of eligibility

- [x] Write red `TestDiscoveryAllSourceCandidatesOnEveryPath`: include a path consumed by a following alias, an older/snapshot model, another tier and an unknown-tier NativeID on that same provider, plus a no-profile subscription path. Every source-backed native candidate gets an exact probe attempt/explicit unknown outcome on each compatible configured path, irrespective of followers or tier/order/immutability. Only confirmed evidence creates first-use events. Missing mapping stays unknown; D8 native paths reject confirmation.
- [x] Replace `probe_schedule.go`'s `pathFollowed` skip and `newestPerTier` discovery filter with full-source candidate enumeration. Remove the test expectation `TestSelectorDiscoversNewestPerTierOnPathsWithoutFollowers` that asserts older models are never checked. Follower priority and per-profile selection bounds may control work order, never permanently exclude a candidate. Use the persisted per-path/config cursor for bounded fair progress; include unknown/native-only candidates in that queue. Deduplicate actual proof work, retain all paths/provider identity, and keep strict followability/order filters only in replacement selection.
- [x] Verify success/failure/error/edge cases across tiny-budget ticks and restart: tail usable model is eventually persisted/announced despite a failing head; confirmed unchanged evidence is reused; unsupported mapping and wrong model cannot confirm; temporary config failure retries without deleting historical discovery; all completed evidence/cursors persist on degraded exhaustion. The outer cycle remains bounded to at most 3h including fetch/normalize (current budget starts only after those operations); reserve 30s for commit/publication and drain with a live context, cancel promptly on shutdown. Run focused tests red then green; P3T7/P4T3 drive execution/receiver sinks.

### Cold creation and durable recovery

- [x] Write red `TestProspectiveBootstrapPersistsProof`: empty SQLite, no startup refresh yet, complete prospective routing input; Newest/ValidateStart share the scheduled/manual coordinator and populate catalog/evidence/event before profile persistence. Reopen and revalidate without a second successful probe; reject missing pipeline/coordinator, wrong target/token/config, unknown exact evidence and store faults. A concurrent refresh/create gets explicit busy or a documented retry, never uncoordinated network work/deadlock. Tests for cache/policy/history faults assert no profile write and preserve previous working rows.
- [x] Implement those candidate dependency/Collect changes and durable prospective commit, rerun candidate/refresh tests green. Do not call profile mutation or SeedProfiles inside Collect; the read-only AdvanceValidator is allowed. Root injection is P3T7; first-boot seed is P2T2 consuming this producer.
- [x] Write red `TestRefreshRepairsPublicationWithoutRestart`: commit direct Y with failed publication/ack and no report; next actual cycle (including fetch failure or zero advances) consumes PublicationJournal.Unpublished plus CatalogStore.Recover, publishes/acknowledges original changes and clears pending reports without a second CAS. A repeated reload/journal/ack fault returns ErrPublicationPending and never full success; Announcer still drains independently. Also repair a prior cycle report after its model is superseded/deleted.
- [x] Invoke recovery inside each coordinated Refresh before successful completion, not only `modelRefresh.start`. Keep recovery serialized with selection/publication and ensure new-run Y is visible before acknowledgement. Rerun focused tests green. P3T7 tests both actual clock ticks and registered manual refresh; a log promising next-cycle repair is not a producer.
- [x] Verify `TestAutomaticAdvanceUsesConfiguredCapabilities`: real validator rejects incompatible Y, accepts compatible Z, and automatic profile/journal/cache sinks follow the same rules as direct AdvanceModel. Nulling the validator must fail construction/positive controls. Rerun task and affected-package gates, then race tests.

Selection reads every fully confirmed eligible candidate from the completed/current merged view. Remove fixed maxCandidatesPerProfile truncation from final newerCandidates/newestEntries selection: a work budget may delay probing, but cannot permanently hide the fourth eligible model after three newer unusable ones. Add red/green tests with more than three newer releases, the first three unavailable and a later candidate confirmed on every path; following creation/advancement must select the newest actually eligible version. A budget-limited scan is degraded until required coverage is complete, never a successful refresh concealing excluded candidates.

## Completion evidence (2026-10-18)

This pass skipped the previously recorded P2T2 cold-producer, P3T4 Codex restriction and P3T5 prospective-feed blockers rather than re-failing them. It implements this task's local producers; P2T2/P3T5 can now consume durable prospective discovery. P3T7 still owns full first-boot seed/registered manual-tool acceptance and P4T3 owns receiver/release acceptance; neither is claimed complete here.

Strict red/green evidence:
- `TestDiscoveryAllSourceCandidatesOnEveryPath`, wrong-executed-model/D8 and fourth-eligible-release tests failed against the old follower-only/newest-per-tier/three-candidate behavior. Full-source enumeration now covers every compatible path, including native-only unknown metadata; followability remains a separate selection filter.
- `TestRefreshBudgetIncludesFetchAndLeavesCommitContextLive` and the caller-deadline reserve test failed before fetch/normalize joined the bounded collection context. The 3h outer bound reserves 30s for commit/publication, persists partial observations/cursors, and uses a live context for capability validation after scan exhaustion.
- `TestProspectiveBootstrapPersistsProof` failed from empty catalog storage before coordinated bootstrap existed. It now drives real root assembly, source normalization, authenticated exact HTTP execution, SQLite report/evidence/event persistence and reopen/local revalidation without a second probe. Additional root tests reject unknown proof, policy/history/write faults without changing existing profiles, and exercise manual/bootstrap busy admission.
- `TestRefreshRepairsPublicationWithoutRestart` failed when a repeated acknowledgement fault incorrectly returned success. It now repairs original direct (no report) and cycle journal rows on the next coordinated cycle even with fetch failure, including superseded/deleted profiles, without another CAS; new-run output and old pins are checked.
- `TestAutomaticAdvanceUsesConfiguredCapabilities` failed by persisting incompatible Y. The mandatory real ProfileService validator now retains X, then commits/publishes/executes compatible Z with unchanged axes/tools/target. Typed-nil root injection and missing pipeline validator both fail construction.
- `TestRefreshAdvancesBySuccessfulFourHourTick` first failed compilation for the absent root ticker field, then behaviorally failed with that field present but unwired. The actual root startup/loop now consumes the injected 4h ticker and shared clock; an active two-call run executes X/X while a fresh run executes Y/Y.
- `TestDiscoveryUsesFourBoundedProbeWorkers` failed on the serial scheduler. Four workers now own isolated checks and join before collection returns; results merge in dispatch order for stable cursors and diagnostics. The real-I/O tiny-budget fixture was updated from a serial single-check allowance to one worker-sized batch and passed ten repeated runs. Batch/interactive exact confirmations also survive actual root/SQLite reopen without re-probing.

Self-review was performed in order: (1) spec compliance, including the four-worker repair, immutable/ambiguous current-model guards, full proof/token revalidation and classified diagnostics; (2) anti-green-theater, removing the real evidence feed, validator injection, unknown-tier enumeration and recurring recovery in controlled mutations, each making the corresponding durable/profile/output tests fail, then restoring all mutations; (3) code quality, deterministic parallel result merging, mandatory raw-settings/announcer ports, lifecycle replacement rejection, shared coordination, deep-copying prospective inputs, formatting and removal of the memory-only proof cache and candidate truncation. No schema/query/UI changes or regeneration were needed.

Fresh final gates (exit 0):
- `go test ./domain/modelcatalog ./domain/provider -run 'Refresh|Candidate|Follow|Loop' -count=1` (also rerun with Discovery coverage).
- `go test ./domain/modelcatalog ./domain/settings -count=1`; the catalog package additionally passed ten repeated runs.
- `go test ./adapter/sqlite ./adapter/probe ./adapter/provider -run 'Catalog|Cycle|Usability|Advance|Inventory' -count=1 -timeout=5m`.
- Root tests matching `^(TestProspectiveBootstrap.*|TestDiscoveryAllSourceCandidatesOnEveryPathRoot|TestRefreshRepairsPublicationWithoutRestart|TestAutomaticAdvanceUsesConfiguredCapabilities|TestRefreshAdvancesBySuccessfulFourHourTick|TestModelRefreshBudgetRestartsReachTailExecution|TestModelRefreshRootInteractiveChatRouteProvesCandidateFromStream)$`, three repeated runs, and the same tests with `-race -p 1`.
- `go test -race -p 1 ./domain/modelcatalog ./domain/provider ./adapter/sqlite ./adapter/probe ./adapter/provider -run 'Refresh|Candidate|Follow|Loop|Catalog|Cycle|Usability|Advance|Inventory|Discovery' -count=1 -timeout=8m`.
- `git diff --check`.

Broader-suite concerns (not a full release pass): a full provider run still fails unrelated fixtures lacking version-selection ports. A full root run exposes legacy fixtures assuming only newest-tier discovery/one announcement/one harness probe, source bootstraps that now reach real external catalogs, and fixtures refreshing a separate graph without injecting its source into ProfileService before later prospective-route validation. Those broader fixture/root-release repairs remain P3T7/P4T3 work; the task-specific and affected-adapter gates above are green. No production input was disabled to preserve those old expectations.

## Current explicit field producers (2026-10-19)

Preserve the implemented feeds in `candidates.go` and `refresh.go`: the selector stamps every attempted evidence row's `Provider` from the exact checked Entry (catalog provider, not Connection.Provider), builds each AdvanceRequest.Evidence from all actual consuming paths/config revisions, and Collect records one PolicyRevision for all fresh classifications, including prospective/no-advance discovery. A failure baseline that reuses the unchanged persisted catalog may retain PolicyRevision=0; fresh mappings never do. RefreshService populates DeferDiscovery only for affected canonical IDs whose proposals it drops on stale configuration; CommitCycle accounts for transaction conflicts. These fields are consumed by P3T5's existing transaction checks/event builder, not inferred later from a live profile or today's catalog.

- [x] Strengthen selector/refresh tests with nonempty Provider, Evidence and PolicyRevision assertions plus checking stores that reject missing/mismatched fields. Happy/success feeds reach P3T5 reopen and original event payload; failure/error controls cover wrong offering, omitted path, changed policy even without Advances, and persistence failure; edge controls defer a stale proposal then commit on the next cycle without re-probing unchanged confirmation or starving unrelated discoveries. Run focused tests before any implementation change, observe behavioral red for a missing feed, implement minimally and rerun green. P3T7/P4T3 own the final execution/card forms, so this producer task remains acyclic.

## Explicit field-producer acceptance (2026-10-20)

Selected this independent task after skipping P3T4's already-recorded non-Codex restriction/source-contract blocker. Preserved and completed the existing uncommitted P3T6 tests/change; no other task's acceptance is checked here.

Red/green evidence:
- Restored the original selector before implementation and ran the new selector/producer tests. `TestSelectorEvidenceUsesCheckedOffering` failed behaviorally: stored confirmation from provider `other` bypassed both missing and failed exact checks. Adding the offering comparison then exposed `fresh_confirmed`: a lane's routing-provider value incorrectly rejected valid proof. Compare stored provider provenance (allowing genuinely pre-032 empty provenance) and stamp fresh evidence from the checked Entry before validation. The tests pass with only this selector correction; existing path-reference, policy and deferred-discovery producers needed no new implementation.
- `TestRefreshProducesPolicyAndCompletePathEvidence` drives Collect into a checking commit sink for ordinary advancement and prospective/no-advance discovery. It rejects empty/wrong providers, zero/wrong policy revisions, missing/partial/wrong-config path references and persistence errors. Inventory/profile read-fault controls retain the captured fresh classification revision.
- `TestRefreshProducerFieldsPersistOriginalObservationRoot` substitutes HTTP leaves only. Real startup, persisted policy, inventory, selector, transaction and publication feed SQLite reopen, immutable original event payloads and selected-model execution. Two source-backed offerings share canonical identity with different providers/tiers/spellings; only OpenRouter is confirmed. Persistence and no-advance policy-race failures leave catalog/evidence/events unchanged. A workflow edit defers only the stale proposal's first event; unrelated discovery persists, then the next cycle freezes the actual original advancement without re-probing unchanged proof.

Ordered self-review:
1. Spec compliance: scope is the remaining P3T6 field-producer step. Preserve migration-032 empty-provenance semantics, existing root assembly and all actual inputs; no new API, option, schema, parallel implementation or receiver/release claim.
2. Anti-green-theater: require both offerings after normalization/reopen, not just a fabricated raw response. This exposed an invalid inherited fixture whose recognized native model contradicted its source canonical ID; replace it with an unrecognized alias plus a real persisted classification rule. Independently clear the selector Provider, AdvanceRequest.Evidence and CycleCommit.PolicyRevision feeds, and corrupt DeferDiscovery's canonical ID. Root tests fail at persisted evidence, transaction/advancement, forbidden catalog/event writes and premature frozen payloads respectively. Strengthen the policy-failure assertion to continue to durable-state checks. Restore every mutation; `refresh.go` has no final diff.
3. Code quality: reuse neighboring checking probes, migrated-root lifecycle/HTTP helpers and actual policy service; preserve native spelling, exact evidence enforcement, shared synchronization and legacy confirmation reuse. Format changed Go files; no sqlc/UI generation is needed.

Fresh post-review verification (all exit 0):
- `go test ./domain/modelcatalog ./domain/provider -run 'Refresh|Candidate|Follow|Loop' -count=1`
- `go test ./domain/modelcatalog -count=1`
- `go test ./cmd/arbitron -run '^(TestRefreshProducerFieldsPersistOriginalObservationRoot|TestProspectiveBootstrap.*|TestDiscoveryAllSourceCandidatesOnEveryPathRoot|TestRefreshRepairsPublicationWithoutRestart|TestAutomaticAdvanceUsesConfiguredCapabilities|TestRefreshAdvancesBySuccessfulFourHourTick|TestModelRefreshBudgetRestartsReachTailExecution|TestModelRefreshRootInteractiveChatRouteProvesCandidateFromStream)$' -count=3 -timeout=5m`
- `go test ./adapter/sqlite ./adapter/probe ./adapter/provider -run 'Catalog|Cycle|Usability|Advance|Inventory' -count=1 -timeout=5m`
- `go test -race -p 1 ./domain/modelcatalog ./cmd/arbitron -run 'TestSelectorEvidenceUsesCheckedOffering|TestRefreshProducesPolicyAndCompletePathEvidence|TestRefreshRetainsCapturedPolicy|TestRefreshProducerFieldsPersistOriginalObservationRoot' -count=1 -timeout=5m`
- `go vet ./domain/modelcatalog ./cmd/arbitron`
- `git diff --check`

The full Go/dashboard/build release gate remains with P4T4/P4T3; no full-suite waiver or paid model invocation is claimed. Commit only `domain/modelcatalog/candidates.go`, `domain/modelcatalog/producer_fields_test.go`, `cmd/arbitron/model_refresh_producer_fields_test.go` and this task file with a focused `feat:` commit.
