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

- [ ] **`ChildInputRequest.ProfileAlias` may reach the resolver empty.**
  `validateModelProfiles` deliberately allows a spawn node with **no**
  `model_profile` (`domain/workflow/validation.go:186`), on the grounds that a
  zero-input child never makes a preparation call. But
  `SpawnWorkflowExecutor.prepareAndSpawn` passes `node.Config.ModelProfile`
  unconditionally, and `LLMChildInputPreparer.Prepare` only skips the call when
  `len(req.Inputs) == 0` (`domain/engine/child_input_preparer.go:48`). Verify what
  `provider.ProfileResolver.Resolve(ctx, "")` does for a child that DOES declare
  inputs on a node with no alias: if it silently resolves a default binding, the
  author-time carve-out has widened into an unguarded path. Add the failing case
  (declared inputs + empty alias) and assert a clear error naming the node.

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
