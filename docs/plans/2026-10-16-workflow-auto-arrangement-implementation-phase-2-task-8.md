# Workflow Auto-Arrangement — Phase 2 Task 8: Bounded worker, recovery and publication

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§1–2, 6.3–6.6, 6.8–6.12. Replaces the former missing lifecycle methods, progress-only lease renewal, unbounded detached cleanup and result-only publication fence examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Process each committed immutable saved input with its stored attempt budget, preserve best evidence/fallback, publish only to the current revision/job/destination and recover live outages without another save or restart.

**Files:** `domain/layout/worker.go`, `worker_test.go`, `worker_cache_test.go`, `worker_cancel_test.go`, `worker_lifecycle_test.go`, `worker_recovery_test.go`, `domain/layout/layouttest/fakes.go` and tests. Import ports/domain only. P2T5 already owns Store/Cache ports and FakeCache; never redefine them here.

## Exact interfaces

```go
type WorkerConfig struct {
    Store Store
    Coordinator *Coordinator
    Journal workflow.RevisionJournal
    Committed workflow.CommittedRevisionSource
    Manifests ManifestSource
    Cache CachePublisher
    Requester *Requester
    Owner string
    Concurrency int
    PollInterval time.Duration
    LeaseDuration time.Duration
    JobTimeout time.Duration
    StageTimeout time.Duration
    Clock func() time.Time
    Logger *slog.Logger
}
func NewWorker(cfg WorkerConfig) (*Worker, error)
func (w *Worker) Notify(revision string)
func (w *Worker) Start(ctx context.Context)
func (w *Worker) Stop()
func (w *Worker) Recover(ctx context.Context) error
func (w *Worker) Drain(ctx context.Context) error
func (w *Worker) ProcessRevision(ctx context.Context, revision string) error
func (w *Worker) ReconcileCache(ctx context.Context) error
```

ErrWorkerMisconfigured text: `layout: worker requires store, coordinator, journal, committed source, manifests, cache and requester`. Reject every missing required port. Defaults: owner hostname:pid:random; concurrency 2; poll 15s; lease 5m; job 15m; per Measure/Render/Review stage 2m; clock time.Now; logger slog.Default. All persistence/recovery/fence/cache calls, including detached finalizers, have bounded 30s contexts.

Consumes P1T9 Coordinator.Run/WithStageTimeout and HasRenderedEvidence; P2T5 Store including Checkpoint and fenced MarkCached; P2T7 production CommitSource and cache; P2T5 Requester and complete journal fakes. Phase 1 NewRenderRequest has FOUR arguments `(revision, attempt, geometry, nodeTypes)` and RenderRequest includes WorkflowNodeTypes map from the captured manifest.

## 8.1 Argument-honoring renderer/reviewer test support

```go
type ReviewScript struct { Findings []layout.Finding; Err error }
type FakeRenderer struct {
    MeasureErr error
    RenderErr error
    RenderDelay time.Duration
    OnRender func(req layout.RenderRequest)
    Calls int // access through RenderCalls while running
    // mutex-protected counters and request captures
}
func NewFakeRenderer() *FakeRenderer
func (r *FakeRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error)
func (r *FakeRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error)
func (r *FakeRenderer) RenderCalls() int
func (r *FakeRenderer) MeasureCalls() int
func (r *FakeRenderer) Digests() []string
func (r *FakeRenderer) Revisions() []string
func (r *FakeRenderer) MaxInFlight() int
type FakeReviewer struct { Script []ReviewScript /* mutex-protected */ }
func NewFakeReviewer(script ...ReviewScript) *FakeReviewer
func (r *FakeReviewer) SetScript(script ...ReviewScript)
func (r *FakeReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error)
func (r *FakeReviewer) Calls() int
func (r *FakeReviewer) Attempts() []int
```

Measure uses P2T5 MeasureItems with requested styles/text/wrap widths. Render rejects empty revision, invalid attempt, missing/wrong node type coverage or mismatched type caption, copies all request args, computes actual GeometryDigest and produces matching contract/zoom/overview+tile envelopes via Artifacts. Cancellation/delay honors context. Review checks rendering revision/attempt/digest/full contract/type-derived report identity before claiming Rendered; copies actual argument's artifacts/evidence and scripted findings, sets nonzero ReviewedAt and FontsReady=Report.FontsReady && Report.FontFamilyResolved, and supplies complete actual-request node/edge/state coverage in its unit report (never an empty successful report). Empty or mismatched request never passes. These are unit envelopes; production reviewer later validates actual PNG bytes and DOM, never these fake paths. Test changed arguments and concurrency counters explicitly.

## 8.2 Per-revision processing

Claim with bounded context; not-found/not-claimable is an idempotent skip, other errors propagate. Keep returned fence/owner, never read another claimant's generation. Decode ONLY rec.Manifest; validate revision equals record token and use rec.AttemptLimit for Job.MaxAttempts. ManifestSource may check currency for publication but never supplies graph content. Corrupt saved input becomes failed/arranging with retained fallback, without panic.

Run coordinator under job timeout with an independent per-job copy carrying StageTimeout; do not mutate shared coordinator. Heartbeat every LeaseDuration/3 renews current stage/attempt throughout one long Measure/Render/Review, not merely at progress transitions. Synchronize progress and heartbeat; stale generation/supersede cancels the job. Latest stage identifies the exact failure. Lease/persistence outage never synthesizes pass; job remains recoverable after lease expiry when it cannot finalize.

Map coordinator verdict truthfully: actual reviewed pass→passed; budget/no-refinement exhaustion→needs_attention with reason/findings; measure/render/review/decode/deadline errors→failed. AttemptsUsed is total coordinator starts (limit one means at most one); stage error does not reset budget. Best retains its OWN findings/evidence; no candidate means captured fallback. Compare new candidate with existing best: reviewed evidence outranks unreviewed, pass outranks blocking candidate, then shared Score.Less/tie order. Worse retry leaves old best. Store both generation result and selected best atomically; explicit superseded lifecycle remains superseded even if its candidate passes.

## 8.3 Cancellation and interrupted work

Distinguish parent shutdown from job deadline/review failure and explicit Supersede. On shutdown stop new claims, cancel jobs, let coordinator return its completed best, and bounded Store.Checkpoint that best under the original in_progress fence. Do NOT terminal-complete interrupted work: state/lease remain eligible for automatic expiry recovery, generation increments on reclaim. Identical checkpoint replay is safe; conflicting immutable result errors. Previous best survives even if no new candidate completed. On explicit supersede Complete may preserve best under superseded state; deadline/reviewer failure is terminal failed and retryable. A stale claimant cannot checkpoint/complete newer work.

## 8.4 Cache publication and reconciliation

After Complete, reload current Record including latest Destination, JobGeneration and ResultGeneration, even when ReplaceBest=false. Load its immutable best and validate it; build CacheEntry with `Generation=ResultGeneration`, `JobGeneration=current job generation`, exact contract/verdict/geometry. Publish only as the current worker generation; stale claim cannot republish retained old best after a newer retry.

Filesystem publisher holds destination lock and checks current manifest token, then callback reloads Store and compares ALL job/result/destination values. Ownership move during render therefore publishes only at new location; old folder is never recreated. MarkCached passes exact fence/resultGeneration/destination and cannot settle after concurrent retry/move. Retry and Relocate reset settlement, enabling current retry to republish retained best with current JobGeneration. Noncurrent/gone results are history, so settle only the matching checked fence/destination to prevent unpublishable rows starving reconciliation. Transient errors stay pending; stale settlement errors leave current work pending.

ReconcileCache uses the same path for every pending cache row, reloads destination/fences, aggregates errors and continues other records. Cache failure does not turn a valid stored review into failed or discard it. No cache publication calls Service.Update, Stamp or RequestArrangement.

## 8.5 Lifecycle

Notify is nonblocking, coalesces by revision, can buffer before Start, and is no-op after Stop. Queue overflow loses only a hint; durable Claimable sweep finds all work. One dispatcher bounds active jobs by Concurrency; no unbounded goroutine per notification and no Add-after-Wait race. Start runs Recover once, an immediate claim/cache sweep, then notifications/ticker. Start/Stop are idempotent, including Stop-before-Start and double calls. Every poll performs recovery, cache reconciliation and claim sweep even after earlier poll error. Avoid one permanently failing destination starving others.

Stop cancels dispatch/jobs/heartbeats and JOINS all before returning. No Store/cache/renderer call starts after Stop returns; root closes renderer/DB only after it. Drain uses same concurrency bounds, processes everything claimable at its start, waits for jobs and returns joined errors; cancellation ends finite work without busy looping.

## 8.6 Proven committed recovery

Recover calls Committed.CommittedRequests with bounded context; handles returned committed requests even alongside errors.Join. It never treats Journal.List/Entries prepared input as proof and never enumerates catalog. For each proven request call Requester.RequestArrangement, Remove journal entry+receipt only after ack. Source/request/remove errors preserve evidence; a duplicate row does not change input/generation. CommitSource owns quarantine/rollback and relocation, not Worker. Historical committed input remains eligible despite newer/deleted current workflow; CAS merely prevents current publication. No automatic supersede of another revision or authorized historical retry.

## 8.7 Strict TDD and exact sinks

For EACH numbered group below: write failing assertion; run focused command against compiling stubs and observe behavioral RED; implement minimum; GREEN/refactor; remove one guard temporarily to show the sink test fails, then restore. Port-call counts alone are insufficient.

- [x] **8.1 Fakes.** Changed revision/attempt/geometry/types/styles produce changed matching output; missing args reject, context delay cancels, script errors and counters are race-safe.
- [x] **8.2 Process.** Actual captured nodes/edges/digest reach StoredResult; mutable source later changes but job does not. Test budget 1/5, exact attempt count, pass/evidence, exhaustion/early no-refinement, nil geometry, later-attempt error retaining earlier reviewed best, corrupt input and Store outage. Retry retains superior prior geometry/findings; no pass with missing evidence.
- [x] **8.3 Cancellation.** Gate Measure/Render/Review separately; stage timeout reason exact and completed best survives. Gate persistence honoring context and assert finalizer deadline exists and is at most 30s, then have the fake return promptly. Shutdown checkpoints completed best, leaves interrupted state, expiry reclaim uses higher generation and old worker writes reject.
- [x] **8.4 Cache.** Late old revision; late generation while ResultGeneration remains old; worse retry republishing old best with NEW JobGeneration; old settlement racing retry/move; ownership move during render; deleted/recreated same name with new token; cross-process CAS; cache write outage/reconcile; unpublishable history cannot starve current cache.
- [x] **8.5 Lifecycle.** Start drives both notified and unnotified durable rows to actual terminal results, concurrency max 2 under ten blocked jobs, full queue recovered, before-start notify, cancelled/double start/stop/drain, zero collaborator calls after Stop.
- [x] **8.6 Recovery.** Prepared/committed/abandoned fake source returns only committed; duplicate replay one row; committed history/deletion still processed without cache publish; source partial error handles other committed entries; Store recovers on later poll without restart/resave. Reject phantom unrecorded publish through real P2T7 source integration.

> RESOLVED (revisit): P2T9 and P3T4–P3T7 now supply the actual root/worker/notifier and browser/reviewer. `TestLayoutRender_WorkerRecoveryUsesCommittedSavedInputs` drives initDomain → authoring saves during a real SQLite enqueue outage → committed journal receipts → live periodic recovery → real browser/DOM/PNG review → immutable results/Reader/current cache. It proves older and deleted revisions retain their captured input and budget, rejects/rolls back a visible shared tree without publication proof, checks PNG hashes and prevents deleted-folder recreation. Removing the dispatcher's recovery call makes the terminal-row assertion fail; restored. Existing root serve/MCP command and shutdown/checkpoint/reclaim tests also pass under -race. This closes this worker recovery gate, not the separate rename/stdio identity or multi-sample release gates.
- [x] **8.7 Lease.** Two workers, render longer than short lease, first heartbeats so second cannot claim; actual lease loss cancels first and fences all writes. Stage/attempt heartbeat data remains correct under race detector. Historical retry is not superseded by new save/recovery.

```bash
go test ./domain/layout/... -run 'TestNewWorker|TestWorker|TestFakeRenderer|TestFakeReviewer' -count=1 -race
go vet ./domain/layout/...
```

### Pass verification and self-review

- Each numbered group was exercised RED against compiling stubs, then GREEN. Restored behavioral mutations failed at the sinks for revision identity, stored attempt budget, shutdown checkpoint vs terminal completion, job-generation cache fencing (even with unchanged ResultGeneration), durable claim sweeps, committed-source vs prepared-journal replay, and lease heartbeats.
- Spec review retained exactly WorkerConfig's required ports/defaults and assigned worker/fake scope. Added checks for captured corrupt-input fallback, independent per-job stage timeouts, interrupted prior-best retention and permanently failing cache destinations.
- Anti-green-theater review strengthened overflow to distinct hints and actual durable terminal rows, checked zero persistence/render/cache/manifest calls after Stop, and added separate-process SQLite retry → real filesystem cache rejection/republish assertions. Recovery asserts terminal SQLite graph/evidence and actual cache bytes, not port counts.
- Quality review followed neighboring domain/port patterns, named bounded-I/O/hint constants, removed an unused test option, and kept adapter imports out of production domain code.
- Gates: prescribed focused `-race` command and `go vet ./domain/layout/...`; full `go test ./domain/layout/... -count=1 -race`; `go test ./domain/workflow ./adapter/sqlite ./adapter/filesystem -count=1` all pass. Production-root/browser acceptance remains blocked above.

## Root producer → sink

P2T9 builds real LayoutRepo, journal, CommitSource (exact SQLite publication/registry/tree proof), manifest source, cache, requester and Coordinator; injects all WorkerConfig fields and requester.SetNotifier(worker.Notify). Saved input comes from P2T2 Stamp via P2T3/P2T4 writes; attempt limit from P2T1 YAML/env via Requester to durable row. P2T9 actual serve/MCP tests inspect terminal SQLite captured nodes/budget/best and shutdown/restart checkpoint. P3T7 replaces explicit rendererUnavailable with actual browser/DOM/raster producers; this task's fakes never enter production. P3T8 Reader/QueryService supplies identical revision result to both views.

## Recovery completion revisit — production acceptance

Selection: earlier remaining gates still require the recorded sample/profile correction or
rename/seed protocol decisions; no repeated known-failing acceptance was run. This task's
only remaining gate depended on collaborators which have since landed.

- Added `cmd/arbitron/layout_worker_recovery_test.go`. No production change was necessary:
  this is acceptance coverage for already-implemented recovery, not a replacement worker
  or a second composition root. The test's `LayoutRender` name includes it in the existing
  browser-required `make test-render` selection.
- Behavioral characterization RED: temporarily removed `logError(w.recover(ctx))` from
  the real dispatch sweep. The initial live authoring result still passed, but the saved
  historical revision never reached a terminal SQLite row (74.56s). Restored the exact
  call and observed GREEN. The first exploratory run instead encountered Chrome's 30s
  launch timeout before reaching recovery; it was not counted as the behavioral RED.
  An unchanged rerun launched Chrome and passed; no browser assertion or timeout changed.
- Spec review FIRST: recovery-only scope; real configured attempt limit, exact manifest,
  retained positions, save reason/time, destination and generation reach immutable rows.
  Historical/deleted revisions finish without catalog content or resave. An unrecorded
  publication is quarantined/rolled back, never queued. Existing unit cases retain
  partial-source-error, duplicate replay, journal-remove failure and outage coverage.
- Anti-green-theater SECOND: no graph, source, renderer, reviewer or query fake in the
  new test. Assert real reviewed geometry/text coverage, evidence hashes on disk, Reader
  output and current-revision cache, plus no phantom rows or recreated folders. The
  recovery-call mutation fails at the durable consumer, not a constructor/call count.
- Quality THIRD: reused neighboring root/browser helpers, production polling and normal
  cleanup; bounded waits; gofmt and diff checks clean. No UI or unrelated user files
  changed. No active protocol bypass or knowingly failing test was committed.

Final verification after review:

```bash
go test ./domain/layout/... -run 'TestNewWorker|TestWorker|TestFakeRenderer|TestFakeReviewer' -count=1 -race
go vet ./domain/layout/... ./cmd/arbitron/...
go build ./...
ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^TestLayoutRender_WorkerRecoveryUsesCommittedSavedInputs$|^TestLayoutE2E_(LiveEnqueueOutageRecoversByPoll|CloseCheckpointsAndNewProcessReclaims|ActualCommandsJoinOnContextAndEOF|MCPAuthoringThroughRegisteredServer|PreparedInputIsQuarantinedNeverArranged|CorruptReceiptNeverArranges|PreSeedUndecidedSettlementAbortsBoot)$' -count=1 -v
```

Observed: worker/fake packages pass (5.762s / 2.068s); vet/build succeed. All eight
root tests pass under -race (55.159s, no skips); the new real-browser recovery test
passes in 29.64s. Logs: `/tmp/layout-worker-tests.log`,
`/tmp/layout-worker-recovery-red.log`, `/tmp/layout-worker-final-root.log`.
The unrelated full-module/profile and release sample/rename blockers remain assigned
to their existing tasks; this completion does not waive them.
