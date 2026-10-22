# Focus Areas — Workflow Auto-Arrangement Review Plan

Each area is one **behaviour** this branch promises, traced from its entry point to
its sink across every layer in between. Each needs **two approvals**: Review 1 uses
the wiring lens (does the path exist, and is it fed?) and Review 2 uses the failure
lens (how does it fail?). Pass criteria are in `RALPH.md` under "What every review
actually checks". Seams between areas live in `docs/reference/paired-reviews.md`.

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`. Designs:
`docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-{1,2,3}.md`. Binding
signatures: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md`.

The composition root for every area is `cmd/arbitron/main.go:initDomain` plus
`cmd/arbitron/layout_wiring.go:newLayoutStack`. Both `serve` and stdio `mcp` boot
through `initDomain` (`runServe`, `runMCP`).

Budget: **2 × 12 + 19 = 43** minimum passes; **500 × 0.35 = 175** allowed; **43 ≤ 175**.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | A crashed or failed save is settled before anything reads it: proven commits roll forward, uncommitted trees are restored, no reader sees staging | high | [x] | [x] |
| 2 | A Create or Update commits a server-stamped revision and durably requests arrangement exactly once, without waiting for it | high | [x] | [x] |
| 3 | Publish, Copy and Duplicate commit a fresh destination revision with exact publication proof, and restore the prior tree when publication fails | high | [x] | [x] |
| 4 | Rename, ownership transfer, delete and unpublish move or keep revision history without losing a revision or inventing one | high | [x] | [x] |
| 5 | A workflow or instance layout read returns that revision's own result, its fallback or an explicit unavailability, and only to an authorised caller | high | [x] | [x] |
| 6 | A layout retry from HTTP or MCP starts a new generation for exactly the expected revision, under the same authorisation and lock as a save | high | [x] | [x] |
| 7 | Every committed revision reaches a truthful terminal record within budget and publishes only to its current revision, generation and destination | high | [x] | [x] |
| 8 | Each candidate is drawn by real Chrome through the shared renderer and reviewed from the actual DOM and pixels; missing evidence never passes | high | [x] | [x] |
| 9 | Newly seeded samples arrange exactly once and resume idempotently, while backfill, flat-store migration and boot never arrange | med | [x] | [x] |
| 10 | A saved manifest arranges into continuous downward flow with branches beside it and distinct loop channels, changing presentation only | med | [x] | [x] |
| 11 | Node and branch text is measured by the real renderer with the bundled fonts, and that measurement sizes the boxes it is drawn in | med | [x] | [x] |
| 12 | Both dashboard views draw the revision-joined layout with truthful status, poll until terminal, offer Retry, and never write node positions | med | [x] | [x] |

---

### Area 1 — A crashed or failed save is settled before anything reads it: proven commits roll forward, uncommitted trees are restored, no reader sees staging

- **Promise:** A layout or process failure never loses a saved workflow. Recovery replays only CommitSource-proven input, restores every prior byte of an uncommitted shared replacement, and every read waits for settlement (design phase 2 §2, §4; contracts §6.2, §6.10, §6.16).
- **Entry:** `cmd/arbitron/main.go:initDomain` (boot settlement through `ls.commits.CommittedRequests` before seeding). The per-read entry is `adapter/filesystem/workflow_operations.go:WorkflowRepo.Load` through the read settler.
- **Sink:** `adapter/filesystem/tree_commit.go:WorkflowRepo.RollbackTreeCommit` (restored tree bytes), `adapter/filesystem/revision_journal.go:RevisionJournal.Quarantine` / `MarkCommitted` (journal decision), and `workflow.ErrMutationPending` returned to the reader.
- **Files:** changed: `domain/workflow/commit_source.go`, `domain/workflow/arrangement.go`, `adapter/filesystem/revision_journal.go`, `adapter/filesystem/tree_commit.go`, `adapter/filesystem/manifest_lock.go`, `adapter/filesystem/durable.go`, `adapter/filesystem/workflow_operations.go`, `adapter/filesystem/workflow_repo.go`, `adapter/sqlite/published_workflow_repo.go`, `cmd/arbitron/layout_wiring.go`, `cmd/arbitron/main.go`. Unchanged but crossed: `adapter/filesystem/file_reader.go` (`FileReaderAdapter.ReadFile`), `adapter/http/workflow_handler.go` (raw file routes), `adapter/mcp/tools_workflow.go` (`workflow_read_file`). Tests: `adapter/filesystem/commit_crash_test.go`, `revision_rollback_test.go`, `manifest_lock_test.go`, `domain/workflow/commit_source_test.go`, `cmd/arbitron/layout_recovery_test.go`, `layout_repository_boundary_test.go`, `layout_http_recovery_test.go`, `layout_mcp_recovery_test.go`.
- **Folded in:** `adapter/filesystem/durable.go` (fsync helpers): check that every rename is followed by a directory fsync. `cmd/arbitron/main.go:layoutSettlementTimeout` (30s): check that a boot past the timeout fails with its evidence intact. `domain/queue/manager.go:Manager.Stop`: check that it joins notifications before `domainStack.close` closes SQLite.
- **Review 1 — wiring lens:**
  1. `CommitSource` is built once in `newLayoutStack` with the real journal, repo, publications, registry and requester. Confirm that `repo.SetReadSettler(ls.commits)` (`layout_wiring.go:127`) is the only `SetReadSettler` call outside tests.
  2. Boot settlement runs before `seedSampleWorkflows`, `BackfillRegistry` and the first HTTP/MCP traffic. Cite the line order in `initDomain`, and cite `TestLayoutE2E_PreSeedUndecidedSettlementAbortsBoot`.
  3. `CommitSource.Settle` is reached from `WorkflowRepo.Load`, from `RevisionStamper.Stamp` and from `EnsureStamped`. Grep `Settle(` and name each production caller.
  4. Roll-forward decisions use exact publication proof (`RevisionPublicationStore` revision plus WorkflowID), never folder presence. Cite the comparison in `commit_source.go`.
  5. Every raw manifest read surface goes through settlement. `FileReaderAdapter.ReadFile` calls `os.ReadFile` with no lock or settler (see Seed evidence and gap G3). Either name the wiring that settles it, or fail the check.
  6. Name the tests that demonstrate the happy path (commit, then a read sees the committed bytes) and the success path (a killed writer is settled by the next reader): `TestLayoutE2E_KillAndImmediatePeerRead`, `TestLayoutHTTPE2E_ImmediatePeerCrashRead`.
- **Review 2 — failure lens:**
  1. Rollback of a rollback: when `RollbackTreeCommit` fails, the intent is retained and further mutation of that destination is blocked. Name the test that goes red if the intent is dropped.
  2. An undecided registry or I/O error leaves the evidence undecided rather than quarantining it. Show that `CommittedRequests` returns partial results plus an error, and does not hide other committed inputs (design phase 2 §5).
  3. A corrupt or partial journal entry is quarantined and never arranged. Cite `TestLayoutE2E_CorruptReceiptNeverArranges` and `TestLayoutE2E_PreparedInputIsQuarantinedNeverArranged`.
  4. Concurrency: with two processes on one workspace, the `manifest_lock.go` lock set is sorted and cannot expand. Show that a nested `WithMutations` on an un-owned target fails closed, and does not deadlock or read unlocked.
  5. Ordering: a tree backup is removed only after the receipt is durable. Name the test that goes red if `FinishTreeCommit` runs before `MarkCommitted`.
  6. Unresolved relocation returns `ErrMutationPending` on read and on save. Cite `TestLayoutRepository_UnresolvedRelocationFailsClosed`.
- **Seed evidence:**
  - `grep -rn SetReadSettler`: one production call, `cmd/arbitron/layout_wiring.go:127`.
  - `cmd/arbitron/main.go:561-567` calls `ls.commits.CommittedRequests` before the `seedSampleWorkflows` switch at `:575`.
  - `adapter/filesystem/file_reader.go:FileReaderAdapter.ReadFile` (line 88) reads `os.ReadFile(full)` with no settlement. The P3T9 blocker (plan phase-3-task-9 line 1458) reproduced HTTP 200 with an uncommitted revision on `GET /workflows/target/files/workflow.json`.
  - `domain/workflow/commit_source.go:46` is the only production `Journal.Quarantine` caller.

### Area 2 — A Create or Update commits a server-stamped revision and durably requests arrangement exactly once, without waiting for it

- **Promise:** Every create and update path, private, admin-edited shared or local mode, gets a fresh server token (A → B → A and identical saves are distinct). The save returns before arrangement, and the request survives an enqueue outage (spec "Arrange on every save", "Save immediately"; design phase 2 §1–§2).
- **Entry:** `adapter/http/workflow_handler.go:WorkflowHandler.Create` / `Update` and `adapter/mcp/tools_workflow.go:workflowCreateHandler` / `workflowUpdateHandler`, then `domain/workflow/service.go:Service.Create` / `Update`, then `service_revision.go:createRevision` / `updateRevision`.
- **Sink:** `adapter/filesystem/revision_journal.go:RevisionJournal.MarkCommitted` (receipt), then `domain/layout/requester.go:Requester.RequestArrangement`, then `adapter/sqlite/layout_repo.go:LayoutRepo.Request` (`layout_revisions` row, migration `019`).
- **Files:** changed: `domain/workflow/service.go`, `domain/workflow/service_revision.go`, `domain/workflow/revision_stamper.go`, `domain/workflow/presentation.go`, `domain/workflow/workflow.go`, `domain/workflow/arrangement.go`, `domain/workflow/visibility.go`, `domain/layout/requester.go`, `adapter/sqlite/layout_repo.go`, `adapter/sqlite/migrations/019_layout_revisions.up.sql`, `adapter/filesystem/revision_journal.go`, `adapter/filesystem/workflow_repo.go`, `adapter/http/workflow_handler.go`, `adapter/mcp/tools_workflow.go`, `cmd/arbitron/main.go`. Unchanged but crossed: `domain/engine/engine.go:Engine.TriggerWithDepartment` (the snapshot carries `Presentation`). Tests: `domain/workflow/service_revision_test.go`, `revision_stamper_test.go`, `cmd/arbitron/layout_e2e_test.go`, `layout_service_revision_test.go`, `revision_metadata_e2e_test.go`.
- **Folded in:**
  - `domain/workflow/workflow.go:Workflow.Presentation` (new JSON field): check that client-supplied values are discarded (`TestRevisionMetadataE2E_CreateRejectsClientPresentation`).
  - `migrations/019_layout_revisions.down.sql`: check that it reverses up exactly (`adapter/sqlite/layout_migration_test.go`).
  - `RevisionStamper.retained`: check that the fallback keeps surviving nodes only.
  - The `if s.stamper != nil` legacy branches in `service.go:244,284`: check that production can never take them.
- **Review 1 — wiring lens:**
  1. `workflowSvc.SetRevisionStamper(ls.stamper)` and `SetMutationCoordinator(ls.mutations)` (`main.go:594-595`) are the only production setters. Confirm that no production `Service` is constructed without them (grep `NewWorkflowService`).
  2. `StamperDeps.Displayed` is fed by `ls.store` (`LayoutRepo.DisplayedLayout`), not nil. That makes the retained fallback come from the real displayed result.
  3. `Requester` receives `cfg.Layout.MaxAttempts`, and its notifier is `ls.worker.Notify` (`layout_wiring.go:149`).
  4. The HTTP and MCP create/update responses return before the worker runs. Cite `TestLayoutE2E_SaveIsAsyncAndEngineSnapshotRetainsPendingRevision`.
  5. Stdio MCP authoring reaches `Service.Create` with an identity. `TestLayoutE2E_StdioMissingIdentityIsNotBypassed` shows that it does not (gap G6). Fail this check until a stdio identity producer exists.
  6. Local mode (no registry) still stamps and requests. Cite `TestLayoutE2E_ExplicitStandaloneNoRegistrySave`.
- **Review 2 — failure lens:**
  1. Enqueue failure after the receipt: `RevisionStamper.committed` logs the failure and keeps the journal entry, and recovery re-requests it. Cite `TestLayoutE2E_LiveEnqueueOutageRecoversByPoll`.
  2. A `MarkCommitted` failure leaves the prepared entry, and settlement then decides it. Show that it never silently returns success without either a receipt or a rollback.
  3. A denied caller fails before `Stamp`, so no journal entry is written. Cite `TestServiceRevisionUpdateDeniedBeforePrepare`.
  4. Update reloads under the lock after settlement, so a concurrent writer's revision is the `prior`. Cite `TestServiceRevisionUpdateReloadsAfterLockAndSettlement`.
  5. Cancellation of the request context mid-save restores the prior shared tree. Cite `TestServiceRevisionSharedCancellationRestoresPrior`.
  6. A nil `Workflow.ID` (legacy or local mode) still requests arrangement and never merges with another destination. Cite `TestLayoutE2E_NilCopiedIDStillRequests`.
- **Seed evidence:**
  - `grep -rn SetRevisionStamper` (non-test): `main.go:594` (Service) and `cmd/arbitron/sample_seed.go:23` (Seeder).
  - `domain/workflow/revision_stamper.go:Stamp` overwrites `wf.Presentation` unconditionally, with the token from `deps.NewToken`.
  - `revision_stamper.go:committed` returns `true` after a `RequestArrangement` error without calling `Abandon`, so the entry is retained for recovery.

### Area 3 — Publish, Copy and Duplicate commit a fresh destination revision with exact publication proof, and restore the prior tree when publication fails

- **Promise:** Initial publish, re-publish/overwrite, copy and admin duplicate each create a new destination revision, stamped in staging before it is visible. Registry-backed shared saves commit at a revision-bearing publication record. A failed overwrite restores every prior tree byte (design phase 2 §1–§2; contracts §6.2).
- **Entry:** `adapter/http/workflow_handler.go:WorkflowHandler.Publish` / `Copy`, the MCP `workflow_publish` / `workflow_copy` tools, then `domain/workflow/service.go:Service.Publish` / `Copy` and `admin_ops.go:Service.Duplicate`, then `service_revision_copy.go:publishRevision` / `copyRevision` and `service_revision_move.go:duplicateRevision`.
- **Sink:** `adapter/sqlite/published_workflow_repo.go:PublishedWorkflowRepo.CommitPublication` (exact revision plus WorkflowID), the destination tree via `tree_commit.go:PrepareTreeCommit` / `FinishTreeCommit`, and the `layout_revisions` row via `Requester.RequestArrangement`.
- **Files:** changed: `domain/workflow/service_revision_copy.go`, `domain/workflow/service_revision_move.go` (`duplicateRevision`), `domain/workflow/publishing.go`, `domain/workflow/admin_ops.go`, `domain/workflow/service.go`, `domain/workflow/revision_stamper.go`, `adapter/sqlite/published_workflow_repo.go`, `adapter/filesystem/tree_commit.go`, `adapter/filesystem/workflow_repo.go` (`CopyTreeStamped`), `adapter/http/workflow_handler.go`. Tests: `domain/workflow/service_revision_test.go` (`TestServiceRevisionPublishAndCopy`, `TestServiceRevisionPublishFailureRestoresWholeTree`), `adapter/filesystem/workflow_copy_stamped_test.go`, `adapter/sqlite/published_workflow_repo_test.go`, `cmd/arbitron/layout_publication_e2e_test.go`.
- **Folded in:** A copied `layout.json`: check that it seeds `Retained` for the new revision, never a passed verdict (`TestServiceRevisionPublishFallbackUsesSourceThenDestinationDisplay`). `publishing.go:84`: check that it rejects a publication with an invalid revision or a nil WorkflowID.
- **Review 1 — wiring lens:**
  1. `PublishedWorkflowRepo` is the `RevisionPublicationStore` passed to `newLayoutStack` and to `CommitSource`. Confirm that the same instance (`publishedRepo`) also serves the Service (`main.go:554-557`).
  2. `publishRevision` calls `PrepareTreeCommit`, then stamps, then `CommitPublication`, then the receipt and the request. Cite the call order in `service_revision_copy.go`.
  3. Copy and Duplicate write the stamp in staging through `CopyTreeStamped`'s callback, before the tree is visible.
  4. Each operation requests exactly one destination revision. Cite `TestLayoutPublicationE2E_ExactProofAndFailedReplacement` and the request counts asserted in `TestServiceRevisionPublishAndCopy`.
  5. The publisher-only overwrite rule and the source-owner re-check run under the lock. Cite `TestServiceRevisionPublishRechecksSourceOwnerUnderLock`.
- **Review 2 — failure lens:**
  1. A `CommitPublication` failure on overwrite restores the prior manifest, supporting files and cache byte-for-byte, then abandons the prepared entry. Name the byte-identity assertion.
  2. An older publication row with the same name is never proof of a new re-publish. Name the test that goes red if the proof matched on name only.
  3. Copy-name collisions and an unauthorised source fail before `Stamp`. Cite `TestServiceRevisionCopyPublishAuthorizationAndCollision`.
  4. A copy whose source carries a WorkflowID gets a fresh destination ID or registry identity. Cite `TestServiceRevisionCopySourceIdentityAndFailure`.
  5. Boundary failures at each step (prepare, stamp, proof, receipt) leave a state that settlement can decide. Cite `TestServiceRevisionOperationFailureBoundaries`.
- **Seed evidence:**
  - `grep CommitPublication` (non-test): `domain/workflow/service_revision.go:251`, `publishing.go:88`, `adapter/sqlite/published_workflow_repo.go:63`.
  - `grep PrepareTreeCommit`: `service_revision_copy.go:109`, `service_revision.go:226`, `service_revision_move.go:154`, `domain/samples/seeder_revision.go:116`.

### Area 4 — Rename, ownership transfer, delete and unpublish move or keep revision history without losing a revision or inventing one

- **Promise:** Rename issues a new saved revision after its relabel/identity commit. A transfer or owner move preserves the revision and relocates history and pending jobs through a durable stable-ID intent. Delete and unpublish settle prepared writes first and keep receipts and layout history (design phase 2 §1 table, §3).
- **Entry:** `domain/workflow/rename.go:Service.Rename` to `service_revision_move.go:renameRevision`; `admin_ops.go:Service.TransferOwnership` to `transferRevision`; `delete.go:Service.Delete` / `Unpublish` to `service_revision_support.go:lockedDelete`. Each is reached from its HTTP handler and MCP tool.
- **Sink:** `adapter/filesystem/revision_journal.go:BeginRelocation` / `EndRelocation`, `adapter/sqlite/published_workflow_repo.go:RenamePublication` (one transaction), `domain/layout/requester.go:RelocateArrangements` to `adapter/sqlite/layout_repo.go:LayoutRepo.Relocate`.
- **Files:** changed: `domain/workflow/service_revision_move.go`, `domain/workflow/service_revision_support.go`, `domain/workflow/rename.go`, `domain/workflow/admin_ops.go`, `domain/workflow/delete.go`, `domain/workflow/commit_source.go` (relocation branch), `adapter/filesystem/revision_journal.go`, `adapter/sqlite/published_workflow_repo.go`, `adapter/sqlite/layout_repo.go`, `domain/layout/requester.go`. Tests: `domain/workflow/rename_test.go`, `service_revision_test.go` (`TestServiceRevisionRenameDuplicateTransfer`, `TestServiceRevisionMoveRollbackAndPostcommitCleanup`, `TestServiceRevisionLocalDelete`), `cmd/arbitron/layout_e2e_test.go` (`TestLayoutE2E_RenameProtocolConflict`, `TestLayoutE2E_TransferRelocatesWithoutNewRevision`), `layout_service_revision_test.go` (`TestServiceRevisionE2E_DeleteUnpublishRaceCache`).
- **Folded in:** The admin delete guard (refuses deletion with non-terminal instances): check that the guard runs after settlement (`TestServiceRevisionDeleteRechecksIdentityAfterSettlement`).
- **Review 1 — wiring lens:**
  1. `Service.Rename` succeeds through `initDomain`. It does not today: `renameRevision` calls `BeginRelocation` before `CopyTreeStamped`'s callback calls `Stamp`, and `Stamp` then calls `CommitSource.Settle`, which rejects that same covering intent with `ErrMutationPending` (gap G1). Fail this check until a rename probe through the composition root is green.
  2. `CommitSourceDeps.Relocator` is `ls.requester` (`layout_wiring.go:125`), so `RelocateArrangements` reaches `LayoutRepo.Relocate` in production.
  3. Transfer moves layout rows without stamping a new revision. Cite `TestLayoutE2E_TransferRelocatesWithoutNewRevision`.
  4. Shared rename changes registry identity and publication name/proof in one SQLite transaction (`RenamePublication`). Cite the transaction.
  5. Delete and unpublish keep the `layout_revisions` rows. Grep that no production path deletes from `layout_revisions`.
- **Review 2 — failure lens:**
  1. Crash mid-relocation: when the registry points at the destination, recovery rolls forward even if both folders exist; when it points at the source, recovery rolls back. A missing endpoint, a third destination or a read error leaves the intent undecided. Name the test for each branch.
  2. `rollbackMove` after the identity commit: cancellation after that commit still finishes the relocation. Cite `TestServiceRevisionMoveSurvivesCancellationAfterIdentityCommit`.
  3. A `RenamePublication` failure leaves both the old name and the old proof. Cite `TestServiceRevisionSharedRenameProofFailure`.
  4. Delete races cache publication: a late layout write cannot recreate a deleted folder. Cite `TestServiceRevisionE2E_DeleteUnpublishRaceCache`.
  5. Deleting and recreating the same name gets a new revision and does not adopt the old history as current.
- **Seed evidence:**
  - `domain/workflow/service_revision_move.go:110 renameRevision`; `:203`/`:207` call `Relocate` / `RelocateArrangements`; `:165` calls `RenamePublication`.
  - Blocker text in plan phase-2-task-7 line 95 and phase-2-task-9 line 103: rename fails with `ErrMutationPending` through `initDomain`.
  - `grep -rn 'DELETE FROM layout_revisions' adapter/sqlite`: no match.

### Area 5 — A workflow or instance layout read returns that revision's own result, its fallback or an explicit unavailability, and only to an authorised caller

- **Promise:** Workflow and instance views read one authoritative revision record through a domain query seam. An instance uses its snapshot revision, never the workflow's latest. Pending or failed revisions show their retained fallback. "No arrangement" is reserved for pre-feature saves. Store outage is surfaced as unavailability, not success (spec "Workflow and instance views show the same arrangement"; design phase 3 §1–§3).
- **Entry:** `adapter/http/layout_handler.go:LayoutHandler.GetWorkflowLayout` / `GetInstanceLayout`; `adapter/http/instance_handler.go:InstanceHandler.Get` (embedded `layout`); `adapter/mcp/tools_workflow.go:workflowGetHandler` (arrangement summary). From there through `domain/layout/query.go:QueryService.WorkflowLayout` / `InstanceLayout` to `domain/workflow/layout_access.go:Service.LoadForLayout` and `domain/engine/layout_access.go:Engine.InstanceLayoutSnapshot`.
- **Sink:** `domain/layout/reader.go:Reader.View`, which builds the `View` JSON response (`displayStatus`, `provenance`, `geometry`, generations, `retryEligible`).
- **Files:** changed: `domain/layout/query.go`, `domain/layout/reader.go`, `domain/layout/store.go`, `domain/workflow/layout_access.go`, `domain/engine/layout_access.go`, `adapter/http/layout_handler.go`, `adapter/http/instance_handler.go`, `adapter/http/server.go`, `adapter/mcp/tools_layout.go` (`workflowArrangement`, `arrangementSummary`), `adapter/mcp/tools_workflow.go`, `adapter/mcp/deps.go`, `adapter/sqlite/layout_repo.go` (`Get`, `Result`), `cmd/arbitron/layout_wiring.go` (`attachQuery`), `cmd/arbitron/main.go` (`routerDeps`, `mcpDeps`). Unchanged but crossed: `domain/engine/engine.go:Engine.GetInstance`, `domain/workflow/service.go:Service.Load`. Tests: `domain/layout/query_test.go`, `query_negative_test.go`, `reader_test.go`, `reader_outage_test.go`, `adapter/http/layout_handler_test.go`, `instance_handler_layout_test.go`, `cmd/arbitron/layout_query_e2e_test.go`, `layout_http_e2e_test.go`, `layout_mcp_e2e_test.go`.
- **Folded in:** `adapter/http/server.go` route registration (`/workflows/{name}/layout`, `/instances/{id}/layout`): check that the routes sit behind the same auth middleware as their siblings. `DecodeWorkflowSnapshot`: check that a tokenless legacy snapshot decodes to legacy provenance.
- **Review 1 — wiring lens:**
  1. `ls.attachQuery(workflowSvc, eng)` (`main.go:971`) runs before `routerDeps`/`mcpDeps` read `ds.layoutQuery()`. Confirm that both HTTP and MCP receive a non-nil `Layouts` in production (`TestDomainStack_RouterDeps_ThreadsHandlersAndNilLayouts`).
  2. `InstanceHandler.SetLayouts(d.Layouts)` (`server.go:207`) feeds the embedded block, so the instance GET carries the snapshot revision's view (`TestLayoutHTTPE2E_InstanceReadsCarrySnapshotRevision`).
  3. The workflow read is keyed by the on-disk revision, and the instance read by the snapshot revision, not by `Instance.WorkflowID` or a hash. Cite `TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate`.
  4. `Reader` is fed the real store, journal and cache (`layout.NewReader(ls.store, ls.journal, ls.cache)`).
  5. MCP `workflow_get`'s `arrangement.savedRevision` equals the manifest's revision even under a racing save. Cite `TestLayoutMCPE2E_WorkflowGetRevisionRace`.
- **Review 2 — failure lens:**
  1. Unauthorised workflow or instance reads follow the existing concealment (not-found, not forbidden), and there is no path that looks up an arbitrary revision. Cite `TestLayoutHTTPE2E_AuthorizationAndBodyBoundaries` and `query_negative_test.go`.
  2. A historical instance whose workflow was deleted or made invisible still reads its revision. Cite `TestLayoutQueryE2E_HistoricalMissingRowAfterOwnershipMove`.
  3. Store outage returns the captured fallback with `available=false`, never legacy and never passed. Cite `reader_outage_test.go`.
  4. A `passed` row with missing, incompatible or unevidenced geometry is downgraded by `Reader`, and `Reader` stays read-only. Name the test.
  5. A missing row for a revision that has request metadata shows pending recovery, not "no arrangement".
  6. A nil `Layouts` wired into the handlers returns an explicit unavailable error. Cite `TestLayoutHTTPE2E_UnwiredLayoutsAreExplicit`.
- **Seed evidence:**
  - `cmd/arbitron/layout_wiring.go:30 attachQuery`; `main.go:971`.
  - `adapter/http/server.go:207-208,286-287,350-351`.
  - `adapter/http/instance_handler.go:510` returns "layout service not configured" when the service is nil.
  - `domain/layout/reader.go:226` computes `RetryEligible`.

### Area 6 — A layout retry from HTTP or MCP starts a new generation for exactly the expected revision, under the same authorisation and lock as a save

- **Promise:** Retry is offered for needs_attention, failed or enqueue-failed revisions. It uses the `Service.Update` write policy, or the instance intervention policy for historical revisions. It is fenced by the expected revision under the MutationCoordinator, and it never rewrites the manifest or redirects to a newer revision (design phase 3 §1–§3).
- **Entry:** `adapter/http/layout_handler.go:LayoutHandler.RetryWorkflowLayout` / `RetryInstanceLayout`; `adapter/mcp/tools_layout.go:workflowLayoutRetryHandler` / `instanceLayoutRetryHandler`. From there through `domain/layout/query.go:QueryService.RetryWorkflow` / `RetryInstance` to `workflow/layout_access.go:Service.AuthorizeLayoutRetry` and `engine/layout_access.go:Engine.AuthorizeInstanceLayoutRetry`.
- **Sink:** `domain/layout/requester.go:Requester.Retry` to `adapter/sqlite/layout_repo.go:LayoutRepo.Retry` (new `job_generation`, or `InvalidateResult` first), then the worker notify. The acknowledgement is `RetryAck`.
- **Files:** changed: `domain/layout/query.go`, `domain/layout/requester.go`, `domain/workflow/layout_access.go`, `domain/engine/layout_access.go`, `adapter/filesystem/manifest_lock.go`, `adapter/sqlite/layout_repo.go`, `adapter/http/layout_handler.go`, `adapter/mcp/tools_layout.go`, `adapter/mcp/register.go`. Unchanged but crossed: `domain/engine/engine.go:Engine.authorizeIntervention`, `domain/workflow/service.go:Service.adminSharedWrite`. Tests: `domain/layout/query_retry_test.go`, `requester_test.go`, `domain/workflow/layout_access_test.go`, `domain/engine/layout_access_test.go`, `adapter/mcp/tools_layout_test.go`, `tools_layout_race_test.go`, `cmd/arbitron/layout_mcp_retry_test.go`, `layout_query_concurrency_test.go`.
- **Folded in:** `adapter/mcp/register.go`: check that `RegisterLayoutTools` is called by `RegisterAllTools`, and that the registry test lists `workflow_layout_retry` and `instance_layout_retry`.
- **Review 1 — wiring lens:**
  1. Shared-workflow retry succeeds through the real filesystem lock. It does not today: `RetryWorkflow` locks `Shared()/name`, then `AuthorizeLayoutRetry` loads `Private(caller)/name` first, and `manifest_lock.go` rejects the expansion with "mutation lock set cannot expand" (gap G2). Fail this check until a shared retry through `initDomain` is green.
  2. Private retry and historical instance retry reach `LayoutRepo.Retry` and bump the generation. Cite `TestLayoutMCPE2E_RetryGenerationSinks` and `TestLayoutQueryE2E_RetryWorkflowThroughCompositionRoot`.
  3. HTTP and MCP call the same `QueryService` methods, with no duplicated policy in the adapters. Grep the adapters for `Authorize`.
  4. The worker sees the new generation: `Requester.Retry` notifies, and `Worker.Notify` is the notifier.
  5. A passed row whose result cannot be displayed is invalidated through `InvalidateResult` under exact fences before retry. Cite `TestLayoutHTTPE2E_RetryRepairsIncompatiblePersistedPass`.
- **Review 2 — failure lens:**
  1. A stale expected revision returns a conflict (HTTP 409 / MCP error) and the generation stays unchanged. Cite `TestLayoutHTTPE2E_RetryValidatesAndConflicts`.
  2. When a concurrent save and a retry race across processes, exactly one wins, and the loser either conflicts or targets its own revision. Cite `TestLayoutQueryE2E_RetryCrossProcessSaveFence`.
  3. `domain/layout/reader.go:Reader.View` eligibility agrees with `requester.go:Requester.Retry` and `query.go:QueryService.retry`: check pending missing-row recovery, in-progress conflict, valid passed conflict, invalid passed repair, needs_attention, failed and superseded. Both read/retry policy sides belong to P2T5 and share `passProblem`; keep this state matrix in this lens.
  4. Store unavailability returns an explicit retry error, and the view still shows Retry.
  5. A historical instance retry never supersedes or touches the current workflow's revision or execution data.
  6. A private shadow carrying the same token does not let a retry authorise against the wrong scope.
- **Seed evidence:**
  - `domain/layout/query.go:147-155` calls `WithMutations` on `{Scope: scope, Name: name}`, then calls `AuthorizeLayoutRetry` again inside the lock.
  - Blocker text: plan phase-3-task-8 lines 2134 and 2149.
  - `domain/layout/requester.go:133` is the only production `InvalidateResult` call.

### Area 7 — Every committed revision reaches a truthful terminal record within budget and publishes only to its current revision, generation and destination

- **Promise:** In both serve and stdio mcp, every committed input is durably queued and processed with the configured total attempt limit (default 5, minimum 1). SQLite retains the best geometry and its own evidence with a truthful terminal verdict. Only the matching current revision, job/result generation and destination may receive layout.json; cache failure never discards the authoritative result (spec "Bound refinement", "Handle incomplete results", "Respect saved revisions"; design phase 2 §4–§6).
- **Entry:** `domain/layout/requester.go:Requester.RequestArrangement` → `Worker.Notify` / `Worker.dispatch` → `worker.go:processRevision` → `domain/layout/coordinator.go:Coordinator.Run`. `processRevision` calls `publish` immediately after `Complete`; the same dispatcher's `reconcileCache` retries that path on every poll.
- **Sink:** `adapter/sqlite/layout_repo.go:LayoutRepo.Complete` / `Checkpoint` (state, attempts, best geometry, evidence, findings and failure), then `adapter/filesystem/layout_cache.go:LayoutCache.PublishCache` (layout.json) and `LayoutRepo.MarkCached`. `domain/layout/reader.go:Reader.cachedFallback` consumes the cache only during store outage.
- **Files:** changed: `domain/layout/worker.go`, `coordinator.go`, `finding.go`, `store.go`, `requester.go`, `manifests.go`, `reader.go` (`cachedFallback`), `adapter/sqlite/layout_repo.go`, `adapter/sqlite/migrations/019_layout_revisions.up.sql`, `adapter/filesystem/layout_cache.go`, `manifest_lock.go`, `workflow_repo.go` (reserved-file guards), `domain/config/config.go`, `adapter/config/loader.go`, `cmd/arbitron/layout_wiring.go`, `cmd/arbitron/main.go`. Tests: `domain/layout/coordinator_test.go`, `coordinator_evidence_test.go`, `coordinator_search_test.go`, `worker_test.go`, `worker_lease_test.go`, `worker_cancel_test.go`, `worker_lifecycle_test.go`, `worker_recovery_test.go`, `worker_missing_result_test.go`, `worker_cache_test.go`, `worker_cache_integration_test.go`, `adapter/filesystem/layout_cache_test.go`, `adapter/config/loader_layout_test.go`, `cmd/arbitron/layout_lifecycle_test.go`, `layout_coordinator_e2e_test.go`, `layout_worker_recovery_test.go`, `layout_e2e_test.go`.
- **Folded in:**
  - `domain/config/config.go:LayoutConfig` and `adapter/config/loader.go`: trace default 5, `ARBITRON_LAYOUT_MAXATTEMPTS`, minimum-1 clamp and `initDomain`'s `cfg.LoadWarnings` logging (`TestLayoutE2E_ConfigBudgetAndWarnings`).
  - `cmd/arbitron/main.go:domainStack.close`: queue/engine stop precedes `ls.stop`, which joins workers before renderer and DB close.
  - `adapter/filesystem/workflow_repo.go:reservedWorkflowFile`: raw `WriteFile`/`DeleteFile` reject layout.json, `ListFiles` hides it, and MCP `workflow_write_file` uses the same guard.
  - `adapter/sqlite/layout_repo.go:Complete` → `PendingCache` → `MarkCached`: the cache tail and its repeated sweep are part of this worker behaviour, not a separate review area (P2T8 §8.2/§8.4).
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` calls `ls.start` for both serve and mcp; `layout_wiring.go:layoutStack.stop` joins the worker before closing its collaborators. Cite `TestLayoutE2E_ActualCommandsJoinOnContextAndEOF`.
  2. `layout_wiring.go:newLayoutStack` feeds `cfg.Layout.MaxAttempts` → `NewRequester` → `NewRequest.AttemptLimit` → SQLite `Record.AttemptLimit` → `worker.go:processRevision`'s `Job.MaxAttempts`. The worker decodes only `rec.Manifest` and checks its saved token. Name the five-attempt sink test.
  3. `layout_wiring.go:newLayoutStack` builds `NewCoordinator` with the real `BrowserRenderer` and `ReadabilityReviewer`. Trace a save to the terminal row with `TestCoordinator_RealBrowserBoundedBestReachesSink` and `TestLayoutRender_RealBrowserPRReview`.
  4. `domain/layout/store.go:Store.Supersede` / `StateSuperseded` are a deliberately reserved lifecycle operation (contracts §6.12 line ~1377: no automatic producer; spec permits but does not require cancellation; no cancel entry point is specified). Verify instead that (i) no save, retry or recovery path calls it implicitly (`TestLayoutE2E_NewSaveDoesNotSupersedeHistoricalWork`), and (ii) every consumer branch (`worker.go:193`, `reader.go`, `requester.go:125`) is exercised against the real SQLite store (`TestWorkerLeaseSupersedeKeepsBest`, `adapter/sqlite/layout_repo_test.go`, `reader_test.go`, `requester_test.go`). Decision recorded 2026-10-18 (G12, option b); revisit only if a cancel operation is ever specified.
  5. `worker.go:processRevision` calls `publish` after `LayoutRepo.Complete`; `dispatch` also calls `reconcileCache`. Trace `persist` clearing `cache_generation`, `PendingCache` selecting it and `MarkCached` settling it through a real on-disk cache assertion in `worker_cache_integration_test.go`.
  6. `layout_wiring.go:newLayoutStack` gives `LayoutCache` the same repo used for mutations; `LayoutCache.PublishCache` settles and checks the current token under that lock, then `Worker.publish`'s callback rechecks job/result generations and destination before writing.
  7. `reader.go:Reader.cachedFallback` validates exact revision, contract, graph coverage and any readable row's generation fences. Cache geometry remains unavailable and unreviewed during outage; it cannot manufacture a reviewed pass.
- **Review 2 — failure lens:**
  1. `coordinator.go:Coordinator.Run` counts once at arrangement start; render/review errors never reset the budget, and budget 1 means one attempt. Its per-stage contexts and `worker.go:processRevision`'s job context enforce 2-minute/15-minute defaults. `TestRun_StageTimeout` is load-sensitive (G11).
  2. `worker.go:processRevision` retains the best candidate's own evidence on renderer/reviewer error or cancellation; `storedBetter` prevents worse or unreviewed retries replacing a reviewed best. Shutdown uses fenced `LayoutRepo.Checkpoint` without terminal completion (`worker_cancel_test.go`).
  3. `worker.go:processRevision` renews leases every lease/3 during blocked stages; lost `Progress` cancels the job, and another process reclaims expiry with a fresh generation. Cite `worker_lease_test.go` and `TestLayoutE2E_CloseCheckpointsAndNewProcessReclaims`.
  4. `worker.go:Notify` / `dispatch` recover durable work despite hint saturation, notifications before Start or restart after Stop; `recover` retains journal input until Request is acknowledged. Name the row-level assertions in `worker_recovery_test.go`.
  5. `Worker.publish` / `LayoutCache.PublishCache` reject late revision A after save B while preserving A's historical result. `LayoutCache.ReadCache` ignores corrupt or mismatched cache files; `Reader.cachedFallback` never substitutes another revision.
  6. `LayoutRepo.MarkCached` and `Worker.publish` fence job generation, result generation and full destination through retry, rename, transfer and delete races; old work cannot settle new work or recreate a deleted folder. Check `worker_cache_test.go` and `TestServiceRevisionE2E_DeleteUnpublishRaceCache`.
  7. `Worker.reconcileCache` retries transient cache failures without changing the SQLite verdict. `Reader.View` keeps serving the authoritative result; `TestLayoutE2E_CacheOutageAndDeletionPreserveAuthoritativeResult` must reach that sink.
- **Seed evidence:**
  - `domain/layout/worker.go:processRevision` returns `w.publish(ctx, fence)` directly after `Store.Complete`; `dispatch`'s sweep calls the same `reconcileCache` → `publish`. `ReconcileCache` has no production external caller.
  - `adapter/sqlite/layout_repo.go:persist` resets cache settlement on best replacement; `Retry` and `Relocate` also reset it. `MarkCached` checks job/result generations and destination.
  - `cmd/arbitron/layout_wiring.go:newLayoutStack` constructs one requester, coordinator, worker and cache; the notifier is `ls.worker.Notify`.
  - `grep '\.Supersede('` outside tests: no caller (G12).

### Area 8 — Each candidate is drawn by real Chrome through the shared renderer and reviewed from the actual DOM and pixels; missing evidence never passes

- **Promise:** Every candidate gets visual readability review. The production renderer draws it with the dashboard's own ArrangedGraph components, CSS and bundled fonts, and captures an overview plus overlapping readable-scale tiles. The reviewer checks the DOM report and pixel features (route corridors, glyphs, arrows, crossing gaps). A pass requires zero blocking findings and evidence bound to that exact candidate and contract (spec "Review the rendered result"; design phase 1 §3, phase 3 §4).
- **Entry:** the `Coordinator.Run` render call, then `adapter/renderreview/browser.go:BrowserRenderer.Render`, then `assets.go:assetServer` serving the embedded `render.html`, then `dashboard/src/render/main.tsx`, then `RenderSurface.tsx:runRenderJob` drawing `ArrangedGraph`, then `report.ts:collectNodes` / `collectEdges` / `collectCrossings`.
- **Sink:** `adapter/renderreview/reviewer.go:ReadabilityReviewer.Review` returns a `layout.Review` with evidence, then `domain/layout/coordinator.go:HasRenderedEvidence` / `evidenceProblem` decides the verdict.
- **Files:** changed: `adapter/renderreview/browser.go`, `assets.go`, `tiles.go`, `reviewer.go`, `reviewer_capture.go`, `reviewer_checks.go`, `domain/layout/render.go`, `domain/layout/coordinator.go` (evidence functions), `dashboard/src/render/main.tsx`, `RenderSurface.tsx`, `report.ts`, `job.ts`, `contrast.ts`, `dashboard/src/graph/arranged/ArrangedGraph.tsx`, `RoutedEdge.tsx`, `routedPath.ts`, `ArrangedNode.tsx`, `buildArrangedFlow.ts`, `*.module.css`, `dashboard/render.html`, `dashboard/vite.config.ts`, `adapter/dashboard/embed.go`, `adapter/dashboard/dist/render.html`, `cmd/arbitron/layout_wiring.go` (`newLayoutRenderReviewer`), `Makefile`, `Dockerfile`, `.github/workflows/layout.yml`. Tests: `adapter/renderreview/*_test.go`, `dashboard/src/render/*.test.ts*`, `dashboard/scripts/test-render-surface-browser.mjs`, `cmd/arbitron/layout_render_e2e_test.go`, `layout_reviewer_e2e_test.go`, `layout_render_capture_e2e_test.go`, `layout_surface_report_e2e_test.go`, `layout_render_wiring_test.go`, `layout_build_test.go`.
- **Folded in:**
  - `adapter/dashboard/dist/**` (931 rebuilt assets): check only that `render.html` is embedded and that the build test requires fresh assets (`TestLayoutBuild_MakeAllRequiresFreshAssetsAndBrowser`).
  - `Dockerfile`: check whether the image ships Chromium (gap G9).
  - `.github/workflows/layout.yml` / `Makefile test-render`: check that CI sets `ARBITRON_REQUIRE_BROWSER=1` (`TestLayoutBuild_CIRequiresTheGate`).
  - `cfg.Layout.BrowserNoSandbox`: check that it defaults to false and reaches the launch flags (`TestNewLayoutRenderReviewer_RealBrowserNoSandboxIsFed`).
- **Review 1 — wiring lens:**
  1. `newLayoutRenderReviewer` installs `BrowserRenderer` (with `dashboard.Assets`) and `ReadabilityReviewer`, and is the only factory reachable from `newLayoutStack`. Cite `TestNewLayoutRenderReviewer_InstallsTheBrowserAdapter`.
  2. A broken embedded build (no `render.html`) fails boot. A missing browser fails jobs as retryable `failed` at the rendering stage. Cite `TestNewLayoutRenderReviewer_BrokenBuildFailsBoot` and `TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable`.
  3. `RenderRequest.WorkflowNodeTypes` is filled from the captured `Job.Manifest.Nodes[].Type`, not reversed from captions. Cite the construction in `coordinator.go` and its forwarding in `browser.go`.
  4. `RenderSurface` draws with the same `ArrangedGraph` that `WorkflowReview` and `ExecutionView` import, and runtime-state probes run before capture.
  5. The reviewer opens the actual PNG tiles and the overview. Cite where `reviewer_capture.go` decodes them, and where tile coverage of the whole zero-origin canvas is asserted (`coordinator.go:tileUnionCovers`).
  6. The happy path is `TestLayoutRender_RealBrowserPRReview` (pr-review passes with all sinks). The success path, feature-pipeline and ralph-loop passing within five attempts, is not met (gap G7).
- **Review 2 — failure lens:**
  1. Missing, partial or mismatched evidence never yields `passed`. A digest, contract or revision mismatch, missing tiles or a reviewer error all fail. Cite `coordinator_evidence_test.go` and `HasRenderedEvidence`.
  2. Injected visual-only defects (clipping, ambiguous crossing, concealed destination, missing branch ink) fail review even when geometry preflight is clean. Cite `TestLayoutRender_RealSurfaceReportDefectsReachSavedResults` and `reviewer_checks_test.go`.
  3. A render timeout, a Chrome launch failure or a Chrome crash mid-capture: the immutable capture directory is cleaned up or preserved as referenced, and the stage error names the rendering stage.
  4. Concurrent captures (worker concurrency 2) use unique directories and SVG marker namespaces. The concurrent-capture test hit Chrome's 30s launch deadline under load (phase-3-task-5 line 2176).
  5. The asset server is loopback-only and its job documents are one-shot. Cite `browser_boundary_test.go`.
  6. Fonts that are not ready, or a family that does not resolve, block review (`reviewer_checks.go:30`).
- **Seed evidence:**
  - `adapter/renderreview/browser.go:394` rejects `req.Contract != layout.DefaultContract()`; `reviewer.go:140` validates the geometry and contract.
  - `cmd/arbitron/layout_wiring.go:64-76` logs a warning on a missing browser but still installs the renderer.
  - Blocker text: plan phase-3-task-7 line 501 (feature-pipeline and ralph-loop `needs_attention`); `Dockerfile` has no Chromium (phase-3-task-7 line 758).

### Area 9 — Newly seeded samples arrange exactly once and resume idempotently, while backfill, flat-store migration and boot never arrange

- **Promise:** Newly created sample manifests arrange after successful seeding, and an interrupted new seed resumes idempotently without rearranging already-seeded workflows. Identity backfill, filesystem migration and reads never request arrangement or stamp a revision. There is no startup bulk arrangement (spec non-goal "automatically rearranging existing workflows"; design phase 2 §1).
- **Entry:** `cmd/arbitron/main.go:initDomain` calls `cmd/arbitron/sample_seed.go:seedSampleWorkflows`, then `domain/samples/seeder.go:Seeder.Seed` / `seedOne`, then `seeder_revision.go:seedRevision`. The non-triggering entries are `domain/workflow/backfill.go:Service.BackfillRegistry` and `cmd/arbitron/flatstore_migrate.go:migrateFlatStore`.
- **Sink:** `PublishedWorkflowRepo.CommitPublication` plus the `layout_revisions` row through `RevisionStamper.Committed`, then `seeder_revision.go:recordSeed` (the ledger append in `filesystem.SeedLedger`). For the non-triggering paths the sink is the absence of any journal entry or row.
- **Files:** changed: `domain/samples/seeder.go`, `domain/samples/seeder_revision.go`, `domain/samples/ports.go`, `adapter/filesystem/sample_source.go`, `cmd/arbitron/sample_seed.go`, `domain/workflow/backfill.go`, `cmd/arbitron/flatstore_migrate.go`, `domain/workflow/revision_stamper.go` (`EnsureStamped`). Tests: `domain/samples/seeder_revision_test.go`, `seeder_test.go`, `adapter/filesystem/sample_source_errors_test.go`, `cmd/arbitron/sample_seed_durable_test.go`, `sample_seed_race_test.go`, `sample_seed_revision_test.go`, `sample_seed_rollback_test.go`, `layout_e2e_test.go` (`TestLayoutE2E_SeedThroughRootAndRestart`, `TestLayoutE2E_LegacyReadsAndBackfillNeverStamp`, `TestLayoutE2E_SeedDoesNotAdoptForeignOrphan`).
- **Folded in:** `cmd/arbitron/flatstore_migrate.go` (modified): check that migration never writes `presentation` or a journal entry. `domain/samples/ports.go` (`SampleSource.Read` error surface): check that a read error aborts only that sample.
- **Review 1 — wiring lens:**
  1. `seedSampleWorkflows` receives `ls.stamper`, `ls.mutations` and `identityRepo`, and calls `SetRevisionStamper`, `SetMutationCoordinator` and `SetRegistry` (`sample_seed.go:23-25`).
  2. The registry setter runs before backfill. Cite the SQL-trigger probe in plan phase-2-task-9 line 106.
  3. A fresh seed produces exactly one prepared token, one publication/identity, one row and one ledger append. Cite `TestSeedSampleWorkflows_TwoProcessesCommitOnce`.
  4. `BackfillRegistry` and reads never call `Stamp`. Cite `TestLayoutE2E_LegacyReadsAndBackfillNeverStamp`.
  5. A notification raised during seeding, which happens before `ls.start`, is still processed once the worker starts.
- **Review 2 — failure lens:**
  1. Ledger failure after `Committed`: `Committed` removes the journal input before `recordSeed`, while `EnsureStamped` requires that exact committed entry, so the sample can never resume (gap G4).
  2. A crash before proof: `CommitSource` rolls back the seed tree but never releases the registry claim, so reseeding fails with `ErrAlreadyExists` (gap G5).
  3. A foreign or orphaned same-name folder is never adopted. Cite `TestLayoutE2E_SeedDoesNotAdoptForeignOrphan`.
  4. A supporting-file write failure on a fresh tree rolls back completely. Cite `TestSeedSampleWorkflows_DurableSupportingFileRollback`.
  5. A seeding error is logged, boot continues, and no partial row is left that shows as arranged.
- **Seed evidence:**
  - `domain/samples/seeder_revision.go:45,48` (`EnsureStamped`), `:98` (`RollbackTreeCommit`), `:116` (`PrepareTreeCommit`), `:161 recordSeed`.
  - `cmd/arbitron/main.go:575` (seed switch) comes after the `:563` settlement.

### Area 10 — A saved manifest arranges into continuous downward flow with branches beside it and distinct loop channels, changing presentation only

- **Promise:** Layered top-to-bottom ranking excludes back-edges. Sequential flow never wraps into columns, branches sit beside the flow, shortcuts and rejoins get traceable routes, and loop returns use reserved side channels. Every node and edge occurrence is represented once, the output is deterministic, the manifest bytes are unchanged, and findings drive targeted refinement (spec "Present continuous downward flow", "Preserve workflow behavior"; design phase 1 §1–§2, §4).
- **Entry:** the `Coordinator.Run` attempt loop, then `domain/layout/graph.go:BuildGraph` (from the captured manifest), then `arrange.go:Arrange`.
- **Sink:** a `domain/layout/geometry.go:Geometry` value (validated by `Geometry.Validate`) plus `preflight.go:Preflight` findings, fed back through `refine.go:Refine` into the next attempt's `Options`.
- **Files:** changed: `domain/layout/graph.go`, `arrange.go`, `loops.go`, `rank.go`, `layers.go`, `place.go`, `route.go`, `size.go` (box growth only; measurement is Area 11), `geometry.go`, `options.go`, `contract.go`, `preflight.go`, `refine.go`, `finding.go`. Tests: `domain/layout/arrange_test.go`, `arrange_wire_test.go`, `loops_test.go`, `rank_test.go`, `layers_test.go`, `route_test.go`, `geometry_test.go`, `preflight_test.go`, `preflight_semantics_test.go`, `refine_test.go`, `samples_geometry_test.go`, `cmd/arbitron/layout_layers_e2e_test.go`, `layout_preflight_e2e_test.go`, `layout_refine_e2e_test.go`.
- **Folded in:** `domain/layout/contract.go:DefaultContract` against `dashboard/src/graph/arranged/contract.json`: check `contract_test.go`. `geometry.go:validateGeometryOptions`: check that it permits zero margin and zero crossing sweeps, which contradicts the P3T1 client rule (gap G10).
- **Review 1 — wiring lens:**
  1. `BuildGraph` is fed the captured `Job.Manifest`, and edge identity is the slice ordinal, so identical parallel edges both route. Cite `graph_test.go`.
  2. `Refine` output reaches the next attempt's `Arrange` options inside `Coordinator.Run`. Name the line, and name the test where a finding changes the next candidate (`TestRefine_RealBrowserSavedBranchClippingReachesSink`).
  3. `Preflight` runs on every candidate, including candidates that fail it, and they are still rendered and reviewed (design phase 1 §4).
  4. The arranger never mutates the manifest. Name the byte-equality test.
  5. For the real samples, pr-review passes. feature-pipeline (66 nodes, 79 edges) and ralph-loop end `needs_attention` (gap G7). Cite `samples_geometry_test.go` and `TestLayoutLayers_RealBrowserSavedRowsReachView`.
- **Review 2 — failure lens:**
  1. Empty graphs, dangling endpoints, duplicate node IDs and non-finite geometry return typed errors without panics. Cite `arrange_test.go`.
  2. Edge cases: a single node, a self-loop, nested or overlapping loops, multiple entrypoints, disconnected components, identical parallel edges and long Unicode labels. Name the test for each.
  3. Determinism: a fixed manifest, options and measurements give byte-identical geometry (`GeometryDigest`) across runs.
  4. The bounded crossing search keeps blocking-contact count, then crossing count, then prefix order. Precedence cycles are never resolved by dropping constraints.
  5. `domain/layout/refine.go:Refine` feeds `coordinator.go:Coordinator.Run` directly: changed=false must mean no applicable option can change and returns needs_attention/no_refinement; changed=true must affect the next `arrange.go:Arrange` candidate rather than consume attempts on identical `GeometryDigest` values. Check capped options and unfixable findings (`refine_test.go`, `coordinator_search_test.go`).
- **Seed evidence:**
  - `domain/layout/geometry.go:393 validateGeometryOptions`.
  - `domain/layout/loops.go:10 classifyEdges` ports `dashboard/src/graph/loops.ts:detectLoops`.
  - Findings for feature-pipeline: edge 49 missing branch ink; labels 31 and 34; crossings [34,49], [34,53], [14,53], [78,61], [77,69], [78,77]. For ralph-loop: crossings [37,20] and [37,38] (plan phase-3-task-7 line 501).

### Area 11 — Node and branch text is measured by the real renderer with the bundled fonts, and that measurement sizes the boxes it is drawn in

- **Promise:** Measurement comes from the production renderer: `MeasureSurface` uses the renderer's own classes and the bundled Inter, Noto Sans SC and Noto Emoji. It does not come from guessed Go font constants. Unsupported glyphs fail visibly. Long labels wrap or grow their boxes and are never ellipsized (design phase 1 §1, phase 3 §4).
- **Entry:** the `Coordinator.Run` measure call, then `layout.MeasureRequestFor`, then `adapter/renderreview/browser.go:BrowserRenderer.Measure`, then `dashboard/src/render/MeasureSurface.tsx:runMeasureJob`, then `dashboard/src/graph/arranged/fonts.ts:ensureBundledTextFonts`.
- **Sink:** `domain/layout/measure.go:Measurements`, consumed by `domain/layout/size.go:nodeSizes` / `edgeLabelSizes` as the box dimensions in the geometry.
- **Files:** changed: `domain/layout/measure.go`, `domain/layout/size.go`, `adapter/renderreview/browser.go` (`Measure`), `dashboard/src/render/MeasureSurface.tsx`, `dashboard/src/render/job.ts`, `dashboard/src/graph/arranged/fonts.ts`, `fontCoverage.json`, `ArrangedNode.module.css`, `dashboard/scripts/build-font-coverage.mjs`, `test-font-coverage.mjs`, `dashboard/package.json` (fontsource deps), `adapter/dashboard/dist/licenses/*`, `dashboard/public/licenses/*`. Tests: `domain/layout/measure_test.go`, `size_test.go`, `adapter/renderreview/sizing_browser_test.go`, `dashboard/src/render/MeasureSurface.test.tsx`, `dashboard/src/graph/arranged/fonts.test.ts`, `cmd/arbitron/layout_sizing_e2e_test.go`.
- **Folded in:** Font license files: check that they are present in `dist/licenses` and `public/licenses`. `dashboard/package.json` / `package-lock.json`: check that the fontsource versions match the contract (Noto Sans SC 5.3.1, Noto Emoji 5.3.2).
- **Review 1 — wiring lens:**
  1. `Coordinator.Run` calls `renderer.Measure` with the full text set from `MeasureRequestFor`, and `Arrange` consumes the result. There is no fallback measurement producer in production.
  2. `MeasureSurface` applies the same CSS classes as `ArrangedNode` and `RoutedEdge` labels (`nodeTextClassNames`, `edgeLabelClassName`).
  3. `fonts.ts:ensureBundledTextFonts` loads the required ranges and weights before measuring, and `fontsReady=false` fails measurement.
  4. Measurement keys (`NodeLabelKey`, `NodeTypeKey`, `EdgeLabelKey`) round-trip through the browser JSON. Cite `TestLayoutSizing_RealBrowserSavedUnicodeMeasurementsReachView`.
  5. `MeasureSurface.tsx:metricsFrom` returns content-box fractional metrics; `domain/layout/size.go:metricForText`, `nodeSizes` and `edgeLabelSizes` consume the same maxWidth, line height and padding contract. `ceilPx` adds rounding slack once; exercise long labels through geometry and the real rendered clipping report (`layout_sizing_e2e_test.go`).
- **Review 2 — failure lens:**
  1. A grapheme outside the bundled coverage fails measurement visibly, keeping the fallback and offering Retry. Cite `TestLayoutSurfaces_MissingBundledSubsetRetainsFallback`.
  2. `domain/layout/coordinator.go:Coordinator.Run` maps a `BrowserRenderer.Measure` timeout or Chrome failure to `StageRendering` with a "measuring text:" reason, counts the attempt and returns before `Arrange`; no partial metrics may become geometry.
  3. A missing key in `Measurements.Metric` returns an error, never a zero-size box.
  4. Long Unicode labels (CJK, emoji ZWJ sequences) wrap within `maxWidth` without clipping. Name the test.
  5. `dashboard/src/graph/arranged/fonts.ts:ensureBundledTextFonts` must resolve the bundled family and requested glyph ranges before `MeasureSurface.tsx:runMeasureJob` emits fontsReady. Inspect missing-subset and fallback-family negatives; cross-browser portability remains unproven on this one-browser host (W2).
- **Seed evidence:**
  - `domain/layout/measure.go:49-59` (keys and `MeasureRequestFor`); `size.go:24 ceilPx`, `:27 metricForText`.
  - `dashboard/src/graph/arranged/fonts.ts:20 RENDERER_FONT_FAMILIES`, `:89 ensureBundledTextFonts`.

### Area 12 — Both dashboard views draw the revision-joined layout with truthful status, poll until terminal, offer Retry, and never write node positions

- **Promise:** `WorkflowReview` and `ExecutionView` render the revision-scoped best or fallback geometry through the shared `ArrangedGraph`. They show pending, arranging, rendering/reviewing, complete, needs attention or failed, with findings and the reason. Both poll while active (independently of the execution WebSocket), ignore out-of-order responses, start at a readable zoom (0.9) and not fit-all, keep follow and callouts with payload dimensions, and contain no drag or position-save path (design phase 3 §5–§6).
- **Entry:** route mount of `dashboard/src/routes/WorkflowReview.tsx:ReviewPage` and `dashboard/src/routes/ExecutionView.tsx:InstanceArrangement`, then `dashboard/src/hooks/useLayoutStatus.ts:useLayoutStatus`, then `dashboard/src/api/client.ts` (`/workflows/{name}/layout`, `/instances/{id}/layout`, and the retry POSTs).
- **Sink:** the DOM rendered by `dashboard/src/graph/arranged/ArrangedGraph.tsx` and `LayoutStatusPanel.tsx` (status title, findings, Retry button).
- **Files:** changed: `dashboard/src/routes/WorkflowReview.tsx`, `ExecutionView.tsx`, `dashboard/src/hooks/useLayoutStatus.ts`, `dashboard/src/api/client.ts`, `dashboard/src/api/types.ts`, `dashboard/src/graph/arranged/LayoutStatusPanel.tsx`, `ArrangedGraph.tsx`, `validate.ts` (`resolveDisplayGeometry`), `fallbackLayout.ts`, `types.ts`, `dashboard/src/graph/useFollowActiveNode.ts`, `CalloutConnector.tsx`, `useGraphLayout.ts`, `dashboard/src/graph/types.ts`, `dashboard/src/graph/nodes/ArbitronNode.tsx`, `nodes/typeColors.ts`, `adapter/http/workflow_handler.go` (layout PUT removed), `adapter/http/server.go`, `domain/workflow/service.go` / `ports.go` (`SaveNodePositions` removed), `adapter/filesystem/workflow_repo.go`. Tests: `dashboard/src/hooks/useLayoutStatus.test.ts`, `dashboard/src/graph/arranged/LayoutStatusPanel.test.tsx`, `ArrangedGraph.test.tsx`, `validate.test.ts`, `fallbackLayout.test.ts`, `dashboard/src/routes/*.test.tsx`, `domain/workflow/layout_writes_removed_test.go`, `adapter/filesystem/workflow_repo_legacy_positions_test.go`, `cmd/arbitron/layout_client_acceptance_test.go`, `layout_surfaces_e2e_test.go`, `layout_http_backoff_test.go`.
- **Folded in:**
  - Removal of `PUT /workflows/{name}/layout`, `Service.SaveNodePositions`, `Repository.SaveNodePositions`, `workflows.saveLayout` and `needsAutoSave`: check that `grep -rn 'SaveNodePositions\|saveLayout\|needsAutoSave'` finds nothing outside tests and history (currently none).
  - `nodesDraggable={false}` (`ArrangedGraph.tsx:165`): check that the explicit fit-view control (`:186`) is the only `fitView`.
  - `typeColors.ts`: check that it is shared by `ArbitronNode` and `ArrangedNode`.
- **Review 1 — wiring lens:**
  1. Both routes import `ArrangedGraph`, `LayoutStatusPanel` and `useLayoutStatus`, and pass the live `LayoutView` from `client.ts`, not a fixture (`WorkflowReview.tsx:173`, `ExecutionView.tsx:150`).
  2. The manifest and layout are joined by `savedRevision`. On a mismatch the view refetches instead of mixing revisions. Name the test.
  3. `ExecutionView` polls layout independently of execution events, and keeps polling after the execution is terminal. Cite `TestLayoutSurfaces_CompletedInstanceRefreshesWithoutWebSocket`.
  4. The Retry button posts `expectedRevision` from the view and installs the minimum generation from `RetryAck` before refetching.
  5. `useFollowActiveNode` and `CalloutConnector` use payload node dimensions, not fixed `NODE_WIDTH`/`NODE_HEIGHT`. Grep the remaining uses.
  6. Name the end-to-end test where real HTTP data reaches the DOM: `TestLayoutSurfaces_ClientValidationAndAPIReachDOM`.
- **Review 2 — failure lens:**
  1. `needs_attention` and unfinished `superseded` never render as complete or "Layout reviewed". Cite `TestLayoutSurfaces_NeedsAttentionShowsFindingsNotReviewed`.
  2. A network error or `available=false` keeps the last compatible same-revision geometry, shows an availability error, and backs off up to `MAX_BACKOFF_MS`. Cite `TestLayoutSurfaces_HTTPRecoveryRetainsGraphAndBacksOff`.
  3. Out-of-order responses (route A→B→A, a stale terminal read after retry) are ignored by request epoch and generation.
  4. `validate.ts:resolveDisplayGeometry` / `validateGeometry`, called by both `ArrangedGraph` and `RenderSurface.tsx:runRenderJob`, reject invalid payloads/version mismatches with fallback and display issues. Compare Go `geometry.go:validateGeometryOptions`, including zero margin/crossingSweeps (G10); this is the same validator used during review, not a separate dashboard boundary. A client font failure on a passed row has no Retry path today (G8).
  5. Polling stops on unmount and at terminal states, and restarts after retry, focus or external-save invalidation.
  6. `canRetryLayout` (`WorkflowReview.tsx:28`) never shows Retry to a caller the server would refuse, and never hides it from an eligible one.
- **Seed evidence:**
  - `grep -rn 'saveLayout\|SaveNodePositions\|needsAutoSave'` (non-test): none.
  - `dashboard/src/api/client.ts:179,184,214,219` (layout endpoints); `useLayoutStatus.ts:13-14` (`POLL_INTERVAL_MS` 2000, `MAX_BACKOFF_MS` 30000).
  - `dashboard/src/graph/arranged/LayoutStatusPanel.tsx` `showRetry` excludes `displayIssues` on passed rows (plan phase-3-task-2 line 1264).
