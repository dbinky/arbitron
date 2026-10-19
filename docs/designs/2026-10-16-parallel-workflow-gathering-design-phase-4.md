# Parallel Workflow Gathering — Design Phase 4: Run Visibility

Spec: `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
Phases: 1 — Definition/Validation/MCP · 2 — Execution core (results only) · 3 — Working-area VCS & file gathering · **4 — Run visibility**

## Goal

Make fan-out, gathering, nested execution and child relationships legible in the workflow
UI, queue live view and individual run view. Show every active node and why each gather
is waiting. Keep independent work visible after parent continuation/completion and retain
ordered outcomes and merge/resolution history for completed runs. No visual editor.

## Existing capabilities and limits

`dashboard/src/routes/ExecutionView.tsx:buildExecutionStates` combines a trace with one
instance cursor. `adapter/http/stream_handler.go:StreamHandler.HandleWebSocket` subscribes
to one instance and closes for terminal instances/events. Neither supplies aggregate live
state for parallel descendants. `useGraphLayout.ts` / `loopLayout.ts` provide compound loop
layout patterns; `hooks/useRunHierarchy.ts` provides authorized, paginated child expansion.
These are reuse points, not producers of group layout or aggregate task states.

## Components

### 1. Authorized aggregate read model

A new domain read service returns the selected instance's graph plus descendant execution
contexts and group panels, recursively for **both branch and workflow tasks**. Each live
node has `(instanceID, nodeID)` identity; group panels have group-run ID and task key.
Child workflows may reuse node IDs and multiple tasks may run the same child workflow;
never flatten these into a map keyed solely by definition node ID.

- States: `pending`, `queued`, `running`, `waiting`, `completed`, `failed`, `skipped`,
  `cancelled`; reflect existing pause as paused/waiting with a reason. Combine owning
  instance state/cursor with its latest trace, with current wait/cancel/failure overriding
  a provisional succeeded wait trace. Unlaunched tasks are queued/not-started at task level;
  never-reached body nodes are pending. A launch error is visible as a task reason.
- Branch-body nodes use their branch instance, with nested groups mapped recursively using
  Phase 1 ownership. Workflow task cards show workflow, task key, child ID/state, active
  node summaries and drill-down; include their descendants' active nodes in the live view.
- Group panels show completion rule, unfinished behavior, gather mode, state, ordered live
  task statuses, progress, blocking/independent flags, outstanding blocking dependencies,
  winner, frozen gathered results/failure details, merge/resolution history and session link.
  Waiting reasons include pending launch/cancellation, task dependencies, file preparation,
  gathering and active resolution; zero outstanding tasks does not imply ready to continue.
- Keep live statuses separate from the immutable decision/final report. A former unfinished
  non-winner can show completed in live inspection while the original gathered snapshot
  still says unfinished. No late result is added to the frozen output.
- Authorize through `domain/engine/engine.go:Engine.GetInstance` for the selected instance
  **and each included descendant**, and equivalent trace access. Shared ownership is not
  sufficient: public/department visibility may differ by child workflow. Redact inaccessible
  descendants' IDs, definitions, results, trace and session details; show a generic hidden
  dependency where needed. Propagate authorization errors without leaking existence.

### 2. HTTP and persistent inspection

Add `GET /instances/{id}/graph-state` through `InstanceHandler` and the authorized domain
service. The response must support nested contexts, source instance/node trace targets,
ordered task panels, a version, and whether any visible descendant is still active.
Add authorized ancestor discovery for active independent descendants to the queue read
service; current root filters do not supply it. Expose parent/group/task/branch identifiers
only after visibility checks; breadcrumb links must also respect parent visibility. Serve terminal parents even while descendants continue.

Phase 2/3 group records retain frozen results, live terminal outcomes, file merge results
and resolution attempts/history links. Do not depend on already-pruned worktrees or only
on transient WebSocket events for completed-run inspection.

### 3. Descendant events and reliable refresh

Extend event delivery with an authorized **subtree subscription**, while preserving existing
per-instance and system-stream contracts. The selected instance and every ancestor view
must receive relevant descendant changes; a root-only subscription would miss an
intermediate parent's view or send it sibling activity. On-wire events identify the source
instance. Deduplicate delivery and keep `SubscribeAll`'s origin identity intact.

No existing bus ancestry producer exists: `EventBus.Publish(instanceID, Event)` has no
ancestry argument, and `adapter/eventbus/eventbus.go:InProcessEventBus` has no repository.
Add a domain publication/subscription bridge or explicit ancestry registration sourced
from persisted links. The exact interface/cache/invalidation is **P4-B**; do not claim the
bus already stamps root IDs. Missing/purged ancestors must degrade without broadening scope.

Authorize each forwarded source via the instance service for EVERY event, including
root events. Do not cache an allow or deny for the lifetime of a subscription; visibility
may change while it is open.
This includes interactive tokens/history/results, which may be more sensitive than task
progress. Route interactive controls by the selected session's owner/identity, never by
whichever child last emitted an event.

Extend `StreamHandler.HandleWebSocket` terminal handling: a terminal descendant must not
close its parent's stream; a terminal parent with active descendants still needs a stream.
Close only once the subscribed subtree is proven inactive, then refetch final state.
Do not silently cap the aggregate at 1,000 contexts and infer inactivity from a prefix.
Storage/read failures that prevent proving inactivity keep refresh alive with unknown
states and warnings; authorization redaction does not expose hidden activity. Likewise,
keep the client subscribed while any included descendant is active. The existing generic
WebSocket hook reconnects unconditionally; base termination/resync on subtree activity.

Refresh aggregate state on emitted `node.*`, `instance.*`, `group.*` and interactive
resolution lifecycle events; remove reliance on nonexistent `node.skipped`,
`variables.updated`, `instance.state_changed` emitters. Existing events do not cover every
queue/wait/skip transition, and the bus drops when buffers fill. Add bounded periodic
refetch while the subtree is active, plus refetch on reconnect, so a dropped final event
or a transition with no emitter cannot leave the last state stale forever.

### 4. Graph rendering and individual run view

Use Phase 1's ownership analysis, rather than a separate reachability interpretation, to
layout nested `groupContainer` boxes alongside loop clusters. Render a group as a fan-out
header and gather footer with synthetic presentation IDs mapped to the same definition
node/group-run. `task` edges leave the header; success/failure leave the footer. Footer
shows rule, progress, winner and file/resolution activity. Render presentation-only sink
connectors into the gather so branch completion is visible without adding manifest edges
that would violate closed bodies or execute branches in the parent cursor.

Workflow-form boxes have ordered task cards and links to child runs. `WorkflowReview`
uses the same layout with static task/child relationships. In `ExecutionView`, selecting a
branch node opens its owning instance's trace; header/footer selection opens the same
group panel. Add queued/cancelled and non-blocking independent styling. Display all active
nodes, rather than allowing a follow-one-node helper to hide other concurrent paths.

### 5. Queue live view and hierarchy

Add/wire the aggregate graph and panels into a queue live-run pane as well as
`ExecutionView`. Updating only an individual run route would miss the spec's queue live
view. Expanded hierarchy rows show branch markers, group/task keys, child relationships,
active state summaries and non-blocking work, using the same authorized DTOs.

`useRunHierarchy` currently caches child levels and caps a page at 100. Refresh open levels
on relevant events/polling and provide pagination/load-more so every fixed task can be
inspected. Keep completed ancestor rows discoverable while they own active independent
work, even when the ordinary queue filter would hide the completed parent. Branch rows
remain non-root; link them from their group rather than losing them under a hidden root.

## Data Provenance

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Graph, node/edge labels and task configuration | Read model / layout | `domain/instance/instance.go:Instance.WorkflowSnapshot`; static definition via `domain/workflow/service.go:Service.Load`; typed config/ownership remains **PLANNED DEPENDENCY P1-A** |
| Per-node attempt history, errors, conversations | Read model / trace panels | `domain/instance/ports.go:NodeExecutionRepository`; `adapter/http/instance_handler.go:InstanceHandler.GetTrace`, `GetNodeTrace` demonstrate current trace reads |
| Instance cursor/state/owner/visibility, retained parent links | Read model / event bridge | `domain/instance/instance.go:Instance` via `InstanceRepository.Get`; migration `018_instance_spawn_link` stores ancestry; new group/task/branch metadata is **P2-C** |
| Group roster, live statuses, progress, winner, frozen results and failure details | Group panels | **PLANNED DEPENDENCY P2-A** group-run repository; do not infer from one parent's cursor |
| Merge progress/reports and resolution session identity/outcomes | Group panels / links | **PLANNED DEPENDENCY P3-B** durable gather records and **P3-C** session association; existing `NodeExecution.LLMConversation` has conversation data but no group/merge identity |
| Visibility decisions for each included or forwarded source | Read service / stream | `domain/engine/engine.go:Engine.GetInstance` (same service used by `InstanceHandler.Get`); child owner inheritance alone is not a visibility decision |
| Aggregate state, namespaced node identities, subtree activity and version | UI / HTTP | **PLANNED DEPENDENCY P4-A** new read service, response DTO and endpoint; derivation uses the concrete repositories above |
| Source instance event and per-instance bus delivery | Bridge | `domain/engine/ports.go:ScopedEvent.InstanceID`, `EventBus`; `adapter/eventbus/eventbus.go:InProcessEventBus.Publish`, `Subscribe`, `SubscribeAll` |
| Group lifecycle events | Refresh | **PLANNED DEPENDENCY P2-A** and **P3-B** new coordinator/gatherer emitters |
| Ancestor routing, authorized subtree subscription, subtree-aware terminal handling | Live views | **PLANNED DEPENDENCY P4-B**: not provided by current bus/stream/client hooks |
| Reconnect signal / periodic refresh scheduling | UI | Existing `dashboard/src/hooks/useRefetchOnReconnect.ts:useRefetchOnReconnect`; bounded active-subtree polling and integration are **P4-C** |
| Layout metrics and loop clusters | Layout | Existing `dashboard/src/graph/useGraphLayout.ts:useGraphLayout`, `graph/loopLayout.ts`; new group layout, cards, connectors and instance-qualified selection are **PLANNED DEPENDENCY P4-C** |
| Queue child lists and expansion | Queue | `dashboard/src/hooks/useRunHierarchy.ts:useRunHierarchy` → authorized `api.instances.list({parent: id})`; queue live aggregate wiring/pagination are **P4-C**; active-descendant ancestor discovery requires new backend reads in **P4-A** |

## Integration and plan obligations

Implement P4-A–C after the Phase 1 ownership, Phase 2 live/frozen record and Phase 3
session/report contracts are available. Register route/wiring in `adapter/http/server.go`
and `cmd/arbitron/main.go`; update dashboard DTOs, client/hooks, graph and both live views.
Run dashboard gates and `make ui` to update embedded assets when implementation ships.
Do not remove unresolved markers merely by referring to a future phase.

## Error handling

Unreadable descendants produce a warning and unknown/unavailable state, not a falsely
pending or completed result. Inaccessible descendants are redacted without revealing
existence details. An unavailable group record cannot be represented as a completed gather.
HTTP errors preserve existing authorization semantics. Event drops/reconnects trigger
resync, and periodic refetch covers silent gaps. Completed history remains available when
resource cleanup removes working directories.

## Testing — STRICT TDD

Write failing Go tests for read models/endpoints/streams and failing Vitest tests for
hooks/layout/components; observe red, implement to green, then refactor.

- **Happy-path:** queue live view and individual view show two running branches, nested
  groups, workflow task cards/active nodes, fan-out/gather connectors and ordered progress.
- **Success:** winner, accurate gather dependencies, interactive activity/link, immutable
  results and merge/resolution outcomes remain inspectable after completion and cleanup.
- **Failure:** failed/cancelled work distinct; launch/finalization/merge failure reasons;
  waiting on resolution despite zero outstanding task dependencies.
- **Error:** unavailable descendant/group data, unauthorized parent or descendant, redaction
  of child tokens/results, dropped last event recovered by polling, reconnect resync.
- **Edge-case:** duplicate node IDs across repeated child workflows, both nesting forms,
  intermediate-parent subscriptions without siblings, descendant terminal does not close
  parent stream, independent work after parent completion stays visible/live in queue,
  paused tasks, cap-limited queued tasks, more than 100 tasks, stale hierarchy cache,
  header/footer trace mapping, static workflow view and existing loop layouts unaffected.

## Plan dependency closure

The [producer/wiring/test map](../plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md) closes
the design IDs above with concrete tasks and orders the nonnumeric prerequisites.
These are planned capabilities, not claims that implementation already exists.
