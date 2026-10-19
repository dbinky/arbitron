# Parallel Workflow Gathering — Phase 4 Task 6: Dashboard graph-state DTOs, client and `useGraphState`

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give the dashboard typed access to the authorized aggregate run state (`GET /api/v1/instances/{id}/graph-state`) and the static group layout (`GET /api/v1/workflows/{name}/group-layout`), plus one hook, `useGraphState`, that keeps the aggregate fresh through subtree WebSocket events, bounded polling while active, and reconnect resync.
**Architecture:** `api/types.ts` gains TypeScript mirrors of the Phase 1 `workflow.GroupGraph`, Phase 2/3 group records (`GroupDecision`, `GatherReport`, …) and the Phase 4 `GraphState` JSON. `api/client.ts` gains `api.instances.graphState` and `api.workflows.groupLayout`, normalizing Go `null` collections to empty ones at the boundary. `hooks/useInstanceWebSocket.ts` gains an opt-in `subtree` mode (`?subtree=1`), and the new `hooks/useGraphState.ts` combines initial fetch + debounced subtree events + 5 s polling only while `active` + `useRefetchOnReconnect`, applying responses in request order.
**Tech Stack:** React 19, TypeScript 6, Vitest 4 + @testing-library/react 16 (fake timers, `vi.stubGlobal` for `fetch`/`WebSocket`).
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Workflow and run visibility": every concurrently active node; why a gather waits); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §3 "Descendant events and reliable refresh" (refresh on emitted `node.*`/`instance.*`/`group.*`/interactive lifecycle; bounded periodic refetch while the subtree is active; refetch on reconnect; keep the client subscribed while any descendant is active) and Data Provenance row "Reconnect signal / periodic refresh scheduling" (P4-C).

**Interfaces**
- Consumes (existing code):
  - `dashboard/src/api/client.ts:37` `async function request<T>(url: string, init?: RequestInit): Promise<T>` — throws `Error("API <status>: <message>")` on non-2xx.
  - `dashboard/src/hooks/useWebSocket.ts:22` `useWebSocket({ url, onEvent }): { connected: boolean; send }` — undefined `url` = no socket; reconnects 3000 ms after a close while `url` is set; an `url` change closes the old socket without reconnecting it.
  - `dashboard/src/hooks/useRefetchOnReconnect.ts:17` `useRefetchOnReconnect(connected: boolean, refetch: () => void)` — fires on every false→true transition after the first connection.
  - `dashboard/src/api/types.ts:2` `InstanceState`, `:284` `WebSocketEvent`.
- Consumes (earlier tasks, JSON only): P4-T1/P4-T2 `engine.GraphState`, `ExecutionContext`, `LiveNodeState`, `GroupPanel`, `GroupPanelTask`, `GroupProgress`; P1-T2 `workflow.GroupGraph`/`GroupScope`/`BodyOwner`/`GroupConfig`/`GroupTask`; P2-T1 `instance.GroupDecision`/`FrozenTask`; P3-T4 `instance.GatherReport`/`GatherTaskReport`/`ResolutionAttempt`/`ResolutionSessionRef`. Field names are the json tags P4-T1 declares on every DTO (including `GroupPanel`/`GroupPanelTask`/`GroupProgress`, which the contract left untagged), plus P4-T1's refinements `ExecutionContext.layout` and `GroupPanel.activeSession` (`groupRunId, instanceId, nodeId, form, completionRule, unfinishedBehavior, gatherMode, state, tasks, progress, waitingReasons, winner, decision, finalOutput, gather`; `key, kind, origin, childInstanceId, liveStatus, frozenStatus, blocking, continuingIndependently, cancelRequested, launchError, redacted`; `total, completed, failed, cancelled, active, notStarted`).
- Produces (exact exported symbols later tasks rely on):
  - `dashboard/src/api/types.ts`: `GroupForm`, `GroupCompletionRule`, `GroupUnfinishedBehavior`, `GroupGatherMode`, `GroupTask`, `GroupConfig`, `BodyOwner`, `GroupScope`, `GroupGraph`, `GroupTaskKind`, `GroupRunState`, `GroupTaskStatus`, `FrozenTask`, `GroupDecision`, `GatherTaskReport`, `ResolutionAttempt`, `ResolutionSessionRef`, `GatherReport`, `LiveNodeStateValue`, `LiveNodeState`, `ExecutionContextKind`, `ExecutionContext`, `GroupProgress`, `GroupTaskLiveStatus`, `GroupPanelTask`, `GroupPanelState`, `GroupPanel`, `GraphState`; `WebSocketEvent.sourceInstanceId?: string`.
  - `dashboard/src/api/client.ts`: `api.instances.graphState(id: string): Promise<GraphState>`; `api.workflows.groupLayout(name: string): Promise<GroupGraph>`; `export function normalizeGroupGraph(raw: GroupGraph | null | undefined): GroupGraph`; `export function normalizeGraphState(raw: GraphState): GraphState`.
  - `dashboard/src/hooks/useInstanceWebSocket.ts`: `export interface InstanceWebSocketOptions { subtree?: boolean }`; `useInstanceWebSocket(instanceId, onEvent, options?: InstanceWebSocketOptions)`.
  - `dashboard/src/hooks/useGraphState.ts`: `export const GRAPH_STATE_POLL_MS = 5000`; `export const GRAPH_STATE_EVENT_DEBOUNCE_MS = 300`; `export function isGraphStateRefreshEvent(type: string): boolean`; `export interface UseGraphStateOptions { enabled?: boolean }`; `export interface GraphStateHandle { state: GraphState | null; loading: boolean; error: string | null; connected: boolean; refetch: () => Promise<void> }`; `export function useGraphState(instanceId: string | undefined, options?: UseGraphStateOptions): GraphStateHandle`.
  - Semantics later tasks rely on: `state` is `null` until the first successful load and is replaced only by a newer-sequence response; an unchanged `version` keeps the same object; after a failure `state` keeps the last good value and `error` is set; the subtree socket is open while `state === null || state.active` and closed (no reconnect) otherwise; polling runs only while `state.active === true`.

**Wiring & data provenance**
- Composition root (live UI): `useGraphState` is mounted by `dashboard/src/routes/ExecutionView.tsx:ExecutionView` (P4-T8) and by the queue live-run pane `dashboard/src/components/RunLivePane.tsx` rendered from `QueueSection`'s row detail (P4-T10). `api.workflows.groupLayout` is called by `dashboard/src/routes/WorkflowReview.tsx` (P4-T9). `api.instances.graphState` is additionally read by `useRunHierarchy` for hierarchy markers (P4-T10). The SPA is embedded by `make ui` (P4-T10 final step).
- Producers:
  - `GET /api/v1/instances/{id}/graph-state` ← `adapter/http/instance_handler.go:InstanceHandler.GraphState` routed in `adapter/http/server.go` under the existing `r.Route("/api/v1", …)` → `r.Route("/instances", …)` block (server.go:237, :321) (P4-T3) ← `domain/engine/graph_state.go:GraphStateReader.GraphState` (P4-T1) with group panels from `domain/engine/graph_state_groups.go` (P4-T2), constructed in `cmd/arbitron/main.go:initDomain` (P4-T3).
  - `GET /api/v1/workflows/{name}/group-layout` ← `InstanceHandler`/workflow route added by P4-T3 under `r.Route("/workflows", …)` (server.go:270) ← `domain/workflow/group_ownership.go:AnalyzeGroups` (P1-T2).
  - Subtree frames (`/api/v1/instances/{id}/ws?subtree=1`, each with `"sourceInstanceId"`) ← `adapter/http/stream_handler.go:StreamHandler.HandleWebSocket` subtree mode (P4-T5) ← `domain/engine/subtree_events.go:SubtreeEventBridge.SubscribeSubtree` (P4-T4).
  - Event types the hook refreshes on are the EMITTED ones: `node.started|completed|failed` and `instance.completed|failed|cancelled` (`domain/engine/ports.go:242-249`), `instance.created|started|paused|resumed|retrying|recovered|intervened` (engine emitters), `group.launched|task_updated|decided|gathering|conflict|resolved|completed` (P2-T13 / P3-T6), `interactive.ended` (`domain/engine/ports.go:254`).
  - Nonexistent events: `rg -n "node\.skipped|variables\.updated|instance\.state_changed" dashboard/src --glob '!*.test.*'` finds them ONLY in `dashboard/src/routes/ExecutionView.tsx:250,262,269` (no hook references them). That handler is deleted and replaced by `useGraphState` in P4-T8 (exact edit shown there); this task's hook never lists them.
- Provenance probes: `useGraphState — initial load … opens a SUBTREE WebSocket` fails if the hook does not request `/api/v1/instances/<id>/graph-state` or opens a non-subtree stream; `recovers a dropped final event by polling` fails if polling is absent; `reconnects … and refetches on reconnect` fails if `useRefetchOnReconnect` is not wired; `ignores an out-of-order (older) response` fails if sequence ordering is absent. Each was verified to fail when its line is removed.

### Task 6: DTOs, client methods, subtree socket option and `useGraphState`
**Files:**
- Modify: `dashboard/src/api/types.ts:284-290` (add `sourceInstanceId` to `WebSocketEvent`) and append after `:355` (new Phase 4 types)
- Modify: `dashboard/src/api/client.ts:1-16` (imports), insert before `:36` (normalizers), after `:170` (`graphState`), before `:205` (`groupLayout`)
- Modify: `dashboard/src/hooks/useInstanceWebSocket.ts:1-22` (whole file)
- Create: `dashboard/src/hooks/useGraphState.ts`
- Test: `dashboard/src/api/client.test.ts` (append after `:163`)
- Test: `dashboard/src/hooks/useInstanceWebSocket.test.ts` (new)
- Test: `dashboard/src/hooks/useGraphState.test.ts` (new)

#### Cycle 1 — DTOs and client methods

- [x] Step 1: Write the failing client tests. Append to `dashboard/src/api/client.test.ts` (after line 163; the file already imports `describe, it, expect, vi, afterEach` and `api`). The fetch fake is keyed by URL, so a wrong path answers 404 and fails the test:

```ts
describe('api.instances.graphState', () => {
  afterEach(() => vi.restoreAllMocks())

  // Fake keyed by URL: only the graph-state endpoint of the requested instance
  // answers; any other URL is a 404, so a wrong path cannot pass.
  function serve(byUrl: Record<string, unknown>) {
    globalThis.fetch = vi.fn(async (url: string | URL | Request) => {
      const key = String(url)
      if (!(key in byUrl)) {
        return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'no route' }) } as Response
      }
      return { ok: true, json: async () => byUrl[key] } as Response
    }) as typeof fetch
  }

  it('GETs /api/v1/instances/{id}/graph-state and returns the aggregate verbatim', async () => {
    const wire = {
      rootInstanceId: 'r1',
      version: 'v1',
      active: true,
      contexts: [
        { instanceId: 'r1', kind: 'root', state: 'waiting', blocking: true, redacted: false, nodes: [{ instanceId: 'r1', nodeId: 'g', state: 'waiting' }] },
        { instanceId: 'b1', parentInstanceId: 'r1', groupRunId: 'gr1', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [{ instanceId: 'b1', nodeId: 'a1', state: 'running', attempt: 1 }] },
      ],
      groups: [
        {
          groupRunId: 'gr1', instanceId: 'r1', nodeId: 'g', form: 'branches', completionRule: 'all_succeed',
          unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running',
          tasks: [{ key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'b1', liveStatus: 'running', frozenStatus: '', blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false }],
          progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 },
          waitingReasons: ['Waiting for task a'],
        },
      ],
      layout: { groups: { g: { nodeId: 'g', config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' }, bodies: { a: ['a1'] }, nested: [], successTarget: 'done', failureTarget: 'oops' } }, owner: { a1: { groupNodeId: 'g', taskKey: 'a' } }, order: ['g'] },
      warnings: [],
    }
    serve({ '/api/v1/instances/r1/graph-state': wire })

    const out = await api.instances.graphState('r1')

    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/instances/r1/graph-state', undefined)
    expect(out).toEqual(wire)
    expect(out.contexts[1].nodes[0]).toEqual({ instanceId: 'b1', nodeId: 'a1', state: 'running', attempt: 1 })
    expect(out.groups[0].tasks[0].childInstanceId).toBe('b1')
  })

  // Go encodes nil slices/maps as JSON null; every consumer iterates these, so
  // the client normalizes them to empty collections at the boundary.
  it('normalizes Go nulls (contexts, nodes, groups, tasks, waitingReasons, layout collections) to empties', async () => {
    serve({
      '/api/v1/instances/r2/graph-state': {
        rootInstanceId: 'r2',
        version: 'v0',
        active: false,
        contexts: [{ instanceId: 'r2', kind: 'root', state: 'completed', blocking: true, redacted: false, nodes: null }],
        groups: [
          {
            groupRunId: 'gr', instanceId: 'r2', nodeId: 'g', form: 'workflows', completionRule: 'first_success',
            unfinishedBehavior: 'finish_independently', gatherMode: 'results', state: 'done',
            tasks: null, progress: null, waitingReasons: null,
          },
        ],
        layout: { groups: { g: { nodeId: 'g', config: { form: 'workflows', tasks: null, completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results' }, bodies: null, nested: null, successTarget: 's', failureTarget: 'f' } }, owner: null, order: null },
        warnings: null,
      },
    })

    const out = await api.instances.graphState('r2')

    expect(out.contexts[0].nodes).toEqual([])
    expect(out.groups[0].tasks).toEqual([])
    expect(out.groups[0].waitingReasons).toEqual([])
    expect(out.groups[0].progress).toEqual({ total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 })
    expect(out.layout!.groups.g.bodies).toEqual({})
    expect(out.layout!.groups.g.nested).toEqual([])
    expect(out.layout!.groups.g.config.tasks).toEqual([])
    expect(out.layout!.owner).toEqual({})
    expect(out.layout!.order).toEqual([])
    expect(out.warnings).toEqual([])
  })

  it('a null contexts/groups envelope and an absent layout normalize to empties and undefined', async () => {
    serve({ '/api/v1/instances/r3/graph-state': { rootInstanceId: 'r3', version: 'v', active: false, contexts: null, groups: null } })
    const out = await api.instances.graphState('r3')
    expect(out.contexts).toEqual([])
    expect(out.groups).toEqual([])
    expect(out.layout).toBeUndefined()
  })

  // Error: the server's authorization semantics surface unchanged — a hidden
  // instance is a 404 with the server's message, never an empty aggregate.
  it('rejects with the server status and message on a non-2xx response', async () => {
    serve({})
    await expect(api.instances.graphState('hidden')).rejects.toThrow('API 404: no route')
  })
})

describe('api.workflows.groupLayout', () => {
  afterEach(() => vi.restoreAllMocks())

  it('GETs /api/v1/workflows/{name}/group-layout and returns the GroupGraph', async () => {
    const graph = {
      groups: {
        g: { nodeId: 'g', config: { form: 'workflows', tasks: [{ key: 'x', workflow: 'child-a' }], completionRule: 'all_succeed', unfinishedBehavior: 'wait_for_remaining', gatherMode: 'results' }, bodies: {}, nested: [], successTarget: 's', failureTarget: 'f' },
      },
      owner: {},
      order: ['g'],
    }
    globalThis.fetch = vi.fn(async (url: string | URL | Request) =>
      String(url) === '/api/v1/workflows/wf-a/group-layout'
        ? ({ ok: true, json: async () => graph } as Response)
        : ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({}) } as Response),
    ) as typeof fetch

    const out = await api.workflows.groupLayout('wf-a')

    expect(out).toEqual(graph)
    expect(out.groups.g.config.tasks[0].workflow).toBe('child-a')
  })

  it('normalizes an all-null GroupGraph (workflow without groups) to empties', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ groups: null, owner: null, order: null }) }) as Response) as typeof fetch
    const out = await api.workflows.groupLayout('plain')
    expect(out).toEqual({ groups: {}, owner: {}, order: [] })
  })

  it('rejects on a non-2xx response', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'workflow not found' }) }) as Response) as typeof fetch
    await expect(api.workflows.groupLayout('nope')).rejects.toThrow('API 404: workflow not found')
  })
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```
Expected:
```
 FAIL  src/api/client.test.ts > api.instances.graphState > GETs /api/v1/instances/{id}/graph-state and returns the aggregate verbatim
TypeError: api.instances.graphState is not a function
...
 FAIL  src/api/client.test.ts > api.workflows.groupLayout > rejects on a non-2xx response
TypeError: api.workflows.groupLayout is not a function
      Tests  7 failed | 13 passed (20)
```

- [x] Step 3: Add the types. In `dashboard/src/api/types.ts` replace lines 284-290 (`WebSocketEvent`) with:

```ts
/** Domain event streamed over WebSocket. */
export interface WebSocketEvent {
  type: string
  timestamp: string
  data: unknown
  /** Present on system-WS envelopes; identifies the originating instance. */
  instanceId?: string
  /**
   * Present on subtree-mode instance-WS frames (`?subtree=1`); identifies the
   * descendant (or the subscribed instance itself) that emitted the event.
   */
  sourceInstanceId?: string
}
```

Then append to the end of the file (after line 355, the `InteractiveEvent` union):

```ts
// ---------------------------------------------------------------------------
// Parallel groups — static ownership layout (Go workflow.GroupGraph JSON)
// ---------------------------------------------------------------------------

/** Group form: parallel branches of one graph, or awaited child workflows. */
export type GroupForm = 'branches' | 'workflows'

/** Group completion rule. */
export type GroupCompletionRule = 'all_succeed' | 'first_success'

/** What happens to unfinished tasks once a group is decided. */
export type GroupUnfinishedBehavior = 'wait_for_remaining' | 'cancel_unfinished' | 'finish_independently'

/** Whether a group gathers results only, or results plus file changes. */
export type GroupGatherMode = 'results' | 'results_and_files'

/** One fixed task of a group (Go workflow.GroupTask). */
export interface GroupTask {
  key: string
  /** Branches form: entry node id of the task body. */
  entry?: string
  /** Workflows form: child workflow name. */
  workflow?: string
  inputs?: Record<string, unknown>
}

/** A group node's typed configuration (Go workflow.GroupConfig). */
export interface GroupConfig {
  form: GroupForm
  tasks: GroupTask[]
  completionRule: GroupCompletionRule
  unfinishedBehavior: GroupUnfinishedBehavior
  gatherMode: GroupGatherMode
  repoPath?: string
  resolverProfile?: string
  resolverPromptFile?: string
}

/** The immediate (group, task) owning a body node (Go workflow.BodyOwner). */
export interface BodyOwner {
  groupNodeId: string
  taskKey: string
}

/** One group's ownership scope (Go workflow.GroupScope). */
export interface GroupScope {
  nodeId: string
  config: GroupConfig
  /** Absent when the group sits on the main path. */
  parent?: BodyOwner
  /** taskKey -> body node ids in definition order (branches form only; empty for workflows). */
  bodies: Record<string, string[]>
  /** Group node ids nested directly inside this group's task bodies. */
  nested: string[]
  successTarget: string
  failureTarget: string
}

/** Phase 1 ownership analysis of a workflow (Go workflow.GroupGraph). */
export interface GroupGraph {
  /** Keyed by group node id. */
  groups: Record<string, GroupScope>
  /** Body node id -> its IMMEDIATE owning (group, task). */
  owner: Record<string, BodyOwner>
  /** Every group node id in definition order. */
  order: string[]
}

// ---------------------------------------------------------------------------
// Parallel groups — live/frozen run records (Go domain/instance JSON)
// ---------------------------------------------------------------------------

/** Kind of a group task's child run. */
export type GroupTaskKind = 'branch' | 'workflow'

/** Group-run lifecycle state (Go instance.GroupRunState). */
export type GroupRunState =
  | 'launching'
  | 'running'
  | 'decided'
  | 'gathering'
  | 'resolving_conflicts'
  | 'done'

/** Live + frozen task status vocabulary (Go instance.GroupTaskStatus). */
export type GroupTaskStatus =
  | 'not_started'
  | 'queued'
  | 'running'
  | 'waiting'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'unfinished'

/** One task as frozen into the group decision (Go instance.FrozenTask). */
export interface FrozenTask {
  key: string
  kind: GroupTaskKind
  origin: string
  childInstanceId: string
  status: GroupTaskStatus
  result?: Record<string, unknown>
  failure?: Record<string, unknown>
  cancelRequested: boolean
  winner: boolean
}

/** The immutable group decision (Go instance.GroupDecision). */
export interface GroupDecision {
  outcome: 'success' | 'failure'
  rule: string
  behavior: string
  winnerKey?: string
  reason: string
  decidedAt: string
  tasks: FrozenTask[]
}

/** Per-task file-merge outcome (Go instance.GatherTaskReport). */
export interface GatherTaskReport {
  key: string
  merged: boolean
  reason: string
  conflictPaths?: string[]
}

/** One interactive conflict-resolution attempt (Go instance.ResolutionAttempt). */
export interface ResolutionAttempt {
  mergeIndex: number
  attempt: number
  taskKey: string
  conflictPaths: string[]
  /** '' while the attempt is open or ended awaiting verification (Go instance.ResolutionAttempt.Outcome). */
  outcome: 'resolved' | 'still_conflicted' | 'abandoned' | 'cancelled' | 'error' | ''
  startedAt: string
  endedAt?: string
}

/** The live resolution session's identity (Go instance.ResolutionSessionRef). */
export interface ResolutionSessionRef {
  groupRunId: string
  mergeIndex: number
  attempt: number
  /** The instance whose interactive session resolves the conflict (the group's parent). */
  instanceId: string
  /** The group node the session runs at. */
  nodeId: string
}

/** Durable gather/merge report (Go instance.GatherReport). */
export interface GatherReport {
  mode: GroupGatherMode
  state: 'pending' | 'merging' | 'resolving' | 'applied' | 'failed' | 'rolled_back' | 'not_applicable'
  handle?: Record<string, unknown>
  mergeIndex: number
  tasks: GatherTaskReport[]
  attempts: ResolutionAttempt[]
  activeSession?: ResolutionSessionRef
  rollbackStatus?: '' | 'rolled_back' | 'rollback_failed'
  error?: string
}

// ---------------------------------------------------------------------------
// Aggregate run visibility (Go engine.GraphState JSON, GET /instances/{id}/graph-state)
// ---------------------------------------------------------------------------

/** Live state of one node in one instance (Go engine.LiveNodeState.State). */
export type LiveNodeStateValue =
  | 'pending'
  | 'queued'
  | 'running'
  | 'waiting'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'skipped'
  | 'cancelled'
  | 'unknown'

/** One node's live state, identified by (instanceId, nodeId) — never nodeId alone. */
export interface LiveNodeState {
  instanceId: string
  nodeId: string
  state: LiveNodeStateValue
  reason?: string
  attempt?: number
}

/** Position of an execution context in the run tree. */
export type ExecutionContextKind = 'root' | 'branch' | 'workflow' | 'spawn'

/** One instance in the aggregate (Go engine.ExecutionContext). */
export interface ExecutionContext {
  instanceId: string
  parentInstanceId?: string
  groupRunId?: string
  taskKey?: string
  kind: ExecutionContextKind
  workflowName?: string
  state: InstanceState | 'unknown'
  /** False once a decision released the task (independent / non-blocking work). */
  blocking: boolean
  /** The caller may not view this descendant: identity, definition and results are withheld. */
  redacted: boolean
  nodes: LiveNodeState[]
  /** Workflow snapshot; omitted for branch contexts (same graph as the parent). */
  graph?: Record<string, unknown>
  /** Ownership analysis of this context's own graph (workflow/spawn contexts with groups; Go `layout,omitempty`). */
  layout?: GroupGraph
}

/** Task counts of a group (Go engine.GroupProgress). */
export interface GroupProgress {
  total: number
  completed: number
  failed: number
  cancelled: number
  active: number
  notStarted: number
}

/** Live status vocabulary of a panel task (adds "unknown" for unreadable/redacted children). */
export type GroupTaskLiveStatus = GroupTaskStatus | 'unknown'

/** One ordered task row of a group panel (Go engine.GroupPanelTask). */
export interface GroupPanelTask {
  key: string
  kind: GroupTaskKind
  origin: string
  /** Absent/null before launch and for redacted children. */
  childInstanceId?: string | null
  liveStatus: GroupTaskLiveStatus
  /** The status frozen into the decision; "" while undecided. */
  frozenStatus: GroupTaskStatus | ''
  blocking: boolean
  continuingIndependently: boolean
  cancelRequested: boolean
  launchError?: string
  redacted: boolean
}

/** Group-panel state: the group-run state, or "unknown" when the record is unreadable. */
export type GroupPanelState = GroupRunState | 'unknown'

/** One group run's panel (Go engine.GroupPanel). */
export interface GroupPanel {
  groupRunId: string
  /** The instance that executes the group node. */
  instanceId: string
  nodeId: string
  form: GroupForm
  completionRule: GroupCompletionRule
  unfinishedBehavior: GroupUnfinishedBehavior
  gatherMode: GroupGatherMode
  state: GroupPanelState
  /** Author order. */
  tasks: GroupPanelTask[]
  progress: GroupProgress
  /** Human-readable reasons the gather has not continued yet; empty when not waiting. */
  waitingReasons: string[]
  winner?: string
  decision?: GroupDecision
  finalOutput?: Record<string, unknown>
  gather?: GatherReport
  /** The open resolution session, only when it names a visible instance (Go `activeSession,omitempty`). */
  activeSession?: ResolutionSessionRef
}

/** Aggregate graph state of an instance and its authorized descendants (Go engine.GraphState). */
export interface GraphState {
  rootInstanceId: string
  /** Hash of every included state; equal versions mean identical content. */
  version: string
  /** True while any visible context is still non-terminal. */
  active: boolean
  contexts: ExecutionContext[]
  groups: GroupPanel[]
  /** AnalyzeGroups of the root snapshot; absent when the root has no groups. */
  layout?: GroupGraph
  warnings?: string[]
}
```

Notes the implementer must keep: `childInstanceId` on `GroupPanelTask` is optional-and-nullable because the Go field is a `*uuid.UUID` (nil before launch and for redacted children); `frozenStatus` is `''` until the decision exists; `ExecutionContext.graph` is absent for branch contexts (they execute the root snapshot).

- [x] Step 4: Add the client methods. In `dashboard/src/api/client.ts` replace the import block (lines 1-16) with:

```ts
import type {
  DashboardStats,
  EnrichedInstanceSummary,
  GraphState,
  GroupGraph,
  GroupProgress,
  GroupScope,
  Instance,
  InstanceState,
  Me,
  NodeExecution,
  NotificationListResponse,
  Priority,
  QueueConfigUpdate,
  Schedule,
  SystemConfig,
  TeamsConfigUpdate,
  WorkflowStats,
  WorkflowSummary,
} from './types'
import type { WorkflowDefinition } from '../graph/types'
```

Insert immediately above `/** Throws on non-2xx responses with a descriptive message. */` (line 36):

```ts
const EMPTY_PROGRESS: GroupProgress = { total: 0, completed: 0, failed: 0, cancelled: 0, active: 0, notStarted: 0 }

/**
 * Normalizes a Go-encoded workflow.GroupGraph. Go encodes a nil map/slice as
 * JSON null; every consumer iterates these collections, so null becomes the
 * empty collection here, once, at the API boundary.
 */
export function normalizeGroupGraph(raw: GroupGraph | null | undefined): GroupGraph {
  const groups: Record<string, GroupScope> = {}
  for (const [id, scope] of Object.entries(raw?.groups ?? {})) {
    if (!scope) continue
    groups[id] = {
      ...scope,
      config: { ...scope.config, tasks: scope.config?.tasks ?? [] },
      bodies: scope.bodies ?? {},
      nested: scope.nested ?? [],
    }
  }
  return { groups, owner: raw?.owner ?? {}, order: raw?.order ?? [] }
}

/** Normalizes a Go-encoded engine.GraphState (nil slices/maps become empty). */
export function normalizeGraphState(raw: GraphState): GraphState {
  return {
    ...raw,
    contexts: (raw.contexts ?? []).map((c) => ({
      ...c,
      nodes: c.nodes ?? [],
      layout: c.layout ? normalizeGroupGraph(c.layout) : undefined,
    })),
    groups: (raw.groups ?? []).map((g) => ({
      ...g,
      tasks: g.tasks ?? [],
      waitingReasons: g.waitingReasons ?? [],
      progress: g.progress ?? EMPTY_PROGRESS,
    })),
    layout: raw.layout ? normalizeGroupGraph(raw.layout) : undefined,
    warnings: raw.warnings ?? [],
  }
}

```

In the `instances` object, after `getVariables` (lines 167-170) and before the closing `},` of `instances` (line 172), insert:

```ts

    /**
     * Get the authorized aggregate graph state of an instance and its visible
     * descendants (branch tasks, workflow tasks, spawned children): live node
     * states keyed by (instanceId, nodeId), group panels and the root's group
     * layout. Inaccessible descendants arrive redacted, never omitted.
     */
    graphState(id: string): Promise<GraphState> {
      return request<GraphState>(`/api/v1/instances/${id}/graph-state`).then(normalizeGraphState)
    },
```

In the `workflows` object, immediately above `/** Get usage stats for a workflow over a date range. */` (line 205), insert:

```ts
    /**
     * Get the Phase 1 group ownership analysis (workflow.GroupGraph) of a
     * workflow definition, for the static group layout.
     */
    groupLayout(name: string): Promise<GroupGraph> {
      return request<GroupGraph>(`/api/v1/workflows/${name}/group-layout`).then(normalizeGroupGraph)
    },

```

(The name is interpolated unencoded exactly like the neighbouring `get(name)` / `saveLayout(name)` / `getStats(name)`.)

- [x] Step 5: Run it, confirm it passes; typecheck.

  Confirmed red (7 missing-method failures), then green (20 client tests) and a clean TypeScript check. Pre-commit review: (1) DTO fields checked against Go JSON tags; API scope is unchanged. (2) URL-keyed responses carry nonempty descendant nodes, tasks and ownership data, asserted at the returned-client sink; wrong URLs fail. (3) Normalizers and requests follow neighboring client conventions. Reran client tests and typecheck, green.

```bash
cd dashboard && npx vitest run src/api/client.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  20 passed (20)
```
(`tsc` prints nothing.)

- [x] Step 6: Commit.

```bash
git add dashboard/src/api/types.ts dashboard/src/api/client.ts dashboard/src/api/client.test.ts
git commit -m "feat(dashboard): graph-state and group-layout DTOs and client methods" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — subtree mode for the per-instance socket

- [x] Step 7: Write the failing test. Create `dashboard/src/hooks/useInstanceWebSocket.test.ts`:

```ts
import { renderHook } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useInstanceWebSocket } from './useInstanceWebSocket'

const opened: string[] = []

class RecordingWebSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(url: string) {
    opened.push(url)
  }
  addEventListener() {}
  send() {}
  close() {}
}

beforeEach(() => {
  opened.length = 0
  vi.stubGlobal('WebSocket', RecordingWebSocket)
})
afterEach(() => vi.unstubAllGlobals())

describe('useInstanceWebSocket', () => {
  // Regression guard: interactive sessions keep the plain per-instance stream.
  it('connects to the plain per-instance stream by default', () => {
    renderHook(() => useInstanceWebSocket('i-1', () => {}))
    expect(opened).toEqual([`ws://${window.location.host}/api/v1/instances/i-1/ws`])
  })

  it('connects to the subtree stream when subtree is set', () => {
    renderHook(() => useInstanceWebSocket('i-1', () => {}, { subtree: true }))
    expect(opened).toEqual([`ws://${window.location.host}/api/v1/instances/i-1/ws?subtree=1`])
  })

  it('opens nothing without an instance id', () => {
    renderHook(() => useInstanceWebSocket(undefined, () => {}, { subtree: true }))
    expect(opened).toEqual([])
  })
})
```

- [x] Step 8: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/hooks/useInstanceWebSocket.test.ts
```
Expected:
```
 FAIL  src/hooks/useInstanceWebSocket.test.ts > useInstanceWebSocket > connects to the subtree stream when subtree is set
AssertionError: expected [ Array(1) ] to deeply equal [ Array(1) ]
      Tests  1 failed | 2 passed (3)
```

- [x] Step 9: Replace the whole of `dashboard/src/hooks/useInstanceWebSocket.ts` (lines 1-22) with:

```ts
import { useMemo } from 'react'
import type { WebSocketEvent } from '../api/types'
import { useWebSocket } from './useWebSocket'

/** Options for the per-instance WebSocket. */
export interface InstanceWebSocketOptions {
  /**
   * Subscribe to the instance's whole authorized SUBTREE (`?subtree=1`):
   * events of every visible descendant arrive too, each stamped with
   * `sourceInstanceId`. Off by default — the plain per-instance stream (used by
   * interactive sessions) is unchanged.
   */
  subtree?: boolean
}

/**
 * Connects to the per-instance WebSocket at /api/v1/instances/:id/ws
 * (or /api/v1/instances/:id/ws?subtree=1 in subtree mode).
 * Auto-reconnects on disconnect after a 3-second delay; pass an undefined
 * instanceId to close the socket and stop reconnecting.
 *
 * @returns An object with the current connection state and a send function.
 */
export function useInstanceWebSocket(
  instanceId: string | undefined,
  onEvent: (event: WebSocketEvent) => void,
  options: InstanceWebSocketOptions = {},
) {
  const subtree = options.subtree === true
  const url = useMemo(() => {
    if (!instanceId) return undefined
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const base = `${protocol}//${window.location.host}/api/v1/instances/${instanceId}/ws`
    return subtree ? `${base}?subtree=1` : base
  }, [instanceId, subtree])

  return useWebSocket({ url, onEvent })
}
```

The two existing callers (`routes/ExecutionView.tsx:284`, `hooks/useInteractiveSession.ts:124`) pass two arguments and keep the plain stream unchanged.

- [x] Step 10: Run it, confirm it passes.

  Confirmed the subtree URL assertion failed before implementation; all 32 socket/interactive-session tests and typecheck now pass. Pre-commit review: (1) only the planned opt-in option changes; default callers stay plain. (2) The real useWebSocket receives the actual id and option; the browser constructor records and asserts the final URL. (3) Memo dependencies include the scalar subtree flag and follow neighboring socket hooks; focused tests rerun green.

```bash
cd dashboard && npx vitest run src/hooks/useInstanceWebSocket.test.ts src/hooks/useInteractiveSession.test.ts
```
Expected:
```
 Test Files  2 passed (2)
```

- [x] Step 11: Commit.

```bash
git add dashboard/src/hooks/useInstanceWebSocket.ts dashboard/src/hooks/useInstanceWebSocket.test.ts
git commit -m "feat(dashboard): opt-in subtree mode for the instance WebSocket" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 3 — `useGraphState`

- [x] Step 12: Write the failing hook tests. Create `dashboard/src/hooks/useGraphState.test.ts`. Only browser primitives are faked (`fetch` keyed by URL, `WebSocket` with explicit `open/emit/close`), so the real `useInstanceWebSocket` → `useWebSocket` reconnect timer and the real `useRefetchOnReconnect` run under fake timers:

```ts
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { GraphState, WebSocketEvent } from '../api/types'
import {
  GRAPH_STATE_EVENT_DEBOUNCE_MS,
  GRAPH_STATE_POLL_MS,
  isGraphStateRefreshEvent,
  useGraphState,
} from './useGraphState'

// ---------------------------------------------------------------------------
// Fakes. The REAL useInstanceWebSocket/useWebSocket/useRefetchOnReconnect run;
// only the browser primitives (fetch, WebSocket) are faked, so the subtree URL,
// the reconnect timer and the reconnect resync are all exercised for real.
// ---------------------------------------------------------------------------

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []

  readonly url: string
  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((msg: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  private closeListeners: Array<() => void> = []

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  addEventListener(type: string, fn: () => void) {
    if (type === 'close') this.closeListeners.push(fn)
  }

  send() {}

  /** Server accepted the connection. */
  open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }

  /** Server pushed one frame. */
  emit(event: WebSocketEvent) {
    this.onmessage?.({ data: JSON.stringify(event) })
  }

  /** Connection closed (client close or server drop). */
  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
    for (const fn of this.closeListeners) fn()
  }
}

function latestSocket(): FakeWebSocket {
  const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
  if (!ws) throw new Error('no WebSocket was opened')
  return ws
}

function liveSockets(): FakeWebSocket[] {
  return FakeWebSocket.instances.filter((w) => w.readyState !== FakeWebSocket.CLOSED)
}

interface Deferred {
  resolve: (body: unknown) => void
  reject: (status: number, message: string) => void
}

// fetch fake keyed by URL. Each URL owns a queue of replies; an un-queued call
// to a known URL repeats the last reply (a steady backend). Any other URL is a
// 404 — a hook fetching the wrong path can never pass.
let replies: Record<string, Array<unknown | 'defer'>>
let lastReply: Record<string, unknown>
let deferreds: Deferred[]
const fetchMock = vi.fn()

function graphStateCalls(id: string): number {
  return fetchMock.mock.calls.filter(([url]) => url === `/api/v1/instances/${id}/graph-state`).length
}

function state(over: Partial<GraphState> = {}): GraphState {
  return {
    rootInstanceId: 'root-1',
    version: 'v1',
    active: true,
    contexts: [
      { instanceId: 'root-1', kind: 'root', state: 'running', blocking: true, redacted: false, nodes: [{ instanceId: 'root-1', nodeId: 'a', state: 'running' }] },
    ],
    groups: [],
    warnings: [],
    ...over,
  }
}

const done = (version: string): GraphState =>
  state({
    version,
    active: false,
    contexts: [
      { instanceId: 'root-1', kind: 'root', state: 'completed', blocking: true, redacted: false, nodes: [{ instanceId: 'root-1', nodeId: 'a', state: 'completed' }] },
    ],
  })

const ev = (type: string): WebSocketEvent => ({ type, timestamp: '2026-10-16T00:00:00Z', data: {}, sourceInstanceId: 'branch-1' })

async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
  replies = {}
  lastReply = {}
  deferreds = []
  fetchMock.mockReset()
  fetchMock.mockImplementation((url: string) => {
    const queue = replies[url]
    if (!queue && !(url in lastReply)) {
      return Promise.resolve({ ok: false, status: 404, statusText: 'Not Found', json: async () => ({ message: 'not found' }) })
    }
    const next = queue && queue.length > 0 ? queue.shift() : lastReply[url]
    if (next === 'defer') {
      return new Promise((resolve) => {
        deferreds.push({
          resolve: (body) => resolve({ ok: true, json: async () => body }),
          reject: (status, message) => resolve({ ok: false, status, statusText: 'err', json: async () => ({ message }) }),
        })
      })
    }
    lastReply[url] = next
    if (next instanceof Error) {
      return Promise.resolve({ ok: false, status: 500, statusText: 'Internal Server Error', json: async () => ({ message: next.message }) })
    }
    return Promise.resolve({ ok: true, json: async () => next })
  })
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('WebSocket', FakeWebSocket)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function serve(id: string, ...bodies: Array<unknown | 'defer'>) {
  replies[`/api/v1/instances/${id}/graph-state`] = [...bodies]
}

// ---------- isGraphStateRefreshEvent ----------

describe('isGraphStateRefreshEvent', () => {
  it('accepts emitted node.*, instance.*, group.*, interactive.ended and subtree.inactive events', () => {
    for (const t of ['node.started', 'node.completed', 'node.failed', 'instance.completed', 'instance.paused', 'group.launched', 'group.task_updated', 'group.decided', 'group.resolved', 'interactive.ended', 'subtree.inactive']) {
      expect(isGraphStateRefreshEvent(t)).toBe(true)
    }
  })

  it('rejects chat traffic and malformed types', () => {
    for (const t of ['interactive.token', 'interactive.message', 'interactive.history', 'interactive.error', 'node', 'instance', 'group', '', 'nodes.started']) {
      expect(isGraphStateRefreshEvent(t)).toBe(false)
    }
  })
})

// ---------- happy path ----------

describe('useGraphState — initial load and subtree subscription', () => {
  it('fetches the aggregate for the instance and opens a SUBTREE WebSocket', async () => {
    serve('root-1', state())
    const { result } = renderHook(() => useGraphState('root-1'))
    expect(result.current.loading).toBe(true)

    await flush()

    expect(graphStateCalls('root-1')).toBe(1)
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeNull()
    expect(result.current.state?.version).toBe('v1')
    expect(latestSocket().url).toMatch(/\/api\/v1\/instances\/root-1\/ws\?subtree=1$/)
  })

  it('reports connected once the subtree socket opens', async () => {
    serve('root-1', state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    expect(result.current.connected).toBe(false)

    act(() => latestSocket().open())

    expect(result.current.connected).toBe(true)
  })
})

// ---------- success: event-driven refresh ----------

describe('useGraphState — event-driven refresh', () => {
  it('coalesces a burst of descendant events into ONE debounced refetch', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())

    act(() => {
      latestSocket().emit(ev('node.started'))
      latestSocket().emit(ev('group.task_updated'))
      latestSocket().emit(ev('instance.completed'))
    })
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS - 1)
    expect(graphStateCalls('root-1')).toBe(1)

    await advance(1)

    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v2')
  })

  it('refetches on interactive.ended but ignores streaming chat tokens', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())

    act(() => latestSocket().emit(ev('interactive.token')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS * 2)
    expect(graphStateCalls('root-1')).toBe(1)

    act(() => latestSocket().emit(ev('interactive.ended')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(graphStateCalls('root-1')).toBe(2)
  })

  it('keeps the same state object when the refetched version is unchanged', async () => {
    serve('root-1', state(), state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    const first = result.current.state
    act(() => latestSocket().open())

    act(() => latestSocket().emit(ev('node.completed')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)

    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state).toBe(first)
  })
})

// ---------- error: dropped events and reconnects ----------

describe('useGraphState — bounded polling while active', () => {
  // The bus drops events when a subscriber buffer is full, and several
  // transitions have no emitter at all. Polling is what keeps the view from
  // staying stale forever when the FINAL event is lost.
  it('recovers a dropped final event by polling every GRAPH_STATE_POLL_MS while active', async () => {
    serve('root-1', state(), done('v-final'))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    expect(result.current.state?.active).toBe(true)

    // No WebSocket event arrives for the completion.
    await advance(GRAPH_STATE_POLL_MS - 1)
    expect(graphStateCalls('root-1')).toBe(1)
    await advance(1)

    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v-final')
    expect(result.current.state?.contexts[0].state).toBe('completed')
  })

  it('stops polling and closes the subtree socket once the subtree is inactive', async () => {
    serve('root-1', state(), done('v-final'))
    renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    await advance(GRAPH_STATE_POLL_MS) // poll #1 learns the subtree finished
    expect(graphStateCalls('root-1')).toBe(2)

    await advance(GRAPH_STATE_POLL_MS * 4)

    expect(graphStateCalls('root-1')).toBe(2)
    expect(liveSockets()).toHaveLength(0)
    // No reconnect is scheduled for an intentionally closed, inactive subtree.
    await advance(10_000)
    expect(liveSockets()).toHaveLength(0)
  })

  it('never polls an aggregate that is inactive on first load', async () => {
    serve('root-1', done('v-done'))
    renderHook(() => useGraphState('root-1'))
    await flush()

    await advance(GRAPH_STATE_POLL_MS * 3)

    expect(graphStateCalls('root-1')).toBe(1)
    expect(liveSockets()).toHaveLength(0)
  })

  // Bounded: a slow backend must not accumulate one overlapping request per tick.
  it('skips a poll tick while a previous request is still in flight', async () => {
    serve('root-1', state(), 'defer')
    renderHook(() => useGraphState('root-1'))
    await flush()

    await advance(GRAPH_STATE_POLL_MS) // poll #1 hangs
    expect(graphStateCalls('root-1')).toBe(2)
    await advance(GRAPH_STATE_POLL_MS * 3)

    expect(graphStateCalls('root-1')).toBe(2)
  })
})

describe('useGraphState — reconnect resync', () => {
  it('reconnects a dropped subtree socket while active and refetches on reconnect', async () => {
    serve('root-1', state(), state({ version: 'v2' }))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    const first = latestSocket()
    act(() => first.open())

    act(() => first.close()) // network drop
    expect(result.current.connected).toBe(false)
    await advance(3000) // useWebSocket RECONNECT_DELAY_MS
    const second = latestSocket()
    expect(second).not.toBe(first)
    expect(second.url).toContain('?subtree=1')
    expect(graphStateCalls('root-1')).toBe(1)

    act(() => second.open())
    await flush()

    expect(graphStateCalls('root-1')).toBe(2)
    expect(result.current.state?.version).toBe('v2')
  })
})

describe('useGraphState — stale and failed responses', () => {
  // A slow older response must never overwrite a newer one.
  it('ignores an out-of-order (older) response that resolves after a newer one', async () => {
    serve('root-1', 'defer', 'defer')
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('group.decided')))
    await advance(GRAPH_STATE_EVENT_DEBOUNCE_MS)
    expect(deferreds).toHaveLength(2)

    await act(async () => {
      deferreds[1].resolve(state({ version: 'v-new' }))
      await vi.advanceTimersByTimeAsync(0)
    })
    await act(async () => {
      deferreds[0].resolve(state({ version: 'v-old' }))
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(result.current.state?.version).toBe('v-new')
    expect(result.current.loading).toBe(false)
  })

  it('surfaces an error on the first load and clears it on the next successful refetch', async () => {
    serve('root-1', new Error('boom'), state())
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()

    expect(result.current.error).toBe('API 500: boom')
    expect(result.current.state).toBeNull()
    expect(result.current.loading).toBe(false)

    await act(async () => {
      await result.current.refetch()
    })

    expect(result.current.error).toBeNull()
    expect(result.current.state?.version).toBe('v1')
  })

  it('keeps the last good state when a later refetch fails', async () => {
    serve('root-1', state(), new Error('flaky'))
    const { result } = renderHook(() => useGraphState('root-1'))
    await flush()

    await advance(GRAPH_STATE_POLL_MS)

    expect(result.current.error).toBe('API 500: flaky')
    expect(result.current.state?.version).toBe('v1')
  })
})

// ---------- edge cases ----------

describe('useGraphState — disabled and switching', () => {
  it('does nothing without an instance id', async () => {
    const { result } = renderHook(() => useGraphState(undefined))
    await flush()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
    expect(result.current.loading).toBe(false)
    expect(result.current.state).toBeNull()
  })

  it('does nothing while enabled is false', async () => {
    serve('root-1', state())
    renderHook(() => useGraphState('root-1', { enabled: false }))
    await flush()
    await advance(GRAPH_STATE_POLL_MS * 2)
    expect(fetchMock).not.toHaveBeenCalled()
    expect(FakeWebSocket.instances).toHaveLength(0)
  })

  it('drops the previous aggregate and ignores its late response when the instance changes', async () => {
    serve('root-1', 'defer')
    serve('root-2', state({ rootInstanceId: 'root-2', version: 'r2' }))
    const { result, rerender } = renderHook(({ id }: { id: string }) => useGraphState(id), {
      initialProps: { id: 'root-1' },
    })
    await flush()

    rerender({ id: 'root-2' })
    await flush()
    await act(async () => {
      deferreds[0].resolve(state({ version: 'late-root-1' }))
      await vi.advanceTimersByTimeAsync(0)
    })

    expect(result.current.state?.rootInstanceId).toBe('root-2')
    expect(result.current.state?.version).toBe('r2')
    expect(latestSocket().url).toContain('/instances/root-2/ws?subtree=1')
  })

  it('stops polling and cancels a pending debounce on unmount', async () => {
    serve('root-1', state())
    const { unmount } = renderHook(() => useGraphState('root-1'))
    await flush()
    act(() => latestSocket().open())
    act(() => latestSocket().emit(ev('node.started')))

    unmount()
    await advance(GRAPH_STATE_POLL_MS * 2)

    expect(graphStateCalls('root-1')).toBe(1)
    expect(liveSockets()).toHaveLength(0)
  })
})
```

- [x] Step 13: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/hooks/useGraphState.test.ts
```
Expected:
```
 FAIL  src/hooks/useGraphState.test.ts [ src/hooks/useGraphState.test.ts ]
Error: Failed to resolve import "./useGraphState" from "src/hooks/useGraphState.test.ts". Does the file exist?
 Test Files  1 failed (1)
      Tests  no tests
```

- [x] Step 14: Implement. Create `dashboard/src/hooks/useGraphState.ts`:

```ts
import { useCallback, useEffect, useRef, useState } from 'react'
import type { GraphState, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useInstanceWebSocket } from './useInstanceWebSocket'
import { useRefetchOnReconnect } from './useRefetchOnReconnect'

/** Bounded periodic refetch interval while any visible context is active. */
export const GRAPH_STATE_POLL_MS = 5000

/** Debounce window coalescing a burst of subtree events into one refetch. */
export const GRAPH_STATE_EVENT_DEBOUNCE_MS = 300

const REFRESH_PREFIXES = ['node.', 'instance.', 'group.']

/**
 * Whether a subtree event can change the aggregate graph state. Only EMITTED
 * event families count: node.* (started/completed/failed), instance.*
 * (lifecycle), group.* (launched/task_updated/decided/gathering/conflict/
 * resolved/completed), interactive.ended (a resolution session finishing) and
 * subtree.inactive (P4-T5's final subtree frame, sent right before a normal
 * close: refetching on it settles `active=false` without waiting for a
 * reconnect or the next poll). Streaming chat traffic (interactive.token/message/history/error) never
 * changes graph state and is ignored, so a long conversation does not hammer
 * the endpoint.
 */
export function isGraphStateRefreshEvent(type: string): boolean {
  if (type === 'interactive.ended' || type === 'subtree.inactive') return true
  return REFRESH_PREFIXES.some((p) => type.startsWith(p) && type.length > p.length)
}

export interface UseGraphStateOptions {
  /** When false, nothing is fetched or subscribed. Defaults to true. */
  enabled?: boolean
}

export interface GraphStateHandle {
  /** Latest applied aggregate; null until the first successful load. */
  state: GraphState | null
  /** True until the newest request settles. */
  loading: boolean
  /** Message of the newest failed request; cleared by the next success. */
  error: string | null
  /** Whether the subtree WebSocket is open. */
  connected: boolean
  /** Refetch now (used after user actions such as retry/cancel). */
  refetch: () => Promise<void>
}

/**
 * useGraphState keeps the authorized aggregate graph state of an instance and
 * its descendants fresh. Four refresh sources, because no single one is
 * reliable on its own:
 *
 *  1. the initial fetch;
 *  2. subtree WebSocket events (`?subtree=1`), debounced — the socket stays
 *     open only while the subtree is active (or not yet known), and is closed
 *     (no reconnect) once the aggregate reports `active: false`;
 *  3. bounded polling every GRAPH_STATE_POLL_MS, ONLY while `active` is true —
 *     the bus drops events when buffers fill and several transitions have no
 *     emitter, so a lost final event must not leave the view stale forever. A
 *     tick is skipped while a request is still in flight;
 *  4. a refetch on every reconnect (events emitted while the socket was down
 *     are not replayed).
 *
 * Responses are applied in REQUEST order: each request takes a sequence
 * number and a response older than the last applied one is dropped, so a slow
 * early response can never overwrite a newer state. An unchanged `version`
 * keeps the previous object, so consumers' memos do not churn.
 */
export function useGraphState(
  instanceId: string | undefined,
  options: UseGraphStateOptions = {},
): GraphStateHandle {
  const enabled = (options.enabled ?? true) && Boolean(instanceId)
  const [state, setState] = useState<GraphState | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)

  // issuedRef: last sequence handed out; appliedRef: newest sequence whose
  // response (success or failure) has been applied.
  const issuedRef = useRef(0)
  const appliedRef = useRef(0)
  const inFlightRef = useRef(0)

  const refetch = useCallback(async () => {
    if (!enabled || !instanceId) return
    const seq = ++issuedRef.current
    inFlightRef.current += 1
    try {
      const next = await api.instances.graphState(instanceId)
      if (seq <= appliedRef.current) return
      appliedRef.current = seq
      setState((prev) =>
        prev && prev.version === next.version && prev.rootInstanceId === next.rootInstanceId ? prev : next,
      )
      setError(null)
    } catch (err) {
      if (seq <= appliedRef.current) return
      appliedRef.current = seq
      setError(err instanceof Error ? err.message : 'Failed to load graph state')
    } finally {
      inFlightRef.current -= 1
      if (seq === issuedRef.current) setLoading(false)
    }
  }, [enabled, instanceId])

  const refetchRef = useRef(refetch)
  refetchRef.current = refetch

  // New subject (or re-enabled): forget the previous aggregate, and mark every
  // in-flight response as belonging to the previous subject.
  useEffect(() => {
    appliedRef.current = issuedRef.current
    setState(null)
    setError(null)
    setLoading(enabled)
    if (enabled) void refetch()
  }, [enabled, refetch])

  const active = state?.active === true
  useEffect(() => {
    if (!enabled || !active) return
    const timer = setInterval(() => {
      if (inFlightRef.current > 0) return
      void refetchRef.current()
    }, GRAPH_STATE_POLL_MS)
    return () => clearInterval(timer)
  }, [enabled, active])

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const onEvent = useCallback((event: WebSocketEvent) => {
    if (!isGraphStateRefreshEvent(event.type)) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void refetchRef.current()
    }, GRAPH_STATE_EVENT_DEBOUNCE_MS)
  }, [])
  useEffect(
    () => () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    },
    [],
  )

  // Stay subscribed while the subtree is active or not yet known; an inactive
  // subtree closes the socket (undefined id) so no reconnect loop runs.
  const subscribe = enabled && (state == null || state.active)
  const { connected } = useInstanceWebSocket(subscribe ? instanceId : undefined, onEvent, { subtree: true })
  useRefetchOnReconnect(connected, refetch)

  return { state, loading, error, connected, refetch }
}
```

Design notes the implementer must keep (each is pinned by a test):
- The reset effect sets `appliedRef.current = issuedRef.current` before refetching, so any response still in flight for the previous instance id is treated as stale.
- A poll tick is skipped while `inFlightRef.current > 0` ("bounded"); event-driven and reconnect refetches are never skipped, they are ordered by sequence instead.
- The subtree socket is driven by passing `undefined` as the id once `state.active === false`; `useWebSocket` then closes it without scheduling a reconnect.

- [x] Step 15: Run the hook tests, the whole dashboard suite and the typecheck.

  Red/green evidence: missing-hook import failed before implementation; the original 19 hook tests then passed. Final focused gate: 77 tests (24 graph-state, 21 client, 3 instance-socket, 29 interactive-session); TypeScript passes. Full dashboard gate: 54 files / 572 tests pass. `make ui` passes and refreshes the embedded SPA; the unchanged dependency lock emits npm's 9-vulnerability audit warning, and Vite emits its existing large-chunk advisory. No dependency changes were made.

  Final self-review, in the required order:
  1. Spec compliance: checked exported symbols, Go JSON fields, design refresh semantics and P4-T8/T9/T10 live-UI consumers. Fixed three contract/lifecycle gaps with failing regression tests first: refetch loading, previous-subject hung requests blocking new polling, and old event debounces refetching a new subject. All three are green. HTTP graph-state/group-layout and the subtree bridge already have real production wiring; this slice uses those exact endpoints, while the plan explicitly assigns UI mounting to T8/T10 and static-layout use to T9. No extra options/features were added.
  2. Anti-green-theater: tests run the real client and socket/reconnect hooks with only browser primitives replaced. Added returned-aggregate sink assertions for descendant node identity, redaction, frozen results versus live task state, independent activity after root completion, and final socket shutdown; added descendant-layout/session preservation at the API boundary. Temporarily removed the reconnect-resync call and confirmed its test fails; restored it before final green runs. Nil/empty responses or absent subtree/API wiring cannot satisfy these assertions.
  3. Code quality: reviewed neighboring socket/reconnect/hierarchy hooks and client conventions. Sequence ids in a per-subject in-flight set prevent stale completions from decrementing a new subject's count; cleanup invalidates old responses and cancels debounce timers. Added an older-failure/newer-success ordering assertion, using the deferred error seam. Typecheck, full tests, build and diff checks pass after review.

```bash
cd dashboard && npx vitest run src/hooks/useGraphState.test.ts && npx tsc --noEmit && npx vitest run
```
Expected:
```
 Test Files  1 passed (1)
      Tests  19 passed (19)
...
 Test Files  … passed (no failures)
```

- [x] Step 16: Commit.

```bash
git add dashboard/src/hooks/useGraphState.ts dashboard/src/hooks/useGraphState.test.ts
git commit -m "feat(dashboard): useGraphState aggregate refresh (subtree events, bounded polling, reconnect resync)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
