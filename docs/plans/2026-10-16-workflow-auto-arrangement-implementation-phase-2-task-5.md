# Workflow Auto-Arrangement — Phase 2 Task 5: Store, request and read contracts

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§2, 5, 6.3–6.6, 6.8–6.12. This plan replaces the former weak evidence, automatic supersede, unfenced cache-settlement and partial-journal examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Produce one authoritative revision history contract, argument-honoring test support, and domain Requester/Reader/ManifestSource. SQLite P2T6 implements the SAME executable Store suite. Domain imports no adapter.

**Files:** `domain/layout/store.go`, `manifests.go`, `requester.go`, `reader.go`, their tests; `domain/layout/layouttest/{memstore,store_contract,measure,fixtures,committed,fake_ports}.go` and tests. Phase 2/3 layout tests use external `package layout_test` to avoid layout→layouttest→layout cycles. Phase 1 internal test helpers remain separate.

## Exact production interfaces

Declare State constants and values below verbatim; no temporary Store with missing methods. All sentinel error texts are those in contracts §2.

```go
type State string
const ( StatePending State = "pending"; StateInProgress State = "in_progress"; StatePassed State = "passed"; StateNeedsAttention State = "needs_attention"; StateFailed State = "failed"; StateSuperseded State = "superseded" )
type Destination struct { Scope workflow.Scope; Name string; WorkflowID uuid.UUID }
type Record struct {
	Revision         string
	Destination      Destination
	Reason           workflow.SaveReason
	Manifest         []byte
	Retained         workflow.RetainedLayout
	State            State
	Stage            Stage
	JobGeneration    int64
	AttemptsUsed     int
	AttemptLimit     int
	Verdict          Verdict   // best stored result's verdict; "" when none
	Findings         []Finding // best stored result's unresolved findings
	Unreviewed       bool
	FailureStage     Stage
	FailureReason    string
	ResultGeneration int64 // 0 = no stored geometry
	CacheGeneration  int64
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	RequestedAt      time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
}
type StoredResult struct {
	Revision   string
	Generation int64
	Geometry   Geometry
	Review     *Review
	Preflight  []Finding
	Verdict    Verdict
	Score      Score
	CreatedAt  time.Time
}
type NewRequest struct { Revision string; Destination Destination; Reason workflow.SaveReason; Manifest []byte; Retained workflow.RetainedLayout; AttemptLimit int; RequestedAt time.Time }
type Fence struct { Revision string; Generation int64 }
type Completion struct {
	State         State     // passed | needs_attention | failed | superseded (store keeps superseded if row already superseded)
	AttemptsUsed  int
	FailureStage  Stage
	FailureReason string
	Result        *StoredResult // inserted as (revision, fence.Generation) when non-nil
	ReplaceBest   bool          // when true and Result != nil: result_generation, verdict, findings, unreviewed are replaced
}
var (
	ErrRecordNotFound   = errors.New("layout: revision record not found")
	ErrStaleGeneration  = errors.New("layout: job generation is no longer current")
	ErrNotClaimable     = errors.New("layout: revision is not claimable")
	ErrRetryConflict    = errors.New("layout: revision is not eligible for retry")
	ErrStoreUnavailable = errors.New("layout: status store unavailable")
	ErrNotCurrent       = errors.New("layout: revision is not the destination's current revision")
	ErrDestinationGone  = errors.New("layout: destination workflow no longer exists")
)
type Store interface {
	Request(ctx context.Context, req NewRequest) (Record, bool, error) // idempotent by revision; bool=created
	Get(ctx context.Context, revision string) (Record, error)
	ListActive(ctx context.Context, dest Destination) ([]Record, error) // pending|in_progress with same scope kind/owner/name
	Claimable(ctx context.Context, now time.Time, limit int) ([]string, error)
	Claim(ctx context.Context, revision, owner string, now, leaseUntil time.Time) (Record, error)
	Progress(ctx context.Context, f Fence, stage Stage, attemptsUsed int, now, leaseUntil time.Time) error
	Complete(ctx context.Context, f Fence, c Completion, now time.Time) error
	Checkpoint(ctx context.Context, f Fence, result StoredResult, replaceBest bool, now time.Time) error
	Supersede(ctx context.Context, revision string, now time.Time) error
	Retry(ctx context.Context, revision string, attemptLimit int, now time.Time) (Record, error)
	Relocate(ctx context.Context, from, to Destination, now time.Time) (int64, error)
	Result(ctx context.Context, revision string, generation int64) (StoredResult, error)
	PendingCache(ctx context.Context, limit int) ([]Record, error)
	MarkCached(ctx context.Context, f Fence, resultGeneration int64, dest Destination) error
	InvalidateResult(ctx context.Context, f Fence, resultGeneration int64, reason string, now time.Time) error
}
```

Also in store.go (P2T8 consumes, never redeclares):

```go
const CacheFormatV1 = "arbitron.layout-cache/v1"
type CacheEntry struct {
    Format string `json:"format"`
    Revision string `json:"revision"`
    Generation int64 `json:"generation"`
    JobGeneration int64 `json:"jobGeneration"`
    Contract RendererContract `json:"contract"`
    Verdict Verdict `json:"verdict"`
    Geometry Geometry `json:"geometry"`
}
type CachePublisher interface {
    PublishCache(ctx context.Context, dest Destination, entry CacheEntry, fence func(context.Context) error) error
}
type CacheReader interface {
    ReadCache(ctx context.Context, scope workflow.Scope, name string) (CacheEntry, bool, error)
}
func LocatorKey(scope workflow.Scope, name string) string
func (d Destination) Key() string
func (s State) Terminal() bool
func (r StoredResult) UnresolvedFindings() []Finding
func (r StoredResult) IsUnreviewed() bool
```

LocatorKey is `shared/<name>` or `private/<owner>/<name>` with canonical zero shared owner; WorkflowID is metadata, never locator identity. Terminal is passed/needs_attention/failed/superseded. UnresolvedFindings returns defensive non-nil preflight+review findings. IsUnreviewed is `!HasRenderedEvidence(r.Geometry,r.Review)`, including digest/full-contract/font/version/zoom/overview/readable-tile checks; Rendered=true alone is insufficient.

```go
type ManifestSource interface {
	CurrentRevision(ctx context.Context, scope workflow.Scope, name string) (string, bool, error) // ("", false, nil) when gone or pre-feature
}
type RepositoryManifests struct { /* repo workflow.Repository */ }
func NewRepositoryManifests(repo workflow.Repository) *RepositoryManifests
func (m *RepositoryManifests) CurrentRevision(ctx context.Context, scope workflow.Scope, name string) (string, bool, error)

type Requester struct { /* unexported */ }
func NewRequester(store Store, attemptLimit int, logger *slog.Logger) *Requester // attemptLimit < 1 ⇒ 1
func (r *Requester) SetNotifier(fn func(revision string))
func (r *Requester) AttemptLimit() int
func (r *Requester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error // implements workflow.ArrangementRequester
func (r *Requester) RelocateArrangements(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string) error
func (r *Requester) Retry(ctx context.Context, revision string) (Record, error)
// compile-time: var _ workflow.ArrangementRequester = (*Requester)(nil)
```

Define `ErrInvalidArrangementRequest = errors.New("layout: arrangement request needs a revision and a manifest")` and compile-time `workflow.ArrangementRequester` satisfaction.

```go
type DisplayStatus string
const ( DisplayPending DisplayStatus = "pending"; DisplayArranging DisplayStatus = "arranging"; DisplayRendering DisplayStatus = "rendering"; DisplayReviewing DisplayStatus = "reviewing"; DisplayComplete DisplayStatus = "complete"; DisplayNeedsAttention DisplayStatus = "needs_attention"; DisplayFailed DisplayStatus = "failed"; DisplayLegacy DisplayStatus = "legacy"; DisplayUnavailable DisplayStatus = "unavailable" )
type Provenance string
const ( ProvenanceArranged Provenance = "arranged"; ProvenanceRetainedGeometry Provenance = "retained_geometry"; ProvenanceRetainedPositions Provenance = "retained_positions"; ProvenanceLegacy Provenance = "legacy" )
type View struct {
	SavedRevision     string                   `json:"savedRevision"`
	JobGeneration     int64                    `json:"jobGeneration"`
	ResultGeneration  int64                    `json:"resultGeneration"`
	State             State                    `json:"state,omitempty"`
	DisplayStatus     DisplayStatus            `json:"displayStatus"`
	Stage             Stage                    `json:"stage,omitempty"`
	AttemptsUsed      int                      `json:"attemptsUsed"`
	AttemptLimit      int                      `json:"attemptLimit"`
	Findings          []Finding                `json:"findings"`
	FailureStage      Stage                    `json:"failureStage,omitempty"`
	FailureReason     string                   `json:"failureReason,omitempty"`
	Evidence          *Evidence                `json:"evidence,omitempty"`
	Contract          *RendererContract        `json:"contract,omitempty"`
	Geometry          *Geometry                `json:"geometry,omitempty"`
	Retained          *workflow.RetainedLayout `json:"retained,omitempty"`
	Provenance        Provenance               `json:"provenance"`
	Unreviewed        bool                     `json:"unreviewed"`
	RetryEligible     bool                     `json:"retryEligible"`
	Available         bool                     `json:"available"`
	AvailabilityError string                   `json:"availabilityError,omitempty"`
}
type Reader struct { /* store Store; journal workflow.RevisionJournal; cache CacheReader */ }
func NewReader(store Store, journal workflow.RevisionJournal, cache CacheReader) *Reader
func (r *Reader) View(ctx context.Context, wf *workflow.Workflow) View   // never errors; problems are in-band
func IsActive(s DisplayStatus) bool // pending|arranging|rendering|reviewing
```

View fields/tags above are the single JSON source mirrored by P3T1; no adapter invents a second response.

## Store transition semantics

RunStoreContract must assert every row, error and copy behavior against MemStore and SQLite. All methods honor context cancellation, copy mutable input/output slices/maps/pointers, and normalize UTC timestamp precision consistently. Use full precision sufficient for short leases; do not truncate a subsecond heartbeat/lease comparison to whole seconds.

| Operation | Required behavior |
|---|---|
| Request | Idempotent by revision; duplicate never overwrites immutable manifest/fallback/reason/time/budget, resets generation or moves a relocated row back. New row pending/queued, generation 1, zero attempts/result/cache, non-nil empty findings. |
| Claim | Pending claim keeps generation. Expired in_progress lease claim increments generation, resets attempts and returns THAT atomic claimed owner/generation, not a later Get. Unexpired or terminal returns ErrNotClaimable; missing row ErrRecordNotFound. Strict lease expiry is `< now`. |
| Progress | Exact job-generation and in_progress state fence; update latest stage/attempt and lease atomically. Stale worker cannot extend the new worker's lease. |
| Complete | Fence job generation and in_progress/superseded. Transaction inserts immutable result for fence.Generation and updates row together. Reject a second conflicting result at that key. Keep superseded lifecycle even when candidate passes. ReplaceBest controls result pointer/verdict/findings/unreviewed; worse retries preserve old best. Clear lease; stage done and FinishedAt. |
| Checkpoint | Shutdown-only best preservation under in_progress/job fence; transaction inserts immutable result and optionally best pointer while state/stage/lease remain interrupted/reclaimable. Same result is idempotent, conflicting content errors. Expiry reclaim increments generation. |
| Retry | needs_attention/failed or unfinished superseded; ordinary valid passed conflicts. Increment generation, pending/queued, attempts zero/current budget, clear failure/lease/start/finish, retain best and set CacheGeneration=0. |
| Supersede | Only pending/in_progress; terminal no-op, missing errors. Preserve best/fallback. No implicit call from Requester on saves, retries or recovery. |
| Relocate | Change destination metadata for all rows at full locator, preserve immutable Manifest/Retained/results/revision/time, clear CacheGeneration only for actual destination changes. Nil to.WorkflowID preserves existing ID. |
| Claimable | Pending+expired leases sorted RequestedAt/revision, limit<=0 means all. |
| PendingCache | Best result exists and cache settlement is behind; sorted UpdatedAt/revision. Retry/Relocate reset settlement so retained best can republish. |
| MarkCached | Match exact Fence, result generation and destination; then settle that result. Wrong job/result/destination returns ErrStaleGeneration, missing ErrRecordNotFound. A late old process cannot mark a moved/new retry row cached. |
| InvalidateResult | Passed, or superseded with VerdictPassed, with exact job/result fence → needs_attention and supplied reason. Preserve immutable results/input; stale/invalid state errors. Used only by retry, never Reader. |
| Result/Get | Missing sentinel separate from storage/corruption failure. All history survives workflow/instance deletion and relocation. |

## Requester and Reader behavior

RequestArrangement validates nonempty revision/manifest and matching stamped revision and a valid canonical request destination before Store.Request; it never reloads mutable graph content as job input. A committed relocation changes Request.Scope/Name but NEVER Manifest.Presentation; validate the token, not equality between relocated locator and original snapshot destination. Invalid graph topology can remain saved and fail asynchronously: do not call BuildGraph as an authoring-save rejection. Persist attempt limit max(configured,1). Notify only acknowledged durable requests; duplicate notify is safe. Errors propagate to stamper, which retains receipt. No automatic cross-revision supersede: authoritatively saved history/historical retries remain eligible; publication CAS protects current layout. Relocate forwards full canonical from/to locators and errors. Retry preserves best, uses current configured budget and notifies after ack.

Retry of a passed row (including superseded with VerdictPassed) first checks captured graph/result, full Compatible, HasRenderedEvidence, zero blocking preflight/review findings and consistent passed verdict. Missing/incompatible/unsubstantiated result calls InvalidateResult under exact job/result fence before Store.Retry. Store outage does not invalidate. Valid passed conflicts. QueryService P3T8 supplies authorization and current-revision lock/recheck; Requester never provides user authorization.

Reader is read-only and returns errors in-band. Nil/tokenless workflow is legacy and never requests work. A stamped missing row is pending/queued with captured fallback and Retry; missing journal means explicit awaiting recovery, not legacy. Journal I/O error is an availability error. State mapping: pending→pending; in_progress stage→arranging/rendering/reviewing (publishing→reviewing); passed→complete ONLY with compatible reviewed result; needs_attention/failed explicit; superseded complete only when its own result supports pass, otherwise needs_attention.

A missing/corrupt/incompatible referenced result or unsupported pass evidence never shows complete. Clear stale evidence/contract, explain error, retain fallback, show Retry. Failed/superseded rows can still show usable best geometry. Retained complete geometry resolves immutable FromRevision/FromGeneration and validates against the NEW graph/full renderer contract; reference survives cache/source deletion. Otherwise surviving coordinates route actual new connections; all retained fallbacks are Unreviewed for the new revision.

On store outage only, cache may provide geometry for exact revision, positive job/result generations, cache format, matching full contract and compatible graph. It remains Available=false, DisplayUnavailable, explicit availability error, never a claimed reviewed pass. If cache also fails/mismatches, captured retained positions remain. Neither current WorkflowID nor a name lookup substitutes a later revision for a historical snapshot.

## Exact test-support producers

P2T5 owns these before P2T8/P3T8 consume them:

```go
func NewMemStore() *MemStore
func (m *MemStore) SetFailure(err error)
func RunStoreContract(t *testing.T, newStore func(t *testing.T) layout.Store)
func Measurements(g layout.Graph, c layout.RendererContract) layout.Measurements
func MeasureItems(c layout.RendererContract, items []layout.TextItem) layout.Measurements
func SampleWorkflow() *workflow.Workflow
func Arranged(t testing.TB, wf *workflow.Workflow) layout.Geometry
func ManifestJSON(t testing.TB, wf *workflow.Workflow) []byte
func Artifacts(geo layout.Geometry) []layout.Artifact
func NewFakeManifests() *FakeManifests
func NewFakeJournal(entries ...workflow.ArrangementRequest) *FakeJournal
func NewFakeCache() *FakeCache
func NewFakeCommittedRevisions() *FakeCommittedRevisions
func (f *FakeCommittedRevisions) Prepare(req workflow.ArrangementRequest)
func (f *FakeCommittedRevisions) Commit(revision string)
func (f *FakeCommittedRevisions) Abandon(revision string)
func (f *FakeCommittedRevisions) SetError(err error)
func (f *FakeCommittedRevisions) CommittedRequests(ctx context.Context) ([]workflow.ArrangementRequest, error)
```

MemStore.SetFailure makes EVERY Store method return an error wrapping ErrStoreUnavailable until cleared. FakeManifests has Set(scope,name,rev), Remove(scope,name), SetErr(error), Calls() int and exact CurrentRevision. FakeJournal implements the COMPLETE P2T2 RevisionJournal (prepared/receipts/quarantine/relocation), SetErr(error), Has(string) bool and Len() int; constructor entries are prepared unless a test explicitly MarkCommitted. FakeCache implements both ports, SetPublishErr/SetReadErr, Seed(scope,name,entry), Published(dest) []CacheEntry and PublishCalls() int; failed fence writes nothing. Every fake is mutex-protected and copies arguments/results. FakeCommittedRevisions returns only committed entries, ordered RequestedAt/revision, including historical inputs; never a zero request or a catalog scan.

Measurements uses deterministic 7px/rune at 13px scaled per style/max width, handles empty/zero-wrap Unicode safely; it is unit support only. Arranged invokes actual Arrange and validates against actual graph; SampleWorkflow is the four-node start/check/fix/ship branching fixture with four edge occurrences. Artifacts produces unit overview/tile envelopes using actual canvas/zoom/digest, with nonempty deterministic identifiers; these are never production screenshot evidence. P2T8 owns FakeRenderer/FakeReviewer, not FakeCache. Tests must use constructor/accessor methods rather than reading mutable counters during jobs.

## Strict TDD cycles and sink assertions

For every cycle write assertions, run RED against compiling behavior, implement minimum, GREEN, refactor and verify the mutation probe. Do not paste implementation into tests or count compile-only failure as the behavioral RED.

- [x] **5.1 Models/helpers.** Exact JSON, State/locator values, nil IDs/canonical shared owners; actual geometry fixtures and measurement argument changes; evidence mismatches/empty artifacts fail IsUnreviewed/pass assertions.
- [x] **5.2 MemStore and shared suite.** Every transition above; stale leases/retries/results/settlements and wrong destination; defensive copies, failed completion/checkpoint atomicity, checkpoint idempotency/conflict and interrupted-state preservation, cancellation, injected unavailable errors, old immutable result retained, two claims return their own fences. Invalidate passed and superseded-passed invalid results with missing/wrong/current fences. Relocate/Retry make cache pending again.
- [x] **5.3 Complete fakes.** Full journal port compile assertion and behavioral prepare/receipt/quarantine/relocation tests, destination-specific cache and late fence refusal, committed-only source preserving exact captured bytes and errors. No absent collaborator is hidden by a fake.
- [x] **5.4 Manifest source.** Repository.Load feeds SavedRevision, no ID requirement, gone/pre-feature return empty/false; propagate read errors and cancellation without stamp/write/enumeration.
- [x] **5.5 Requester.** Captured input/budget reaches row sink; duplicate immutable input unchanged; no notify on unavailable store; no unrelated supersede; history retry and live recovery; malformed envelope vs valid saved graph failure; relocation-before-enqueue accepts the new request destination with immutable original snapshot bytes; relocated replay cannot undo relocation. Actionable retry for rejected passed geometry/evidence; valid pass conflicts, superseded-invalid-pass retry is actionable, and stale invalidation does nothing.

> Resolved on revisit: P2T6–P2T9 now construct the actual producer chain. `TestLayoutE2E_LiveEnqueueOutageRecoversByPoll` boots initDomain, fails real SQLite enqueue, saves two different graphs, and lets only the production periodic sweep recover both committed journal inputs without restart/resave/manual recovery. Both terminal rows preserve exact manifest/fallback/reason/time/destination and configured budget 7; replaying the old receipt creates no duplicate or supersede. Replacing Requester's budget handoff with 1 produced behavioral RED (expected 7, actual 1); restoring the existing implementation produced GREEN. Domain tests continue to cover envelope validation, relocated replay, notification gating and fenced invalid-pass retry.
- [x] **5.6 Reader.** Exercise all states/stages, missing row/journal, store/cache error/mismatch, missing/corrupt result, valid and forged evidence, full-contract/text/parallel-edge incompatibility, compatible retained reference, deleted source/cache and partial positions. Same stamped manifest yields identical revision/status/geometry for workflow and instance consumers. Reader must perform zero writes.

> Resolved on revisit: the assigned production query and browser producers now exist. `TestLayoutReader_RealBrowserWorkflowAndInstanceParity` boots initDomain, authors a real graph, waits for actual Chrome/ReadabilityReviewer evidence in SQLite/PNG files, triggers persisted engine snapshots, and compares the complete workflow/instance Views (not two nil geometries). A compatible new save resolves the immutable retained result with pending/unreviewed status and no inherited evidence. Both snapshots retain their exact reviewed/retained geometry after cache and definition deletion. With the worker stopped, reads leave the pending row, immutable result and manifest bytes unchanged and do not recreate cache/directories; the existing domain read-only Store traps every write method across the error/state matrix. Removing actual result forwarding, retained geometry forwarding, or root stamper injection independently makes the new sink assertions fail. Every mutation was restored.

```bash
go test ./domain/layout/... -count=1 -race
go vet ./domain/layout/...
```

## Execution evidence

- Selected P2T5 after the previously recorded P1/P2 dependency gates; no earlier task was repeatedly re-failed and no P2T6 implementation was started.
- In step order, added assertions and minimal compiling stubs, observed behavioral RED, then implemented and observed GREEN for: (5.1) values/measurement/geometry/artifact helpers; (5.2) full MemStore and reusable RunStoreContract; (5.3) complete mutex-protected journal/cache/manifests/committed-source fakes; (5.4) repository-backed revision reads; (5.5) Requester and captured-input/retry sinks; (5.6) truthful read-only Reader and exact View JSON.
- The shared Store suite covers immutable request/result history, defensive nested copies, subsecond strict lease expiry/reclaim, atomic claim ownership, stale progress/completion/checkpoint fences, idempotent/conflicting checkpoints, failed insertion atomicity, retained best across worse retry, supersede, passed/superseded-pass invalidation, locator isolation, relocation and job/result/destination cache fences, UTC nanosecond preservation and cancellation. MemStore injection tests exercise every method's unavailable sentinel.
- Self-review, in required order: (1) checked exact APIs/JSON/defaults/ownership and retained no extra option or automatic supersede. Added behavioral RED→GREEN corrections for result-read outage cache fallback, captured revision mismatch on retry, and empty fake manifest revisions. (2) Temporarily removed evidence validation, job-generation fencing, committed-only filtering, repository revision propagation and Requester manifest forwarding, and replaced requested rune measurement with a constant. The corresponding sink/Unicode/lease/prepare-only tests failed; independently removing Reader's pass guard failed unsupported-pass assertions. All mutations restored. These probes are domain/unit evidence, not a substitute for the missing real-root/browser sinks. (3) followed neighboring Go style, grouped imports, documented ownership/fences, and used explicit deep copies rather than JSON-based cloning or unsafe aliases.
- Final verification: `go test ./domain/layout/... -count=1 -race`, `go vet ./domain/layout/...`, `gofmt -l domain/layout/` and `git diff --check` pass. No production fake renderer/metrics/root or comment accepting a missing input was introduced.

## Production provenance

P2T9 constructs LayoutRepo (P2T6), RepositoryManifests over live repo, Requester with YAML/env budget (P2T1), and Reader with real journal/cache (P2T7). Stamper/Worker use the same Requester/Store; P3T8 QueryService reads the same Reader and retries through the same Requester/coordinator. Root sink test authors via initDomain and inspects SQLite captured graph, attempt limit and terminal record. Removing notifier does not lose durable work (sweeps recover); removing real save injection must fail the row assertion. No layouttest fake is used by the production root.

## Completion revisit — 2026-10-17

- Selection: P2T1 still has its recorded unrelated sample/default-profile regression (the sample still references the missing aliases); P2T2–P2T4 still have the recorded active-rename/receipt-retention/seed-rollback protocol blockers. Did not re-fail those windows. P2T5's missing production dependencies have landed, making its two remaining gates independently executable. No other task was implemented or checked off.
- Strict characterization TDD, in step order: strengthened 5.5's real live-recovery test, temporarily replaced the budget handoff with 1, observed expected 7/actual 1 RED, restored the existing production handoff, GREEN. Added 5.6's real-browser/root parity and retained-reference test; removing Reader's result forwarding produced a nil-geometry behavioral RED, restoring it produced GREEN. Production functionality already existed from the assigned later tasks, so no speculative behavior change was needed.
- Ordered self-review: **(1) Spec compliance:** reviewed 5.5/5.6 and contracts §§6.1, 6.6–6.9 against the existing Requester/Reader matrix and actual root. Only closed the missing provenance gates; no extra flags, API, policy, fake root, unrelated protocol fix or release-acceptance claim. **(2) Anti-green-theater:** independently removed real root stamper injection (empty saved revision failed) and retained-result forwarding (nonempty geometry equality failed), in addition to budget/result mutations above. All restored. The new browser test compares full Views, requires nonempty graph geometry and checks actual PNG hashes; deleted cache/definition cannot be hidden by a name lookup. **(3) Code quality:** followed neighboring root helpers, env isolation, context/auth and cleanup conventions; simplified repeated-read/table assertions and used an exact int64 generation assertion; gofmt applied.
- After all review fixes/mutation restoration: `go test ./domain/layout/... -count=1 -race` PASS; `go vet ./domain/layout/... ./cmd/arbitron` PASS; `go build ./...` PASS; focused root tests under `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^(TestLayoutReader_RealBrowserWorkflowAndInstanceParity|TestLayoutE2E_LiveEnqueueOutageRecoversByPoll|TestLayoutE2E_NewSaveDoesNotSupersedeHistoricalWork|TestLayoutHTTPE2E_RetryRepairsIncompatiblePersistedPass|TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate)$' -count=1` PASS (33.275s, no skips). `git diff --check` PASS. Mutation files have no remaining diff. Existing unrelated `docs/visuals/` work is preserved and excluded from the commit.

## Acknowledged seed input producer (required by P2T2/P2T4)

The existing Requester owns:

```go
func (r *Requester) AcknowledgedArrangement(ctx context.Context, revision string) (workflow.ArrangementRequest, error)
var _ workflow.AcknowledgedArrangementReader = (*Requester)(nil)
```

Read Store.Get's immutable manifest/retained/reason/time/revision and current committed
Destination fields regardless of job state; return copies, never requeue or reload a
workflow. Translate ErrRecordNotFound to workflow.ErrNotFound and propagate outages.
No new field or backing store is needed. The production root already passes this Requester
to the stamper; tests must prove that type assertion and the SQLite sink, not substitute
an empty acknowledgement fake.

- [x] RED→GREEN or existing-behavior mutation: pending/failed/passed/superseded rows
  return the exact saved request; missing row, cancellation, outage and mutation of a
  returned map/slice preserve stored bytes; no notifier call and no new row. Real seed
  ledger failure resumes after journal cleanup through initDomain with the same token.
  Removing this method from the actual injected collaborator must fail that resume sink.

## Acknowledged input completion revisit — 2026-10-18

- Selected this task's sole remaining step in numeric phase/task order. The required Requester method and root seed producer already exist; no later task was implemented and no production behavior change was necessary.
- Added `domain/layout/requester_acknowledged_test.go`: pending/failed/passed/superseded history returns the entire exact request with committed relocated destination metadata and unchanged original manifest/retained reference/reason/time/ID. Mutating returned manifest bytes and retained positions leaves subsequent reads and the complete stored row unchanged. Missing input maps to workflow.ErrNotFound without insertion; cancellation and store outage return zero input with the correct error. The shared read-only Store trap rejects every write method, and notifier callbacks fail the test.
- Existing-behavior TDD: temporarily removed manifest forwarding in AcknowledgedArrangement; all four state assertions failed behaviorally against compiling production code. Restoring forwarding made the focused tests pass.
- Real producer/sink TDD: reused `TestSeedSampleWorkflows_LedgerOnlyFailureResumesAfterRecovery`, which boots initDomain with real filesystem/SQLite collaborators, fails only the seed ledger append, cleans journal input through both direct acknowledgement and worker recovery, changes the source, and restarts. Temporarily wrapped the actual root-injected Requester as only workflow.ArrangementRequester, hiding AcknowledgedArrangement while preserving enqueue/relocation. Both cases failed at the missing `.samples_seeded` sink after restart. Restoring the existing injection passed; exact saved bytes, immutable terminal row, publication identity and one-row history remain asserted. All production mutations were restored.
- Ordered self-review: **(1) Spec compliance:** only the remaining acknowledged-input gate, exact captured fields/error semantics and real cleaned-journal seed resume; no new option, policy or unrelated feature. **(2) Anti-green-theater:** nonempty manifest/retained map/reference and changed destination reach the returned request; production interface removal fails the actual resumed ledger sink, not a call counter. Nil/empty forwarding fails the state matrix. **(3) Code quality:** external package tests reuse neighboring saved-request/manifest/read-only Store helpers, public Store transitions and existing root fixtures; deterministic lifecycle offsets, bounded existing root waits, no sleeps or duplicate fake collaborators. No review fixes were needed.
- Final verification after mutation restoration/review: `go test ./domain/layout/... -count=1 -race`, `go vet ./domain/layout/...`, and `go test ./cmd/arbitron -run '^TestSeedSampleWorkflows_LedgerOnlyFailureResumesAfterRecovery$' -count=1 -race` PASS. gofmt and `git diff --check` PASS. Browser-independent seed provenance is proven; later tasks retain their browser/readability acceptance gates.
