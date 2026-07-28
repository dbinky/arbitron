# Workflow Spawn Node — Design Phase 3: Spawn Executor (Fire-and-Forget) and Wiring

## Purpose

The first end-to-end slice. A parent workflow reaches a `spawn_workflow` node
in fire-and-forget mode, prepares startup values, creates a child instance
owned by the parent's owner and linked back to the spawning node, and
continues. Phase 1's link fields get their producer here.

Wait-for-child is **out of scope for this phase** and lands in Phase 4. A node
configured for wait-for-child fails cleanly in this phase rather than silently
behaving as fire-and-forget (see Error Handling).

## Architecture

One new executor plus two narrow port additions. The executor is registered
into the engine's mutable executor map **after** engine construction, because
it depends on the engine itself — the exact pattern `cmd/arbitron/main.go`
already uses for `timer_delay` and `webhook_wait` (lines 805-815), whose
comment records the reason: the engine stores a *reference* to the executor
map, not a copy, so post-construction mutation is the sanctioned way to close a
dependency cycle.

The decisive architectural finding for this phase: **no owner-override refactor
of the trigger path is needed.** `TriggerWithDepartment` already resolves the
run owner from the context identity (`engine.go:281-284`, via `triggerOwner` at
`engine.go:208-216`) and gates the shared-workflow read by that identity's
departments (`engine.go:288-302`). A background caller that reconstructs the
owner's identity and injects it into the context therefore gets owner
inheritance and correct authorization for free.

That reconstruction already exists for exactly this purpose: `schedule`'s
`IdentityResolver` (`domain/schedule/evaluator.go:47-51`), implemented by
`scheduleIdentityResolver` (`cmd/arbitron/main.go:1303-1332`), which builds a
user's full runtime identity — role, departments, and `LedDepartments` — from a
bare user id via `UserRepository.GetByID` + `ListDepartments` +
`identity.BuildIdentity`. Its comment states the intent directly: a scheduled
fire and the same user's interactive request must resolve to *identical*
identities. A spawned child needs the same guarantee.

## Components

### 1. `IdentityResolver` port in `domain/engine`

**One job:** turn a user id into that user's runtime identity.

Declared in `domain/engine/ports.go` as a structurally identical twin of
`schedule.IdentityResolver`. It is redeclared rather than imported because
`domain/engine` must not depend on `domain/schedule`. This mirrors the
codebase's established practice of redeclaring a type across a domain edge to
keep the dependency one-directional — `instance.Claims` vs `identity.Claims`
(`domain/instance/provisioner.go:17-24`) and `instance.Visibility` vs
`workflow.Visibility` (`domain/instance/instance.go:52-56`) both do this, each
with a comment stating the edge being preserved.

The **same concrete value** satisfies both ports and is passed to both the
schedule evaluator and the spawn executor at the composition root, so a
scheduled run and a spawned run can never resolve a user differently. The
concrete type is renamed from `scheduleIdentityResolver` to a neutral name
since it is no longer schedule-specific — a targeted rename inside code this
feature touches.

### 2. `ChildSpawner` port in `domain/engine`

**One job:** create and enqueue a child workflow instance, linked to its parent.

Declared in `domain/engine/ports.go`; satisfied by `*Engine`. Semantically it
takes the child workflow name, the prepared startup values, the parent instance
id, the spawning node id, and the spawn mode; it returns the new child
instance's id, or an error.

Implemented as a thin method on `Engine` that delegates to the existing trigger
path and then stamps the link. Two implementation constraints are architectural,
not incidental:

- **The link must be persisted before the child can reach a terminal state.**
  If the child could complete before its link row is written, Phase 4's wake
  hook would find no parent to notify and the parent would wait forever. The
  link is therefore established as part of instance creation, not as a
  follow-up write after enqueue.
- **Priority is inherited from the parent.** A child of a high-priority run
  competes on equal footing with its parent's siblings. Nothing in the spec
  requires an independent priority axis, and inventing one is YAGNI.

The port exists so the executor depends on a single-operation interface rather
than on the whole `Engine`, which is both an isolation boundary and what makes
the executor unit-testable.

### 3. `SpawnWorkflowExecutor`

**One job:** execute one spawn node — prepare inputs, spawn the child, and tell
the engine how to proceed.

Its collaborators are all ports: `ChildInputPreparer` (Phase 2),
`ChildSpawner`, `IdentityResolver`, `workflow.Repository` (to load the child's
declared inputs), `FileReader` (guidance prose), and the instance repository.

Execution sequence in fire-and-forget mode:

1. **Read the instance id from context** via `InstanceIDFromContext`
   (`domain/engine/executor_timer.go:26`), the established way every
   state-touching executor identifies its run.
2. **Re-check the configured mode.** Instances execute from `WorkflowSnapshot`,
   which may predate Phase 1's validation. The executor does not trust that
   validation ran — the same defensive stance `kind_guard.go:12-21` documents.
   An unrecognized mode is an error, never a default.
3. **Load the parent instance** to obtain its owner id.
4. **Reconstruct the owner's identity** via `IdentityResolver` and derive a
   child-spawn context carrying it.
5. **Load the child workflow's declared inputs** using that identity's owner,
   through `workflow.LoadVisibleFiltered`. If the parent's owner cannot see the
   child workflow, this returns not-found and the node fails — fail-closed, with
   no existence leak, exactly as the trigger path behaves for the same user.
6. **Read optional guidance prose** via `FileReader`.
7. **Prepare startup values** via `ChildInputPreparer`. A contract failure here
   is the spec's "required startup values cannot be produced" condition and
   fails the node.
8. **Spawn the child** via `ChildSpawner`, which creates it under the parent's
   owner with the link fields set.
9. **Return a normal (non-wait) node result** whose output records the child
   instance id and the child workflow name.

The parent then advances along its single outgoing edge through the engine's
ordinary success path (`handleNodeSuccess`, `engine.go:946`). No engine change
is required for fire-and-forget.

Recording the child instance id in the node's output is deliberate: it makes
the child addressable from the parent's variable store, so downstream nodes and
the trace can reference the spawned run.

## Data Flow

```
engine run loop (engine.go:669)
   │  node.Type == spawn_workflow
   ▼
SpawnWorkflowExecutor.Execute(ctx, node, vars, workflowName)
   │
   ├─ InstanceIDFromContext(ctx) ──────────► parent instance id
   ├─ instances.Get(parent id) ────────────► parent OwnerID
   ├─ IdentityResolver.IdentityForUser ────► parent owner's Identity
   │                                            │
   │                                    identity.WithIdentity(ctx, …)
   │                                            │
   ├─ workflow.LoadVisibleFiltered ────────► child's declared Inputs
   ├─ FileReader.ReadFile ─────────────────► guidance prose (optional)
   ├─ vars.Snapshot() ─────────────────────► parent context map
   │                                            │
   ├─ ChildInputPreparer.Prepare ──────────► validated startup values
   │                                            │
   └─ ChildSpawner.Spawn(child wf, values, parent id, node id, mode)
             │
             └─► Engine → TriggerWithDepartment (owner = parent owner)
                        → instance created with link fields
                        → queueManager.Enqueue(child id, parent owner id)
                                    │
                                    ▼
                    NodeResult{Output: {childInstanceId, childWorkflow}}
                                    │
                                    ▼
                    engine handleNodeSuccess → follow single edge
```

## Data Provenance

| Input | Producer that exists today |
| --- | --- |
| Parent instance id | `engine.InstanceIDFromContext` (`domain/engine/executor_timer.go:26`), set by the run loop at `engine.go:817` |
| Parent instance / `OwnerID` | `instance.InstanceRepository.Get` (`domain/instance/ports.go`) |
| Parent owner's runtime identity | `identity.BuildIdentity` (`domain/identity/builder.go:39`) fed by `UserRepository.GetByID` + `ListDepartments`, as assembled in `cmd/arbitron/main.go:1308-1332` |
| Identity injection into context | `identity.WithIdentity` (`domain/identity/identity.go:79`) |
| Child workflow's declared inputs | `workflow.LoadVisibleFiltered` (`domain/workflow/resolve.go:33`) over `workflow.Repository` |
| Shared-scope visibility filter | The publication-lookup filter built at `cmd/arbitron/main.go:1280-1294` and at `engine.go:288-302` |
| Parent context map | `variable.VariableStore.Snapshot()`, the `vars` argument of `Execute` |
| Guidance prose | `engine.FileReader.ReadFile` (`domain/engine/ports.go:17`) |
| Prepared startup values | `ChildInputPreparer` — **Phase 2 of this design** |
| Child instance creation + enqueue | `Engine.TriggerWithDepartment` (`engine.go:274`) → `instance.InstanceRepository.Create` → `QueueManager.Enqueue` (`domain/engine/ports.go:166-169`) |
| Link field storage | The columns added in **Phase 1 of this design** |
| Node output persistence | `instance.NodeExecutionRepository.Create`, written by the engine's `executeWithRetry` (`engine.go:997`) |

Every input traces to a concrete producer. Two of them are earlier phases of
this design; both are named as such. There are no unresolved dependencies.

**Deferred to plans:**

- Composition-root wiring in `cmd/arbitron/main.go`: constructing the preparer
  and the spawn executor, and registering the executor into the `executors` map
  **after** `engine.NewEngine`, following the precedent at lines 805-815.
- Renaming `scheduleIdentityResolver` and passing the single value to both the
  schedule evaluator (lines 867, 880) and the spawn executor.
- Injecting the `IdentityResolver` and `ChildSpawner` (`*Engine`) into the
  executor.
- Exact shape of the spawn node's output object.
- Whether `local` auth mode — which `identity.BuildIdentity`'s comment notes
  bypasses the builder for its hardcoded admin (`domain/identity/builder.go:34-37`)
  — needs a distinct resolution path for spawn; the plan must confirm the
  resolver behaves correctly for the local-mode default user before wiring.

## Integration Seams

- `domain/engine/ports.go` — `IdentityResolver` and `ChildSpawner` declarations.
- New file in `domain/engine` for `SpawnWorkflowExecutor`.
- `domain/engine/engine.go` — the `Spawn` method satisfying `ChildSpawner`,
  delegating to the existing trigger path.
- `cmd/arbitron/main.go` — construction, wiring, post-construction executor
  registration, and the resolver rename.
- No change to the engine run loop.

## Error Handling

Every failure below returns a plain error from `Execute`. The engine then
applies the node's resolved `ErrorBehavior` (`engine.go:1183`) through
`handleNodeError` (`engine.go:873`) — retry, default output, or error-node
routing. The executor encodes no control-flow policy of its own.

- **Wait-for-child mode in this phase** — explicit "not yet supported" error.
  Failing loudly is required: silently treating it as fire-and-forget would
  leave the parent's `success`/`failure` ports unreachable and the parent would
  proceed as though a child had succeeded, which is a wrong answer rather than
  a visible gap.
- **Unrecognized mode** — error, never coerced to a default.
- **Missing instance id in context** — error, matching
  `executor_timer.go:63-66`.
- **Identity resolution failure** — error. It is *not* downgraded to a system
  identity: falling back would silently change which workflows are visible to
  the spawn and could widen reach beyond the owner.
- **Child workflow not visible or not found** — error. Indistinguishable from
  "does not exist" by design; the visibility filter's contract
  (`domain/workflow/resolve.go:29-32`) is to never reveal that a
  department-scoped workflow exists to an outsider.
- **Child workflow archived** — the existing archived check in the trigger path
  (`engine.go:317-331`) refuses and returns an error; it fails closed even on a
  lookup error, and the spawn node inherits that posture unchanged.
- **Input preparation failure** — error, propagated with the preparer's message
  naming the missing required inputs.
- **Child creation or enqueue failure** — error. If creation partially
  succeeded, the child exists in a queued state with its link set and is
  visible in the hierarchy; a retry of the parent node would create a second
  child. This is accepted for fire-and-forget, where the spec states child
  outcomes do not affect the parent, and it is called out in the plan so the
  behavior is a decision rather than a surprise.
- **Recursion is not bounded.** The spec lists recursion limits as a non-goal,
  and the engine has no general runaway guard (`AGENTS.md:77-79`). A workflow
  that spawns itself grows instance count geometrically, each consuming a queue
  slot and a workspace. **Recorded here as a known, accepted risk**, not
  designed against.

## Testing Strategy — STRICT TDD

Tests precede implementation, using fakes for every port. All five categories
are required.

- **Happy path** — a fire-and-forget spawn node prepares values, creates one
  child owned by the parent's owner with parent instance id / parent node id /
  mode set, returns a non-wait result carrying the child id, and the engine
  advances along the single outgoing edge.
- **Success** — the child is created under the **parent instance's** owner and
  not the ambient context identity, asserted by running the parent with a
  *different* identity in context (this is the spec's owner-inheritance
  requirement and the whole reason for the resolver); the reconstructed
  identity carries the owner's departments so a department-scoped child
  workflow resolves; the child inherits the parent's priority; the node output
  records the child instance id; a spawn from a run that itself has a parent
  produces a correctly linked grandchild.
- **Failure** — preparation reports unsatisfied required inputs → node fails
  and **no child is created** (asserted, because a child created from an
  incomplete payload is the worst failure mode here); the child workflow is
  invisible to the parent's owner → node fails; the child workflow is archived
  → node fails; a wait-for-child node in this phase → explicit unsupported
  error, asserting it does not fall through to fire-and-forget.
- **Error** — identity resolution fails → error, and no child is created and no
  fallback to a system identity occurs; instance load fails → error; enqueue
  fails → error surfaced rather than swallowed; missing instance id in context
  → error.
- **Edge case** — a child workflow with zero declared inputs spawns with an
  empty payload; a spawn node whose snapshot carries an unrecognized mode fails
  even though current validation would have rejected it at author time (proving
  the executor does not trust upstream validation); a parent whose owner has no
  departments still spawns a private child workflow; the node's error behavior
  configured with retries re-invokes the executor and creates a second child —
  asserted so the behavior is pinned rather than discovered later; a spawn node
  reached twice via a graph back-edge spawns twice, consistent with the
  documented re-execution semantics of loop revisits
  (`domain/engine/loop_mechanics_test.go:49`).
