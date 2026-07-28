# Workflow Spawn Node — Design Phase 5: Recursive Cancellation and Run Hierarchy

## Purpose

Make the parent/child relationship operable and visible: cancelling a parent
takes down its awaited children recursively, and users see child runs grouped
under their parent in a recursive expandable hierarchy instead of a flat list
of disconnected runs.

This is the phase that delivers the spec's operator-facing outcomes — inspect,
retry, resume, or cancel at the child level, and understand a complex run as a
hierarchy.

## Architecture

Two independent capabilities sharing the Phase 1 link model:

1. **Cancel cascade** — an extension to `Engine.Cancel` (`engine.go:1385`).
2. **Hierarchy read** — a lazy, one-level-per-request read built on the
   existing `ListInstances` authorization boundary, surfaced through the REST
   and MCP list paths and rendered by the dashboard queue view.

The read path deliberately adds **no new authorization surface**. Every level
of the hierarchy is fetched through `Engine.ListInstances`, which service-sets
the owner and department constraints from the request identity
(`engine.go:479`) and whose filter type carries an explicit standing warning
that callers must never populate those fields from request input
(`domain/instance/instance.go:231-244`). A hierarchy built from repeated calls
to that method inherits its guarantees at every depth for free.

## Components

### 1. Cancel cascade

**One job:** when a run is cancelled, cancel the awaited children it is
responsible for.

Extends `Engine.Cancel`, which today authorizes, transitions by current state,
deactivates wait resources, publishes an event, and records an intervention.
The cascade runs **after** the parent's own transition completes.

**Ordering is a correctness requirement, not a style preference.** Phase 4's
wake only re-queues a parent that is still in the waiting state. Cancelling the
parent first means it is already cancelled by the time its children reach their
terminal state, so the wake is a no-op and the parent cannot be resurrected
through its own `failure` port by the very children being torn down. Reversing
the order produces a parent that flips from cancelled back to queued.

**Scope of the cascade: awaited children only.** Children linked with
fire-and-forget mode are left running. This is a deliberate product decision:
the parent explicitly disclaimed the child's lifecycle when the author chose
that mode, so the child is an independent run that happens to record where it
came from. Awaited children are the opposite — the parent is blocked on them,
and leaving them running after the parent is gone would burn queue capacity and
workspace on work no one will consume. The mode is read from the link column
added in Phase 1, which exists in part for exactly this filter.

**Recursion** proceeds depth-wise through awaited links. The link graph is
acyclic by construction — a child is always created after its parent, so no
instance can be its own ancestor. The traversal nonetheless carries a visited
set, so corrupt or hand-edited data degrades into a halted traversal rather
than unbounded recursion.

**Authorization** is satisfied at every level without special-casing: children
inherit the parent's owner (Phase 3), so an identity authorized to intervene on
the parent is authorized on every descendant. The cascade routes through the
same authorized cancellation path rather than writing states directly, so
intervention audit records are produced for each cancelled run — preserving the
audit trail `recordIntervention` (`engine.go:181`) maintains.

**Partial failure is tolerated, not fatal.** A child that cannot be cancelled —
already terminal, or a transient storage error — is logged and the traversal
continues. The parent's own cancellation has already succeeded and must not be
reported as failed because a descendant was uncooperative. A child already in a
terminal state is the common case and returns the existing `ErrCannotCancel`
(`domain/instance/instance.go:26`), which the cascade treats as success.

### 2. Hierarchy read

**One job:** answer "what are this run's children?" for one level at a time.

Built from three pieces added in Phase 1: the parent filter on `ListFilters`,
the parent field on `InstanceSummary`, and the batched child-count query.

- **The top-level list returns roots only** — instances with no parent link.
- **Expanding a row fetches that row's children** in one request.
- **Recursive expansion is recursive fetching.** Depth is bounded by what the
  user opens.

The alternative — a recursive query returning the whole tree flat, assembled
client-side — was rejected because it breaks pagination: a child can land on a
different page than its parent, so the tree renders with holes whose cause is
invisible to the user. Lazy fetching keeps every page internally coherent.

**Child counts are batched, never N+1.** The count query resolves a whole
page's expander state in one round trip, mirroring `AggregateByInstances` and
`UsersByID` (`domain/instance/ports.go`), which exist for precisely this reason
— their contracts note that the list enricher uses them to avoid a per-row
load. The counts flow through the existing `InstanceEnricher`
(`domain/engine/enricher.go`), which is already the place list rows are
decorated with derived presentation data.

**Counts are computed post-authorization.** A count must reflect children the
*caller* may see, not all children that exist. Otherwise an expander appears
for a run whose children are invisible, and expanding it yields an empty list —
leaking the existence of runs the caller cannot view. This is the same
non-disclosure discipline the shared-workflow visibility filter enforces
(`domain/workflow/resolve.go:29-32`).

### 3. API surface

**One job:** expose the parent filter through the existing list endpoints
without widening what a caller may see.

Both the REST list handler (`adapter/http/instance_handler.go:359`) and the MCP
`instance_list` tool accept an optional parent selector with the same
three-state semantics as the domain filter: unset, roots-only, or a specific
parent. Both continue to route through `Engine.ListInstances` rather than
touching the repository, honoring the boundary `AGENTS.md:73-74` states
directly — service-layer authorization is a boundary, and handlers and MCP
tools must not bypass services when adding read paths.

REST and MCP are kept at parity. The repository already asserts this with a
dedicated parity test (`adapter/mcp/parity_test.go:79`) confirming an MCP
`instance_list` call returns the same scoped set as the REST endpoint; the
parent filter is covered by the same discipline.

### 4. Dashboard hierarchy

**One job:** render the run list as an expandable tree.

Extends `dashboard/src/routes/QueueView.tsx`, which today renders a flat list
with no expansion concept. The changes are: roots-only for the top-level
fetch, an expander control on rows reporting a non-zero child count, per-row
lazy fetch on expand, and nested rendering that recurses to arbitrary depth.

Child rows retain the full existing per-row action set, which is what delivers
the spec's outcome that operators can retry, inspect, resume, or cancel at the
child level rather than restarting a larger process — those actions already
exist per instance and need no per-level special-casing.

The workflow graph editor also needs the new node type available in the
palette, with two output ports shown in wait-for-child mode and one in
fire-and-forget mode, rendered through the existing `ArbitronNode`
(`dashboard/src/graph/nodes/ArbitronNode.tsx`) rather than a new node
component — the node-type registry (`dashboard/src/graph/nodes/nodeTypes.ts`)
maps every Arbitron node kind onto that single component today.

## Data Flow

**Cancellation:**

```
Cancel(parent)
   ├─ authorize intervention           (engine.go:574)
   ├─ transition parent → cancelled    (engine.go:1385 switch)
   ├─ deactivate wait resources        (engine.go:1428)
   ├─ publish cancelled event, record intervention
   │
   └─ cascade: list active children of parent WHERE mode = wait_for_child
         └─ for each, recursively Cancel  (visited-set guarded)
              └─ its own awaited children …

  ↳ parent is already cancelled, so each child's terminal wake (Phase 4)
    finds no waiting parent and is a no-op — the parent cannot be resurrected
```

**Hierarchy read:**

```
GET instances (roots only)
   └─ Engine.ListInstances  ── service-set owner/department constraints
        └─ InstanceEnricher ── batched child counts (post-authorization)
             └─ rows with childCount > 0 render an expander

user expands row R
   └─ GET instances (parent = R)
        └─ same authorized path, one level deeper
             └─ recurse on further expansion
```

## Data Provenance

| Input | Producer that exists today |
| --- | --- |
| Parent/child link and spawn mode | Columns added in **Phase 1**, written by the spawner in **Phase 3** |
| Active children of a parent | The active-children query added in **Phase 1** |
| Batched child counts | The batch count query added in **Phase 1** |
| Parent filter on list queries | `instance.ListFilters` parent field added in **Phase 1** |
| Authorized instance list | `Engine.ListInstances` (`engine.go:479`), with owner/department constraints service-set from `identity.FromContext` |
| Enriched list rows | `Engine.ListEnriched` (`engine.go:505`) → `InstanceEnricher` (`domain/engine/enricher.go`) |
| Cancellation transition + authorization | `Engine.Cancel` (`engine.go:1385`) and `authorizeIntervention` (`engine.go:574`) |
| Intervention audit records | `Engine.recordIntervention` (`engine.go:181`) → `instance.InterventionRepository` |
| Already-terminal cancellation signal | `instance.ErrCannotCancel` (`domain/instance/instance.go:26`) |
| REST list endpoint | `InstanceHandler.List` (`adapter/http/instance_handler.go:359`) |
| MCP list tool | `instance_list`, registered in `adapter/mcp` |
| Dashboard run list | `dashboard/src/routes/QueueView.tsx` |
| Dashboard node rendering | `dashboard/src/graph/nodes/ArbitronNode.tsx` via `nodeTypes.ts` |

Every input traces to a concrete producer or to a named earlier phase. There
are no unresolved dependencies.

**Deferred to plans:**

- Query-parameter and MCP argument naming for the parent selector, and how the
  three-state semantics are encoded on the wire.
- The `adapter/sqlite` implementations of the active-children and batch-count
  queries, and whether the count query filters by the caller's visibility in
  SQL or by intersecting with the already-authorized page.
- Dashboard component decomposition for nested rows, expansion state
  management, and CSS-module additions.
- Palette entry, port labels, and any editor-side configuration form for the
  spawn node.
- Running `make ui` so `adapter/dashboard/dist` stays in sync with the embedded
  SPA, per `AGENTS.md:28`.
- Whether the hierarchy is also surfaced on the single-instance detail view;
  this design covers the list view only.

## Integration Seams

- `domain/engine/engine.go` — cascade inside `Cancel`.
- `domain/engine/enricher.go` — child counts on enriched rows.
- `adapter/http/instance_handler.go` — parent selector on the list endpoint.
- `adapter/mcp` — parent selector on `instance_list`, at REST parity.
- `adapter/sqlite` — the two query implementations.
- `dashboard/src/routes/QueueView.tsx` — expandable hierarchy.
- `dashboard/src/graph/nodes/nodeTypes.ts` + `ArbitronNode.tsx` — palette and
  port rendering.
- **Unchanged:** the authorization model. No new authorization path is
  introduced at any level.

## Error Handling

- **A child that cannot be cancelled** — logged, traversal continues. Already
  terminal (`ErrCannotCancel`) is treated as success, since the goal state is
  already reached.
- **Cascade storage failure** — logged; the parent's cancellation still
  succeeds and is reported as such. The parent's state is the caller's
  contract; a descendant's failure to comply is an operational event, not a
  reason to report the requested cancellation as failed. Any child left running
  remains visible in the hierarchy, so the inconsistency is observable rather
  than hidden.
- **Cycle or corrupt link data** — the visited set halts traversal. The
  acyclicity argument makes this unreachable in practice; the guard exists so
  the failure mode is a stopped traversal rather than a stack overflow.
- **Deep hierarchies** — expansion is user-driven and one level per request, so
  depth costs requests, not a single unbounded query. There is no server-side
  depth limit on the read path.
- **Child count unavailable** — the row renders without an expander rather than
  failing the whole page. A missing expander is a degraded but usable list; a
  failed page is not.
- **Expansion fetch fails** — surfaced on that row only; sibling rows and the
  rest of the page are unaffected.
- **Parent filter naming a run the caller cannot see** — returns an empty list,
  identical to a parent with no children. Existence is not disclosed, matching
  the non-disclosure posture used throughout the read paths.

## Testing Strategy — STRICT TDD

Tests precede implementation. Go tests live beside the packages they cover; the
dashboard is covered by Vitest per `AGENTS.md:95-99`. All five categories are
required.

- **Happy path** — cancelling a parent with one awaited child cancels both; the
  top-level list returns roots only; expanding a root returns its children;
  expanding a child returns grandchildren.
- **Success** — the cascade reaches grandchildren and deeper; the parent
  transitions to cancelled **before** any child does, asserted as an ordered
  sequence because the ordering is what prevents resurrection; an intervention
  record is written for every cancelled run; child counts for a whole page
  resolve in a single batched call rather than one call per row; REST and MCP
  return the same scoped set for an identical parent filter, extending the
  existing parity test; a child row exposes the same actions as a root row.
- **Failure** — a fire-and-forget child is **not** cancelled when its parent is
  (the explicit product decision, and the single most important assertion in
  this phase); a child already terminal is skipped without failing the cascade;
  a parent in a state that cannot be cancelled returns `ErrCannotCancel` and
  cascades to nothing.
- **Error** — a storage failure cancelling one child leaves the parent
  cancelled and the operation reported as successful; a child-count query
  failure degrades the row rather than failing the page; an expansion fetch
  failure is confined to its row.
- **Edge case** — a cancelled parent's awaited child reaching a terminal state
  afterwards does **not** re-queue the cancelled parent (the resurrection
  guard, asserted end-to-end across Phase 4 and Phase 5); a run with no
  children renders no expander; a parent whose children are all invisible to
  the caller reports a zero count and no expander, so no existence is leaked; a
  mixed parent with both awaited and fire-and-forget children cancels exactly
  the awaited ones; an instance predating this feature carries null links and
  appears as a root; a hierarchy several levels deep expands correctly at each
  level; a corrupt self-referential link halts traversal instead of recursing
  without bound.
