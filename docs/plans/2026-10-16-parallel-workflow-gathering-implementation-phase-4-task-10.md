# Parallel Workflow Gathering — Phase 4 Task 10: Queue live-run pane, hierarchy markers/pagination/refresh, ancestor discoverability, `make ui`

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make the queue route (`/`) show every concurrently active node of a selected run across all fanned-out paths, keep its run hierarchy fresh, paged past 100 and annotated (branch, group/task key, non-blocking, active state), keep completed parents that still own active independent work discoverable, and ship it in the embedded SPA.
**Architecture:** A new `components/RunLivePane.tsx` mounts P4-T6 `useGraphState(row.id)` inside `QueueSection`'s existing row-detail slot and renders the P4-T8 `GraphView` (aggregate path, instance-qualified selection), P4-T8 `GroupPanel` and an instance-qualified "Active now" list. `useRunHierarchy` gains stale-while-revalidate refresh with per-level request ordering, offset pagination (`loadMore`) and per-child markers derived from the parent's authorized `GET /graph-state` aggregate (new pure module `graph/liveSummary.ts`). `QueueView` adds the P4-T3 ancestor-discovery list read (`activeDescendantAncestors=true`) into the Active section, refetches on `group.*` events, and polls every 10 s while anything is active; `make ui` then refreshes `adapter/dashboard/dist`, pinned by a Go content probe.
**Tech Stack:** React 19, React Router 7, `@xyflow/react` 12, TypeScript 6, Vitest 4 + @testing-library/react 16 (fake `setInterval`, `vi.stubGlobal('WebSocket')`), CSS modules; Go 1.25.5 (`adapter/dashboard` embed test); `make ui`.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Workflow and run visibility": the queue live view reflects every concurrently active node; tasks continuing independently remain visible after the parent continues/completes; independent tasks are distinguished from blocking work); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §5 "Queue live view and hierarchy" (wire the aggregate graph and panels into a queue live-run pane; expanded hierarchy rows show branch markers, group/task keys, child relationships, active state summaries and non-blocking work using the same authorized DTOs; refresh open levels on events/polling; pagination/load-more; completed ancestors discoverable while they own active independent work; branch rows non-root and linked from their group), §3 (bounded periodic refetch, refetch on reconnect), "Integration and plan obligations" (run dashboard gates and `make ui`), Testing (happy: queue live view shows two running branches; edge: duplicate node IDs across repeated child workflows, paused tasks, more than 100 tasks, stale hierarchy cache, independent work after parent completion stays visible/live in queue; error: unavailable descendant/group data, redaction, dropped last event recovered by polling).

**Interfaces**
- Consumes (existing code):
  - `dashboard/src/App.tsx:19` `<Route path="/" element={<QueueView />} />` — the queue route.
  - `dashboard/src/routes/QueueView.tsx:67-332` `QueueView` (`STATE_CHANGE_EVENTS` `:22-32`, `fetchAll` `:88-120`, system-WS handler `:171-202`, `useRefetchOnReconnect(connected, fetchAll)` `:207`, sections `:252-312` each given `refreshSignal={refreshedAt}` and `registerRowPatcher`).
  - `dashboard/src/components/QueueSection.tsx:83` `useRunHierarchy()`, `:89-92` `invalidate()` on every `refreshSignal` bump, `:94-95` `patchRow` registration, `:169` `topLevelRows(sorted)`, `:238-260` `<HierarchyRows … renderDetail={(inst) => <RowDetailPanel …/>} />`.
  - `dashboard/src/components/HierarchyRows.tsx:42-159` `HierarchyRows(props)` (recursive; `renderDetail` reaches every depth).
  - `dashboard/src/components/QueueTableRow.tsx:38-164` `QueueTableRow({ row, columns, …, depth, hierarchyExpanded, onToggleHierarchy })` (`data-row-id`, `data-depth`, cells `td[data-col]`, first column is the tree cell).
  - `dashboard/src/components/topLevelRows.ts:25-42` `topLevelRows(rows)` — suppresses a row at top level only when its parent is in the SAME section.
  - `dashboard/src/hooks/useRunHierarchy.ts:21` `CHILD_PAGE_LIMIT = 100`, `:23-45` `RunHierarchy`, `:94-112` `fetchLevel` (`api.instances.list({ parent, limit, includeDismissed: true })`), `:131-146` `invalidate`.
  - `dashboard/src/api/client.ts:19-34` `ListInstancesParams`, `:91-102` `api.instances.list(params)`.
  - Server paging: `adapter/http/instance_handler.go:394-410` (`limit` capped at `sharedWindowLimit = 500` `:359`, `offset`), `:424` parent scope; order `adapter/sqlite/instance_repo.go:216` `ORDER BY created_at DESC LIMIT ? OFFSET ?`.
  - System events: `adapter/http/system_stream_handler.go:67` `eventBus.SubscribeAll()` forwards every event type (incl. `group.*`) of instances the caller may see (`canSee`, `:118-130`).
  - Embed: `Makefile:27-30` `ui:` (`cd dashboard && npm ci && npm run build`, replace `adapter/dashboard/dist`); `adapter/dashboard/embed.go:10` `//go:embed dist/*`; `adapter/dashboard/export_test.go` `AssetsForTest() fs.FS`.
- Consumes (earlier tasks):
  - P4-T3: list query param `activeDescendantAncestors=true` on `GET /api/v1/instances` (`ListFilters.ActiveDescendantAncestors`) — returns only TERMINAL (`completed|failed|cancelled`) rows that are ancestors, at any depth, of a still-active (`queued|running|waiting|paused`) run visible to the same caller; composes with `parent` and the service-set visibility scope; anything but `true|false|1|0` is a 400. `GET /api/v1/instances/{id}/graph-state` → `engine.GraphState` (404 for an unauthorized/absent root).
  - P4-T5: `GET /api/v1/instances/{id}/ws?subtree=1` frames carry `sourceInstanceId` (consumed only through P4-T6 `useGraphState`).
  - P4-T6 (`dashboard/src/api/types.ts`, `client.ts`, `hooks/useGraphState.ts`): types `GraphState`, `ExecutionContext`, `ExecutionContextKind`, `LiveNodeState`, `LiveNodeStateValue`, `GroupGraph`, `GroupPanel`, `GroupPanelTask`, `WebSocketEvent.sourceInstanceId?`; `api.instances.graphState(id: string): Promise<GraphState>`; `useGraphState(instanceId: string | undefined, options?: UseGraphStateOptions): GraphStateHandle` with `GraphStateHandle { state: GraphState | null; loading: boolean; error: string | null; connected: boolean; refetch: () => Promise<void> }` (subtree socket open while `state === null || state.active`; 5 s polling only while `active`; reconnect resync; unchanged `version` keeps the same object).
  - P4-T7 (`dashboard/src/graph/groupLayout.ts`): rendered ids `groupHeaderId(g) = "group:<g>:header"`, `groupFooterId(g) = "group:<g>:footer"` (reached through `GraphView` → `useGraphLayout`).
  - P4-T8: `export function GraphView({ instance, workflowDef, trace, onNodeClick?, selectedNodeId, graphState?, onSelect? })` in `dashboard/src/routes/ExecutionView.tsx` (`instance: Instance | null`, `workflowDef: WorkflowDefinition | null`, `trace: NodeExecution[]`, `selectedNodeId: string | null`, `graphState?: GraphState | null`, `onSelect?: (s: GraphSelection) => void`); default `GroupPanel({ panel: GroupPanel | null; label?: string; onOpenSession?: (s: ResolutionSessionRef) => void })` in `dashboard/src/components/GroupPanel.tsx` (task rows `data-testid="group-task-row-<key>"`, child link text `open run` → `/instances/<childInstanceId>`, redacted task text `hidden dependency`); in `dashboard/src/graph/aggregateState.ts`: `qualifiedKey(instanceId: string, nodeId: string): string`, `findGroupPanel(gs: GraphState | null, instanceId: string | null, nodeId: string): GroupPanel | null`, `activeNodeLabels(gs: GraphState, instanceId: string, rootDef: WorkflowDefinition): string[]`, `type GraphSelection = { kind: 'node' | 'group'; rfId: string; nodeId: string; instanceId: string | null }`.
- Produces:
  - `dashboard/src/api/client.ts`: `ListInstancesParams.activeDescendantAncestors?: boolean` (sent as `activeDescendantAncestors=true`).
  - `dashboard/src/graph/liveSummary.ts`: `EMPTY_DEFINITION: WorkflowDefinition`; `interface ActiveNodeRef { key; instanceId; nodeId; label; state: LiveNodeStateValue; taskKey: string | null; independent: boolean }`; `interface HierarchyMarker { kind: ExecutionContextKind; groupNodeId: string | null; groupLabel: string | null; taskKey: string | null; blocking: boolean; independent: boolean; activeSummary: string | null }`; `rootDefinitionOf(gs: GraphState | null): WorkflowDefinition | null`; `activeNodesOf(gs: GraphState, rootDef: WorkflowDefinition): ActiveNodeRef[]`; `buildHierarchyMarkers(gs: GraphState, parentId: string): Record<string, HierarchyMarker>`.
  - `dashboard/src/hooks/useRunHierarchy.ts`: `RunHierarchy` gains `hasMore(id): boolean`, `isLoadingMore(id): boolean`, `loadMore(id): Promise<void>`, `markersOf(id): Record<string, HierarchyMarker>`; `invalidate()` now keeps open levels' rows visible while it refetches every loaded page; responses apply in request order per level.
  - `dashboard/src/components/QueueTableRow.tsx`: props `badges?: ReactNode`, `independent?: boolean` (row gets `data-independent="true"` and the `independent` class).
  - `dashboard/src/components/HierarchyRows.tsx`: props `markers?: Record<string, HierarchyMarker>`, `ownsActiveWork?: ReadonlySet<string>`; marker container `data-testid="hierarchy-markers"`, summary `data-testid="hierarchy-active-summary"`; button `Load more child runs` / `Loading more child runs…`.
  - `dashboard/src/components/RunLivePane.tsx`: default `RunLivePane({ instanceId }: { instanceId: string })`; test ids `run-live-pane`, `run-live-error`, `run-live-active`, `run-live-active-item`, `run-live-selection`.
  - `dashboard/src/components/QueueSection.tsx`: prop `ownsActiveWork?: ReadonlySet<string>`; the row detail renders `<RunLivePane instanceId={inst.id} />` below `RowDetailPanel`.
  - `dashboard/src/routes/queueRefresh.ts`: `QUEUE_POLL_MS = 10_000`; `isQueueRefreshEvent(type: string): boolean`.
  - Test fixtures: `dashboard/src/graph/liveSummary.fixtures.ts` (`summary`, `fanoutDef`, `fanoutLayout`, `childWfDef`, `wfGroupDef`, `wfGroupLayout`, `live`, `task`, `panel`, `fanoutContexts`, `fanoutAggregate`, `independentAggregate`, `repeatedChildAggregate`, `HIDDEN_ID`); `dashboard/src/routes/queueWorld.fixtures.ts` (`ListArgs`, `QueueWorld`, `newWorld`, `localAdmin`, `listFrom`, `graphStateFrom`, `sockets`, `StubSocket`).
  - Go: `adapter/dashboard/embed_bundle_test.go` `TestEmbeddedBundle_ContainsParallelGroupVisibility`; refreshed `adapter/dashboard/dist/**`.

**Wiring & data provenance**
- Composition root: the REAL queue route — `dashboard/src/App.tsx:19` mounts `QueueView`; `QueueView` renders four `QueueSection`s; each passes `renderDetail` to `HierarchyRows`, which renders it for the expanded row at ANY depth; this task makes that detail render `<RunLivePane instanceId={inst.id} />`, which calls `useGraphState(instanceId)`. The SPA reaches users only through `adapter/dashboard/dist` embedded by `adapter/dashboard/embed.go:10` and refreshed by `make ui` (Task 10.7).
- Producers (every input):
  - Pane aggregate `graph.state` ← `dashboard/src/hooks/useGraphState.ts:useGraphState` (P4-T6) ← `dashboard/src/api/client.ts:api.instances.graphState` (P4-T6) ← `GET /api/v1/instances/{id}/graph-state` → `adapter/http/instance_handler.go:InstanceHandler.GraphState` (P4-T3) ← `domain/engine/graph_state.go:GraphStateReader.GraphState` (P4-T1, per-descendant `Engine.GetInstance` authz + redaction) + `domain/engine/graph_state_groups.go` panels (P4-T2). The id fed in is the row's `EnrichedInstanceSummary.id` from `api.instances.list` (`InstanceHandler.List`).
  - Pane live refresh ← subtree WS `?subtree=1` (`adapter/http/stream_handler.go`, P4-T5, via `domain/engine/subtree_events.go:SubtreeEventBridge`, P4-T4), 5 s polling and reconnect — all inside `useGraphState` (P4-T6).
  - Pane graph definition ← `rootDefinitionOf(state)` = the selected run's root `ExecutionContext.graph`, set from `inst.WorkflowSnapshot` for every non-branch context by `GraphStateReader.addVisible` (P4-T1).
  - Pane rendering ← `dashboard/src/routes/ExecutionView.tsx:GraphView` (P4-T8) → `dashboard/src/graph/useGraphLayout.ts:useGraphLayout` with `groups/groupLive/nodeContexts` (P4-T7, built by P4-T8 `buildAggregateView`); group detail ← `dashboard/src/components/GroupPanel.tsx` + `aggregateState.ts:findGroupPanel` (P4-T8).
  - Hierarchy rows ← `api.instances.list({ parent, limit: CHILD_PAGE_LIMIT, offset?, includeDismissed: true })` → `InstanceHandler.List` (`instance_handler.go:394-424`) → `Engine.ListEnriched` (authorized).
  - Hierarchy markers ← `api.instances.graphState(parentId)` (same authorized aggregate) → `graph/liveSummary.ts:buildHierarchyMarkers` (this task); kind/taskKey/groupRunId/blocking from P4-T1 `ExecutionContext`, `continuingIndependently` from P4-T2 `GroupPanelTask`, group label from the parent snapshot.
  - Ancestor rows ← `api.instances.list({ activeDescendantAncestors: true, includeDismissed: true })` → `InstanceHandler.List` param parse (P4-T3) → `instance.ListFilters.ActiveDescendantAncestors` → `adapter/sqlite/instance_repo.go:activeDescendantAncestorsCTE` (P4-T3).
  - Queue refresh triggers ← system WS (`adapter/http/system_stream_handler.go:67`, `SubscribeAll`): `instance.*` lifecycle (existing emitters) and `group.launched|task_updated|decided|gathering|conflict|resolved|completed` (P2-T13 / P3-T6 emitters, as listed in P4-T6); bounded `QUEUE_POLL_MS` polling (this task); reconnect resync (`QueueView.tsx:207`, existing).
- Provenance probes (each fails if its input is empty or its wiring is absent):
  - `QueueView live-run pane … shows BOTH concurrently running branch nodes as running in the queue view` and `feeds the pane the expanded row's instance id …` — fail if `RunLivePane` is not mounted in `QueueSection`'s detail, if it is fed an empty/wrong id (the `graphState` fake 404s every id but `root`; `useGraphState('')` never fetches), or if it opens a non-subtree socket.
  - `QueueView — completed ancestors … a completed parent with an active independent branch is still listed …` — fails if `QueueView` drops the `activeDescendantAncestors` read (the list fake answers ancestors only for exactly `{ activeDescendantAncestors: true, includeDismissed: true }`) or does not place the rows in the Active section.
  - `QueueView hierarchy … the 101st fixed task is reachable through load-more` — fails if `offset` is not sent (the fake cuts pages by `offset`).
  - `QueueView — open hierarchy levels refresh … group lifecycle event` / `… bounded polling …` — fail if `group.*` does not refetch or polling is absent.
  - `QueueSection — live-run pane in the row detail` — component-level probe that the detail calls `graphState` with that row's id.
  - `TestEmbeddedBundle_ContainsParallelGroupVisibility` — fails until `make ui` embeds the current build.

### Task 10.1: Client — ancestor-discovery list parameter
**Files:**
- Modify: `dashboard/src/api/client.ts:19-34` (`ListInstancesParams`; P4-T6's import rewrite shifts this block by +3 lines — match on the interface text) and `:91-102` (`api.instances.list`)
- Test: `dashboard/src/api/client.test.ts` (append at the end of the file, after the describes P4-T6 appends after `:163`)

- [x] Step 1: Write the failing client tests. Append to the end of `dashboard/src/api/client.test.ts` (the file imports `describe, it, expect, vi, beforeEach, afterEach` and `api` at `:1-2`):

```ts
describe('api.instances.list — active-descendant ancestor discovery (P4-T10)', () => {
  afterEach(() => vi.restoreAllMocks())

  // The queue keeps a completed parent discoverable while it owns active
  // independent work through P4-T3's list NARROWING; the exact wire name is
  // load-bearing (anything else is a 400 INVALID_FILTER or silently ignored).
  it('sends activeDescendantAncestors=true when requested', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ activeDescendantAncestors: true, includeDismissed: true })
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances?includeDismissed=true&activeDescendantAncestors=true',
      undefined,
    )
  })

  it('composes with the roots scope', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ parent: 'roots', activeDescendantAncestors: true })
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/instances?parent=roots&activeDescendantAncestors=true', undefined)
  })

  // The ordinary per-state queue lists must be byte-for-byte unchanged.
  it('omits the parameter when false or absent', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'running', activeDescendantAncestors: false })
    await api.instances.list({ state: 'running' })
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls
    expect(calls[0][0]).toBe('/api/v1/instances?state=running')
    expect(calls[1][0]).toBe('/api/v1/instances?state=running')
  })
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```
Expected:
```
 FAIL  src/api/client.test.ts > api.instances.list — active-descendant ancestor discovery (P4-T10) > sends activeDescendantAncestors=true when requested
AssertionError: expected "spy" to be called with arguments: [ '/api/v1/instances?includeDismissed=true&activeDescendantAncestors=true', undefined ]
...
 FAIL  src/api/client.test.ts > api.instances.list — active-descendant ancestor discovery (P4-T10) > composes with the roots scope
      Tests  2 failed | … passed
```
(`omits the parameter …` already passes: it pins that the ordinary lists stay unchanged.)

- [x] Step 3: Implement. In `dashboard/src/api/client.ts` replace the `ListInstancesParams` interface (currently `:19-34`) with:

```ts
/** Parameters for listing instances. */
export interface ListInstancesParams {
  state?: InstanceState
  workflowName?: string
  since?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
  /**
   * Narrow by position in the spawn hierarchy: omit for all runs, 'roots' for
   * top-level runs only, or an instance UUID for that run's children. The
   * server treats this as a NARROWING that composes with — and can never
   * replace — the visibility scope it derives from the request identity.
   */
  parent?: string
  /**
   * Ancestor discovery (query `activeDescendantAncestors=true`): narrows the
   * list to TERMINAL runs that are ancestors, at any depth, of a still-active
   * (queued/running/waiting/paused) run the caller may see. The queue uses it
   * to keep a completed parent discoverable while it owns active independent
   * work. Composes with `parent` and the server-set visibility scope.
   */
  activeDescendantAncestors?: boolean
}
```

and in `api.instances.list` (currently `:91-102`) add the parameter after the `parent` line:

```ts
    /** List instances with optional filters. */
    list(params?: ListInstancesParams): Promise<EnrichedInstanceSummary[]> {
      const q = new URLSearchParams()
      if (params?.state) q.set('state', params.state)
      if (params?.workflowName) q.set('workflowName', params.workflowName)
      if (params?.since) q.set('since', params.since)
      if (params?.limit != null) q.set('limit', String(params.limit))
      if (params?.offset != null) q.set('offset', String(params.offset))
      if (params?.includeDismissed) q.set('includeDismissed', 'true')
      if (params?.parent) q.set('parent', params.parent)
      if (params?.activeDescendantAncestors) q.set('activeDescendantAncestors', 'true')
      const qs = q.toString()
      return request(`/api/v1/instances${qs ? `?${qs}` : ''}`)
    },
```

- [x] Step 4: Run it, confirm it passes; typecheck.

```bash
cd dashboard && npx vitest run src/api/client.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
```
(`tsc` prints nothing.)

- [x] Step 5: Commit.

```bash
git add dashboard/src/api/client.ts dashboard/src/api/client.test.ts && git commit -m "feat(dashboard): activeDescendantAncestors list parameter for queue ancestor discovery" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.2: `graph/liveSummary.ts` — instance-qualified active nodes and hierarchy markers
**Files:**
- Create: `dashboard/src/graph/liveSummary.ts`
- Create: `dashboard/src/graph/liveSummary.fixtures.ts` (shared by Tasks 10.3–10.6)
- Test: `dashboard/src/graph/liveSummary.test.ts` (new)

- [x] Step 1: Create the shared fixtures `dashboard/src/graph/liveSummary.fixtures.ts` (pure data; no test runner imports, so `tsc` checks it and the production build never includes it):

```ts
import type {
  EnrichedInstanceSummary,
  ExecutionContext,
  GraphState,
  GroupGraph,
  GroupPanel,
  GroupPanelTask,
  LiveNodeState,
  LiveNodeStateValue,
} from '../api/types'
import type { WorkflowDefinition } from './types'

/** A queue row with every enriched field the queue components read. */
export function summary(o: Partial<EnrichedInstanceSummary> & { id: string }): EnrichedInstanceSummary {
  return {
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2026-10-16T10:00:00Z',
    updatedAt: '2026-10-16T10:00:00Z',
    runName: `run ${o.id}`,
    retryCount: 0,
    ...o,
  }
}

const step = (id: string, type = 'llm_call', label = id.toUpperCase()) => ({ id, type, label, description: '', config: {} })
const asGraph = (d: WorkflowDefinition) => d as unknown as Record<string, unknown>

/** start -> g (branches; a: a1 -> a2, b: b1) -> done / oops (same shape as P4-T8's view fixture). */
export const fanoutDef: WorkflowDefinition = {
  name: 'fanout',
  description: '',
  author: '',
  nodes: [step('start'), step('g', 'parallel_group', 'Fan out'), step('a1'), step('a2'), step('b1'), step('done'), step('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'a1', output: 'task', label: 'a' },
    { source: 'g', target: 'b1', output: 'task', label: 'b' },
    { source: 'a1', target: 'a2' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}

export const fanoutLayout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: {
        form: 'branches',
        tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'b1' }],
        completionRule: 'all_succeed',
        unfinishedBehavior: 'cancel_unfinished',
        gatherMode: 'results',
      },
      bodies: { a: ['a1', 'a2'], b: ['b1'] },
      nested: [],
      successTarget: 'done',
      failureTarget: 'oops',
    },
  },
  owner: { a1: { groupNodeId: 'g', taskKey: 'a' }, a2: { groupNodeId: 'g', taskKey: 'a' }, b1: { groupNodeId: 'g', taskKey: 'b' } },
  order: ['g'],
}

/** The child workflow every task of the workflows-form group runs: node id "build" repeats per task. */
export const childWfDef: WorkflowDefinition = {
  name: 'child-wf',
  description: '',
  author: '',
  nodes: [step('build', 'script', 'Build')],
  edges: [],
}

/** start -> g (workflows; x, y, z, q all run child-wf) -> done / oops. */
export const wfGroupDef: WorkflowDefinition = {
  name: 'repeat',
  description: '',
  author: '',
  nodes: [step('start'), step('g', 'parallel_group', 'Fan out'), step('done'), step('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}

export const wfGroupLayout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: {
        form: 'workflows',
        tasks: ['x', 'y', 'z', 'q'].map((key) => ({ key, workflow: 'child-wf' })),
        completionRule: 'all_succeed',
        unfinishedBehavior: 'wait_for_remaining',
        gatherMode: 'results',
      },
      bodies: {},
      nested: [],
      successTarget: 'done',
      failureTarget: 'oops',
    },
  },
  owner: {},
  order: ['g'],
}

export const live = (instanceId: string, nodeId: string, state: LiveNodeStateValue): LiveNodeState => ({ instanceId, nodeId, state })

export function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key,
    kind: 'branch',
    origin: key === 'a' ? 'a1' : 'b1',
    childInstanceId: `br-${key}`,
    liveStatus: 'running',
    frozenStatus: '',
    blocking: true,
    continuingIndependently: false,
    cancelRequested: false,
    redacted: false,
    ...over,
  }
}

export function panel(over: Partial<GroupPanel> = {}): GroupPanel {
  return {
    groupRunId: 'run-g',
    instanceId: 'root',
    nodeId: 'g',
    form: 'branches',
    completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished',
    gatherMode: 'results',
    state: 'running',
    tasks: [task('a'), task('b')],
    progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
    waitingReasons: ['Waiting for tasks a, b'],
    ...over,
  }
}

/** Root waits at g; branch a runs a2; branch b's b1 is `bState`. */
export function fanoutContexts(bState: 'running' | 'completed' = 'running'): ExecutionContext[] {
  return [
    { instanceId: 'root', kind: 'root', state: 'waiting', blocking: true, redacted: false, graph: asGraph(fanoutDef), nodes: [live('root', 'start', 'completed'), live('root', 'g', 'waiting')] },
    { instanceId: 'br-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] },
    { instanceId: 'br-b', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: bState, blocking: true, redacted: false, nodes: [live('br-b', 'b1', bState)] },
  ]
}

export function fanoutAggregate(over: Partial<GraphState> = {}): GraphState {
  return { rootInstanceId: 'root', version: 'v1', active: true, layout: fanoutLayout, warnings: [], contexts: fanoutContexts(), groups: [panel()], ...over }
}

/**
 * p-done: a COMPLETED first_success parent. Winner b (c-win) finished; the
 * non-winner a (c-ind) keeps running under finish_independently.
 */
export function independentAggregate(): GraphState {
  return {
    rootInstanceId: 'p-done',
    version: 'd1',
    active: true,
    layout: fanoutLayout,
    warnings: [],
    contexts: [
      { instanceId: 'p-done', kind: 'root', state: 'completed', blocking: false, redacted: false, graph: asGraph(fanoutDef), nodes: [live('p-done', 'start', 'completed'), live('p-done', 'g', 'completed'), live('p-done', 'done', 'completed')] },
      { instanceId: 'c-ind', parentInstanceId: 'p-done', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: false, redacted: false, nodes: [live('c-ind', 'a1', 'completed'), live('c-ind', 'a2', 'running')] },
      { instanceId: 'c-win', parentInstanceId: 'p-done', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: 'completed', blocking: false, redacted: false, nodes: [live('c-win', 'b1', 'completed')] },
    ],
    groups: [
      panel({
        instanceId: 'p-done',
        completionRule: 'first_success',
        unfinishedBehavior: 'finish_independently',
        state: 'done',
        winner: 'b',
        waitingReasons: [],
        tasks: [
          task('a', { childInstanceId: 'c-ind', liveStatus: 'running', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
          task('b', { childInstanceId: 'c-win', liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        ],
        progress: { total: 2, completed: 1, failed: 0, cancelled: 0, active: 1, notStarted: 0 },
      }),
    ],
  }
}

/** The id Go encodes for a redacted context (uuid.Nil): it must never surface. */
export const HIDDEN_ID = '00000000-0000-0000-0000-000000000000'

const wtask = (key: string, child: string, liveStatus: GroupPanelTask['liveStatus']): GroupPanelTask =>
  task(key, { kind: 'workflow', origin: 'child-wf', childInstanceId: child, liveStatus })

/**
 * p-wf waits at a workflows-form group whose tasks x/y/z run the SAME child
 * workflow (each child reuses node id "build"): x running, y waiting, z
 * paused; q is a child the caller may not see (redacted).
 */
export function repeatedChildAggregate(): GraphState {
  const child = (id: string, key: string, state: 'running' | 'waiting' | 'paused'): ExecutionContext => ({
    instanceId: id,
    parentInstanceId: 'p-wf',
    groupRunId: 'run-w',
    taskKey: key,
    kind: 'workflow',
    workflowName: 'child-wf',
    state,
    blocking: true,
    redacted: false,
    graph: asGraph(childWfDef),
    nodes: [live(id, 'build', state)],
  })
  return {
    rootInstanceId: 'p-wf',
    version: 'w1',
    active: true,
    layout: wfGroupLayout,
    warnings: [],
    contexts: [
      { instanceId: 'p-wf', kind: 'root', state: 'waiting', blocking: true, redacted: false, graph: asGraph(wfGroupDef), nodes: [live('p-wf', 'start', 'completed'), live('p-wf', 'g', 'waiting')] },
      child('w1', 'x', 'running'),
      child('w2', 'y', 'waiting'),
      child('w3', 'z', 'paused'),
      { instanceId: HIDDEN_ID, parentInstanceId: 'p-wf', groupRunId: 'run-w', taskKey: 'q', kind: 'workflow', state: 'unknown', blocking: true, redacted: true, nodes: [] },
    ],
    groups: [
      panel({
        groupRunId: 'run-w',
        instanceId: 'p-wf',
        form: 'workflows',
        unfinishedBehavior: 'wait_for_remaining',
        tasks: [
          wtask('x', 'w1', 'running'),
          wtask('y', 'w2', 'waiting'),
          wtask('z', 'w3', 'paused'),
          task('q', { kind: 'workflow', origin: 'child-wf', childInstanceId: null, liveStatus: 'unknown', redacted: true }),
        ],
        progress: { total: 4, completed: 0, failed: 0, cancelled: 0, active: 3, notStarted: 0 },
        waitingReasons: ['Waiting for tasks x, y, z and a hidden dependency'],
      }),
    ],
  }
}
```

- [x] Step 2: Write the failing tests `dashboard/src/graph/liveSummary.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { activeNodesOf, buildHierarchyMarkers, EMPTY_DEFINITION, rootDefinitionOf } from './liveSummary'
import {
  fanoutAggregate,
  fanoutContexts,
  fanoutDef,
  HIDDEN_ID,
  independentAggregate,
  live,
  repeatedChildAggregate,
  wfGroupDef,
} from './liveSummary.fixtures'

describe('rootDefinitionOf', () => {
  it("returns the selected run's own snapshot from its root context", () => {
    expect(rootDefinitionOf(fanoutAggregate())?.nodes.map((n) => n.id)).toEqual(fanoutDef.nodes.map((n) => n.id))
  })

  // Error: an unreadable snapshot must not be drawn as an empty-but-valid graph.
  it('is null without an aggregate, without a root context, or with an unreadable snapshot', () => {
    const [root, ...rest] = fanoutContexts()
    expect(rootDefinitionOf(null)).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ rootInstanceId: 'other' }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, graph: { nodes: 'x' } }, ...rest] }))).toBeNull()
    expect(rootDefinitionOf(fanoutAggregate({ contexts: [{ ...root, redacted: true }, ...rest] }))).toBeNull()
  })
})

describe('activeNodesOf', () => {
  // Happy path: every concurrently active node, across both fanned-out paths.
  it('lists EVERY running/waiting node of the root and both branches, instance-qualified', () => {
    const out = activeNodesOf(fanoutAggregate(), fanoutDef)
    expect(out.map((a) => [a.key, a.label, a.state, a.taskKey])).toEqual([
      ['root:g', 'Fan out', 'waiting', null],
      ['br-a:a2', 'A2', 'running', 'a'],
      ['br-b:b1', 'B1', 'running', 'b'],
    ])
  })

  // Edge: duplicate node ids across repeated child workflows; paused tasks; redaction.
  it('keeps repeated node ids apart, includes paused work, and never lists a redacted context', () => {
    const out = activeNodesOf(repeatedChildAggregate(), wfGroupDef)
    expect(out.map((a) => [a.key, a.label, a.state, a.taskKey])).toEqual([
      ['p-wf:g', 'Fan out', 'waiting', null],
      ['w1:build', 'Build', 'running', 'x'],
      ['w2:build', 'Build', 'waiting', 'y'],
      ['w3:build', 'Build', 'paused', 'z'],
    ])
    expect(out.some((a) => a.instanceId === HIDDEN_ID)).toBe(false)
  })

  it('marks work continuing independently after its group was decided as independent', () => {
    expect(activeNodesOf(independentAggregate(), fanoutDef)).toEqual([
      { key: 'c-ind:a2', instanceId: 'c-ind', nodeId: 'a2', label: 'A2', state: 'running', taskKey: 'a', independent: true },
    ])
  })

  it('falls back to the node id when no snapshot names the node', () => {
    const [root, ...rest] = fanoutContexts()
    const out = activeNodesOf(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }), EMPTY_DEFINITION)
    expect(out.map((a) => a.label)).toEqual(['g', 'a2', 'b1'])
  })

  it('an inactive aggregate has no active nodes', () => {
    expect(activeNodesOf(fanoutAggregate({ contexts: fanoutContexts('completed').map((c) => ({ ...c, nodes: [] })) }), fanoutDef)).toEqual([])
  })
})

describe('buildHierarchyMarkers', () => {
  it('annotates each branch child with its group, task key, blocking flag and active nodes', () => {
    const m = buildHierarchyMarkers(fanoutAggregate(), 'root')
    expect(Object.keys(m)).toEqual(['br-a', 'br-b'])
    expect(m['br-a']).toEqual({
      kind: 'branch',
      groupNodeId: 'g',
      groupLabel: 'Fan out',
      taskKey: 'a',
      blocking: true,
      independent: false,
      activeSummary: 'running: A2',
    })
    expect(m['br-b'].activeSummary).toBe('running: B1')
  })

  // Success: finish_independently work is flagged non-blocking; finished work has no live summary.
  it('flags finish_independently work as non-blocking; a terminal child has no active summary', () => {
    const m = buildHierarchyMarkers(independentAggregate(), 'p-done')
    expect(m['c-ind']).toMatchObject({ kind: 'branch', taskKey: 'a', blocking: false, independent: true, activeSummary: 'running: A2' })
    expect(m['c-win']).toMatchObject({ taskKey: 'b', independent: false, activeSummary: null })
  })

  it('keeps repeated child workflows apart, summarises paused work, and never keys a redacted child', () => {
    const m = buildHierarchyMarkers(repeatedChildAggregate(), 'p-wf')
    expect(Object.keys(m).sort()).toEqual(['w1', 'w2', 'w3'])
    expect(m.w1).toMatchObject({ kind: 'workflow', groupNodeId: 'g', groupLabel: 'Fan out', taskKey: 'x', activeSummary: 'running: Build' })
    expect(m.w2.activeSummary).toBe('waiting: Build')
    expect(m.w3.activeSummary).toBe('paused')
  })

  it("only DIRECT children get markers; a grandchild's activity rolls up into its parent's summary", () => {
    const gs = fanoutAggregate({
      contexts: [
        ...fanoutContexts(),
        { instanceId: 'gc', parentInstanceId: 'br-a', kind: 'spawn', state: 'running', blocking: false, redacted: false, nodes: [live('gc', 'x', 'running')] },
      ],
    })
    const m = buildHierarchyMarkers(gs, 'root')
    expect(m.gc).toBeUndefined()
    expect(m['br-a'].activeSummary).toBe('running: A2, x')
  })

  it('a plain spawned child gets its state summary but no group or task', () => {
    const gs = fanoutAggregate({
      contexts: [
        ...fanoutContexts(),
        { instanceId: 'sp', parentInstanceId: 'root', kind: 'spawn', workflowName: 'side', state: 'queued', blocking: false, redacted: false, nodes: [] },
      ],
    })
    expect(buildHierarchyMarkers(gs, 'root').sp).toEqual({
      kind: 'spawn',
      groupNodeId: null,
      groupLabel: null,
      taskKey: null,
      blocking: false,
      independent: false,
      activeSummary: 'queued',
    })
  })

  // Error: an unreadable parent snapshot degrades to the group node id, never a guessed label.
  it('falls back to the group node id when the parent snapshot is unreadable', () => {
    const [root, ...rest] = fanoutContexts()
    expect(buildHierarchyMarkers(fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest] }), 'root')['br-a'].groupLabel).toBe('g')
  })
})
```

- [x] Step 3: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/liveSummary.test.ts
```
Expected:
```
 FAIL  src/graph/liveSummary.test.ts
Error: Failed to resolve import "./liveSummary" from "src/graph/liveSummary.test.ts". Does the file exist?
 Test Files  1 failed (1)
```

- [x] Step 4: Implement `dashboard/src/graph/liveSummary.ts`:

```ts
import type { ExecutionContext, ExecutionContextKind, GraphState, LiveNodeStateValue } from '../api/types'
import type { WorkflowDefinition } from './types'
import { activeNodeLabels, qualifiedKey } from './aggregateState'

/** A definition with no nodes: the label fallback when a run's snapshot is unreadable. */
export const EMPTY_DEFINITION: WorkflowDefinition = { name: '', description: '', author: '', nodes: [], edges: [] }

/** Node states that count as active work in a live summary (paused work is still unfinished). */
const ACTIVE_NODE_STATES = new Set<LiveNodeStateValue>(['running', 'waiting', 'paused'])

/** Run states whose hierarchy row carries an active-state summary. */
const ACTIVE_RUN_STATES = new Set<string>(['queued', 'running', 'waiting', 'paused'])

/** One currently active node of an aggregate, identified by (instanceId, nodeId). */
export interface ActiveNodeRef {
  /** qualifiedKey(instanceId, nodeId): repeated child workflows reuse node ids, so nodeId alone is never a key. */
  key: string
  instanceId: string
  nodeId: string
  /** Label from the owning context's snapshot (walking up to the nearest ancestor that has one), else the node id. */
  label: string
  state: LiveNodeStateValue
  /** Task key of the group task the owning context runs; null for the selected run itself or a plain spawn. */
  taskKey: string | null
  /** The owning task continues after its group was decided (finish_independently). */
  independent: boolean
}

/** Branch / task / active-state annotation of one CHILD row in the queue hierarchy. */
export interface HierarchyMarker {
  /** branch | workflow (group tasks) | spawn (plain spawned child). */
  kind: ExecutionContextKind
  /** The parent's parallel_group node that launched this child; null for a plain spawn. */
  groupNodeId: string | null
  /** That group node's label in the parent's snapshot (falls back to its id). */
  groupLabel: string | null
  taskKey: string | null
  /** False once a group decision released the task (or for a plain spawn). */
  blocking: boolean
  /** Continues after its group was decided (finish_independently): non-blocking work. */
  independent: boolean
  /** "<state>: <active node labels of the child and its descendants>", or just the state; null when terminal. */
  activeSummary: string | null
}

type NodeRef = { id: string; label?: string }

function isDefinition(g: unknown): g is { nodes: NodeRef[]; edges: unknown[] } {
  const d = g as { nodes?: unknown; edges?: unknown } | null | undefined
  return Array.isArray(d?.nodes) && Array.isArray(d?.edges)
}

/**
 * The selected run's own workflow snapshot: the graph of its (non-redacted)
 * root context. Null when absent or unreadable — callers must not draw an
 * empty-but-valid graph for a snapshot they could not read.
 */
export function rootDefinitionOf(gs: GraphState | null): WorkflowDefinition | null {
  if (!gs) return null
  const root = gs.contexts.find((c) => c.instanceId === gs.rootInstanceId && !c.redacted)
  if (!root || !isDefinition(root.graph)) return null
  return { ...EMPTY_DEFINITION, ...(root.graph as unknown as WorkflowDefinition) }
}

/** Child instance ids whose task continues independently after its group decision. */
function independentChildren(gs: GraphState): Set<string> {
  const out = new Set<string>()
  for (const p of gs.groups) {
    for (const t of p.tasks) {
      if (t.continuingIndependently && !t.redacted && t.childInstanceId) out.add(t.childInstanceId)
    }
  }
  return out
}

/**
 * Every running, waiting or paused node of every VISIBLE context of the
 * aggregate, in context order. Keys are instance-qualified so the same node id
 * in two child runs of one workflow stays two entries. Redacted contexts are
 * skipped entirely (no id, no node, no label).
 */
export function activeNodesOf(gs: GraphState, rootDef: WorkflowDefinition): ActiveNodeRef[] {
  const byId = new Map(gs.contexts.filter((c) => !c.redacted).map((c) => [c.instanceId, c]))
  const independent = independentChildren(gs)
  const nodesFor = (ctx: ExecutionContext): NodeRef[] => {
    let cur: ExecutionContext | undefined = ctx
    for (let guard = 0; cur && guard < 64; guard++) {
      if (cur.graph) return isDefinition(cur.graph) ? cur.graph.nodes : []
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
    }
    return rootDef.nodes
  }
  const out: ActiveNodeRef[] = []
  for (const ctx of gs.contexts) {
    if (ctx.redacted) continue
    const nodes = nodesFor(ctx)
    for (const n of ctx.nodes) {
      if (!ACTIVE_NODE_STATES.has(n.state)) continue
      out.push({
        key: qualifiedKey(ctx.instanceId, n.nodeId),
        instanceId: ctx.instanceId,
        nodeId: n.nodeId,
        label: nodes.find((d) => d.id === n.nodeId)?.label || n.nodeId,
        state: n.state,
        taskKey: ctx.taskKey || null,
        independent: independent.has(ctx.instanceId),
      })
    }
  }
  return out
}

/**
 * Markers for the DIRECT children of `parentId`, keyed by child instance id,
 * from the parent's authorized aggregate (`GET /instances/{parentId}/graph-state`).
 * Redacted children are never keyed: the list endpoint never returns them
 * either, and their placeholder id must not surface. A child's active summary
 * includes its visible descendants' running/waiting nodes (child relationships
 * roll up), resolved per instance so repeated node ids never cross.
 */
export function buildHierarchyMarkers(gs: GraphState, parentId: string): Record<string, HierarchyMarker> {
  const def = rootDefinitionOf(gs) ?? EMPTY_DEFINITION
  const panels = new Map(gs.groups.map((p) => [p.groupRunId, p]))
  const out: Record<string, HierarchyMarker> = {}
  for (const ctx of gs.contexts) {
    if (ctx.redacted || ctx.parentInstanceId !== parentId || ctx.instanceId === parentId) continue
    const panel = ctx.groupRunId ? panels.get(ctx.groupRunId) : undefined
    const panelTask = panel?.tasks.find((t) => t.childInstanceId === ctx.instanceId)
    const groupNodeId = panel?.nodeId ?? null
    let activeSummary: string | null = null
    if (ACTIVE_RUN_STATES.has(ctx.state)) {
      const labels = activeNodeLabels(gs, ctx.instanceId, def)
      activeSummary = labels.length > 0 ? `${ctx.state}: ${labels.join(', ')}` : ctx.state
    }
    out[ctx.instanceId] = {
      kind: ctx.kind,
      groupNodeId,
      groupLabel: groupNodeId ? def.nodes.find((n) => n.id === groupNodeId)?.label || groupNodeId : null,
      taskKey: ctx.taskKey || null,
      blocking: ctx.blocking,
      independent: panelTask?.continuingIndependently === true,
      activeSummary,
    }
  }
  return out
}
```

- [x] Step 5: Run it, confirm it passes; typecheck.

```bash
cd dashboard && npx vitest run src/graph/liveSummary.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  13 passed (13)
```

- [x] Step 6: Commit.

```bash
git add dashboard/src/graph/liveSummary.ts dashboard/src/graph/liveSummary.fixtures.ts dashboard/src/graph/liveSummary.test.ts && git commit -m "feat(dashboard): instance-qualified active-node summaries and hierarchy markers from the aggregate" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.3: `useRunHierarchy` — load-more, stale-while-revalidate refresh, request ordering, markers
**Files:**
- Modify: `dashboard/src/hooks/useRunHierarchy.ts:1-169` (whole file)
- Test: `dashboard/src/hooks/useRunHierarchy.test.ts` — replace `:5-8` (mock + import), `:25-27` (`beforeEach`), append after `:271`

- [x] Step 1: Write the failing hook tests. In `dashboard/src/hooks/useRunHierarchy.test.ts` replace lines 5-8 with:

```ts
const listMock = vi.fn()
const graphStateMock = vi.fn()
vi.mock('../api/client', () => ({
  api: {
    instances: {
      list: (...a: unknown[]) => listMock(...a),
      graphState: (...a: unknown[]) => graphStateMock(...a),
    },
  },
}))

import { CHILD_PAGE_LIMIT, useRunHierarchy } from './useRunHierarchy'
import { fanoutAggregate, fanoutContexts } from '../graph/liveSummary.fixtures'
```

replace lines 25-27 (`beforeEach`) with:

```ts
beforeEach(() => {
  listMock.mockReset()
  graphStateMock.mockReset()
  // Default: the parent's aggregate is unreadable (hidden parent -> 404).
  graphStateMock.mockRejectedValue(new Error('API 404: instance not found'))
})
```

and append after line 271:

```ts
// ---------- pagination (P4-T10) ----------

const many = (parent: string, n: number, from = 0) =>
  Array.from({ length: n }, (_, i) => row(`${parent}-c${from + i}`, { parentInstanceId: parent }))

// Fake keyed by (parent, limit, offset): a page is cut exactly where the server
// would cut it (adapter/sqlite/instance_repo.go:216 LIMIT ? OFFSET ?).
function pagedList(all: Record<string, EnrichedInstanceSummary[]>) {
  return ({ parent, limit, offset }: { parent: string; limit: number; offset?: number }) => {
    const rows = all[parent]
    if (!rows) return Promise.reject(new Error('API 404: instance not found'))
    const from = offset ?? 0
    return Promise.resolve(rows.slice(from, from + limit))
  }
}

it('a full first page reports more children, and loadMore appends the next page by offset', async () => {
  listMock.mockImplementation(pagedList({ p1: many('p1', 150) }))
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.childrenOf('p1')).toHaveLength(CHILD_PAGE_LIMIT)
  expect(result.current.hasMore('p1')).toBe(true)

  await act(async () => { await result.current.loadMore('p1') })

  expect(listMock).toHaveBeenLastCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.childrenOf('p1')).toHaveLength(150)
  expect(result.current.childrenOf('p1')[100].id).toBe('p1-c100')
  expect(result.current.hasMore('p1')).toBe(false)
  expect(result.current.isLoadingMore('p1')).toBe(false)
})

it('a short page reports no more children, and loadMore is then a no-op (also for an unfetched row)', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  expect(result.current.hasMore('p1')).toBe(false)

  await act(async () => { await result.current.loadMore('p1') })
  await act(async () => { await result.current.loadMore('never-expanded') })
  expect(listMock).toHaveBeenCalledTimes(1)
})

// Children list newest-first, so a child created between two page requests
// pushes the first page's last row onto the second page.
it('loadMore de-duplicates a row that shifted across the page boundary', async () => {
  const first = many('p1', CHILD_PAGE_LIMIT)
  listMock
    .mockResolvedValueOnce(first)
    .mockResolvedValueOnce([first[CHILD_PAGE_LIMIT - 1], row('p1-older', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.loadMore('p1') })

  const ids = result.current.childrenOf('p1').map((r) => r.id)
  expect(ids).toHaveLength(CHILD_PAGE_LIMIT + 1)
  expect(new Set(ids).size).toBe(ids.length)
  expect(ids[ids.length - 1]).toBe('p1-older')
  expect(result.current.hasMore('p1')).toBe(false)
})

// Edge: >100 tasks + refresh — a refresh must never shrink a level the user
// paged through, or the 101st task disappears on the next lifecycle event.
it('a refresh after load-more refetches EVERY loaded page', async () => {
  listMock.mockImplementation(pagedList({ p1: many('p1', 150) }))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })
  await act(async () => { await result.current.loadMore('p1') })
  listMock.mockClear()

  await act(async () => { result.current.invalidate() })
  await waitFor(() => expect(result.current.isLoading('p1')).toBe(false))

  expect(listMock).toHaveBeenCalledTimes(2)
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(result.current.childrenOf('p1')).toHaveLength(150)
})

// Failure: a failed page keeps what is already shown.
it('a failed load-more keeps the loaded rows and reports the error on the row', async () => {
  listMock.mockResolvedValueOnce(many('p1', CHILD_PAGE_LIMIT)).mockRejectedValueOnce(new Error('API 500: page boom'))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  await act(async () => { await result.current.loadMore('p1') })

  expect(result.current.errorOf('p1')).toContain('page boom')
  expect(result.current.childrenOf('p1')).toHaveLength(CHILD_PAGE_LIMIT)
  expect(result.current.hasMore('p1')).toBe(true)
  expect(result.current.isLoadingMore('p1')).toBe(false)
})

// ---------- stale cache (P4-T10) ----------

it('keeps the cached rows visible while a refresh is in flight, then replaces them', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1', state: 'running' })])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  act(() => { result.current.invalidate() })

  await waitFor(() => expect(result.current.isLoading('p1')).toBe(true))
  expect(result.current.childrenOf('p1').map((r) => r.state)).toEqual(['running'])

  await act(async () => { resolve([row('c1', { parentInstanceId: 'p1', state: 'completed' })]) })
  await waitFor(() => expect(result.current.childrenOf('p1')[0].state).toBe('completed'))
})

it('an older refresh response never overwrites a newer one', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })])
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  let resolveOld!: (v: EnrichedInstanceSummary[]) => void
  let resolveNew!: (v: EnrichedInstanceSummary[]) => void
  listMock
    .mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolveOld = r }))
    .mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolveNew = r }))
  act(() => { result.current.invalidate() })
  act(() => { result.current.invalidate() })

  await act(async () => { resolveNew([row('c1', { parentInstanceId: 'p1', state: 'completed' })]) })
  await act(async () => { resolveOld([row('c1', { parentInstanceId: 'p1', state: 'running' })]) })

  expect(result.current.childrenOf('p1')[0].state).toBe('completed')
  expect(result.current.isLoading('p1')).toBe(false)
})

it('a failed refresh keeps the last rows and reports the error, leaving the row expanded', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })]).mockRejectedValueOnce(new Error('API 500: boom'))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('p1') })

  await act(async () => { result.current.invalidate() })

  await waitFor(() => expect(result.current.errorOf('p1')).toContain('boom'))
  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
  expect(result.current.isExpanded('p1')).toBe(true)
})

// ---------- markers (P4-T10) ----------

it("builds branch/task/active markers for each child from the parent's authorized graph state", async () => {
  listMock.mockResolvedValue([row('br-a', { parentInstanceId: 'root' }), row('br-b', { parentInstanceId: 'root' })])
  graphStateMock.mockImplementation((id: string) =>
    id === 'root' ? Promise.resolve(fanoutAggregate()) : Promise.reject(new Error('API 404: instance not found')),
  )
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('root') })

  expect(graphStateMock).toHaveBeenCalledWith('root')
  expect(result.current.markersOf('root')['br-a']).toEqual({
    kind: 'branch',
    groupNodeId: 'g',
    groupLabel: 'Fan out',
    taskKey: 'a',
    blocking: true,
    independent: false,
    activeSummary: 'running: A2',
  })
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')
})

// Error: markers are an overlay on the authoritative list. An unreadable
// aggregate (403/404 hidden parent, 5xx) leaves the rows listed and
// unannotated — it neither fails the level nor invents a state.
it('an unreadable graph state leaves the level listed with no markers and no error', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  graphStateMock.mockRejectedValue(new Error('API 403: forbidden'))
  const { result } = renderHook(() => useRunHierarchy())

  await act(async () => { await result.current.toggle('p1') })

  expect(result.current.childrenOf('p1').map((r) => r.id)).toEqual(['c1'])
  expect(result.current.markersOf('p1')).toEqual({})
  expect(result.current.errorOf('p1')).toBeNull()
})

it('markers refresh with their level: a branch that finished loses its active summary', async () => {
  listMock.mockResolvedValue([row('br-a', { parentInstanceId: 'root' }), row('br-b', { parentInstanceId: 'root' })])
  graphStateMock
    .mockResolvedValueOnce(fanoutAggregate())
    .mockResolvedValueOnce(fanoutAggregate({ version: 'v2', contexts: fanoutContexts('completed') }))
  const { result } = renderHook(() => useRunHierarchy())
  await act(async () => { await result.current.toggle('root') })
  expect(result.current.markersOf('root')['br-b'].activeSummary).toBe('running: B1')

  await act(async () => { result.current.invalidate() })

  await waitFor(() => expect(result.current.markersOf('root')['br-b'].activeSummary).toBeNull())
  expect(graphStateMock).toHaveBeenCalledTimes(2)
  expect(result.current.markersOf('root')['br-a'].activeSummary).toBe('running: A2')
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/hooks/useRunHierarchy.test.ts
```
Expected (the 16 existing tests still pass):
```
 FAIL  src/hooks/useRunHierarchy.test.ts > a full first page reports more children, and loadMore appends the next page by offset
TypeError: result.current.hasMore is not a function
 FAIL  src/hooks/useRunHierarchy.test.ts > keeps the cached rows visible while a refresh is in flight, then replaces them
AssertionError: expected [] to deeply equal [ 'running' ]
 FAIL  src/hooks/useRunHierarchy.test.ts > an older refresh response never overwrites a newer one
AssertionError: expected 'running' to be 'completed'
 FAIL  src/hooks/useRunHierarchy.test.ts > a failed refresh keeps the last rows and reports the error, leaving the row expanded
AssertionError: expected [] to deeply equal [ 'c1' ]
 FAIL  src/hooks/useRunHierarchy.test.ts > builds branch/task/active markers for each child from the parent's authorized graph state
TypeError: result.current.markersOf is not a function
...
      Tests  11 failed | 16 passed (27)
```

- [x] Step 3: Implement. Replace the whole of `dashboard/src/hooks/useRunHierarchy.ts` (lines 1-169) with:

```ts
import { useCallback, useRef, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api, type ListInstancesParams } from '../api/client'
import { buildHierarchyMarkers, type HierarchyMarker } from '../graph/liveSummary'

interface Level {
  rows: EnrichedInstanceSummary[]
  /** A full (re)fetch of every loaded page is in flight. */
  loading: boolean
  /** A load-more page is in flight. */
  loadingMore: boolean
  error: string | null
  /** The last fetched page was full, so more children may exist beyond it. */
  hasMore: boolean
  /** Child id -> marker, from the parent's authorized aggregate. */
  markers: Record<string, HierarchyMarker>
}

const EMPTY: EnrichedInstanceSummary[] = []
const NO_MARKERS: Record<string, HierarchyMarker> = {}
const NEW_LEVEL: Level = { rows: EMPTY, loading: false, loadingMore: false, error: null, hasMore: false, markers: NO_MARKERS }

/**
 * CHILD_PAGE_LIMIT is the explicit page size for one expansion.
 *
 * It is stated rather than inherited: without it the request took the server's
 * unspoken default, so a row with more children than that rendered a truncated
 * list INDISTINGUISHABLE from a complete one. With an explicit limit the caller
 * knows what a full page means, offers load-more after a full page, and
 * HierarchyRows can say how many are shown.
 */
export const CHILD_PAGE_LIMIT = 100

export interface RunHierarchy {
  /** Whether the given row is currently expanded. */
  isExpanded: (id: string) => boolean
  /** The fetched children of the given row (empty until fetched). */
  childrenOf: (id: string) => EnrichedInstanceSummary[]
  /** Whether the given row's level is being (re)fetched. */
  isLoading: (id: string) => boolean
  /** The last expansion, refresh or load-more error for the given row, or null. */
  errorOf: (id: string) => string | null
  /** Expand (fetching once) or collapse the given row. */
  toggle: (id: string) => Promise<void>
  /**
   * Refresh: keep every OPEN level's rows on screen while refetching all of its
   * loaded pages (stale-while-revalidate), drop collapsed levels, and PRESERVE
   * which rows are expanded. Used on every top-level refetch (lifecycle/group
   * events, polling, reconnect).
   */
  invalidate: () => void
  /**
   * Merge `fields` into every cached row whose id matches, at any depth. Used
   * to live-patch an expanded child from a node event without a refetch; a
   * row that is not cached is ignored (it fetches fresh on expand).
   */
  patchRow: (id: string, fields: Partial<EnrichedInstanceSummary>) => void
  /** Whether more children may exist beyond the loaded pages (the last page was full). */
  hasMore: (id: string) => boolean
  /** Whether a load-more page for the given row is in flight. */
  isLoadingMore: (id: string) => boolean
  /** Fetch the next CHILD_PAGE_LIMIT children of an expanded row and append them. */
  loadMore: (id: string) => Promise<void>
  /** Branch/group/task/active markers of the given row's children, keyed by child id. */
  markersOf: (id: string) => Record<string, HierarchyMarker>
}

function pageParams(parent: string, offset: number): ListInstancesParams {
  // includeDismissed: true — a dismissed run is hidden from the top-level
  // QUEUE, which is a personal inbox; it is not hidden from a run TREE, which
  // is a structural view of what a run spawned. Omitting them also made the
  // expansion contradict the row's own childCount, which is computed
  // server-side without any dismissal filter.
  const p: ListInstancesParams = { parent, limit: CHILD_PAGE_LIMIT, includeDismissed: true }
  if (offset > 0) p.offset = offset
  return p
}

/** Concatenate pages, keeping the first occurrence of each id (rows shift between page requests). */
function mergeRows(...pages: EnrichedInstanceSummary[][]): EnrichedInstanceSummary[] {
  const seen = new Set<string>()
  const out: EnrichedInstanceSummary[] = []
  for (const page of pages) {
    for (const r of page) {
      if (seen.has(r.id)) continue
      seen.add(r.id)
      out.push(r)
    }
  }
  return out
}

/**
 * Markers of `parentId`'s children from the parent's AUTHORIZED aggregate —
 * the same DTO the run views use. They are an overlay on the authoritative
 * list: an unreadable aggregate (hidden parent -> 403/404, transient 5xx)
 * leaves the rows unannotated rather than failing the level or inventing a
 * state for them.
 */
async function loadMarkers(parentId: string): Promise<Record<string, HierarchyMarker>> {
  try {
    const gs = await api.instances.graphState(parentId)
    return buildHierarchyMarkers(gs, parentId)
  } catch {
    return NO_MARKERS
  }
}

const errorText = (err: unknown, fallback: string) => (err instanceof Error ? err.message : fallback)

/**
 * useRunHierarchy owns the queue list's expansion state and its per-row lazy
 * child fetching.
 *
 * Expansion is RECURSIVE FETCHING: each expand is ONE request per page for ONE
 * level, keyed by instance id, so the tree opens to arbitrary depth with no
 * depth-aware code. Every level is fetched through the SAME authorized list
 * endpoint the top level uses, so each depth inherits the server's visibility
 * scoping for free; its markers come from the parent's authorized aggregate.
 *
 * A level is paged: the first page is CHILD_PAGE_LIMIT rows, `loadMore`
 * appends the next by offset, so every fixed task of a large group can be
 * inspected. A refresh refetches every loaded page and keeps the old rows on
 * screen until the new ones land; responses are applied in REQUEST order per
 * level, so a slow older response can never overwrite a newer one.
 *
 * An expansion failure is confined to its own row: siblings and the rest of the
 * page are unaffected, a failed refresh keeps the last rows, and collapsing
 * then re-expanding retries.
 *
 * EXPANSION STATE AND FETCHED DATA ARE SEPARATE CONCERNS. Which rows are open
 * is USER INTENT; the rows inside them are a cache. Only the cache goes stale
 * on a refetch, so only the cache is refreshed — see `invalidate`.
 */
export function useRunHierarchy(): RunHierarchy {
  const [expanded, setExpandedState] = useState<Record<string, boolean>>({})
  const [levels, setLevelsState] = useState<Record<string, Level>>({})

  // Mirrors of the state above, updated inside the same functional setState
  // call. `toggle`/`loadMore` read these instead of the closed-over values so
  // two calls fired back to back — a real double-click — each see the OTHER's
  // effect rather than both slipping past the guards.
  const expandedRef = useRef(expanded)
  const levelsRef = useRef(levels)
  // Per-level request sequence: only the newest full fetch (and a load-more
  // issued after it) may write the level.
  const seqRef = useRef<Record<string, number>>({})

  const setExpanded = useCallback((updater: (prev: Record<string, boolean>) => Record<string, boolean>) => {
    setExpandedState((prev) => (expandedRef.current = updater(prev)))
  }, [])
  const setLevels = useCallback((updater: (prev: Record<string, Level>) => Record<string, Level>) => {
    setLevelsState((prev) => (levelsRef.current = updater(prev)))
  }, [])

  const isExpanded = useCallback((id: string) => expanded[id] === true, [expanded])
  const childrenOf = useCallback((id: string) => levels[id]?.rows ?? EMPTY, [levels])
  const isLoading = useCallback((id: string) => levels[id]?.loading === true, [levels])
  const errorOf = useCallback((id: string) => levels[id]?.error ?? null, [levels])
  const hasMore = useCallback((id: string) => levels[id]?.hasMore === true, [levels])
  const isLoadingMore = useCallback((id: string) => levels[id]?.loadingMore === true, [levels])
  const markersOf = useCallback((id: string) => levels[id]?.markers ?? NO_MARKERS, [levels])

  const fetchLevel = useCallback(
    async (id: string) => {
      const seq = (seqRef.current[id] ?? 0) + 1
      seqRef.current[id] = seq
      // Refetch every page already shown, so a refresh never shrinks a level
      // the user paged through.
      const pages = Math.max(1, Math.ceil((levelsRef.current[id]?.rows.length ?? 0) / CHILD_PAGE_LIMIT))
      setLevels((prev) => ({ ...prev, [id]: { ...(prev[id] ?? NEW_LEVEL), loading: true, loadingMore: false, error: null } }))
      try {
        const [pageRows, markers] = await Promise.all([
          Promise.all(Array.from({ length: pages }, (_, i) => api.instances.list(pageParams(id, i * CHILD_PAGE_LIMIT)))),
          loadMarkers(id),
        ])
        if (seqRef.current[id] !== seq) return // a newer fetch owns this level
        const last = pageRows[pageRows.length - 1]
        setLevels((prev) => ({
          ...prev,
          [id]: {
            rows: mergeRows(...pageRows),
            loading: false,
            loadingMore: false,
            error: null,
            hasMore: last.length >= CHILD_PAGE_LIMIT,
            markers,
          },
        }))
      } catch (err) {
        if (seqRef.current[id] !== seq) return
        const msg = errorText(err, 'Failed to load child runs')
        // The last good rows stay: a failed refresh must not blank a level.
        setLevels((prev) => ({ ...prev, [id]: { ...(prev[id] ?? NEW_LEVEL), loading: false, loadingMore: false, error: msg } }))
      }
    },
    [setLevels],
  )

  const toggle = useCallback(
    async (id: string) => {
      if (expandedRef.current[id]) {
        setExpanded((prev) => ({ ...prev, [id]: false }))
        return
      }
      setExpanded((prev) => ({ ...prev, [id]: true }))

      // Already fetched successfully: re-expanding uses the cache. A level that
      // ERRORED is retried.
      if (levelsRef.current[id] && levelsRef.current[id].error === null) return

      await fetchLevel(id)
    },
    [setExpanded, fetchLevel],
  )

  const loadMore = useCallback(
    async (id: string) => {
      const level = levelsRef.current[id]
      if (!level || level.loading || level.loadingMore || !level.hasMore) return
      const seq = seqRef.current[id] ?? 0
      const offset = level.rows.length
      setLevels((prev) => (prev[id] ? { ...prev, [id]: { ...prev[id], loadingMore: true, error: null } } : prev))
      try {
        const page = await api.instances.list(pageParams(id, offset))
        if (seqRef.current[id] !== seq) return // a refresh replaced this level meanwhile
        setLevels((prev) => {
          const cur = prev[id]
          if (!cur) return prev
          return {
            ...prev,
            [id]: { ...cur, rows: mergeRows(cur.rows, page), loadingMore: false, hasMore: page.length >= CHILD_PAGE_LIMIT },
          }
        })
      } catch (err) {
        if (seqRef.current[id] !== seq) return
        const msg = errorText(err, 'Failed to load more child runs')
        setLevels((prev) => (prev[id] ? { ...prev, [id]: { ...prev[id], loadingMore: false, error: msg } } : prev))
      }
    },
    [setLevels],
  )

  const invalidate = useCallback(() => {
    // Expansion is USER INTENT and survives: collapsing everything on any
    // lifecycle event made the tree unusable precisely while watching a
    // spawning run. The open levels' rows stay on screen (stale-while-
    // revalidate) and are refetched; collapsed levels are dropped and fetch
    // fresh on their next expand.
    const open = Object.keys(expandedRef.current).filter((id) => expandedRef.current[id])
    setLevels((prev) => {
      const next: Record<string, Level> = {}
      for (const id of open) if (prev[id]) next[id] = prev[id]
      return next
    })
    for (const id of open) {
      void fetchLevel(id)
    }
  }, [setLevels, fetchLevel])

  const patchRow = useCallback(
    (id: string, fields: Partial<EnrichedInstanceSummary>) => {
      setLevels((prev) => {
        let changed = false
        const next: Record<string, Level> = {}
        for (const [parent, level] of Object.entries(prev)) {
          if (!level.rows.some((r) => r.id === id)) {
            next[parent] = level
            continue
          }
          changed = true
          next[parent] = { ...level, rows: level.rows.map((r) => (r.id === id ? { ...r, ...fields } : r)) }
        }
        // No cached row matched: keep the same object so nothing re-renders.
        return changed ? next : prev
      })
    },
    [setLevels],
  )

  return { isExpanded, childrenOf, isLoading, errorOf, toggle, invalidate, patchRow, hasMore, isLoadingMore, loadMore, markersOf }
}
```

- [x] Step 4: Run it, confirm it passes; run the hierarchy consumers and typecheck.

```bash
cd dashboard && npx vitest run src/hooks/useRunHierarchy.test.ts src/components/HierarchyRows.test.tsx src/components/QueueSection.test.tsx src/routes/QueueView.hierarchypatch.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  4 passed (4)
```
(Consumers whose api mock has no `graphState` keep passing: `loadMarkers` turns the missing method's `TypeError` into "no markers".)

- [x] Step 5: Commit.

Self-review (spec → input/sink proof → quality): preserved the planned API and refresh behavior; keyed page/aggregate fakes assert actual cached child rows and markers, with route sinks covered next. Added red/green regressions for overlapping full pages (server offsets/page count must not derive from deduplicated row count) and two same-event load-more calls (synchronous refs prevent duplicate requests). Focused four-suite gate and typecheck pass.

```bash
git add dashboard/src/hooks/useRunHierarchy.ts dashboard/src/hooks/useRunHierarchy.test.ts && git commit -m "feat(dashboard): run hierarchy load-more, stale-while-revalidate refresh and aggregate markers" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.4: `HierarchyRows` + `QueueTableRow` — markers, non-blocking styling, load-more, no flash on refresh
**Files:**
- Modify: `dashboard/src/components/QueueTableRow.tsx:1-164` (whole file); `dashboard/src/components/QueueTableRow.module.css` (append after `:73`)
- Modify: `dashboard/src/components/HierarchyRows.tsx:1-159` (whole file); `dashboard/src/components/HierarchyRows.module.css` (append after `:18`)
- Create: `dashboard/src/routes/queueWorld.fixtures.ts`
- Test: `dashboard/src/components/HierarchyRows.test.tsx` — replace `:1` and `:13` (imports), append after `:236`
- Test: `dashboard/src/routes/QueueView.groups.test.tsx` (new; extended in Task 10.6)

- [x] Step 1: Create the queue-route fakes `dashboard/src/routes/queueWorld.fixtures.ts`:

```ts
import type { EnrichedInstanceSummary, GraphState, Me, WebSocketEvent } from '../api/types'
import { CHILD_PAGE_LIMIT } from '../hooks/useRunHierarchy'

/** Every argument api.instances.list can receive from the queue. */
export interface ListArgs {
  state?: string
  since?: string
  parent?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
  activeDescendantAncestors?: boolean
}

/** What the server would answer, per request shape. */
export interface QueueWorld {
  /** Ordinary per-state queue lists (running/waiting/queued/paused/failed/completed). */
  byState: Record<string, EnrichedInstanceSummary[]>
  /** The ancestor-discovery answer, or the error it fails with. */
  ancestors: EnrichedInstanceSummary[] | Error
  /** EVERY child of a parent; served one CHILD_PAGE_LIMIT page at a time. */
  children: Record<string, EnrichedInstanceSummary[]>
  /** graph-state by instance id; an absent id is a 404 (hidden or missing). */
  graphStates: Record<string, GraphState | Error>
}

export const newWorld = (): QueueWorld => ({ byState: {}, ancestors: [], children: {}, graphStates: {} })

export const localAdmin: Me = {
  userId: 'u1',
  username: 'local',
  role: 'admin',
  departments: ['default'],
  ledDepartments: [],
  isAdmin: true,
  authMode: 'local',
}

const notFound = () => Promise.reject(new Error('API 404: instance not found'))

/**
 * The list endpoint keyed on EVERY load-bearing argument: an ancestor-discovery
 * request must carry exactly the narrowing (no state, no parent, dismissed
 * included); a child page must have the hierarchy's page shape and is cut by
 * offset/limit. A caller that drops or mangles any of them gets a wrong
 * answer, never a lucky one.
 */
export function listFrom(world: QueueWorld) {
  return (p: ListArgs = {}): Promise<EnrichedInstanceSummary[]> => {
    if (p.activeDescendantAncestors !== undefined) {
      if (p.activeDescendantAncestors !== true || p.state || p.parent || p.includeDismissed !== true) {
        return Promise.reject(new Error('API 400: unexpected ancestor-discovery scope'))
      }
      return world.ancestors instanceof Error ? Promise.reject(world.ancestors) : Promise.resolve(world.ancestors)
    }
    if (p.parent) {
      const all = world.children[p.parent]
      if (!all) return notFound()
      if (p.limit !== CHILD_PAGE_LIMIT || p.includeDismissed !== true) {
        return Promise.reject(new Error('API 400: unexpected child page shape'))
      }
      const from = p.offset ?? 0
      return Promise.resolve(all.slice(from, from + CHILD_PAGE_LIMIT))
    }
    return Promise.resolve(p.state ? world.byState[p.state] ?? [] : [])
  }
}

/** graph-state keyed by instance id: any other id is a 404, so a pane fed the wrong id shows nothing. */
export function graphStateFrom(world: QueueWorld) {
  return (id: string): Promise<GraphState> => {
    const v = world.graphStates[id]
    if (v === undefined) return notFound()
    return v instanceof Error ? Promise.reject(v) : Promise.resolve(v)
  }
}

/** Every WebSocket a view opened, with a way to push a frame into it. */
export const sockets: Array<{ url: string; emit: (e: WebSocketEvent) => void }> = []

/** Browser WebSocket stand-in (install with vi.stubGlobal('WebSocket', StubSocket)). */
export class StubSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) {
    sockets.push({ url, emit: (e) => this.onmessage?.({ data: JSON.stringify(e) }) })
  }
  addEventListener() {}
  send() {}
  close() {}
}
```

- [x] Step 2: Write the failing component tests. In `dashboard/src/components/HierarchyRows.test.tsx` replace line 1 with:

```ts
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
```

replace line 13 with:

```ts
import { CHILD_PAGE_LIMIT, useRunHierarchy, type RunHierarchy } from '../hooks/useRunHierarchy'
```

and append after line 236:

```tsx
// ---------- refresh & load-more (P4-T10) ----------

function ControlledHarness({ rows, onHierarchy }: { rows: EnrichedInstanceSummary[]; onHierarchy: (h: RunHierarchy) => void }) {
  const hierarchy = useRunHierarchy()
  onHierarchy(hierarchy)
  return (
    <MemoryRouter>
      <table>
        <tbody>
          <HierarchyRows rows={rows} columns={COLUMNS} depth={0} timeMode="elapsed" authMode="local" hierarchy={hierarchy} />
        </tbody>
      </table>
    </MemoryRouter>
  )
}

// Stale cache fix without a flash: every queue refetch refreshes the open
// levels, so hiding children while their level reloads made an open tree
// blink (and lose the user's place) on every lifecycle event.
it('keeps showing the cached children while their level refreshes', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })])
  let h!: RunHierarchy
  render(<ControlledHarness rows={[row('p1', { childCount: 1 })]} onHierarchy={(x) => { h = x }} />)
  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  act(() => { h.invalidate() })

  expect(screen.getByText('wf-c1')).toBeTruthy()
  expect(screen.queryByText('Loading child runs…')).toBeNull()

  await act(async () => { resolve([row('c1', { parentInstanceId: 'p1', workflowName: 'wf-c1-v2' })]) })
  await waitFor(() => expect(screen.getByText('wf-c1-v2')).toBeTruthy())
})

it('renders load-more only while more children exist, disabled while its page is in flight', async () => {
  listMock.mockResolvedValueOnce(Array.from({ length: CHILD_PAGE_LIMIT }, (_, i) => row(`c${i}`, { parentInstanceId: 'p1' })))
  render(<Harness rows={[row('p1', { childCount: CHILD_PAGE_LIMIT + 1 })]} />)
  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  const more = await screen.findByRole('button', { name: 'Load more child runs' })

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  fireEvent.click(more)

  expect(await screen.findByRole('button', { name: 'Loading more child runs…' })).toBeDisabled()
  expect(listMock).toHaveBeenLastCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })

  await act(async () => { resolve([row('c100', { parentInstanceId: 'p1', workflowName: 'wf-c100' })]) })
  await waitFor(() => expect(screen.getByText('wf-c100')).toBeTruthy())
  expect(screen.queryByRole('button', { name: /Load more/ })).toBeNull()
  expect(screen.queryByText(/Showing/)).toBeNull()
})
```

- [x] Step 3: Write the failing route tests. Create `dashboard/src/routes/QueueView.groups.test.tsx` — the REAL `QueueView` renders; only `api.client` (keyed on every load-bearing argument via `queueWorld.fixtures`), the system WebSocket hook and the density preference are faked:

```tsx
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { CHILD_PAGE_LIMIT } from '../hooks/useRunHierarchy'
import { HIDDEN_ID, repeatedChildAggregate, summary } from '../graph/liveSummary.fixtures'
import { graphStateFrom, listFrom, localAdmin, newWorld, type QueueWorld } from './queueWorld.fixtures'

vi.mock('../hooks/useSystemWebSocket', () => ({ useSystemWebSocket: () => ({ connected: true }) }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const apiMock = vi.hoisted(() => ({
  me: vi.fn(),
  instances: {
    list: vi.fn(),
    graphState: vi.fn(),
    getNodeTrace: vi.fn(),
    dismiss: vi.fn(),
    restore: vi.fn(),
    dismissAll: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import QueueView from './QueueView'

let world: QueueWorld

beforeEach(() => {
  world = newWorld()
  apiMock.me.mockResolvedValue(localAdmin)
  apiMock.instances.list.mockImplementation((p) => listFrom(world)(p))
  apiMock.instances.graphState.mockImplementation((id: string) => graphStateFrom(world)(id))
  apiMock.instances.getNodeTrace.mockRejectedValue(new Error('API 404: not found'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

function renderQueue() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

const rowEl = (id: string) => document.querySelector<HTMLElement>(`tr[data-row-id="${id}"]`)
const cellOf = (id: string, col: string) => rowEl(id)?.querySelector(`td[data-col="${col}"]`)?.textContent
const summaryOf = (id: string) => within(rowEl(id)!).queryByTestId('hierarchy-active-summary')?.textContent ?? null

async function expandChildren(runName: string, firstChildId: string) {
  fireEvent.click(await screen.findByRole('button', { name: `Show child runs of ${runName}` }))
  await waitFor(() => expect(rowEl(firstChildId)).not.toBeNull())
}

describe('QueueView hierarchy — every fixed task is inspectable beyond the 100-row page (P4-T10)', () => {
  it('the 101st fixed task is reachable through load-more', async () => {
    world.byState.waiting = [summary({ id: 'p-big', runName: 'Big fan-out', state: 'waiting', childCount: 150 })]
    world.children['p-big'] = Array.from({ length: 150 }, (_, i) =>
      summary({ id: `c-${String(i).padStart(3, '0')}`, runName: `Task ${i}`, parentInstanceId: 'p-big', state: 'queued' }),
    )
    renderQueue()

    await expandChildren('Big fan-out', 'c-000')
    expect(rowEl('c-099')).not.toBeNull()
    expect(rowEl('c-100')).toBeNull()
    expect(screen.getByText('Showing 100 of 150 child runs')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Load more child runs' }))

    await waitFor(() => expect(rowEl('c-100')).not.toBeNull())
    expect(rowEl('c-149')).not.toBeNull()
    expect(rowEl('c-100')!.getAttribute('data-depth')).toBe('1')
    expect(apiMock.instances.list).toHaveBeenCalledWith({ parent: 'p-big', limit: CHILD_PAGE_LIMIT, offset: 100, includeDismissed: true })
    expect(screen.queryByRole('button', { name: 'Load more child runs' })).toBeNull()
    expect(screen.queryByText(/Showing \d+ of 150 child runs/)).toBeNull()
  })
})

describe('QueueView hierarchy — markers from the authorized aggregate (P4-T10)', () => {
  // Edge: duplicate node ids across repeated child workflows, paused tasks, redaction.
  it('repeated child workflows with the SAME node id each show their own state; paused work is summarised; a redacted task never renders', async () => {
    world.byState.waiting = [summary({ id: 'p-wf', runName: 'Repeat parent', state: 'waiting', childCount: 3 })]
    world.children['p-wf'] = [
      summary({ id: 'w1', runName: 'Task x', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'running' }),
      summary({ id: 'w2', runName: 'Task y', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'waiting' }),
      summary({ id: 'w3', runName: 'Task z', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'paused' }),
    ]
    world.graphStates['p-wf'] = repeatedChildAggregate()
    renderQueue()

    await expandChildren('Repeat parent', 'w1')

    await waitFor(() => expect(summaryOf('w1')).toBe('running: Build'))
    expect(summaryOf('w2')).toBe('waiting: Build')
    expect(summaryOf('w3')).toBe('paused')
    expect(within(rowEl('w1')!).getByText('workflow task')).toBeInTheDocument()
    expect(within(rowEl('w1')!).getByText('Fan out · x')).toBeInTheDocument()
    expect(within(rowEl('w2')!).getByText('Fan out · y')).toBeInTheDocument()
    expect(within(rowEl('w3')!).getByText('Fan out · z')).toBeInTheDocument()
    expect(cellOf('w3', 'status')).toBe('paused')
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-wf')
    expect(screen.queryByText('Fan out · q')).toBeNull()
    expect(document.body.textContent).not.toContain(HIDDEN_ID)
  })

  it('a level whose aggregate is unreadable stays listed and unannotated', async () => {
    world.byState.waiting = [summary({ id: 'p-wf', runName: 'Repeat parent', state: 'waiting', childCount: 1 })]
    world.children['p-wf'] = [summary({ id: 'w1', runName: 'Task x', parentInstanceId: 'p-wf' })]
    world.graphStates['p-wf'] = new Error('API 500: aggregate down')
    renderQueue()

    await expandChildren('Repeat parent', 'w1')

    await waitFor(() => expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-wf'))
    expect(within(rowEl('w1')!).queryByTestId('hierarchy-markers')).toBeNull()
    expect(screen.queryByText(/aggregate down/)).toBeNull()
  })
})
```

- [x] Step 4: Run them, confirm they fail.

```bash
cd dashboard && npx vitest run src/components/HierarchyRows.test.tsx src/routes/QueueView.groups.test.tsx
```
Expected:
```
 FAIL  src/components/HierarchyRows.test.tsx > keeps showing the cached children while their level refreshes
TestingLibraryElementError: Unable to find an element with the text: wf-c1
 FAIL  src/components/HierarchyRows.test.tsx > renders load-more only while more children exist, disabled while its page is in flight
TestingLibraryElementError: Unable to find role="button" and name "Load more child runs"
 FAIL  src/routes/QueueView.groups.test.tsx > QueueView hierarchy — every fixed task is inspectable beyond the 100-row page (P4-T10) > the 101st fixed task is reachable through load-more
TestingLibraryElementError: Unable to find role="button" and name "Load more child runs"
 FAIL  src/routes/QueueView.groups.test.tsx > QueueView hierarchy — markers from the authorized aggregate (P4-T10) > repeated child workflows …
AssertionError: expected null to be 'running: Build'
      Tests  4 failed | 14 passed (18)
```
(`a level whose aggregate is unreadable …` already passes: it pins the degradation once markers exist.)

- [x] Step 5: Implement the row. Replace the whole of `dashboard/src/components/QueueTableRow.tsx` (lines 1-164) with:

```tsx
import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import type { EnrichedInstanceSummary } from '../api/types'
import type { ColumnDef } from './queueColumns'
import { formatDuration, relativeTime, localDateTime, runDuration } from '../utils/time'
import styles from './QueueTableRow.module.css'

/** Horizontal indent applied per hierarchy level, in pixels. */
const INDENT_PX = 16

interface QueueTableRowProps {
  row: EnrichedInstanceSummary
  columns: ColumnDef[]
  timeMode: 'elapsed' | 'relative'
  /** Page clock (ms) from useNow; drives the ticking time cells. Defaults to Date.now(). */
  now?: number
  authMode: 'local' | 'org' | 'edge'
  dimmed?: boolean
  expanded?: boolean
  onExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  /** Nesting depth in the run hierarchy; 0 for a root row. */
  depth?: number
  /** Whether this row's children are currently shown. */
  hierarchyExpanded?: boolean
  /** Toggles this row's children. Absent when the row reports no children. */
  onToggleHierarchy?: (id: string) => void
  /** Hierarchy markers rendered after the tree cell's text (branch, group · task, non-blocking, active state). */
  badges?: ReactNode
  /** Work continuing independently after its group was decided: styled as non-blocking. */
  independent?: boolean
}

/**
 * QueueTableRow renders one enriched row's visible cells as a <tr>. The
 * submitter renders "you" in local mode and the display name in shared mode.
 * Elapsed is computed client-side from the page clock: created → now while
 * active, created → completed once terminal (frozen). Row click navigates to
 * the execution view; the expand affordance stops propagation so it does not
 * also navigate.
 */
export default function QueueTableRow({
  row: rowVal,
  columns,
  timeMode,
  now = Date.now(),
  authMode,
  dimmed,
  expanded,
  onExpand,
  onDismiss,
  onRestore,
  depth = 0,
  hierarchyExpanded = false,
  onToggleHierarchy,
  badges,
  independent = false,
}: QueueTableRowProps) {
  const navigate = useNavigate()
  const isTerminal = rowVal.state === 'failed' || rowVal.state === 'completed'

  function cellText(col: ColumnDef): string {
    if (col.key === 'submitter') {
      if (authMode === 'local') return 'you'
      return rowVal.submitterName ?? '—'
    }
    if (col.key === 'elapsed') {
      return timeMode === 'elapsed' ? formatDuration(runDuration(rowVal, now)) : relativeTime(rowVal.updatedAt, now)
    }
    if (col.key === 'submittedAt') {
      return localDateTime(rowVal.createdAt)
    }
    return col.accessor(rowVal)
  }

  return (
    <tr
      data-row-id={rowVal.id}
      data-depth={depth}
      data-independent={independent ? 'true' : undefined}
      className={`${styles.row} ${dimmed ? styles.dimmed : ''} ${independent ? styles.independent : ''}`}
      onClick={() => navigate(`/instances/${rowVal.id}`)}
      role="row"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          navigate(`/instances/${rowVal.id}`)
        }
      }}
    >
      {columns.map((col, i) => (
        <td key={col.key} className={styles.cell} data-col={col.key}>
          {i === 0 ? (
            <span className={styles.treeCell} style={{ paddingLeft: `${depth * INDENT_PX}px` }}>
              {onToggleHierarchy ? (
                <button
                  type="button"
                  className={styles.hierarchyToggle}
                  aria-label={`${hierarchyExpanded ? 'Hide' : 'Show'} child runs of ${rowVal.runName}`}
                  aria-expanded={hierarchyExpanded}
                  onClick={(e) => {
                    e.stopPropagation()
                    onToggleHierarchy(rowVal.id)
                  }}
                >
                  {hierarchyExpanded ? '▾' : '▸'}
                </button>
              ) : (
                <span className={styles.hierarchySpacer} aria-hidden="true" />
              )}
              {cellText(col)}
              {badges}
            </span>
          ) : col.key === 'git' && rowVal.gitRef ? (
            // gitRef is an author/run-controlled string; it is rendered as a JSX
            // text child and a plain `title` attribute only (no HTML sink).
            <span className={styles.gitRef} title={rowVal.gitRef}>
              {rowVal.gitRef}
            </span>
          ) : (
            cellText(col)
          )}
        </td>
      ))}
      <td className={styles.cell}>
        {isTerminal &&
          (dimmed ? (
            <button
              type="button"
              className={styles.expand}
              aria-label="Restore"
              title="Restore"
              onClick={(e) => {
                e.stopPropagation()
                onRestore?.(rowVal.id)
              }}
            >
              {'↺'}
            </button>
          ) : (
            onDismiss && (
              <button
                type="button"
                className={styles.expand}
                aria-label="Dismiss"
                title="Dismiss"
                onClick={(e) => {
                  e.stopPropagation()
                  onDismiss(rowVal.id)
                }}
              >
                {'×'}
              </button>
            )
          ))}
        <button
          type="button"
          className={styles.expand}
          aria-label={expanded ? 'Collapse' : 'Expand'}
          aria-expanded={expanded}
          onClick={(e) => {
            e.stopPropagation()
            onExpand?.(rowVal.id)
          }}
        >
          {expanded ? '▾' : '▸'}
        </button>
      </td>
    </tr>
  )
}
```

Append to `dashboard/src/components/QueueTableRow.module.css` (after line 73):

```css
/* Work continuing independently after its group was decided
   (finish_independently): non-blocking, so it reads as set apart from the
   blocking work its parent waits on. */
.independent > td:first-child {
  box-shadow: inset 2px 0 0 var(--state-waiting);
}

.independent {
  font-style: italic;
}
```

- [x] Step 6: Implement the level renderer. Replace the whole of `dashboard/src/components/HierarchyRows.tsx` (lines 1-159) with:

```tsx
import { Fragment, type ReactNode } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import type { HierarchyMarker } from '../graph/liveSummary'
import type { ColumnDef } from './queueColumns'
import type { RunHierarchy } from '../hooks/useRunHierarchy'
import QueueTableRow from './QueueTableRow'
import styles from './HierarchyRows.module.css'

interface HierarchyRowsProps {
  rows: EnrichedInstanceSummary[]
  columns: ColumnDef[]
  depth: number
  timeMode: 'elapsed' | 'relative'
  /** Page clock (ms) from useNow, passed down to rows. */
  now?: number
  authMode: 'local' | 'org' | 'edge'
  hierarchy: RunHierarchy
  detailExpandedId?: string | null
  onDetailExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  renderDetail?: (row: EnrichedInstanceSummary) => ReactNode
  /** QueueSection's detail-row class names, passed through so the existing
   *  framing survives the move out of QueueSection. */
  detailRowClassName?: string
  detailCellClassName?: string
  /** Markers of THIS level's rows (keyed by row id), built by the parent level
   *  from its authorized aggregate. Absent at the top level. */
  markers?: Record<string, HierarchyMarker>
  /** Terminal rows listed only because they still own active work (queue
   *  ancestor discovery); passed down so an intermediate ancestor is marked too. */
  ownsActiveWork?: ReadonlySet<string>
}

const KIND_TEXT: Partial<Record<HierarchyMarker['kind'], string>> = { branch: 'branch', workflow: 'workflow task' }

/**
 * The row's hierarchy markers. Task keys and labels are author-controlled
 * strings and render only as escaped JSX text.
 */
function RowMarkers({ marker, ownsActive }: { marker: HierarchyMarker | undefined; ownsActive: boolean }) {
  if (!marker && !ownsActive) return null
  const kindText = marker ? KIND_TEXT[marker.kind] : undefined
  return (
    <span className={styles.markers} data-testid="hierarchy-markers">
      {ownsActive && <span className={`${styles.marker} ${styles.markerOwner}`}>owns active work</span>}
      {marker && kindText && (
        <span className={`${styles.marker} ${marker.kind === 'branch' ? styles.markerBranch : styles.markerWorkflow}`}>
          {kindText}
        </span>
      )}
      {marker?.taskKey && (
        <span className={styles.marker}>{`${marker.groupLabel ?? marker.groupNodeId ?? 'group'} · ${marker.taskKey}`}</span>
      )}
      {marker?.independent && <span className={`${styles.marker} ${styles.markerIndependent}`}>non-blocking</span>}
      {marker?.activeSummary && (
        <span className={styles.markerActive} data-testid="hierarchy-active-summary">
          {marker.activeSummary}
        </span>
      )}
    </span>
  )
}

/**
 * HierarchyRows renders one level of the run tree and recurses into any
 * expanded row's children.
 *
 * Recursion lives here (rather than in QueueSection) so the section stays a
 * table-shell component. Depth is passed down purely for indentation; nothing
 * in the fetch or render path is depth-aware, so the tree opens to arbitrary
 * depth.
 *
 * Child rows are the SAME QueueTableRow as roots, so the per-row action set —
 * which is what lets operators inspect, retry, resume, or cancel at the CHILD
 * level rather than restarting a larger process — needs no per-level
 * special-casing. A child row additionally shows what it is to its parent
 * (branch / workflow task, group · task key, non-blocking) and what it is doing
 * now (active-state summary), from the parent's authorized aggregate.
 */
export default function HierarchyRows({
  rows,
  columns,
  depth,
  timeMode,
  now,
  authMode,
  hierarchy,
  detailExpandedId,
  onDetailExpand,
  onDismiss,
  onRestore,
  renderDetail,
  detailRowClassName,
  detailCellClassName,
  markers,
  ownsActiveWork,
}: HierarchyRowsProps) {
  return (
    <>
      {rows.map((inst) => {
        const total = inst.childCount ?? 0
        const hasChildren = total > 0
        const expanded = hierarchy.isExpanded(inst.id)
        const error = hierarchy.errorOf(inst.id)
        const loading = hierarchy.isLoading(inst.id)
        const loadingMore = hierarchy.isLoadingMore(inst.id)
        const more = hierarchy.hasMore(inst.id)
        const children = hierarchy.childrenOf(inst.id)
        const shown = children.length
        const marker = markers?.[inst.id]

        return (
          // A plain Fragment, not a <tbody> — this block recurses into its own
          // children's rows, and nesting a <tbody> inside another <tbody> is
          // invalid HTML that real browsers do not auto-correct when the DOM
          // is built via the JS API. The single owning <tbody> lives once, at
          // the call site in QueueSection.
          <Fragment key={inst.id}>
            <QueueTableRow
              row={inst}
              columns={columns}
              timeMode={timeMode}
              now={now}
              authMode={authMode}
              dimmed={inst.dismissed === true}
              expanded={detailExpandedId === inst.id}
              onExpand={onDetailExpand}
              onDismiss={onDismiss}
              onRestore={onRestore}
              depth={depth}
              hierarchyExpanded={expanded}
              // A row reporting no visible children renders NO expander, so a
              // parent whose children are all invisible to this caller leaks no
              // existence.
              onToggleHierarchy={hasChildren ? () => void hierarchy.toggle(inst.id) : undefined}
              badges={<RowMarkers marker={marker} ownsActive={ownsActiveWork?.has(inst.id) === true} />}
              independent={marker?.independent === true}
            />

            {detailExpandedId === inst.id && renderDetail && (
              <tr className={detailRowClassName}>
                <td colSpan={columns.length + 1} className={detailCellClassName}>
                  {renderDetail(inst)}
                </td>
              </tr>
            )}

            {/* First load only: a refresh keeps the cached rows on screen. */}
            {expanded && loading && shown === 0 && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelStatus}>
                  Loading child runs…
                </td>
              </tr>
            )}

            {/* A failure is confined to THIS row: siblings and the rest of the
                page are unaffected, and the last good rows stay listed. */}
            {expanded && error && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelError}>
                  {error}
                </td>
              </tr>
            )}

            {expanded && shown > 0 && (
              <HierarchyRows
                rows={children}
                columns={columns}
                depth={depth + 1}
                timeMode={timeMode}
                now={now}
                authMode={authMode}
                hierarchy={hierarchy}
                detailExpandedId={detailExpandedId}
                onDetailExpand={onDetailExpand}
                onDismiss={onDismiss}
                onRestore={onRestore}
                renderDetail={renderDetail}
                detailRowClassName={detailRowClassName}
                detailCellClassName={detailCellClassName}
                markers={hierarchy.markersOf(inst.id)}
                ownsActiveWork={ownsActiveWork}
              />
            )}

            {/* TRUNCATION IS STATED, NOT CLAMPED. The row's childCount comes
                from the server and counts every child the caller may see; the
                expansion is paged. When they disagree, say so and say by how
                much — clamping the badge would make them agree by LYING. */}
            {expanded && !loading && !error && shown < total && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelTruncated}>
                  Showing {shown} of {total} child runs
                </td>
              </tr>
            )}

            {/* Every fixed task must be inspectable: after a full page, offer
                the next one (also after a failed page, to retry it). */}
            {expanded && more && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.loadMore}>
                  <button
                    type="button"
                    className={styles.loadMoreButton}
                    disabled={loading || loadingMore}
                    onClick={() => void hierarchy.loadMore(inst.id)}
                  >
                    {loadingMore ? 'Loading more child runs…' : 'Load more child runs'}
                  </button>
                </td>
              </tr>
            )}
          </Fragment>
        )
      })}
    </>
  )
}
```

Append to `dashboard/src/components/HierarchyRows.module.css` (after line 18):

```css
/* Hierarchy markers: branch / workflow task, group · task key, non-blocking
   (finish_independently) work, the active-state summary, and "owns active
   work" on a terminal ancestor kept discoverable. */
.markers {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: 6px;
}

.marker {
  padding: 0 5px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  font-size: 10px;
  line-height: 16px;
  color: var(--text-secondary);
}

.markerBranch {
  border-color: var(--state-running);
  color: var(--state-running);
}

.markerWorkflow {
  border-color: var(--accent);
  color: var(--accent);
}

.markerIndependent {
  border-style: dashed;
  border-color: var(--state-waiting);
  color: var(--state-waiting);
}

.markerOwner {
  border-color: var(--state-completed);
  color: var(--state-completed);
}

.markerActive {
  font-size: 11px;
  color: var(--text-muted);
}

.loadMore {
  padding: 6px 10px;
}

.loadMoreButton {
  padding: 3px 12px;
  font-size: 12px;
  background: var(--bg-surface);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  color: var(--text-primary);
  cursor: pointer;
}

.loadMoreButton:disabled {
  opacity: 0.6;
  cursor: default;
}
```

- [x] Step 7: Run them, confirm they pass; run every queue suite and typecheck.

```bash
cd dashboard && npx vitest run src/components/HierarchyRows.test.tsx src/routes/QueueView.groups.test.tsx src/components/QueueTableRow.test.tsx src/components/QueueSection.test.tsx src/routes/QueueView.test.tsx src/routes/QueueView.hierarchypatch.test.tsx src/routes/QueueView.livepatch.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  7 passed (7)
```

- [x] Step 8: Commit.

```bash
git add dashboard/src/components/QueueTableRow.tsx dashboard/src/components/QueueTableRow.module.css dashboard/src/components/HierarchyRows.tsx dashboard/src/components/HierarchyRows.module.css dashboard/src/components/HierarchyRows.test.tsx dashboard/src/routes/queueWorld.fixtures.ts dashboard/src/routes/QueueView.groups.test.tsx && git commit -m "feat(dashboard): hierarchy branch/task/non-blocking markers, load-more and flash-free refresh" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.5: `RunLivePane` mounted in the queue route's row detail
**Files:**
- Create: `dashboard/src/components/RunLivePane.tsx`, `dashboard/src/components/RunLivePane.module.css`
- Modify: `dashboard/src/components/QueueSection.tsx:8` (import), `:31-35` (prop), `:75` (destructure), `:238-260` (`HierarchyRows` call)
- Test: `dashboard/src/components/QueueSection.test.tsx` — replace `:1-7`, append after `:275`
- Test: `dashboard/src/routes/QueueView.livepane.test.tsx` (new)

- [x] Step 1: Write the failing route tests. Create `dashboard/src/routes/QueueView.livepane.test.tsx`. The REAL `QueueView` → `QueueSection` → `HierarchyRows` → `RunLivePane` → `useGraphState` → `useInstanceWebSocket` chain runs; only `api.client` (graph-state keyed by instance id — every other id is a 404), the browser `WebSocket` (URL recorder) and React Flow (nodes rendered as clickable divs exposing `data-state`, as in P4-T8's `ExecutionView.groups.test.tsx`) are faked:

```tsx
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { fanoutAggregate, fanoutContexts, panel, summary, task } from '../graph/liveSummary.fixtures'
import { graphStateFrom, listFrom, localAdmin, newWorld, sockets, StubSocket, type QueueWorld } from './queueWorld.fixtures'

// React Flow stub: every rendered node becomes a clickable div exposing its
// type and execution state, so the test reads what the graph would highlight.
vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: Array<{ id: string; type?: string; selected?: boolean; data: Record<string, unknown> }>
    onNodeClick?: (e: unknown, n: unknown) => void
    children?: React.ReactNode
  }) => (
    <div>
      {props.nodes.map((n) => (
        <div
          key={n.id}
          data-testid={`node-${n.id}`}
          data-type={n.type}
          data-state={String(n.data.executionState ?? '')}
          data-selected={n.selected ? 'true' : 'false'}
          onClick={() => props.onNodeClick?.({}, n)}
        />
      ))}
      {props.children}
    </div>
  ),
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: vi.fn(),
    getZoom: () => 1,
    fitBounds: vi.fn(() => Promise.resolve(true)),
  }),
}))

vi.mock('../hooks/useSystemWebSocket', () => ({ useSystemWebSocket: () => ({ connected: true }) }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const apiMock = vi.hoisted(() => ({
  me: vi.fn(),
  instances: {
    list: vi.fn(),
    graphState: vi.fn(),
    getNodeTrace: vi.fn(),
    dismiss: vi.fn(),
    restore: vi.fn(),
    dismissAll: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import QueueView from './QueueView'

let world: QueueWorld

beforeEach(() => {
  sockets.length = 0
  vi.stubGlobal('WebSocket', StubSocket)
  world = newWorld()
  // The fan-out run waits at its group while both branches run. The branch
  // runs are ALSO in the running list (the per-state lists are not
  // parent-filtered); topLevelRows nests them under the root, so the root is
  // the only top-level row of the Active section.
  world.byState.waiting = [summary({ id: 'root', runName: 'Fan-out run', workflowName: 'fanout', state: 'waiting', childCount: 2 })]
  world.byState.running = [
    summary({ id: 'br-a', runName: 'Branch a', workflowName: 'fanout', parentInstanceId: 'root' }),
    summary({ id: 'br-b', runName: 'Branch b', workflowName: 'fanout', parentInstanceId: 'root' }),
  ]
  world.graphStates.root = fanoutAggregate()
  apiMock.me.mockResolvedValue(localAdmin)
  apiMock.instances.list.mockImplementation((p) => listFrom(world)(p))
  apiMock.instances.graphState.mockImplementation((id: string) => graphStateFrom(world)(id))
  apiMock.instances.getNodeTrace.mockRejectedValue(new Error('API 404: not found'))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function renderQueue() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

const rowEl = (id: string) => document.querySelector<HTMLElement>(`tr[data-row-id="${id}"]`)

/** Opens the queue row's detail — the only place the live-run pane is mounted. */
async function openLivePane(id: string): Promise<HTMLElement> {
  await waitFor(() => expect(rowEl(id)).not.toBeNull())
  fireEvent.click(within(rowEl(id)!).getByRole('button', { name: 'Expand' }))
  return screen.findByTestId('run-live-pane')
}

const stateIn = (pane: HTMLElement, nodeId: string) => within(pane).getByTestId(`node-${nodeId}`).getAttribute('data-state')
const activeItems = (pane: HTMLElement) => within(pane).getAllByTestId('run-live-active-item').map((li) => li.textContent)

describe('QueueView live-run pane — aggregate graph (happy path + provenance)', () => {
  // The root's own cursor knows nothing of its branches: only the aggregate
  // can show a2 AND b1 running at the same time.
  it('shows BOTH concurrently running branch nodes as running in the queue view', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    await waitFor(() => expect(stateIn(pane, 'a2')).toBe('running'))
    expect(stateIn(pane, 'b1')).toBe('running')
    expect(stateIn(pane, 'a1')).toBe('completed')
    expect(within(pane).getByTestId('node-group:g:header')).toBeInTheDocument()
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a', 'B1 · running · task b'])
  })

  // Provenance: fails if the pane is not mounted in the route's row detail, or
  // if useGraphState is fed an empty or wrong id (the fake 404s every other id,
  // and useGraphState('') never fetches or subscribes).
  it("feeds the pane the expanded row's instance id: its graph-state and its SUBTREE socket", async () => {
    renderQueue()
    const pane = await openLivePane('root')
    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('running'))

    expect(apiMock.instances.graphState).toHaveBeenCalledWith('root')
    expect(apiMock.instances.graphState.mock.calls.every(([id]) => id === 'root')).toBe(true)
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))).toBe(true)
    expect(within(pane).getByRole('link', { name: 'Open run view' })).toHaveAttribute('href', '/instances/root')
  })
})

describe('QueueView live-run pane — instance-qualified selection and group panel (success)', () => {
  it('selecting a branch-body node points at the OWNING branch run, not the queue row', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-b1'))

    const sel = await within(pane).findByTestId('run-live-selection')
    expect(sel).toHaveTextContent('B1')
    expect(sel).toHaveTextContent('branch task b')
    expect(within(sel).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/br-b')
    expect(within(pane).getByTestId('node-b1').getAttribute('data-selected')).toBe('true')
  })

  // Branch rows are non-root: they are linked from their group.
  it('header and footer open the same group panel, which links each branch run from its group', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-group:g:header'))
    const fromHeader = await within(pane).findByTestId('group-panel')
    expect(within(fromHeader).getByText('Fan out')).toBeInTheDocument()
    expect(within(fromHeader).getByText('All succeed')).toBeInTheDocument()
    expect(within(fromHeader).getByText('Waiting for tasks a, b')).toBeInTheDocument()
    expect(within(within(fromHeader).getByTestId('group-task-row-a')).getByRole('link', { name: 'open run' })).toHaveAttribute('href', '/instances/br-a')
    expect(within(within(fromHeader).getByTestId('group-task-row-b')).getByRole('link', { name: 'open run' })).toHaveAttribute('href', '/instances/br-b')

    fireEvent.click(within(pane).getByTestId('node-group:g:footer'))
    expect(within(await within(pane).findByTestId('group-panel')).getByText('All succeed')).toBeInTheDocument()
  })
})

describe('QueueView live-run pane — redaction and errors', () => {
  it('a redacted branch is a hidden dependency: unknown state, not listed as active, its id never rendered', async () => {
    world.byState.running = [summary({ id: 'br-a', runName: 'Branch a', workflowName: 'fanout', parentInstanceId: 'root' })]
    world.graphStates.root = fanoutAggregate({
      contexts: fanoutContexts().slice(0, 2),
      groups: [panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] })],
    })
    renderQueue()
    const pane = await openLivePane('root')

    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('unknown'))
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a'])
    fireEvent.click(within(pane).getByTestId('node-group:g:header'))
    const rowB = within(await within(pane).findByTestId('group-panel')).getByTestId('group-task-row-b')
    expect(within(rowB).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(rowB).queryByRole('link')).toBeNull()
    expect(pane.textContent).not.toContain('br-b')
  })

  it('a graph-state failure is confined to the pane: the message shows and the queue rows stay', async () => {
    world.graphStates.root = new Error('API 500: boom')
    renderQueue()
    const pane = await openLivePane('root')

    expect(await within(pane).findByTestId('run-live-error')).toHaveTextContent('Live view unavailable: API 500: boom')
    expect(within(pane).queryByTestId('node-a2')).toBeNull()
    expect(rowEl('root')).not.toBeNull()
  })

  it('an unreadable root snapshot shows the active nodes without drawing an empty graph', async () => {
    const [root, ...rest] = fanoutContexts()
    world.graphStates.root = fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest], warnings: ['workflow snapshot of run root could not be read'] })
    renderQueue()
    const pane = await openLivePane('root')

    expect(await within(pane).findByText('Workflow graph unavailable for this run.')).toBeInTheDocument()
    expect(within(pane).getByText('workflow snapshot of run root could not be read')).toBeInTheDocument()
    expect(within(pane).getByTestId('run-live-active')).toHaveTextContent('Active now (3)')
    expect(within(pane).queryByTestId('node-a2')).toBeNull()
  })
})

describe('QueueView live-run pane — live refresh', () => {
  it('a subtree event refreshes the pane: a finished branch flips while the other keeps running', async () => {
    renderQueue()
    const pane = await openLivePane('root')
    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('running'))

    world.graphStates.root = fanoutAggregate({
      version: 'v2',
      contexts: fanoutContexts('completed'),
      groups: [panel({ tasks: [task('a'), task('b', { liveStatus: 'completed' })] })],
    })
    const subtree = sockets.find((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))!
    act(() => subtree.emit({ type: 'group.task_updated', timestamp: '2026-10-16T10:01:00Z', data: {}, sourceInstanceId: 'br-b' }))

    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('completed'))
    expect(stateIn(pane, 'a2')).toBe('running')
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a'])
  })
})
```

- [x] Step 2: Write the failing component probe. In `dashboard/src/components/QueueSection.test.tsx` replace lines 1-7 with:

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'
import type { EnrichedInstanceSummary } from '../api/types'
import { sockets, StubSocket } from '../routes/queueWorld.fixtures'

// graphState never settles by default: the row detail now mounts the live-run
// pane, and these table tests are about the table, not the pane.
vi.mock('../api/client', () => ({
  api: { instances: { getNodeTrace: vi.fn(), list: vi.fn(), graphState: vi.fn(() => new Promise<never>(() => {})) } },
}))

beforeEach(() => {
  sockets.length = 0
  vi.stubGlobal('WebSocket', StubSocket)
})
afterEach(() => vi.unstubAllGlobals())
```

and append after line 275:

```tsx
describe('QueueSection — live-run pane in the row detail (P4-T10)', () => {
  afterEach(async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.graphState).mockImplementation(() => new Promise<never>(() => {}))
  })

  it("expanding a row mounts the live-run pane fed with THAT row's instance id", async () => {
    const { api } = await import('../api/client')
    vi.mocked(api.instances.graphState).mockImplementation((id: string) =>
      Promise.reject(new Error(id === 'i1' ? 'API 404: instance not found' : `wrong id ${id}`)),
    )
    renderSection({ instances: [summary({ id: 'i1' })] })

    fireEvent.click(screen.getByRole('button', { name: /expand/i }))

    expect(await screen.findByTestId('run-live-pane')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('run-live-error')).toHaveTextContent('API 404: instance not found'))
    expect(vi.mocked(api.instances.graphState)).toHaveBeenCalledWith('i1')
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/i1/ws?subtree=1'))).toBe(true)
  })
})
```

- [x] Step 3: Run them, confirm they fail.

```bash
cd dashboard && npx vitest run src/routes/QueueView.livepane.test.tsx src/components/QueueSection.test.tsx
```
Expected:
```
 FAIL  src/routes/QueueView.livepane.test.tsx > QueueView live-run pane — aggregate graph (happy path + provenance) > shows BOTH concurrently running branch nodes as running in the queue view
TestingLibraryElementError: Unable to find an element by: [data-testid="run-live-pane"]
...
 FAIL  src/components/QueueSection.test.tsx > QueueSection — live-run pane in the row detail (P4-T10) > expanding a row mounts the live-run pane fed with THAT row's instance id
TestingLibraryElementError: Unable to find an element by: [data-testid="run-live-pane"]
      Tests  9 failed | … passed
```

- [x] Step 4: Implement the pane. Create `dashboard/src/components/RunLivePane.tsx`:

```tsx
import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ReactFlowProvider } from '@xyflow/react'
import type { NodeExecution } from '../api/types'
import { useGraphState } from '../hooks/useGraphState'
import { findGroupPanel, type GraphSelection } from '../graph/aggregateState'
import { activeNodesOf, EMPTY_DEFINITION, rootDefinitionOf } from '../graph/liveSummary'
import { GraphView } from '../routes/ExecutionView'
import GroupPanel from './GroupPanel'
import styles from './RunLivePane.module.css'

const NO_TRACE: NodeExecution[] = []

interface RunLivePaneProps {
  /** The queue row's run; it and every descendant the caller may see are shown. */
  instanceId: string
}

/**
 * RunLivePane is the queue's live-run view of one row: the AUTHORIZED aggregate
 * of the run and its descendants (P4-T6 useGraphState — subtree events,
 * bounded polling while active, reconnect resync), drawn with the same
 * GraphView/group layout as the run view (P4-T7/P4-T8), an instance-qualified
 * list of every concurrently active node, and the group panel of a selected
 * group header/footer. Selecting a step names the instance that OWNS it and
 * links to that run, never to whichever row was expanded.
 */
export default function RunLivePane({ instanceId }: RunLivePaneProps) {
  const graph = useGraphState(instanceId)
  const navigate = useNavigate()
  const [selection, setSelection] = useState<GraphSelection | null>(null)
  const state = graph.state
  const def = useMemo(() => rootDefinitionOf(state), [state])
  // Active nodes are listed even when the root snapshot is unreadable (labels
  // then fall back to each context's own snapshot, else the node id).
  const active = useMemo(() => (state ? activeNodesOf(state, def ?? EMPTY_DEFINITION) : []), [state, def])
  const labelOf = (nodeId: string) => def?.nodes.find((n) => n.id === nodeId)?.label || nodeId

  let detail: ReactNode = null
  if (state && selection?.kind === 'group') {
    detail = (
      <GroupPanel
        panel={findGroupPanel(state, selection.instanceId, selection.nodeId)}
        label={labelOf(selection.nodeId)}
        // A resolution session belongs to the group's parent instance: open
        // that run, whose view routes the session through its own stream.
        onOpenSession={(s) => navigate(`/instances/${s.instanceId}`)}
      />
    )
  } else if (state && selection?.kind === 'node') {
    const owner = selection.instanceId ? state.contexts.find((c) => c.instanceId === selection.instanceId) ?? null : null
    detail = (
      <div className={styles.selection} data-testid="run-live-selection">
        <span className={styles.selectionLabel}>{labelOf(selection.nodeId)}</span>
        {!selection.instanceId || !owner || owner.redacted ? (
          <span className={styles.muted}>No visible run for this step (not started, or hidden)</span>
        ) : (
          <>
            <span className={styles.muted}>
              {owner.kind === 'root' ? 'this run' : `${owner.kind}${owner.taskKey ? ` task ${owner.taskKey}` : ''}`}
            </span>
            <Link to={`/instances/${owner.instanceId}`} className={styles.link}>
              Open run
            </Link>
          </>
        )}
      </div>
    )
  }

  return (
    <div className={styles.pane} data-testid="run-live-pane">
      <div className={styles.header}>
        <span className={styles.title}>Live run</span>
        {state && <span className={styles.muted}>{state.active ? 'active' : 'finished'}</span>}
        <span
          className={`${styles.conn} ${graph.connected ? styles.connOn : ''}`}
          title={graph.connected ? 'Live updates connected' : 'Live updates not connected'}
        />
        <div className={styles.spacer} />
        <Link to={`/instances/${instanceId}`} className={styles.link}>
          Open run view
        </Link>
      </div>

      {graph.error && (
        <div className={styles.error} data-testid="run-live-error">
          {state ? 'Live refresh failed: ' : 'Live view unavailable: '}
          {graph.error}
        </div>
      )}
      {!state && !graph.error && <div className={styles.muted}>Loading live state…</div>}

      {state &&
        (state.warnings ?? []).map((w) => (
          <div key={w} className={styles.warning}>
            {w}
          </div>
        ))}

      {state && (
        <div className={styles.active} data-testid="run-live-active">
          <span className={styles.label}>Active now ({active.length})</span>
          {active.length === 0 ? (
            <span className={styles.muted}>nothing running</span>
          ) : (
            <ul className={styles.activeList}>
              {active.map((a) => (
                <li key={a.key} data-testid="run-live-active-item" className={a.independent ? styles.independent : undefined}>
                  {`${a.label} · ${a.state}${a.taskKey ? ` · task ${a.taskKey}` : ''}${a.independent ? ' · non-blocking' : ''}`}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {state && def && (
        <div className={styles.body}>
          <div className={styles.graph}>
            <ReactFlowProvider>
              <GraphView
                instance={null}
                workflowDef={def}
                trace={NO_TRACE}
                graphState={state}
                onSelect={setSelection}
                selectedNodeId={selection?.rfId ?? null}
              />
            </ReactFlowProvider>
          </div>
          {detail && <div className={styles.detail}>{detail}</div>}
        </div>
      )}
      {state && !def && <div className={styles.muted}>Workflow graph unavailable for this run.</div>}
    </div>
  )
}
```

Create `dashboard/src/components/RunLivePane.module.css`:

```css
.pane {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 16px 14px;
  border-top: 1px solid var(--border-subtle);
}

.header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.title {
  font-weight: 600;
  color: var(--text-primary);
}

.spacer {
  flex: 1;
}

.muted {
  font-size: 12px;
  color: var(--text-muted);
}

.conn {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--state-queued);
}

.connOn {
  background: var(--state-completed);
}

.link {
  font-size: 12px;
  color: var(--accent);
  text-decoration: none;
}

.link:hover {
  color: var(--accent-hover);
  text-decoration: underline;
}

.error {
  font-size: 12px;
  color: var(--state-failed);
}

.warning {
  font-size: 12px;
  color: var(--state-waiting);
}

.active {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
}

.label {
  font-size: 10px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--text-muted);
}

.activeList {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.activeList li {
  padding: 1px 8px;
  border-radius: var(--radius-sm);
  background: var(--bg-hover);
  color: var(--text-secondary);
}

.activeList li.independent {
  border: 1px dashed var(--state-waiting);
  font-style: italic;
}

.body {
  display: flex;
  gap: 8px;
  height: 360px;
  min-height: 0;
}

.graph {
  display: flex;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
}

.detail {
  flex-shrink: 0;
  width: 360px;
  overflow: auto;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
}

.selection {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  font-size: 12px;
}

.selectionLabel {
  font-weight: 600;
  color: var(--text-primary);
}
```

- [x] Step 5: Mount it in the queue section. In `dashboard/src/components/QueueSection.tsx`:

After line 8 (`import RowDetailPanel from './RowDetailPanel'`) add:

```tsx
import RunLivePane from './RunLivePane'
```

Replace lines 31-35 (the `registerRowPatcher` prop and the closing brace of `QueueSectionProps`) with:

```tsx
  /** Registers this section's hierarchy row patcher with the caller, which
   *  feeds it live node events so expanded children update without a refetch.
   *  Returns the unregister function. */
  registerRowPatcher?: (patch: RunHierarchy['patchRow']) => () => void
  /** Terminal rows listed in this section only because they still own active
   *  descendant work (QueueView's ancestor discovery); marked in the row. */
  ownsActiveWork?: ReadonlySet<string>
}
```

Replace line 75 (`  registerRowPatcher,` in the destructuring) with:

```tsx
  registerRowPatcher,
  ownsActiveWork,
```

Replace lines 238-260 (the `<HierarchyRows … />` element) with:

```tsx
                <HierarchyRows
                  rows={roots}
                  columns={columns}
                  depth={0}
                  timeMode={timeMode}
                  now={now}
                  authMode={authMode}
                  hierarchy={hierarchy}
                  detailExpandedId={expandedId}
                  onDetailExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                  onDismiss={onDismiss}
                  onRestore={onRestore}
                  ownsActiveWork={ownsActiveWork}
                  detailRowClassName={styles.detailRow}
                  detailCellClassName={styles.detailCell}
                  renderDetail={(inst) => (
                    <>
                      <RowDetailPanel
                        row={inst}
                        authMode={authMode}
                        errorText={errorText}
                        errorLoading={errorLoading}
                      />
                      {/* The queue live-run pane: the aggregate graph of THIS
                          row's run and every visible descendant. Mounted for
                          every expanded row at any depth. */}
                      <RunLivePane instanceId={inst.id} />
                    </>
                  )}
                />
```

- [x] Step 6: Run them, confirm they pass; run every queue suite and typecheck.

```bash
cd dashboard && npx vitest run src/routes/QueueView.livepane.test.tsx src/components/QueueSection.test.tsx src/routes/QueueView.groups.test.tsx src/routes/QueueView.test.tsx src/routes/QueueView.hierarchypatch.test.tsx src/routes/QueueView.livepatch.test.tsx src/routes/ExecutionView.groups.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  7 passed (7)
```

- [x] Step 7: Commit.

The supplied never-settling default `graphState` mock also blocked existing hierarchy table tests after 10.3's parallel marker fetch. Changed that table-only default to an explicit 404 so both consumers settle honestly; the pane probe still asserts the exact row id and subtree URL. All seven specified suites and typecheck pass. Spec review kept the planned pane scope; anti-theater review confirmed route-rendered node states, child links, redaction and event refresh; quality review checked the neighboring ExecutionView/GroupPanel composition.

```bash
git add dashboard/src/components/RunLivePane.tsx dashboard/src/components/RunLivePane.module.css dashboard/src/components/QueueSection.tsx dashboard/src/components/QueueSection.test.tsx dashboard/src/routes/QueueView.livepane.test.tsx && git commit -m "feat(dashboard): queue live-run pane with the aggregate graph, active nodes and group panel" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.6: `QueueView` — ancestor discoverability, group-event refresh, bounded polling
**Files:**
- Create: `dashboard/src/routes/queueRefresh.ts`
- Test: `dashboard/src/routes/queueRefresh.test.ts` (new)
- Modify: `dashboard/src/routes/QueueView.tsx:1-332` (whole file)
- Test: `dashboard/src/routes/QueueView.groups.test.tsx` — replace the header (everything above `function renderQueue`), append at the end
- Test: `dashboard/src/routes/QueueView.test.tsx:128-134` (tighten the show-dismissed assertion to the terminal-state calls; the new ancestor read always sends `includeDismissed: true`, which would otherwise satisfy it vacuously)

- [x] Step 1: Write the failing refresh-policy tests `dashboard/src/routes/queueRefresh.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { QUEUE_POLL_MS, isQueueRefreshEvent } from './queueRefresh'

describe('isQueueRefreshEvent', () => {
  it.each([
    'instance.created',
    'instance.started',
    'instance.completed',
    'instance.failed',
    'instance.cancelled',
    'instance.paused',
    'instance.resumed',
    'instance.retrying',
    'instance.recovered',
    'instance.intervened',
  ])('refetches on lifecycle event %s', (type) => {
    expect(isQueueRefreshEvent(type)).toBe(true)
  })

  // Group lifecycle changes task rows, decisions and independent work without
  // any instance event on the expanded parent.
  it.each([
    'group.launched',
    'group.task_updated',
    'group.decided',
    'group.gathering',
    'group.conflict',
    'group.resolved',
    'group.completed',
  ])('refetches on group event %s', (type) => {
    expect(isQueueRefreshEvent(type)).toBe(true)
  })

  // node.started/failed PATCH rows in place; chat traffic never changes rows;
  // never-emitted event names are not listened for.
  it.each(['node.started', 'node.failed', 'node.completed', 'interactive.token', 'interactive.ended', 'group.', 'instance.state_changed', 'variables.updated', ''])(
    'does not refetch on %s',
    (type) => {
      expect(isQueueRefreshEvent(type)).toBe(false)
    },
  )
})

describe('QUEUE_POLL_MS', () => {
  it('is a bounded 10 s interval', () => {
    expect(QUEUE_POLL_MS).toBe(10_000)
  })
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/routes/queueRefresh.test.ts
```
Expected:
```
 FAIL  src/routes/queueRefresh.test.ts
Error: Failed to resolve import "./queueRefresh" from "src/routes/queueRefresh.test.ts". Does the file exist?
 Test Files  1 failed (1)
```

- [x] Step 3: Implement `dashboard/src/routes/queueRefresh.ts`:

```ts
/**
 * Bounded periodic queue refetch while any run is active or pending. Events
 * are not enough on their own: the bus drops when buffers fill, several
 * transitions have no emitter, and a dropped final event would otherwise leave
 * an expanded hierarchy level stale forever. Each refetch also refreshes every
 * open hierarchy level (QueueSection.refreshSignal -> useRunHierarchy.invalidate).
 */
export const QUEUE_POLL_MS = 10_000

/** Instance lifecycle events (existing emitters) that change which rows exist or their state. */
const LIFECYCLE_EVENTS = new Set([
  'instance.created',
  'instance.started',
  'instance.completed',
  'instance.failed',
  'instance.cancelled',
  'instance.paused',
  'instance.resumed',
  'instance.retrying',
  'instance.recovered',
  'instance.intervened',
])

const GROUP_PREFIX = 'group.'

/**
 * Whether a system-stream event should refetch the queue (and so refresh the
 * open hierarchy levels). Group lifecycle events (`group.launched`,
 * `group.task_updated`, `group.decided`, `group.gathering`, `group.conflict`,
 * `group.resolved`, `group.completed`) change task rows, markers and
 * independent work with no instance event on the expanded parent. node.started
 * / node.failed are NOT refetches — QueueView patches those rows in place.
 */
export function isQueueRefreshEvent(type: string): boolean {
  if (LIFECYCLE_EVENTS.has(type)) return true
  return type.startsWith(GROUP_PREFIX) && type.length > GROUP_PREFIX.length
}
```

- [x] Step 4: Run it, confirm it passes.

```bash
cd dashboard && npx vitest run src/routes/queueRefresh.test.ts
```
Expected:
```
 Test Files  1 passed (1)
      Tests  27 passed (27)
```

- [x] Step 5: Commit.

```bash
git add dashboard/src/routes/queueRefresh.ts dashboard/src/routes/queueRefresh.test.ts && git commit -m "feat(dashboard): queue refresh policy (group lifecycle events, bounded poll interval)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [x] Step 6: Write the failing route tests. In `dashboard/src/routes/QueueView.groups.test.tsx` replace the header (from line 1 down to, but not including, `function renderQueue()`) with:

```tsx
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WebSocketEvent } from '../api/types'
import { CHILD_PAGE_LIMIT } from '../hooks/useRunHierarchy'
import { HIDDEN_ID, independentAggregate, repeatedChildAggregate, summary } from '../graph/liveSummary.fixtures'
import { graphStateFrom, listFrom, localAdmin, newWorld, type QueueWorld } from './queueWorld.fixtures'
import { QUEUE_POLL_MS } from './queueRefresh'

let systemHandler: ((e: WebSocketEvent) => void) | null = null
vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    systemHandler = cb
    return { connected: true }
  },
}))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const apiMock = vi.hoisted(() => ({
  me: vi.fn(),
  instances: {
    list: vi.fn(),
    graphState: vi.fn(),
    getNodeTrace: vi.fn(),
    dismiss: vi.fn(),
    restore: vi.fn(),
    dismissAll: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import QueueView from './QueueView'

let world: QueueWorld

beforeEach(() => {
  world = newWorld()
  systemHandler = null
  apiMock.me.mockResolvedValue(localAdmin)
  apiMock.instances.list.mockImplementation((p) => listFrom(world)(p))
  apiMock.instances.graphState.mockImplementation((id: string) => graphStateFrom(world)(id))
  apiMock.instances.getNodeTrace.mockRejectedValue(new Error('API 404: not found'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

```

and append at the end of the file:

```tsx
describe('QueueView — completed ancestors that own active independent work stay discoverable (P4-T10)', () => {
  // Edge: independent work after parent completion stays visible and live in
  // the queue. The completed parent is outside the Completed window/dismissed
  // (world.byState.completed is empty), so only ancestor discovery lists it.
  it('a completed parent with an active independent branch is listed, and the branch nests under it (non-root)', async () => {
    world.byState.running = [summary({ id: 'c-ind', runName: 'Independent task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'running' })]
    world.ancestors = [summary({ id: 'p-done', runName: 'Done parent', workflowName: 'fanout', state: 'completed', childCount: 2, completedAt: '2026-10-16T10:05:00Z' })]
    world.children['p-done'] = [
      summary({ id: 'c-ind', runName: 'Independent task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'running' }),
      summary({ id: 'c-win', runName: 'Winner task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'completed' }),
    ]
    world.graphStates['p-done'] = independentAggregate()
    renderQueue()

    await waitFor(() => expect(rowEl('p-done')).not.toBeNull())
    expect(apiMock.instances.list).toHaveBeenCalledWith({ activeDescendantAncestors: true, includeDismissed: true })
    expect(cellOf('p-done', 'status')).toBe('completed')
    expect(within(rowEl('p-done')!).getByText('owns active work')).toBeInTheDocument()
    // Non-root: the branch is not a top-level row; it is reached from its parent.
    expect(rowEl('c-ind')).toBeNull()

    await expandChildren('Done parent', 'c-ind')

    const ind = rowEl('c-ind')!
    expect(ind.getAttribute('data-depth')).toBe('1')
    expect(ind.getAttribute('data-independent')).toBe('true')
    expect(within(ind).getByText('branch')).toBeInTheDocument()
    expect(within(ind).getByText('Fan out · a')).toBeInTheDocument()
    expect(within(ind).getByText('non-blocking')).toBeInTheDocument()
    await waitFor(() => expect(summaryOf('c-ind')).toBe('running: A2'))
    const win = rowEl('c-win')!
    expect(within(win).getByText('Fan out · b')).toBeInTheDocument()
    expect(within(win).queryByText('non-blocking')).toBeNull()
    expect(win.getAttribute('data-independent')).toBeNull()
    expect(summaryOf('c-win')).toBeNull()
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-done')
  })

  // Error: discovery failing must not take the queue down; the active child is
  // never lost (with no parent row it stays at top level).
  it('an ancestor-discovery failure is reported without breaking the queue', async () => {
    world.byState.running = [summary({ id: 'c-ind', runName: 'Independent task', parentInstanceId: 'p-done', state: 'running' })]
    world.ancestors = new Error('API 500: ancestors down')
    renderQueue()

    expect(await screen.findByTestId('ancestor-warning')).toHaveTextContent(
      "Couldn't load completed runs that still own active work: API 500: ancestors down",
    )
    expect(rowEl('c-ind')).not.toBeNull()
    expect(screen.queryByText(/Failed to load instances/)).toBeNull()
  })
})

describe('QueueView — open hierarchy levels refresh (stale cache, P4-T10)', () => {
  function seedOpenLevel() {
    world.byState.waiting = [summary({ id: 'p-run', runName: 'Running parent', state: 'waiting', childCount: 1 })]
    world.children['p-run'] = [summary({ id: 'c1', runName: 'Child one', parentInstanceId: 'p-run', state: 'running' })]
  }
  function finishChild() {
    world.children['p-run'] = [
      summary({ id: 'c1', runName: 'Child one', parentInstanceId: 'p-run', state: 'completed', completedAt: '2026-10-16T10:02:00Z' }),
    ]
  }

  it('an open level refreshes on a group lifecycle event and stays expanded', async () => {
    seedOpenLevel()
    renderQueue()
    await expandChildren('Running parent', 'c1')
    expect(cellOf('c1', 'status')).toBe('running')

    finishChild()
    act(() => systemHandler!({ type: 'group.task_updated', timestamp: '2026-10-16T10:02:00Z', instanceId: 'p-run', data: {} }))

    await waitFor(() => expect(cellOf('c1', 'status')).toBe('completed'))
    expect(screen.getByRole('button', { name: 'Hide child runs of Running parent' })).toBeInTheDocument()
  })

  // Error: the final event was dropped (no event at all) — bounded polling recovers it.
  it('bounded polling refreshes an open level when the final event was dropped', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    seedOpenLevel()
    renderQueue()
    await expandChildren('Running parent', 'c1')
    expect(cellOf('c1', 'status')).toBe('running')

    finishChild()
    const before = apiMock.instances.list.mock.calls.length
    act(() => {
      vi.advanceTimersByTime(QUEUE_POLL_MS)
    })

    await waitFor(() => expect(cellOf('c1', 'status')).toBe('completed'))
    expect(apiMock.instances.list.mock.calls.length).toBeGreaterThan(before)
  })

  it('does not poll while nothing is active or pending', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    world.byState.completed = [summary({ id: 'old', runName: 'Old run', state: 'completed', completedAt: '2026-10-16T09:00:00Z' })]
    renderQueue()
    await screen.findByRole('button', { name: /show dismissed/i })
    const before = apiMock.instances.list.mock.calls.length

    act(() => {
      vi.advanceTimersByTime(QUEUE_POLL_MS * 3)
    })

    expect(apiMock.instances.list.mock.calls.length).toBe(before)
  })
})
```

- [x] Step 7: Run them, confirm they fail.

```bash
cd dashboard && npx vitest run src/routes/QueueView.groups.test.tsx
```
Expected:
```
 FAIL  src/routes/QueueView.groups.test.tsx > QueueView — completed ancestors that own active independent work stay discoverable (P4-T10) > a completed parent with an active independent branch is listed, and the branch nests under it (non-root)
AssertionError: expected null not to be null
 FAIL  src/routes/QueueView.groups.test.tsx > … > an ancestor-discovery failure is reported without breaking the queue
TestingLibraryElementError: Unable to find an element by: [data-testid="ancestor-warning"]
 FAIL  src/routes/QueueView.groups.test.tsx > QueueView — open hierarchy levels refresh (stale cache, P4-T10) > an open level refreshes on a group lifecycle event and stays expanded
AssertionError: expected 'running' to be 'completed'
 FAIL  src/routes/QueueView.groups.test.tsx > … > bounded polling refreshes an open level when the final event was dropped
AssertionError: expected 'running' to be 'completed'
      Tests  4 failed | 4 passed (8)
```
(`does not poll while nothing is active or pending` already passes: it pins the bound.)

- [x] Step 7b: Keep the existing show-dismissed test meaningful. The ancestor read added below always sends `includeDismissed: true` (structural context, not the inbox), so `QueueView.test.tsx`'s "passes includeDismissed when shown" assertion — which accepts ANY list call — would pass even if the Show Dismissed wiring broke. In `dashboard/src/routes/QueueView.test.tsx` replace lines 128-134:

```tsx
    // After enabling, the failed/completed fetches request includeDismissed.
    await waitFor(() => {
      const calledWithInclude = list.mock.calls.some(
        (c) => (c[0] as { includeDismissed?: boolean } | undefined)?.includeDismissed === true,
      )
      expect(calledWithInclude).toBe(true)
    })
```

with:

```tsx
    // After enabling, the failed/completed fetches request includeDismissed.
    // Only the TERMINAL-state calls count: the ancestor-discovery read always
    // sends includeDismissed (structural context, not the inbox).
    await waitFor(() => {
      const calledWithInclude = list.mock.calls.some(
        (c) =>
          ['failed', 'completed'].includes((c[0] as { state?: string } | undefined)?.state ?? '') &&
          (c[0] as { includeDismissed?: boolean } | undefined)?.includeDismissed === true,
      )
      expect(calledWithInclude).toBe(true)
    })
```

(It passes before and after Step 8; it now fails if the Show Dismissed toggle stops reaching the terminal fetches.)

- [x] Step 8: Implement. Replace the whole of `dashboard/src/routes/QueueView.tsx` (lines 1-332) with:

```tsx
import { useEffect, useState, useCallback, useRef } from 'react'
import type { EnrichedInstanceSummary, Me, NodeEventData, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import { useRefetchOnReconnect } from '../hooks/useRefetchOnReconnect'
import { useDensityPreference } from '../hooks/useDensityPreference'
import { useNow } from '../hooks/useNow'
import QueueSection from '../components/QueueSection'
import DensityToggle from '../components/DensityToggle'
import { QUEUE_POLL_MS, isQueueRefreshEvent } from './queueRefresh'

/** Returns an ISO date string for N days ago. */
function daysAgo(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return d.toISOString()
}

const HISTORY_DAYS = 5
const DEBOUNCE_MS = 300
const NO_IDS: ReadonlySet<string> = new Set()

/** Node events that should patch a single row in place (no refetch). */
const NODE_PATCH_EVENTS = new Set(['node.started', 'node.failed'])

type RowPatcher = (id: string, fields: Partial<EnrichedInstanceSummary>) => void

/**
 * The row fields a node event patches. A field ABSENT from the payload (older
 * server) is left out so the row keeps its prior value; an explicit "" (a
 * script node has no model) is kept so it overwrites. node.started carries the
 * same binding + git ref a refetch would compute (server-side
 * resolveNodeBinding/deriveGitRef), so the whole row patches together.
 * node.failed patches the step only: its legacy `backend` ignores the profile,
 * and instance.failed refetches anyway.
 */
function nodeEventPatch(type: string, d: NodeEventData): Partial<EnrichedInstanceSummary> {
  const src: Partial<EnrichedInstanceSummary> = { currentNodeId: d.nodeId, friendlyStep: d.nodeLabel }
  if (type === 'node.started') {
    Object.assign(src, {
      nodeType: d.nodeType,
      model: d.model,
      effort: d.effort,
      backend: d.backend,
      provider: d.provider,
      gitRef: d.gitRef,
    })
  }
  const fields: Partial<EnrichedInstanceSummary> = {}
  for (const [k, v] of Object.entries(src)) {
    if (v != null) (fields as Record<string, unknown>)[k] = v
  }
  return fields
}

/** Ancestor rows follow the active rows they own; a row already listed keeps its place. */
function withAncestors(active: EnrichedInstanceSummary[], ancestors: EnrichedInstanceSummary[]): EnrichedInstanceSummary[] {
  const listed = new Set(active.map((r) => r.id))
  return [...active, ...ancestors.filter((a) => !listed.has(a.id))]
}

interface AncestorResult {
  rows: EnrichedInstanceSummary[]
  error: string | null
}

export default function QueueView() {
  const [active, setActive] = useState<EnrichedInstanceSummary[]>([])
  const [pending, setPending] = useState<EnrichedInstanceSummary[]>([])
  const [failed, setFailed] = useState<EnrichedInstanceSummary[]>([])
  const [completed, setCompleted] = useState<EnrichedInstanceSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showDismissed, setShowDismissed] = useState(false)
  // Terminal runs listed in Active only because they still own active work.
  const [ownsActiveWork, setOwnsActiveWork] = useState<ReadonlySet<string>>(NO_IDS)
  const [ancestorError, setAncestorError] = useState<string | null>(null)
  // Bumped on every successful top-level refetch so each QueueSection
  // refreshes its (independent) run-hierarchy cache — an expanded row's
  // children are otherwise never refreshed on their own, so a completed/failed
  // child kept rendering its stale state under an already-open parent.
  const [refreshedAt, setRefreshedAt] = useState(0)
  const inFlight = useRef(false)

  const { density, cycle: cycleDensity } = useDensityPreference(me?.userId ?? null)
  // One page-level clock so Elapsed / "N ago" cells advance between events.
  const now = useNow()
  const authMode = me?.authMode ?? 'local'

  const fetchAll = useCallback(async () => {
    inFlight.current = true
    try {
      const since = daysAgo(HISTORY_DAYS)

      // Ancestor discovery: TERMINAL runs that are ancestors of still-active
      // work the caller may see. Without it a completed fire-and-forget or
      // finish_independently parent falls out of the queue (outside the
      // Completed window, or dismissed) while its child keeps running.
      // includeDismissed: true — this is structural context for live work,
      // not the personal Completed inbox. A failure is reported but never takes
      // the queue down: the active child still shows (topLevelRows keeps a row
      // whose parent is not listed at top level), only its parent row is missing.
      const ancestorsReq: Promise<AncestorResult> = api.instances
        .list({ activeDescendantAncestors: true, includeDismissed: true })
        .then(
          (rows) => ({ rows, error: null }),
          (err: unknown) => ({ rows: [], error: err instanceof Error ? err.message : 'Unknown error' }),
        )

      // Seam: `showDismissed` drives the FETCH (includeDismissed), per-row
      // `inst.dismissed` from the server drives the RENDER (Restore vs Dismiss,
      // dimming). The server reconciles the two — do NOT re-thread showDismissed
      // down into the rows or it reintroduces the toggle/refetch flicker.
      const [running, waiting, queued, paused, failedRes, completedRes, meResp, ancestors] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed }),
          api.me(),
          ancestorsReq,
        ])

      // Ancestors go into the ACTIVE section: topLevelRows nests a row under
      // its parent only within one section, so this is what makes an active
      // branch a non-root row reached from its completed parent.
      setActive(withAncestors([...running, ...waiting, ...paused], ancestors.rows))
      setOwnsActiveWork(new Set(ancestors.rows.map((r) => r.id)))
      setAncestorError(ancestors.error)
      setPending(queued)
      setFailed(failedRes)
      setCompleted(completedRes)
      setMe(meResp)
      setError(null)
      setRefreshedAt((n) => n + 1)
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setError(msg)
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [showDismissed])

  const handleDismiss = useCallback(async (id: string) => {
    try {
      await api.instances.dismiss(id)
      await fetchAll()
    } catch (err) {
      // Route failures through the shared error surface so the unhappy path
      // isn't silent (a swallowed catch reads as "broken" to the user).
      setError(err instanceof Error ? err.message : 'Failed to dismiss instance')
    }
  }, [fetchAll])

  const handleRestore = useCallback(async (id: string) => {
    try {
      await api.instances.restore(id)
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restore instance')
    }
  }, [fetchAll])

  const handleDismissAll = useCallback(async (state: 'failed' | 'completed') => {
    try {
      // Scope to the same window the Failed/Completed sections show (daysAgo(5)),
      // so [Dismiss All] dismisses exactly "every item currently listed".
      await api.instances.dismissAll(state, daysAgo(HISTORY_DAYS))
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to dismiss instances')
    }
  }, [fetchAll])

  useEffect(() => {
    void fetchAll()
  }, [fetchAll])

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Each QueueSection owns a run hierarchy whose expanded children are cached
  // rows separate from `active` (a child may be past the list window, or sit
  // under a completed parent), so node events are fed to every section's
  // hierarchy as well as to `active`.
  const rowPatchers = useRef(new Set<RowPatcher>())
  const registerRowPatcher = useCallback((patch: RowPatcher) => {
    rowPatchers.current.add(patch)
    return () => {
      rowPatchers.current.delete(patch)
    }
  }, [])

  const { connected } = useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (NODE_PATCH_EVENTS.has(event.type)) {
          const instId = event.instanceId
          if (!instId) return
          // `nodeLabel`/`nodeId` are untrusted: they originate from
          // workflow-author-controlled definitions and cross the Go→JSON→TS
          // boundary unvalidated. They are safe ONLY because `friendlyStep`
          // renders through React's escaped text sink (JSX text child). Never
          // route these into a raw-HTML (dangerouslySetInnerHTML) or attribute
          // sink (e.g. `title`) without sanitizing first.
          const fields = nodeEventPatch(event.type, (event.data ?? {}) as NodeEventData)
          setActive((prev) => {
            const idx = prev.findIndex((r) => r.id === instId)
            if (idx === -1) return prev // not listed yet; instance.started refetch will surface it
            const next = [...prev]
            next[idx] = { ...next[idx], ...fields }
            return next
          })
          for (const patch of rowPatchers.current) patch(instId, fields)
          return
        }
        // Lifecycle AND group events refetch (and so refresh open hierarchy
        // levels): a group decision or task update changes child rows and
        // their markers with no instance event on the expanded parent.
        if (!isQueueRefreshEvent(event.type)) return
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => {
          void fetchAll()
        }, DEBOUNCE_MS)
      },
      [fetchAll],
    ),
  )

  // The system WebSocket auto-reconnects after a drop, but events emitted
  // while it was down are not replayed. Resync on reconnect so the queue
  // doesn't sit stale until the next event arrives.
  useRefetchOnReconnect(connected, fetchAll)

  // Bounded periodic refetch while anything is active or pending: the bus
  // drops events when buffers fill and some transitions have no emitter, so a
  // lost final event must not leave a row or an open level stale forever. A
  // tick is skipped while a refetch is still in flight; nothing polls once the
  // queue holds only terminal runs.
  const hasLiveWork = active.length > 0 || pending.length > 0
  useEffect(() => {
    if (!hasLiveWork) return
    const timer = setInterval(() => {
      if (inFlight.current) return
      void fetchAll()
    }, QUEUE_POLL_MS)
    return () => clearInterval(timer)
  }, [hasLiveWork, fetchAll])

  // Clean up debounce timer on unmount.
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [])

  if (loading) {
    return (
      <div style={{ padding: 32, color: 'var(--text-muted)', textAlign: 'center' }}>
        Loading instances...
      </div>
    )
  }

  if (error) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <div style={{ color: 'var(--state-failed)', marginBottom: 12 }}>
          Failed to load instances: {error}
        </div>
        <button
          onClick={() => { setLoading(true); void fetchAll() }}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div style={{ padding: '16px 0' }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '0 16px 8px' }}>
        <DensityToggle density={density} onCycle={cycleDensity} />
      </div>
      {ancestorError && (
        <div
          role="status"
          data-testid="ancestor-warning"
          style={{
            margin: '0 16px 8px',
            padding: '6px 12px',
            fontSize: 12,
            color: 'var(--state-waiting)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
          }}
        >
          {`Couldn't load completed runs that still own active work: ${ancestorError}`}
        </div>
      )}
      <QueueSection
        title="Active"
        instances={active}
        titleColor="var(--state-running)"
        countBg="rgba(52, 152, 219, 0.2)"
        countColor="var(--state-running)"
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        ownsActiveWork={ownsActiveWork}
      />
      <QueueSection
        title="Pending"
        instances={pending}
        titleColor="var(--state-queued)"
        countBg="rgba(241, 196, 15, 0.2)"
        countColor="var(--state-queued)"
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
      />
      <QueueSection
        title="Failed"
        instances={failed}
        titleColor="var(--state-failed)"
        countBg="rgba(231, 76, 60, 0.2)"
        countColor="var(--state-failed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        onDismissAll={() => handleDismissAll('failed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />
      <QueueSection
        title="Completed"
        instances={completed}
        titleColor="var(--state-completed)"
        countBg="rgba(46, 204, 113, 0.2)"
        countColor="var(--state-completed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        defaultCollapsed
        onDismissAll={() => handleDismissAll('completed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />

      <div style={{ textAlign: 'center', padding: '16px 0' }}>
        <button
          type="button"
          onClick={() => setShowDismissed((s) => !s)}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          {showDismissed ? 'Hide Dismissed' : 'Show Dismissed'}
        </button>
      </div>
    </div>
  )
}
```

- [x] Step 9: Run them, confirm they pass; run every queue suite and the typecheck.

```bash
cd dashboard && npx vitest run src/routes/QueueView.groups.test.tsx src/routes/QueueView.livepane.test.tsx src/routes/QueueView.test.tsx src/routes/QueueView.hierarchypatch.test.tsx src/routes/QueueView.livepatch.test.tsx src/routes/queueRefresh.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  6 passed (6)
```
(The existing `QueueView` suites stay green: their `list` fakes answer the extra ancestor call with `[]`, the 5 s clock test never reaches the 10 s poll, and `node.started` still patches without a refetch.)

- [x] Step 10: Commit.

Self-review (spec → anti-theater → quality): retained the specified ancestor read, event policy and active-only polling; four route regressions failed before implementation and now pass. Added real-route sink coverage for nested row ids, repeated child node ids including paused work, and the live graph of a completed discovered ancestor with independent work. The strict ancestor/page/id fakes cannot supply these results for missing or empty inputs. Six queue suites (75 tests) and typecheck pass; diff check clean.

```bash
git add dashboard/src/routes/QueueView.tsx dashboard/src/routes/QueueView.groups.test.tsx dashboard/src/routes/QueueView.test.tsx && git commit -m "feat(dashboard): queue keeps ancestors of active work discoverable; refresh on group events and bounded polling" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10.7: Embed the dashboard (`make ui`) and run every gate
**Files:**
- Create: `adapter/dashboard/embed_bundle_test.go` (package `dashboard_test`)
- Regenerate: `adapter/dashboard/dist/**` (via `make ui`, `Makefile:27-30`; replaces `assets/index-644lyjCO.css`, `assets/index-88VMqy-v.js`, `index.html`)

- [x] Step 1: Write the failing embed probe. Create `adapter/dashboard/embed_bundle_test.go`:

```go
package dashboard_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

// The SPA reaches users only through adapter/dashboard/dist (//go:embed
// dist/*). A stale dist compiles and serves perfectly well, so only a content
// probe catches a release that forgot `make ui`. Each marker is a string
// literal the minifier keeps, owned by one Phase 4 dashboard task.
func TestEmbeddedBundle_ContainsParallelGroupVisibility(t *testing.T) {
	sub := dashboard.AssetsForTest()
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil {
		t.Fatalf("reading assets dir: %v", err)
	}
	var js strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := fs.ReadFile(sub, "assets/"+e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		js.Write(b)
	}
	if js.Len() == 0 {
		t.Fatal("no JavaScript bundle is embedded")
	}
	bundle := js.String()
	for _, marker := range []string{
		"/graph-state",              // P4-T6 api.instances.graphState
		"/group-layout",             // P4-T6 api.workflows.groupLayout (WorkflowReview, P4-T9)
		"subtree=1",                 // P4-T6 subtree WebSocket (P4-T5 server mode)
		"activeDescendantAncestors", // P4-T10 queue ancestor discovery (P4-T3 filter)
		"run-live-pane",             // P4-T10 queue live-run pane
		"Load more child runs",      // P4-T10 hierarchy pagination
	} {
		if !strings.Contains(bundle, marker) {
			t.Errorf("embedded dashboard bundle is stale: %q not found (run `make ui`)", marker)
		}
	}
}
```

- [x] Step 2: Run it, confirm it fails against the current (pre-Phase-4) embedded build.

Observed the current bundle missing the three Task 10 markers (ancestor discovery, live pane, load-more). Earlier Phase 4 markers were already embedded.

```bash
go test ./adapter/dashboard/ -run TestEmbeddedBundle_ContainsParallelGroupVisibility -count=1
```
Expected:
```
--- FAIL: TestEmbeddedBundle_ContainsParallelGroupVisibility (0.00s)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "/graph-state" not found (run `make ui`)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "/group-layout" not found (run `make ui`)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "subtree=1" not found (run `make ui`)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "activeDescendantAncestors" not found (run `make ui`)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "run-live-pane" not found (run `make ui`)
    embed_bundle_test.go:…: embedded dashboard bundle is stale: "Load more child runs" not found (run `make ui`)
FAIL
FAIL	github.com/arbitron/arbitron/adapter/dashboard	…
```

- [x] Step 3: Refresh the embedded assets (this is the "implementation" of the embed step; it is the single `make ui` for the whole feature — P1-T9 and P4-T6..T9 defer to it). `npm run build` runs `tsc && vite build`, so a type error anywhere in Phase 4 fails here.

```bash
make ui
```
Expected (tail):
```
vite v8.… building for production...
✓ … modules transformed.
dist/index.html                  …
dist/assets/index-….css          …
dist/assets/index-….js           …
✓ built in …
rm -rf adapter/dashboard/dist
cp -r dashboard/dist adapter/dashboard/dist
```

- [x] Step 4: Run it, confirm it passes; run the existing embed tests.

```bash
go test ./adapter/dashboard/... -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/dashboard	…
```

- [x] Step 5: Run every gate (dashboard typecheck + full Vitest suite, Go build/vet, full Go suite — `make test-all` is `test` + `test-ui`).

Verification: dashboard typecheck and all 65 Vitest files / 760 tests pass after final review. `make ui`, `go test ./adapter/dashboard/... -count=1`, `go build ./...`, and `go vet ./adapter/dashboard/...` pass. `make test-all` runs the full Go suite: only the unrelated existing `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` fails (UNKNOWN_PROFILE sample references); reproduced with the focused test in a clean `git archive 2e0cb5f` checkout. All other Go packages pass. Because make stops before test-ui on that failure, also ran `make test-ui` separately: green. No task verification remains blocked.

Build concerns: npm reports 9 advisories (1 low, 2 moderate, 6 high) in the unchanged dependency lockfile, and Vite warns about the existing large bundle. No dependency upgrades or bundle-splitting options were added outside this task.

```bash
cd dashboard && npx tsc --noEmit && npx vitest run && cd .. && go build ./... && go vet ./adapter/dashboard/... && make test-all
```
Expected:
```
 Test Files  … passed (…)
      Tests  … passed (…)
...
ok  	github.com/arbitron/arbitron/adapter/dashboard	…
...
(no FAIL lines; make exits 0)
```

- [x] Step 6: Commit (stage deletions of the old content-hashed assets too).

Final self-review, in required order:
1. **Spec compliance:** checked the produced interfaces and seven subtask slices against the design and actual App `/` → QueueView → QueueSection → HierarchyRows composition. Real authorized list/graph-state endpoints and subtree hook feed the pane/markers. No additional feature, option or dependency was introduced; unreadable data and redaction remain explicit. Pagination, ancestor discovery, group refresh and independent-work live visibility are exercised at rendered sinks.
2. **Anti-green-theater:** strengthened real-route coverage for nested row ids, repeated workflow node ids/paused tasks and completed ancestors' independent live work. Keyed fakes reject incorrect ancestor scopes, page arguments and graph ids. Temporarily changed the production pane input to an empty id: BOTH running-branch/provenance route tests failed. Restored the actual row id; focused route/hierarchy/client suites pass. Mutation is not retained.
3. **Code quality:** followed neighboring aggregate traversal and table patterns, replacing the proposed magic depth cutoff with cycle detection. Red/green review regressions fixed deduplicated-page offset/count drift, duplicate same-event load-more requests and late responses resurrecting invalidated collapsed levels. Formatting and diff checks are clean. Reran the 12 focused suites (195 tests), full dashboard gate (760 tests), rebuilt the final embedded assets, and reran embed/build/vet/full-Go gates; only the independently reproduced sample-profile failure remains.

```bash
git add -A adapter/dashboard/dist adapter/dashboard/embed_bundle_test.go && git commit -m "build(ui): embed the dashboard with parallel-group run visibility" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
