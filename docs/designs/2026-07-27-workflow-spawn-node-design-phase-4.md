# Workflow Spawn Node — Design Phase 4: Wait-for-Child

## Purpose

The parent suspends at a spawn node until its child finishes, then resumes
through exactly one of two required ports: `success` when the child completed,
`failure` when the child failed or was cancelled.

This is the highest-risk phase. It introduces a suspension whose wake signal
comes from a *different instance*, which means it must be correct across
process restarts and must not wake the wrong parent or the wrong node.

## Architecture

Three mechanisms, each reusing an established seam:

1. **Suspension** — the executor returns a wait result and transitions the
   parent to waiting, the contract `WebhookWaitExecutor` follows
   (`domain/engine/executor_webhook.go:117-142`).
2. **Resumption** — on re-entry the executor short-circuits instead of
   re-spawning, the pattern `InteractiveExecutor` documents at
   `domain/engine/executor_interactive.go:69-74`: after a wait ends, the queue
   re-starts the instance with `CurrentNodeID` still pointing at the same node,
   so the executor must recognize its own completed work and let the engine
   advance.
3. **Wake** — when a child reaches a terminal state, its awaiting parent is
   re-queued.

The engine's run loop is **not** modified. It already re-enters at
`CurrentNodeID` (`engine.go:722-726`), already exits cleanly on a wait result
(`handleNodeSuccess`, `engine.go:957-969`), and already routes by output port
(`findNextNode`, `engine.go:1195`). Wait-for-child composes from behavior that
exists.

## Components

### 1. Spawn executor — wait path

**One job:** suspend the parent after starting the child, exactly once.

Ordering is load-bearing and stated as an invariant:

> **Enqueue the child → write the node execution record → transition the parent
> to waiting → notify the queue that capacity was freed.**

The reasoning: parent and child are enqueued under the *same* owner
(`QueueManager.Enqueue` takes an owner id, `domain/engine/ports.go:167`), so
they compete for the same per-user capacity. With a per-user cap of one, the
child sits queued until the parent's waiting transition releases the slot.
Skipping the notify, or transitioning to waiting before the child is enqueued,
produces a stall that looks like a hung workflow rather than an error. Both
`WebhookWaitExecutor` (`executor_webhook.go:139`) and `TimerDelayExecutor`
(`executor_timer.go:120`) end with the same notification for the same reason.

The executor writes its own node execution record before suspending, because
`executeWithRetry` deliberately skips writing one on the wait path
(`engine.go:1055-1056`) — the same reason the webhook executor writes its own
at `executor_webhook.go:96-108`.

### 2. Resumption short-circuit

**One job:** on re-entry, decide whether to spawn, keep waiting, or route.

The short-circuit keys on **the link**, not on node-execution status:

- Look up the awaited child for `(this parent instance, this node id,
  mode = wait_for_child)`.
- **No child found** → first execution. Prepare inputs and spawn (Phase 3's
  sequence), then suspend.
- **Child found, still active** → return a wait result *without* re-spawning
  and without re-transitioning state. This is the idempotent path that makes a
  spurious re-entry harmless.
- **Child found, terminal** → return a normal result whose output port is
  `success` (child completed) or `failure` (child failed **or** cancelled).

Keying on the link rather than on node-execution status is a deliberate
departure from the interactive executor's technique. The interactive node only
needs to know *that* it finished; the spawn node needs to know *how the child
ended* in order to choose a port. The child's persisted state is the single
authority for that; a second copy in the node record could disagree with it
after a retry or a late cancellation.

Cancelled children route to `failure`. The spec is explicit that there is no
separate cancellation path.

**Documented limitation — an awaited spawn node is not supported inside a loop
body.** The short-circuit cannot distinguish a re-entry-after-wait from a
back-edge revisit: both arrive at the node with a terminal link already
recorded for `(parent, node)`. A revisit would therefore read the *previous*
child's outcome and route immediately instead of spawning a new child.

This is not a new class of restriction. `AGENTS.md:79-80` already states that
interactive nodes must not be placed in loop bodies, for the same underlying
reason — their reuse/resumption behavior is not appropriate for repeated
passes. The awaited spawn node joins that list, and this design does **not**
attempt to lift the restriction for either node type.

**Fire-and-forget spawn is loop-safe** and carries no such restriction: it has
no short-circuit, so each revisit spawns a new child, consistent with the
documented re-execution semantics of loop revisits
(`domain/engine/loop_mechanics_test.go:49`).

### 3. Child-terminal wake

**One job:** re-queue the parent that is waiting on a specific child.

The match condition is the correctness core of this phase:

> A terminal child wakes its parent **only if** the parent is in the waiting
> state **and** the parent's current node id equals the child's recorded parent
> node id **and** the link's mode is wait-for-child.

All three conjuncts are required. Matching only on "this child's parent is
waiting" is wrong: a parent can be waiting at spawn node *B* while a
fire-and-forget child launched earlier from node *A* reaches a terminal state.
That child would re-queue the parent, which would then resume at node *B*,
short-circuit against *B*'s still-running child, and — worse, if *B*'s child
had by then finished — route node *B* by an event that had nothing to do with
it. The mode conjunct alone also isn't sufficient, because a parent may carry
two awaited spawn nodes in sequence, each with its own child.

The wake performs the state transition to queued and notifies the queue, the
same two steps the webhook receive path takes (`adapter/http/webhook_handler.go:136-146`)
and that `queue.Manager.OnTimerFired` takes for an expired delay
(`domain/queue/manager.go:176-181`).

**Where it is invoked.** Terminal-state writes are scattered across twelve call
sites (ten in `domain/engine/engine.go`, two in `domain/queue`). Hooking all
twelve would be fragile, so the design does not depend on it:

- **The reconciler (below) is the authority for correctness.**
- The wake is invoked from a small set of consolidated points: `failInstance`
  (`engine.go:861`), the run loop's successful completion (`engine.go:836`),
  and `Cancel` (`engine.go:1385`). These cover the overwhelming majority of
  terminal transitions and make the common case immediate.
- The webhook-timeout path in `domain/queue/manager.go:183-195` sits outside
  `domain/engine` and cannot import it. It receives an **optional, nil-safe
  hook setter** on `queue.Manager`, the pattern the Manager already uses for
  two other optional collaborators (`SetWebhookRegistry` at
  `domain/queue/manager.go:61`, `SetScheduler` at line 67).

**Targeted improvement, in scope:** three of the run loop's terminal paths
(`engine.go:685`, `:719`, `:739`) call `SetFailed` directly instead of the
`failInstance` helper that exists specifically to consolidate that pattern and
that the rest of the loop uses. Routing them through the helper reduces the
hook surface and is a focused cleanup of code this feature touches. No other
consolidation is proposed.

### 4. Spawn-wait reconciler

**One job:** guarantee that no parent waits forever on a child that is already
finished.

An in-process wake is lost if the process dies between the child's terminal
write and the parent's re-queue. The reconciler closes that hole: it finds
instances in the waiting state whose awaited child is already terminal and
re-queues them.

It runs at startup, beside `RecoverCrashedInstances` (`engine.go:1592`) and the
queue's `RecoverTimers`/`RecoverWebhooks` (`domain/queue/recovery.go:14,47`),
which exist for precisely this class of problem.

**Startup ordering is significant:** the reconciler must run **after**
`RecoverCrashedInstances`. That sweep marks instances left in the running state
by a crash as failed — which includes crashed *children*. Running the
reconciler afterwards means those children are already terminal, so their
parents resume through `failure` in the same startup pass instead of hanging
until the next restart. A waiting parent is itself untouched by
`RecoverCrashedInstances`, since it is not in the running state.

The query it needs — waiting instances that have an awaited child — is a new
port capability alongside the existing `ListWaitingWithTimer` and
`ListWaitingWithWebhook` (`domain/instance/ports.go`), which serve the same
role for the other two wait kinds.

## Data Flow

**Suspension:**

```
parent run loop → SpawnWorkflowExecutor.Execute
   │  mode = wait_for_child, no existing link
   ├─ prepare inputs + spawn child        (Phase 3 sequence)
   ├─ write node execution record          (executeWithRetry skips it on wait)
   ├─ instances.SetWaiting(parent)
   └─ queueMgr.NotifyCapacityFreed         ← releases the slot the child needs
        │
        └─► NodeResult{IsWait: true} → handleNodeSuccess halts the run loop
```

**Wake and resumption:**

```
child reaches terminal state (completed | failed | cancelled)
   │
   ├─ engine failInstance / run-completion / Cancel
   └─ queue.Manager webhook-timeout hook            [optional, nil-safe]
        │
        ▼
   match: parent.State == waiting
       ∧ parent.CurrentNodeID == link.parentNodeID
       ∧ link.mode == wait_for_child
        │
        ├─ queueRepo.SetQueued(parent)
        └─ queueMgr.NotifyCapacityFreed
             │
             ▼
   queue starts parent → run() re-enters at CurrentNodeID (the spawn node)
             │
             ▼
   SpawnWorkflowExecutor.Execute → link found, child terminal
             │
             ▼
   NodeResult{OutputPort: "success" | "failure"}
             │
             ▼
   handleNodeSuccess → findNextNode(port) → parent continues on the wired branch

[startup]  RecoverCrashedInstances  →  SpawnWaitReconciler
           (crashed children → failed)   (their parents → queued)
```

## Data Provenance

| Input | Producer that exists today |
| --- | --- |
| Parent instance id at execution | `engine.InstanceIDFromContext` (`domain/engine/executor_timer.go:26`) |
| Existing link for `(parent, node)` | The link columns from **Phase 1**, written by the spawner in **Phase 3**; read via the link-addressed query added in Phase 1 |
| Child's terminal state | `instance.Instance.State` via `InstanceRepository.Get` (`domain/instance/ports.go`) |
| Parent's current node id | `instance.Instance.CurrentNodeID`, written by the run loop via `UpdateCurrentNode` (`engine.go:804`) |
| Waiting-state transition | `instance.InstanceRepository.SetWaiting` (`domain/instance/ports.go`) |
| Re-queue transition | `instance.QueueRepository.SetQueued` (`domain/instance/ports.go`), as used at `domain/queue/manager.go:178` |
| Capacity release | `engine.QueueManager.NotifyCapacityFreed` (`domain/engine/ports.go:168`) |
| Node execution record on the wait path | `instance.NodeExecutionRepository.Create`, as the webhook executor calls it at `executor_webhook.go:106` |
| Output-port routing | `Engine.findNextNode` (`engine.go:1195`), fed from `NodeResult.OutputPort` (`domain/engine/result.go`) |
| Waiting-on-child query for the reconciler | **New capability on the queue repository port**, added in this phase alongside `ListWaitingWithTimer` / `ListWaitingWithWebhook` (`domain/instance/ports.go`) |
| Crashed-child failure at startup | `Engine.RecoverCrashedInstances` (`engine.go:1592`) |

Every input traces to a concrete producer or to a named earlier phase of this
design. There are no unresolved dependencies.

**Deferred to plans:**

- The SQL and index for the waiting-on-child query, and its `adapter/sqlite`
  implementation.
- Registering the reconciler in `cmd/arbitron/main.go` **after**
  `RecoverCrashedInstances` in the startup sequence.
- Wiring the optional child-terminal hook onto `queue.Manager` at the
  composition root.
- Routing `engine.go:685/719/739` through the `failInstance` helper.
- Extending the `AGENTS.md` loop-body restriction (currently naming interactive
  nodes only, lines 79-80) to name the awaited spawn node, so the limitation is
  discoverable where authors already look for it.
- Whether the wake transition and the child's terminal write should share a
  transaction; the reconciler makes non-atomicity recoverable rather than
  fatal, so this is a durability refinement, not a correctness prerequisite.

## Integration Seams

- `domain/engine` — spawn executor's wait and resumption paths; the wake
  invocation inside `failInstance`, run completion, and `Cancel`; the
  reconciler beside `RecoverCrashedInstances`.
- `domain/instance/ports.go` — the waiting-on-child query.
- `domain/queue/manager.go` — optional child-terminal hook setter.
- `adapter/sqlite` — query implementation.
- `cmd/arbitron/main.go` — startup ordering and hook wiring.
- **Unchanged:** the run loop, `findNextNode`, `handleNodeSuccess`,
  `NodeResult`.

## Error Handling

- **Suspension fails partway.** If the child is enqueued but the parent's
  waiting transition fails, the executor returns an error and the parent fails
  at that node. The child continues and is visible in the hierarchy as an
  orphaned-but-linked run. This is preferred over rolling the child back: a
  child may already be executing side effects, and cancelling it from a failing
  parent would be a second failure layered on the first. The plan records this
  as intended behavior.
- **A parent that fails at a spawn node leaves its child running.** Consistent
  with the above, and with the engine's existing choice to retain workspaces on
  failure for debugging (`engine.go:855-859`).
- **Missing branch edge at resume time.** `findNextNode` returns an empty
  string when no edge matches the port (`engine.go:1204-1207`), which ends the
  run as if complete. Phase 1's validation requires both ports, but a snapshot
  predating that validation could reach here. The executor therefore verifies
  both ports are present in the snapshot at *first* execution and fails the
  node then — before a child is created — rather than silently completing the
  parent after the child finishes.
- **Wake for a parent no longer waiting** — no-op. This mirrors
  `OnTimerFired`'s guard (`domain/queue/manager.go:170-173`) and is what makes
  cancel-then-cascade safe in Phase 5: a parent cancelled first is no longer
  waiting, so its dying children cannot resurrect it.
- **Wake matching a different node** — no-op, by the three-part match
  condition.
- **Duplicate wake** (hook plus reconciler) — no-op on the second, for the same
  reason: the parent is no longer in the waiting state.
- **Re-entry while the child is still active** — returns a wait result without
  re-spawning. Never creates a second child.
- **Reconciler query failure at startup** — logged and surfaced; it must not
  prevent the process from starting, matching the posture of the existing
  recovery sweeps.
- **Node-level retry on a spawn node.** A retry re-invokes `Execute`, which
  finds the existing link and short-circuits rather than spawning again — so
  retry semantics on an awaited spawn are idempotent, unlike the
  fire-and-forget case in Phase 3.

## Testing Strategy — STRICT TDD

Tests precede implementation. All five categories are required.

- **Happy path** — a parent reaches an awaited spawn node, goes to waiting with
  a linked child; the child completes; the parent is re-queued, re-enters,
  short-circuits, and follows the `success` edge to the next node.
- **Success** — a failed child routes the parent through `failure`; a
  **cancelled** child routes through `failure` (the spec's explicit
  no-separate-path requirement); the child is enqueued before the parent
  transitions to waiting and `NotifyCapacityFreed` is called after, asserted as
  an ordered sequence because the ordering is the deadlock guard; a node
  execution record is written on the wait path; the reconciler re-queues a
  parent whose child was already terminal at startup; the reconciler running
  after `RecoverCrashedInstances` resumes a parent whose child crashed
  mid-flight.
- **Failure** — a parent whose snapshot lacks the `failure` edge fails at first
  execution and creates **no** child; likewise for a missing `success` edge;
  input preparation failure fails the node without suspending the parent.
- **Error** — the waiting transition fails after the child is enqueued → node
  errors, child survives, parent does not sit in a half-suspended state; the
  re-queue transition fails during wake → error surfaced and the reconciler
  recovers the parent on the next startup; the reconciler's query fails →
  logged without blocking startup.
- **Edge case** — a fire-and-forget child from node *A* reaching a terminal
  state while the parent waits at node *B* does **not** wake the parent (the
  central mis-wake guard); a second awaited spawn node in the same parent is
  matched independently; a parent re-entering while its child is still running
  returns to waiting without creating a second child; a duplicate wake from
  both the hook and the reconciler is a no-op; a child that completes *before*
  its parent's waiting transition commits is still detected — either by the
  short-circuit on re-entry or by the reconciler — and never hangs; an
  **awaited** spawn node revisited through a graph back-edge reads its existing
  terminal link and routes without spawning again, pinning the documented
  loop-body limitation as tested behavior rather than an untested claim; a
  **fire-and-forget** spawn node revisited through a back-edge spawns a new
  child each pass, consistent with the documented loop re-execution semantics
  (`domain/engine/loop_mechanics_test.go:49`); a parent with a per-user
  capacity of one still drains, proving the ordering invariant.
