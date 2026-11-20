# Pause/resume cascade to child runs — design

Date: 2026-11-19 · Branch: `child-state-bug` · Size: small change

## Intent

Pausing a parent run must pause every run it spawned. Seen in the deployed
build: a "pause all" paused a running `feature-pipeline`, but its running
child stage workflow kept going and had to be paused by hand. Cancel already
cascades to descendants (`Engine.Cancel` → `cascadeCancel`); pause and resume
do not (`Engine.Pause` / `Engine.Resume` touch only the named run).

## Goals

- Pausing a run pauses all active descendants (whole subtree, every spawn
  mode: fire-and-forget, wait-for-child, group task).
- Resuming a run resumes only the descendants that **its** pause stopped.
- Works identically from the dashboard, REST, and MCP (lives in the engine).
- Survives a server restart.

## Non-goals

- Pausing a child never pauses its parent.
- No change to cancel, retry, or rerun.
- No new "pause all" bulk endpoint.

## Anti-goals

- Resuming a parent must never undo a pause the operator placed on a child
  deliberately.
- A queued child must not slip past a parent pause and start later.
- A resumed, previously-queued child must not jump the queue.

## Approaches

- **Stored cascade marker on the child run (chosen).** A nullable column on
  `instances` naming the ancestor whose pause stopped this run. Pause walks
  the spawn links (as cancel does) setting it; resume walks and resumes only
  runs whose marker names the resumed run, clearing it. Durable, cheap,
  engine-level.
- Derive "who paused it" from the intervention audit trail — no schema
  change, but turns audit into control state; fragile. Rejected.
- In-memory set of cascade-paused runs — lost on every redeploy/restart.
  Rejected.

User chose resume option **B** (resume only what the cascade paused) over
**A** (resume every paused descendant).

## Behaviour

1. **Pause(X)**: pause X as today, then walk descendants via
   `SpawnLinkRepository.ListChildren`. Each `running` / `waiting` / `queued`
   descendant becomes `paused` with `paused_by_instance_id = X`. Terminal
   descendants are left untouched but traversed. Already-paused descendants
   are left alone (their marker is not overwritten) but traversed. Each
   descendant is re-authorized, as in `cascadeCancel`; a visited set bounds
   link cycles; the walk is detached from request cancellation
   (`context.WithoutCancel`).
2. **Resume(X)**: resume X as today, then walk descendants; each `paused`
   descendant with `paused_by_instance_id = X` has the marker cleared and is
   resumed. Others are left alone but traversed (a cascade-paused grandchild
   below a manually-paused child is still marked with X and is resumed).
3. **Queued children** (cascade only): pausing a queued descendant is allowed
   inside the cascade; on resume it returns to `queued` (keeping its queue
   position semantics), not straight to running. Direct `Pause` of a queued
   run keeps today's rejection.
4. **Child spawned during the pause**: when `Engine.Spawn` creates a child of
   a parent that is already `paused`, the child is created `paused` with the
   marker set to the pausing ancestor (the parent's own marker if it has one,
   else the parent).
5. **Operator pauses an already-paused child**: clears its marker (claims the
   pause) and succeeds instead of returning "not running". Resuming the
   parent then leaves it paused.
6. **Parent paused while awaiting a child that finishes meanwhile**: the
   child-terminal wake is a no-op on a non-waiting parent; on resume the
   spawn node's short-circuit must pick up the finished child's outcome.
   Covered by a regression test.

Interventions on cascaded descendants are recorded through the existing
intervention audit (pause/resume actions).

## Constraints

- Hexagonal layering: marker read/write goes through domain ports
  (`instance` repository), implemented in `adapter/sqlite`; new migration
  `040_*`.
- Avoid blanket `sqlc generate` (hand-curated generated tree).
- Strict TDD.

## Left to the builder

Exact column/port method names, whether the marker write is folded into the
state update, and test placement within `domain/engine` and `adapter/sqlite`.
