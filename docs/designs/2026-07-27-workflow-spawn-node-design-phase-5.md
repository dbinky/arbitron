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

**One job:** when a run is cancelled, cancel every active child it spawned,
recursively.

Extends `Engine.Cancel`, which today authorizes, transitions by current state,
deactivates wait resources, publishes an event, and records an intervention.
The cascade runs **after** the parent's own transition completes.

**Ordering is a correctness requirement, not a style preference.** Phase 4's
wake only re-queues a parent that is still in the waiting state. Cancelling the
parent first means it is already cancelled by the time its children reach their
terminal state, so the wake is a no-op and the parent cannot be resurrected
through its own `failure` port by the very children being torn down. Reversing
the order produces a parent that flips from cancelled back to queued.

**Scope of the cascade: every active child, both modes.** The spec states the
requirement without qualification — "Cancelling a parent cancels active
children recursively" — so fire-and-forget children are cancelled alongside
awaited ones.

The tempting reading is that fire-and-forget children should survive, on the
grounds that the parent disclaimed the child's lifecycle. That reading confuses
two different directions. The spec's fire-and-forget carve-outs are all
child→parent: "fire-and-forget child outcomes do not affect the parent after
the child has been spawned", and the matching non-goal. Nothing in the spec
weakens the parent→child direction, and the outcome the cascade serves —
operators tearing down a run tree without hunting for orphans — is defeated
precisely by the children nobody is waiting on, since those are the ones no
later failure will surface. A fire-and-forget child left running after its
parent is cancelled burns queue capacity and a workspace on work whose
requester is gone, and is invisible in the flat list it was cancelled from.

The link's spawn mode is therefore **not** a filter on the cascade. It is read
by Phase 4's wake match and resumption short-circuit; the cascade takes the
whole active-children set.

**Recursion** proceeds depth-wise through all links. The link graph is
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

- **The list query offers a roots-only narrowing** — instances with no parent
  link — as one of the parent filter's three states.
- **Expanding a row fetches that row's children** in one request.
- **Recursive expansion is recursive fetching.** Depth is bounded by what the
  user opens.

**Roots-only is a capability, not a blanket rule for every caller.** A consumer
that lists runs as one undifferentiated set (the REST/MCP API, a future single
"all runs" view) wants roots-only at the top level. The dashboard queue view
does not: it renders one section PER STATE, and the ordinary fire-and-forget
shape is a parent that completes while its child keeps running, so parent and
child land in *different* sections. A roots-only Running section would therefore
drop that child entirely — reachable only by expanding a completed parent inside
the `since`-windowed Completed section. That is precisely the "losing
visibility" this spec rules out, and it defeats the operator outcome
(inspect/retry/resume/cancel at the child level) the hierarchy exists to serve.

So a state-segmented view suppresses a row at top level **only when its parent is
present in the same section** — the only case where the row is genuinely
reachable by expanding its parent. Everything else stays visible where it is.
The rule is one pure function over the page, unit-tested independently of
rendering.

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

Extends the queue list (`dashboard/src/components/QueueSection.tsx`, rendered
per state by `dashboard/src/routes/QueueView.tsx`), which today renders a flat
list with no expansion concept. The changes are: the section-aware top-level
rule above, an expander control on rows reporting a non-zero child count,
per-row lazy fetch on expand, and nested rendering that recurses to arbitrary
depth. The per-state fetches themselves are unchanged.

Child rows retain the full existing per-row action set, which is what delivers
the spec's outcome that operators can retry, inspect, resume, or cancel at the
child level rather than restarting a larger process — those actions already
exist per instance and need no per-level special-casing.

**The graph surface is a read-only visualization, so it needs an accent colour
and nothing else.** `dashboard/src/graph` has no editor: no node palette, no
per-type port rendering, and no editor-side configuration form exists for any
node type (`conditional` has had named output ports far longer than this feature
and still renders through `ArbitronNode`'s single source handle). The node-type
registry (`dashboard/src/graph/nodes/nodeTypes.ts`) maps every Arbitron node kind
onto that one component, so `spawn_workflow` needs only a `typeColors` entry.
Spawn nodes are authored in `workflow.json` and policed by Phase 1's validation,
which is where the two-port contract is actually enforced.

## Data Flow

**Cancellation:**

```
Cancel(parent)
   ├─ authorize intervention           (engine.go:574)
   ├─ transition parent → cancelled    (engine.go:1385 switch)
   ├─ deactivate wait resources        (engine.go:1428)
   ├─ publish cancelled event, record intervention
   │
   └─ cascade: list ALL active children of parent (both spawn modes)
         └─ for each, recursively Cancel  (visited-set guarded)
              └─ its own active children …

  ↳ parent is already cancelled, so each child's terminal wake (Phase 4)
    finds no waiting parent and is a no-op — the parent cannot be resurrected
```

**Hierarchy read:**

```
GET instances (per-state page; parent filter optional)
   └─ Engine.ListInstances  ── service-set owner/department constraints
        └─ InstanceEnricher ── batched child counts (post-authorization)
             └─ top-level rule: hide a row whose parent is on this same page
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
- The exact predicate and placement of the section-aware top-level rule.
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
- `dashboard/src/components/QueueSection.tsx` — expandable hierarchy and the
  section-aware top-level rule.
- `dashboard/src/graph/nodes/ArbitronNode.tsx` — accent colour only.
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

- **Happy path** — cancelling a parent with one awaited child cancels both;
  cancelling a parent with one fire-and-forget child cancels both (the spec's
  unqualified recursive-cancel requirement, and the single most important
  assertion in this phase — it is the one a mode-filtered cascade would fail);
  a `parent=roots` list returns roots only; a row whose parent is on the same
  page renders nested instead of at the top level; expanding a root returns its
  children; expanding a child returns grandchildren.
- **Success** — the cascade reaches grandchildren and deeper; the parent
  transitions to cancelled **before** any child does, asserted as an ordered
  sequence because the ordering is what prevents resurrection; an intervention
  record is written for every cancelled run; child counts for a whole page
  resolve in a single batched call rather than one call per row; REST and MCP
  return the same scoped set for an identical parent filter, extending the
  existing parity test; a child row exposes the same actions as a root row.
- **Failure** — a child already terminal is skipped without failing the cascade;
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
  still-running child whose parent has already completed stays visible in the
  running section rather than being hidden under a finished parent; a
  mixed parent with both awaited and fire-and-forget children cancels all of
  them; a cancelled fire-and-forget child does **not** wake its cancelled
  parent, because the Phase 4 wake requires `mode = wait_for_child`; an instance predating this feature carries null links and
  appears as a root; a hierarchy several levels deep expands correctly at each
  level; a corrupt self-referential link halts traversal instead of recursing
  without bound.
