# Workflow Auto-Arrangement — Design Phase 2: Persistence & Async Pipeline

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`
Depends on: Phase 1 geometry, attempt coordinator and rendered-review port. Production
completion also depends on Phase 3's shared renderer and the RENDER-REVIEW adapter.

## Goal

Save immediately, then arrange and visually review every committed authoring revision.
Preserve the saved workflow and best available presentation on every failure. Publish only
to the reviewed revision, and give workflow and instance reads one authoritative result.
Do not arrange untouched workflows merely because the service starts or backfills IDs.

## Shared contracts

- `savedRevision`: server-issued unique token for each committed authoring save, including
  identical-content and position-only saves. A content hash cannot distinguish an A → B → A
  save sequence or two identical saves and is not the stale-write guard.
- `jobGeneration`: increases for retry of the same revision; fences cancelled or late jobs.
- SQLite is authoritative for revision results/status. `layout.json` is a versioned cache
  of published geometry, not an independent read authority. Both views use the same
  revision record and result generation.
- New revision metadata travels in the saved manifest and therefore its engine snapshot.
  It includes an immutable retained-presentation reference/value sufficient to recover
  the existing layout independently of the current `layout.json`. Plans must implement
  that durability, not assume a previous file will remain available forever.
- Geometry is presentation only. Arrangement publication never calls `Service.Update`,
  stamps a new authoring revision or recursively enqueues itself.

## Components

### 1. Saved revision capture and complete save-path coverage

Add presentation metadata to `domain/workflow/workflow.go:Workflow` and stamp it in hidden staging before destination visibility, then record a durable commit receipt
at the successful authoring commit boundary. Ignore client-supplied revision tokens. Capture the
exact saved manifest, resolved destination `Scope`/name and retained presentation for the
request. The worker must not replace this input with whatever a later reload returns.
Use a unique revision as the history key; retain workflow ID and scoped locator as
metadata. `Workflow.ID` may be nil in local mode or legacy manifests, and copied manifests
may carry the source ID. Neither case may suppress arrangement or mix two destinations.

The save-side `ArrangementRequester` is a consumer-owned port in `domain/workflow`.
Enqueue only after an exact successful write/operation commit and durable receipt, without
waiting for arrangement or review. Its production wiring is mandatory; a no-op is only for isolated tests.

| Existing write path | Required arrangement behavior |
|---|---|
| `domain/workflow/service.go:Service.Create`, `Service.Update` | One request for the successfully saved private or admin-edited shared destination; local mode included |
| `domain/workflow/service.go:Service.Publish` | Initial publication and overwrite/re-publication both create and arrange a new shared destination revision, after the publication commit |
| `domain/workflow/service.go:Service.Copy` | Fresh destination revision; copied `layout.json` can seed retained presentation, never a passed verdict for the new revision |
| `domain/workflow/admin_ops.go:Service.Duplicate` | Same fresh-destination rule; enqueue once after copy/manifest relabel/identity commit |
| `domain/workflow/rename.go:Service.Rename` | New saved revision after successful relabel/identity commit; no request for rolled-back copies |
| `domain/samples/seeder.go:Seeder.seedOne`, `Seeder.writeTree` | Newly created sample manifests arrange after successful seeding; resume an interrupted new save idempotently, without rearranging already-seeded workflows |
| `domain/workflow/backfill.go:Service.BackfillRegistry`, `Service.backfillScope` | Identity reconciliation only: preserve revision/presentation metadata and do **not** request arrangement or stamp an authoring revision |
| `domain/workflow/admin_ops.go:Service.TransferOwnership` | A folder/owner move alone preserves revision and relocates revision history/pending jobs through a durable stable-ID intent; no new arrangement unless it commits an authoring manifest change |
| `domain/workflow/delete.go:Service.Delete`, `Service.Unpublish` | Settle prior prepared writes before deleting publication/folder/identity; preserve committed receipts and layout history; no new authoring revision |

Raw `WriteFile`/`DeleteFile` cannot write `workflow.json`; supporting-file changes do not
alter the graph. Repository internal writes for ID reconciliation and copy/move staging
must be distinguishable from committed authoring saves so instrumentation does not queue
intermediate, rolled-back or startup-only states. Filesystem migration and reads likewise
must not trigger arrangement. No startup bulk arrangement of pre-feature workflows.

### 2. Durable request and retained-presentation capture

Persist enough request metadata with the manifest save to discover a committed revision
whose asynchronous enqueue failed or whose process crashed. Save success depends on the
manifest commit, not on the queue, SQLite or review availability. A read can synthesize
`pending` from that saved request metadata until the worker records it. Keep a two-phase filesystem journal: prepared request before visibility, exact publication proof
for registry-backed shared saves, then durable receipt before enqueue. A prepare alone never
proves save success. Preserve a full prior-tree backup and durable intent before shared
replacement, restoring all prior bytes when publication fails, including overwrite. Publication
proof stores the exact saved revision and workflow ID atomically; an old publication row
cannot prove a new re-publish. Shared rename commits registry and publication proof together
in one SQLite transaction. Persist a lightweight failure marker where possible; an unavailable status store must surface as
an availability error with Retry, never “no arrangement” or a false success.

At save capture the existing displayed layout as the new revision's fallback. Reuse
complete prior geometry only if node/edge occurrences, text and renderer contract are
compatible. Otherwise retain surviving nodes' existing coordinates and render the new
manifest's actual connections with the fallback renderer. Never display stale edges or
omit newly saved nodes. If no artifact exists, retain the manifest's `Node.Position`
values; the legacy renderer handles incomplete coordinates. This fallback is explicitly
unreviewed for the new revision. Both views receive the same revision-scoped fallback,
including an instance opened before arrangement starts or after it fails.

P2T2 produces the revision/fallback/request envelope; P2T3/P2T4 own every authoring caller;
P2T6 produces exact revision-bearing publication proof; P2T7 produces durable receipts,
tree backups and CommitSource settlement; P2T9 constructs them before seeding. These
are implementation tasks, not current capabilities. No layout outage may
roll back or lose a successfully saved workflow.

### 3. `layout.Store` and SQLite adapter

New durable rows keyed by `savedRevision`, with scoped destination/optional workflow ID,
immutable input/fallback reference, current `jobGeneration`, attempts/limit, stage,
timestamps, best geometry, review evidence, findings, failure reason and result generation.
States: `pending`, `in_progress`, `passed`, `needs_attention`, `failed`, `superseded`.
Use stage to distinguish arranging, rendering and reviewing, and retain the best
candidate's review verdict separately from job state. Failed/superseded rows may
retain usable best geometry and evidence; state alone does not mean geometry is absent.

Operations include idempotent request, generation-fenced claim/progress/result writes,
read by revision and abandoned-job recovery. A retry preserves existing best geometry
until a better result is available. History survives workflow deletion/rename so saved
instances can still display their revision. A superseded job's retained best result remains
readable; cancellation does not erase completed evidence or fallback.

Follow `adapter/sqlite` migration conventions; use hand-written database/sql for layout
and publication proof rather than regenerating the hand-curated sqlc tree. `019` is next
in this checkout; recheck before implementation. Layout-specific INTEGER UnixNano timestamps
preserve lease precision without changing other repositories. Claim returns its own fence
atomically; result insertion and completion/checkpoint are transactional. Retry and relocation
invalidate cache settlement; MarkCached fences job/result generation and full destination.
Shutdown checkpoints completed best geometry while leaving interrupted work recoverable by
lease expiry. StatePassed with missing/incompatible geometry or invalid rendered evidence
is downgraded by Reader; retry conditionally invalidates it before creating a generation.

### 4. Publication and `layout.json` cache

Reject raw writes/deletes of `layout.json` alongside `workflow.json` in
`WorkflowRepo.WriteFile`/`DeleteFile`, and exclude layout.json from `ListFiles`. Use a versioned payload containing saved revision, job generation, result generation,
renderer contract and geometry. The record in SQLite is authoritative; cache failure does
not prevent both views from reading that same result.

Publication is fenced by `(savedRevision, jobGeneration, resultGeneration, destination)` and compares the destination's
current saved token under the **same write coordination as all manifest mutations**.
Atomic tmp+rename alone is not compare-and-swap: `WorkflowRepo` currently has no lock.
Introduce workflow-owned MutationCoordinator: whole-operation sorted source/destination
locks across processes, with coordinator-specific owned contexts for nested repo calls.
WorkflowRepo.Load shares that lock and an explicitly injected CommitSource read settler,
so reads/triggers cannot observe partial tree/proof commits or overwrite rename gaps.
A peer-process crash is settled before external read; unresolved relocation returns
ErrMutationPending until recovery resolves both endpoints using stable registry authority. Raw identity reconciliation shares the same coordination.
The protocol must also coordinate independent `serve`/stdio MCP processes sharing the
workspace, deletion/recreation, rename, copy and owner moves. Never overwrite a newer
revision or a newer retry generation. Old results can be stored against their own revision
for historical instances, but cannot become the current layout. Stale cache files are
ignored as current results. All readers validate revision, generation, geometry coverage
and format/renderer version before use.

### 5. Worker and recovery

`layout.Worker` is new domain orchestration over ports. Start/Stop follows
`domain/schedule/evaluator.go:Evaluator.Start` / `Stop`; use bounded concurrency, finite
stage deadlines and cancellation. One dispatcher defaults to two jobs; renew leases every
lease/3 even during a blocked renderer/reviewer. Default total job timeout is 15 minutes,
each measure/render/review stage two minutes, and persistence/recovery/finalizer I/O 30 seconds.
Stop joins dispatcher/jobs/heartbeats before renderer/SQLite close. Coalesce notifications for the same saved revision;
a retry is a new generation, not a duplicate. Do not automatically supersede other revisions on save/recovery: each committed immutable
revision remains eligible, including authorized historical retries. Explicit cancellation
still preserves best geometry/history; publication CAS prevents older work replacing current. Queue saturation
must leave durable discoverable work, not silently drop it.

Per job: claim revision/generation → arrange/render/review with the configured total
attempt budget → persist best geometry, evidence and terminal state → conditionally
refresh cache. Renderer/reviewer failure means `failed`, never `passed` based only on
geometry. Budget/policy exhaustion means `needs_attention`, with best layout and findings.
If no candidate exists, keep the captured fallback. Every failed stage remains retryable.
A retry may target the current workflow revision or an authorised instance's historical
revision, always using its captured input. Historical results update only that revision's
record; cache publication still requires that exact revision to be current. Superseded
unfinished review retains its attention reason and can be retried through the instance
surface rather than silently being treated as complete.

On restart recover only already-requested post-save revisions, including old revisions
needed by instances; mark intentionally cancelled work superseded while retaining its
fallback/best result. Recover only CommitSource-proven requests, at startup and every poll, with acknowledged
journal cleanup. A partial recovery error does not hide other committed inputs. Never replay
Journal.List as proof. Reconcile saved request metadata missing a row. Do not discover work
by arranging every workflow in the catalog. Resolve relocated destinations safely; a
deleted workflow cannot be recreated by a late layout write.

### 6. Configuration and composition root

Add `LayoutConfig.MaxAttempts` under `domain/config/config.go:Config`; default `5` in
`adapter/config/loader.go:defaults` and `defaultYAMLContent`, configurable as
`layout.maxattempts` in YAML (and `ARBITRON_LAYOUT_MAXATTEMPTS` via the existing loader).
Values below one clamp to one with a visible warning. Persist the effective budget per
job. Manual retry uses the current configured budget.

Wire repository, worker, renderer/reviewer adapter, requester and query seams in
`cmd/arbitron/main.go:initDomain` / `domainStack`. Both `serve` and stdio `mcp` must run the
pipeline, recover saved requests and stop it before closing SQLite. Construct the request
capture path, exact publication proof store and workflow identity repository before seeding.
Inject the same repo as mutation coordinator/tree backup producer and the same CommitSource
as stamper settler/worker committed source. Before seeds/backfill/traffic run bounded durable
mutation settlement; fail boot with evidence intact if unresolved tree/relocation state would
expose uncommitted content. Worker.Start later owns initial enqueue recovery and recurring sweeps.
Seeder receives stamper/coordinator/registry explicitly; ID backfill remains non-triggering. Production must not
silently install a no-op requester or reviewer.

## Data flow and Phase 3 seam

1. Commit authoring manifest with new saved revision, retained fallback and durable request
   metadata; return save success without waiting for arrangement/review.
2. Enqueue captured revision. Worker claims a generation, reports stage/attempt progress,
   and calls Phase 1 with the real RENDER-REVIEW port implementation.
3. Persist terminal result, review evidence and best/fallback geometry per revision.
4. Fenced current-revision publication refreshes `layout.json`; a cache miss/failure leaves
   the authoritative record available to both views.
5. Phase 3 reads current saved revision or the instance snapshot's saved revision through
   the same query service. Never substitute the workflow's latest revision for a snapshot.

## Data Provenance

| Leaf input | Existing producer or explicit unresolved dependency |
|---|---|
| Saved nodes/edges/positions, optional workflow ID | `domain/workflow/workflow.go:Workflow`, `Node.Position`, `Edge`; `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` |
| Authoring save/copy/publication destinations | `domain/workflow/service.go:Service.Create`, `Update`, `Publish`, `Copy`; `domain/workflow/admin_ops.go:Service.Duplicate`; `domain/workflow/rename.go:Service.Rename`; arguments plus `domain/workflow/scope.go:Scope`, `Private`, `Shared` |
| Newly seeded manifests and supporting files | `domain/samples/ports.go:SampleSource.Read`; `domain/samples/seeder.go:Seeder.seedOne`, `Seeder.writeTree`; `Ledger.Seeded` distinguishes already-seeded samples |
| Existing persisted geometry | **PLANNED DEPENDENCY LAYOUT-CORE (P1T1–P1T9):** Phase 1 must produce versioned geometry/results; no `domain/layout` package exists today. Existing fallback positions alone come from `Node.Position`. |
| Exact saved token, captured manifest/fallback, durable request and snapshot propagation | **PLANNED DEPENDENCY SAVE-REVISION (P2T2–P2T4/P2T7, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 must add and persist presentation metadata and its save/recovery protocol, including local mode and every path above. Existing `domain/engine/engine.go:Engine.TriggerWithDepartment` snapshots `Workflow` via JSON, but currently supplies no saved-revision token. |
| Status/history, generations, progress, timestamps and retained geometry | **PLANNED DEPENDENCY LAYOUT-STORE (P2T5/P2T6/P2T9):** Phase 2 must implement `layout.Store` and SQLite adapter, schema, clock and generation-fenced operations; existing SQLite DB is not an arrangement producer. |
| Asynchronous requests, context, coalescing and stage deadlines | **PLANNED DEPENDENCY ASYNC-PIPELINE (P2T8 Tasks 8.1–8.7/P2T9, commit protocol per SAVE-COMMIT-PROOF resolution / P2T7 Task 7.5):** Phase 2 must build/wire `ArrangementRequester`, `layout.Worker` and recovery. `domain/schedule/evaluator.go:Evaluator` supplies a lifecycle pattern only. |
| Atomic stale-write protection and cache write/read | **PLANNED DEPENDENCY PUBLICATION-CAS (P2T7/P2T8/P3T8):** `adapter/filesystem/workflow_repo.go:WorkflowRepo` has atomic renames, but no shared lock/CAS or layout artifact operations. Phase 2 must implement cross-process coordination and revision/generation checks. |
| Actual rendered review and renderer measurements | **PLANNED DEPENDENCY RENDER-REVIEW (P3T2–P3T7):** Phase 1 port + Phase 3 shared renderer + new production adapter/runtime; no existing provider is asserted. |
| Configured attempt limit | **PLANNED DEPENDENCY LAYOUT-CONFIG (P2T1/P2T9):** `domain/config/config.go:Config`, `adapter/config/loader.go:Load`, `defaults`, `defaultYAMLContent` exist; layout fields/defaults/normalisation are new Phase 2 work. |
| DB handle and lifecycle host | `cmd/arbitron/main.go:domainStack.db`, `initDomain`, `domainStack.Close` (worker fields/wiring are new ASYNC-PIPELINE work) |

The implementation contracts §§2 and 6 select the complete port signatures, schemas,
worker defaults, publication proof, tree rollback, cross-process lock and recovery protocol.
P2 task plans assign every producer and root injection; these remain planned implementation
work. Durability, all-save coverage, real visual review and stale protection are mandatory.

## Error handling

Enqueue/status/storage/render/review/cache failures never invalidate the committed save.
Expose failing stage/reason and Retry. If SQLite cannot record failure, saved metadata and
an explicit query availability error keep the failure visible and recoverable. Keep the
best new geometry, or retained presentation when none exists. Cache-write failure after a
stored result uses that result through the query seam and schedules cache reconciliation;
it must not leave the workflow view on a different layout from the instance view.

## Testing strategy (STRICT TDD — required)

Write each failing test first, observe the expected failure, implement, then refactor.
Use fake ports for domain tests and real SQLite/filesystem adapters for durability/races.

- **Happy path:** Update returns before blocked review completes; pending/stages become
  passed, and the recorded result is available through the shared revision read.
- **Success:** all authoring paths in the table (including initial/re-publish, local-mode
  save and seeding) capture and request the committed destination revision once; both
  command modes start/stop the worker; YAML/default budget reaches the attempt loop.
- **Failure:** exhausted review persists best layout/findings as needs_attention; review
  failure persists failed plus best/fallback; enqueue failure still returns save success
  and is visible/recoverable; retry retains the prior best until replacement is ready.
- **Error:** full queue, unavailable SQLite, missing manifest, renderer timeout, corrupt
  cache and cache-write failure preserve saved content and expose Retry; process crash
  between save/enqueue/result/cache stages recovers without inventing a passed result.
- **Edge cases:** A → B → A and identical-content saves use distinct revisions; late old
  revision and late retry generations cannot publish; cross-process save/CAS interleaving;
  rename/delete/recreate/owner move; nil workflow ID; copied source IDs; superseded history
  remains readable and retryable without publishing to the current workflow; configured
  zero/negative budget; reserved artifact APIs reject writes.
- **Non-goal regression:** startup/backfill/read/file migration does not arrange pre-feature
  workflows, and publication does not recursively save. Instance snapshot carries exact
  revision/fallback even when triggered while review is pending. Cache loss cannot change
  same-revision geometry between workflow and instance query responses.

## Implementation alignment and commit provenance

The implementation contracts §§2 and 6 and P2T1–P2T9 plans are the exact implementation
reference. The human-selected SAVE-COMMIT-PROOF resolution (2026-10-17) is a two-phase
journal with durable receipts inside the destination mutation lock; registry-backed shared
authoring operations commit at their exact revision-bearing publication record.

Prepare → stamped staged tree/manifest → publication proof where required → receipt →
request → acknowledged journal cleanup. Settle runs before overwrite/delete. A receipt
proves historical committed input even after deletion or a later save. Without receipt,
roll forward only when the destination and publication prove the same token/ID; otherwise
restore prior tree and quarantine the failed prepare. Read/registry/rollback errors remain
undecided, preserve evidence and block unsafe mutation. Mere publication existence never
proves re-publication. Retain tree backup until receipt is durable.

P2T2 owns the complete workflow ports/types. P2T3 covers service commits/stamped staging;
P2T4 covers seed commit/resume; P2T5 owns Store/Reader/Requester and executable contract;
P2T6 owns SQLite exact proof, atomic shared RenamePublication, Store and Checkpoint; P2T7
owns filesystem journal/locks/tree rollback and domain CommitSource; P2T8 owns bounded
worker/heartbeat/repeated recovery/publication; P2T9 constructs all real collaborators in
both command modes before seeding and proves actual SQLite/file/snapshot sinks.

Relocation uses stable registry identity from durable intent, with both endpoints locked.
If registry points to destination, roll forward even when source cleanup left BOTH folders.
If registry points to source, roll back target; missing/third destination/read error keeps
intent undecided. Shared rename uses one SQLite identity+publication transaction. Immutable
snapshots/history retain their original saved metadata; Store destination tracks committed
move. Cache lookup outage uses captured fallback with explicit availability error.

The selected production rendered review remains P3T2–P3T7: bundled Inter, actual shared
browser renderer, measured DOM and deterministic raster checks covering readable tiles.
P2T9's temporary renderer fails explicitly until P3T7 supplies it. Production parity for both
views and real sample graphs is P3T14; fake evidence or PNG presence alone cannot pass.

## Current-source and parallel-group alignment (2026-10-18)

Implementation contracts §6.18 gives the concrete current-source protocols: the
operation-private active-rename context under BOTH endpoint locks; exact SQLite-acknowledged
seed input for ledger-failure resume; journal-before-identity seed capture and exact abandoned
claim compensation; complete private/shared retry lock sets; and the root-injected settled
raw FileReader. External reads still settle live peer crashes; registry authority still
selects rename/transfer recovery. Initial producer-absence statements describe the design
baseline. Historical execution notes do not authorize skipping newly executable gates.

Local stdio uses the existing default-user principal; credential-free org/edge authoring
remains denied. P2T9.8 supplies stderr console logging and an unfiltered stdout protocol test,
with actual subprocess and SQLite-sink proof; preserve those implemented guards. Sample profile tests
use bundled PROFILES.md rather than assuming every sample alias is a default profile.

P3T16/P3T17's mandatory v2 group geometry/report additions use the existing JSON result/cache
storage and captured immutable input. No separate group result store or new revision key
is introduced. Store/Reader/cache round trips and retries must preserve all group fields,
full digest and generation fences. Old v1 geometry becomes explicit compatibility fallback,
never a passed group layout or automatic startup arrangement. P3T20 repeats the actual
root→review→persisted result→authorized views chain with nonempty groups and missing-input
controls. Runtime child tiles obtain their own saved snapshot revision results through the
existing authorized instance query; they do not overwrite the root's revision geometry.

## Split-task persistence and root closure (2026-10-19)

P3T15 input declarations are already produced; P3T16 adds complete v2 geometry/validation,
P3T17 actual report transport, P3T18 reviewer and JSON round-trip/retained-best truth,
P3T19 route polling and P3T20 actual-root release proof. Existing initDomain/worker/query
constructors supply the production path; no new group queue, store or revision lookup
is needed. Intermediate capture failure stays explicit and retryable until its successor
producer lands; it is never accepted as successful group review. Every missing-map,
missing-paint, nil-live and missing-child-layout mutation must fail its final consumer.
