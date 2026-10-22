# Workflow Auto-Arrangement — Phase 2 Task 6: SQLite authority and publication proof

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), §§2, 6.3, 6.9–6.10. Replaces the former unfenced Claim/Get, result upsert and MarkCached examples.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Implement the complete layout.Store with transactional immutable results and exact generation/destination fencing, plus revision-bearing publication commits needed by save recovery.

**Files:** create `adapter/sqlite/layout_repo.go`, `layout_repo_test.go`, `layout_migration_test.go`, migration `019_layout_revisions.{up,down}.sql`; modify `published_workflow_repo.go` and tests. Recheck next free migration number: this checkout ends at 018. Do not modify hand-curated generated sqlc files or globally change timestamp conversion used by unrelated repositories.

## Exact interfaces

```go
type LayoutRepo struct { db *sql.DB }
func NewLayoutRepo(db *sql.DB) *LayoutRepo
var _ layout.Store = (*LayoutRepo)(nil)
var _ workflow.DisplayedLayoutReader = (*LayoutRepo)(nil)
func (r *LayoutRepo) DisplayedLayout(ctx context.Context, revision string) (workflow.RetainedLayout, bool, error)
// PublishedWorkflowRepo additions:
func (r *PublishedWorkflowRepo) CommitPublication(ctx context.Context, name string, publication workflow.Publication) error
func (r *PublishedWorkflowRepo) RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, publication workflow.Publication) error
var _ workflow.RevisionPublicationStore = (*PublishedWorkflowRepo)(nil)
```

Implement EVERY Store method declared in P2T5/contracts §2, including:

```go
func (r *LayoutRepo) Checkpoint(ctx context.Context, f layout.Fence, result layout.StoredResult, replaceBest bool, now time.Time) error
func (r *LayoutRepo) MarkCached(ctx context.Context, f layout.Fence, resultGeneration int64, dest layout.Destination) error
func (r *LayoutRepo) InvalidateResult(ctx context.Context, f layout.Fence, resultGeneration int64, reason string, now time.Time) error
```

Keep exact signatures of Request/Get/ListActive/Claimable/Claim/Progress/Complete/Supersede/Retry/Relocate/Result/PendingCache from the complete P2T5 Store declaration. InvalidateResult accepts StatePassed or StateSuperseded with VerdictPassed when its referenced pass is invalid, using identical job/result fences and transitioning needs_attention so Reader-promised Retry remains actionable. Shared `layouttest.RunStoreContract` is required against both adapters, not copied/reinterpreted.

## Schema and transactions

`layout_revisions` columns map every Record field: revision TEXT PK; workflow_id nullable; scope_kind CHECK private/shared; canonical owner_id; workflow_name; reason; input_manifest BLOB; retained JSON; state CHECK pending/in_progress/passed/needs_attention/failed/superseded; stage; job_generation positive default 1; attempts_used >=0 default 0; attempt_limit >=1; verdict; findings JSON default []; unreviewed boolean; failure_stage/reason; result_generation/cache_generation >=0; lease_owner; nullable lease_expires_at; requested_at/updated_at; nullable started_at/finished_at. Index destination(scope_kind,owner_id,workflow_name), and state/lease/request ordering.

`layout_results` has composite PK(revision,generation), FK revision→layout_revisions, geometry/review/preflight/score JSON, verdict, created_at. No FK to workflow_identity, filesystem catalog or instance lifetime: historical input/results survive source deletion. Nil Review must round-trip as nil. Results are immutable; never UPDATE an existing generation with different geometry/evidence. Complete and shutdown-only Checkpoint result INSERT/best-pointer updates succeed or rollback together. Checkpoint preserves in_progress/stage/lease, accepts identical replay and rejects conflicting result content; restart reclaim increments generation.

Use layout-specific INTEGER UnixNano values for timestamp/lease ordering and nullable integer timestamps; conversion preserves subsecond heartbeats and UTC instants. JSON public fields remain time.Time RFC3339 serialization. Global timeToString truncates seconds, so it is not a lease producer for layout. Tests inject short leases and exact boundary instants.

Claim is one conditional UPDATE ... RETURNING of the complete claimed row (or one transaction returning its own fenced snapshot before release). Expired in_progress reclaim increments job_generation; pending claim does not. Never UPDATE then an unfenced Get, which can return another owner's claim. Progress/Complete/Retry/InvalidateResult/MarkCached use the exact conditional fences in P2T5. Zero affected rows are classified into not-found, stale, not-claimable or retry-conflict sentinels; driver/decoding errors wrap layout.ErrStoreUnavailable. No no-op success on a stale cache settlement.

Relocate atomically rewrites destination, preserves immutable bytes/time/results and clears CacheGeneration when destination changes. Retry also clears CacheGeneration even if best remains from an older generation. MarkCached compares full scope/owner/name plus job/result fence; stale pre-move/post-retry acknowledgements cannot settle current cache work. Complete stores best's actual findings/evidence and preserves superseded lifecycle.

DisplayedLayout reads the referenced best result by record.result_generation, validates graph/contract compatibility against captured manifest, and returns node top-left positions with FromRevision and FromGeneration. Missing best is ok=false; unreadable/corrupt data is an explicit unavailable error, never fabricated positions. This reader is a fallback producer, not a claim that a new revision was reviewed.

## Exact publication proof producer

Migration adds nullable `published_workflows.saved_revision`; existing workflow_id comes from migration 017. Legacy NULL is readable but does not prove a new save. PublishedWorkflow gains saved revision/ID fields, and Get/PublicationOf read them explicitly; the older generated.GetPublishedWorkflow row lacks them, so do not silently map missing proof to a successful record.

CommitPublication requires nonempty revision and atomically upserts name, publisher, source, visibility, department, workflow_id and saved_revision. It must never commit token separately from workflow ID. Metadata-only existing Upsert/SetVisibility preserves saved_revision/workflow_id; no authoring token is invented during visibility changes/backfill.

RenamePublication runs one SQLite transaction: verify stable workflow_identity ID/name and matching publication source, update registry name, move publication name and atomically replace final publication fields/ID/revision. Name conflicts, proof failure or any SQL error rolls back BOTH stores. Invalid/missing evidence on passed or superseded-passed history supports fenced invalidation and retry; valid reviewed passed history remains a conflict. Private rename still uses Registry.Rename; shared rename uses only this atomic seam. SQL transaction helpers are local to this adapter, not a workflow-domain import of SQLite.

## Strict TDD cycles

- [x] **6.1 Migration RED→GREEN.** Empty DB migration creates tables/indexes/constraints, exact publication column and next version. Invalid state/scope/budget/generation and orphan/duplicate results fail. Down drops results before revisions and removes only this migration's publication column; existing publication values remain. Repeated migration is idempotent.
- [x] **6.2 Shared Store suite RED→GREEN.** Run P2T5 RunStoreContract without weakening any assertions. Test corrupt JSON, nil Review, defensive copies, unavailable/closed DB, UTC/subsecond round-trip and exact immutable input bytes.
- [x] **6.3 Real concurrency RED→GREEN.** Use two independently opened handles to the same file, not a single-connection in-memory fixture. Concurrent Claim returns each caller's OWN fence; expired old caller cannot write new generation. Complete/Checkpoint failure after result insert rolls back the result. Checkpoint leaves interrupted work claimable after lease expiry and cannot modify another generation. Retry/Relocate races stale MarkCached/InvalidateResult without corrupting current row. Cache settlement under changed destination/job fence errors.
- [x] **6.4 Displayed fallback RED→GREEN.** Best follows ReplaceBest only; worse retry retains old result and positions, relocated/deleted workflow still has history. Missing/corrupt referenced result exposes error; no cache file is needed.
- [x] **6.5 Publication proof RED→GREEN.** CommitPublication round-trips exact token/ID/metadata; old/NULL proof does not match new token; visibility-only Upsert preserves proof. Inject SQL failure and assert no partial proof. RenamePublication succeeds atomically or rolls back both registry/publication names and proof on conflict/failure; include private/shared and nil-ID local cases as permitted.

> VERIFIED on revisit: P2T7/P2T8/P2T9 and the real browser/root now exist. `TestLayoutPublicationE2E_ExactProofAndFailedReplacement` drives actual initDomain authoring through filesystem/SQLite/requester/worker: exact destination revision/identity/metadata and captured manifest, visibility-only proof preservation, SQL-triggered proof rejection restoring old manifest/supporting bytes with no phantom row, and successful replacement retaining stable identity. Existing shared before-proof/after-proof subprocess crash tests prove exact publication authority and recovery of committed input. The real-browser Reader parity probe proves saved input → reviewed SQLite result → workflow/instance query sinks with cache deleted and definition removed. Adapter RenamePublication atomicity is green; successful Service.Rename remains the separately recorded P2T3/P2T7 protocol blocker, not acceptance claimed by this adapter task.

```bash
go test ./adapter/sqlite -run 'TestLayout|TestPublished|TestRevisionPublication' -count=1 -race
go test ./adapter/sqlite/... -race
go vet ./adapter/sqlite/...
```

Observe behavioral RED after minimal compiling declarations; compile errors alone are insufficient. Mutate one conditional fence or omit proof fields and verify tests fail before restoring. The original documentation-only review performed no implementation/commit; execution evidence for this implementation pass follows below.

## Root and sink

P2T9 constructs one LayoutRepo over the migrated DB and injects it into Requester, Worker, Reader and Stamper.Displayed. It constructs one PublishedWorkflowRepo and WorkflowIdentityRepo BEFORE seeding and gives them to Service/Seeder/CommitSource. Actual initDomain tests inspect layout_revisions input/budget/terminal result, publication saved_revision/workflow_id and post-crash restored tree. P2T7 CommitSource uses PublicationOf exact proof; no registry row existence is substituted for it. P3T8/9/10 consume the same Store through domain query/retry, including actionable invalidated-pass retry.

## Execution evidence

- Selection: listed all task files in numeric phase/task order. P1T4–P1T9 and P2T1–P2T5 have only previously recorded dependency/production gates remaining; those dependencies are still absent. Selected P2T6's independent implementation rather than repeat those failures.
- 6.1 RED: `TestLayoutMigration` observed schema max version 18 rather than 19. Added migration 019 (the next free number); GREEN verifies both indexes, all requested constraints, orphan/duplicate result rejection, idempotent runner and down/up preserving preexisting publication fields.
- 6.2 RED: unchanged `layouttest.RunStoreContract` and decode/outage tests failed against compiling unavailable/empty Store stubs. Implemented every Store method with raw database/sql, immutable JSON/BLOB inputs/results, UTC UnixNano timestamps, exact conditional fences and atomic result/best-pointer updates. Shared suite remains unchanged. Nil review, nested defensive copies, malformed JSON/UUIDs, exact captured bytes, closed DB and cancellation are covered.
- 6.3 uses two independently opened WAL handles to one temporary file. Competing claims/reclaims assert each winner's own owner/generation/lease; stale writes cannot change the peer-visible row. SQL triggers abort after result insertion; both Complete and Checkpoint leave zero inserted results and an unchanged row. Checkpoint replay/reclaim, retry-versus-cache settlement, relocation-versus-cache settlement and late invalidation are covered. These add adapter-specific characterization of behavior already required by the shared suite: removing the Progress generation predicate caused RED, as did temporarily committing the failed result transaction. Restored both guards, then GREEN.
- 6.4 RED: compiling empty DisplayedLayout failed best-position and corrupt-data assertions. GREEN follows only result_generation, retains prior best through worse retry/relocation with no workflow/catalog/cache, returns independent positions and explicitly rejects missing/corrupt/incompatible referenced results or captured manifests.
- 6.5 RED: compiling no-op CommitPublication/RenamePublication failed round-trip, rejection and registry/publication sink assertions. GREEN writes proof/identity/metadata together; source-checked shared rename moves registry and publication in one transaction and rolls both back on conflict/SQL failure. Reads explicitly select both proof columns; curated generated files and global timestamp helpers are untouched. Production completion remains blocked above.
- Self-review, in order: (1) checked this exact schema, port signatures, lifecycle/fence rules, ownership and root dependencies; added no flags, fallback authority or unrelated features. (2) strengthened stale-generation tests to assert the unchanged persisted row, and added the real Requester→SQLite raw input/budget/ID→Reader geometry/findings/evidence sink (Unicode graph, nonempty distinct preflight/review findings and later-source replay). Temporarily discarded stored Review and omitted PublicationOf.Revision: both sink tests failed; all mutations were restored. Synthetic review metadata is explicitly unit evidence, not browser acceptance. (3) reused neighboring UUID/nullable department/time helpers, kept layout timestamp conversion local, removed fatal assertions from goroutines, made immutable JSON column ordering explicit and formatted long SQL for reviewability.
- Final GREEN: the prescribed focused race command, `go test ./adapter/sqlite/... -race`, `go test ./domain/layout/...`, `go vet ./adapter/sqlite/...`, `go test ./cmd/arbitron -run '^$'` (composition package compiles only), gofmt and `git diff --check`. Full SQLite race runs require approximately 121–136 seconds here (the initial 120-second harness timeout was rerun with 300 seconds and passed).

## Revisit — production completion and ordered self-review

- Selected this task as the first now-unblocked task after the recorded P2T1–P2T4 blockers; P2T5 is already complete. No other task was implemented.
- Added the production publication regression described above. Existing behavior was initially green; followed this task's explicit omission probe rather than inventing a missing implementation: temporarily omitted `PublicationOf.Revision`, observed behavioral RED (persisted destination revision versus empty proof), restored the existing mapping and observed GREEN. No production mutation remains.
- **Spec compliance FIRST:** checked all 6.5 adapter cases and the Root and sink requirements. Kept changes to publication-proof acceptance; added no protocol exceptions, configuration, flags or rename claims. Existing adapter tests cover atomic shared RenamePublication and its rollback/rejection matrix.
- **Anti-green-theater SECOND:** the new test uses the actual initDomain constructors and real authoring producer, checks raw SQL proof plus the exact worker-captured destination manifest, and rejects phantom rows after recovery. Omitting the load-bearing proof field fails its sink assertion. Replaced a redundant manifest re-read with comparison against the persisted row's Manifest. Actual subprocess crashes cover old-proof/new-tree authority; actual Chrome covers nonempty reviewed geometry/evidence and authoritative read sinks.
- **Code quality THIRD:** followed neighboring root test helpers and SQL-trigger failure injection, kept filesystem byte assertions explicit, used scoped temporary workspace/cleanup and gofmt; unrelated pre-existing `docs/visuals/` remains untouched and excluded.
- After review, GREEN: focused SQLite race suite (30.848s), full SQLite race suite (182.564s), SQLite vet, root publication/authoring/shared-crash race tests (5.891s), domain/layout packages, whole-module build, required-browser Reader parity (7.198s, no skip), and whitespace checks. This closes the obsolete missing-root gate, not other tasks' recorded protocol/release blockers.
