# Workflow Auto-Arrangement — Phase 2 Task 3: All Service authoring commits

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§2, 6.2–6.3, 6.10–6.11. Replaces the former copy-visible-then-restamp and fresh-only publication rollback examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** One new saved revision/request per committed Create, Update, Publish, Copy, Duplicate or Rename; preserve existing revisions on owner transfer, and never arrange identity reconciliation or untouched workflows.

**Files:** modify `domain/workflow/service.go`, `ports.go`, `publishing.go`, `admin_ops.go`, `rename.go`, `delete.go`, `backfill.go`; modify `adapter/filesystem/workflow_repo.go` for stamped staging; create `domain/workflow/service_revision_test.go`, extend publication/rename/transfer/backfill tests. Update all Repository fakes/decorators to support the new method with argument-honoring implementations.

## Interfaces

Consumes P2T2's complete RevisionStamper, MutationCoordinator, TreeCommitter and RevisionPublicationStore ports. P2T7 supplies durable trees/locks; P2T6 supplies exact publication proof. Declare and implement:

```go
func (s *Service) SetRevisionStamper(st *RevisionStamper)
func (s *Service) SetMutationCoordinator(m MutationCoordinator)
// Add to Repository; implemented by filesystem.WorkflowRepo in this task:
CopyTreeStamped(ctx context.Context, src Scope, srcName string, dst Scope, dstName string, overwrite bool, stamp func(*Workflow) error) error
```

`CopyTreeStamped` copies to hidden staging, decodes its workflow, calls stamp there, writes the final stamped manifest into staging, and only then changes destination visibility. It holds both source/destination locks through its caller's operation context (P2T7). Stamp callback failure leaves the destination byte-for-byte unchanged. Final destination name/identity are set before Stamp so captured input equals saved input. Legacy `CopyTree` remains for identity-preserving moves; it must not be used for authoring copies followed by RestampCopy.

P2T2 declares `RevisionPublicationStore.RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, publication Publication) error`; P2T6 implements its atomic registry/publication transaction. `PublishRequest` gains `PreparedPublication func() (Publication, error)`. The callback returns the final proof produced inside WriteTree (including Revision/WorkflowID). `WriteAndRecord` checks RevisionPublicationStore availability BEFORE WriteTree, then calls PreparedPublication and CommitPublication after the successful stamped write. A nil callback retains the old Upsert branch only for isolated legacy/no-stamper callers. Production stamped paths always supply it; a missing proof implementation is an explicit precommit error. Its write callback must stamp before visibility and its rollback callback must restore the durable tree backup for BOTH fresh and overwrite. No late SetWorkflowID step can follow successful publication proof.

## Authoritative operation table

Each operation resolves authorization/destination, acquires the unique source/destination locks in sorted order, re-resolves authorization and identity inside those locks, and settles previous prepares before mutation. Re-resolution matters when a name moves between initial authorization and lock acquisition. Raw repository writes do not generate requests.

| Path | Operation inside the callback |
|---|---|
| Create local/private | Allocate/claim identity when registry-backed, Stamp, repo.Create, receipt, request. Failed create rolls registry claim back and abandons only definitely unwritten input. Local nil ID still arranges. |
| Update private | Load exact prior under lock, preserve stable destination identity, Stamp, repo.Update, receipt, request. Preserve existing Update ownership/concealment rules. |
| Update admin shared | Same fallback/Stamp flow; prepare durable prior-tree backup before replacing bytes, then `CommitPublication` with the existing publisher/source/visibility/department and final ID/revision. Publication failure restores prior tree before abandonment. |
| Publish fresh/re-publish | Lock source and shared destination. Validate publisher-only overwrite/visibility policy. Resolve/register final shared ID before stamping. Stamp staged copied manifest with publish reason, retaining source layout for fresh copy or current destination display for overwrite. Durable backup precedes replacement. `CommitPublication` is the exact-revision commit point; only then receipt and request. Any observed precommit failure restores prior tree and reverses new registry claims; overwrite never loses the previously published content. |
| Copy | Shared source → caller private destination through CopyTreeStamped with fresh token/copy reason; copied source ID is metadata only until existing policy assigns destination identity. Never arrange an intermediate source token. |
| Duplicate | Admin-only source → caller private target; assign new stable ID and target Name in staging before Stamp; commit registry claim before successful request, rollback on failure. Source remains unchanged. |
| Rename private | Begin durable relocation intent with stable ID and both endpoints after settlement. Stage target with final Name, same stable ID, new rename revision. Registry.Rename is the identity commit. Receipt/request for target, relocate old revision history, then clean source/end intent. Source cleanup failure after commit cannot abandon the new revision. |
| Rename shared | As private, but `RenamePublication` atomically commits registry name and publication name/ID/new revision. Never split Registry.Rename and CommitPublication into two independent commits. Backup/intent supports precommit rollback and postcommit roll-forward. |
| TransferOwnership private | Settle both endpoints, BeginRelocation with stable ID, copy unchanged tree, SetOwner, relocate journal and Store records, remove source, EndRelocation. No new token/request; immutable saved presentation bytes survive. Recover registry-committed moves even when both trees exist. |
| TransferOwnership shared | Attribution/registry-only mutation, no authoring save or request. Preserve existing publication proof while updating permitted attribution. |
| Delete/Unpublish | Under destination lock, settle BEFORE publication/registry/folder removal. Committed receipts and historical layout rows survive deletion. Do not stamp. |
| BackfillRegistry/backfillScope/resolveOrAllocateID | Coordinate the full read/modify/write under the same lock, preserving Presentation and retained fields; no token, journal or request. |
| Load/List/supporting file APIs/migration/Archive/SetVisibility | No authoring revision. Visibility-only Upsert preserves saved proof. Position-write APIs are deleted by P3T14; until then they share the lock and are not an auto-arrangement producer. |

No enqueue/status/cache/review outage can make a committed save fail. Precommit journal/settlement failures may abort without changing content. When rollback itself fails, join errors and retain prepare/tree intent; do not Remove proof. Receipt failure leaves committed bytes recoverable. Bound any detached postcommit I/O (30s) so caller cancellation neither loses proof nor hangs indefinitely.

## Strict TDD cycles

Write tests first, run RED against a compiling stub, implement GREEN and refactor per cycle. Existing successful legacy tests are characterization only; mutation probes must show each new guard is load-bearing. Local fakes deep-copy inputs and implement the entire P2T2 ports, including receipts/intents/settlement and source/destination locks.

- [x] **3.1 Create/update.** Test local nil ID, registry ID, client token rejection, A→B→A, identical-content and position-only authoring Update, retained surviving positions and partial positions. Assert one captured request, exact destination, final ID/name and JSON equality to persisted workflow. Missing/unauthorized/shared non-admin update performs no prepare/write/request. Exercise admin shared fallback separately.
- [x] **3.2 Failure ordering.** Inject settle, journal, repository, registry, publication, receipt, enqueue and cleanup errors at each operation boundary. Before commit no phantom job; after commit content remains, receipt/input stays discoverable and save succeeds on queue outage. Observe receipt-before-request and ack-before-Remove with a recording fake.
- [x] **3.3 Publish/copy.** Cover fresh and overwrite, different publisher, hidden source access, same-name collision, nil/copied source IDs, source token never visible at destination, and stamp failure leaving prior content unchanged. Replace obsolete `TestService_Publish_OverwriteUpsertFailure_DoesNotDelete` expectation with restoration of the entire prior tree/publication proof. Add re-publish A row + B staged tree + crash before proof: only A is committed, B rolled back. Re-read supporting files too.
> VERIFIED on revisit: P2T6/P2T7/P2T9 now supply the real producers/root. `TestLayoutE2E_KillAndImmediatePeerRead/shared/{before-proof,after-proof}` kills an actual authoring process and uses an already-running root for the first settling read. Strengthened assertions establish visible B bytes with exact A proof before settlement, restore A manifest/supporting/cache bytes and the entire proof, reject B's phantom row, and retain exact committed B input after proof. Omitting the publication revision comparison makes the before-proof sink fail. `TestLayoutE2E_InitDomainAuthoring` and `TestLayoutE2E_NilCopiedIDStillRequests` cover final persisted identity/input and source-token isolation through SQLite.

- [x] **3.4 Rename/duplicate/transfer.** Source unchanged for copies, correct new stable ID for duplicate, same ID/new token for rename, unchanged manifest/token for owner transfer. Registry failure rolls destination back. Old source deletion failure after commit retains new target request and relocation intent. Test both source+destination present with registry pointing either side, new destination collision, and cancelled context.
> VERIFIED on this revisit (§6.18): the private/shared composition-root rename tests and `TestCommitSource_CrashedRenameRecoversByRegistryAuthority` cover both trees before/after the registry identity commit with exact prepared input, stable ID and shared publication proof. `TestServiceRevisionMoveRollbackAndPostcommitCleanup` covers retained target request/intent after source deletion failure. New `TestServiceRevisionE2E_MoveRegistryRollback` injects actual SQLite identity transaction failures and asserts no target directory/phantom row, unchanged source/supporting bytes and shared proof. New `MoveCollisionAndCancellation` covers real destination collisions and cancelled contexts for rename/duplicate/transfer. Existing root authoring/transfer tests establish duplicate identity and unchanged transfer token. Omitting rename rollback fails the new target-directory sink; restoring it passes.

- [x] **3.5 Non-authoring regressions.** Reads, list, raw supporting files, backfill, archive, visibility-only edits and owner-only moves cannot stamp/enqueue. Backfill races authoring Update without overwriting newer Presentation. Delete/Unpublish races cache publication without recreating a removed folder; missed prior receipt settles before removal.
> VERIFIED on revisit: existing non-authoring/backfill/error tests remain green. New `TestServiceRevisionE2E_DeleteUnpublishRaceCache` saves through initDomain, requires real BrowserRenderer/ReadabilityReviewer evidence and consumes the worker's actual persisted cache. It races the root's cache publisher against actual Service Delete/Unpublish in both lock orderings, asserting no resurrected directory, removed identity/publication, unchanged captured manifest and immutable reviewed history, and no extra authoring row. Its fence consumes the real SQLite job/result/destination values and owned context. Reconciliation uses the root's real worker afterward. Removing the cache operation lock fails the real filesystem write sink. The existing private/visible kill test also covers a missed receipt settled before later overwrite/deletion.

- [x] **3.6 Wiring probe.** Service without stamper preserves legacy isolated-test behavior; Service with stamper/coordinator reaches the requester sink. Missing production coordinator is not silently replaced by a no-op. P2T9's real initDomain save must reach SQLite; removing either setter must fail that test.

> VERIFIED on revisit: `TestLayoutE2E_InitDomainAuthoring` drives actual Create/Update/shared admin Update/Publish+overwrite/Copy/Duplicate through initDomain and checks final filesystem JSON/ID/revision, exact captured SQLite manifest/budget/destination, one row per save, and terminal state. Independently removing SetRevisionStamper fails the saved-revision sink; removing SetMutationCoordinator fails Create with ErrMutationMisconfigured. Both were restored and the root probe passes under -race. Legacy/no-stamper isolation and fail-closed missing ports remain covered by the original Service tests. No production substitute or new feature was needed: the deferred dependencies already implement the required protocol.

```bash
go test ./domain/workflow -run 'TestServiceRevision|TestService_Publish|TestPublish|TestService_Rename|TestService_Transfer|TestService_Duplicate|TestService_Delete|TestService_Unpublish|TestBackfill' -count=1 -race
go test ./adapter/filesystem -run 'TestWorkflowRepo.*Copy' -count=1 -race
go vet ./domain/workflow ./adapter/filesystem
```

Verification this pass: both prescribed race-test commands, `go vet ./domain/workflow ./adapter/filesystem`, and all tests in workflow/filesystem/engine/samples/HTTP/MCP pass. Spec → anti-green-theater → code-quality review completed in order. Mutations removing request delivery, clearing captured Manifest, skipping CommitPublication or removing coordinator injection fail the sink regressions and were restored. These results establish the Service consumer protocol, not the missing real-root/durable recovery/cache gates above.

## Production provenance and next owners

Existing HTTP `WorkflowHandler` and MCP `tools_workflow.go` decode manifests and call Service; retain the service authorization boundary. P2T9 injects the SAME stamper/coordinator/identity/publication repositories used here, before seeding. P2T7 implements cross-process locks, durable tree backup/recovery, receipts and CommitSource; unit fakes here do not establish crash safety. P2T9 must cover all real authoring paths at the SQLite sink and actual serve/MCP commands. P3T8 reuses the same MutationCoordinator for expected-revision retry. P3T14 removes the manual-position mutation surfaces.

## Revisit — deferred production acceptance (2026-10-17)

Selected P2T3's newly unblocked production gates after skipping P2T1's unchanged sample-profile regression and P2T2's documented resume/rename protocol conflicts. No other task was implemented or checked off. Steps 3.3, 3.5 and 3.6 now have real producer/root/sink coverage; 3.4 remains blocked as documented above.

- RED→GREEN guard verification: removing exactPublication's revision equality failed the already-running peer's first read (B leaked instead of restored A). Removing the production cache operation lock failed the browser-fed filesystem write during Delete; restoring the lock passes all four Delete/Unpublish/order cases. Removing either root setter independently failed the actual authoring probe. These are mutation probes of dependencies already implemented by later tasks, not a claim that new production behavior was necessary in this verification pass. All mutations are restored.
- Self-review, in order: **spec** — restricted changes to Task 3's deferred crash/non-authoring/wiring assertions; retained the unresolved rename gate, no protocol exceptions or extra features. **Anti-green-theater** — the new race probe requires actual saved input, real Chrome DOM/PNG review and worker-produced cache geometry, checks filesystem/SQLite/history sinks, and consumes every fence argument. Improved its negative path to reach the filesystem write result instead of stopping only at a lock-order assertion. Missing wiring and proof/lock mutations fail. **Quality** — followed neighboring root/crash helpers; bounded race contexts, joined spawned operations even on assertion failure, no fatal assertions in goroutines, no production changes or fake renderer. Preserved unrelated untracked `docs/visuals/` work.

Revisit verification (no browser skips):

```bash
go test ./domain/workflow -run 'TestServiceRevision|TestService_Publish|TestPublish|TestService_Rename|TestService_Transfer|TestService_Duplicate|TestService_Delete|TestService_Unpublish|TestBackfill' -count=1 -race
go test ./adapter/filesystem -run 'TestWorkflowRepo.*Copy' -count=1 -race
go vet ./domain/workflow ./adapter/filesystem
ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^TestServiceRevisionE2E_|^TestLayoutE2E_(InitDomainAuthoring|KillAndImmediatePeerRead|NilCopiedIDStillRequests|TransferRelocatesWithoutNewRevision|LegacyReadsAndBackfillNeverStamp)$' -count=1 -v
```

Focused workflow/filesystem race gates and vet pass. The real-root race gate passes, including the real subprocess crash windows and four browser-fed cache/removal cases. Whole-task completion is not claimed while Step 3.4 remains blocked.

## Post-merge authoring coverage and validation lock boundaries

`domain/workflow/group_ops.go:Service.UpsertGroup` / `DeleteGroup` mutate through
Service.Update; these are authoring saves and must produce exactly one stamped revision
and captured group config/task/edge input. Include their registered HTTP/MCP callers in
the root coverage, along with full manifest imports. AnalyzeGroups/ParseGroupConfig and
Service group checkers remain the existing authority; arrangement never changes execution.

- [x] RED→GREEN/mutation: create, group upsert, group delete and full manifest update
  through actual root services/tools; exact saved nodes/config/task author order/edges
  reach the immutable SQLite input once, with fresh revision and previous fallback.
  Invalid group import makes no partial save/request. Removing stamp forwarding must
  fail the captured-input sink. P3T15 consumes this input for reviewed group geometry.
> VERIFIED: `TestServiceRevisionE2E_GroupAuthoringCapturedInput` runs both forms through actual registered HTTP/MCP create/import and MCP group upsert/delete. It checks full saved/captured JSON, author task/edge order, final ID/destination, fresh tokens, one row per save and filtered previous fallback; invalid create/import leaves no partial save/row. Disabling Update's stamp forwarding fails the exact SQLite manifest assertion (old task order/rule/label); restoring it passes all four cases. These tests intentionally verify capture, not P3T15's unimplemented reviewed group geometry.
- [x] Child-contract validation can read other workflow destinations. Follow existing
  service_revision_move.go:precheckCopiedGroups and samples/seeder_revision.go's
  validation-before-lock pattern; do not widen an owned mutation lock set or drop
  settlement. Preserve the existing final source/destination authorization checks
  under their locks. Run the existing group-copy/import and seeder child-contract
  lookup failure tests against the real coordinator; authoring must not deadlock,
  silently skip validation or leave a partial stamped revision.
> VERIFIED: `TestServiceRevisionE2E_GroupImportsChildContractLockBoundary` drives real MCP publish/copy/duplicate/rename, filesystem locks, child contract reads and SQLite captured-input sinks; corrupting the real child manifest refuses every import without changing source bytes, identity, rows or prepared journal input. Moving publish validation under its owned lock set fails the valid import with `mutation lock set cannot expand`; restoring the existing precheck passes. `GroupSeedChildContractLockBoundary` boots actual initDomain with a real sample file set and valid/corrupt shared child, checking captured JSON or zero tree/proof/ledger/identity/row/prepare respectively. Omitting seed validation fails the ledger sink. Existing group-copy/import and seed child-store failure tests also pass under -race.

## Completion revisit — remaining authoring gates

This pass supersedes the historical blocked status for Step 3.4 above. The required
production protocol already exists; no new production behavior, port, flag or bypass
was necessary. Added acceptance tests first, demonstrated behavioral RED through
independent rollback/stamp-forwarding/lock-boundary/seed-validation mutations, restored
each mutation, and confirmed GREEN. No mutation remains in production source.

Ordered self-review:
1. **Spec:** restricted to Task 3's outstanding move/group authoring gates; no claim
   that captured group input is reviewed group geometry (P3T15).
2. **Anti-green-theater:** all new root tests consume actual filesystem/SQLite data,
   not a fake renderer/requester. Exact saved/captured JSON, stable identity, destination,
   fallback and rejected-save sinks fail with missing input/wiring. Strengthened cleanup
   failure assertions to check the exact target request and relocation endpoints, and
   rejected writes to check absence of recoverable prepares, not only current row counts.
3. **Quality:** followed existing root/MCP/HTTP helpers; deterministic unavailable-browser
   configuration keeps this authoring-only gate independent of review acceptance; bounded
   import contexts, joined root cleanup, gofmt and no production or unrelated edits.

Final verification: the prescribed workflow/filesystem race commands and vet, plus
focused root move/group/authoring, registry-authoritative rename recovery, existing
workflow group-import and sample child-store tests under -race. All pass; no skips.
