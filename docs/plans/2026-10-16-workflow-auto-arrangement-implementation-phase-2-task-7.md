# Workflow Auto-Arrangement — Phase 2 Task 7: Coordinated filesystem commits and recovery

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§2, 6.2–6.3, 6.10–6.12. This plan replaces the former per-method-only locks, prepare-only replay and tree-presence relocation examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Durable exact authoring provenance, reversible shared-tree replacement and cross-process revision/generation-safe cache publication. Filesystem history never recreates a deleted workflow or treats a prepared/rolled-back operation as saved.

**Files:** `adapter/filesystem/{manifest_lock,revision_journal,layout_cache,tree_commit}.go` and tests; modify workflow_repo.go; create `domain/workflow/commit_source.go`/tests and `adapter/filesystem/commit_crash_test.go`. Add gofrs/flock dependency using current Go module conventions. P2T2 already owns every workflow port; P2T3 already implemented CopyTreeStamped's staging method. This task strengthens it with real operation locks/backup durability without leaving earlier tasks uncompilable.

## Exact interfaces

```go
const LayoutCacheFile = "layout.json"
func NewRevisionJournal(basePath string) *RevisionJournal
func NewLayoutCache(repo *WorkflowRepo) *LayoutCache
func (r *WorkflowRepo) SetReadSettler(s workflow.DestinationSettler)
func (r *WorkflowRepo) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error
func (r *WorkflowRepo) PrepareTreeCommit(ctx context.Context, dest workflow.MutationTarget, revision string) (workflow.TreeCommit, error)
func (r *WorkflowRepo) FinishTreeCommit(ctx context.Context, id string) error
func (r *WorkflowRepo) RollbackTreeCommit(ctx context.Context, id string) error
func (r *WorkflowRepo) PendingTreeCommits(ctx context.Context) ([]workflow.TreeCommit, error)
func (c *LayoutCache) PublishCache(ctx context.Context, dest layout.Destination, entry layout.CacheEntry, fence func(context.Context) error) error
func (c *LayoutCache) ReadCache(ctx context.Context, scope workflow.Scope, name string) (layout.CacheEntry, bool, error)
```

RevisionJournal implements the FULL P2T2 interface: Record/MarkCommitted/Remove/Quarantine/Entries/List/BeginRelocation (including workflowID)/RelocationIntents/Relocate/EndRelocation. Add compile-time port assertions only once all required methods compile in this task. Neither a three-method partial journal nor a raw List adapter satisfies recovery proof.

```go
// domain/workflow/commit_source.go (no adapter imports)
type CommitSourceDeps struct {
    Journal RevisionJournal
    Repo Repository
    Mutations MutationCoordinator
    Publications RevisionPublicationStore
    Trees TreeCommitter
    Registry Registry
    Relocator ArrangementRequester
    Logger *slog.Logger
}
func NewCommitSource(deps CommitSourceDeps) (*CommitSource, error)
func (c *CommitSource) Settle(ctx context.Context, scope Scope, name string) error
func (c *CommitSource) CommittedRequests(ctx context.Context) ([]ArrangementRequest, error)
```

Require Journal/Repo/Mutations/Trees/Relocator. Publications and Registry may be nil only for actual no-registry/local mode; production passes both. Constructor rejects misconfiguration with ErrCommitSourceMisconfigured and names missing ports. P2T9 injects the same live repo for Repo/Mutations/Trees, real SQLite publications/registry, and requester as Relocator. Never install no-op commit proof.

## 7.1 Locks and readable operation boundaries

Use `<workspace>/.arbitron-locks/<sha256 canonical locator>.lock` with in-process cancellable mutex AND cross-process flock. Resolve/validate scope/name before lock creation. Sort/deduplicate all targets, release reverse order, including on error/cancellation/panic cleanup. The callback receives coordinator-specific owned-lock context; methods recognize only their OWN coordinator's token. Nested calls for held targets skip reacquisition. Do not add new lower-order locks inside an operation; callers name all endpoints upfront.

Create/Update/Delete/SaveNodePositions, CopyTree/CopyTreeStamped (BOTH endpoints), raw supporting-file writes and identity reconciliation all participate. Service/Seeder hold these locks across the entire authoring+proof+receipt operation. WorkflowRepo.Load uses the same lock and owned context, so external reads/triggers cannot see intermediate copied tokens, partial seeds or overwrite rename gaps. P2T9 injects SetReadSettler(commits) before any seed/traffic. An external Load acquires its lock, calls read settler with the owned context, then reads; an already-owned internal Load bypasses this call to prevent recursion. For external/foreign-operation contexts Settle returns workflow.ErrMutationPending when a live relocation covers this endpoint, because resolving it needs BOTH locks; only the exact private active-rename context in §6.18 is exempt. Global CommittedRequests resolves intents first. Other prepare/tree commits roll forward/restore under the held destination lock before read. Internal Settle/Stamp/load do not deadlock. Test an already-running second process reading immediately after a peer crashes before its next recovery poll: committed/restored bytes or explicit pending error only.

List already delegates candidates to Load in current source. Preserve that gated read path, but propagate ErrMutationPending/settlement availability errors rather than silently presenting an incomplete successful catalog; keep existing skip policy for genuinely malformed legacy entries. Add peer-crash catalog tests proving no uncommitted summary leaks.

Reserve layout.json against WriteFile/DeleteFile and omit it from ListFiles. workflow.json remains reserved. Validate normalized relative paths so `./layout.json` cannot bypass protection. Internal cache access is separate. `.arbitron-locks`, `.arbitron-layout-journal`, `.arbitron-tree-commits` and staging/backup dirs are excluded from catalog/file APIs and flat-store migration. Cache publication creates no destination directory when absent.

## 7.2 Durable journal and tree intents

Journal directory `.arbitron-layout-journal`: `<revision>.json` prepared input; `<revision>.committed` receipt; quarantine entry/receipt/reason; relocation intents under relocations/. Validate revision/intent filenames against traversal, encode JSON with stable value semantics, temp+rename and fsync file AND parent directory for every durable write/move/removal. Copy slices/maps defensively. Context cancellation aborts before exposing incomplete evidence.

Entries/List are stable RequestedAt/revision order and exclude quarantine. Entries derives committed only from a valid matching receipt; orphan receipt, corrupt JSON or inconsistent envelope is diagnosed/quarantined and never arranged. Aggregate errors without dropping unrelated valid entries. Record replay of identical revision must not downgrade an existing receipt or overwrite a relocated destination; a conflicting immutable input is an error. Remove deletes entry+receipt idempotently. Quarantine retains unchanged evidence and reason. Relocate changes only request destination, not immutable Manifest/Retained/revision/time/receipt; durable intent makes partial per-entry rewrites repeatable.

TreeCommitter uses `.arbitron-tree-commits/<id>/` containing intent and full prior-tree backup. Prepare determines HadPrior/PriorRevision, copies+fsyncs all prior bytes before recording a ready intent and before any replacement. An interrupted incomplete backup never authorizes rollback from incomplete bytes; preserve/detect incomplete state. Rollback atomically restores complete prior manifest/files/cache or removes only that fresh revision's destination, guarded against a newer revision. Finish deletes backup only after durable commit proof/receipt, is idempotent, and retains diagnosable intent on cleanup errors. Methods take/reuse the same destination lock. No directory scan bulk-arranges workflows.

## 7.3 Fenced cache

Inside destination lock, explicitly run the configured read settler BEFORE the owned-context Load (which otherwise skips the external read gate). Unresolved relocation returns ErrMutationPending and remains pending/transient, never a false noncurrent settlement. Then load current manifest: absent→ErrDestinationGone; wrong saved token→ErrNotCurrent. Validate format, positive job/result generations, full contract and geometry against current graph. Invoke supplied Store fence WHILE locked, matching job generation, result generation AND latest destination. Write temp+fsync+rename layout.json only after checks; no recursive Service.Update or authoring stamp. ReadCache validates envelope and geometry; copied source cache is never accepted as destination proof. SQLite remains authority and cache failure cannot erase its result.

## 7.4 TDD filesystem tests

Each listed group is RED (behavior against compiling stub) → minimum GREEN → refactor. Use channel barriers, actual subprocesses and context deadlines, not production-duration sleeps.

- [x] Lock tests: one repo, independent repos/processes, same locator exclusion, different locators concurrency, inverse copy operations sorted/no deadlock, source mutation during copy, duplicate targets, owned-context reentry, foreign coordinator token rejection, cancellation/error release.
- [x] Repository tests: every write waits; Load/trigger blocks during staged authoring and sees only final committed/restored prior revision. Source token never becomes target-visible. Reserved artifact aliases rejected; supporting files still work; missing target never recreated by cache.

> RESOLVED: P2T9 now constructs the real CommitSource and injects SetReadSettler before traffic. TestLayoutRepository_StagedPublicationReadBoundaries boots an already-running initDomain peer, holds an actual second process at shared publication before/after exact proof, and proves Service.Load, Engine.Trigger's persisted SQLite snapshot and Service.List wait while the writer owns its locks. After kill, each consumer's FIRST read (without a recovery poll) returns the fully restored old graph/summary or exact committed new graph/summary, including supporting bytes and distinct target/source revisions. TestLayoutRepository_UnresolvedRelocationFailsClosed proves all three consumers propagate ErrMutationPending, persist no instance, change no manifest bytes and retain the undecided intent. Removing root SetReadSettler fails the three uncommitted-data sinks and pending guards; bypassing Load's lock fails all three staged-read deadline assertions. Both mutations were restored. Existing adapter write/alias/cache regressions remain green. The independent rename protocol blockers under 7.5 remain unchecked.
- [x] Journal tests: prepared/receipt transition/idempotency, receipt unknown revision errors, Remove pair, quarantine preserves bytes, corrupt/orphan/torn temp entries, replay does not downgrade/move, destination-only relocation and durable intents, fsync/write/rename failures and cancellation.
- [x] Tree tests: full old manifest/supporting-file/cache byte restoration after fresh/overwrite failure, Prepare error leaves content unchanged, crashed incomplete backup cannot restore partial content, postproof cleanup retry, rollback guard against newer token, no backup leak in public file/migration APIs.
- [x] Cache tests: old revision, retry generation/result generation/destination fence failures, cache write failure retains source/result, wrong graph/full contract/parallel-edge identity rejection, no authoring request on publish. Real cross-process save/cache interleaving proves CAS. After peer crash at uncommitted replacement, publication settles/rolls back before deciding currency, so a restored old revision is not permanently marked noncurrent.

## 7.5 CommitSource and real crash windows

Settle is called with destination lock already held; never reacquire. Before interpreting any prepared manifest, detect covering unresolved relocation and return ErrMutationPending without enqueue/quarantine until global intent recovery has decided it, except the exact private active-rename context under both endpoint locks (§6.18). A valid receipt is committed history even if current workflow is gone/overwritten. For prepared input, current manifest must carry the SAME token; registry-backed shared publication must carry that exact Revision and matching WorkflowID. Existence of ANY publication row proves nothing about a new re-publish.

- Exact visible revision + exact publication (where required): MarkCommitted, then finish its tree backup; retain request until enqueue ack.
- Visible prepared revision + no/mismatching publication: restore prior tree from durable intent (fresh target removed), then quarantine failed prepare. Old publication remains valid for old tree. Failed rollback is undecided: preserve evidence and reject next overwrite/delete until settled.
- Missing/different manifest without receipt: quarantine definitely uncommitted prepare only after any linked tree intent is safely decided. Never discard a previously committed historical revision because its destination advanced; settle-before-overwrite preserved its receipt.
- Read/publication/journal/tree error: leave undecided; errors.Join with other destination errors while returning every proven committed request. Recover consumes successful partial results too.

Resolve relocation intents with BOTH locks and stable registry authority, not tree presence. Use Registry.ResolveName(intent.WorkflowID) then Resolve(name), verifying the same ID and full destination. If registry points To, roll forward journal/Store relocation and source cleanup even when both folders exist. If it points From, roll back copied target and end intent only after cleanup succeeds. Unknown/missing/error/third destination is undecided, preserving intent. Shared rename atomically updates registry/publication via P2T6 RenamePublication; do not infer it committed from the target tree. Nil-registry mode supports only operations with an explicit unambiguous filesystem commit protocol, never registry-based rename/transfer.

CommittedRequests first resolves intents/tree commits, then settles prepared destinations under WithMutations; returns defensive copies of all committed inputs oldest first, including deleted/overwritten history. Iterate journal/intents only; never Repository.List/catalog.

- [x] Unit RED→GREEN: every settle branch above, missing required constructor port, exact revision vs old publication, mismatch ID, local private mode, historical receipts, aggregate errors with successful destination, no catalog scan, cancellation, and relocation with BOTH trees and registry selecting either side.

> Current plan correction (§6.18): the existing operation-private `withOwnRelocation` marker is the agreed narrow active-operation producer. Settle exempts only its exact intent; callers hold both endpoint locks, and external/foreign reads remain pending. Declare this internal contract and test private/shared success, foreign/peer contexts and registry-authoritative recovery. No exported bypass or folder-presence authority is allowed.
- [x] Subprocess RED→GREEN: kill actual stamper+repo+journal+publication protocol after prepare; manifest/staged-tree rename; publication commit; receipt; enqueue; partial relocation; source cleanup failure; rollback restoration. Parent opens fresh collaborators and asserts exact committed token set, unchanged/fully restored old bytes and zero phantom rows. Include overwrite missed receipt then next save, and old publication row during new tree crash.

> Current plan correction (§6.18): the active rename producer now exists and the crash matrix is executable. Extend actual two-process probes through target visibility, private registry Rename/shared RenamePublication, receipt, partial relocation and source cleanup; assert the first surviving peer raw/manifest/trigger read before any worker poll. Keep this combined gate unchecked until its complete sink matrix passes.
- [x] Mutation probes: make prepare imply committed, remove exact publication-token check, or choose relocation by folder existence; each must fail the crash/sink tests. Restore guards.

```bash
go test ./adapter/filesystem ./domain/workflow -run 'TestManifestLocks|TestWorkflowRepo|TestRevisionJournal|TestTreeCommit|TestLayoutCache|TestCommitSource|TestCommitProtocol' -count=1 -race
go vet ./adapter/filesystem ./domain/workflow
```

P2T9 repeats kill/restart through real initDomain and SQLite, including both serve/MCP. P2T8 Recover consumes CommittedRevisionSource and ack-cleans journal; P3T8 retry uses the SAME operation lock. P3T14 later removes SaveNodePositions and its lock path. Production completion still depends on the real Phase 3 rendered reviewer; no filesystem test substitutes visual review.

## Execution evidence

- Selection: listed plans in numeric phase/task order; P1T4–P2T6 retain documented production gates. The missing worker/root/render producers remain absent, so those gates were skipped rather than re-failed. This pass worked only on independent P2T7.
- Behavioral RED→GREEN: compiling no-op lock, journal, tree, cache and CommitSource implementations failed the new exclusion, exact-byte/state, restoration, fence and proof assertions before behavior was implemented. Reserved nested artifact aliases and migration exclusions also failed before fixes. Per-instance syscall seams first existed without forwarding; file-fsync, directory-fsync and rename fault tests failed, then passed after real journal/tree/cache write paths consumed them. No process-global fault hook or public durability option was added.
- Locks use validated canonical locators, deterministic sorted/deduplicated acquisition, cancellable per-repo gates and gofrs/flock; reverse deferred release covers cancellation/errors/panics. Callback ownership is coordinator-specific and expires. Nested expansion is rejected. Tests cover independent repos/processes, inverse copies, source mutation, duplicates, foreign/expired ownership and all repository write/read boundaries.
- Journal implements the full port with matching immutable-input receipts, stable ordering, defensive JSON values, quarantine, replay preserving relocated destinations and durable relocation intents. Atomic files are fsynced before rename and their parent after. Tests retain unchanged evidence, reject malformed envelopes/filenames, isolate corrupt/orphan entries from valid ones, ignore torn temps, and prove cancellation/I/O failures do not fabricate committed input.
- Tree intents are ready only after complete prior-byte backup and fsync. Restore preserves raw manifest/unknown fields, supporting files and cache, refuses newer revisions and never trusts an incomplete backup. A finished-intent directory rename makes interrupted cleanup distinguishable from an incomplete prepare; blocked cleanup is retryable. Existing service test adapters now delegate to these real tree methods rather than inventing additional backup workflow locks. The missing-tree-port regression explicitly hides the now-promoted real port.
- Cache validates revision, format, both positive generations, full renderer contract, verdict and exact graph geometry; calls the configured settler and then the supplied fence while locked; never creates a missing destination. Error probes preserve prior cache/manifest bytes. Real two-process crash tests cover save/cache exclusion and settlement-before-currency. Cache does not stamp or enqueue.
- CommitSource iterates only intents/journal/tree intents, checks exact revision/identity publication proof, preserves committed history, handles successful partial results with aggregate errors, and uses registry authority under both locks even when both trees exist. Actual private saves with nil workflow IDs and an overwritten missed receipt reach immutable SQLite rows through Requester. Read/publication errors preserve undecided evidence. The active-rename conflict is tracked as fail-closed behavior, NOT a successful rename test or a waived gate.
- Self-review 1 (spec): checked exact ports, filesystem paths, durable proof/rollback rules, manifest ownership, reserved artifacts, full-contract/generation fences and assigned root ownership. Added no production fake, fallback commit proof or feature option; no adjacent task was started. Left the conflicting rename and production gates unchecked.
- Self-review 2 (anti-green-theater): removing exact publication proof failed both old-proof/wrong-ID sinks; making prepare imply committed failed the real killed-before-visibility no-phantom assertion; forcing target authority despite registry selecting source failed exact recovered destination/source-preservation assertions. All mutations were restored. Real protocol tests assert saved/supporting bytes and SQLite captured-manifest rows, not just notifications. They are not real-root or visual acceptance.
- Self-review 3 (quality): read neighboring repository/service/SQLite conventions; preserved malformed-legacy catalog skips while propagating settlement unavailability; kept syscall injection private and per instance; removed obsolete test-only backup locations and import placeholders. gofrs/flock is a direct pinned dependency. Unrelated untracked docs/visuals/ was preserved and excluded.
- Verification: prescribed focused race command, go vet ./adapter/filesystem ./domain/workflow, owning filesystem/workflow/samples package gates, migration tests, gofmt and diff checks pass. No full rendered-production completion is claimed. Subsequent passes should skip the recorded P2T7 contract/root gates and select independent P2T8 work; return here after the rename protocol is resolved and P2T9 constructs the real chain.

## Repository production-boundary revisit

- Selection: skipped P2T1's unchanged sample-profile regression and P2T2–P2T4's recorded retention/rename/seed-claim contract blockers, without repeating those failures. P2T7 §7.4's missing-root dependency is now available. This pass only closes that independent repository step; §7.5's two combined rename gates remain blocked.
- Strict TDD: these are existing-behavior acceptance tests (contracts §6.1), not a claim of new production behavior. Corrected the initial catalog fixture to include the service's existing scope/publisher/visibility fields. With root SetReadSettler removed, behavioral RED showed the uncommitted new manifest, persisted Engine snapshot and catalog description instead of restored old values; unresolved-relocation reads also succeeded and persisted an instance. Restoring the already-implemented real wiring yielded GREEN. A separate temporary Load lock/settlement bypass failed all three staged-read deadline assertions. No production mutation remains.
- Self-review, in order: (1) exact P2T7 repository slice, actual root/services/FS/SQLite, no new options or guessed protocol repair; existing write/alias/cache tests supply the other required repository cases. (2) Each consumer is the first post-kill read in its own fresh two-process case; the peer worker is stopped before staging, so a poll/previous Load cannot make the tests falsely green. Nonempty prepared input, different source/target tokens, full graph/presentation/snapshot JSON, catalog metadata, byte-identical rollback, supporting files and instance counts assert the actual sinks. Removing wiring and bypassing locks failed those assertions. (3) Followed neighboring subprocess/barrier/context-deadline and cleanup patterns, reused the real crash producer, kept error propagation and identity policy intact, and ran gofmt/vet/diff checks.
- Final verification: prescribed filesystem/workflow focused `-race` command passed (21.903s/17.944s); new root boundary tests passed under `-race` (23.013s); full filesystem/workflow/samples packages passed (14.232s/18.079s/3.944s); vet passed for those two task packages plus cmd/arbitron. Browser-required `TestLayoutRender_RealBrowserPRReview` passed without skips (21.842s), proving the installed root still feeds the real browser/reviewer and terminal row. This does not waive the recorded rename crash matrix or complex-sample acceptance blockers.

## Current operation and raw-read boundaries to verify

The existing private withOwnRelocation(ctx,id) / ownsRelocation(ctx,id) functions live
in domain/workflow/commit_source.go; renameRevision alone supplies that marker to Stamp
inside the two-endpoint owned callback. It exempts only the matching live intent during
this operation. Recovery, external reads and other intents keep the fail-closed rules.

FileReaderAdapter.SetSettledRepository(repo *WorkflowRepo) in file_reader.go is an
additional production read consumer of the same locks/settler. initDomain injects it;
withExisting settles before private/shared existence selection and retains the lock while
ReadFile reads workflow.json/layout.json. Use its bounded adapter context because the
existing FileReader port carries no context. ResolvePath does not grant a lock for a later
external os.ReadFile; raw handlers must use ReadFile.

- [x] Complete the §7.5 crash matrix with private/shared rename, same/wrong/foreign
  intent contexts and a surviving peer's FIRST reserved-byte read, including a missing
  private tree in the swap gap. A wrong proof/omitted setter/ignored context must fail
  the actual bytes or unavailable assertion, not just call counts.
- [x] Before quarantining an abandoned fresh seed, releaseSeedClaim checks no tree or
  publication remains and compares exact registered ID/name/shared scope/owner to the
  prepared request under the destination lock. Prepared input precedes Register. Test
  a post-Register kill before tree intent, subsequent reseed, foreign claims, ID mismatch,
  surviving publication/tree and registry I/O errors. Never delete a claim by name only.

These are required verification of named existing production inputs, not permission to
leave the old protocol blockers unexamined or to suppress pending-mutation errors.

## Completion revisit — existing rename, raw-read and seed producers (2026-10-18)

This revisit supersedes the historical skip/blocker guidance above. All P2T7 steps are
now verified; it does not close the separate complex-sample/browser or v2 group tasks.

- Selected the first unfinished task in numeric phase/task order, P2T7 only. Rechecked
  the available producers against contracts §§6.1 and 6.18 rather than repeating the
  obsolete missing-rename diagnosis.
- Unit coverage now includes named constructor-port errors in both misconfigured
  publication/registry directions; unproven visible input without rollback; exact/old/
  absent proof for journal-less backup cleanup; receipt, finish, rollback, quarantine,
  partial enumeration and tree-enumeration I/O errors. Retry preserves durable evidence,
  returns unrelated committed history, restores exact prior bytes or enqueues exact
  immutable input into real SQLite. Recovery never scans the catalog.
- Private AND shared active rename tests exercise matching, wrong, foreign covering
  and unrelated intents through the actual Service/Stamper/CommitSource/FS/SQLite seam.
  Broadening the private marker exemption failed at the preserved-source byte sink;
  dropping the exact publication revision failed the unlinked-backup byte sink. Restored
  both guards and passed the complete CommitSource/context gate under `-race`.
- `TestCommitProtocol_RenamePeerCrash` kills an actual initDomain-backed Service.Rename
  at prepare, target-visible/pre-identity, post-identity, receipt, enqueue, journal-before-
  Store relocation, post-Store relocation, source cleanup failure, source removal and
  rollback restoration, in both scopes. A real receipt-cleanup outage retains source
  history for the partial-relocation case; no prepared fixture is declared committed.
  The surviving root's worker is stopped before the writer starts. Its FIRST reserved-
  byte reads, then manifest/trigger/HTTP/MCP reads, return pending without leaking bytes
  or persisting an instance. Fresh FS/journal/proof/registry/requester collaborators
  resolve by registry authority and assert exact manifest/support/cache bytes, immutable
  SQLite inputs/destinations, the complete row count, no losing tree/intent/backup and
  idempotent replay. Omitting root FileReader injection leaked reserved bytes; forcing
  target authority retained the losing tree/deleted the committed source. Restored both.
- `TestCommitProtocol_RollbackSwapGap` kills the actual RollbackTreeCommit after its
  first directory rename, for private/shared and manifest/cache FIRST reads. A live
  settled FileReader restores every prior byte before scope selection, never the
  deliberately different same-name shared fallback, and creates no phantom SQLite row.
  Initial behavioral RED showed the rollback bypassing the existing per-instance rename
  syscall seam. Minimal GREEN routes rollback/install/compensation through that same
  private seam (default remains os.Rename). No global hook, flag or alternate protocol.
  Moving existence selection ahead of settlement failed at wrong private fallback bytes
  and missing shared bytes; restored the production ordering.
- Seed gate consumes the real post-Register/pre-tree subprocess, not a synthetic claim.
  Existing KillAndRecover and PostRegisterClaimControls cover reseeding, foreign ID,
  private scope/shared owner, surviving tree/publication and SQLite deletion failure;
  CommitSource unit tests add registry/publication read errors and cancellation. Moved
  the existing seed log assertion AFTER manifest/SQLite sinks so a log failure cannot
  substitute for provenance. Removing compensation failed the saved-manifest sink;
  removing scope/owner guards deleted foreign registry rows. Restored every mutation.
- Self-review IN ORDER: (1) spec: exact remaining P2T7 slice, existing composition roots
  and real data, no new policy, options, renderer or neighboring-task implementation.
  (2) anti-green-theater: nonempty graphs, distinct revisions/IDs, actual filesystem and
  SQLite sinks, first-read ordering with no worker sweep, and the independent missing-
  wiring/wrong-proof/wrong-context/wrong-scope controls above. No argument-ignoring
  successful fake or fabricated receipt. (3) quality: reused neighboring bounded process,
  cleanup and lock patterns; kept syscall injection per instance; named its helper
  renamePath because rollback renames directories; retained unknown/prior bytes and all
  existing error propagation. gofmt, vet and diff checks pass.
- Final post-review verification: prescribed filesystem/workflow focused `-race` gate
  passes (30.452s/59.903s); combined real-root rename, repository, private/shared save/
  restart and seed crash/claim `-race` gate passes (77.874s). Full filesystem/workflow/
  samples packages pass (36.510s/33.302s/2.536s). `go vet ./adapter/filesystem
  ./domain/workflow ./cmd/arbitron`, `go build ./...`, gofmt and `git diff --check` pass.
  No mutation remains; no browser readability/release completion is claimed here.
