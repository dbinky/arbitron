# Focus Areas — Workflow Auto-Arrangement (branch over `345f751`)

Each area is one **behaviour** this branch promises. It is traced from its entry point to
its sink, across every layer in between. Rows are ordered highest risk first, ranked by
consequence. The detail block for each area lists every file on the path, the small
changes folded into it, and two sets of checks that ask different questions. Review 1
asks: does the path exist and is it fed? Review 2 asks: how does it fail?

How the areas were derived, and which candidates were merged, is recorded at the end of
this file under "Merge decisions".

**Budget:** 7 single-system areas + 7 paired seams; 2 × 7 + 7 = **21** minimum passes ≤ 500 × 0.35 = **175**. No deferrals.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | a writer killed mid private-save swap or shared overwrite leaves every surviving raw, workflow and layout read serving committed bytes or the fixed unavailable error until settlement rolls forward or restores the prior tree | high | [x] | [x] |
| 2 | a rename or ownership transfer settles only its own relocation intent, peers never see a half-moved tree, and a killed move is decided by registry authority | high | [x] | [x] |
| 3 | a boot seed interrupted after its registry claim or after commit is compensated or resumed from its exact acknowledged input, never adopted from a folder | high | [x] | [x] |
| 4 | authorised workflow and instance layout reads and retries preserve identity, cancellation and revision ownership through the response, generation bump and dashboard fallback | high | [x] | [x] |
| 5 | the stdio MCP transport writes only JSON-RPC to stdout, authors as the default user only in local mode, and boots beside a running server without a lock error | high | [x] | [x] |
| 6 | saved revisions reach the shipped browser and rendered review with measured text and captured ownership; ordinary samples pass, unsupported groups and font failures remain explicit | med | [x] | [x] |
| 7 | every authoring path for a group workflow validates child contracts before mutation locks and captures the exact saved manifest and identity into the arrangement request | med | [x] | [x] |

---

### Area 1 — a writer killed mid private-save swap or shared overwrite leaves every surviving raw, workflow and layout read serving committed bytes or the fixed unavailable error until settlement rolls forward or restores the prior tree

- **Promise:** Commit `5fd8afd` and contracts §6.18 ("Settled raw manifest/cache reads"). A peer process that dies inside a private Update swap or an admin shared overwrite never exposes its uncommitted tree. Surviving HTTP, MCP and engine reads of `workflow.json`/`layout.json` return the committed bytes or HTTP 503 UNAVAILABLE / an MCP tool error. The next settlement then rolls forward on proof or restores the prior tree.
- **Entry:** `adapter/filesystem/file_reader.go:FileReaderAdapter.ReadFile` (raw read on a surviving process); the crash producer is the private swap in `domain/workflow/service_revision.go` and `adapter/filesystem/tree_commit.go:PrepareTreeCommit`.
- **Sink:** `domain/workflow/commit_source.go:CommitSource.Settle` → `settlePrepared` → `adapter/filesystem/tree_commit.go:WorkflowRepo.RollbackTreeCommit` (restored tree) or the roll-forward receipt; the HTTP/MCP response bytes.
- **Files:**
  - Changed: `adapter/filesystem/durable.go`, `adapter/filesystem/tree_commit.go`, `adapter/filesystem/commit_rollback_crash_test.go`, `domain/workflow/commit_source_boundary_test.go`, `domain/workflow/commit_source_test.go`, `cmd/arbitron/layout_http_peer_test.go` (`cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPPrivateSwapProcess`, `cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPE2E_PrivateSwapGapRawFirst`), `cmd/arbitron/layout_mcp_stdio_crash_test.go` (`cmd/arbitron/layout_mcp_stdio_crash_test.go:TestLayoutMCPStdio_SharedPeerCrash`), `cmd/arbitron/layout_http_recovery_test.go`.
  - Unchanged on path: `adapter/filesystem/file_reader.go`, `adapter/filesystem/file_reader_settle_test.go`, `adapter/filesystem/workflow_operations.go:reservedWorkflowFile`, `domain/workflow/commit_source.go`, `domain/workflow/service_revision.go`, `cmd/arbitron/main.go:initDomain` (lines 650, 675), `cmd/arbitron/layout_wiring.go:newLayoutStack` (line 127 `repo.SetReadSettler`), `adapter/http/layout_handler.go:writeMutationPending`, `adapter/mcp/tools_layout.go:mutationPendingToolError`.
- **Folded in:**
  1. `adapter/filesystem/durable.go:durableFiles.renamePath`. Check that all three renames in `RollbackTreeCommit` now go through `r.files.renamePath`. Check that no production constructor sets `durableFiles.rename`, so production uses `os.Rename`; grep for `rename:` outside `*_test.go`.
  2. `cmd/arbitron/layout_http_recovery_test.go` (4-line change). Check that the edit does not weaken a sink assertion.
- **Review 1 — wiring lens:**
  1. `main.go:650` constructs exactly one `FileReaderAdapter`. Check that this same instance reaches HTTP, MCP and the engine, and that `SetSettledRepository(workflowRepo)` (`main.go:675`) runs before any of them serves.
  2. `layout_wiring.go:127` installs `repo.SetReadSettler(ls.commits)`, so `FileReaderAdapter.settled` takes the locked branch in production. Show that deleting `main.go:675` turns `cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPE2E_PrivateSwapGapRawFirst` red before any other settling read or poll (G2); distinguish failure of that mutation test from a runtime error policy for an intentionally unwired reader.
  3. `CommitSource.Settle` is reached both from raw reads (`file_reader.go:46`) and from `WorkflowRepo.Load`'s read settler. Grep `readSettler.Settle` and name both call sites.
  4. The real killed-process tests (`cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPPrivateSwapProcess`, `cmd/arbitron/layout_mcp_stdio_crash_test.go:TestLayoutMCPStdio_SharedPeerCrash`) must assert the sink. That means the response bytes equal the committed manifest or the fixed unavailable error, and post-settlement bytes are asserted too, not just a status code.
  5. `adapter/filesystem/commit_rollback_crash_test.go:TestCommitProtocol_RollbackSwapGap` must inject the crash through `durableFiles.rename` at the real swap gap inside `RollbackTreeCommit`, not around it.
- **Review 2 — failure lens:**
  1. Crash between the aside rename and the staging rename, leaving the destination absent: `withExisting` must settle before choosing a scope. A missing private tree must not fall through to a shared copy. Tests: `adapter/filesystem/file_reader_settle_test.go:TestFileReader_MissingTreeSettlesBeforeScopeSelection`, `adapter/filesystem/file_reader_settle_test.go:TestFileReader_MissingPrivateTreeFailsClosed`.
  2. A visible but unproven tree with no backup stays pending and is never rolled forward (`domain/workflow/commit_source_boundary_test.go:TestCommitSource_VisibleUnprovenWithoutBackupStaysPending`). An unlinked tree uses exact proof (`domain/workflow/commit_source_boundary_test.go:TestCommitSource_UnlinkedTreeUsesExactProof`).
  3. A settlement I/O error keeps journal evidence and partial success, and a re-run converges (`domain/workflow/commit_source_boundary_test.go:TestCommitSource_SettlementIOKeepsEvidenceAndPartialSuccess`).
  4. If the staging rename in `RollbackTreeCommit` fails, the aside is renamed back (`errors.Join` branch, `tree_commit.go:240-244`). Name the test that drives that branch, or add one.
  5. A held destination lock: `rawReadSettleTimeout` bounds the wait and yields an error, never unsettled bytes.
  6. Non-reserved files skip settlement by design (`reservedWorkflowFile`). Confirm that a script resolved through `ResolvePath` during a half-swap cannot come from the aside or staging directory.
- **Seed evidence:**
  - `grep -rn SetSettledRepository` → `cmd/arbitron/main.go:675` (only production caller); `adapter/filesystem/file_reader.go:35`.
  - `file_reader.go:40` `if r.repo == nil || r.repo.readSettler == nil || !reservedWorkflowFile(relativePath) { return fn() }` serves unsettled bytes when unwired. `adapter/filesystem/file_reader_settle_test.go:TestFileReader_OrdinaryFilesAndUnwiredReaderSkipSettlement` blesses that behaviour. Logged as gap G2.
  - `grep -rn RollbackTreeCommit` (production) → `commit_source.go:158,194`, `service_revision.go:237`, `service_revision_copy.go:97`, `service_revision_move.go:195`, `domain/samples/seeder_revision.go:110`.

### Area 2 — a rename or ownership transfer settles only its own relocation intent, peers never see a half-moved tree, and a killed move is decided by registry authority

- **Promise:** Contracts §6.18 ("Active rename"). `renameRevision` creates an operation-private context while it holds BOTH sorted endpoint locks. `Stamp` is the only call that receives it. `Settle` skips only that exact intent. Every other covering intent and every external read still gets `ErrMutationPending`. Recovery after a killed move uses registry authority, never folder existence.
- **Entry:** `domain/workflow/rename.go:Service.Rename` → `domain/workflow/service_revision_move.go:renameRevision`; `domain/workflow/admin_ops.go:Service.TransferOwnership`.
- **Sink:** the registry entry, the destination tree, `RenamePublication` on `published_workflows`, and the relocated `layout_revisions` destination; `EndRelocation` removes the intent.
- **Files:**
  - Changed (tests): `domain/workflow/commit_source_context_test.go`, `domain/workflow/revision_stamper_relocation_test.go`, `domain/workflow/revision_stamper_test.go`, `domain/workflow/service_revision_test.go`, `cmd/arbitron/layout_commit_rename_test.go`, `cmd/arbitron/layout_http_peer_test.go` (`cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPE2E_RenamePeerCrash`), `cmd/arbitron/layout_mcp_stdio_crash_test.go` (`cmd/arbitron/layout_mcp_stdio_crash_test.go:TestLayoutMCPStdio_RenamePeerCrash`), `cmd/arbitron/layout_surfaces_recovery_test.go`, `cmd/arbitron/service_revision_authoring_e2e_test.go`.
  - Unchanged on path: `domain/workflow/commit_source.go` (`withOwnRelocation`, `ownsRelocation`, `Settle`), `domain/workflow/service_revision_move.go` (`renameRevision`, `rollbackMove`), `domain/workflow/rename.go`, `domain/workflow/admin_ops.go`, `domain/workflow/revision_stamper.go:Stamp`, the revision journal (`BeginRelocation`, `EndRelocation`, `RelocationIntents`), the `Registry.Resolve` implementation.
- **Folded in:** none. This branch adds no production line on this path, only real-root acceptance.
- **Review 1 — wiring lens:**
  1. `withOwnRelocation` has exactly one production call site, `service_revision_move.go:153`. It runs inside the callback that already owns both endpoint locks. `stampCtx` goes only to `Stamp`, not to `CopyTreeStamped` or `PrepareTreeCommit` (grep `stampCtx`).
  2. `ownsRelocation` matches the exact intent ID only (`commit_source.go:89`). Test: `domain/workflow/commit_source_context_test.go:TestCommitSource_ActiveRenameContextMatchesOnlyItsIntent`.
  3. `TransferOwnership` takes the same relocation path. Name the `BeginRelocation` call it reaches.
  4. `cmd/arbitron/layout_commit_rename_test.go:TestCommitProtocol_RenameProcess` drives a real subprocess rename through `initDomain` and asserts the registry, tree, publication and row destination sinks.
  5. No exported wrapper or option bypasses settlement. Grep for exported functions returning a context built by `withOwnRelocation`.
- **Review 2 — failure lens:**
  1. A move killed before and after the registry update is recovered by `Registry.Resolve` choosing the endpoint (`cmd/arbitron/layout_commit_rename_test.go:TestCommitProtocol_RenamePeerCrash`, `cmd/arbitron/layout_http_peer_test.go:TestLayoutHTTPE2E_RenamePeerCrash`, `cmd/arbitron/layout_mcp_stdio_crash_test.go:TestLayoutMCPStdio_RenamePeerCrash`).
  2. A peer read during a live intent returns `ErrMutationPending` (HTTP 503 / MCP error), not either half. A reviewed graph is retained until settlement (`cmd/arbitron/layout_surfaces_recovery_test.go:TestLayoutSurfaces_KilledRenameRetainsReviewedGraphUntilSettlement`).
  3. A relocation failure keeps the intent (`domain/workflow/revision_stamper_relocation_test.go:TestRevisionStamperRelocationFailurePreservesIntent`). A peer intent or unrelated destination still returns pending (`domain/workflow/revision_stamper_relocation_test.go:TestRevisionStamperActiveRenameContextIsNarrow`).
  4. Registry rollback on collision and cancellation (`cmd/arbitron/service_revision_authoring_e2e_test.go:TestServiceRevisionE2E_MoveRegistryRollback`, `cmd/arbitron/service_revision_authoring_e2e_test.go:TestServiceRevisionE2E_MoveCollisionAndCancellation`).
  5. A cancelled caller context during the callback: `rollbackMove` uses `context.WithoutCancel` plus `postcommitTimeout` (`service_revision_move.go:191`). Show that a cancelled rename still rolls back the tree or the destination.
- **Seed evidence:**
  - `grep -rn withOwnRelocation` (production) → `domain/workflow/commit_source.go:50`, `domain/workflow/service_revision_move.go:153` only.
  - `grep -rn ownsRelocation` → `commit_source.go:54,89`.
  - `service_revision_move.go:146` `BeginRelocation(ctx, scope, name, target.Scope, newName, id)` comes before the stamped copy.

### Area 3 — a boot seed interrupted after its registry claim or after commit is compensated or resumed from its exact acknowledged input, never adopted from a folder

- **Promise:** Contracts §6.18 ("Seed acknowledgement", "Abandoned fresh seed claim") and commit `315da30`. `seedRevision` journals the prepared request before `Registry.Register` and before any visible tree. `CommitSource.releaseSeedClaim` removes only that exact abandoned claim. If `ledger.Record` fails after commit, the next boot resumes through `Requester.AcknowledgedArrangement`, but only on exact equality. An existing or unproven tree is never overwritten.
- **Entry:** `cmd/arbitron/main.go:846` `seedSampleWorkflows` → `cmd/arbitron/sample_seed.go` → `domain/samples/seeder.go:Seeder.Seed` → `domain/samples/seeder_revision.go:seedRevision`.
- **Sink:** the registry claim, the shared tree, the `published_workflows` row, the seed ledger (`filesystem.NewSeedLedger`), and the `layout_revisions` row through `RevisionStamper.Committed`.
- **Files:**
  - Changed (tests): `cmd/arbitron/sample_seed_claim_test.go`, `cmd/arbitron/sample_seed_resume_test.go`, `cmd/arbitron/sample_seed_durable_test.go`, `domain/layout/requester_acknowledged_test.go`.
  - Unchanged on path: `domain/samples/seeder.go`, `domain/samples/seeder_revision.go` (`seedRevision`, `recordSeed`), `domain/workflow/revision_stamper.go:EnsureStamped` (lines 240-290), `domain/workflow/commit_source.go:releaseSeedClaim` (lines 198-230), `domain/workflow/arrangement.go:AcknowledgedArrangementReader`, `domain/layout/requester.go:Requester.AcknowledgedArrangement`, `adapter/sqlite/layout_repo.go:Retry`, `domain/layout/query.go:retry` (paired row 6), `cmd/arbitron/layout_wiring.go:122-128`, `cmd/arbitron/sample_seed.go`, the filesystem seed ledger.
- **Folded in:** none.
- **Review 1 — wiring lens:**
  1. `sample_seed.go:24` passes the live `ds.layout.stamper` to `SetRevisionStamper`. Prove that the legacy `seedOne` branch (`seeder.go:60`, taken when `stamper == nil`) is unreachable in production.
  2. The stamper's `Requester` is `ls.requester` (`layout_wiring.go:128`), which satisfies `AcknowledgedArrangementReader` (`requester.go:248`). So the type assertion at `revision_stamper.go:274` is live, not an optional empty reader.
  3. `seedRevision` records the prepared request before `Registry.Register` (`seeder_revision.go:131`) and before the tree is created. Cite the line order.
  4. `recordSeed` calls `stamper.Committed` before `ledger.Record` (`seeder_revision.go:181-188`).
  5. `cmd/arbitron/sample_seed_claim_test.go:TestSeedSampleWorkflows_PostRegisterClaimControls` and `cmd/arbitron/sample_seed_resume_test.go:TestSeedSampleWorkflows_ResumeRequiresExactPublication` assert the registry, ledger, publication and row sinks through `initDomain`.
- **Review 2 — failure lens:**
  1. Crash right after `Register`, before the tree intent: `releaseSeedClaim` removes only that prepared ID. An absent, foreign or different claim is untouched.
  2. Ledger failure after `Committed`: resume needs exact revision, scope, owner, name, reason, time, ID, retained and input equality. A mismatched publication revision, ID, publisher or source is not ledgered.
  3. A store outage in `requester.go:Requester.AcknowledgedArrangement` (non-`ErrNotFound`, `revision_stamper.go:276`) propagates. Saved content survives, no new token is minted, the ledger is not reordered. Name what boot does with that error.
  4. An I/O error in `commit_source.go:CommitSource.releaseSeedClaim` keeps the prepared evidence for the next settlement.
  5. `seeder_revision.go:Seeder.recordSeed` runs under `context.WithoutCancel` plus `seedCleanupTimeout`. A cancelled boot cannot skip `Committed` once the tree is visible.
  6. `seeder_revision.go:seedRevision` reaches `RevisionStamper.EnsureStamped` before `Registry.Register`; `CommitSource.Settle` must refuse a covering peer relocation with `ErrMutationPending`. Assert no fresh claim, ledger entry or tree is created, including when another already-running process is moving the sample.
- **Seed evidence:**
  - `grep -rn AcknowledgedArrangement` (production) → `domain/layout/requester.go:73,248`, `domain/workflow/arrangement.go:33`, `domain/workflow/revision_stamper.go:274-275`.
  - `grep -rn releaseSeedClaim` → `domain/workflow/commit_source.go:198,211`.
  - `cmd/arbitron/sample_seed.go:24-26` calls `SetRevisionStamper`, `SetMutationCoordinator` and `SetRegistry` on every boot.

### Area 4 — authorised workflow and instance layout reads and retries preserve identity, cancellation and revision ownership through the response, generation bump and dashboard fallback

- **Promise:** Contracts §6.18 and commits `1b378f1`, `dd81c44`, `c8fc0a6`, `b59fd27`. HTTP/MCP reads and retries share `QueryService`: authenticate and conceal before exposing a revision, carry cancellation to the store, and lock all private/shared resolution candidates before reauthorising scope and token. Historical retries use the authorised frozen snapshot and committed relocated envelope. `Reader.View` derives group ownership from that same authorised input even without a result; invalid saved groups remain failed and retryable, with no reviewed geometry. The Go/TS wire and dashboard preserve that status and reject unsupported v1 group geometry.
- **Entry:** `adapter/http/layout_handler.go:GetWorkflowLayout`, `GetInstanceLayout`, `RetryWorkflowLayout`, `RetryInstanceLayout`; `adapter/mcp/tools_layout.go:workflowArrangement`, `workflowLayoutRetryHandler`, `instanceLayoutRetryHandler` → `domain/layout/query.go:WorkflowLayout`, `InstanceLayout`, `RetryWorkflow`, `RetryInstance`.
- **Sink:** `Requester.Retry` → `Store.Retry` (`layout_revisions.job_generation`) and `RetryAck`; authorised `Reader.View` JSON → `dashboard/src/api/client.ts` → `ArrangedGraph.tsx:resolveDisplayGeometry` and `validate.ts:validateGeometry` → response/DOM, or concealed not-found.
- **Files:**
  - Changed (tests): `adapter/http/layout_context_test.go`, `adapter/http/layout_handler_test.go`, `adapter/mcp/tools_layout_context_test.go`, `adapter/mcp/tools_layout_test.go`, `domain/layout/query_lock_test.go`, `domain/layout/query_test.go`, `domain/layout/testhelpers_test.go`, `cmd/arbitron/layout_http_context_test.go`, `cmd/arbitron/layout_mcp_context_test.go`, `cmd/arbitron/layout_query_retry_acceptance_test.go`, `cmd/arbitron/layout_query_publication_test.go`, `cmd/arbitron/layout_query_concurrency_test.go`.
  - Query/retry path: `domain/layout/query.go` (`RetryWorkflow` 134-185, `RetryInstance` 187-235, `committedEnvelope`, `historicalDestination`, `retry`), `domain/workflow/layout_access.go` (`LayoutRetryTargets`, `AuthorizeLayoutRetry`, `AuthorizeInstanceLayoutRetry`), `domain/layout/requester.go`, `domain/layout/reader.go`, `adapter/sqlite/layout_repo.go:Retry`, `cmd/arbitron/layout_wiring.go:attachQuery`, `cmd/arbitron/main.go:1093`.
  - Changed: `domain/layout/reader.go`, `domain/layout/query.go`, `dashboard/src/graph/arranged/types.ts`, `dashboard/src/graph/arranged/validate.ts`, `dashboard/src/graph/arranged/layoutView.contract.json`, `dashboard/src/graph/arranged/layoutViewContract.test.ts`, `dashboard/src/graph/arranged/validate.test.ts`, `domain/layout/reader_groups_test.go`, `domain/layout/query_groups_test.go`, `cmd/arbitron/layout_group_surfaces_test.go`, `cmd/arbitron/graph_state_e2e_test.go`, `cmd/arbitron/group_dead_parent_recovery_e2e_test.go`.
  - Unchanged on path: `dashboard/src/graph/arranged/ArrangedGraph.tsx`, `dashboard/src/routes/WorkflowReview.tsx` (`groups={groupLayout.graph}`), `dashboard/src/routes/ExecutionView.tsx`, `dashboard/src/graph/groupLayout.ts`, `domain/engine/graph_state.go:GraphStateReader`, `domain/layout/geometry.go:validateGeometryOptions`.
- **Folded in:**
  1. Zero-policy parity (contracts §6.18): `validate.test.ts` adds Go-emitted zero `margin`/`nodeTextSlack`/`crossingSweeps` fixtures. Check that both `geometry.go:validateGeometryOptions` and `validate.ts:validateGeometry` accept them, and reject negative, fractional and non-finite values.
  2. `graph_state_e2e_test.go` and `group_dead_parent_recovery_e2e_test.go` add test-only gates and SQLite triggers for main's group execution. Check that the gates hold real production transitions, that every trigger is dropped before the asserted phase, and that no assertion was weakened.
- **Review 1 — wiring lens:**
  1. `main.go:1093` `ls.attachQuery(workflowSvc, eng)` injects the real Service and Engine. `QueryService.mutations` is `ls.mutations`, the same coordinator saves use (`layout_wiring.go:31`).
  2. `RetryWorkflow` calls `LayoutRetryTargets` (`query.go:155`) and checks that the first-resolved destination is included before `WithMutations` (`query.go:163`).
  3. Inside the lock, `AuthorizeLayoutRetry` reloads and `checkExpected` compares scope AND token before `Requester.Retry`. Cite the lines.
  4. HTTP and MCP pass the request or tool context (identity plus cancellation), not `context.Background()`: `adapter/http/layout_context_test.go:TestLayoutHTTP_ForwardsIdentityAndCancellation`, `adapter/mcp/tools_layout_context_test.go:TestLayoutMCP_ForwardsIdentityAndCancellation`, `adapter/mcp/tools_layout_context_test.go:TestFakeMCPLayouts_InstanceReadContext`. Check that these fakes assert the identity they received rather than ignoring it.
  5. `domain/layout/query_lock_test.go:TestQueryService_RetryWorkflow_CoordinatorContextReachesGeneration` shows the locked context reaches the generation bump.
  6. `reader.go:186-188` calls `BuildGraph(wf)` once and sets `v.Groups` before every early return: empty `SavedRevision`, missing row, pending, and store outage or `cachedFallback` (`domain/layout/reader_groups_test.go:TestReader_GroupsBoundToInputEvenWithoutResult`).
  7. `query.go:queryDefinition` tolerates only `ErrInvalidGroups` after read authorisation and through retry `checkExpected`; `ready` checks collaborator availability, not graph validity (`domain/layout/query_groups_test.go:TestQueryService_InvalidSavedGroupsRemainReadableAndRetryable`).
  8. The instance read uses the frozen snapshot manifest, so `View.Groups` keeps the old configs and order after a later save. `cmd/arbitron/layout_group_surfaces_test.go:TestLayoutGroupSurfaces_RevisionBoundOwnershipBeforeGeometry` must assert this through authorised HTTP.
  9. The Go JSON tag `groups` is listed in `layoutView.contract.json` and typed in `types.ts:LayoutView.groups`, and the parity test reads the Go side.
  10. Dashboard consumers: `WorkflowReview.tsx:404` passes `groups={groupLayout.graph}` (client analysis), not `view.groups`. `ArrangedGraph.tsx:70-79` `groupFallback` drops the geometry. Record which surface reads `view.groups` today (none, P3T15.5; see Won't Fix W1).
- **Review 2 — failure lens:**
  1. A same-token private shadow conflicts. A shared-only retry succeeds. Missing or empty target sets fail without a bump (`domain/layout/query_lock_test.go:TestQueryService_RetryWorkflow_InvalidResolutionTargetsPreserveRow`, `cmd/arbitron/layout_query_retry_acceptance_test.go:TestLayoutQueryE2E_InvalidRetryTargetsPreserveBytesAndGeneration`).
  2. An unauthorised shared retry preserves bytes and generation (`cmd/arbitron/layout_query_retry_acceptance_test.go:TestLayoutQueryE2E_UnauthorizedSharedRetryPreservesBytesAndGeneration`). An unauthorised read is concealed with the same shape as a missing workflow.
  3. Cancellation between lock acquisition and `Store.Retry` cannot advance the store (`cmd/arbitron/layout_http_context_test.go:TestLayoutHTTPE2E_CanceledRetryCannotAdvanceStore`, `cmd/arbitron/layout_mcp_context_test.go:TestLayoutMCPE2E_CancellationAndConcealmentPreserveStore`).
  4. Retry first, then a later save: the old publication is rejected (`cmd/arbitron/layout_query_publication_test.go:TestLayoutQueryE2E_RetryFirstLaterSaveRejectsOldPublication`).
  5. Historical retry after rename, transfer and delete prefers the committed relocated envelope and never adopts a prepared one (`cmd/arbitron/layout_query_retry_acceptance_test.go:TestLayoutQueryE2E_HistoricalRetryAfterRenameTransferDelete`). Check its row destination assertions.
  6. Concurrent retry plus save (`layout_query_concurrency_test.go`) linearises under the lock. Exactly one generation bump per accepted retry.
  7. Invalid groups: the deferred override (`reader.go:192-204`) nulls `Geometry`/`Contract`/`Evidence`, sets `DisplayFailed` unless a job is active, and sets `RetryEligible` only when none is. It also sets `FailureReason` while a job is pending. Check the UI does not show a failure during pending.
  8. Ordinary graphs leave `Groups` nil and omit it from JSON (`domain/layout/reader_groups_test.go:TestReader_OrdinaryInputHasNoGroupOwnership`). Malformed basic nodes or edges still error, because only `ErrInvalidGroups` is tolerated.
  9. `validate.ts` rejects a non-empty group array and a non-empty `elementId` ("group geometry is not supported by v1"), and accepts null or empty ones.
  10. Store outage for a group workflow: `cachedFallback` never restores reviewed status, and `Groups` is still present.
  11. Client group analysis returns null while the manifest has groups, so `groupFallback` is false. Check that the retained v1 ordinary-box best for a failed group row is drawn as unreviewed or failed, never with a reviewed badge.
  12. `query.go:queryDefinition` must not turn the group-validation exception into an authorisation bypass: `WorkflowLayout`/`InstanceLayout` load authorised input first, and `RetryWorkflow`/`RetryInstance` authorise before `checkExpected`. Exercise a hidden invalid-group workflow over HTTP and MCP and assert concealed errors plus unchanged generation.
- **Seed evidence:**
  - `grep -rn LayoutRetryTargets` (production) → `domain/workflow/layout_access.go:51` (producer), `domain/layout/query.go:23,155` (consumer).
  - `cmd/arbitron/layout_wiring.go:31` `layout.NewQueryService(ls.reader, ls.requester, workflows, instances, ls.mutations)`; `cmd/arbitron/main.go:1093` `ls.attachQuery(workflowSvc, eng)`.
  - `grep -rn "view\.groups\|layout\.groups" dashboard/src` (non-test) → no consumer. `ArrangedGraph.tsx:70` derives `groupFallback` from the `groups` prop.
  - `grep -rn ErrInvalidGroups` (production) → `graph.go:23,110,116,130`, `group_geometry.go:122`, `query.go:74`, `reader.go:192`.

### Area 5 — the stdio MCP transport writes only JSON-RPC to stdout, authors as the default user only in local mode, and boots beside a running server without a lock error

- **Promise:** Contracts §6.18 ("Stdio principal/logging", P2T9.8) and commits `cfe19ac`, `b7ab8ab`. `SetupLogging` sends console text to stderr. `arbitron mcp` and `serve --mcp-stdio` resolve `stdioContext`: in local mode the default user with `LocalAdminIdentity`; in org or edge mode no identity, so authoring is denied. Boot errors never write to stdout. `sqlite.Open` sets `busy_timeout` before enabling WAL, so a second process waits instead of failing.
- **Entry:** the `mcp` command in `cmd/arbitron/main.go` (lines 1630-1642) and the `serve --mcp-stdio` goroutine (lines 1567-1582); `initDomain` → `observability.SetupLogging` (`main.go:521`) → `adapter/sqlite/db.go:Open`.
- **Sink:** `adapter/mcp/server.go:80` `stdio.Listen(ctx, os.Stdin, os.Stdout)` frames; SQLite rows for create, update and retry.
- **Files:**
  - Changed: `adapter/observability/logging.go`, `adapter/observability/logging_test.go`, `adapter/sqlite/db.go`, `adapter/sqlite/db_test.go`, `cmd/arbitron/layout_stdio_protocol_test.go`, `cmd/arbitron/layout_e2e_test.go` (`runStdioSession`), `cmd/arbitron/layout_mcp_stdio_crash_test.go` (its harness).
  - Unchanged on path: `cmd/arbitron/stdio_identity.go:domainStack.stdioContext`, `cmd/arbitron/main.go`, `adapter/mcp/server.go:ServeStdio`, `domain/identity/builder.go:LocalAdminIdentity`, `adapter/http/auth_middleware.go:235` (the HTTP local-identity counterpart).
- **Folded in:**
  1. `adapter/sqlite/db.go` pragma order. Check that `busy_timeout` is first and that `adapter/sqlite/db_test.go:TestOpen_WaitsForStartupLockBeforeEnablingWAL` holds `BEGIN EXCLUSIVE` and observes `Open` succeed only after release.
  2. Moving `serve` console logs from stdout to stderr changes operations. Grep `docs/install` and `install.sh` for anything that captures arbitron stdout.
- **Review 1 — wiring lens:**
  1. `logging.go:115` builds the text handler on `os.Stderr`, and `main.go:521` is the only `SetupLogging` call. Grep confirms no production `os.Stdout` write other than `adapter/mcp/server.go:80`.
  2. Both stdio launch sites (`main.go:1574`, `main.go:1639`) pass `ds.stdioContext`'s context to `ServeStdio`.
  3. `stdioContext` in local mode uses `GetDefaultUser` plus `LocalAdminIdentity(uid, ds.groupCfg)`, the same identity HTTP local mode builds (`auth_middleware.go:235`).
  4. `cmd/arbitron/layout_stdio_protocol_test.go:TestLayoutStdioProtocol_LocalAuthoringAndRetry` drives a real subprocess through create, update and retry and asserts the SQLite rows. `runStdioSession` (`layout_e2e_test.go`) now decodes every stdout line as JSON-RPC, with no prefix filter.
  5. `adapter/observability/logging_test.go:TestSetupLogging_ConsoleUsesStderrOnly` fails if the handler writes to stdout.
- **Review 2 — failure lens:**
  1. Non-local mode: with no identity, every authoring tool AND layout retry is denied (`cmd/arbitron/layout_stdio_protocol_test.go:TestLayoutStdioProtocol_NonLocalDeniesAuthoring`). Check that retry and publish are covered, not just create.
  2. Boot failure (bad config, unreadable DB) writes nothing to stdout, including before `SetupLogging` runs and in cobra's error printing (`cmd/arbitron/layout_stdio_protocol_test.go:TestLayoutStdioProtocol_BootErrorsNeverWriteStdout`).
  3. `stdio_identity.go:domainStack.stdioContext` returns a `GetDefaultUser` error: `mcp` exits with an error, while `serve --mcp-stdio` logs and returns with HTTP still serving. Confirm both are intended and visible.
  4. EOF and cancellation join the session. No response frame after EOF (`cmd/arbitron/layout_e2e_test.go:runStdioSession` post-EOF loop).
  5. DB contention: `adapter/sqlite/db.go:Open` waits up to `busy_timeout=5000` ms, then surfaces an error. It never proceeds without WAL silently.
- **Seed evidence:**
  - `grep -rn "os.Stdout\|fmt.Println\|fmt.Printf(" --include=*.go adapter domain plugin cmd` (non-test) → only `adapter/mcp/server.go:80`.
  - `grep -rn stdioContext` → `cmd/arbitron/stdio_identity.go:18`, `cmd/arbitron/main.go:1574,1639`.

### Area 6 — saved revisions reach the shipped browser and rendered review with measured text and captured ownership; ordinary samples pass, unsupported groups and font failures remain explicit

- **Promise:** P3T15.1 commits `f3b37cc`, `b59fd27`, `44390f5`, `2864188`, `e18ab53` and P3T2/P3T7 renderer release commits. The real Worker/Coordinator measures immutable input with the shipped Chromium and bundled fonts, sizes group header supplements, and forwards independent ownership to render/review on every attempt. Ordinary pr-review, feature-pipeline and ralph-loop reach a passed SQLite result within five attempts and display it in both routes. Group input remains failed at rendering with its unreviewed best retained until W1 is implemented. Server font/render failures retain saves and Retry; client font failures offer Reload reviewed fonts without changing the saved revision or job generation.
- **Entry:** `cmd/arbitron/layout_wiring.go:newLayoutStack` installs `newLayoutRenderReviewer` → `domain/layout/worker.go:Worker.processRevision` → `coordinator.go:Coordinator.Run` → `BuildGraph(job.Manifest)` → `BrowserRenderer.Measure`/`Render` → `MeasureSurface.tsx`/`RenderSurface.tsx`.
- **Sink:** `adapter/renderreview/reviewer.go:ReadabilityReviewer.Review` → `Store.Complete` in SQLite (passed with evidence for ordinary samples; failed at rendering with retained candidate for groups); WorkflowReview/ExecutionView DOM and `ArrangedGraph.tsx` font-reload control.
- **Files:**
  - Changed: `domain/layout/graph.go`, `domain/layout/group_geometry.go`, `domain/layout/group_size.go`, `domain/layout/size.go`, `domain/layout/measure.go`, `domain/layout/preflight.go`, `domain/layout/arrange.go`, `domain/layout/geometry.go`, `domain/layout/coordinator.go`, `domain/layout/render.go`, `domain/layout/layouttest/fakes.go`, `adapter/renderreview/browser.go` (`renderJob`, `renderJobDocument`), `adapter/renderreview/reviewer.go` (`checkIdentity`), `dashboard/src/render/job.ts` (`groupsForJob`, `workflowForJob`, `parseJob`), `dashboard/src/render/RenderSurface.tsx` (`buildSurfaceModel`).
  - Changed (tests and fixtures): `domain/layout/graph_group_test.go`, `domain/layout/arrange_group_input_test.go`, `domain/layout/arrange_group_test.go`, `domain/layout/coordinator_groups_test.go`, `domain/layout/group_measure_test.go`, `domain/layout/group_size_test.go`, `domain/layout/group_input_validation_test.go`, `domain/layout/group_geometry_wire_test.go`, `domain/layout/measure_test.go`, `domain/layout/preflight_semantics_test.go`, `domain/layout/layouttest/fakes_test.go`, `adapter/renderreview/browser_groups_test.go`, `adapter/renderreview/reviewer_groups_test.go`, `dashboard/src/render/job.groups.test.ts`, `dashboard/src/graph/arranged/groupGeometry.contract.test.ts`, `groupGeometry.wire.json`, `groupGeometry.fields.json`, `groupText.contract.json`, `cmd/arbitron/layout_groups_e2e_test.go`.
  - Unchanged on path: `domain/workflow` group analysis (`AnalyzeGroups`, `HasGroups`, `ParseGroupConfig`), `domain/layout/worker.go`, `domain/layout/arrange.go` callers, `adapter/sqlite` layout repo `Complete`.
  - Changed: `Dockerfile`, `dashboard/package.json`, `dashboard/src/render/report.ts`, `dashboard/src/render/report.test.ts`, `adapter/dashboard/dist/*`, `adapter/renderreview/browser_boundary_test.go`, `cmd/arbitron/layout_build_test.go`, `cmd/arbitron/layout_render_e2e_test.go`, `cmd/arbitron/layout_font_recovery_test.go`, `cmd/arbitron/layout_component_surfaces_test.go`, `cmd/arbitron/layout_surfaces_e2e_test.go`, `domain/layout/coordinator_regression_test.go`, `dashboard/scripts/test-arranged-markers-browser.mjs`, `dashboard/scripts/test-font-portability-browser.mjs`, `dashboard/tests/arranged-markers-fixture.tsx`, `dashboard/tests/font-portability-fixture.tsx`.
  - Unchanged on path: `adapter/renderreview/browser.go` (`findBrowser`, `browserCandidates`, launch), `adapter/renderreview/reviewer.go`, `dashboard/src/render/RenderSurface.tsx`, `dashboard/src/render/MeasureSurface.tsx`, `dashboard/src/graph/arranged/fonts.ts`, `dashboard/src/graph/arranged/ArrangedGraph.tsx`, `Makefile` (`test-render`, `test-all`), `.github/workflows/layout.yml`.
- **Folded in:**
  1. `geometry.go` adds `Attachment.ElementID` and the four `Geometry` group arrays. Check that v1 `Validate` rejects non-empty arrays and element attachments, and that their JSON tags match the TS mirror (`domain/layout/group_geometry_wire_test.go:TestGroupGeometry_FieldParity`).
  2. `group_geometry.go:groupIDJSON` encodes IDs like `JSON.stringify`, including U+2028/U+2029. Check against `groupText.contract.json` via `domain/layout/group_measure_test.go:TestGroupGeometry_CanonicalIDs`.
  3. `layouttest/fakes.go:validateUnitGeometry` rejects any non-nil groups. Check that no unit test reaches a group "pass" through these fakes.
  4. Rebuilt `adapter/dashboard/dist`. Check that it matches a fresh `make ui` (no stale `render-*.js` or `ArrangedGraph-*.js`).
  5. `dashboard/package.json` scripts `test:font-portability` and `test:arranged-browser` (now including the markers script). Check whether any gate runs them (gap G1).
  6. `Dockerfile` pins. Check that the runtime stage (not a build stage) installs chromium and font-noto*, sets `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true`, and that the installed binary path is in `browser.go:browserCandidates`.
- **Review 1 — wiring lens:**
  1. `BuildGraph` sets `Groups` and `capturedGroups` only when `workflow.HasGroups(wf)`. The worker feeds it `job.Manifest` decoded from the SQLite row, not the current file (`coordinator.go:153`; trace the `Job` construction in `worker.go`).
  2. One producer, three consumers: `measure.go:72` appends `groupTextItems`, `size.go:114` calls `reserveGroupHeaderText`, and `preflight.go:32` calls `checkGroupHeaderTextFits`. All three use `groupHeaderTextItems`, so the strings measured are the strings sized.
  3. `coordinator.go:234-258` fills `RenderRequest.WorkflowGroups` and `ReviewRequest.WorkflowGroups` from `cloneGroupGraph(g.Groups)` on every attempt (`domain/layout/coordinator_groups_test.go:TestRun_WorkflowGroupsAreCapturedAndIndependentlyFed`).
  4. `browser.go:renderJobDocument` embeds the whole `RenderRequest`, so `workflowGroups` reaches `job.ts:parseJob` (`adapter/renderreview/browser_groups_test.go:TestRenderJobDocument_ForwardsCapturedWorkflowGroups`).
  5. The real-root tests `cmd/arbitron/layout_groups_e2e_test.go:TestLayoutGroups_RealBrowserMeasuresCapturedGroupText` and `cmd/arbitron/layout_groups_e2e_test.go:TestLayoutGroups_RealRootCapturedInputCannotPassV1Capture` (run with `ARBITRON_REQUIRE_BROWSER=1`) assert the failed SQLite row, the retained candidate's measured header size and the Reader geometry, for branches, workflows and nested forms.
  6. `layoutRenderReviewerFactory` defaults to `newLayoutRenderReviewer` and is the only constructor `newLayoutStack` uses for both serve and MCP. Grep for overrides outside `*_test.go`.
  7. `cmd/arbitron/layout_build_test.go:TestLayoutBuild_DockerShipsPinnedBrowser` reads the final stage. `browser.go:182-184` lists `/usr/bin/chromium` and `/usr/bin/chromium-browser`.
  8. `RenderSurface.tsx:86` and `MeasureSurface.tsx:87` refuse to run when `!fontFamilyResolved`. The one-pass signature set in `report.ts:40-50` compares exactly what the old predicate did: unquoted family, weight, style, normalised range and loaded status.
  9. `cmd/arbitron/layout_render_e2e_test.go:TestLayoutRender_RealBrowserAcceptance` (with `ARBITRON_REQUIRE_BROWSER=1`) asserts `StatePassed` for pr-review, feature-pipeline and ralph-loop at the SQLite row, within the attempt limit of 5.
  10. Both routes draw the reviewed geometry (`layout_component_surfaces_test.go`). The markers browser test asserts six unique marker IDs and actual PNG ink.
  11. `browser.go:renderJobDocument` → `job.ts:parseJob`/`groupsForJob` → `RenderSurface.tsx:buildSurfaceModel` is one internal path. Run both ordinary-sample and group real-browser acceptance: ordinary jobs omit ownership (null is also tolerated), non-null empty ownership is rejected, and a manifest-typed group cannot lose ownership or pass v1 review. This absorbs former paired row 5.
- **Review 2 — failure lens:**
  1. Invalid config, topology or group-on-cycle → `ErrInvalidGroups` at `BuildGraph` → failed at arranging with no candidate (`domain/layout/graph_group_test.go:TestBuildGraph_InvalidGroupsFailClosed`, `cmd/arbitron/layout_groups_e2e_test.go:TestLayoutGroups_InvalidHistoricalInputFailsAtSavedRevisionSink`).
  2. Stale or edited ownership is rejected by `validateGroups` in both `Arrange` and `Geometry.Validate`, while an exact independent copy is accepted (`domain/layout/arrange_group_input_test.go:TestArrange_GroupInputMustMatchCapturedManifest`, `domain/layout/arrange_group_input_test.go:TestArrange_GroupOwnershipMustMatchCapturedNesting`, `domain/layout/arrange_group_input_test.go:TestArrange_GroupInputRejectsExtraOwnershipOnOrdinaryGraph`, `domain/layout/arrange_group_input_test.go:TestArrange_GroupInputAcceptsIndependentExactCopy`).
  3. A missing or corrupt measured supplement fails arrangement (`domain/layout/group_size_test.go:TestArrange_GroupHeaderRejectsMissingOrCorruptSupplements`). Insufficient clearance is a blocking preflight finding (`domain/layout/group_size_test.go:TestPreflight_GroupHeaderRequiresMeasuredSupplementClearance`).
  4. `group_size.go:14` discards `NodeIndex`'s ok. Show that it is unreachable, or fail closed (gap G4).
  5. The reviewer finds group nodes by display caption (`reviewer.go:128`, `TypeCaption == "parallel group"`), while `job.ts` uses manifest types. Show that they cannot diverge (gap G3). Ordinary graphs omit `workflowGroups` (omitempty), so `groupsForJob`'s `raw != null` branch passes them.
  6. A `cloneGroupGraph` error before render or review fails the attempt with its stage instead of panicking. The render failure keeps the candidate as best (`best = better(best, cand)`).
  7. Missing browser: boot logs a WARN (`layout_wiring.go:71-73`), every job fails at rendering with Retry, and saves are unaffected. A broken embedded build fails boot.
  8. A server font subset missing at measure or capture → failed and Retry-eligible, with saved labels retained (`cmd/arbitron/layout_font_recovery_test.go:TestLayoutSurfaces_ServerFontFailureRetainsSavedContentAndRetry`).
  9. Client font failure → "Reload reviewed fonts", no POST, revision, generation and digest unchanged (`layout_component_surfaces_test.go`).
  10. Stage-timeout entry race (`domain/layout/coordinator_regression_test.go:TestRun_StageTimeout` in `coordinator_regression_test.go`) and queued cancellation or launch timeout (`adapter/renderreview/browser_boundary_test.go:TestBrowserRenderer_QueuedCancellationAndLaunchTimeout`).
  11. `fontFamilyResolved` rejects unloaded faces and wrong range, style or weight (`report.test.ts`).
  12. A crashed browser is relaunched (`browser.go:254` `stopBrowser`). Name the test.
- **Seed evidence:**
  - `grep -rn WorkflowGroups` (production) → `domain/layout/render.go:20,134`, `domain/layout/coordinator.go:235,256`, `adapter/renderreview/reviewer.go:125-136`, `domain/layout/layouttest/fakes.go:75,177`.
  - `grep -rn "reserveGroupHeaderText\|checkGroupHeaderTextFits\|groupTextItems"` → `size.go:114`, `preflight.go:32`, `measure.go:72` (the only callers).
  - The P3T15 plan records the actual-root result: StateFailed/StageRendering, "reviewed group geometry is unavailable in v1", for all three forms.
  - `grep -rn "test:font-portability\|test:arranged-browser" Makefile .github/workflows` → no match; `layout.yml:42` runs only `make test-all`, which is `test test-ui test-render` (`Makefile:47`). Logged as gap G1.
  - `cmd/arbitron/layout_wiring.go:134` `ls.renderer, ls.reviewer, closeFn, err = layoutRenderReviewerFactory(cfg, logger)`.
  - `Dockerfile:56-59` adds `chromium=131.0.6778.108-r0`, `font-noto`, `font-noto-cjk`, `font-noto-emoji`; `Dockerfile:88` sets `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true`.
  - `layout_wiring.go:newLayoutStack` supplies the same `ls.renderer` to `NewCoordinator` for Measure and Render; `coordinator.go:Run` feeds the group supplements into that browser before sizing. The former worker/renderer areas read the same browser, reviewer and surface files.

### Area 7 — every authoring path for a group workflow validates child contracts before mutation locks and captures the exact saved manifest and identity into the arrangement request

- **Promise:** Commits `debd49a`, `315da30` and `cfe19ac`, plus the group save-path acceptance. HTTP and MCP create and update, publish, copy, duplicate, rename and sample seeding of a branches- or workflows-form group all validate child-workflow contracts before acquiring the fixed mutation lock set, then retain the existing final source/destination authorisation and settlement checks under those locks. Invalid writes are rejected with 422 or an MCP error and no revision. Each accepted save captures the exact saved manifest and workflow ID into the arrangement request. A copy of a legacy nil-ID source gets its own new identity.
- **Entry:** `domain/workflow/service.go` Create/Update (`validateGroupsForWrite` at line 317); `domain/workflow/service_revision_copy.go` (Publish, Copy); `domain/workflow/service_revision_move.go` (Duplicate, Rename); `domain/workflow/rename.go`; `domain/samples/seeder_revision.go`; HTTP `POST/PUT /api/v1/workflows`; MCP `workflow_create`/`workflow_update`.
- **Sink:** `workflow.json` plus `layout_revisions.Manifest` and `Destination.WorkflowID`, through `Requester.RequestArrangement`.
- **Files:**
  - Changed (tests): `cmd/arbitron/service_revision_groups_e2e_test.go`, `cmd/arbitron/service_revision_group_imports_e2e_test.go`, `cmd/arbitron/layout_availability_test.go` (`cmd/arbitron/layout_availability_test.go:TestLayoutE2E_NilCopiedIDStillRequests`).
  - Unchanged on path: `domain/workflow/service.go`, `domain/workflow/service_group.go` (`ChildContracts`, `validateGroupsAs`, `validateGroupsForWrite`), `domain/workflow/service_revision.go`, `domain/workflow/service_revision_copy.go`, `domain/workflow/service_revision_move.go`, `domain/workflow/revision_stamper.go:Stamp`, `adapter/filesystem/manifest_lock.go:WithMutations`, `domain/samples/seeder_revision.go`, the HTTP workflow handlers, `adapter/mcp` workflow tools.
- **Folded in:**
  1. `cmd/arbitron/layout_availability_test.go:TestLayoutE2E_NilCopiedIDStillRequests` was the one failing test at the branch point. `cfe19ac` changed it to expect a fresh ID. Check that production `Copy` assigns `uuid.New()` by design (`service_revision_copy.go:221`; contracts wording), that the row's `Destination.WorkflowID` equals it, and that the rewrite corrects a wrong expectation rather than weakening a real guarantee.
- **Review 1 — wiring lens:**
  1. `service.go:Create`/`updateWithGroupFindings` call `rejectInvalidGroups`/`validateGroupsForWrite` before `createRevision`/`updateRevision`; `service_revision_copy.go:publishRevision`/`copyRevision` call `rejectCopiedGroups` before `withMutations`; `service_revision_move.go:duplicateRevision`/`renameRevision` call `precheckCopiedGroups` before `withIdentityMutation`; `seeder_revision.go:seedRevision` calls `ValidateGroupImport` before `WithMutations`. Trace every precheck to `service_group.go:ChildContracts`, whose cross-workflow reads must not expand an owned lock set.
  2. `cmd/arbitron/service_revision_groups_e2e_test.go:TestServiceRevisionE2E_GroupAuthoringCapturedInput` asserts, for both forms over HTTP and MCP, that `rec.Manifest` is JSONEq to the saved `workflow.json` and that `Destination.WorkflowID` equals the saved ID.
  3. `revision_stamper.go:Stamp` captures the saved presentation and immutable manifest; compare its output with `Requester.RequestArrangement` and `cmd/arbitron/service_revision_groups_e2e_test.go:TestServiceRevisionE2E_GroupAuthoringCapturedInput` so manual positions cannot become arrangement input or diverge between file and row.
  4. `cmd/arbitron/service_revision_group_imports_e2e_test.go:TestServiceRevisionE2E_GroupImportsChildContractLockBoundary` drives publish, copy, duplicate and rename at the real root.
  5. `cmd/arbitron/service_revision_group_imports_e2e_test.go:TestServiceRevisionE2E_GroupSeedChildContractLockBoundary` covers both the valid case and a child-store failure.
- **Review 2 — failure lens:**
  1. `service_group.go:validateGroupsForWrite`/`ValidateGroupImport` reject invalid input before `revision_stamper.go:Stamp`; `cmd/arbitron/service_revision_groups_e2e_test.go:TestServiceRevisionE2E_GroupAuthoringCapturedInput` must assert HTTP 422/MCP `IsError`, no new revision/row and unchanged prior bytes.
  2. `seeder_revision.go:seedRevision` must not reach `recordSeed` after a child-contract lookup failure; `cmd/arbitron/service_revision_group_imports_e2e_test.go:TestServiceRevisionE2E_GroupSeedChildContractLockBoundary` checks no tree, identity, proof, ledger or row and a visible checker failure for retry on a later boot.
  3. `manifest_lock.go:WorkflowRepo.WithMutations` rejects a nested lock-set expansion. Keep child-contract prechecks outside the parent/rename lock set, and verify `service_revision_move.go:withIdentityMutation` and copy/publish callbacks still recheck authority and settle under the final locks. `cmd/arbitron/service_revision_group_imports_e2e_test.go:TestServiceRevisionE2E_GroupImportsChildContractLockBoundary` must fail if validation is moved under those locks.
  4. `service_revision.go:abandonUnwritten` and `service_revision_move.go:rollbackMove` preserve prior bytes on cancellation before commit and retain evidence on uncertain rollback; distinguish those cases from `Service.committed` finishing an already-visible save under `context.WithoutCancel`.
  5. `service_revision_copy.go:copyRevision` assigns `uuid.New()` to a nil-ID source copy; `cmd/arbitron/layout_availability_test.go:TestLayoutE2E_NilCopiedIDStillRequests` must assert the new ID, exactly one row and captured JSON equal to the saved manifest.
- **Seed evidence:**
  - `grep -rn validateGroupsForWrite` (production) → `domain/workflow/service.go:317`, `domain/workflow/service_group.go:224,231`.
  - `domain/workflow/service_revision_copy.go:71` `finalID := uuid.New()` (publish), `:221` `staged.ID, staged.Name = uuid.New(), name` (copy).
  - `go test ./cmd/arbitron -run 'TestLayoutE2E_NilCopiedIDStillRequests$'` → ok on HEAD (baseline failure no longer reproduces).
  - Critic evidence: P2T3 "Post-merge authoring coverage and validation lock boundaries" explicitly requires validation before locks. `service_revision_copy.go:publishRevision` and `seeder_revision.go:seedRevision` implement that order; the real-root import test documents the same requirement.

---

## Merge decisions

- **Authorised query/retry and group read/display → Area 4 (seeded Areas 4 and 7).** Same HTTP/MCP entry points, `query.go` authorisation and `queryDefinition` gate, Reader status and retry eligibility. The group exception cannot be judged safe without the authorisation and generation path. Merge-test clauses: same files; wiring depends on the other path.
- **Group worker and shipped browser/rendered review → Area 6 (seeded Areas 6 and 9).** The same Coordinator invokes Measure, Render and Review through the root's one browser; captured group text and header sizes cannot be judged fed separately from its fonts and real surface. Both reviews read `coordinator.go`, `browser.go`, `reviewer.go` and the render surfaces. Merge-test clauses: same files; wiring depends on the other path. Former paired row 5 is now an internal wiring check.
- **Retained sub-paths:** Areas 1, 2, 3 and 7 have distinct entry-to-sink traces: surviving raw reads, registry identity moves, boot seed acknowledgement, and pre-write group validation/capture. Their common commit helpers do not make them separate layers of one save operation. Area 5 owns process protocol and principal selection; Area 4 owns authorised queries; Area 6 owns execution of the arrangement job.
- **Small changes remain folded:** rename injection in Area 1; zero geometry policies and execution-test hardening in Area 4; SQLite startup ordering and stderr logging in Area 5; group DTOs, Docker/browser pins, embedded assets, font inventory and marker scripts in Area 6; nil-ID copy expectation in Area 7.
- **No split:** each merged area has one complete behaviour with its success and failure paths. Group ownership still crosses worker and authorised-reader consumers at paired row 7.
