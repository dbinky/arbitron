# Queue List Visibility — Design Phase 1: Backend list-projection enrichment & run name

> Scope: produce the enriched, per-row data the richer queue table needs, sourced
> from producers that exist today. Architecture-level only — component boundaries,
> contracts, data flow, provenance, error handling, and testing strategy. Exact
> signatures, SQL, and field-level schemas belong to produce-plans.

## Context & existing architecture this phase reuses

This is a Go hexagonal codebase (`domain/` ports, `adapter/` implementations,
`cmd/arbitron` composition root) with a React dashboard. The queue list already
exists end to end; this phase **extends** it rather than inventing a new surface.

Reused / extended seams:

- `domain/instance.InstanceSummary` — the lean list projection returned by
  `instance.InstanceRepository.List(ctx, filters) []InstanceSummary`
  (`domain/instance/ports.go`).
- `domain/engine.Engine.ListInstances` — the **service-layer authorization
  boundary**; it service-sets `filters.OwnerID`, `filters.Departments`,
  `filters.IncludePublic` from caller identity. This phase adds enrichment
  *behind* that boundary, never bypassing it.
- `adapter/http/instance_handler.go` — `InstanceHandler.List`, the `summaryDTO`
  and `toSummaryDTO` mapper, and the per-user dismissal decoration.
- `domain/instance.NodeExecution` + `NodeExecutionRepository`
  (`domain/instance/ports.go`) — authoritative per-node records.
- `domain/engine.FindNodeByID(snapshot, nodeID) (*workflow.Node, error)`
  (`domain/engine/conversation.go`) — already used by the single-instance path to
  inspect the current node.
- `domain/instance.User` (`domain/instance/user.go`) and
  `domain/identity.Identity` / `domain/identity.Mode` — submitter identity and
  local/shared mode.

## Component architecture

Introduce **one new service-layer component**: an *instance-list enrichment step*
that takes an already-authorized page of `InstanceSummary` and returns a richer
projection. It is the only new orchestration unit in this phase; everything else
is a small additive contract on an existing port or DTO.

Single responsibility of each unit:

1. **Enrichment step (service layer).** One job: given the authorized
   `[]InstanceSummary` for a page, compose the additive fields by calling the
   producers below, and return `[]EnrichedInstanceSummary`. It owns *composition
   and degradation policy only* — no business rules of its own. Consumers (HTTP
   handler, MCP) could be changed to call it without touching the producers.
2. **`DeriveRunName` helper (domain, pure).** One job: compute a display run name
   from a workflow name, the submitted variables, the workflow input metadata, and
   the instance id. Pure and deterministic; no I/O. Internals (the ladder) can
   change without consumers caring — they only depend on "give me a name".
3. **`EnrichedInstanceSummary` (domain type).** One job: carry the enriched row
   shape. Additive to `InstanceSummary` (embeds or wraps it), so the lean shape and
   its existing consumers are untouched.
4. **`enrichedSummaryDTO` + mapper (HTTP).** One job: JSON projection of the
   enriched type, following the existing `toSummaryDTO`/`summaryResponse` pattern
   (including the existing `dismissed` decoration).
5. **Node-execution aggregate (port extension).** One job: collapse many
   `NodeExecution` rows per instance into one per-instance aggregate
   (latest model, max attempt, first-started). New read-only method on
   `NodeExecutionRepository`; SQLite adapter implements it as a single grouped
   query. Note: backend is **not** an aggregate output — `NodeExecution` carries
   no backend column; backend is read from the workflow snapshot's relevant
   `NodeConfig` (see Backend in the provenance table and the data flow below).
6. **User batch lookup (existing port, used in batch).** One job: resolve owner
   display name + account identity for the page's distinct `OwnerID`s.

These stay small and explicitly bounded: each has one input set, one output, and
no hidden coupling to the others except through the enrichment step that composes
them.

## Data flow

```
HTTP List request
  → InstanceHandler.List
    → Engine.ListInstances            (authz boundary — UNCHANGED filters logic)
        → InstanceRepository.List      → []InstanceSummary (authorized page)
    → Enrichment step (NEW, service layer):
        • DeriveRunName(workflowName, variables, inputs, id)   per row
        • NodeExecutionRepository.AggregateByInstances(ids)    one call / page
        • User batch lookup by distinct OwnerID                one call / page
        • per row: load WorkflowSnapshot, FindNodeByID(relevantNodeID)
              → .Label (friendly step) and .Config.Backend (backend)
        → []EnrichedInstanceSummary
    → toEnrichedSummaryDTO (+ existing dismissal decoration)
  → JSON response
```

Elapsed time is **not** computed server-side: the client derives `now − CreatedAt`
from the existing `createdAt` field. Last-error content is **not** carried in the
list at all (see Phase 3 — it is lazy-loaded from the existing trace endpoint).

## Data Provenance

Each enriched input traced to the concrete producer that feeds it in production:

| Enriched field | Producer (`file:symbol`) | Status |
| --- | --- | --- |
| Run name | `DeriveRunName` (new, pure) over `instance.Instance.WorkflowName`, `Instance.Variables`, workflow `Inputs []InputField` (via `Instance.WorkflowSnapshot`), `Instance.ID` | ✅ all inputs exist today |
| Submitter display name | `domain/instance.User.Username`, resolved by `Instance.OwnerID` via the existing user repository (batch lookup) | ✅ exists; batch lookup wiring is new |
| Submitter account identity (email/subject) | `domain/instance.User.Email` / `User.Subject` via same lookup | ✅ exists |
| Local vs shared "you" | `domain/identity.Mode` (`local`/`org`/`edge`) from request identity | ✅ exists |
| Friendly current step (label) | `domain/engine.FindNodeByID(Instance.WorkflowSnapshot, nodeID).Label`; `nodeID` = `Instance.CurrentNodeID` (active), `Instance.FailedNodeID` (failed), else empty — terminal (completed/cancelled) rows have no *current* step, so the cell degrades to a placeholder (the spec asks "what step it is currently on", which terminal runs do not have) | ✅ exists (`WorkflowSnapshot` on full `Instance`, not on `InstanceSummary` — see seam below) |
| Model | `domain/instance.NodeExecution.LLMModel` aggregated by `NodeExecutionRepository.AggregateByInstances` (new method) | ✅ producer exists; aggregate method is new |
| Backend | `domain/workflow.NodeConfig.Backend` of the relevant node, read from `Instance.WorkflowSnapshot` via `FindNodeByID(...).Config.Backend` (same snapshot load as the friendly-step label) — **not** from the node-exec aggregate, which has no backend column | ✅ producer exists; configured value may be `BackendUnset` (`""` = inherit), which degrades to an empty cell since the *effective* backend is not persisted on `NodeExecution` |
| Retry count | `domain/instance.NodeExecution.Attempt`, aggregated (max across nodes) | ✅ exists |
| First-started (for optional active-runtime elapsed) | `domain/instance.NodeExecution.StartedAt`, min across nodes (from aggregate) | ✅ exists |
| Elapsed | client-side `now − InstanceSummary.CreatedAt` | ✅ field already present |
| Submitted local time | `InstanceSummary.CreatedAt` (rendered in viewer-local tz client-side) | ✅ exists |
| Queue position | **Provided by Phase 2** (`queue.Manager.QueuePositions`); stamped here onto queued rows | ✅ resolved in Phase 2 |
| Last error | **Not a list field** — `instance.NodeExecution.Error` via existing `GetNodeTrace(FailedNodeID)`, consumed in Phase 3 | ✅ existing endpoint |

There are **no `UNRESOLVED DEPENDENCY` items** in this phase: every enriched input
traces to an existing producer. The genuinely-aggregated fields
(model/retry/first-started) all derive from existing `NodeExecution` columns via
the new read-time aggregate method; backend is the one model/backend axis that is
**not** aggregated — it is read from the relevant node's `NodeConfig.Backend` in
the already-loaded `WorkflowSnapshot` (and may be empty when the node inherits its
backend).

### Key seam: snapshot access in the list path

`Instance.WorkflowSnapshot` exists on the full `Instance` but **not** on the lean
`InstanceSummary`. The friendly-step label is resolved **for every row**
(per the approved decision), so the enrichment step must obtain each listed
instance's snapshot. The per-instance snapshot load is the recognized cost of the
"every row" choice and is bounded/optimized in plans (see Deferred).

### Deferred to plans

- Composition-root wiring (`cmd/arbitron`) of the enrichment step and the user
  repository into `Engine.ListInstances`' read path.
- SQLite implementation of `NodeExecutionRepository.AggregateByInstances`
  (the grouped query) plus its mock for handler/service tests.
- The mechanism + cost control for fetching `WorkflowSnapshot` per listed instance
  (e.g. distinct-snapshot caching across rows that share a workflow), since the
  label is resolved for every row.
- Exact JSON field names/shape of `enrichedSummaryDTO` (additive to `summaryDTO`).
- MCP list path adoption of the same enrichment step (parity with HTTP).

## Integration seams

- **Authorization:** enrichment runs strictly *after* `Engine.ListInstances` has
  constrained the page; it never re-queries unfiltered. No handler/MCP path
  bypasses the service (per AGENTS.md).
- **Additive DTO:** `enrichedSummaryDTO` extends `summaryDTO`; the existing lean
  shape and the `dismissed` decoration remain intact, so current consumers and
  tests keep working.
- **Credential safety:** aggregates expose model *names* and backend identifiers
  only — never connection keys (consistent with the repo's redaction rules).

## Error handling strategy

Enrichment is **best-effort and degrades per field; it never fails the list.**

- Each producer call is isolated. If user lookup, the node-exec aggregate, snapshot
  parse / `FindNodeByID`, or run-name derivation fails for a row, *that field only*
  is omitted/empty and the row still renders with its core summary intact.
- Producer errors are logged server-side; they are not surfaced into the row.
- The endpoint hard-fails only on the conditions it already does today
  (authorization failure, the underlying `InstanceRepository.List` failing).
- `DeriveRunName` never errors: the final ladder rung (short id) always yields a
  value, so a run name is always present.

## Testing strategy (strict TDD — all five categories required)

Tests are written before implementation, near the package changed
(`domain/instance`, `domain/engine`/service, `adapter/http`).

- **Happy-path:** a fully-populated instance enriches to all fields present
  (run name from a `title` var, model/retry from the aggregate, backend and step
  label resolved from the snapshot's relevant node, submitter name).
- **Success (variations):** run-name ladder — each rung exercised: `title`, then
  `name`, then `slug`, then first-required input, then first non-empty input, then
  short id; "you" rendering in local mode vs display name + account identity in
  shared mode.
- **Failure:** a producer returns a domain error (user not found, aggregate query
  fails, snapshot missing a node) → that field degrades to empty, row still
  returned; verify no propagation to the list result.
- **Error:** `InstanceRepository.List` itself errors → endpoint returns the same
  error status it does today (enrichment never reached).
- **Edge-case:** empty page; instance with no node executions (queued — model
  empty, retry 0); relevant node with `BackendUnset` (`""`) → backend cell empty
  (effective backend not persisted); `CurrentNodeID` nil; `Variables` empty/non-string values;
  duplicate `OwnerID`s deduped to one lookup; very long submitted titles.
- **HTTP:** `enrichedSummaryDTO` JSON round-trip including nil-pointer fields and
  the preserved `dismissed` flag, following `instance_handler_test.go` mock
  patterns.
