# Workflow Auto-Arrangement — Phase 2 Task 9: Real serve and MCP construction

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§2, 5.8, 6.1–6.4, 6.7, 6.10–6.12. Replaces the former missing-settler root, blind journal fixtures, duplicate recovery and query-before-producer examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Every authorized save and new sample in both command modes reaches the durable asynchronous pipeline, with explicit commit proof and lifecycle cleanup. Root tests inspect actual SQLite/file/snapshot sinks rather than constructor calls alone.

**Files:** create `cmd/arbitron/layout_wiring.go`, `layout_e2e_test.go`; modify main.go, sample_seed.go/call sites, flatstore_migrate.go/tests as needed. P3T7 later replaces the explicit failing renderer seam; P3T8 introduces QueryService and root query field together.

## Exact root API

```go
type layoutStack struct {
    store *adaptsqlite.LayoutRepo
    journal *filesystem.RevisionJournal
    commits *workflow.CommitSource
    cache *filesystem.LayoutCache
    requester *layout.Requester
    stamper *workflow.RevisionStamper
    reader *layout.Reader
    mutations workflow.MutationCoordinator
    worker *layout.Worker
    renderer layout.Renderer
    reviewer layout.Reviewer
    closeFns []func()
    cancel context.CancelFunc
}
func newLayoutStack(cfg *config.Config, db *sql.DB, repo *filesystem.WorkflowRepo,
    publications workflow.RevisionPublicationStore, registry workflow.Registry,
    logger *slog.Logger) (*layoutStack, error)
func newLayoutRenderReviewer(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error)
var layoutRenderReviewerFactory = newLayoutRenderReviewer
func (ls *layoutStack) start(logger *slog.Logger)
func (ls *layoutStack) stop()
```

`domainStack` gains `layout *layoutStack`. Do NOT declare `query *layout.QueryService` or attachQuery until P3T8 produces that type. `newLayoutStack` validates cfg/DB/repo and production proof collaborators; nil Publications/Registry are permitted only in an explicitly exercised true no-registry test mode. Do not silently convert an absent required publisher interface to nil. Real initDomain always passes publishedRepo and identityRepo.

Temporary P2T9 seam: `rendererUnavailable` implements Renderer and Reviewer; every Measure/Render/Review returns `errRendererNotInstalled = errors.New("layout renderer is not installed in this build; arrangement cannot be rendered or reviewed")`. It produces no measurements/ready-fonts/artifacts/evidence. Failed rendering is visible/retryable. No production fake or no-op reports pass. P3T7 replaces this body with the actual browser and deterministic DOM+raster reviewer; no release is complete before that replacement and P3T14 parity acceptance.

## Construction and lifecycle order

1. Load cfg; create logger and log EVERY cfg.LoadWarnings entry with logger.Warn. Open DB/run migrations. Construct filesystem repo; keep concrete `*filesystem.WorkflowRepo` locally (domainStack.workflowRepo may remain workflow.Repository).
2. Preserve flat-store migration ordering, excluding `.arbitron-locks`, `.arbitron-layout-journal`, `.arbitron-tree-commits` and staging/backup trees. Migration is non-authoring and never requests layout.
3. Construct ONE PublishedWorkflowRepo and move ONE WorkflowIdentityRepo construction BEFORE seeding. Use these same values for Service, Seeder and CommitSource; do not instantiate a hidden second identity registry or depend on later backfill to supply seed proof ID.
4. newLayoutStack builds store, journal, cache, RepositoryManifests, Requester(cfg.Layout.MaxAttempts), then `workflow.NewCommitSource(workflow.CommitSourceDeps{Journal: journal, Repo: repo, Mutations: repo, Publications: publications, Trees: repo, Registry: registry, Relocator: requester, Logger: logger})`. Retain commits and mutations; immediately repo.SetReadSettler(commits) so external graph reads settle durable mutation state or return explicit ErrMutationPending while awaiting two-endpoint relocation recovery.
5. Build Stamper with Journal, Requester, Displayed:store, Settler:commits. Reader uses same store/journal/cache. Build actual renderer factory seam, Coordinator and Worker with Store, Coordinator, Journal, Committed:commits, Manifests, Cache, Requester and logger; default finite deadlines from P2T8. Finally requester.SetNotifier(worker.Notify). Constructor failure closes every already-created resource in reverse order.
6. BEFORE seeding, backfill or serving reads, run a bounded CommitSource.CommittedRequests settlement pass to resolve old tree/relocation intents. This does not enqueue or Remove receipts; Worker.Start owns enqueue recovery. Undecided mutation error aborts boot before traffic/seeding with evidence intact, rather than expose a half-committed tree. Successful committed inputs stay discoverable until worker ack.
7. Call full P2T4 seedSampleWorkflows(..., workflowRepo, publishedRepo, ls.stamper, ls.mutations, identityRepo, owner, logger). Seed content/proof commits under locks. Routine malformed/optional seed errors retain the existing observable convenience behavior; undecided prior mutation recovery is a separate boot gate. Construct workflowSvc, SetRegistry(identityRepo), SetRevisionStamper(ls.stamper), SetMutationCoordinator(ls.mutations) BEFORE any authoring operation/backfill.
8. Build engine/schedule/rest of stack. Start worker ONCE via ls.start next to schedule evaluator; worker.Start owns initial Recover and each-poll Recover. ls.start uses an owned cancellable lifetime independent of a transient request; do not run a second uncoordinated Recover in start.
9. Both runServe and runMCP call initDomain, use its same services and defer Close. Close stops worker/dispatch/jobs/heartbeats, then renderer/reviewer closeFns reverse, THEN DB and logger. Stop/Close are nil-safe/idempotent, including error paths and partially built stacks. No collaborator use after DB close.

## Producer inventory and required sink probes

| Input/collaborator | Real producer injected here | Sink assertion that must fail if removed |
|---|---|---|
| attempt budget/warnings | P2T1 adapter/config.Load YAML/env/default+normalize | layout_revisions.attempt_limit equals nondefault configured value; exact clamp warning in root log |
| revision/retained input | P2T2 Stamp, P2T3 Service, P2T4 Seeder | saved Presentation, byte-identical journal/store Manifest, expected nodes/edges/ID and snapshot token |
| exact commit proof | P2T6 CommitPublication/RenamePublication and WorkflowIdentityRepo | killed new publish with old proof restores prior tree/no new row; killed committed publication yields one new row |
| locks/tree backup | P2T7 SAME WorkflowRepo as MutationCoordinator/TreeCommitter | blocked save/read/cache ordering, rollback all old bytes, ownership move cannot publish at old path |
| durable source | P2T7 CommitSource over full journal and proof stores | startup settlement before first read/seed; prepared operation quarantined, committed history recovered after overwrite/delete |
| requester/notifier | P2T5 NewRequester and worker.Notify | acknowledged save reaches SQLite; queue overflow still reaches result through sweep |
| worker/coordinator | P2T8 Worker + P1T9 Coordinator | actual terminal state, attempt count, stage, best/fallback/evidence; stop checkpoint then restart reclaims |
| Reader/cache | P2T5 Reader + P2T7 LayoutCache | same revision best/fallback across manifest/snapshot; cache deletion does not change authoritative geometry |
| renderer/reviewer | temporary explicit P2T9 failure, real P3T7 browser/DOM/raster | browserless explicit rendering failure with retained fallback; real visual pass accepted only by P3T7/P3T14 |

HTTP WorkflowHandler and MCP workflow tools already call domain Service; keep their authorization boundary. P2T9's MCP test builds actual server from ds.mcpDeps and sends an authoring tool request through existing registration; do not call the repo instead. P3T9 extracts real routerDeps and tests the actual HTTP router; P3T10 injects QueryService in mcpDeps. P2T9 must not invent their later query symbols.

## Stable test support consumed by Phase 3

P2T9 owns these helpers in cmd/arbitron/layout_e2e_test.go; P3T7/P3T9/P3T10/P3T14 extend/reuse them rather than duplicate roots:

```go
type layoutE2EOptions struct { maxAttempts string; seedSample string }
type layoutE2EEnv struct { root, workspaceDir, dbPath, logPath, configPath, samplesDir string }
func newLayoutE2EEnv(t *testing.T, opts layoutE2EOptions) *layoutE2EEnv
func (e *layoutE2EEnv) boot(t *testing.T) (*domainStack, func())
func (e *layoutE2EEnv) privatePath(name string) string
func (e *layoutE2EEnv) sharedPath(name string) string
func layoutE2EWorkflow(name, label string) *workflow.Workflow
func layoutE2EDiscardLogger() *slog.Logger
func layoutE2EOpenDB(t *testing.T) *sql.DB
func layoutE2EConfig(workspace string, maxAttempts int) *config.Config
func layoutE2EManifest(t *testing.T, path string) *workflow.Workflow
func layoutE2ESavedRevision(t *testing.T, path string) string
func layoutE2EWaitTerminal(t *testing.T, ds *domainStack, revision string) layout.Record
func layoutE2EAssertOutcome(t *testing.T, rec layout.Record)
func layoutE2ECountRows(t *testing.T, db *sql.DB, workflowName string) int
var layoutE2EPositions map[string]workflow.NodePosition
```

Workflow fixture is the three-node transform t1→t2→t3 chain with meaningful label, saved positions and no external LLM requirement. Environment writes isolated config/workspace/DB/log paths, clears ambient layout env overrides and uses real initDomain. boot returns idempotent cleanup, all DB queries occur before close. Wait helpers poll actual record with bounded deadline, not a fabricated success. Outcome requires captured nonempty graph, correct budget, retained positions, terminal state; if failed then stage rendering and explicit renderer reason. A passed row additionally has compatible geometry/complete rendered evidence. P3T7 adds `realSample string` to options and copies actual samples/pr-review, feature-pipeline, ralph-loop for required browser tests.

Tests overriding the global layoutRenderReviewerFactory register restoration with t.Cleanup and do not run in parallel. Production default remains the explicit real seam.

No journal fixture is assumed committed: either use actual authoring protocol, or MarkCommitted explicitly for a known-committed unit setup. Prepared fixtures assert quarantine/no row. Real subprocess recovery tests kill actual protocol at barriers; don't manually fabricate receipt and call that crash safety.

## Strict TDD cycles

Every cycle writes tests, observes behavioral RED against compiling construction, implements minimum, GREEN/refactor and checks a targeted mutation probe. Missing symbols alone do not prove wiring.

- [x] **9.1 Stack construction.** Reject each missing required collaborator, renderer-factory error cleanup, nil logger default, stop-before-start/double-stop. Actual stamper save through stack reaches SQLite with configured budget/captured graph; temporary renderer always produces failed/rendering with fallback. Root's proof source is real CommitSource, never FakeCommittedRevisions.
- [x] **9.2 initDomain authoring.** Create/update/private+admin shared/publish+overwrite/copy/duplicate/rename produce exact saved revisions/IDs and one row each. Transfer relocates no new token; backfill/startup/read do not stamp pre-feature manifests. Actual engine trigger while pending snapshots exact same token/fallback. Save returns while a gated renderer remains blocked. Configuration 3 and clamped 0/negative arrive at row sink and root warning log.
> Current plan correction (§6.18): private/shared Rename now have production active-operation context and real-root success tests. Re-run all listed authoring sinks, adding group editing/import entrypoints from P2T3; complete the crash matrix rather than skipping rename for the removed conflict test.

- [x] **9.3 Seeding.** Real SampleSource file set seeds stamped destination/proof/ID before worker, root reaches row. Restart produces zero extra saved revisions. Committed seed before ledger resumes same token; foreign orphan is not adopted. Removing stamper/mutations/registry setter must fail the sink assertions.
> Current plan correction (§6.18): ack-cleaned seed resumption now has the real Requester immutable-input reader, exact seed matching and publication proof. Verify complete fresh/restart/ledger-failure/race and missing-wiring sinks, including the post-Register crash/claim compensation case.

- [x] **9.4 Actual commands/MCP.** Execute real Cobra serve and stdio mcp modes, drive MCP authoring tool via real deps/server and verify disk manifest plus terminal SQLite row. Stop serve via context and MCP via EOF, assert command returns, worker/renderer joined and no writes after DB closes. Tests must not count constructing newRootCmd as running it.
> Current plan correction (§6.18): `domainStack.stdioContext` now provides the seeded principal only in local mode and denies credential-free org/edge authoring. Console logging still contaminates stdout, and runStdioSession currently filters it out. P2T9.8 below MUST repair that producer and validate the whole actual subprocess stream before 9.4 can be complete.

- [x] **9.5 Shutdown checkpoint.** Gated later review yields a completed best; Close cancels, checkpoints best in recoverable in_progress state and joins. New process after lease expiry reclaims at higher generation, retains prior best and reaches terminal; stale old worker cannot overwrite. Fake I/O asserts finalizer Deadline exists and <=30s, then returns promptly (no production-length sleep).
- [x] **9.6 Real kill/restart.** At Record/before visibility no row; after atomic private Create/Update manifest rename before receipt one row; Service.Rename requires its registry identity commit before recovery can accept its new target; publish overwrite old proof + new tree before CommitPublication restores every old manifest/supporting/cache byte and no new row; after proof/before receipt one committed new row; receipt/before enqueue and enqueue/before cleanup idempotent. Run missed-receipt then overwrite/delete. Relocation both trees selects actual registry, not folder presence. An already-running second process reading immediately after a peer crash (before recovery poll) receives committed/restored bytes or ErrMutationPending, never uncommitted target bytes. Removing SetReadSettler makes this sink test fail. Pre-seed settlement happens before FIRST graph read/seed/backfill; undecided I/O aborts boot with evidence preserved. All use actual FS+SQLite.
> Current plan correction (§6.18): rename no longer lacks an active-operation producer. Preserve existing private/shared proof tests, then finish target-visible/pre-identity, post-identity/pre-receipt, partial relocation and cleanup crash windows through actual processes. Compare exact old/new bytes and SQLite history; external and raw reads must settle before returning. This gate stays unchecked until all windows pass.

- [x] **9.7 Availability.** Live SQLite enqueue outage recovers by poll without restart; cache outage preserves Store result; prepared/corrupt proof never arranges; nil/copy ID does not suppress requests. Flat migration ignores all three layout dot-directories and staging trees. No automatic cross-revision supersede cancels authorized historical work.

```bash
go test ./cmd/arbitron -run 'TestLayoutE2E|TestSeedSampleWorkflows|TestMigrateFlatStore' -count=1 -race
go test ./domain/workflow/... ./domain/samples/... ./domain/layout/... ./adapter/filesystem/... ./adapter/sqlite/... ./adapter/config/...
go build ./...
go vet ./cmd/arbitron/...
```

## Pass verification and review (2026-10-17)

- Behavioral RED: a compiling stack with no start never reaches terminal SQLite; unwired initDomain saves have no revision/row; queue Stop without joining returns during I/O; actual root Rename rejects its own intent. Minimal root/lifecycle wiring makes the unblocked probes green.
- Spec review: retained the prescribed explicit renderer failure (zero fabricated metrics/evidence), real proof/identity stores, single pre-seed settlement (not enqueue recovery), Worker-owned recovery and no premature QueryService symbols. The four genuinely blocked steps above remain unchecked, not production-complete claims.
- Anti-green-theater review: exact manifest/ID/graph/budget/fallback/snapshot sinks; publication-time identity trigger prevents later backfill from hiding missing seed wiring. Removing seed registry, notifier, read settler or root worker shutdown fails the relevant persisted/file sink. All mutations restored. Test-only argument-bound rendering envelopes exercise cache/checkpoint lifecycle and are not browser/visual acceptance.
- Quality review: root cleanup now joins sweeps and queue capacity notifications before closing collaborators; renderer cleanup observes zero in-flight work and an open root DB. Close is idempotent/nil-safe and failed initialization uses the same cleanup path. Signal/stdio command goroutines are joined. Unrelated pre-existing docs/visuals work is not included.
- PASS: prescribed focused cmd/arbitron command with -race; entire cmd/arbitron package; workflow, samples, layout, filesystem, SQLite and queue packages; worker shutdown/checkpoint/finalizer bounded-context probes; go build ./...; go vet ./cmd/arbitron/...; gofmt; git diff --check.
- Existing unrelated regression: the prescribed aggregate command still fails adapter/config TestSampleFeaturePipeline_ValidatesWithDefaultProfiles because samples/feature-pipeline refers to unregistered mixed-model aliases (e.g. assessor-sonnet5-high, interviewer-codex, coder-pi-sol-high), already documented under P2T1. No configuration/profile code was changed by this pass.

Final implementation gate at this phase establishes durable pipeline and truthful visible failure seam. Production readability acceptance remains P3T7/P3T14: actual browser/font/DOM/raster pipeline, real HTTP wire response and both graph surfaces for pr-review/feature-pipeline/ralph-loop, required by make test-render/test-all and CI. A placeholder or fake pass cannot satisfy it.

## Task 9.8: Clean stdio transport and unfiltered production proof

**Files:** modify `adapter/observability/logging.go`, `logging_test.go`,
`cmd/arbitron/layout_e2e_test.go`; add `cmd/arbitron/layout_stdio_protocol_test.go`.
Consumes the existing `stdioContext`, UserRepo and local Auth mode, real Cobra mcp
command/ServeStdio, real logger construction and registered layout tools. Keeps
`SetupLogging(cfg config.LoggingConfig) (*slog.Logger, io.Closer, error)` unchanged.
Produces a console handler writing to `os.Stderr`, file JSON logging unchanged, and a
protocol test that reads EVERY stdout line. No stdout filtering or fabricated principal.

- [x] **RED:** launch the actual compiled binary in a subprocess with temp local config,
  nonempty authoring input, info logging and a missing-browser warning; initialize MCP,
  create/update/get/retry using actual registration, then EOF. Decode every stdout line
  as JSON-RPC (valid response/notification), assert expected IDs/results and nonempty
  saved/captured SQLite inputs with distinct revisions. Any text log on stdout fails.
  Capture stderr independently and assert expected boot/render warning there. The
  existing runStdioSession deliberately drops text and must stop doing so.
- [x] **GREEN:** route SetupLogging's console handler to stderr and remove the filter.
  Verify explicit local default-user identity reaches the service and real Store; keep
  org/edge unauthenticated with authorization errors/no authoring row. Missing default
  user and logger-file errors fail boot explicitly; EOF/cancellation join all goroutines.
- [x] **Edge/mutation:** multiline/Unicode manifests, repeated revision retry conflict,
  multiple response IDs and concurrent boot warnings cannot corrupt frames or create
  phantom saves. Restore stdout logging or remove stdioContext injection independently:
  the full-stream/provenance sink tests must fail. No successful fake renderer is needed
  to prove transport; the browser-required saved-result gate remains P3T7/P3T15.
- [x] Run `go test ./adapter/observability -count=1` and
  `go test ./cmd/arbitron -run 'TestLayoutStdioProtocol_|TestLayoutE2E_Stdio|TestStdioContext_' -count=1 -race`.
  Only then check 9.4 and commit. P3T10 consumes the same transport and does not redo it.

## Completion pass — real command transport and reconciled root gates (2026-10-18)

- Worked only P2T9, the first incomplete task in numeric phase/task order. Rechecked
  current producers under contracts §§6.18–6.19 rather than retaining historical
  blockers. No Phase 3 browser/readability or group-format completion is claimed.
- **9.2:** all private/shared create, update, publish/overwrite, copy, duplicate,
  rename, transfer, legacy/backfill, configured-budget/warning and pending Engine
  snapshot sinks pass under `-race`. Existing registered HTTP/MCP group upsert,
  delete and full-manifest import probes also pass. Removing the root Service
  stamper causes empty saved revisions at the actual file sinks; restoration passes.
- **9.3:** real root fresh/restart/foreign-orphan, exact ledger resumption, two-process
  seeding race, missing-wiring and killed protocol tests pass under `-race`. The
  crash matrix includes immediately after Register, partial tree, pre/post proof,
  receipt, enqueue and acknowledged input before ledger. Independently removing
  root stamper, mutations or registry prevents the expected committed sample from
  reaching the manifest/proof/SQLite sink. All three mutations were restored.
- **9.4/9.8 RED:** added an observability stream-sink test and
  `cmd/arbitron/layout_stdio_protocol_test.go`, which compiles the actual binary
  with `-race` and launches its real Cobra MCP command. The unchanged logger fails
  both tests: a text warning on stdout is not JSON-RPC. Changed only the console
  handler to stderr; file JSON logging and the public API are unchanged. Removed
  runStdioSession's stdout filter and joined its command/scanner cleanup.
- **9.8 GREEN/edges:** every stdout line, including output after EOF, is decoded as
  a JSON-RPC response/notification. Real initialization, multiline/Unicode create,
  update, get, repeated stale-revision conflicts and successful retry use distinct
  numeric/string IDs. Independent requested nodes/edges, saved metadata, captured
  SQLite manifests, local owner/ID, two distinct revisions, unchanged historical
  row and a newer retry generation are asserted. Boot/clamp/browser warnings reach
  independently captured stderr. Org/edge boot real auth adapters using local
  discovery metadata, supply no credential/principal, and deny authoring with no
  saved row. Missing default user and log-file errors exit explicitly. EOF joins
  the child and leaves logs unchanged; actual serve context shutdown and MCP EOF
  also retain their worker/renderer close-order probes. Removing stdioContext
  independently fails the successful authoring sink; it was restored.
- **9.6:** actual process kill/peer-read/restart tests pass under `-race`, including
  private visibility/receipt/enqueue, publish overwrite rollback of exact old
  manifest/support/cache/proof bytes, pre/post identity private/shared rename,
  partial journal/Store relocation and cleanup windows. Reserved raw reads and
  actual HTTP/MCP reads fail closed before recovery. Missed receipt survives later
  overwrite/delete; undecided pre-seed settlement aborts boot with evidence intact.
  Removing SetReadSettler exposes the wrong publication token at the first peer
  read and fails; restoration passes.
- **Self-review, in order:** (1) spec: production change is precisely stderr console
  routing, with no new flags/features or synthetic identity. MCP get intentionally
  returns its contracted status summary, not geometry/retained positions; the test
  checks full saved graph plus summary provenance instead. (2) anti-green-theater:
  compared independently requested Unicode graph inputs against disk, immutable
  Store and actual get response, then retry against the persisted generation; no
  renderer/identity/server fake and no stdout filtering. All wiring mutations above
  fail their sinks. (3) quality: reused root fixtures, real adapters and registered
  tools, inspected neighboring logging/MCP tests, bounded child lifetime and scanner
  buffers, joined readers/processes on failure, and ran gofmt/diff checks.
- The first aggregate verification exposed a stale P2T9 nil-source-copy assertion:
  Copy now correctly allocates an independent destination identity. Corrected that
  test to explicitly prove the source has nil ID, destination has its own ID, and
  exact requested graph/manifest/ID reach SQLite. Did not change production identity
  semantics or weaken the layout-request assertion.
- Final verification: `go test ./adapter/observability -count=1`; prescribed root
  `-race` gate extended with `TestLayoutStdioProtocol_`, `TestStdioContext_`,
  `TestCommitProtocol_RenamePeerCrash` and `TestServiceRevisionE2E_Group` (PASS,
  166.362s); workflow, samples, layout, filesystem, SQLite and config packages;
  additional real HTTP/MCP immediate peer-crash/read and repository boundary gates;
  `go build ./...`; `go vet ./cmd/arbitron/...`; gofmt; `git diff --check`.
