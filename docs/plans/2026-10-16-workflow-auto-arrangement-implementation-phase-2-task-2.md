# Workflow Auto-Arrangement — Phase 2 Task 2: Saved revision metadata and commit ports

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), especially §§2, 6.2–6.3, 6.10. This plan replaces the former prepare-only journal and post-copy restamp examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Capture a unique server-issued revision, immutable input and retained presentation for each authoring save, propagate them into instance snapshots, and declare the complete two-phase commit protocol without importing `domain/layout` into `domain/workflow`.

**Files:** modify `domain/workflow/workflow.go`, `visibility.go`; create `presentation.go`, `arrangement.go`, `revision_stamper.go`; tests `revision_stamper_test.go`, `presentation_test.go`, `domain/engine/engine_presentation_test.go`. P2T7 implements the filesystem-backed ports and `CommitSource`; P2T6 implements publication proof.

## Exact interfaces and owners

`Workflow` gains `Presentation *Presentation` with JSON tag `presentation,omitempty`. Implement the complete `SaveReason`, `RetainedSource`, `RetainedLayout`, `Presentation` values and JSON tags from contracts §2. Keep `Workflow.ID` optional; neither ID nor a content hash is the revision key.

```go
func ScopeFromPresentation(p *Presentation) Scope
func ScopeLabel(s Scope) string
func (w *Workflow) SavedRevision() string

type ArrangementRequest struct {
    Revision string
    Scope Scope
    Name string
    WorkflowID uuid.UUID
    Reason SaveReason
    Manifest []byte
    Retained RetainedLayout
    RequestedAt time.Time
}
type ArrangementRequester interface {
    RequestArrangement(ctx context.Context, req ArrangementRequest) error
    RelocateArrangements(ctx context.Context, from Scope, fromName string, to Scope, toName string) error
}
type CommittedRevisionSource interface {
    CommittedRequests(ctx context.Context) ([]ArrangementRequest, error)
}
var ErrMutationPending = errors.New("workflow: destination has an undecided durable mutation")
type DestinationSettler interface {
    Settle(ctx context.Context, scope Scope, name string) error
}
type MutationTarget struct { Scope Scope; Name string }
type MutationCoordinator interface {
    WithMutations(ctx context.Context, targets []MutationTarget, fn func(context.Context) error) error
}
type DisplayedLayoutReader interface {
    DisplayedLayout(ctx context.Context, revision string) (RetainedLayout, bool, error)
}
type JournalState string
const (
    JournalPrepared JournalState = "prepared"
    JournalCommitted JournalState = "committed"
)
type JournalEntry struct { Request ArrangementRequest; State JournalState }
type RelocationIntent struct {
    ID string
    From Scope
    FromName string
    To Scope
    ToName string
    WorkflowID uuid.UUID
    CreatedAt time.Time
}
type RevisionJournal interface {
    Record(ctx context.Context, req ArrangementRequest) error
    MarkCommitted(ctx context.Context, revision string) error
    Remove(ctx context.Context, revision string) error
    Quarantine(ctx context.Context, revision, reason string) error
    Entries(ctx context.Context) ([]JournalEntry, error)
    List(ctx context.Context) ([]ArrangementRequest, error)
    BeginRelocation(ctx context.Context, from Scope, fromName string, to Scope, toName string, workflowID uuid.UUID) (RelocationIntent, error)
    RelocationIntents(ctx context.Context) ([]RelocationIntent, error)
    Relocate(ctx context.Context, from Scope, fromName string, to Scope, toName string) error
    EndRelocation(ctx context.Context, id string) error
}
type TreeCommit struct {
    ID string
    Destination MutationTarget
    Revision string
    PriorRevision string
    HadPrior bool
}
type TreeCommitter interface {
    PrepareTreeCommit(ctx context.Context, dest MutationTarget, revision string) (TreeCommit, error)
    FinishTreeCommit(ctx context.Context, id string) error
    RollbackTreeCommit(ctx context.Context, id string) error
    PendingTreeCommits(ctx context.Context) ([]TreeCommit, error)
}
type RevisionPublicationStore interface {
    PublishedStore
    CommitPublication(ctx context.Context, name string, publication Publication) error
    RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, publication Publication) error
}
```

`Publication` gains `Revision string` and `WorkflowID uuid.UUID`. `CommitPublication` is the exact-revision publication commit point, atomically persisting publisher, source, visibility, department, ID and revision. Existing `Upsert` for visibility-only edits preserves those proof fields. Implement shared-rename atomic identity/publication support exactly as contracts §6.10 selects; its producer is P2T6 and its caller P2T3.

```go
type StamperDeps struct {
    Journal RevisionJournal
    Requester ArrangementRequester
    Displayed DisplayedLayoutReader
    Settler DestinationSettler
    NewToken func() string
    Now func() time.Time
    Logger *slog.Logger
}
func NewRevisionStamper(repo Repository, deps StamperDeps) (*RevisionStamper, error)
func (s *RevisionStamper) Stamp(ctx context.Context, scope Scope, name string, wf *Workflow, prior *Workflow, reason SaveReason) (ArrangementRequest, error)
func (s *RevisionStamper) Committed(ctx context.Context, req ArrangementRequest)
func (s *RevisionStamper) Abandon(ctx context.Context, req ArrangementRequest)
func (s *RevisionStamper) EnsureStamped(ctx context.Context, scope Scope, name string, reason SaveReason) (ArrangementRequest, error)
```

Require repository, journal, requester and settler. `ErrStamperMisconfigured` text is `workflow: revision stamper requires a journal, a requester and a settler`; a nil repository may wrap it with more context. Defaults: UUIDv7 tokens, `time.Now`, `slog.Default`; Displayed is optional. Production never supplies a no-op settler.

## Commit algorithm

All authoring calls run inside one `MutationCoordinator.WithMutations` callback covering source and destination. P2T3/P2T4 own that call; `Stamp` does not reacquire the lock.

1. Validate input and canonicalize shared scope (zero owner), then `Settler.Settle` before issuing a token or changing the manifest. An undecided earlier prepare prevents overwriting its proof. An external or foreign-operation context with an unresolved relocation intent covering this destination returns ErrMutationPending until the global recovery sweep acquires BOTH endpoints and resolves registry authority; only renameRevision's exact private withOwnRelocation context under both endpoint locks is exempt (§6.18); a target manifest alone never proves private Rename committed.
2. Derive fallback before touching `wf` because `prior == wf` is possible. Filter positions to surviving node IDs. Precedence: displayed prior result; prior retained positions; prior manifest positions; input manifest positions; none. Preserve the immutable result reference when retained positions originate from layout. Reader errors log WARN and fall through. Never trust client `Presentation`/retained fields as a saved result.
3. Replace `wf.Presentation` with a new pointer, token, saved time, reason and destination. Deep-copy retained maps. Marshal the stamped workflow and `Journal.Record` prepared input before visibility. Restore the old pointer on marshal/journal failure. Manifest denotes the JSON value exactly committed, allowing the repo's indentation; assert JSON equality plus exact captured bytes throughout journal/store recovery.
4. Caller performs final identity/name edits before stamping, then writes stamped staging before one destination visibility change. Tree saves use `CopyTreeStamped`; No post-visibility restamp helper is introduced. Shared commits keep a durable prior-tree backup and commit exact publication proof before the receipt.
5. `Committed` first writes `MarkCommitted` while still locked. Receipt failure logs ERROR, retains prepared entry and backup, and does not enqueue; save content is already committed and remains successful. Only after receipt success request arrangement. Request failure retains entry+receipt; acknowledged request permits Remove. Backup cleanup follows durable commit proof, never precedes it. Cleanup errors log and remain recoverable.
6. `Abandon` removes only a definitely unwritten or successfully rolled-back request. Rollback failure leaves evidence/intents for settlement; never erase the only proof. Empty request/nil receiver are no-ops for Committed/Abandon.
7. `EnsureStamped` may rebuild the same revision only for a proven interrupted authoring operation at that destination. Preserve original `Presentation.SavedAt` as RequestedAt; do not generate a new token or silently re-journal a committed receipt as prepared. Mere folder existence, matching name, or source-supplied token is insufficient evidence. An unrelated pre-feature manifest must not be stamped during startup.
8. Journal and Store destinations move directly through their error-returning ports inside the recoverable P2T7 relocation protocol; no best-effort stamper relocation wrapper is introduced. Immutable manifest bytes and saved presentation remain unchanged on ownership transfer.

## Strict TDD cycles

For each cycle write assertions, run RED, add only compiling declarations if needed, observe the intended behavioral failure, implement GREEN and refactor. Compilation failure alone is not behavior evidence. Tests use full argument-honoring journal/settler fakes implementing every method above; none is a production fallback.

- [x] **2.1 Values and serialization.** Test exact JSON keys/omitempty, shared-owner normalization, nil-safe SavedRevision, legacy manifest round trip. Reject invalid token/destination/envelope values without panic. UUIDv7 A→B→A, identical-content and position-only saves all differ. Client revision and client retained metadata cannot survive Create.

> Verified on revisit: `TestRevisionMetadataE2E_CreateRejectsClientPresentation` drives actual `initDomain` → authorized Service.Create/Update → filesystem → SQLite. It rejects forged revision/time/reason/destination/retained layout metadata, asserts exact captured bytes, budget and fallback, and proves fresh UUIDv7 tokens for identical, A→B→A and position-only saves. Removing the root stamper injection fails the forged-revision assertion; restoring it passes. This is save-side provenance, not rendered acceptance.
- [x] **2.2 Stamp.** Assert settle → token → prepared record ordering; settle error means no token/write/request. Test all fallback precedence/filter cases, deleted/new nodes, partial positions, empty sources, reader failure, source pointer aliasing and deep copies. Journal/marshal failures restore the original input and never request work. Nil workflow errors cleanly.
- [x] **2.3 Receipt and enqueue.** Assert receipt before requester and Remove only after ack. Inject receipt, enqueue, Remove and rollback errors independently. Captured requests remain byte-identical and retryable; no prepare-only request reaches the sink. Include cancelled request context: a committed operation retains recovery evidence even when its caller disconnects.

> Verified on revisit: Existing receipt/enqueue/Remove/cancellation tests plus `TestRevisionStamper_RealRollbackPreservesRecoveryEvidence` now exercise actual Service.Update, filesystem tree backups/journal, SQLite registry/publication proof and requester. A post-rename error and real nonempty-directory rollback obstruction retain exact prepared bytes and intent, reject another save, and produce no SQLite job. Fresh collaborators restore all old manifest/supporting/cache bytes once I/O recovers; successful rollback abandons immediately. Mutating the error branch to Abandon loses the journal entry and fails the sink assertion.
- [x] **2.4 Resume and relocation.** Proven same-destination interrupted seed keeps token/time/input; foreign/source and arbitrary pre-feature files are rejected for resume. Error and missing-destination cases change nothing. Relocation errors preserve the durable intent. Real crash durability belongs to P2T7/P2T9.

> Current plan correction (§6.18): existing `withOwnRelocation`/`ownsRelocation` close the active-intent producer gap, and `AcknowledgedArrangementReader` plus the real Requester close ack-cleaned seed input provenance. Declare and test the seed-only port below; preserve exact-match proof and external read settlement. Run the complete resume/relocation matrix before checking 2.4; old blocker notes do not authorize skipping it.
- [x] **2.5 Engine snapshot.** Trigger the real engine with the committed stamped workflow while review is pending; assert frozen snapshot revision, positions, source reference, reason/time/destination and graph equal the committed manifest. Legacy snapshot omits Presentation. This characterization may start green: temporarily remove the JSON tag, observe the sink assertion fail, restore it and rerun.

> Verified on revisit: Strengthened `TestLayoutE2E_SaveIsAsyncAndEngineSnapshotRetainsPendingRevision` drives real initDomain authoring while an argument-honoring renderer gate keeps review pending. It compares the entire saved manifest to the persisted instance snapshot and exact SQLite captured bytes, fallback source/positions/reference, save reason/time/destination and configured budget; a subsequent authoring save leaves both historical sinks unchanged. A pre-feature manifest omits Presentation and creates no layout row. Removing Presentation only from the engine snapshot fails the complete-snapshot assertion; restoring it passes. The measurement gate also asserts the exact saved graph request, not only a call or nonempty slice. No rendered/readability acceptance is claimed.

```bash
go test ./domain/workflow ./domain/engine -run 'TestPresentation|TestNewRevisionStamper|TestRevisionStamper|TestEngine.*Presentation' -count=1 -race
go vet ./domain/workflow ./domain/engine
```

## Producer → root → sink

P2T7 `filesystem.RevisionJournal` feeds Journal; P2T7 `workflow.CommitSource` feeds Settler; P2T5 Requester feeds enqueue; P2T6 LayoutRepo feeds Displayed. P2T9 `newLayoutStack` constructs all four, injects `StamperDeps{Journal: journal, Requester: requester, Displayed: store, Settler: commits}`, then attaches the same stamper to Service and Seeder before any authoring save. Real `initDomain` tests must read the stamped manifest, SQLite captured row/attempt budget and engine snapshot. Removing any setter/injection must fail those sink assertions. HTTP/MCP inputs are decoded by their existing workflow handlers and remain service-authorized.

## Execution evidence

- Selected after skipping the already-recorded P1T4–P1T9 and P2T1 production gates. Their missing P2/P3 dependencies remain absent; this pass implements only P2T2's available slice.
- Behavioral RED with compiling value/helper/stamper stubs: value helpers, UUIDv7 request capture, missing collaborators and invalid envelopes failed assertions. Implemented the exact metadata/ports/publication proof fields and default token/time/logger dependencies; focused tests passed.
- Stamp RED: displayed/retained/prior/input/alias fallback assertions failed against input-only preparation. GREEN covers settlement-before-token-before-record, canonical shared destination, all fallback precedence and filtering, source-pointer aliasing, defensive copies, marshal/journal/cancellation restoration and nil workflow rejection. No rendering/layout import or workflow write was added.
- Receipt RED: missing receipt/enqueue/removal failed the sink/order assertions. GREEN covers receipt-first acknowledgement, independent receipt/enqueue/cleanup failures, cancellation retaining evidence, byte-identical replay and nil/empty Committed/Abandon.
- Resume RED: the empty compiling EnsureStamped stub failed both valid-resume and rejection assertions. GREEN requires an exact committed journal input after settlement, preserves the original token/time/bytes, never downgrades a receipt, and rejects foreign/pre-feature/prepared-only/changed/mismatched input and I/O failures without mutation.
- Engine snapshot characterization starts GREEN as expected: Engine.Trigger already JSON-copies loaded workflow values into the persisted instance. Removing the presentation JSON tag made the persisted-snapshot test fail; restored it. The test compares the complete manifest and verifies subsequent node/position/metadata mutation cannot change the instance. It is not a real authoring/root integration test.
- Self-review in order: (1) compared all fields, tags, interfaces and protocol ownership with contracts §§2, 6.2, 6.3 and 6.10; kept Service/seed/filesystem/SQLite/root work with its assigned owners and left the corresponding gates blocked. (2) strengthened source/request/manifest alias assertions; constant-empty fallback and skipped-receipt mutations failed their sink tests, then were restored. Removed a fake-only relocation test rather than presenting test-double behavior as production evidence. (3) matched neighboring Go conventions, documented the consumer-owned ports and lock/receipt responsibilities, grouped imports and formatted code. No compiling stub, mutation, substitute producer or fake production adapter remains.
- Final verification: prescribed focused `go test ./domain/workflow ./domain/engine -run 'TestPresentation|TestNewRevisionStamper|TestRevisionStamper|TestEngine.*Presentation' -count=1 -race`, full tests for both packages, `go vet ./domain/workflow ./domain/engine`, `go build ./...`, gofmt and `git diff --check` pass. Unrelated pre-existing `docs/visuals/` files are preserved and excluded from the commit.
- Production completion is not claimed. Later passes should skip the documented gates here and implement independent P2T3 steps, then return when the real producers/root/sinks exist.

## Revisit — durable producers and real root now available

- Selection: earlier P1 gates still lack the installed browser/reviewer sink (P3T6/P3T7); P2T1 still has its recorded sample/profile verification failure. P2T2 is the first task whose formerly missing save-side producers/root now support independent unblocked verification. Worked only this task; did not bypass or re-fail the recorded rename/seed protocol conflicts.
- Existing-behavior characterization followed contracts §6.1: wrote the new sink assertions first, observed behavioral RED with temporary production mutations, then restored the existing correct implementation to GREEN. Removing the root stamper preserved the forged client revision; removing the coordinator rejected the real save; abandoning on failed rollback erased the actual journal input; deleting only the engine snapshot's Presentation lost frozen metadata. Each named regression failed. Changing the JSON tag to `-` also failed the prescribed engine wire-key assertion. All mutations were restored; no production behavior change was needed.
- Completed 2.1, 2.3 and 2.5. Step 2.4 remains unchecked for the already-recorded upstream protocol/API conflicts; its blocker now identifies the actual current dependencies rather than claiming merged adapters are absent.
- Self-review, in order: (1) limited changes to P2T2 save metadata/provenance, caller rollback evidence and snapshot propagation; did not implement browser wiring, change commit order or add configuration. (2) asserted exact saved/request/SQLite bytes and full historical instance snapshot, not just tokens or calls; strengthened the rollback test to prove the new bytes really reached disk and the prior backup survived. Root measurement gate consumes the complete real saved graph; nil/empty/different input cannot pass. No synthetic successful render/result was introduced. (3) used existing root helpers and per-repository syscall-fault seam, kept real SQLite foreign keys, matched neighboring test style and ran gofmt. Unrelated `docs/visuals/` files are excluded.
- Verification after review: prescribed `go test ./domain/workflow ./domain/engine -run 'TestPresentation|TestNewRevisionStamper|TestRevisionStamper|TestEngine.*Presentation' -count=1 -race` and `go vet ./domain/workflow ./domain/engine` pass. New root and real-rollback tests pass under `-race`; full `go test ./domain/workflow ./domain/engine ./adapter/filesystem ./cmd/arbitron -count=1` passes. `go vet ./cmd/arbitron ./adapter/filesystem`, `go build ./...`, gofmt and `git diff --check` pass. This closes save-side/root/snapshot evidence only; missing production rendered acceptance remains with its assigned later tasks.

## Current required seed-resume contract

P2T2 declares in arrangement.go:

```go
type AcknowledgedArrangementReader interface {
    AcknowledgedArrangement(ctx context.Context, revision string) (ArrangementRequest, error)
}
```

P2T5's real Requester implements it; P2T9 passes that same Requester in StamperDeps.
EnsureStamped first uses an exact committed journal entry. Only for a seed with no live
entry may it read this acknowledged input, verify every immutable envelope field and
JSON equality, then restore Record + MarkCommitted without minting a token. Seeder owns
matching exact publication/publisher/source proof. Missing reader/input, conflicting
entry, moved/foreign destination or Store error never falls back to folder adoption.

- [x] RED→GREEN/mutation tests: acknowledged same-seed resume after journal cleanup;
  missing/wrong revision, ID, scope, owner, name, reason, time, retained/input mismatch;
  acknowledged non-seed rejection; Store outage leaves manifest/journal unchanged;
  defensive copies and repeated resume produce one input/token. Run the existing
  TestRevisionStamperAcknowledgedSeedResume plus actual P2T9 ledger-failure restart.
- [x] Resume/relocation completion uses §6.18's private active-operation context under
  both endpoint locks; external reads never receive it. Verify complete 2.4 rather than
  changing old evidence or treating the narrow marker as an exported bypass.

## Completion revisit — 2026-10-18

- Selected the first unfinished task, P2T2. Contracts §6.18 supersedes the historical producer/protocol blockers above: the real acknowledgement reader and private active-rename context are present. No other task was changed or checked off.
- Expanded `TestRevisionStamperAcknowledgedSeedResume` to cover wrong shared owner, nil identity, absent reader, conflicting committed input, Store outage, canonical shared callers, byte-preserving formatted input, defensive copies and repeated resume. Existing tests cover every remaining immutable envelope field, missing/pre-feature/foreign destinations, settlement/read/journal errors, prepared-only rejection and non-seed rejection. Repeated resume cannot call either the token or clock producer, rewrite a receipt, or enqueue before publication authority is checked.
- Strengthened the actual `initDomain` ledger-failure restart test for both immediate acknowledgement cleanup and worker-recovery cleanup. It compares the complete saved/captured manifest, retained input, reason/time/destination/ID/budget, publication publisher/source proof, supporting bytes and journal cleanup. Restart after the source becomes malformed repairs only the ledger; the saved bytes and complete SQLite row remain identical across repeated boots, with one row/token.
- Added `TestRevisionStamperActiveRenameContextIsNarrow`: real Service/Stamp/filesystem locks/journal/SQLite exercise a second intent introduced after prechecks. An unrelated intent does not block rename; a different covering intent still prevents preparation/enqueue and preserves the original workflow and row. Neither case consumes the foreign intent. No exported bypass was added.
- Added `TestRevisionStamperRelocationFailurePreservesIntent`: real journal and SQLite relocation faults retain both endpoint manifests, exact committed inputs, old SQLite history and durable intent across a fresh recovery collaborator. External reads remain pending. Repair rolls forward using the same two tokens/inputs and removes only the obsolete endpoint. The existing full registry-authoritative private/shared pre/post-identity crash matrix, undecided-winner error matrix and actual-root private/shared rename and HTTP/MCP external-read tests pass. Real process-kill durability remains covered by its owning P2T7/P2T9 gates, not claimed by these synchronous fault tests.
- Strict characterization TDD (§6.1): after writing assertions, temporarily (1) ignored the acknowledged owner, (2) hid `AcknowledgedArrangementReader` on the real root's injected Requester, (3) ended the relocation intent on journal relocation failure, and (4) exempted every intent whenever any private marker existed. Each compiled and failed behaviorally at its expected rejection, ledger, durable-intent or SQLite/save sink. Restoring the existing production implementation produced GREEN. All mutation changes are restored; no production feature change was necessary.
- Self-review completed in order: **spec** — only P2T2's remaining resume/relocation gates, no extra API/flag/policy or ledger reorder; **anti-green-theater** — real root/filesystem/SQLite sinks, exact immutable input, outage and missing-interface mutations above, no fabricated rendering evidence; **quality** — neighboring fixture/port patterns, real SQL-trigger fault, cancellation-aware journal decorators, simplified assertions and gofmt. New and existing task tests were rerun after review.
- PASS: prescribed workflow/engine focused race command and vet; complete CommitSource own-intent/authority/undecided/crashed-rename matrix under `-race`; actual-root ledger-only restart, private/shared rename, repository/HTTP/MCP pending-relocation tests under `-race`; `go vet ./cmd/arbitron`; `go build ./...`; `git diff --check`. These tests intentionally exercise missing-browser rendering failure, not successful rendered readability acceptance.
- Optional broader verification: full `go test ./domain/workflow ./domain/engine -count=1 -race` passes workflow but exposes unrelated existing engine test races: `engine_spawn_stamp_test.go:65,82` reads returned mock instances while `mockInstanceRepo.UpdateCurrentNode`, `UpdateVariables` and `SetCompleted` mutate them asynchronously. The race detector fails `TestTrigger_PartialSpawnLinkIsNotStamped`; no engine source/test files were changed in this pass. This is not reported as a green full engine suite. The prescribed Presentation-only engine race gate remains green.
