# Gaps Identified — Workflow Spawn Node

Concrete findings from the seeding sweep of
`64768283d2c32b64023801103c7aab2f5ea538d3..HEAD`. Each was found by grepping the
whole repo for the symbol and checking for a **production** (non-`*_test.go`,
non-`*.test.ts`) constructor or caller.

Tick a box only when the gap is closed in code with a test that would fail if the
production input were nil/empty. An unfed production path is a gap to **fix or
escalate** — never a tolerated no-op.

---

## Open

- [ ] **`useRunHierarchy.invalidate` has no production caller.**
  `dashboard/src/hooks/useRunHierarchy.ts:78` exports `invalidate` and documents
  it as "used on a full refetch", but the only reference outside the hook is
  `dashboard/src/hooks/useRunHierarchy.test.ts:161`.
  `dashboard/src/components/QueueSection.tsx` calls `useRunHierarchy()` and never
  calls `invalidate`. Consequence: `QueueSection`'s refetch replaces the top-level
  `instances` but every expanded level stays cached forever, so a child that
  completes, fails, or is cancelled keeps rendering its stale state until the user
  collapses and re-expands it — and a newly spawned child never appears under an
  already-open parent. Fix: invalidate (or re-fetch open levels) when the
  section's row set is refreshed, with a Vitest case asserting a stale cached
  child is not re-rendered after a refetch.

- [ ] **Verify `ChildCount` / `parentInstanceId` field names on the MCP surface.**
  `adapter/mcp/tools_instance.go:139` returns `MarshalResult(enriched)` over
  `[]instance.EnrichedInstanceSummary` directly. The embedded `InstanceSummary`
  carries JSON tags (`parentInstanceId`), but `EnrichedInstanceSummary.ChildCount`
  (`domain/instance/instance.go:213`) has **no** JSON tag, so it marshals as
  `ChildCount` while REST emits `childCount`
  (`adapter/http/instance_handler.go:258`). Confirm whether this divergence is
  pre-existing for the sibling fields (`RetryCount`, `QueuePosition`) and
  therefore consistent, or whether the REST/MCP parity contract
  (`adapter/mcp/parity_test.go`) is now weaker than it reads. Record the decision;
  do not leave it unexamined.

- [ ] **Confirm the awaited-spawn wait is invisible to the timer recovery sweep.**
  `executeWait` transitions the parent with `instances.UpdateState(ctx, id,
  StateWaiting)` and the comment claims `waiting_until` stays NULL so
  `RecoverTimers` cannot pick it up (`domain/engine/executor_spawn.go:159-163`).
  Verify against the actual `UpdateState` implementation and the
  `ListWaitingWithTimer` predicate in `adapter/sqlite/instance_repo.go` that a
  spawn-waiting parent is genuinely excluded — an untested claim in a comment is
  not a guarantee, and a false positive here would fail a parent that is waiting
  correctly.

- [ ] **`Engine.OnChildTerminal` is called with `context.Background()` from three
  sites.** `engine.go` (run completion, `failInstance`, `cancelAuthorized`) all
  pass a background context deliberately, but `OnChildTerminal` then performs
  `instances.Get` ×2 and `queueRepo.SetQueued` with no deadline and no request
  identity. Confirm none of those repository calls depends on a context value
  (identity, tenant, tracing) that the background context drops, and that an
  unbounded call here cannot wedge the terminal transition that invoked it.

- [ ] **No end-to-end coverage that a fire-and-forget child from node A does not
  wake a parent waiting at node B.** Phase 4's design calls this "the central
  mis-wake guard" and the single most important assertion in the phase.
  `domain/engine/spawn_wake_test.go` exists — confirm it covers the two-node case
  against the real `OnChildTerminal` (not a fake that already filters by node id),
  and that the fake `InstanceRepository` it uses does not ignore `ParentNodeID`.

- [ ] **Verify the cascade produces an intervention record for every descendant.**
  `cascadeCancel` routes through `cancelAuthorized`, which calls
  `recordIntervention` — but `authorizeIntervention` runs per child against the
  ambient context identity. Confirm with a test that cancelling a deep tree
  writes one intervention row per cancelled run, and that an admin cancelling
  another user's run does not fail authorization partway down the tree and
  silently orphan the remaining descendants (the cascade logs and continues, so a
  systematic authorization failure would be invisible).

---

## Fixed

- [x] **Area 18 (awaited-spawn resumption short-circuit), review 2: independent
  re-verification found no live gap.** Re-derived every checklist item from
  source rather than trusting review 1's write-up. `go tool cover` over
  `TestSpawnResume`-prefixed tests shows `resumeFromLink` at 100% statement
  coverage. Confirmed the LINK-not-node-exec-status key by reading
  `FindChildBySpawnNode` (`adapter/sqlite/instance_repo.go:693-712`) and its
  own suite, including `TestFindChildBySpawnNode_MultipleChildrenReturnsTheNewest`
  — which specifically defeats same-second `created_at` collisions with a
  `rowid DESC` tie-break and documents that dropping the tie-break "would
  silently make the executor's resumption short-circuit key on the WRONG
  child" — and confirmed the fake used in the executor's own suite,
  `fakeSpawnWaits`, honours all three key parts (parent, node, mode) rather
  than collapsing them, so the mis-wake/wrong-mode guards in
  `TestSpawnResume_SecondSpawnNodeMatchedIndependently` and
  `_FireAndForgetLinkDoesNotSatisfyAwaitedLookup` are unfailable-if-broken by
  construction, not by luck. Confirmed the still-active path (no re-spawn, no
  re-transition) is exercised for all four non-terminal `instance.State`
  values (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`) and
  terminal routing — including cancelled-routes-to-failure — is proven both
  against fakes and end-to-end through the real composition root
  (`cmd/arbitron/spawn_wait_e2e_test.go`'s
  `TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess`,
  `_FailedChildRoutesParentThroughFailure`,
  `_CancelledChildRoutesParentThroughFailure`). Confirmed review 1's
  `childState`-across-all-outcomes fix
  (`TestSpawnResume_ChildStateReflectsExactLinkStateAcrossAllOutcomes`) is
  exhaustive over all seven `instance.State` values, matching
  `domain/instance/instance.go`'s complete enumeration — no state was left
  untested. Verified `TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild`
  is not a redundant duplicate of the Completed-routes-success case despite
  identical assertions: per `AGENTS.md:88`, the resumption short-circuit
  genuinely cannot distinguish a wake re-entry from a back-edge revisit — they
  are the identical code path by design — so the test correctly pins that
  documented limitation as tested behaviour rather than fabricating a
  distinct mechanism that does not exist. Confirmed
  `SpawnWorkflowExecutor`/`resumeFromLink` has no separate construction site
  outside the composition root already verified in area 16. No code change
  was needed; this closes review 2 approval for area 18 — both approvals for
  area 18 are now recorded.

- [x] **`resumeFromLink`'s "childState in the output" claim was pinned for only
  one of seven possible link states.** Focus area 18 (awaited-spawn resumption
  short-circuit), review 1. `domain/engine/executor_spawn.go:183-184` writes
  `output["childState"] = string(link.ChildState)` unconditionally, BEFORE the
  terminal/still-active branch — so every resumption, terminal or not, must
  carry the child's exact state in its output. But
  `TestSpawnResume_ResultCarriesChildOutcome`
  (`domain/engine/executor_spawn_resume_test.go`) only asserted this for a
  `Completed` child; the sibling routing tests
  (`TestSpawnResume_FailedChildRoutesFailure`,
  `_CancelledChildRoutesFailure`) asserted only `OutputPort`, and the
  still-active edge-case test
  (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`) never
  inspected `res.Output` at all — the same shape of gap fixed repeatedly
  elsewhere in this loop: a claim proven at one call site and silently assumed
  at every sibling one. A regression that moved the `childState` write inside
  the terminal branch, or hard-coded it only for the success path, would have
  shipped undetected — leaving the trace silently missing "how the child
  ended" for a failed/cancelled/still-waiting resumption, exactly the
  self-explanatory-trace guarantee the field's own comment promises. Added
  `TestSpawnResume_ChildStateReflectsExactLinkStateAcrossAllOutcomes`, which
  drives `resumeWith` across all seven `instance.State` values and asserts
  `out["childState"]` equals the exact state given every time; confirmed it
  fails for the right reason (`childState` absent from every case) when the
  `output["childState"] = ...` line is temporarily removed, then confirmed it
  passes against the real code. Independently reviewed the rest of area 18's
  checklist from source: `executeWait` keys resumption on
  `FindChildBySpawnNode`'s link, never on node-execution status
  (`TestSpawnResume_BackEdgeRevisitRoutesFromPreviousChild` pins this as
  tested behaviour, not an assumption); the still-active branch correctly
  covers all four non-terminal states with no re-spawn and no re-transition
  (`TestSpawnResume_ActiveChildReturnsToWaitingWithoutSpawning`); terminal
  routing sends `Completed` to success and both `Failed` and `Cancelled` to
  the SAME failure port, matching the spec's no-separate-cancellation-path
  requirement, and is proven at the sink by the real composition root in
  `cmd/arbitron/spawn_wait_e2e_test.go` (`TestSpawnWaitE2E_*RoutesParentThrough*`),
  not just through fakes; the link key includes both node id and mode
  (`TestSpawnResume_SecondSpawnNodeMatchedIndependently`,
  `_FireAndForgetLinkDoesNotSatisfyAwaitedLookup`); `instance.ChildLink{...}`
  is constructed only by the real `InstanceRepo.FindChildBySpawnNode`
  (`adapter/sqlite/instance_repo.go:711`), grepped for all non-test call
  sites; and the "attempt=1 written twice" shape (once by `executeWait` on
  suspend, once by `executeWithRetry` on a terminal resume) is inherited,
  pre-existing behaviour shared by every other wait-style executor
  (`WebhookWaitExecutor` does the identical thing), not a gap this feature
  introduced. This closes review 1 approval for area 18; review 2 remains
  open.

- [x] **Area 17 (spawn executor — wait-for-child suspension), review 2: independent
  re-verification found no live gap.** Re-derived every checklist item from
  source rather than trusting review 1's write-up. Confirmed via `go tool
  cover` (running the full `TestSpawn`-prefixed suite) that `Execute`,
  `executeFireAndForget`, `executeWait`, `resumeFromLink`, `prepareAndSpawn`,
  `spawnOutput`, and `assertSpawnBranchPorts` are all at 100% statement
  coverage (a `TestSpawnWait`-only run shows `executeWait` at 95.5% because the
  `link != nil` short-circuit belongs to area 18's own tests, not this area's).
  Re-confirmed the ordering invariant (enqueue → node-exec record →
  `UpdateState(waiting)` → `NotifyCapacityFreed`) is proven twice: once as a
  synchronous call-order assertion against fakes
  (`TestSpawnWait_OrderingInvariant_SpawnThenRecordThenWaitingThenNotify`) and
  once end-to-end against the REAL `queue.Manager` with `perusercap: 1` — a
  config under which the test would hang if the ordering broke
  (`cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack`/
  `TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess` and siblings).
  Confirmed `assertSpawnBranchPorts` scopes edges by `edge.Source == nodeID`
  (no cross-node false positives) and is checked BEFORE `prepareAndSpawn`
  (`TestSpawnWait_MissingFailurePort_FailsBeforeCreatingChild`,
  `_MissingSuccessPort_...`, `_ParentLoadForBranchPortCheck_...`, each
  asserting zero preparer calls / zero spawned children). Confirmed
  `NewSpawnWorkflowExecutor` is constructed only at the real composition root
  (`cmd/arbitron/main.go:894`, grepped for all non-test call sites). Confirmed
  `queue.Manager.NotifyCapacityFreed`'s own `_ context.Context` parameter (it
  fires its evaluation via `context.Background()` in a goroutine) is
  deliberate fire-and-forget behavior, not a dropped-argument bug, and the
  test double (`mockQueueManager`) matches this shape rather than silently
  diverging from it. No code change was needed; this closes review 2 approval
  for area 17 — both approvals for area 17 are now recorded.

- [x] **`executeWait`'s own `instances.Get` failure (the load before
  `assertSpawnBranchPorts`) was untested.** Focus area 17 (spawn executor —
  wait-for-child suspension), review 1. `domain/engine/executor_spawn.go:117-120`
  loads the parent to check its snapshot's branch ports BEFORE any child is
  created — a distinct call site from `prepareAndSpawn`'s own `instances.Get`
  (already covered by `TestSpawnExecutor_ParentInstanceLoadFailure_Errors` for
  the fire-and-forget path). `go tool cover` over `TestSpawnWait`-prefixed
  tests showed this branch (lines 118-120) at 0% hits: no existing test in
  `executor_spawn_wait_test.go` set `f.instances.getErr`. Added
  `TestSpawnWait_ParentLoadForBranchPortCheck_FailsBeforeCreatingChild`
  (`domain/engine/executor_spawn_wait_test.go`), asserting the node errors, no
  child is spawned, and the preparer is never called (the load failure must
  precede any LLM cost); confirmed it fails for the right reason (nil-pointer
  panic dereferencing `parent.WorkflowSnapshot`) when the error check is
  temporarily replaced with `_ = err; if false { ... }`, then confirmed it
  passes against the real code. `go tool cover` afterward shows the branch at
  1 hit and `executeWait`'s own-scope statements at 95.5% (the remaining
  uncovered branch, the `link != nil` resumption early-return, belongs to area
  18, not this area). Also re-confirmed the rest of area 17's checklist: the
  full first-execution path (happy/success), both missing-port failures, the
  preparation and waiting-transition failures, and the node-exec-write failure
  (fixed below) together cover all five scenario categories; the ordering
  invariant is proven against a REAL `queue.Manager` (not a mock) by
  `cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack` fixture; and
  `SpawnWorkflowExecutor` is constructed only at the real composition root
  (`cmd/arbitron/main.go:894`), confirmed by grepping for
  `NewSpawnWorkflowExecutor` outside `*_test.go`. This closes review 1
  approval for area 17; review 2 remains open.

- [x] **`nodeExecs.Create`'s failure inside `executeWait` had no test, unlike
  the identical failure mode already pinned for the sibling
  `WebhookWaitExecutor`.** Focus area 17 (spawn executor — wait-for-child
  suspension); review 1 remains open (see the `executeWait` `instances.Get` gap
  above). `domain/engine/executor_spawn.go:146-157` writes the wait path's own
  node-execution record — deliberately, since `executeWithRetry` skips one on
  the wait path — as part of the load-bearing ordering this focus area exists
  to check: `enqueue → node-exec record → UpdateState(waiting) →
  NotifyCapacityFreed`. `go tool cover` over `TestSpawnWait`-prefixed tests
  showed the `Create` error branch (lines 155-157) at 0% hits, while
  `TestWebhookWaitExecutor_NodeExecCreateFails_ReturnsError`
  (`executor_webhook_test.go`) already pins the identical shape one call away
  in the sibling wait executor — the same gap already fixed repeatedly in this
  loop: a behavior proven at one call site and silently assumed at a sibling
  one. A regression that swallowed this error (e.g. logging instead of
  returning) would leave the child running with the parent stuck in
  `StateRunning` forever, since neither `UpdateState(waiting)` nor
  `NotifyCapacityFreed` would ever run afterward — undetected by any existing
  test. Added
  `TestSpawnWait_NodeExecutionRecordWriteFails_ChildSurvivesAndParentNotSuspended`
  (`domain/engine/executor_spawn_wait_test.go`), asserting the child is not
  rolled back and the parent remains `StateRunning`; confirmed it fails for the
  right reason (no error returned) when the `Create` error check is temporarily
  replaced with a discarded `_ =`, then confirmed it passes against the real
  code. Also confirmed via the same coverage run that the whole-of-instance
  per-user-capacity deadlock guard this focus area names ("ordering invariant")
  is already proven against the REAL `queue.Manager` — not just a mock — by
  `cmd/arbitron/spawn_wait_e2e_test.go`'s `bootWaitStack` fixture, which sets
  `perusercap: 1` specifically so the test would hang if the ordering broke;
  this was not a gap.

- [x] **`eng.SetSpawnLinks(instanceRepo)`'s composition-root wiring for the
  cancel cascade was verified only by reading `main.go`, never by a real
  composition-root test.** Focus area 16 review 2. `cascadeCancel`
  (`domain/engine/cancel_cascade.go:47-49`) documents and implements a
  load-bearing no-op: `if e.spawnLinks == nil { return }` — cancelling a parent
  silently leaves every active child running, exactly the shape of gap this
  loop has fixed repeatedly for other composition-root seams (the reconciler
  ordering fixed in review 1, above). Every existing `cascadeCancel` test
  (`domain/engine/cancel_cascade_test.go`) drives the cascade through a fake
  `SpawnLinkRepository` that the test itself constructs and injects — none of
  them prove `cmd/arbitron/main.go` actually calls `eng.SetSpawnLinks(instanceRepo)`
  before a real cancel reaches it. A regression that dropped, reordered past a
  point where `instanceRepo` isn't yet constructed, or conditionally skipped
  that one call in `main.go` would have shipped undetected: every child of a
  cancelled run would keep executing with no operator-visible error. Added
  `TestSpawnWaitE2E_CancellingParentCascadesToActiveChildThroughCompositionRoot`
  (`cmd/arbitron/spawn_wait_e2e_test.go`), which boots the real composition
  root via `initDomain`, triggers a `wait_for_child` parent so it suspends in
  `StateWaiting` (a cancellable state) with a still-`StateRunning` child, calls
  `ds.eng.Cancel` on the PARENT, and asserts the child is cascaded into
  `StateCancelled` too. Confirmed it fails for the right reason (child never
  reaches `StateCancelled`, 40s timeout) when `eng.SetSpawnLinks(instanceRepo)`
  is temporarily commented out in `main.go`, then confirmed it passes against
  the real code. Independently re-verified area 16's other six checklist items
  by re-reading `main.go` and re-deriving each claim from source rather than
  trusting review 1's write-up: `instanceEnricher.SetSpawnLinks` (:798),
  `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` (:815), the single
  `identityResolver` value (a stateless struct of interfaces/maps, so
  pass-by-value to three call sites is equivalent to sharing one instance)
  (:880, :900, :911, :924), `NewChildInputPreparer` fed real `providerFactory`/
  `profileResolver` (:893), `executors[workflow.NodeTypeSpawnWorkflow]`
  assigned after `engine.NewEngine` onto the SAME map the engine holds by
  reference (confirmed the map is built at :744 and passed to `NewEngine` at
  :757, so the later assignment at :894 is visible to the engine — the
  identical pattern already used for `timer_delay`/`webhook_wait`), and the
  reconciler ordering re-confirmed passing via
  `TestSpawnWaitE2E_RestartReconcilesParentAfterCrash`. This closes review 2
  approval for area 16; both approvals for area 16 are now recorded.

- [x] **The startup ordering "`spawnReconciler.Reconcile` runs AFTER
  `RecoverCrashedInstances`" was proven only through fakes, never through the
  real composition root.** Focus area 16 review 1. `cmd/arbitron/main.go`
  reads all six of area 16's checklist items correctly on inspection:
  `instanceEnricher.SetSpawnLinks(instanceRepo)` (:798), `eng.SetSpawnLinks(instanceRepo)`
  (:841), `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` (:815), a single
  `identityResolver` value constructed once (:880) and passed to the spawn
  executor (:900), the schedule evaluator (:911), and the schedule runner
  (:924), `NewChildInputPreparer` constructed and fed into the spawn executor
  (:893-903), and `executors[workflow.NodeTypeSpawnWorkflow]` assigned AFTER
  `engine.NewEngine` via the shared-map-reference pattern also used for
  `timer_delay`/`webhook_wait` (:894, confirmed exercised end-to-end by
  `TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot`,
  `cmd/arbitron/spawn_e2e_test.go`). The one claim with no composition-root
  test at all was the ordering comment at :1047 ("ORDERING IS SIGNIFICANT:
  this runs AFTER RecoverCrashedInstances... so their parents resume through
  the failure port in this same startup pass instead of hanging until the
  next restart") — `domain/engine/spawn_reconciler_test.go`'s
  `TestSpawnWaitReconciler_ResumesParentOfACrashedChildAfterRecovery` proves
  the ordering DEPENDENCY (call `RecoverCrashedInstances` then `Reconcile` on
  a shared fixture) but never proves `main.go` itself calls them in that
  order at a real restart — a regression that reordered, dropped, or
  conditionally skipped the reconciler call in `main.go` (the exact shape of
  every prior composition-root gap this loop has fixed) would have shipped
  undetected. Added `TestSpawnWaitE2E_RestartReconcilesParentAfterCrash`
  (`cmd/arbitron/spawn_wait_e2e_test.go`), which boots the real composition
  root via `initDomain`, triggers a `wait_for_child` parent, captures its
  child mid-flight (still `StateRunning`, asleep), severs the database
  out from under the live process to simulate a hard crash (no graceful
  `Shutdown`/drain), then calls `initDomain` a SECOND time against the SAME
  database file — the real restart path — and asserts the parent, which
  nothing else in the system would ever wake, resumes through the failure
  branch in this second boot. Confirmed it fails for the right reason (parent
  never reaches `StateCompleted`, 40s timeout) when the reconciler call in
  `main.go` is temporarily wrapped in `if false { ... }`, then confirmed it
  passes against the real code. This closes review 1 approval for area 16;
  review 2 remains open.

- [x] **`Engine.Spawn`'s inherited archived-workflow refusal was never exercised
  through `Spawn` itself.** Focus area 15 review 2. `Engine.Spawn`
  (`domain/engine/engine_spawn.go:52-69`) is deliberately thin: it derives a
  context carrying the link and delegates entirely to `TriggerWithDepartment`,
  whose docstring states every one of its behaviours — including refusing an
  archived workflow (`engine.go:330-348`) — is "inherited rather than
  reimplemented" so no owner-override refactor of the trigger path is needed.
  Design Phase 3's own testing strategy lists "the child workflow is archived
  → node fails" as a required **failure**-category case for the spawn path.
  But every existing archived-refusal test
  (`TestTrigger_Archived_Refused`/`_NotArchived_Succeeds`/
  `_NotArchivedResolver_NeverBlocks`/`_IsArchivedError_FailsClosed`,
  `domain/engine/engine_archived_trigger_test.go`) drives the check through
  `Trigger`, never through `Spawn` — the exact shape of gap already fixed
  repeatedly in this loop (areas 12, 13, 14): a shared helper's behavior
  proven at one call site and silently assumed at a sibling one that reaches
  it one line away through identical delegation. Confirmed today's code has
  no live bug — `Spawn` inherits the check for free by construction — but an
  untested inheritance claim is exactly the gap this loop exists to close,
  and a future refactor that gave `Spawn` its own bypass of
  `TriggerWithDepartment`, or reordered the archive check past instance
  creation, would ship undetected. Added
  `TestEngineSpawn_ArchivedChildWorkflow_Refused`
  (`domain/engine/engine_spawn_test.go`), reusing the existing
  `spawnerFixture`/`archiveResolver` fixtures, asserting `Spawn` returns
  `workflow.ErrArchived` and creates no instance for an archived child
  workflow; confirmed it fails for the right reason (`nil` error) when the
  archived-check branch in `TriggerWithDepartment` is temporarily neutered
  (`if false && e.resolver != nil`), then confirmed it passes against the
  real code. Independently re-verified the rest of area 15's checklist:
  `go tool cover` over `TestSpawn`-prefixed tests shows `Execute`,
  `executeFireAndForget`, `prepareAndSpawn`, `spawnOutput`, and
  `assertSpawnBranchPorts` at 100% statement coverage (the wait-mode branch
  of `Execute` is covered by area 17/18's own `TestSpawnWait_*` suite, not
  this scope); every design-phase-3 testing-strategy item (happy path, owner
  inheritance + departments, priority inheritance, guidance read+substitute,
  no-child-on-preparation-failure, invisible/missing child workflow,
  identity-resolution failure with no fallback, instance-load failure, enqueue
  failure surfaced, zero-declared-inputs edge case, unrecognized-snapshot-mode
  edge case, department-less owner, retry-creates-second-child, and
  grandchild-links-to-immediate-parent) is present and passing in
  `executor_spawn_test.go`; the executor is constructed only at the real
  composition root (`cmd/arbitron/main.go:894-903`) with all eight
  collaborators real, non-nil, non-hardcoded values; and `ownerFromContext`'s
  use for the parent's OWN guidance-template read (as opposed to the
  reconstructed owner identity used for `ChildInputs`/`Spawn`) is the correct,
  pre-existing pattern shared by every other file-reading executor
  (`executor_llm.go`, `executor_agent.go`, `executor_interactive.go`), not a
  gap. This closes review 2 approval for area 15; both approvals for area 15
  are now recorded.

- [x] **`fakeChildSpawner.ChildInputs` ignored `ctx`, so no test proved the
  executor passes the reconstructed OWNER identity — not the ambient caller
  identity — into `ChildInputs`.** Focus area 15 review 1.
  `domain/engine/executor_spawn.go:219-223` (`prepareAndSpawn`) builds
  `childCtx := identity.WithIdentity(ctx, ownerIdentity)` and calls
  `e.spawner.ChildInputs(childCtx, ...)` — and production's
  `Engine.ChildInputs` (`domain/engine/engine_spawn.go:24-37`) genuinely
  depends on that context: it derives the owner via `triggerOwner(ctx, ...)`
  and gates the department-visibility read via
  `e.sharedVisibilityFilter(ctx)`, exactly like the trigger path. But
  `executor_spawn_test.go`'s `fakeChildSpawner.ChildInputs` had signature
  `ChildInputs(_ context.Context, workflowName string)` — the identity
  parameter was discarded outright — so a regression that passed the raw
  incoming `ctx` (the caller's own identity, e.g. an intruder admin triggering
  the parent) instead of `childCtx` to `spawner.ChildInputs` would have shipped
  undetected: `TestSpawnExecutor_ChildOwnerIsParentOwnerNotAmbientIdentity`
  only inspects the identity that reaches `Spawn`, never the one that reaches
  `ChildInputs`, and no other test in the suite calls `ChildInputs` through the
  executor with a distinguishable ambient identity. The consequence of such a
  regression: a spawn node could silently read a DIFFERENT owner's
  department-scoped child-workflow visibility than the one it will actually
  spawn under, an existence-leak/authorization-narrowing bug in the same shape
  already fixed repeatedly in this loop (areas 12, 13, 14) — a shared contract
  proven at one call site (`Spawn`) and silently assumed at a sibling one
  (`ChildInputs`) despite both being fed by the SAME `childCtx` value one line
  apart. Extended `fakeChildSpawner` with an `inputsIdentity
  []identity.Identity` field, recording `identity.FromContext(ctx)` on every
  `ChildInputs` call (`domain/engine/executor_spawn_test.go`), and added
  `TestSpawnExecutor_ChildInputsCallCarriesOwnerIdentityNotAmbient`, which runs
  the executor with an intruder identity in the ambient context and asserts
  `ChildInputs` was called with the PARENT OWNER's identity, not the
  intruder's; confirmed it fails for the right reason (asserts the intruder's
  id where the owner's was expected) when `childCtx` is temporarily replaced
  with the raw `ctx` at the `ChildInputs` call site, then confirmed it passes
  against the real code. Reviewed the rest of area 15's checklist
  independently: `Execute`'s mode re-check against the snapshot is fully
  covered (`TestSpawnExecutor_UnrecognizedSnapshotMode_Fails`,
  `_EmptySnapshotMode_Fails`); `prepareAndSpawn`'s ordering (parent load →
  identity → child inputs → guidance read+`variable.Resolve` → prepare →
  spawn) is enforced by Go's own sequential control flow and pinned end-to-end
  by the existing failure/error-category tests (a failure at any stage leaves
  `spawner.spawned` empty); `go tool cover` shows
  `executeFireAndForget`/`prepareAndSpawn`/`spawnOutput` at 100% statement
  coverage; and the executor is constructed only at the real composition root
  (`cmd/arbitron/main.go:894-903`) with no `nil`/hardcoded collaborator. This
  closes review 1 approval for area 15; review 2 remains open.

- [x] **`sharedVisibilityFilter`'s error-propagation branch was only pinned
  through `Trigger`, not through `ChildInputs`.** Focus area 14 review 2.
  `sharedVisibilityFilter`'s docstring and review 1's fix establish that
  `Trigger`/`TriggerWithDepartment` and `ChildInputs` must share the identical
  gate so "a spawn node can never reach a workflow the same caller could not
  trigger directly" — and area 13's prior passes already pinned the
  not-found/CanSeePublication branches for `ChildInputs` via
  `TestEngineChildInputs_DepartmentWorkflow_HiddenFromOutsider`/
  `_AllowedForMember`. But the `PublicationOf` **error**-propagation branch
  (`domain/engine/engine.go:290-292`, `return false, err`) was pinned only via
  `TestEngine_Trigger_PublicationLookupError_FailsClosed`, which drives it
  through `Trigger`. `ChildInputs` calls its OWN `e.sharedVisibilityFilter(ctx)`
  at `domain/engine/engine_spawn.go:32` — same shared logic, a genuinely
  distinct call site — and this is exactly the shape of gap already fixed
  repeatedly in this loop (areas 12, 13): a shared helper's behavior proven at
  one call site and silently assumed at a sibling one. A regression that
  swallowed the `PublicationOf` error only along the `ChildInputs` path (e.g. a
  future refactor that built ChildInputs its own ad hoc filter instead of
  reusing `sharedVisibilityFilter`, or dropped the error check on just that
  call site) would let a spawn node's child-workflow lookup silently treat a
  lookup outage as "unpublished ⇒ open" instead of failing closed — undetected
  by any existing test, since none drove a `PublicationOf` error through
  `ChildInputs`. Added `TestEngineChildInputs_PublicationLookupError_FailsClosed`
  (`domain/engine/engine_spawn_test.go`), mirroring
  `TestEngine_Trigger_PublicationLookupError_FailsClosed`'s fixture shape but
  calling `ChildInputs` instead of `Trigger`; confirmed it fails for the right
  reason (no error returned) when the error branch is temporarily changed to
  `return true, nil`, then confirmed it passes against the real code. Also
  independently re-verified the rest of area 14's checklist by reading
  `TriggerWithDepartment` end to end again: `WithSpawnLink` has exactly one
  production caller (`Engine.Spawn`, `domain/engine/engine_spawn.go:63`), which
  rejects an invalid link BEFORE ever writing it to context, so the
  incomplete-link-ignored branch inside `TriggerWithDepartment` is a genuine
  defensive guard (tested via `TestTrigger_PartialSpawnLinkIsNotStamped`) rather
  than a load-bearing production path; confirmed via `go tool cover` that the
  entire link-stamping block (`engine.go:448-457`) is fully exercised; traced
  that the run-loop context (`runCtx` in `Engine.run`, derived fresh from
  `context.Background()`) never carries a parent's `Origin` or `SpawnLink`
  forward into a child's own node executions, so nested spawns cannot leak a
  grandparent's link (confirmed by `TestEngineSpawn_GrandchildLinksToItsOwnParent`
  plus direct code reading); and confirmed the single struct literal claim
  still holds (`inst := &instance.Instance{...}` then one `Create` call, no
  interim mutation). This closes review 2 approval for area 14; both approvals
  for area 14 are now recorded.

- [x] **`sharedVisibilityFilter`'s own error and not-found branches were never
  exercised through the real closure.** Focus area 14 review 1.
  `domain/engine/engine.go:282-298` is a pure code-move extraction of logic
  that predates this feature (confirmed by diffing against
  `64768283d2c32b64023801103c7aab2f5ea538d3:domain/engine/engine.go`, which
  has the identical inline closure inside `TriggerWithDepartment`) — but the
  extraction now makes this closure shared by `ChildInputs`, a NEW caller this
  feature adds. Confirmed via `go test -coverprofile` over the whole
  `domain/engine` suite that two of its three branches had 0 hits: the
  `pubs.PublicationOf` error path (`return false, err`) and the not-found path
  (`return true, nil // unpublished shared folder ⇒ open`) — every existing
  test that exercises gating at all (`TestEngine_Trigger_DepartmentWorkflow_*`)
  registers a `Publication` for the workflow name it triggers, so only the
  `CanSeePublication` branch was ever reached. `workflow.LoadVisibleFiltered`'s
  own suite tests a filter-returns-error contract generically with a fake
  filter function, which is a different thing from the engine's actual
  `PublicationOf`-backed closure ever being driven down either path. Traced the
  consequence of each: `LoadVisibleFiltered` (`domain/workflow/resolve.go`)
  propagates a filter error immediately (fail-closed, correct) and treats a
  `false` result as "absent, try next scope" (never leaks existence) — so
  today's behavior is correct, but a regression that swallowed the
  `PublicationOf` error (fail-open) or inverted the not-found branch (hiding
  every unpublished shared workflow, including newly-spawnable children) would
  have shipped undetected on both the trigger AND the spawn path. Extended
  `fakePubLookup` (`domain/engine/engine_trigger_visibility_test.go`) with an
  `errs map[string]error` field, and added
  `TestEngine_Trigger_UnpublishedSharedWorkflow_VisibleWithIdentity` (a shared
  workflow with a configured publisher lookup but no publication row must
  remain visible) and `TestEngine_Trigger_PublicationLookupError_FailsClosed`
  (a lookup error must propagate, not be swallowed); confirmed each fails for
  the right reason when its branch is temporarily neutered (not-found forced
  to `false`; error forced to `true, nil`), then confirmed both pass against
  the real code. Also reviewed the rest of area 14's checklist by reading
  `TriggerWithDepartment` end to end: the spawn link is read from
  `instance.SpawnLinkFromContext` and stamped into the SAME `Instance` struct
  literal as `TriggerSource`/`ScheduleID` (lines 441-492, one literal, no
  follow-up write — confirmed by `TestTrigger_StampsSpawnLinkAtConstruction`
  and `TestTrigger_SpawnLinkAndOriginCoexist`); an incomplete link
  (`SpawnLink.Valid()` false) is ignored wholesale, never partially persisted
  (`TestTrigger_PartialSpawnLinkIsNotStamped`); and the four pre-existing
  `TestEngine_Trigger_DepartmentWorkflow_*`/`_SystemTrigger_*` tests already
  exercise the extracted helper through its one pre-existing caller
  (`TriggerWithDepartment`), while area 13's prior pass added the equivalent
  coverage for the new caller (`ChildInputs`) reusing the same scope-aware
  fixtures — so the "unchanged for existing callers" claim is now backed by
  passing tests on both callers, not just a code-identity argument. This
  closes review 1 approval for area 14; review 2 remains open.

- [x] **`Engine.ChildInputs`'s "not-found on invisible" claim was only tested
  against a scope-agnostic mock.** Focus area 13 review 2.
  `domain/engine/engine_spawn.go:32` calls `workflow.LoadVisibleFiltered` with
  `e.sharedVisibilityFilter(ctx)` — the exact same department-visibility gate
  `TriggerWithDepartment` uses, documented as shared so "a spawn node can never
  reach a workflow the same caller could not trigger directly." But the only
  existing coverage, `TestEngineChildInputs_ScopedToContextIdentity`
  (`domain/engine/engine_spawn_test.go`), explicitly notes in its own comment
  that "the mock workflow repo is scope-agnostic, so this asserts the CALL
  SHAPE" — it cannot fail if the gate were dropped or bypassed, since the mock
  `Load` ignores the `Scope` argument entirely and a private-scope hit always
  succeeds before shared scope (and its filter) is ever consulted. This is the
  same shape of gap already fixed three times in this loop (area 12's
  Subject/Email derefs, area 9's fail-closed carve-out, area 8's zero-scope
  branch): a shared helper's behavior is proven at one call site
  (`TestEngine_Trigger_DepartmentWorkflow_HiddenFromOutsider`,
  `domain/engine/engine_trigger_visibility_test.go`) and silently assumed at a
  sibling one. A regression that passed `nil` instead of
  `e.sharedVisibilityFilter(ctx)` into `ChildInputs`'s `LoadVisibleFiltered`
  call would have let a spawn node resolve a department-restricted child
  workflow from outside that department — undetected by any existing test.
  Added `TestEngineChildInputs_DepartmentWorkflow_HiddenFromOutsider` and
  `TestEngineChildInputs_DepartmentWorkflow_AllowedForMember`
  (`domain/engine/engine_spawn_test.go`), reusing the existing scope-aware
  `newTriggerHarness`/`fakePubLookup`/`ctxWithDept` fixtures from
  `engine_trigger_scope_test.go`/`engine_trigger_visibility_test.go` — an
  outsider gets `workflow.ErrNotFound` on a department-gated shared workflow,
  a department member does not. Confirmed both fail for the right reason (no
  error returned) when the filter argument is temporarily replaced with `nil`
  in `engine_spawn.go`, then confirmed both pass against the real code.
  Reviewed the rest of area 13 independently: `Engine.Spawn`'s link-validity
  guard and thin-delegation shape are unchanged since review 1 and still
  covered (`TestEngineSpawn_RejectsInvalidLinkBeforeCreating` and siblings);
  `ChildSpawner`/`SpawnRequest` are still constructed only at the composition
  root (`cmd/arbitron/main.go:894`, passing the concrete `*Engine` to
  `NewSpawnWorkflowExecutor`); and the five-category bar (happy, success,
  failure, error, edge) is now met for both methods with a genuine
  invisibility case rather than a call-shape stand-in. This closes review 2
  approval for area 13; both approvals for area 13 are now recorded.

- [x] **`Engine.ChildInputs`'s default-user-unavailable fail-closed path was
  untested.** Focus area 13 review 1. `domain/engine/engine_spawn.go:28-31`
  calls the same `triggerOwner` helper `TriggerWithDepartment` uses, and
  correctly returns an error rather than proceeding with a nil owner when a
  system caller (no context identity) hits an unresolvable default user — but
  this is `ChildInputs`'s OWN call to `triggerOwner`, and `go tool cover`
  showed its `!ok` branch (lines 29-31) at 0% while the identical branch in
  `Trigger`/`TriggerWithDepartment` is pinned by
  `TestTrigger_DefaultUserUnavailable_FailsClosed`
  (`engine_trigger_scope_test.go:183`) — the same shape of gap already fixed
  twice in this loop (area 12's Subject/Email derefs, area 8's zero-scope
  branch): a shared helper's behavior is proven at one call site and silently
  assumed at a sibling one. A regression that dropped or inverted `ChildInputs`'s
  own `!ok` check would have let a nil-owner query fall through to
  `workflow.LoadVisibleFiltered` with `uuid.Nil`, silently reading the "private
  to nobody" scope instead of failing the node — undetected by any existing
  test, since the only production caller (`prepareAndSpawn`) always injects a
  resolved identity before calling, so this is a latent defensive-branch gap
  today, not a live bug, but the fail-closed contract is `ChildInputs`'s own
  and deserves its own pin independent of `Trigger`'s. Added
  `TestEngineChildInputs_DefaultUserUnavailable_FailsClosed`
  (`domain/engine/engine_spawn_test.go`), which sets `f.userRepo.defaultUserErr`
  and calls `ChildInputs` with a bare `context.Background()`, asserting the
  "failed to resolve owner" error; confirmed it fails for the right reason (no
  error returned) when the `!ok` check is temporarily replaced with a
  discarded `_`, then confirmed it passes against the real code. Also reviewed
  the rest of area 13 against the wired-and-fed criteria: `ChildSpawner` and
  `SpawnRequest` are constructed only at the composition root
  (`cmd/arbitron/main.go:894-903`, `eng` — the concrete `*Engine` — passed to
  `NewSpawnWorkflowExecutor`), `Spawn`'s link-validity-before-creation guard
  and thin-delegation shape are unchanged and already covered
  (`TestEngineSpawn_RejectsInvalidLinkBeforeCreating` and siblings), and the
  five-category bar (happy, success, failure, error, edge) is now met for both
  methods — `ChildInputs`'s "not-found on invisible" half of the checklist
  inherits `workflow.LoadVisibleFiltered`'s own pre-existing coverage in
  `domain/workflow/resolve_test.go` (confirmed via `git show` against the
  branch point that `LoadVisibleFiltered` predates this feature, so its own
  test suite is not this feature's job to extend). The remaining uncovered
  line (`if ctx == nil` in both `Spawn` and `ChildInputs`) mirrors an existing,
  never-tested defensive pattern already present in `Trigger`/
  `TriggerWithDepartment` pre-dating this feature — not a gap this feature
  introduced. This closes review 1 approval for area 13; review 2 remains
  open.

- [x] **`userIdentityResolver.IdentityForUser`'s Subject/Email → `LedDepartments`
  plumbing was untested.** Focus area 12 review 2.
  `cmd/arbitron/main.go:1384-1391` duplicates `AuthMiddleware.enrich`'s
  Subject/Email pointer-deref extraction (`adapter/http/auth_middleware.go:315-322`)
  rather than sharing it — the type's own docstring calls this out as
  deliberate, so "a scheduled fire, a spawned child run, and the same user's
  interactive request all resolve to identical identities" — but every
  existing `userIdentityResolver` test (`TestUserIdentityResolver_LocalDefaultUser`,
  `_CarriesStoredDepartments`, `_UnknownUserErrors`,
  `_ListDepartmentsErrorPropagates`) leaves `Subject`/`Email` nil on the stub
  user, so the two `if u.Subject != nil` / `if u.Email != nil` branches were
  only ever exercised on their false arm. The request-path copy of this exact
  logic is thoroughly pinned (`TestAuthMiddleware_Enrich_SubjectLead_Match` and
  siblings in `adapter/http/auth_middleware_test.go`), but the resolver's own
  copy had no equivalent — a regression that dropped or swapped the two
  derefs in `main.go` (breaking the documented cross-path identity-parity
  guarantee for every scheduled fire and spawned child) would have shipped
  undetected. Added
  `TestUserIdentityResolver_SubjectAndEmailDriveLedDepartments`
  (`cmd/arbitron/spawn_wiring_test.go`), which sets a matching `Subject`/`Email`
  against a `GroupConfig.Leads` entry and asserts `LedDepartments` comes back
  populated; confirmed it fails for the right reason (empty `LedDepartments`)
  when the two derefs are temporarily blanked out in `main.go`, then confirmed
  it passes against the real code. Also reviewed the rest of area 12's
  checklist independently: the twin-port relationship still holds (the
  compile-time assertion plus the real call sites at
  `schedule.NewEvaluator`/`schedule.NewRunner` in `cmd/arbitron/main.go:907-926`
  both require `userIdentityResolver` to satisfy `schedule.IdentityResolver`,
  which only compiles if the twin stays structurally identical), the
  schedule→neutral rename and the single shared composition-root value
  (`cmd/arbitron/main.go:880`) still hold, and `groups` (`groupCfg`) is fed
  from real parsed config (`cmd/arbitron/main.go:354`), the same value passed
  to both auth middlewares — not a hardcoded or test-only value. This closes
  review 2 approval for area 12; both approvals for area 12 are now recorded.

- [x] **`userIdentityResolver.IdentityForUser`'s `ListDepartments` failure path
  was untested.** Focus area 12 review 1. `cmd/arbitron/main.go:1375-1383`
  calls `r.users.GetByID` then `r.users.ListDepartments`, propagating an error
  from either — but `cmd/arbitron/spawn_wiring_test.go`'s `stubUserRepo.
  ListDepartments` had no way to return an error, so only the `GetByID` failure
  path (`TestUserIdentityResolver_UnknownUserErrors`) was exercised; a
  regression that silently swallowed a `ListDepartments` error (e.g. `depts,
  _ := ...`) and built a departmentless identity from a real user's DB row
  would have shipped undetected — the scheduler and spawn executor (the two
  production callers, `domain/schedule/runner.go:79` and
  `domain/engine/executor_spawn.go:215`) both already propagate whatever error
  the resolver returns, so this was purely a gap in the resolver's own test
  coverage, not a live bug. Also confirmed the other three parts of area 12's
  checklist already hold: `IdentityResolver` is a structurally identical twin
  of `schedule.IdentityResolver` (same method signature, redeclared per the
  codebase's established cross-domain-edge practice); the type was already
  renamed from schedule-specific to `userIdentityResolver` and ONE value is
  constructed at `cmd/arbitron/main.go:880` and passed to the spawn executor
  (`:900`), the schedule evaluator (`:911`), and the schedule run-now runner
  (`:924`); and `TestUserIdentityResolver_LocalDefaultUser` already pins Phase
  3's local-mode open question (`docs/designs/...-phase-3.md:214-217`) — the
  local-mode default user resolves cleanly through the same `BuildIdentity`
  the request path's `AuthMiddleware.enrich` uses, without needing a distinct
  resolution path. Added
  `TestUserIdentityResolver_ListDepartmentsErrorPropagates`
  (`cmd/arbitron/spawn_wiring_test.go`), extending `stubUserRepo` with a
  `deptErr` field; confirmed it fails for the right reason (no error returned)
  when the `ListDepartments` error check is temporarily replaced with `depts,
  _ := ...`, then confirmed it passes against the real code. This closes
  review 1 approval for area 12; review 2 remains open.

- [x] **A present-but-null OPTIONAL child input was untested (distinct from the
  already-tested absent case).** Focus area 11 review 2.
  `domain/engine/child_input_contract.go`'s docstring enumerates exactly three
  things `validateChildInputs` rejects — nil map, missing required, present-but-
  null REQUIRED — implying a present-but-null OPTIONAL input is accepted and
  passed through unchanged, but only the absent-optional case had a test
  (`TestValidateChildInputs_AbsentOptionalIsFine`); the present-null-optional
  shape (the model explicitly emitting `"dryRun": null` rather than omitting
  the key) was unexercised. Traced the downstream consequence in
  `Engine.TriggerWithDepartment` (`domain/engine/engine.go:377-393`): a
  present-nil variable and an absent variable take the identical branch
  (`!provided || val == nil`), so the child's own `Default` applies either way
  — the two code paths (validateChildInputs's accept-and-pass-through, then
  TriggerWithDepartment's present-nil-equals-absent) combine correctly today,
  but nothing pinned it. Added
  `TestValidateChildInputs_NullOptionalPassesThroughUnchanged`
  (`domain/engine/child_input_contract_test.go`), asserting an optional input
  present with an explicit `nil` satisfies the contract and the key survives
  unchanged in the produced map; confirmed it fails for the right reason
  (reports `dryRun` as a missing required input) when the `!f.Required`
  short-circuit is temporarily removed from the required-fields loop, then
  confirmed it passes against the real code. Reviewed the rest of area 11
  against the wired-and-fed criteria: `childInputSchema`/`validateChildInputs`
  are still called only from `Prepare` (confirmed unchanged since review 1),
  `childInputPrompt`'s stability and `structuredOutputInstruction`'s JSON
  marshaling (which sorts map keys, so the schema's property ordering was
  never actually an instability risk despite living in a `map[string]any`) were
  already covered by area 10's passes, and the five-category bar (happy,
  success, failure, error, edge) was otherwise already met by the existing
  suite. This closes review 2 approval for area 11.

- [x] **`validateChildInputs`'s "no type coercion" invariant was untested.**
  Focus area 11 review 1. `domain/engine/child_input_contract.go:84-86`
  documents that a produced value is never type-checked or coerced against its
  declared `InputField.Type` — "the trigger path is the single place input
  semantics are enforced" — but traced `Engine.TriggerWithDepartment`
  (`domain/engine/engine.go:375-405`) and confirmed the trigger path *also*
  never checks `InputField.Type` against the supplied value; it only enforces
  presence (`Required`) and the reserved-name guard via `SetInput`. So a
  declared type is pure prompt guidance, never validated anywhere in the
  engine, for spawn-produced values or ordinary manual triggers alike — this is
  pre-existing behaviour the feature does not change, not a gap to fix. Also
  reviewed `childInputSchema`/`childInputPrompt`/`validateChildInputs` as a unit
  against the wired-and-fed criteria: all three are called only from `Prepare`
  (`domain/engine/child_input_preparer.go:54-55,89`), which area 10 already
  confirmed is constructed at the real composition root
  (`NewChildInputPreparer` in `cmd/arbitron/main.go`) and fed real
  `ChildInputRequest` values built from `node.Config`/`vars.Snapshot()` in
  `executor_spawn.go`, never hardcoded; and confirmed the five-category bar
  (happy, success, failure, error, edge) was already met by the existing
  `child_input_contract_test.go` suite except for this one invariant. Added
  `TestValidateChildInputs_TypeMismatchIsNeitherRejectedNorCoerced`
  (`domain/engine/child_input_contract_test.go`), which produces a required
  `number` input as a string and asserts both that the contract is satisfied
  and that the value is passed through unchanged — pinning the documented
  no-coercion behaviour so a future change cannot silently start rejecting or
  mutating type-mismatched values without this test failing first. This closes
  review 1 approval for area 11; review 2 remains open.

- [x] **`LLMChildInputPreparer.Prepare` never set `WorkDir` on its preparation
  `ChatRequest`.** Focus area 10 review 2. Compared `Prepare`
  (`domain/engine/child_input_preparer.go`) against its sibling
  `LLMCallExecutor.Execute` (`domain/engine/executor_llm.go`), which sets
  `WorkDir: workDirStr` via the existing `WorkDirFromVars(vars)` helper
  (`domain/engine/conventions.go`) — `Prepare`'s `ChatRequest` literal built the
  same kind of tool-suppressed reasoning call but omitted `WorkDir` entirely,
  so it always defaulted to `""`. Confirmed the production consequence by
  reading `adapter/claudecli/chat_adapter.go` and `adapter/claudecli/provider.go`:
  both `ChatAdapter.Chat` and `Provider.Chat` call `validateWorkDir(req.WorkDir)`
  before doing anything else and hard-fail with `ErrWorkDirRequired` on an
  empty `WorkDir` — a deliberate PR #8 security guard against a subprocess
  inheriting the engine process's own cwd (which holds `arbitron.db`,
  `config.yaml`, and other secrets) — and confirmed via
  `TestFactory_ChatProviderFor_EmptyIsClaudeCLI`
  (`adapter/provider/factory_test.go`) that claude-cli is the default backend
  when a profile sets no `Backend`. So every spawn node with a claude-cli-backed
  `model_profile` and at least one declared child input would fail at the
  preparation call in production, on every run, with no way to configure
  around it — a "wired-and-fed" gap (a production call site building a request
  literal with a load-bearing field left at its zero value) that no existing
  test caught, because `mockChatProvider` never inspects `WorkDir`. Fixed by
  threading the parent's workspace end to end: added `WorkDir string` to
  `ChildInputRequest` (`domain/engine/ports.go`), set `WorkDir: req.WorkDir` on
  the `ChatRequest` in `Prepare` (`domain/engine/child_input_preparer.go`), and
  populated it in `prepareAndSpawn` via the same `WorkDirFromVars(vars)` helper
  `LLMCallExecutor` already uses (`domain/engine/executor_spawn.go`) — no new
  functionality, only a missing wire-up between two already-existing pieces.
  Added `TestChildInputPreparer_WorkDirReachesChatRequest`
  (`domain/engine/child_input_preparer_test.go`), pinning that a `WorkDir` on
  the incoming request reaches the outgoing `ChatRequest`, and
  `TestSpawnExecutor_ParentWorkspaceReachesPreparerAsWorkDir`
  (`domain/engine/executor_spawn_test.go`), pinning that a workspace set via the
  reserved `workspace` variable on the executor's `VariableStore` reaches the
  preparer's `ChildInputRequest.WorkDir`; confirmed both fail (unknown field /
  undefined field) against the pre-fix code and pass against the fix. This
  closes review 2 approval for area 10.

- [x] **`childInputPrompt`'s sorted-key stability invariant was untested.**
  Focus area 10 review 1. `domain/engine/child_input_preparer.go:148-150`
  documents and implements a load-bearing invariant: both the declared-input
  listing and the parent-context listing must render in SORTED key order, "so
  the prompt is stable across runs" for provider-side caching and quiet trace
  diffs — but no test asserted the order, and Go's randomized map iteration
  means a regression that dropped the `sort.Strings`/`sort.Slice` calls would
  produce a merely *flaky* prompt rather than a reliably-failing test, so it
  could ship and pass CI by chance for a long time. Reviewed the full
  `LLMChildInputPreparer`/`Prepare` implementation against Design Phase 2's
  spec line by line (resolve → assertBindingKind → select → chat → parse
  ordering, `structuredOutputInstruction` reuse, zero-input short-circuit,
  `parseChildInputResponse`'s code-fence/prose/non-object handling,
  `boundedExcerpt`, the fail-closed posture-guard-before-selection ordering)
  and confirmed every item in the design's five-category testing strategy is
  already covered, all real production wiring is fed from
  `cmd/arbitron/main.go` (`providerFactory`, `profileResolver`, both real
  adapters) and `executor_spawn.go` (`ChildInputRequest` built from
  `node.Config`/`vars.Snapshot()`, never hardcoded), and every adapter treats
  `ChatRequest.MaxTokens == 0` as "provider default" rather than "zero
  tokens" — this was the one invariant in the design with zero test coverage.
  Added `TestChildInputPreparer_PromptFieldsAndContextAreSortedForStability`
  (`domain/engine/child_input_preparer_test.go`), which declares inputs and
  parent-context keys in deliberately unsorted order and asserts their
  appearance order in the rendered prompt is alphabetical; confirmed it fails
  for the right reason (declaration order, not sorted) when the `sort.Slice`
  call is temporarily removed, then confirmed it passes against the real
  code. This closes review 1 approval for area 10.

- [x] **`ChildInputRequest.ProfileAlias` may reach the resolver empty.**
  Focus area 9 review 1. `validateModelProfiles` deliberately allows a spawn
  node with **no** `model_profile` (`domain/workflow/validation.go:183-190`),
  on the grounds that a zero-input child never makes a preparation call. But
  `SpawnWorkflowExecutor.prepareAndSpawn` passes `node.Config.ModelProfile`
  unconditionally, and `LLMChildInputPreparer.Prepare` only skips the call
  when `len(req.Inputs) == 0`. Traced `provider.ProfileResolver.Resolve(ctx,
  "")` for both the production resolver (`adapter/provider/dynamic.go:73`)
  and its cache lookup (`adapter/provider/cache.go:92`): a miss on alias `""`
  behaves identically to a miss on any other unknown alias — `ProfileCache.get`
  is a plain map lookup keyed by `Profile.Alias`, and no code path ever
  registers a profile under the empty alias — so `Resolve(ctx, "")` returns
  `provider.ErrUnknownProfile` wrapped with `""`, not a silently-resolved
  default binding. The author-time carve-out does fail closed at run time, as
  the validation comment claims. Added
  `TestChildInputPreparer_EmptyProfileAliasWithDeclaredInputs_FailsClosed`
  (`domain/engine/child_input_preparer_errors_test.go`), which asserts a child
  that declares inputs on a node with an empty `ProfileAlias` gets
  `provider.ErrUnknownProfile` naming the node, the provider is never called,
  and no partial map is returned — pinning the fail-closed behaviour so a
  future resolver change cannot silently widen this into an unguarded path.
  This closes review 1 approval for area 9.

- [x] **`childVisibilityClause`'s `IncludePublic` disjunct was never exercised in
  isolation.** Focus area 8 review 2. `adapter/sqlite/instance_repo.go:568-570`
  adds `c.visibility = 'public'` to the disjunction when `vis.IncludePublic` is
  set, but the only existing test that sets `IncludePublic`
  (`TestChildCounts_ExcludesInvisibleChildren`) also sets an `OwnerID` that
  already admits the one counted child via the `c.owner_id = ?` arm — so the
  `IncludePublic` arm itself was never the reason a child was counted.
  Confirmed by hand: replacing `if vis.IncludePublic` with `if false &&
  vis.IncludePublic` left every test in `adapter/sqlite` green. A regression
  that dropped or broke the disjunct (the same shape as the already-fixed
  zero-scope branch) would have shipped silently, under-counting children and
  hiding a hierarchy expander for a caller who should see a public child.
  Added `TestChildCounts_IncludePublicAloneCountsPublicChildren`
  (`adapter/sqlite/instance_spawn_queries_test.go`), which creates a public
  child owned by a stranger with neither `OwnerID` nor `Departments` matching
  and asserts it is counted purely under `IncludePublic`; confirmed it fails
  for the right reason against the neutered branch, then passes against the
  real code. Also reviewed the other three link-addressed queries
  (`ListActiveChildren`, `FindChildBySpawnNode`, `ListWaitingOnTerminalChild`)
  and the `ParentScope` clause in `List` against the five-category bar (happy,
  success, failure, error, edge) — all four already have dedicated coverage
  for every state/mode/tie-break/storage-failure case, and all four have a
  confirmed production caller (`cancel_cascade.go`, `executor_spawn.go`,
  `enricher.go`, `spawn_reconciler.go`) fed real data, not a fake or
  hardcoded value; `ParentScope`/`ParentInstanceID` are fed from the shared
  `instance.ParseParentScope` decoder at both the REST and MCP call sites.
  This closes review 2 approval for area 8.

- [x] **`childVisibilityClause`'s zero-scope fail-closed branch was untested.**
  Focus area 8 review 1. `adapter/sqlite/instance_repo.go:579-581` documents and
  implements a load-bearing security guard: when a caller's `ChildVisibility` is
  not `Unconstrained` and carries no `OwnerID`, `Departments`, or
  `IncludePublic` (the zero value), the disjunction is empty and the clause
  must render `0=1` — count nothing — rather than falling through to an
  unfiltered (fail-open) count. No test ever constructed a totally zero-value
  `instance.ChildVisibility{}`: the existing invisible-children coverage
  (`TestChildCounts_AllChildrenInvisibleIsAbsent`,
  `TestChildCounts_ExcludesInvisibleChildren`) always sets `OwnerID`, which
  takes the `"c.owner_id = ?"` disjunct arm, never the empty-`ors` arm this
  gap is about. A regression flipping `0=1` to `1=1` (or dropping the guard)
  would have shipped silently and leaked child-existence counts to a caller
  with no authorized scope at all. Added
  `TestChildCounts_ZeroScopeVisibilityFailsClosed`
  (`adapter/sqlite/instance_spawn_queries_test.go`), which creates a *public*
  child (visible under `IncludePublic` or an owner match, neither set here)
  and asserts it is absent from the count under `instance.ChildVisibility{}`;
  confirmed it fails for the right reason when the branch is temporarily
  changed to `1=1`, then confirmed it passes against the real code. Also
  traced the only production caller (`InstanceEnricher.batchChildCounts`,
  `domain/engine/enricher.go:104-111`) and confirmed it already short-circuits
  before ever constructing a zero-scope `ChildVisibility` (no identity on the
  context skips the call entirely) — so this was a latent, not live, gap, but
  the fail-closed contract is documented as the query layer's own invariant
  and deserves its own coverage independent of today's one caller. This is
  the "edge"/security-invariant category area 8 was missing — happy, success,
  failure, and error were already covered for all four link-addressed queries
  and the `ParentScope` clause via the existing extensive suite
  (`instance_spawn_queries_test.go`, `instance_spawn_wait_test.go`,
  `instance_repo_parent_scope_test.go`). Closes review 1 approval for area 8.

- [x] **The generated-query staleness bug that hit `trigger_source`/`schedule_id`/
  `workflow_id` recurred for migration 018's spawn-link columns.** Focus area 7
  review 2. This exact codebase already has a documented failure mode
  (`adapter/sqlite/instance_repo_stale_queries_test.go`'s pre-existing
  provenance tests): four hand-frozen `sqlc`-generated query functions in
  `adapter/sqlite/generated/*.sql.go` (`ListRunningInstances`, `NextEligible`,
  `ListWaitingWithTimer`, `ListWaitingWithWebhook`) each embed a literal SELECT
  column list and a matching `Scan(...)` call, and neither is regenerated in
  this environment (no `sqlc` binary — `which sqlc` exits 127) when a
  migration adds columns. Migration `018_instance_spawn_link` added
  `parent_instance_id`/`parent_node_id`/`spawn_mode`, but none of the four
  queries' SELECT lists or Scan calls were updated for them, and
  `generated.Instance` (`adapter/sqlite/generated/models.go`) had no fields to
  hold them either — so every instance returned by these four paths silently
  reported a zero-value spawn link (`IsRoot() == true`, `SpawnMode == ""`)
  regardless of what was actually stored, the identical silent-drop shape
  already characterized for the three provenance fields. Traced all four call
  sites (`Engine.RecoverCrashedInstances`, `Manager.Evaluate` via
  `queueRepo.NextEligible`, `queue.RecoverTimers`/`RecoverWebhooks`) and
  confirmed none has a LIVE bug today — each either re-fetches the instance
  fresh via `Get` before touching spawn-link fields, or only reads fields the
  missing columns don't affect — but left uncorrected this is a landmine for
  the very next caller that reads `.ParentInstanceID`/`.SpawnMode` off one of
  these four result sets. Fixed by adding the three `sql.NullString` fields to
  `generated.Instance`, appending `parent_instance_id, parent_node_id,
  spawn_mode` to the SELECT column list and `&i.ParentInstanceID,
  &i.ParentNodeID, &i.SpawnMode` to the Scan call in all four generated
  functions, and populating the domain conversion in `rowToInstance`
  (`adapter/sqlite/instance_repo.go`) so any caller of the shared helper gets
  the correct value rather than silently dropping it. Added four failing-first
  characterization tests in `adapter/sqlite/instance_repo_stale_queries_test.go`
  (`TestListRunning_PreservesSpawnLink`, `TestNextEligible_PreservesSpawnLink`,
  `TestListWaitingWithTimer_PreservesSpawnLink`,
  `TestListWaitingWithWebhook_PreservesSpawnLink`), each creating a real
  parent+child pair via the production `InstanceRepo`/`QueueRepo` and driving
  the real repo method, confirmed all four failed for the right reason before
  the fix and passed after, with no regression in the pre-existing provenance
  or spawn-link suites. This is the "error"/latent-defect category the area
  was missing (the round-trip, partial-link, root, and pre-existing-row cases
  were already covered by review 1) — closes review 2 approval for area 7.

- [x] **The partial-link guard in `InstanceRepo.Create` was undocumented by any
  test.** Focus area 7 review 1. `adapter/sqlite/instance_repo.go:80-88`
  documents and implements a load-bearing guard — a caller supplying only SOME
  of `ParentInstanceID`/`ParentNodeID`/`SpawnMode` must have all three written
  as NULL, never a half-written link with a dangling `parent_instance_id` and
  no `parent_node_id` (which the wake and resumption short-circuit could never
  match, stranding the parent forever) — but no test exercised any of the three
  partial-input shapes. Added
  `TestInstanceRepo_PartialSpawnLinkNeverWritten`
  (`adapter/sqlite/instance_spawn_link_test.go`), table-driven over "parent id
  without node id", "node id without parent id", and "parent+node id without a
  valid mode"; confirmed by hand that removing the guard (writing each
  NullString unconditionally) makes two of the three subtests fail, proving the
  test is load-bearing rather than vacuous. This is the "failure" category the
  area was missing — happy/success/error/edge were already covered by the
  round-trip, same-statement, not-found, and root/pre-existing-row tests. This
  closes review 1 approval for area 7.

- [x] **`ListWaitingOnTerminalChild`'s retry-produces-duplicate-candidates case
  was untested.** Focus area 5 review 2. The reconciler's query JOINs every
  terminal child of a waiting parent's current node
  (`adapter/sqlite/instance_repo.go:725-733`), with no analogous tie-break to
  `FindChildBySpawnNode`'s `ORDER BY ... LIMIT 1` — by design, since it answers
  "is there a terminal child" rather than "which child answers", but that
  means a node-level retry that leaves more than one terminal child for the
  same (parent, node) key (the exact scenario the tie-break test above
  exercises) surfaces as TWO `SpawnWaitCandidate` rows naming the same parent,
  and no test verified either the SQL layer produced this shape or that
  `SpawnWaitReconciler.Reconcile` tolerates it. Confirmed by hand that it is
  harmless — the reconciler re-verifies `parent.State == waiting` against the
  live row before each `SetQueued`, so the second candidate for an
  already-requeued parent is skipped — but an untested "confirmed by hand" is
  exactly the gap this loop exists to close. Added
  `TestListWaitingOnTerminalChild_RetryLeavesOneRowPerTerminalChild`
  (`adapter/sqlite/instance_spawn_wait_test.go`) pinning the two-row shape, and
  `TestSpawnWaitReconciler_DuplicateCandidatesForSameParentAreHandledOnce`
  (`domain/engine/spawn_reconciler_test.go`) asserting the parent is queued
  and `NotifyCapacityFreed` fires exactly once despite the duplicate. Also
  reviewed and confirmed as correct-by-design (not a gap): `SpawnWaitCandidate.
  ChildState`, populated by the SQL adapter, has no production reader —
  `SpawnWaitReconciler.Reconcile` only uses it to identify *that* a child is
  terminal (already filtered by the WHERE clause), never *how*, because the
  routing decision is deliberately deferred to `resumeFromLink`'s fresher
  `FindChildBySpawnNode` read at resumption. This closes the "edge" category
  gap for area 5 — review 2 approval.

- [x] **`FindChildBySpawnNode`'s newest-child tie-break was untested.**
  Focus area 5 review 1. `adapter/sqlite/instance_repo.go:690-697` documents
  `ORDER BY created_at DESC, rowid DESC LIMIT 1` as deliberate: a node-level
  retry can leave more than one child row for the same (parent, node, mode)
  key, and the newest must answer. But `created_at` is `strftime('%Y-%m-%dT
  %H:%M:%SZ')` — second resolution (migration `001_initial_schema.up.sql:8`)
  — so two children created back-to-back in a retry (or in any fast test)
  land on an identical timestamp; `rowid DESC` was the ONLY thing breaking the
  tie, and no test exercised the multiple-children case at all. A regression
  that dropped or reversed the tie-break would have shipped silently and made
  the executor's resumption short-circuit (`FindChildBySpawnNode` is consulted
  by area 18) key on the wrong child. Added
  `TestFindChildBySpawnNode_MultipleChildrenReturnsTheNewest`
  (`adapter/sqlite/instance_spawn_wait_test.go`), which creates a stale child
  then a newest child under the same key and asserts the newest one answers;
  confirmed it fails (returns the stale child) when the ordering is reversed
  to `ASC, rowid ASC`, then confirmed the real ordering passes it. This closes
  the "edge" category gap for area 5 — review 1 approval.

- [x] **`assertBindingKind`'s unmapped-type no-op was only verified indirectly.**
  Focus area 3 requires verifying that an UNMAPPED node type really would no-op
  `assertBindingKind`, but the existing coverage
  (`TestExpectedKindForNodeType_OtherMappingsUnchanged`) only called
  `expectedKindForNodeType` directly, never `assertBindingKind` itself, so a
  future change that made the wrapper ignore `ok` would have gone undetected.
  Added `TestAssertBindingKind_UnmappedNodeType_NoOp`
  (`domain/engine/kind_guard_spawn_test.go`), which calls `assertBindingKind`
  with an unmapped type (`workflow.NodeTypeScript`) across chat, agent, and a
  bogus `ProfileKind`, asserting `nil` every time — this is the review-2
  approval for area 3.

- [x] **`instance.Instance.IsRoot()` had no production caller.**
  `domain/instance/instance.go:159` documents itself as "the ONE place the null
  link means root convention is expressed, so read paths do not each re-derive
  it" — but no production code called it. `domain/engine/spawn_wake.go:43`
  (`Engine.OnChildTerminal`) independently re-derived the exact same convention
  with a two-field nil check (`child.ParentInstanceID == nil ||
  child.ParentNodeID == nil`) guarded by the comment "a root run has nobody to
  wake" — literally `IsRoot()`'s definition, inline. The two-field check is
  safe to collapse to `child.IsRoot()` because `ParentInstanceID` and
  `ParentNodeID` are never set independently: `engine.go`'s trigger-path
  stamping only writes either both or neither (gated on `SpawnLink.Valid()`),
  and `adapter/sqlite`'s create/scan path persists and reads the three link
  columns as a single NULL-together unit. Replaced the inline check with
  `child.IsRoot()`, giving the method its first production caller; existing
  coverage (`TestOnChildTerminal_RootRunIsANoOp`,
  `TestInstanceNullLinksAreRoots`, `TestInstanceWithLinkIsNotRoot`) still
  passes unchanged. The other two re-derivations named in the original
  finding — the `ParentScopeRoots` SQL clause and the dashboard's
  `topLevelRows` — are a different layer (a SQL predicate and a TypeScript
  predicate over a JSON DTO) that cannot call a Go method, so they are not
  candidates for this fix; `IsRoot()`'s scope is Go in-process read paths over
  an already-loaded `Instance`.

## Won't Fix

Out of scope for this feature. Recorded so the omission is a decision, not an
oversight. **None of these is an unfed production path** — each is either
explicitly a spec non-goal or pre-existing behaviour this change set did not
touch.

- **Recursion / runaway limits on spawning.** The spec lists recursion limits and
  loop prevention as explicit non-goals, and `AGENTS.md:85` records that the
  engine has no general runaway guard. A workflow that spawns itself grows
  instance count geometrically. Phase 3's design records this as a known, accepted
  risk. Out of scope.
- **Lifting the awaited-spawn loop-body restriction.** Phase 4's design states the
  short-circuit cannot distinguish re-entry-after-wait from a back-edge revisit,
  and explicitly does not attempt to lift the restriction — for either awaited
  spawn nodes or interactive nodes, which carry the same restriction for the same
  reason. Documented in `AGENTS.md` instead.
- **Duplicate child on fire-and-forget node retry.** A node-level retry of a
  fire-and-forget spawn re-invokes `Execute` and creates a second child. Phase 3
  records this as intended and pinned by test, since the spec states
  fire-and-forget child outcomes do not affect the parent. (The awaited path is
  idempotent via the short-circuit.)
- **Atomicity of the child's terminal write and the parent's wake.** Phase 4
  defers this as a durability refinement: `SpawnWaitReconciler` makes
  non-atomicity recoverable rather than fatal.
- **A child left running when the parent fails at the spawn node.** Deliberate:
  the child may already be executing side effects, and cancelling it from a
  failing parent would layer a second failure on the first. Consistent with the
  engine's existing choice to retain workspaces on failure.
- **Hierarchy on the single-instance detail view.** Phase 5 covers the list view
  only.
- **Editor-side UI for spawn nodes** (node palette, per-type port rendering,
  configuration form). `dashboard/src/graph` has no editor for any node type;
  `conditional` has had named ports far longer and still renders through
  `ArbitronNode`'s single source handle. Spawn nodes are authored in
  `workflow.json` and policed by Phase 1 validation.
- **Server-side depth limit on hierarchy expansion.** Expansion is user-driven,
  one level per request; depth costs requests, not an unbounded query.
- **Pre-existing baseline failure**
  `adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`. Present
  at the branch point; not this feature's job. Must not block the loop, and must
  not be "fixed" by weakening the sample-pipeline validation this feature extends.
