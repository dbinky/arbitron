# Latest Model Profiles — Phase 2 Task 3: Atomic advancement journal and confirmed ordered publication Implementation Plan

> **Alignment baseline (2026-10-19):** D1–D11 govern this task. D10 permits restricted Codex probes; D11 assigns feature-induced suite repairs to P4T4. Earlier execution evidence is historical, not a current dependency inventory or permission to skip unfinished work. The current follow-through sections supersede earlier interface sketches. The existing probe/router (`adapter/probe/model_usability.go`), discovery/selection/recovery (`domain/modelcatalog/{refresh,candidate_source,probe_schedule,loop}.go`) and root assembly (`cmd/arbitron/model_refresh.go:newModelRefresh`) are extension points. Implement unchecked local obligations without waiting for downstream integration acceptance; P3T7 owns cross-phase execution sinks and P4T3 owns receiver/release sinks. See the Phase 3 design’s current closure map.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this task. Track the checkboxes and use strict red/green/refactor TDD.

**Goal:** Atomic advancement journal and confirmed ordered publication.

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

**Depends on:** P2T1 (`docs/plans/2026-09-29-latest-model-profiles-implementation-phase-2-task-1.md`); P2T2 service validation and P3T3 ExecutionGate (both required before implementation).

**Files:** Create `adapter/sqlite/profile_advance.go`, `profile_advance_test.go`, `domain/provider/advance_service.go`, tests. Modify `domain/provider/profile_store.go`, `domain/provider/profile_service.go`, `adapter/sqlite/profile_repo.go`, queries/generated conversions, `adapter/provider/cache.go`, its tests, `cmd/arbitron/main.go`; create `cmd/arbitron/profile_advance_wiring_test.go`.

## Interfaces (owned here unless explicitly consumed)

Implement P2T1 `AdvanceStore.CommitAdvance`. Reuse the adapter-local existing `commitAdvanceTx(context.Context,*sql.Tx,provider.Profile,provider.AdvanceRequest) (provider.Advancement,error)` (normalized expected profile, request) for reuse by P3T5; never expose SQL transactions in domain. `ProfileService.ValidateAdvance(context.Context,AdvanceRequest) error` checks following target/version/capabilities. `AdvanceModel(context.Context,AdvanceRequest) (Advancement,error)` commits then publishes; on publication failure returns committed Advancement plus `ErrPublicationPending`. `ProfileCache.Reload` serializes entire read/build/swap. `PublishCommitted(context.Context,[]Advancement) error` reloads and verifies latest authoritative revisions are present (deleted/superseded rows reconciled, not overwritten).

Preserve the existing `AdvanceStore.ReplayAdvance(context.Context,AdvanceRequest) (Advancement,bool,error)` and `AdvanceRequest.Evidence []provider.PathEvidenceRef` from P2T1's current contract. `ProfileRepo.ReplayAdvance` is the existing producer; replay checks request identity against the original journal and repairs publication without another CAS, even if current discovery is unavailable. New commits require a nonempty complete list of actual consuming `(PathKey,ConfigRevision)` pairs and exact candidate-model confirmation. P3T6 supplies the list, P3T5 checks it against stored/cycle evidence, and this task's direct path uses the same validation. Add missing/wrong path, stale config, wrong model and duplicate/replayed-ID controls to the existing SQL/service tests; keep empty Evidence from passing through a fake that checks only PathRevision. P3T7 drives valid publication through new-run execution.

## Data producers and composition-root wiring

Direct AdvanceModel acquires the P3T3 gate via PathRevision before committing (mandatory injection at P3T7; tests supply a checking lease). Expected Profile/revision comes from ListInternal, selected Model/CanonicalID and PathRevision from P3T6 under P3T3 lease; current SQL row is the CAS authority. Migration 021 journal persists actual changes. Wire direct cache publication into ProfileService at initDomain (do not use loggingReloader that swallows failures for automatic changes). P3T5 reuses transaction helper; P3T6 retries publication from persisted journal before reporting success.

## Strict-TDD steps

- [x] Write the following named behavioral tests in the listed test files. Use real migrated SQLite for persistence and root assembly for wiring; introduce only I/O-leaf test substitutions.

> Decisions D1–D7 are resolved in `docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Follow the concrete contracts in this task and implement its named prerequisites first; unchecked work remains unimplemented.

- Happy: `TestAdvanceCASAndJournal` replaces X with Y, revision+1, exactly one journal row and cache snapshot Y.
- Success: `TestAdvanceWiringPublishesNewRun` drives actual stack service and new trigger to final provider model Y; old run stays X.
- Failure: pinned/target/config mismatch or stale revision returns conflict with no row/journal changes; invalid candidate rejected before SQL.
- Error: journal write failure rolls back model; reload fault returns committed record+pending status and repair publishes Y without another journal entry.
- Edge: ABA, concurrent admin patch, duplicate request, reverse reload completion and deletion/supersession during repair.
- Input guard: nil publication dependency fails startup; fake store checks every expected CAS field. Sink assertion checks new-run request body/argv, not merely `Reload` called.

Assertion sketch for the named happy path (variables are observed fixture outputs; substitute fixture-backed versions where shown):

```go
assert.Equal(t, "version-Y", executed.Model)
assert.Len(t, journalRows, 1)
```

- [x] Run the focused gate below before implementation. Confirm each new assertion fails for the intended absent behavior (a compile failure may establish a missing API, but rerun to observe behavioral red after the API exists).
- [x] Implement the exact contracts above in the listed files:

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

CAS compares revision and complete expected selection/target/model/kind/connection/backend/axes/tools; updates only Model+revision and journal in one transaction. Request ID replay returns original journal change without new revision. Admin full/patch edits use revision-aware conditional update so a read-then-write cannot erase concurrent advancement; conflict is explicit. Serialize Load/Reload across admin, startup and automatic callers. Do not retry CAS as a second advancement after a successful durable commit.

- [x] Run the focused gate again; require exit 0 and every named test green. Refactor only while those tests remain green; format Go and regenerate sqlc for query/schema changes. Run dependent root tests when an interface changes.

**Focused gate:** `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run "Advance|Publication|Reload" -count=1`. Commands separated by semicolons here are separate gates: run each and inspect every exit status. For dashboard changes run `make ui` from the repository root so embedded assets match source.

- [x] Review diff for scope, production wiring and forbidden fake shortcuts. Commit this task with `git add <listed changed files>` and a focused `feat:` commit only after verification; do not stage unrelated changes.

## Independent optimistic admin-mutation slice (2026-10-16)

Selection: skip P1T4's unchanged Azure/Bedrock evidence blocker and P2T2's already-recorded validated-store/runtime blocker. Complete only P2T3's independent ProfileMutationStore/admin-write obligation, not its full-task gates.

- [x] Write SQLite expected-field/advancement/ABA/fault tests, service missing-port/following-tools race tests, and real-root full/patch/delete stale-read tests with durable rows, exact HTTP model/auth/prompt inputs, node output and old/new run pins as sinks.
- [x] Run tests red: missing repository API first fails compilation; temporary unconditional API bodies then accept every stale expected field, advancement and ABA. Actual initDomain admin full/patch/delete calls also accept stale writes. Service tests accept missing conditional ports and erase the competing following-model change. Remove temporary bodies before implementation.
- [x] Implement ProfileMutationStore on the existing SQLite ProfileRepo. Compare every expected field plus revision in conditional SQL; return the committed updated row/revision from the same transaction. Route actual admin full/patch/delete operations through the conditional port under the existing shared mutation gate; missing port fails closed, conflicts never reload. Preserve low-level CRUD signatures, authorization, capability checks and best-effort recache after successful writes.
- [x] Run the exact task focused gate plus explicit new tests, full affected provider/MCP/root packages, focused SQLite profile tests and race gates after review.
- [x] Complete ordered spec-compliance, anti-green-theater and code-quality review, rerun gates and commit only this slice plus plan evidence.

Review and verification evidence:

1. Spec-compliance first: only conditional admin full/patch/delete writes and their required port; no selection runtime, policy/probe, option, migration or publication-recovery feature was added. The existing initDomain constructs ProfileService with the actual ProfileRepo and shared gate, so the new port is constructed and fed real persisted rows/submitted patches without alternate wiring. Full P2T3 remains unchecked for the contracts above.
2. Anti-green-theater second: root scheduling waits for the real service to block at its mutation gate after reading, then a second independently opened SQLite connection writes Y. Conflict must preserve the entire Y row and avoid cache reload; a fresh retry publishes Y, then a real trigger executes Y at the HTTP/node-output sink while the earlier run retains X pins. HTTP refuses missing model/auth/route/nonempty prompt. Temporarily replace only service conditional calls with unconditional CRUD: full/patch fail durable-row, cache, new pin, HTTP-model and node-output assertions; delete fails because the row disappears. Restore conditional wiring before final gates. SQLite tests separately race actual CommitAdvance against stale admin expected rows and require exactly one unchanged journal entry. Checking service/MCP stores compare every expected field instead of ignoring the precondition.
3. Code-quality third: follow adjacent repository/service conventions; reuse NormalizeSelection, tools/axis conversion, existing generated GetProfile and row decoder. SQL conditions include every selection/configuration field and revision; update/read/return share one transaction, with rollback on errors. Preserve authorization, sparse semantics and low-level CRUD; update the affected checking test stores instead of adding a production fallback. Format all Go. No schema or sqlc query inputs/generated conversions changed; new conditional SQL is handwritten like the existing advancement helper.

Post-review gates, all exit 0:
- `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1`
- `go test ./domain/provider ./adapter/sqlite ./cmd/arbitron -run ProfileMutation -count=1`
- `go test ./domain/provider ./adapter/provider ./adapter/mcp ./cmd/arbitron -count=1`
- `go test ./adapter/sqlite -run 'Profile|Advance' -count=1`
- `go test -race ./domain/provider ./adapter/provider ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run 'ProfileMutation|Advance|Publication|Reload' -count=1`
- `git diff --check`

The temporary unconditional stubs/mutation are absent from the final diff. The whole SQLite suite's previously recorded new-instance pin-fixture failures are outside this slice; the focused profile/advancement gate passes. No full-task completion, durable candidate selection or direct-publication recovery claim is made.

## Independent validated-selection transaction slice (2026-10-16)

Selection: skip P1T4's unchanged Azure/Bedrock evidence blocker and P2T2's recorded missing validated-store prerequisite. Implement this task's owned SQL port using the existing PreparedSelection contract. P2T2 owns prepare/revalidate and service/seed consumption; P3T6/P3T7 own the real candidate producer and final root execution acceptance. Do not count repository tests as those sinks.

- [x] Write `TestValidatedSelectionRejectsExternalRevisionChange` and success/malformed-proof/expected-row/fault tests against migrated SQLite, with a separately opened database connection changing persistent policy/connection revisions between preparation and commit. Assert durable rows/revisions, rollback and reopen, not intermediate calls.
- [x] Run tests red on missing APIs, then again with temporary API bodies to observe behavioral failures before implementation. The combined task/selection gate fails compilation for the missing port/methods; CRUD-backed temporary bodies then accept stale policy/credential/override revisions and absent/contradictory proofs, and fail create readback rollback. Remove these temporary bodies in the implementation.
- [x] Add SelectionMutationStore and implement CreateValidated/UpdateValidated on the existing ProfileRepo, sharing conditional SQL and transaction-local revision checks. Preserve low-level CRUD and metadata-only mutation behavior.
- [x] Run the exact task gate, explicit selection tests, affected package tests and race verification.
- [x] Complete spec-compliance, anti-green-theater and code-quality reviews in order, rerun gates and commit this verified slice only.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Ordered self-review and verification evidence:

1. Spec first: only the owned SelectionMutationStore contract and ProfileRepo transactional implementation. Following writes require exact model/canonical/path/token proof and the prospective connection, and validate every token policy/connection revision before writing. Pinned writes do not depend on follow policy. Create owns revision 1; updates reuse the existing complete expected-row CAS. No source runtime, discovery, service orchestration, migration, flag or publication recovery was added. P2T2 can now consume this port; P3T7 still owns real candidate/root injection. Full-task steps remain unchecked, including end-to-end wiring/feed acceptance.
2. Anti-green-theater second: tests read real persisted revisions, rotate a credential via a separately opened ConnectionRepo, bump policy/override revisions through the second SQLite connection, and assert durable rows/revisions and DB reopen. Missing/contradictory proof, omitted prospective routes, all expected fields, duplicate aliases, deletion, ABA, real CommitAdvance races, write/readback/revision faults and policy-independent pinned writes are covered. Temporarily remove only checkSelectionTx from the write path: all external-revision cases fail both error and durable-row/model/revision sinks. Restore the check before verification. These are repository sinks, not a claim of service/seed/runtime integration; the complete prepared input is shared by supplied-start and catalog-filled creation, whose upstream preparation remains P2T2-owned.
3. Quality third: factor the existing generated insert/read and handwritten conditional update for transaction-local reuse rather than duplicate SQL or change applied migrations. Readback failure rolls back the mutation; callers receive the authoritative row only after commit. Keep errors free of credentials/probe output. Follow adjacent repository/test conventions, isolate each file-backed DB, avoid mutable table-test statements, gofmt and rerun all gates.

Post-review gates (all exit 0):
- `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1`
- `go test ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection' -count=1`
- `go test ./domain/provider ./adapter/provider ./adapter/mcp ./cmd/arbitron -count=1`
- `go test -race -p 1 ./domain/provider ./adapter/provider ./adapter/sqlite ./adapter/mcp ./cmd/arbitron -run 'ValidatedSelection|ProfileMutation|Advance|Publication|Reload' -count=1 -timeout=8m`
- `git diff --check`

The final race run is serialized and passes (SQLite: 192 seconds). Earlier harness timeouts at 120/240 seconds and an intentionally shorter diagnostic test timeout interrupted slow race-instrumented migrations; the diagnostic stack was inside RunMigrations, not blocked on a selection lease. No assertions or production behavior were weakened to pass.

No schema/sqlc-input/UI changes require regeneration. The previously recorded full SQLite instance/queue pin-fixture failures are outside this slice. No temporary CRUD-backed API bodies or disabled validation remain.

## Independent direct-advance capability slice (2026-10-16)

Selection: skip P1T4's unchanged Azure/Bedrock proof blocker and P2T2's recorded candidate/seed runtime blocker. Complete P2T3's independently executable candidate capability check in the existing direct AdvanceModel path only.

- [x] Write `TestAdvanceModelValidatesCandidateCapabilities` and `TestAdvanceWiringRejectsIncompatibleCandidateCapabilities`, covering model-specific context/effort rejection, invalid axes, unknown backend, compatible replacement, real migrated SQLite, root-loaded capability overrides, persisted rows/revisions/journal/acks, cache and final HTTP/node-output/new-run pin sinks.
- [x] Run the exact task gate red, then correct the root fixture's unsupported YAML axes fields and rerun the root case to behavioral red: incompatible Y commits/publishes/acknowledges, replaces X and breaks the next real run. Unit cases also reach CAS/publication instead of rejecting before the gate.
- [x] Implement replacement-model capability validation before gate/SQL in the existing production service, reusing the same axis validation as admin writes and preserving requested configuration/flooring semantics.
- [x] Run exact task gate, affected package tests and focused race tests after ordered review.
- [x] Review spec compliance, anti-green-theater and code quality in that order; rerun gates and commit only this verified slice.

> Historical execution record (2026-10-16), superseded by the current ownership and acceptance requirements below. This is not a prerequisite gate; preserve unverified acceptance as unchecked work in its owning task.

Ordered review and verification evidence for the direct capability slice:

1. Spec first: only the required candidate capability rejection in the existing direct AdvanceModel path. Reuse ProfileService.validateAxes on the proposed model, before taking the lease or committing. Do not add an option, route guess, discovery source, partial public ValidateAdvance API or runtime. Keep existing auth, pin/CAS/publication behavior and effort flooring. The real initDomain already constructs this service with loaded capability rules, SQLite store, shared gate, cache publisher and journal; the test feeds that exact production composition persisted profiles and inventory tokens.
2. Anti-green-theater second: the real-root test loads a non-empty capability override file, real connection/model/auth/prompt data and a persisted working configuration. Initial X succeeds; incompatible Y leaves the entire row/revision, cache, journal and acknowledgements unchanged; a new trigger still pins/executes X; compatible Z commits/publishes/acknowledges exactly once and reaches HTTP plus durable output/new-run pins, while the old run retains X. Without the guard the test reproduced durable Y publication and a failed next execution. Removing rules/model/axes inputs cannot satisfy both negative and positive controls. Initial following-row axes are fixture setup through the actual conditional repository because ProfileConfig has no axes fields; an unchanged admin patch publishes them. This is not a claim of missing cold candidate initialization or YAML-axis support. Unit success checks the complete unmodified AdvanceRequest at CAS, and negative tests assert no gate/SQL/publication.
3. Quality third: follow the existing shared axis parser/capability rules and owned profile clone; no duplicate capability rules or write path. Update the old direct-service fixture to provide real rules/kind/backend instead of empty policy. Add a compatible floored-effort control proving requested axes are not rewritten. Format Go; no schema/query/UI changes require regeneration.

Post-review gates (all exit 0): exact task focused gate; full `go test ./domain/provider ./adapter/provider ./adapter/mcp ./cmd/arbitron -count=1`; `go test ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection' -count=1`; `go test -race -p 1 ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1 -timeout=8m`; `git diff --check`. Full task completion remains unclaimed for the blocker above.

## Provenance / release gate (resolved 2026-10-01)

None.

## Store and publication seams owned here

Keep existing ProfileStore CRUD signatures for compatibility, but add a separate `ProfileMutationStore.UpdateExpected(context.Context,Profile,Profile) (Profile,error)` (expected, replacement) and `DeleteExpected(context.Context,Profile) error`, implemented by ProfileRepo. ProfileService.Update/UpdatePatch/Delete capture authoritative prior Profile and use this port under the P3T3 mutation lease; full replacement has the same optimistic check as sparse patches. A conflict returns ErrAdvanceConflict and never overwrites an automatic change. P2T1's unconditional Update remains a low-level compatibility path, never used by the production admin service after this task. Test a real admin sparse patch racing an advancement whose Model changes while the admin patch edits only tools.

Produce `provider.AdvancementPublisher.PublishCommitted(context.Context,[]Advancement) error`, implemented by ProfileCache, and existing `ProfileService.SetAdvancementDependencies(AdvanceStore,AdvancementPublisher,*ExecutionGate)`. P3T7 injects all three, separate from the existing best-effort Reloader. Implement published revision acknowledgement via P3T5 CatalogStore.MarkPublished; after reload failure, startup/next refresh reloads durable changes and verifies the new-run sink before marking them published. Direct AdvanceModel and CommitCycle share one transaction helper, expected-field validation and path-revision checks. No nil gate/publisher may turn a durable change into reported full success.

P2T1 extends AdvanceRequest and EligibleCandidate with the existing ExecutionRevision; P3T3 produces the token values; use that exact type rather than a second path/config struct. Test stale durable policy/connection revisions in a second DB connection while in-process fakes still report the old green digest. CommitCycle validation must reject before any CAS/journal/event writes. The SQL adapter owns persistent revision validation, while the root-wired gate owns external file/account/binary fingerprint checks.

## Validated selection transaction and shared helper ownership

Extend the existing ProfileRepo rather than duplicate its advancement implementation. Its commitAdvanceTx currently takes `(ctx, tx, normalizedExpected, request)`; CommitCycle must use that exact signature. `ProfileService.ValidateAdvance` remains owned by this task: check selection shape, fixed requested version and existing capability rules before lease/SQL. P3T6 owns same-target identity, strictly-newer ordering and all-path eligibility; it selects from the merged evidence and does not invoke fresh discovery inside the lease.

Add `SelectionMutationStore.CreateValidated(context.Context,PreparedSelection) (Profile,error)` and `UpdateValidated(context.Context,Profile,PreparedSelection) (Profile,error)` on ProfileRepo (expected row for update). These reuse conditional profile SQL and check the prepared ExecutionRevision's policy/connection revisions inside the same transaction before persistence. A following prepared selection without candidate/model/canonical ID/token is rejected. Candidate.Model must equal PreparedSelection.Profile.Model. Service and seed use these methods for selections that need proof, after source.RevalidateStart under the shared mutation lease; metadata-only UpdateExpected remains conditional. Pinned prepared selections require prior fixed-version validation but no following-tier proof. Production code never falls back to unconditional CRUD if this port is absent.

Add red/green SQLite tests `TestValidatedSelectionRejectsExternalRevisionChange` using a second DB connection between preparation and commit, including supplied-start and cold-create cases. Check zero rows/reloads on stale proof, exact selected model on success, rollback on repository error, and retained existing profiles on seed failure. P3T7 injects the actual ProfileRepo port/source into both seed and admin paths. CatalogStore.MarkPublished is owned later by P3T5; this task tests the journal/publication port without taking a reverse implementation dependency on that store or P1T4 root. TestAdvanceWiringPublishesNewRun's old/new-run sink is executed at P3T7 once those runtimes exist.

## Direct-advance publication recovery contract

A direct AdvanceModel journal row may have no RefreshReport; report-only Recover cannot repair that committed row. Define `provider.PublicationJournal.Unpublished(context.Context) ([]Advancement,error)` and `Acknowledge(context.Context,[]string) error` (advancement IDs), plus `ProfileService.SetPublicationJournal(PublicationJournal)` here. P3T5 supplies the actual SQLite implementation in migration 022; P3T7 injects it. Require the port before a production direct advancement, and return committed Advancement + ErrPublicationPending on reload or acknowledgement failure. Do not retry the CAS or discard the original journal result.

P2T3 unit tests use a checking journal port, leaving the later schema/root implementation to P3T5/P3T7. Add `TestDirectAdvancePublicationRecoveryWithoutReport`: commit Y, fail publication/ack, reopen with no report row, then drive actual startup/next refresh recovery to a new-run Y sink and one journal entry. That root acceptance test lands in P3T7; no dependency on a nonexistent report or a reverse P3T5 prerequisite is introduced here.

## Capability validation consumed by automatic selection

Produce the already planned `ProfileService.ValidateAdvance(context.Context,AdvanceRequest) error` in `domain/provider/advance_service.go`, without SQL, network, gate acquisition or mutation. Validate expected following shape, requested fixed version using the injected route-only VersionRequests/version resolver, unchanged target/execution configuration and candidate capability axes using the same configured CapabilityRules as admin writes. Never derive routing provider from FollowTarget. D8 native paths reject following advancement. Reuse this method inside direct AdvanceModel and expose it as the narrow `AdvanceValidator` port consumed by P3T6; P3T7 injects the actual ProfileService.

- [x] Write red happy/success/failure/error/edge tests proving ValidateAdvance accepts a compatible newer fixed model, rejects model-specific context/effort incompatibility, unsupported route/D8/missing validator inputs and preserves requested axes/flooring. No SQL/journal/publication may occur on rejection.
- [x] Implement the validator, run existing Advance/Publication/Reload tests plus validator cases green. Direct tests alone do not certify the automatic pipeline: P3T6 invokes this port before including each proposal, and P3T7 loads a real capability override file, refreshes incompatible Y then compatible Z and asserts durable row/journal/cache and final new-run model X then Z. Dropping the validator injection must fail that sink test.

## Read-only advancement validator slice (2026-10-18)

Selection: P2T2's recorded cold-bootstrap blocker is unchanged: CandidateDeps still lacks a prospective pipeline/coordinator, Collect has no prospective input, and candidate reads start from the stored catalog. Do not repeat that failed acceptance. Complete this task's independent ValidateAdvance producer; automatic proposal consumption/root acceptance remain explicitly owned by P3T6/P3T7, not prerequisites for this local producer (D9). Original full-task acceptance checkboxes above remain unchecked; this pass does not certify them.

- [x] Write and run TestValidateAdvance and TestValidateAdvanceNeedsNoMutationPorts red: missing API fails compilation, then a temporary no-op API fails behavioral assertions for malformed selection, capabilities, missing ports, route/version faults, unsupported/mismatched routes, all-route D8 rejection, empty evidence and cancellation. Extend the actual-root capability test: a mutable replacement commits/acknowledges, changes the durable row/cache and prevents the next trigger. Remove the no-op API during implementation.
- [x] Implement provider.AdvanceValidator and ProfileService.ValidateAdvance using existing NormalizeSelection, validateAxes and route-only checkRouteVersions/ValidatePinRoutes. Clone and replace only Model; preserve target/configuration/requested axes/tools. No discovery, probes, lease or mutation in the validator. Direct AdvanceModel calls it for new writes; retain original committed-result replay before new-write validation so an unavailable source cannot prevent publication repair.
- [x] Feed the production validator through the existing initDomain ProfileService and its actual CandidateService -> ExecutionPathInventory route requests, fixed-version resolver and configured capability file. Real SQLite/root tests assert rejected mutable/incompatible candidates leave row/revision/cache/journal/acks unchanged, compatible Z reaches HTTP/output/new-run pins, and old pins retain X. SQLite replay coverage now uses supported fixed IDs plus a complete-profile-checking route port, then removes both selection ports after commit and verifies repair still publishes the original result exactly once.
- [x] Review spec first: only the owned validator/port and direct consumption, no automatic runtime implementation, flag, schema or alternate selection path. Repair only fixtures needed by the task gate to supply mandatory routes/fixed IDs; do not weaken production validation. The original replay read remains intentional; validation itself does no SQL and rejected new writes never reach CAS/journal/publication.
- [x] Review anti-green-theater second: temporarily bypass route validation, observe the real-root durable model/revision/cache/journal/ack and next-trigger sinks fail. Restore it. Temporarily remove initDomain's selection-port injection; observe compatible Z cannot persist and final new-run pins/output remain X/fail. Strengthen the success control to continue to durable/executed-model assertions rather than stop at its error assertion. Restore injection. No mutation remains in main.go.
- [x] Review quality third: reuse existing shared helpers/errors, clone input ownership, checking ports reject lost prospective configuration and version-request fields, keep API internal, gofmt. Run post-review task and dependent/race gates and commit this slice only.

Fresh post-review gates (exit 0):
- `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1`
- `go test ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection' -count=1`
- `go test ./cmd/arbitron -count=1`
- `go test -race -p 1 ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1 -timeout=8m`
- `go test ./domain/modelcatalog -count=1` (also passed in the combined broader invocation)
- `git diff --check`

Broader concern: a subsequent full adapter/provider run fails its existing TestPaired_ServiceWrite_RecachesResolver fixture because it creates profiles without the now-required model-selection ports. Its failure is outside the task's passing Advance/Publication/Reload gate and this validator slice; no fallback was added. No schema/query/UI changes require regeneration. Automatic incompatible-Y/compatible-Z refresh, mandatory validator construction/injection and final timer/manual sinks remain unchecked in P3T6/P3T7; direct tests are not substituted for those acceptance tests.

## Confirmed publication completion (2026-10-18)

Selection: P2T2 remains blocked only on the already-recorded P3T6 cold prospective discovery producer (CandidateDeps/Collect/coordinator). Do not repeat that blocked acceptance. Finish P2T3's independent publication obligations, retaining all existing SQL CAS, conditional admin mutation, validated selection and capability work. Initial worktree changes in the publication files belong to this slice; inspect and verify them rather than discard them.

- [x] Tests: `TestAdvanceCASAndJournal` now checks the real migrated repository and entire authoritative cache row before/after publication. `TestAdvanceWiringPublishesNewRun` drives initDomain, SQLite, real inventory tokens, the engine and Claude subprocess adapter: an active run blocks at its first X call, publication occurs, its second call still executes X, and both calls of the new run execute Y. Require nonempty exact prompt/model/format inputs, no fallback flag, durable outputs and unchanged original pins. Existing SQL/service tests cover expected-field conflicts, ABA/admin races, duplicate request, journal rollback, capability/version rejection and acknowledgement faults.
- [x] Add deletion/batch/cache-error reconciliation controls and real-root `TestPublicationWiringRequiresDependencies` / `TestDirectAdvancePublicationRecoveryWithoutReport`. The latter injects only a database-leaf reload fault after durable commit, verifies repeated pending status and the original journal result, then repairs by direct replay or real close/reopen/startup. Cover unchanged/deleted/superseded/deleted-and-recreated aliases; assert exact HTTP model/auth/prompt, durable new-run output/pins, original journal count/ack, no report dependency and no alias resurrection.
- [x] Red: temporarily restore the two production files' pre-change behavior, leaving all tests intact. Run the exact task gate: missing publisher/gate incorrectly permits startup; deletion repair remains pending with no acknowledgement both on retry and actual startup. Restore only the minimal implementation after observing these behavioral failures.
- [x] Implementation: after a successful authoritative cache reload, absence reconciles a deleted alias instead of rejecting it or restoring the original model. Continue validating every other batch member. Fail construction at newModelRefresh when publisher or shared gate is missing. Existing initDomain feeds these paths with real ProfileRepo/cache/gate/catalog journal, not a placeholder; best-effort admin recache remains separate.
- [x] Review stage 1 — spec compliance: only this task's confirmed publication/startup guard and tests; no automatic refresh/cold-bootstrap producer, option, migration or UI changes. Keep next-cycle timer/manual recovery and automatic validator consumption in P3T6/P3T7. Existing SQL helpers/interfaces already implement the task's remaining contracts; no duplicate write path or generation is needed. Strengthen the direct root success test from completed-pin inspection to actual active X continuation and new Y output.
- [x] Review stage 2 — anti-green-theater: temporarily omit AdvanceModel's publisher call; the real root fails at new-run pins, both persisted node outputs and both harness argv models (X instead of Y). Restore it. Temporarily omit startup Recover; every reopened case retains the original unacknowledged direct journal row. Restore it. Restore the old missing-alias rejection independently; the explicit cache reconciliation test fails. No disabled production checks remain. Fix a test race exposed during this review: assert zero reports before starting recovery, then check no report references the original direct journal row; an unrelated failed startup cycle is allowed to record its own report concurrently.
- [x] Review stage 3 — quality: inspect neighboring cache/root tests, preserve whole read/build/swap serialization and authoritative revisions, use existing bounded sink wait helpers and release the harness before stack cleanup even on assertion failure. Add missing kind/provider CAS field controls against real SQLite. Reuse existing errors/ports; no dead code, new flag or schema/query/UI change. Format and run final gates before committing only this task's changed files.

Fresh post-review verification (all exit 0):
- `go test ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload' -count=1`
- `go test ./adapter/provider -run 'PublishCommitted|ConcurrentLoadsCannotRegressSnapshot' -count=1` (the task regex does not match PublishCommitted)
- `go test ./adapter/sqlite -run 'Profile|Advance|ValidatedSelection' -count=1`
- `go test ./cmd/arbitron -count=1`
- `go test -race -p 1 ./domain/provider ./adapter/provider ./adapter/sqlite ./cmd/arbitron -run 'Advance|Publication|Reload|PublishCommitted|ConcurrentLoadsCannotRegressSnapshot' -count=1 -timeout=8m`
- `git diff --check`

Broader concern, freshly reproduced: `go test ./domain/provider ./adapter/provider ./adapter/mcp -count=1` remains red in existing admin/patch/axes/auth/schema fixtures that omit the now-mandatory selection ports, including TestProfileServiceMutationsWaitOnGate, TestPaired_ServiceWrite_RecachesResolver and ProfileAuthzE2E/ProfileTools cases. No domain/provider or MCP production changes are part of this pass, and no compatibility bypass is introduced. These failures are outside the passing task gate; full-release fixture refinement remains owned by P3T7/P4T3. All P2T3 steps are now complete; this does not certify cold prospective discovery, per-cycle publication recovery, automatic validator consumption or timer/manual acceptance owned by P3T6/P3T7.
