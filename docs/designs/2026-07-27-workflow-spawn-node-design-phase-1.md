# Workflow Spawn Node — Design Phase 1: Contract & Linkage Foundation

## Purpose

Establish the vocabulary and the data model that every later phase builds on: a
`spawn_workflow` node type with an author-facing contract that validation can
enforce, and a parent→child link on the instance aggregate that the executor,
the wake hook, the cancel cascade, and the hierarchy read all address.

This phase deliberately ships **no runtime behavior**. A workflow containing a
spawn node validates (or is rejected) correctly, and an instance can carry and
be queried by a parent link. The code that *writes* those links arrives in
Phase 3. See "Data Provenance" — this is stated there as an explicit,
named gap, not left to read as if it were already wired.

## Architecture

Three existing seams are extended. No new package is introduced.

| Seam | Existing location | Extension |
| --- | --- | --- |
| Node type vocabulary | `domain/workflow/workflow.go` (`NodeType` constants, lines 9–20) | Add `NodeTypeSpawnWorkflow` |
| Node configuration | `domain/workflow/workflow.go` (`NodeConfig`) | Add spawn-specific optional fields |
| Author-time validation | `domain/workflow/validation.go` (per-type switch, ~line 515) | Add a `validateSpawnWorkflowNode` arm |
| Tool-posture guard | `domain/engine/kind_guard.go` (`expectedKindForNodeType`) | Map the new type to the chat profile kind |
| Instance aggregate | `domain/instance/instance.go` (`Instance`, `InstanceSummary`, `ListFilters`) | Add link fields |
| Instance persistence port | `domain/instance/ports.go` (`InstanceRepository`, `QueueRepository`) | Add link-addressed queries |

The choice to extend rather than introduce follows the codebase's own
statement of intent: `AGENTS.md` names the node-type constants in
`domain/workflow/workflow.go` as the single source of truth shared by
validation, dashboard behavior, and tests, and instructs that no
vendor-specific or parallel node kinds be added.

## Components

### 1. `spawn_workflow` node type and configuration

**One job:** name the capability and carry the author's declared intent for a
single spawn.

The node's configuration surface carries: the **child workflow name**, the
**mode** (fire-and-forget or wait-for-child), an optional **guidance template
file** for input preparation, and a **model profile** for the preparation call.
These reuse `NodeConfig`'s existing optional-field convention — `TemplateFile`
and `ModelProfile` already exist on `NodeConfig` and are reused verbatim rather
than duplicated under spawn-specific names.

The mode vocabulary is a closed, exact-match, lowercase set of exactly two
values — `fire_and_forget` and `wait_for_child` — with a named validity
predicate, mirroring `IsValidAgentBackend`
(`domain/workflow/workflow.go:50-57`) — including its rationale: an
unrecognized value must fail closed at workflow load rather than mis-route or
silently fall back at run time.

Unlike the agent-backend axis, **the empty mode is not valid**. There is no
sensible inherited default: the two modes have opposite control-flow
consequences (the parent continues vs. the parent suspends and requires two
connected ports), so an unset mode is an authoring error, not an inheritance
request.

**Consumers depend on:** the type constant and the mode predicate. Everything
else about the node's configuration is read only by the executor in Phase 3.

### 2. Spawn node validation

**One job:** reject an unauthorable spawn node before it can ever run.

Rules enforced:

- The child workflow name is present and non-empty.
- The mode is one of the two recognized values (no empty, no near-miss).
- **Wait-for-child mode requires both a `success` and a `failure` outgoing
  edge.** This is the spec's hard requirement ("Both wait-for-child paths must
  be connected for the parent workflow to be valid").
- Fire-and-forget mode requires exactly one unnamed outgoing edge, and rejects
  `success`/`failure` ports — an author who wires branch ports in
  fire-and-forget mode has misunderstood the mode, and silently ignoring the
  ports would strand half the graph.
- A spawn node cannot be an entrypoint.

The port-presence check reuses the technique in `validateConditionalEdges`
(`domain/workflow/validation.go:297-330`): build a `(source, output)` set from
`wf.Edges`, then assert each required output has a matching edge. The
entrypoint and edge-count checks mirror `validateWebhookWaitNode`
(`domain/workflow/validation.go:613-625`).

Validation errors follow the established `ValidationError{Code, Message,
NodeID}` shape with new codes in the existing style
(`MISSING_REQUIRED_CONFIG`, plus spawn-specific codes for an invalid mode and a
missing branch edge).

### 3. Tool-posture guard extension

**One job:** ensure the spawn node's LLM call cannot resolve to a
tool-enabled (agent) binding.

`expectedKindForNodeType` (`domain/engine/kind_guard.go:31-38`) returns
`ok=false` for any unmapped node type, which makes `assertBindingKind` a no-op.
A new node type that performs a chat call would therefore **silently skip the
guard**. Because input preparation is a tool-suppressed reasoning call, the
spawn type is mapped to the chat profile kind so a misconfigured profile fails
closed with the existing `ErrBindingKindMismatch`, exactly as `llm_call` does.

This is a targeted fix inside code this feature touches. No other node type's
mapping changes.

### 4. Instance link fields

**One job:** record, on the child, how and by whom it was created.

Three fields are added to the instance aggregate:

- **parent instance id** — nullable; the spawning parent run.
- **parent node id** — nullable; the specific spawn node within that parent.
  This is what makes a link *addressable*: "the child spawned by this node of
  this parent" is the key the Phase 4 resumption short-circuit and wake hook
  both match on.
- **spawn mode** — nullable; how the link was created.

The mode is stored on the link rather than re-derived from the parent's
workflow snapshot for two reasons, both of which are correctness, not
convenience:

1. **The resumption short-circuit looks up by it.** Phase 4's executor asks
   "does an awaited child already exist for `(this parent, this node)`?" to
   decide between spawning, continuing to wait, and routing. The mode is part
   of that key, and it must be answerable without parsing every parent's
   workflow snapshot.
2. **The wake hook filters on it.** A parent can be waiting at spawn node *B*
   while a fire-and-forget child launched earlier from node *A* reaches a
   terminal state. Matching only on "this child's parent is in a waiting state"
   would wrongly re-queue that parent and route it through node *B*'s
   `failure` port. Phase 4 states the full invariant.

**No foreign key** is placed on the parent instance id. This follows the
existing, deliberate precedent for `ScheduleID` on `instance.Instance`
(`domain/instance/instance.go:117-119`), whose comment records the reasoning:
a referenced row may be removed while historical runs remain. A purged parent
must not cascade-delete a child's execution history.

The parent instance id is also surfaced on `InstanceSummary`, which is the
projection the list path and enricher already consume.

### 5. Link-addressed persistence port additions

**One job:** let callers ask link-shaped questions without any caller knowing
how links are stored.

Three capabilities are added to the instance persistence ports:

- **Filter a list by parent.** `instance.ListFilters` gains a parent-instance
  filter with three-state semantics: unset (no filtering), "roots only", and
  "children of a specific parent". Phase 5's lazy hierarchy read depends on all
  three.
- **Batch child counts.** Given a page of instance ids, return the number of
  children per id in one round trip. This mirrors `AggregateByInstances`
  (`domain/instance/ports.go`) exactly, including its contract details: ids
  with no children are simply absent from the result (absence means zero, not
  an error), and an empty input touches no storage. It exists for the same
  reason — the queue list must not issue N+1 queries per page.
- **List a parent's active children.** Returns children in states that can
  still be cancelled, **regardless of spawn mode** — the spec's cancel cascade
  covers every active child, not only awaited ones. Phase 5's cascade consumes
  it.

`ListFilters` carries a standing security contract
(`domain/instance/instance.go:231-244`): `OwnerID`, `Departments`, and
`IncludePublic` are **service-set** from the request identity and must never be
populated from request input. The new parent filter is different in kind — it
is a legitimate caller-supplied *narrowing*, never a widening, and it composes
with (never replaces) the service-set owner/department constraints. Phase 5
states how the read path enforces this.

## Data Flow

This phase has no runtime data flow. The flow it *enables*:

```
author edits workflow
  → workflow.Validate (domain/workflow/validation.go)
      → spawn node contract enforced (mode, child name, required ports)
  → workflow saved

[Phase 3+] executor writes link fields onto the child instance at creation
[Phase 4]  wake hook reads (parent id, parent node id, mode) to match a waiting parent
[Phase 5]  cancel cascade and hierarchy read query by parent id
```

## Data Provenance

| Input this phase's code consumes | Producer that exists today |
| --- | --- |
| Node type / node config values | `workflow.Repository.Load` → `domain/workflow/workflow.go` `Node`/`NodeConfig`, deserialized from `workflow.json` |
| Workflow edge set (for port validation) | `workflow.Workflow.Edges`, same load path; identical to what `validateConditionalEdges` (`domain/workflow/validation.go:297`) already reads |
| Node type → required profile kind | `domain/engine/kind_guard.go:31` `expectedKindForNodeType` (extended here) |
| Resolved binding kind (for the guard) | `provider.ProfileResolver.Resolve` → `provider.ResolvedBinding.Kind`, as consumed at `domain/engine/executor_llm.go:64-77` |
| Instance persistence | `adapter/sqlite` `InstanceRepo`, satisfying `instance.InstanceRepository` (`domain/instance/ports.go`) |

**Producer gap, stated explicitly:** the three link fields have **no producer in
this phase**. Nothing writes a parent instance id, parent node id, or spawn
mode until the `SpawnWorkflowExecutor` lands in **Phase 3**. Until then every
instance carries null links and the new queries return empty results. This is
an accepted, sequenced gap — not a deferral of *whether* the data has a source.

**Deferred to plans:**

- The migration adding the three link columns (next sequential number after
  `adapter/sqlite/migrations/017_workflow_id_refs`), its down-migration, and
  the index supporting parent-keyed lookup.
- Exact column names, types, and nullability.
- The `adapter/sqlite` query definitions under `adapter/sqlite/queries` and the
  regenerated code under `adapter/sqlite/generated`.
- Field-level JSON tags for the new `NodeConfig` fields and the
  `InstanceSummary` parent field.
- Updating existing in-memory/mock repository implementations across
  `domain/engine`'s test fixtures to satisfy the widened ports.

## Integration Seams

- `domain/workflow/workflow.go` — node type constant, config fields, mode
  predicate.
- `domain/workflow/validation.go` — new per-type validation arm in the existing
  switch (~line 515).
- `domain/engine/kind_guard.go` — one new case in `expectedKindForNodeType`.
- `domain/instance/instance.go` — link fields on `Instance`, parent field on
  `InstanceSummary`, parent filter on `ListFilters`.
- `domain/instance/ports.go` — the three link-addressed capabilities.
- `adapter/sqlite` — migration plus repository implementation.
- `AGENTS.md` lists the node-type vocabulary and instructs that the code
  constants are the vocabulary of record; the new type is added to that list so
  future work uses the correct name.

## Error Handling

- **Author-time contract violations** are `ValidationError` values returned in
  a batch by the existing validation entrypoint, not exceptions. An author sees
  every problem with their spawn node at once, consistent with every other node
  type.
- **Unknown mode fails closed.** An unrecognized or empty mode is a hard
  validation error. It is never coerced to a default. The reasoning is recorded
  in `validateAgentBackend`'s comment
  (`domain/workflow/validation.go:547-554`) and applies with more force here,
  because the two modes differ in whether the parent suspends.
- **Binding kind mismatch fails closed** with the existing
  `ErrBindingKindMismatch`, before any provider is selected.
- **Snapshot tolerance.** Instances execute from `WorkflowSnapshot`, which may
  predate this validation. Phase 3's executor therefore re-checks the mode at
  execution time rather than trusting that validation ran — the same defensive
  posture `kind_guard.go:12-21` documents for binding kinds.
- **Null links are normal, not an error.** Every instance created before this
  feature, and every manually triggered run after it, has null links. Read
  paths treat null as "this is a root run".

## Testing Strategy — STRICT TDD

Tests are written before implementation. All five scenario categories are
required in this phase.

- **Happy path** — a workflow with a wait-for-child spawn node wired to both
  `success` and `failure` targets validates clean; a fire-and-forget spawn node
  with one unnamed outgoing edge validates clean.
- **Success** — the mode predicate accepts exactly the two recognized values;
  the kind-guard maps the spawn type to the chat kind; an instance round-trips
  its three link fields through the repository; a parent-filtered list returns
  only that parent's children; batch child counts return one entry per parent
  that has children.
- **Failure** — a wait-for-child node missing the `failure` edge is rejected; a
  node missing the `success` edge is rejected; a node with no child workflow
  name is rejected; a fire-and-forget node carrying branch ports is rejected; a
  spawn node marked as entrypoint is rejected. Each asserts the specific error
  code and the offending node id, not merely that *some* error occurred.
- **Error** — a repository failure on each new query surfaces as a wrapped
  error rather than an empty result silently indistinguishable from "no
  children"; a spawn node whose resolved binding is agent-kind fails with
  `ErrBindingKindMismatch` under `errors.Is`.
- **Edge case** — an empty mode is rejected (not treated as inherit); a
  wrong-case mode (`Wait_For_Child`) is rejected, matching the exact-match
  discipline `IsValidAgentBackend` establishes; batch child counts with an
  empty id slice returns an empty result without touching storage; a parent id
  with zero children is absent from the count map rather than mapped to zero; a
  null-link instance is reported as a root; the parent filter composes with —
  and cannot override — a service-set owner constraint.

A totality test over the mode vocabulary guards against a third mode being
added without extending validation, following the pattern
`instance.VisibilityFromWorkflow` uses (`domain/instance/instance.go:74-80`),
where a totality test is explicitly preferred over a defaulting switch that
would silently downgrade an unknown value.
