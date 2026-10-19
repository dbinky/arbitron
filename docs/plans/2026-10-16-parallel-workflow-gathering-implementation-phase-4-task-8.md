# Parallel Workflow Gathering — Phase 4 Task 8: ExecutionView aggregate integration, `GroupPanel` and state styles

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make the individual run view show every concurrently active node across all fanned-out paths, open the OWNING instance's trace for a branch-body node, open a group panel (rule, unfinished behaviour, gather mode, state, ordered live vs frozen tasks, progress, waiting reasons, winner, frozen results/failures, merge report, resolution history, session link) from a group's header or footer, style queued/cancelled/independent/hidden work, and keep all active paths in view.
**Architecture:** A pure module `graph/aggregateState.ts` turns the P4-T6 `GraphState` into node states keyed `${instanceId}:${nodeId}`, resolves each rendered definition node to its owning instance through Phase 1 ownership (recursively through nested groups), and builds the P4-T7 `groupLive`/`nodeContexts` overlays. `ExecutionView` mounts `useGraphState`, passes the aggregate into `GraphView` (which keeps its legacy trace path as a fallback), replaces the old per-event WebSocket handler (which listened for never-emitted events), and renders the new `components/GroupPanel.tsx`. `useFollowActiveNode` frames the bounds of all active nodes instead of centering on one.
**Tech Stack:** React 19, React Router 7, `@xyflow/react` 12, TypeScript 6, Vitest 4 + @testing-library/react 16, CSS modules.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Workflow and run visibility": every concurrently active node; task states distinguish queued/running/waiting/completed/failed/cancelled; each gather shows rule, progress, outstanding dependencies, winner, file-gathering/resolution activity; independent tasks distinguished from blocking work; completed runs retain outcomes); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §1 (live vs frozen; redaction as hidden dependency; zero outstanding tasks ≠ ready), §3 (remove reliance on `node.skipped`/`variables.updated`/`instance.state_changed`), §4 (instance-qualified selection; header/footer open the same panel; queued/cancelled/non-blocking styling; display all active nodes rather than letting a follow-one-node helper hide concurrent paths).

**Interfaces**
- Consumes (existing code):
  - `dashboard/src/routes/ExecutionView.tsx:29-68` `buildExecutionStates`, `:70-170` `GraphView`, `:178-393` `ExecutionView` (state, `fetchData`, `handleWsEvent` at `:242-282`, `useInstanceWebSocket` at `:284`, `useRefetchOnReconnect` at `:290`, `handleNodeClick` at `:293-306`, panel switch at `:365-380`).
  - `dashboard/src/graph/useFollowActiveNode.ts:23-69` `useFollowActiveNode(anchor, isRunning)`; `dashboard/src/graph/constants.ts:1-4`.
  - `dashboard/src/graph/types.ts:1` `NodeExecutionState`; `dashboard/src/graph/nodes/ArbitronNode.tsx:19-41` state class maps and `ArbitronNodeData`.
  - `dashboard/src/hooks/useInteractiveSession.ts:52` `useInteractiveSession(instanceId)` → `{ isActive, seedFromInstance, … }`; `dashboard/src/components/InteractivePanel.tsx:12` `InteractivePanel({ session, nodeName })`; `dashboard/src/components/SidePanel.tsx` `SidePanel({ execution, variables, selectedNodeId, workflowDef })`; `dashboard/src/utils/time.ts:93` `localDateTime`.
- Consumes (earlier tasks):
  - P4-T6: `useGraphState(instanceId): GraphStateHandle` (`dashboard/src/hooks/useGraphState.ts`), types `GraphState`, `ExecutionContext`, `GroupPanel`, `GroupPanelTask`, `FrozenTask`, `GatherReport`, `ResolutionSessionRef`, `LiveNodeStateValue`, `GroupGraph` (`dashboard/src/api/types.ts`).
  - P4-T7: `useGraphLayout({ …, groups, groupLive, nodeContexts })`; `GroupLiveSummary`, `GroupLiveTask`, `NodeContextInfo`, `groupHeaderId`, `groupFooterId` (`dashboard/src/graph/groupLayout.ts`); `ruleText`, `unfinishedText`, `gatherText`, `progressText`, `stateText` (`dashboard/src/graph/groupText.ts`); fixtures `branchNodes/branchEdges/branchGraph/mixedNodes/mixedEdges/mixedGraph` (`dashboard/src/graph/groupLayout.fixtures.ts`).
- Produces (exact exported symbols later tasks rely on):
  - `dashboard/src/graph/aggregateState.ts`: `qualifiedKey(instanceId, nodeId): string`; `buildAggregateExecutionStates(gs): Record<string, NodeExecutionState>`; `findGroupPanel(gs, instanceId, nodeId): GroupPanel | null`; `resolveNodeContexts(gs, def, layout): Record<string, NodeContextInfo>`; `projectExecutionStates(qualified, nodeContexts, def)`; `activeNodeLabels(gs, instanceId, rootDef): string[]`; `gatherActivity(panel): string | undefined`; `buildGroupLive(gs, nodeContexts, layout, rootDef)`; `interface AggregateView`; `buildAggregateView(gs, def): AggregateView`; `type GraphSelection`; `selectionFromNode(n, fallbackInstanceId): GraphSelection`.
  - `dashboard/src/components/GroupPanel.tsx`: default `GroupPanel({ panel: GroupPanel | null; label?: string; onOpenSession?: (s: ResolutionSessionRef) => void })`; `outstandingTasks(panel): GroupPanelTask[]`.
  - `dashboard/src/routes/ExecutionView.tsx`: `GraphView` gains optional `graphState?: GraphState | null` and `onSelect?: (s: GraphSelection) => void`; `onNodeClick` becomes optional. (P4-T10's queue pane reuses `GraphView` with these props.)
  - `dashboard/src/graph/useFollowActiveNode.ts`: `export interface FollowBounds`; `useFollowActiveNode(anchor, isRunning, bounds?: FollowBounds | null)`. `dashboard/src/graph/constants.ts`: `FOLLOW_BOUNDS_PADDING = 0.25`.
  - `dashboard/src/graph/types.ts`: `NodeExecutionState` adds `'queued' | 'paused' | 'cancelled' | 'unknown'`; `ArbitronNodeData` adds `instanceId?`, `independent?`, `hiddenDependency?`; the node root exposes `data-state`.

**Wiring & data provenance**
- Composition root (live UI): `ExecutionView` is mounted at `<Route path="/instances/:id">` in `dashboard/src/App.tsx:20`; this task makes it call `useGraphState(id)` and pass `graph.state` into `GraphView`. `GroupPanel` is mounted by `ExecutionView` (this task) and by the queue live pane (P4-T10). The SPA is embedded by `make ui` (P4-T10).
- Producers:
  - `graph.state` ← `useGraphState` (P4-T6) ← `GET /api/v1/instances/{id}/graph-state` → `adapter/http/instance_handler.go:InstanceHandler.GraphState` (P4-T3) ← `domain/engine/graph_state.go:GraphStateReader.GraphState` (P4-T1: contexts + `LiveNodeState` per (instance,node), authz per descendant, redaction) + `domain/engine/graph_state_groups.go` (P4-T2: `GroupPanel` live/frozen, waiting reasons, gather/resolution history).
  - `graph.state.layout` ← `workflow.AnalyzeGroups` of the root snapshot (P1-T2 via P4-T1).
  - Branch-node trace ← existing `GET /api/v1/instances/{owner}/trace/{nodeId}` (`api.instances.getNodeTrace`, `adapter/http/instance_handler.go:InstanceHandler.GetNodeTrace`), authorized per owner instance by `Engine.GetInstance`.
  - Session link ← `GroupPanel.gather.activeSession` (`instance.ResolutionSessionRef`, P3-T4/P3-T8); the session's own stream is the existing per-instance WS (`useInteractiveSession(sessionRef.instanceId)`), i.e. routed by the session owner, never by whichever child emitted last.
  - Refresh: the subtree WS (P4-T5) + polling + reconnect inside `useGraphState`; the old `handleWsEvent` (`ExecutionView.tsx:242-282`, with `node.skipped` at `:250`, `variables.updated` at `:262`, `instance.state_changed` at `:269` — none emitted by `domain/engine/ports.go:241-256`) is deleted.
- Provenance probes: `ExecutionView — highlights the running nodes of BOTH concurrent branches` fails if `ExecutionView` does not pass the aggregate (verified: with `graphState={null}` 9 of 10 view tests fail, first with `expected 'pending' to be 'running'`); `selecting a branch-body node opens the OWNING branch instance's trace` fails if selection is not instance-qualified (the fake 404s `getNodeTrace('root','b1')`).

### Task 8: ExecutionView aggregate integration, GroupPanel and state styles
**Files:**
- Modify: `dashboard/src/graph/types.ts:1` (`NodeExecutionState`)
- Modify: `dashboard/src/graph/nodes/ArbitronNode.tsx:1-75` (whole file)
- Modify: `dashboard/src/graph/nodes/ArbitronNode.module.css` (insert after `:58` and after `:126`)
- Create: `dashboard/src/graph/aggregateState.ts`
- Modify: `dashboard/src/graph/useFollowActiveNode.ts:1-69` (whole file), `dashboard/src/graph/constants.ts:1-4` (whole file)
- Create: `dashboard/src/components/GroupPanel.tsx`, `dashboard/src/components/GroupPanel.module.css`
- Modify: `dashboard/src/routes/ExecutionView.tsx:1-393` (whole file), `dashboard/src/routes/ExecutionView.module.css` (append after `:133`)
- Test: `dashboard/src/graph/nodes/ArbitronNode.test.tsx` (append after `:68`)
- Test: `dashboard/src/graph/aggregateState.test.ts` (new)
- Test: `dashboard/src/graph/useFollowActiveNode.test.ts` (edit `:4-9`, `:13-15`; append after `:222`)
- Test: `dashboard/src/components/GroupPanel.test.tsx` (new)
- Test: `dashboard/src/routes/ExecutionView.groups.test.tsx` (new)

#### Cycle 1 — aggregate node states and their styles

- [x] Step 1: Write the failing node-style tests. Append to `dashboard/src/graph/nodes/ArbitronNode.test.tsx` (after line 68; the file already imports `render`, `describe/it/expect`, `vi` and `ArbitronNode`, and stubs `Handle`):

```tsx
describe('ArbitronNode — aggregate states and task styling (P4-T8)', () => {
  const renderWith = (data: Record<string, unknown>) =>
    render(
      <ArbitronNode
        {...({ data: { label: 'Step', nodeType: 'script', executionState: 'pending', ...data }, selected: false } as unknown as Parameters<typeof ArbitronNode>[0])}
      />,
    )

  it.each([
    ['queued', 'stateQueued'],
    ['paused', 'statePaused'],
    ['cancelled', 'stateCancelled'],
    ['unknown', 'stateUnknown'],
    ['running', 'stateRunning'],
  ])('styles a %s node with %s and exposes data-state', (state, cls) => {
    const { container } = renderWith({ executionState: state })
    const root = container.firstElementChild as HTMLElement
    expect(root.className).toContain(cls)
    expect(root.getAttribute('data-state')).toBe(state)
  })

  it('marks work continuing independently as non-blocking', () => {
    const { container, getByText } = renderWith({ executionState: 'running', independent: true })
    expect((container.firstElementChild as HTMLElement).className).toContain('independent')
    expect(getByText('non-blocking')).toBeTruthy()
  })

  it('shows a hidden dependency instead of the non-blocking note for a redacted owner', () => {
    const { getByText, queryByText } = renderWith({ executionState: 'unknown', hiddenDependency: true, independent: true })
    expect(getByText('hidden dependency')).toBeTruthy()
    expect(queryByText('non-blocking')).toBeNull()
  })

  it('degrades an unrecognized state to the pending style instead of crashing', () => {
    const { container } = renderWith({ executionState: 'bogus' })
    expect((container.firstElementChild as HTMLElement).className).toContain('statePending')
  })
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/nodes/ArbitronNode.test.tsx
```
Expected:
```
 FAIL  src/graph/nodes/ArbitronNode.test.tsx > ArbitronNode — aggregate states and task styling (P4-T8) > styles a queued node with stateQueued and exposes data-state
AssertionError: expected '_node_0b62c9' to contain 'stateQueued'
...
 FAIL  src/graph/nodes/ArbitronNode.test.tsx > ArbitronNode — aggregate states and task styling (P4-T8) > styles a running node with stateRunning and exposes data-state
AssertionError: expected null to be 'running' // Object.is equality
      Tests  8 failed | 3 passed (11)
```
(The CSS-module hash suffix differs per machine.)

- [x] Step 3: Implement. In `dashboard/src/graph/types.ts` replace line 1 with:

```ts
/**
 * Rendered execution state of a node. The first six are the trace-derived
 * states; queued/paused/cancelled/unknown come from the aggregate graph state
 * (P4-T1 LiveNodeState): a queued task's entry, a paused instance's cursor, a
 * cancelled branch, an unreadable/redacted owner.
 */
export type NodeExecutionState =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'waiting'
  | 'skipped'
  | 'queued'
  | 'paused'
  | 'cancelled'
  | 'unknown'
```

Replace the whole of `dashboard/src/graph/nodes/ArbitronNode.tsx` (lines 1-75) with:

```tsx
import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'

/** Accent colors keyed by workflow node type. */
const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
}

const stateNodeClass: Record<NodeExecutionState, string> = {
  completed: styles.stateCompleted,
  running: styles.stateRunning,
  pending: styles.statePending,
  failed: styles.stateFailed,
  waiting: styles.stateWaiting,
  skipped: styles.stateSkipped,
  queued: styles.stateQueued,
  paused: styles.statePaused,
  cancelled: styles.stateCancelled,
  unknown: styles.stateUnknown,
}

const stateIndicatorClass: Record<NodeExecutionState, string> = {
  completed: styles.indicatorCompleted,
  running: styles.indicatorRunning,
  pending: styles.indicatorPending,
  failed: styles.indicatorFailed,
  waiting: styles.indicatorWaiting,
  skipped: styles.indicatorSkipped,
  queued: styles.indicatorQueued,
  paused: styles.indicatorPaused,
  cancelled: styles.indicatorCancelled,
  unknown: styles.indicatorUnknown,
}

export interface ArbitronNodeData {
  label: string
  nodeType: string
  executionState: NodeExecutionState
  /** Instance executing this node in the aggregate view (P4-T7 nodeContexts); null = not launched/hidden. */
  instanceId?: string | null
  /** The owning task continues independently of its gathered group (non-blocking). */
  independent?: boolean
  /** The owning context is invisible to the viewer. */
  hiddenDependency?: boolean
  [key: string]: unknown
}

type ArbitronNodeType = Node<ArbitronNodeData, 'arbitronNode'>

function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeType>) {
  const accentColor = typeColors[data.nodeType] ?? '#888888'
  const execState = data.executionState ?? 'pending'
  const nodeClasses = [
    styles.node,
    stateNodeClass[execState] ?? styles.statePending,
    data.independent ? styles.independent : '',
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={nodeClasses} data-state={execState}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeBar} style={{ background: accentColor }} />
      <div className={styles.content}>
        <div className={styles.typeLabel} style={{ color: accentColor }}>
          {data.nodeType.replace(/_/g, ' ')}
        </div>
        <div className={styles.name}>
          <span
            className={`${styles.stateIndicator} ${stateIndicatorClass[execState] ?? styles.indicatorPending}`}
          />
          {data.label}
        </div>
        {data.hiddenDependency && <div className={styles.note}>hidden dependency</div>}
        {!data.hiddenDependency && data.independent && <div className={styles.note}>non-blocking</div>}
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

export default memo(ArbitronNode)
```

In `dashboard/src/graph/nodes/ArbitronNode.module.css` insert after the `.stateSkipped { … }` block (after line 58):

```css

/* Aggregate-only states (P4-T8). */
.stateQueued {
  border-color: var(--state-queued);
  border-style: dashed;
}

.statePaused {
  border-color: var(--state-paused);
}

.stateCancelled {
  border-color: var(--state-cancelled);
  opacity: 0.6;
}

.stateUnknown {
  border-color: var(--border-color);
  border-style: dotted;
  opacity: 0.6;
}

/* Work that continues independently of its (already gathered) group. */
.independent {
  border-style: dashed;
  background: rgba(155, 89, 182, 0.08);
}

.note {
  font-size: 10px;
  font-style: italic;
  color: var(--text-muted);
}
```

and after the `.indicatorSkipped { … }` block (after line 126 of the original file):

```css

.indicatorQueued {
  background: var(--state-queued);
}

.indicatorPaused {
  background: var(--state-paused);
}

.indicatorCancelled {
  background: var(--state-cancelled);
}

.indicatorUnknown {
  background: transparent;
  border: 1px dotted var(--text-dim);
}
```

- [x] Step 4: Run it, confirm it passes; typecheck.

```bash
cd dashboard && npx vitest run src/graph/nodes/ArbitronNode.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  11 passed (11)
```

- [x] Step 5: Commit.

Cycle 1: observed 8 failing / 3 passing tests before implementation; all 11 pass afterward and TypeScript is clean. Preserved the existing typed accent map. Reviewed scope, the existing layout-to-node context seam (wired by Cycle 5), and neighboring styles.

```bash
git add dashboard/src/graph/types.ts dashboard/src/graph/nodes/ArbitronNode.tsx dashboard/src/graph/nodes/ArbitronNode.module.css dashboard/src/graph/nodes/ArbitronNode.test.tsx
git commit -m "feat(dashboard): queued/paused/cancelled/unknown, non-blocking and hidden-dependency node styles" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — aggregate state projection (`aggregateState.ts`)

- [x] Step 6: Write the failing tests `dashboard/src/graph/aggregateState.test.ts` (two-level branches aggregate with an independent nested task; duplicate node ids across repeated child workflows; unlaunched, not-started and redacted owners; workflow task cards):

```ts
import { describe, it, expect } from 'vitest'
import type { ExecutionContext, GraphState, GroupPanel, GroupPanelTask } from '../api/types'
import type { WorkflowDefinition } from './types'
import {
  activeNodeLabels,
  buildAggregateExecutionStates,
  buildAggregateView,
  buildGroupLive,
  findGroupPanel,
  gatherActivity,
  projectExecutionStates,
  qualifiedKey,
  resolveNodeContexts,
  selectionFromNode,
} from './aggregateState'
import { branchEdges, branchGraph, branchNodes, mixedEdges, mixedGraph, mixedNodes } from './groupLayout.fixtures'

const branchDef: WorkflowDefinition = { name: 'wf', description: '', author: '', nodes: branchNodes, edges: branchEdges }
const mixedDef: WorkflowDefinition = { name: 'wf', description: '', author: '', nodes: mixedNodes, edges: mixedEdges }

function ctx(instanceId: string, over: Partial<ExecutionContext> = {}): ExecutionContext {
  return { instanceId, kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [], ...over }
}
function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key, kind: 'branch', origin: key, childInstanceId: null, liveStatus: 'running', frozenStatus: '',
    blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
  }
}
function panel(instanceId: string, nodeId: string, tasks: GroupPanelTask[], over: Partial<GroupPanel> = {}): GroupPanel {
  return {
    groupRunId: `run-${nodeId}`, instanceId, nodeId, form: 'branches', completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running', tasks,
    progress: { total: tasks.length, completed: 0, failed: 0, cancelled: 0, active: tasks.length, notStarted: 0 },
    waitingReasons: [], ...over,
  }
}
const live = (instanceId: string, nodeId: string, state: ExecutionContext['nodes'][number]['state']) => ({ instanceId, nodeId, state })

/**
 * Running two-level branches aggregate:
 *   root waits at g1; branch A (br-a) runs a2; branch B (br-b) passed nested g2
 *   (x won in br-x; y continues independently in br-y running y2) and runs b2.
 */
function branchState(): GraphState {
  return {
    rootInstanceId: 'root',
    version: 'v1',
    active: true,
    layout: branchGraph,
    contexts: [
      ctx('root', { kind: 'root', state: 'waiting', nodes: [live('root', 'start', 'completed'), live('root', 'g1', 'waiting')] }),
      ctx('br-a', { parentInstanceId: 'root', groupRunId: 'run-g1', taskKey: 'a', nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] }),
      ctx('br-b', { parentInstanceId: 'root', groupRunId: 'run-g1', taskKey: 'b', nodes: [live('br-b', 'g2', 'completed'), live('br-b', 'b2', 'running')] }),
      ctx('br-x', { parentInstanceId: 'br-b', groupRunId: 'run-g2', taskKey: 'x', state: 'completed', blocking: false, nodes: [live('br-x', 'x1', 'completed')] }),
      ctx('br-y', { parentInstanceId: 'br-b', groupRunId: 'run-g2', taskKey: 'y', blocking: false, nodes: [live('br-y', 'y1', 'completed'), live('br-y', 'y2', 'running')] }),
    ],
    groups: [
      panel('root', 'g1', [task('a', { childInstanceId: 'br-a' }), task('b', { childInstanceId: 'br-b' })]),
      panel('br-b', 'g2', [
        task('x', { childInstanceId: 'br-x', liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        task('y', { childInstanceId: 'br-y', liveStatus: 'running', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
      ], { completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'x' }),
    ],
    warnings: [],
  }
}

describe('qualified node identity', () => {
  it('keys every live node by instance AND node id', () => {
    expect(qualifiedKey('br-a', 'a2')).toBe('br-a:a2')
    const states = buildAggregateExecutionStates(branchState())
    expect(states['br-a:a2']).toBe('running')
    expect(states['br-y:y2']).toBe('running')
    expect(states['root:g1']).toBe('waiting')
  })

  // Two workflow tasks running the SAME child workflow reuse node ids; a map
  // keyed by node id alone would collapse them into one.
  it('keeps duplicate node ids of repeated child workflows apart', () => {
    const gs: GraphState = {
      rootInstanceId: 'root', version: 'v', active: true, groups: [], warnings: [],
      contexts: [
        ctx('c1', { kind: 'workflow', nodes: [live('c1', 'build', 'running')] }),
        ctx('c2', { kind: 'workflow', nodes: [live('c2', 'build', 'failed')] }),
      ],
    }
    const states = buildAggregateExecutionStates(gs)
    expect(states['c1:build']).toBe('running')
    expect(states['c2:build']).toBe('failed')
  })

  it('maps aggregate-only states (queued, paused, cancelled, unknown) through', () => {
    const gs: GraphState = {
      rootInstanceId: 'r', version: 'v', active: true, groups: [], warnings: [],
      contexts: [ctx('r', { kind: 'root', nodes: [live('r', 'q', 'queued'), live('r', 'p', 'paused'), live('r', 'c', 'cancelled'), live('r', 'u', 'unknown')] })],
    }
    expect(buildAggregateExecutionStates(gs)).toEqual({ 'r:q': 'queued', 'r:p': 'paused', 'r:c': 'cancelled', 'r:u': 'unknown' })
  })
})

describe('resolveNodeContexts — owning instance via Phase 1 ownership', () => {
  it('maps main-path nodes to the root and body nodes to their branch, recursively through nested groups', () => {
    const c = resolveNodeContexts(branchState(), branchDef, branchGraph)
    expect(c.start.instanceId).toBe('root')
    expect(c.done.instanceId).toBe('root')
    expect(c.g1.instanceId).toBe('root')
    expect(c.a2.instanceId).toBe('br-a')
    expect(c.g2.instanceId).toBe('br-b')
    expect(c.b2.instanceId).toBe('br-b')
    expect(c.x1.instanceId).toBe('br-x')
    expect(c.y2.instanceId).toBe('br-y')
  })

  it('marks only work continuing independently as independent', () => {
    const c = resolveNodeContexts(branchState(), branchDef, branchGraph)
    expect(c.y2.independent).toBe(true)
    expect(c.x1.independent).toBe(false)
    expect(c.a2.independent).toBe(false)
  })

  it('an unlaunched task leaves its body without an owning instance (pending)', () => {
    const gs = branchState()
    gs.groups[0] = panel('root', 'g1', [task('a', { childInstanceId: 'br-a' }), task('b', { childInstanceId: null, liveStatus: 'queued' })])
    const c = resolveNodeContexts(gs, branchDef, branchGraph)
    expect(c.b2.instanceId).toBeNull()
    expect(c.x1.instanceId).toBeNull()
    expect(projectExecutionStates(buildAggregateExecutionStates(gs), c, branchDef).b2).toBe('pending')
  })

  it('a group that has not started leaves its bodies pending', () => {
    const gs = branchState()
    gs.groups = []
    const view = buildAggregateView(gs, branchDef)
    expect(view.nodeContexts.a2.instanceId).toBeNull()
    expect(view.executionStates.a2).toBe('pending')
    expect(view.groupLive).toEqual({})
  })

  it('a redacted task or context hides the body as a hidden dependency with unknown state', () => {
    const gs = branchState()
    gs.groups[0].tasks[0] = task('a', { childInstanceId: null, redacted: true, liveStatus: 'unknown' })
    gs.contexts[3] = ctx('br-x', { redacted: true, nodes: [] })
    const view = buildAggregateView(gs, branchDef)
    expect(view.nodeContexts.a2).toEqual({ instanceId: null, independent: false, hiddenDependency: true })
    expect(view.executionStates.a2).toBe('unknown')
    expect(view.nodeContexts.x1.hiddenDependency).toBe(true)
    expect(view.executionStates.x1).toBe('unknown')
  })
})

describe('projectExecutionStates — every concurrent path is live', () => {
  it('shows BOTH running branches and the nested independent task as running', () => {
    const view = buildAggregateView(branchState(), branchDef)
    expect(view.executionStates.a2).toBe('running')
    expect(view.executionStates.b2).toBe('running')
    expect(view.executionStates.y2).toBe('running')
    expect(view.executionStates.x1).toBe('completed')
    expect(view.executionStates.done).toBe('pending')
    expect(view.executionStates.g1).toBe('waiting')
  })
})

describe('buildGroupLive', () => {
  it('summarizes each group for its header/footer and marks winner and independent task', () => {
    const gs = branchState()
    const view = buildAggregateView(gs, branchDef)
    expect(view.groupLive.g1.instanceId).toBe('root')
    expect(view.groupLive.g1.groupRunId).toBe('run-g1')
    expect(view.groupLive.g2.winner).toBe('x')
    expect(view.groupLive.g2.tasks.x.winner).toBe(true)
    expect(view.groupLive.g2.tasks.y.continuingIndependently).toBe(true)
    expect(view.groupLive.g2.tasks.y.frozenStatus).toBe('unfinished')
    expect(view.groupLive.g2.tasks.y.activeNodes).toEqual(['Y2'])
    // Task b's subtree includes the nested branches: b2 and y2 are both active.
    expect(view.groupLive.g1.tasks.b.activeNodes.sort()).toEqual(['B2', 'Y2'])
  })

  it('workflow task cards list their own child run active nodes (labels from the child graph), never a sibling\'s', () => {
    const gs: GraphState = {
      rootInstanceId: 'root', version: 'v', active: true, layout: mixedGraph, warnings: [],
      contexts: [
        ctx('root', { kind: 'root', state: 'waiting', nodes: [live('root', 'g1', 'waiting')] }),
        ctx('br-b', { parentInstanceId: 'root', taskKey: 'b', nodes: [live('br-b', 'w1', 'waiting')] }),
        ctx('c-t1', { kind: 'workflow', parentInstanceId: 'br-b', taskKey: 't1', workflowName: 'child-a', graph: { nodes: [{ id: 'build', label: 'Build A' }] }, nodes: [live('c-t1', 'build', 'running')] }),
        ctx('c-t3', { kind: 'workflow', parentInstanceId: 'br-b', taskKey: 't3', workflowName: 'child-a', graph: { nodes: [{ id: 'build', label: 'Build A' }] }, nodes: [live('c-t3', 'build', 'completed')] }),
        ctx('gc', { kind: 'spawn', parentInstanceId: 'c-t3', nodes: [live('gc', 'lint', 'waiting')], graph: { nodes: [{ id: 'lint', label: 'Lint' }] } }),
      ],
      groups: [
        panel('root', 'g1', [task('a'), task('b', { childInstanceId: 'br-b' })]),
        panel('br-b', 'w1', [
          task('t1', { kind: 'workflow', origin: 'child-a', childInstanceId: 'c-t1' }),
          task('t2', { kind: 'workflow', origin: 'child-b', childInstanceId: null, liveStatus: 'queued' }),
          task('t3', { kind: 'workflow', origin: 'child-a', childInstanceId: 'c-t3' }),
        ], { form: 'workflows' }),
      ],
    }
    const view = buildAggregateView(gs, mixedDef)
    expect(view.groupLive.w1.instanceId).toBe('br-b')
    expect(view.groupLive.w1.tasks.t1.activeNodes).toEqual(['Build A'])
    expect(view.groupLive.w1.tasks.t2.activeNodes).toEqual([])
    expect(view.groupLive.w1.tasks.t2.status).toBe('queued')
    // Descendants of a task's child run count as that task's active work.
    expect(view.groupLive.w1.tasks.t3.activeNodes).toEqual(['Lint'])
  })

  it('withholds a redacted task\'s child id', () => {
    const gs = branchState()
    gs.groups[0].tasks[1] = task('b', { childInstanceId: 'br-b', redacted: true })
    const liveMap = buildGroupLive(gs, resolveNodeContexts(gs, branchDef, branchGraph), branchGraph, branchDef)
    expect(liveMap.g1.tasks.b.childInstanceId).toBeNull()
    expect(liveMap.g1.tasks.b.activeNodes).toEqual([])
  })
})

describe('activeNodeLabels', () => {
  it('ignores redacted contexts', () => {
    const gs = branchState()
    gs.contexts[4] = { ...gs.contexts[4], redacted: true }
    expect(activeNodeLabels(gs, 'br-b', branchDef)).toEqual(['B2'])
  })
})

describe('gatherActivity', () => {
  const base = panel('root', 'g', [], { gatherMode: 'results_and_files' })
  it('describes gathering progress', () => {
    expect(gatherActivity({ ...base, state: 'gathering' })).toBe('Gathering files')
    expect(gatherActivity({
      ...base, state: 'gathering',
      gather: { mode: 'results_and_files', state: 'merging', mergeIndex: 1, tasks: [{ key: 'a', merged: true, reason: 'clean' }, { key: 'b', merged: false, reason: 'pending' }], attempts: [] },
    })).toBe('Gathering files (1/2 merged)')
  })
  it('describes an active resolution attempt', () => {
    expect(gatherActivity({
      ...base, state: 'resolving_conflicts',
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 1, attempt: 2, instanceId: 'root', nodeId: 'g' } },
    })).toBe('Resolving conflicts (attempt 2)')
  })
  it('describes a failed or rolled-back gather and is silent otherwise', () => {
    expect(gatherActivity({ ...base, state: 'done', gather: { mode: 'results_and_files', state: 'failed', mergeIndex: 0, tasks: [], attempts: [], error: 'git unavailable' } })).toBe('File gathering failed: git unavailable')
    expect(gatherActivity({ ...base, state: 'done', gather: { mode: 'results_and_files', state: 'rolled_back', mergeIndex: 0, tasks: [], attempts: [] } })).toBe('File gathering rolled back')
    expect(gatherActivity({ ...base, state: 'running' })).toBeUndefined()
  })
})

describe('findGroupPanel / selectionFromNode', () => {
  it('finds a panel by (instance, node) only', () => {
    const gs = branchState()
    expect(findGroupPanel(gs, 'br-b', 'g2')?.groupRunId).toBe('run-g2')
    expect(findGroupPanel(gs, 'root', 'g2')).toBeNull()
    expect(findGroupPanel(gs, null, 'g2')).toBeNull()
  })

  it('header, footer and task cards select their group; step nodes select (instance, node)', () => {
    expect(selectionFromNode({ id: 'group:g1:header', type: 'groupHeader', data: { definitionNodeId: 'g1', instanceId: 'root' } }, 'root'))
      .toEqual({ kind: 'group', rfId: 'group:g1:header', nodeId: 'g1', instanceId: 'root' })
    expect(selectionFromNode({ id: 'group:g2:footer', type: 'groupFooter', data: { definitionNodeId: 'g2', instanceId: 'br-b' } }, 'root'))
      .toEqual({ kind: 'group', rfId: 'group:g2:footer', nodeId: 'g2', instanceId: 'br-b' })
    expect(selectionFromNode({ id: 'a2', type: 'arbitronNode', data: { instanceId: 'br-a' } }, 'root'))
      .toEqual({ kind: 'node', rfId: 'a2', nodeId: 'a2', instanceId: 'br-a' })
  })

  it('falls back to the route instance only when the node carries no owner info', () => {
    expect(selectionFromNode({ id: 'a', type: 'arbitronNode', data: { label: 'A' } }, 'root').instanceId).toBe('root')
    expect(selectionFromNode({ id: 'x1', type: 'arbitronNode', data: { instanceId: null } }, 'root').instanceId).toBeNull()
  })
})
```

- [x] Step 7: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/aggregateState.test.ts
```
Expected:
```
 FAIL  src/graph/aggregateState.test.ts [ src/graph/aggregateState.test.ts ]
Error: Failed to resolve import "./aggregateState" from "src/graph/aggregateState.test.ts". Does the file exist?
```

- [x] Step 8: Implement `dashboard/src/graph/aggregateState.ts`:

```ts
import type { Node } from '@xyflow/react'
import type {
  ExecutionContext,
  GraphState,
  GroupGraph,
  GroupPanel,
  LiveNodeStateValue,
} from '../api/types'
import type { NodeExecutionState, WorkflowDefinition } from './types'
import type { GroupLiveSummary, GroupLiveTask, NodeContextInfo } from './groupLayout'

/** Instance-qualified node identity: child workflows reuse node ids, so nodeId alone is never a key. */
export function qualifiedKey(instanceId: string, nodeId: string): string {
  return `${instanceId}:${nodeId}`
}

const LIVE_TO_EXEC: Record<LiveNodeStateValue, NodeExecutionState> = {
  pending: 'pending',
  queued: 'queued',
  running: 'running',
  waiting: 'waiting',
  paused: 'paused',
  completed: 'completed',
  failed: 'failed',
  skipped: 'skipped',
  cancelled: 'cancelled',
  unknown: 'unknown',
}

/** Every live node state of every visible context, keyed `${instanceId}:${nodeId}`. */
export function buildAggregateExecutionStates(gs: GraphState): Record<string, NodeExecutionState> {
  const out: Record<string, NodeExecutionState> = {}
  for (const ctx of gs.contexts) {
    for (const n of ctx.nodes) {
      out[qualifiedKey(n.instanceId || ctx.instanceId, n.nodeId)] = LIVE_TO_EXEC[n.state] ?? 'unknown'
    }
  }
  return out
}

/** The panel of the group node `nodeId` executed by `instanceId`. */
export function findGroupPanel(gs: GraphState | null, instanceId: string | null, nodeId: string): GroupPanel | null {
  if (!gs || !instanceId) return null
  return gs.groups.find((p) => p.instanceId === instanceId && p.nodeId === nodeId) ?? null
}

/**
 * Which instance executes each definition node of the ROOT graph. Main-path
 * nodes run in the root; a body node runs in the branch instance of its owning
 * (group, task), resolved outermost-first through Phase 1 ownership so nested
 * groups map recursively: root --g1/b--> branch B --g2/x--> branch X.
 * An unlaunched task yields instanceId null; a redacted one hides it.
 */
export function resolveNodeContexts(gs: GraphState, def: WorkflowDefinition, layout: GroupGraph | null): Record<string, NodeContextInfo> {
  const contexts = new Map(gs.contexts.map((c) => [c.instanceId, c]))
  const out: Record<string, NodeContextInfo> = {}
  const root = gs.rootInstanceId
  const ownerChain = (nodeId: string): Array<{ groupNodeId: string; taskKey: string }> => {
    const chain: Array<{ groupNodeId: string; taskKey: string }> = []
    let cur = layout?.owner[nodeId]
    let guard = 0
    while (cur && guard < 64) {
      chain.unshift(cur)
      cur = layout?.owner[cur.groupNodeId]
      guard++
    }
    return chain
  }
  for (const n of def.nodes) {
    let instanceId: string | null = root
    let independent = false
    let hidden = false
    for (const step of ownerChain(n.id)) {
      const panel = findGroupPanel(gs, instanceId, step.groupNodeId)
      const task = panel?.tasks.find((t) => t.key === step.taskKey)
      if (!task) {
        instanceId = null
        break
      }
      if (task.redacted) {
        instanceId = null
        hidden = true
        break
      }
      // A decision releases EVERY task (blocking=false); only a task left
      // running on its own after a first-success decision is independent work.
      if (task.continuingIndependently) independent = true
      instanceId = task.childInstanceId ?? null
      if (!instanceId) break
      if (contexts.get(instanceId)?.redacted) {
        instanceId = null
        hidden = true
        break
      }
    }
    out[n.id] = { instanceId, independent, hiddenDependency: hidden }
  }
  return out
}

/** Projects the qualified states onto the rendered (definition-id keyed) graph. */
export function projectExecutionStates(
  qualified: Record<string, NodeExecutionState>,
  nodeContexts: Record<string, NodeContextInfo>,
  def: WorkflowDefinition,
): Record<string, NodeExecutionState> {
  const out: Record<string, NodeExecutionState> = {}
  for (const n of def.nodes) {
    const c = nodeContexts[n.id]
    if (c?.hiddenDependency) {
      out[n.id] = 'unknown'
      continue
    }
    if (!c?.instanceId) {
      out[n.id] = 'pending'
      continue
    }
    out[n.id] = qualified[qualifiedKey(c.instanceId, n.id)] ?? 'pending'
  }
  return out
}

function nodesOf(graph: Record<string, unknown> | undefined): Array<{ id: string; label?: string }> {
  const raw = graph?.nodes
  return Array.isArray(raw) ? (raw as Array<{ id: string; label?: string }>) : []
}

/** Labels of the running/waiting nodes of `instanceId` and all of its visible descendants. */
export function activeNodeLabels(gs: GraphState, instanceId: string, rootDef: WorkflowDefinition): string[] {
  const byId = new Map(gs.contexts.map((c) => [c.instanceId, c]))
  const graphOf = (ctx: ExecutionContext): Array<{ id: string; label?: string }> => {
    let cur: ExecutionContext | undefined = ctx
    let guard = 0
    while (cur && guard < 64) {
      if (cur.graph) return nodesOf(cur.graph)
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
      guard++
    }
    return rootDef.nodes
  }
  const inSubtree = (ctx: ExecutionContext): boolean => {
    let cur: ExecutionContext | undefined = ctx
    let guard = 0
    while (cur && guard < 64) {
      if (cur.instanceId === instanceId) return true
      cur = cur.parentInstanceId ? byId.get(cur.parentInstanceId) : undefined
      guard++
    }
    return false
  }
  const labels: string[] = []
  for (const ctx of gs.contexts) {
    if (ctx.redacted || !inSubtree(ctx)) continue
    const nodes = graphOf(ctx)
    for (const n of ctx.nodes) {
      if (n.state !== 'running' && n.state !== 'waiting') continue
      labels.push(nodes.find((d) => d.id === n.nodeId)?.label || n.nodeId)
    }
  }
  return labels
}

/** Human text of a group's file-gathering / resolution activity, if any. */
export function gatherActivity(panel: GroupPanel): string | undefined {
  const g = panel.gather
  if (panel.state === 'resolving_conflicts') {
    return g?.activeSession ? `Resolving conflicts (attempt ${g.activeSession.attempt})` : 'Resolving conflicts'
  }
  if (panel.state === 'gathering') {
    if (g && g.tasks.length > 0) {
      const merged = g.tasks.filter((t) => t.merged).length
      return `Gathering files (${merged}/${g.tasks.length} merged)`
    }
    return 'Gathering files'
  }
  if (g?.state === 'failed') return g.error ? `File gathering failed: ${g.error}` : 'File gathering failed'
  if (g?.state === 'rolled_back') return 'File gathering rolled back'
  return undefined
}

/** Live overlays for every group node of the root layout, keyed by group node id. */
export function buildGroupLive(
  gs: GraphState,
  nodeContexts: Record<string, NodeContextInfo>,
  layout: GroupGraph | null,
  rootDef: WorkflowDefinition,
): Record<string, GroupLiveSummary> {
  const out: Record<string, GroupLiveSummary> = {}
  for (const gid of layout?.order ?? []) {
    const instanceId = nodeContexts[gid]?.instanceId ?? null
    const panel = findGroupPanel(gs, instanceId, gid)
    if (!panel) continue
    const tasks: Record<string, GroupLiveTask> = {}
    for (const t of panel.tasks) {
      const child = t.redacted ? null : t.childInstanceId ?? null
      tasks[t.key] = {
        key: t.key,
        status: t.liveStatus,
        frozenStatus: t.frozenStatus,
        childInstanceId: child,
        blocking: t.blocking,
        continuingIndependently: t.continuingIndependently,
        cancelRequested: t.cancelRequested,
        redacted: t.redacted,
        launchError: t.launchError || undefined,
        winner: panel.winner === t.key,
        activeNodes: child ? activeNodeLabels(gs, child, rootDef) : [],
      }
    }
    out[gid] = {
      instanceId: panel.instanceId,
      groupRunId: panel.groupRunId,
      state: panel.state,
      progress: panel.progress,
      winner: panel.winner || undefined,
      activity: gatherActivity(panel),
      waitingReason: panel.waitingReasons[0],
      tasks,
    }
  }
  return out
}

/** Everything GraphView needs from an aggregate, derived once per GraphState version. */
export interface AggregateView {
  qualifiedStates: Record<string, NodeExecutionState>
  executionStates: Record<string, NodeExecutionState>
  nodeContexts: Record<string, NodeContextInfo>
  groupLive: Record<string, GroupLiveSummary>
}

export function buildAggregateView(gs: GraphState, def: WorkflowDefinition): AggregateView {
  const layout = gs.layout ?? null
  const qualifiedStates = buildAggregateExecutionStates(gs)
  const nodeContexts = resolveNodeContexts(gs, def, layout)
  return {
    qualifiedStates,
    executionStates: projectExecutionStates(qualifiedStates, nodeContexts, def),
    nodeContexts,
    groupLive: buildGroupLive(gs, nodeContexts, layout, def),
  }
}

/** What a click on the graph selected: a step node (with its owning instance) or a group. */
export type GraphSelection =
  | { kind: 'node'; rfId: string; nodeId: string; instanceId: string | null }
  | { kind: 'group'; rfId: string; nodeId: string; instanceId: string | null }

const GROUP_TYPES = new Set(['groupHeader', 'groupFooter', 'groupTaskCard'])

/**
 * Selection of a clicked React Flow node. Header, footer and task cards select
 * their GROUP (same panel); a step node selects (owning instance, node). A
 * node without owning-instance info (static/legacy graph) belongs to
 * `fallbackInstanceId`; an explicit null (unlaunched/redacted owner) stays null.
 */
export function selectionFromNode(n: Pick<Node, 'id' | 'type' | 'data'>, fallbackInstanceId: string | null): GraphSelection {
  const data = (n.data ?? {}) as { definitionNodeId?: unknown; instanceId?: unknown }
  const instanceId =
    'instanceId' in data ? (typeof data.instanceId === 'string' ? data.instanceId : null) : fallbackInstanceId
  if (n.type && GROUP_TYPES.has(n.type) && typeof data.definitionNodeId === 'string') {
    return { kind: 'group', rfId: n.id, nodeId: data.definitionNodeId, instanceId }
  }
  return { kind: 'node', rfId: n.id, nodeId: n.id, instanceId }
}
```

Design notes the implementer must keep:
- States are keyed `${instanceId}:${nodeId}` and only PROJECTED to definition ids for drawing, through the owner chain of `GroupGraph.owner` resolved outermost-first (root → panel of (instance, group) → task → `childInstanceId` → next level). Never collapse by node id.
- "Independent" means `continuingIndependently` only. A decision sets `blocking=false` on every task (P2-T1 `Decide`), so `!blocking` alone would mislabel finished tasks.
- A redacted task or context yields `instanceId: null, hiddenDependency: true` and state `unknown` — never `pending`/`completed` (design: unreadable descendants are not falsely pending or completed).

- [x] Step 9: Run it, confirm it passes.

```bash
cd dashboard && npx vitest run src/graph/aggregateState.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  19 passed (19)
```

- [x] Step 10: Commit.

Cycle 2: missing-module red confirmed, then all 19 projection tests and TypeScript passed. Reviewed instance-qualified identity, nested ownership, live/frozen separation and redaction. Replaced arbitrary traversal limits with cycle detection; Cycle 5 supplies the route/API-to-rendered-sink proof.

```bash
git add dashboard/src/graph/aggregateState.ts dashboard/src/graph/aggregateState.test.ts
git commit -m "feat(dashboard): instance-qualified aggregate node states and group overlays" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 3 — follow ALL active nodes (exact change to the follow helper)

The current helper (`useFollowActiveNode.ts:35-54`) only ever calls `setCenter` on ONE anchor — the root cursor chosen by `selectAnchorNodeId` — so with two branches running the viewport chases one and pans the other off screen. The change: an optional third argument `bounds` (the rectangle around every active node, passed by `GraphView` when ≥2 nodes are active). When bounds are present the effect and `recenter` call `fitBounds(bounds, { padding: FOLLOW_BOUNDS_PADDING, duration })` instead of `setCenter`; with `bounds` null the behaviour is byte-for-byte the old one (all 14 existing tests unchanged).

- [x] Step 11: Write the failing tests. In `dashboard/src/graph/useFollowActiveNode.test.ts` replace lines 4-9 with:

```ts
const setCenter = vi.fn()
const getZoom = vi.fn(() => 1.5)
const fitBounds = vi.fn(() => Promise.resolve(true))

vi.mock('@xyflow/react', () => ({
  useReactFlow: () => ({ setCenter, getZoom, fitBounds }),
}))
```

add `  fitBounds.mockClear()` directly under `  getZoom.mockClear()` in the `beforeEach` (line 15), and append after the last line (222):

```ts
// P4-T8: several concurrently active nodes must ALL stay in view — following a
// single node would pan the other parallel paths off screen.
describe('useFollowActiveNode — several concurrently active nodes (bounds)', () => {
  type B = { x: number; y: number; width: number; height: number }
  const a1 = { x: 100, y: 100 }
  const b1: B = { x: 0, y: 0, width: 600, height: 200 }

  it('frames the bounds of all active nodes instead of centering on one', () => {
    const { rerender } = renderHook(
      ({ a, b }: { a: { x: number; y: number }; b: B | null }) => useFollowActiveNode(a, true, b),
      { initialProps: { a: a1, b: b1 } },
    )
    expect(fitBounds).not.toHaveBeenCalled() // first sight: recorded only

    const b2: B = { x: 0, y: 0, width: 900, height: 400 }
    rerender({ a: { x: 450, y: 200 }, b: b2 })

    expect(fitBounds).toHaveBeenCalledTimes(1)
    expect(fitBounds).toHaveBeenCalledWith(b2, { padding: 0.25, duration: 400 })
    expect(setCenter).not.toHaveBeenCalled()
  })

  it('does not move when the bounds are unchanged (new object, same rectangle)', () => {
    const { rerender } = renderHook(
      ({ b }: { b: B }) => useFollowActiveNode({ x: 300, y: 100 }, true, b),
      { initialProps: { b: b1 } },
    )
    rerender({ b: { ...b1 } })
    expect(fitBounds).not.toHaveBeenCalled()
    expect(setCenter).not.toHaveBeenCalled()
  })

  it('does not move after the user paused following', () => {
    const { result, rerender } = renderHook(
      ({ b }: { b: B }) => useFollowActiveNode({ x: 300, y: 100 }, true, b),
      { initialProps: { b: b1 } },
    )
    act(() => result.current.onMoveStart({ type: 'pointerdown' }))
    rerender({ b: { x: 0, y: 0, width: 1200, height: 300 } })
    expect(fitBounds).not.toHaveBeenCalled()
  })

  it('recenter frames the latest bounds again', () => {
    const { result } = renderHook(() => useFollowActiveNode({ x: 300, y: 100 }, true, b1))
    act(() => result.current.recenter())
    expect(fitBounds).toHaveBeenCalledWith(b1, { padding: 0.25 })
    expect(setCenter).not.toHaveBeenCalled()
  })

  it('falls back to centering once only one node is active again', () => {
    const { rerender } = renderHook(
      ({ a, b }: { a: { x: number; y: number }; b: B | null }) => useFollowActiveNode(a, true, b),
      { initialProps: { a: { x: 300, y: 100 } as { x: number; y: number }, b: b1 as B | null } },
    )
    rerender({ a: { x: 100, y: 500 }, b: null })
    expect(setCenter).toHaveBeenCalledWith(100, 500, { zoom: 1.5, duration: 400 })
    expect(fitBounds).not.toHaveBeenCalled()
  })
})
```

- [x] Step 12: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/useFollowActiveNode.test.ts
```
Expected:
```
 FAIL  src/graph/useFollowActiveNode.test.ts > useFollowActiveNode — several concurrently active nodes (bounds) > frames the bounds of all active nodes instead of centering on one
AssertionError: expected "vi.fn()" to be called 1 times, but got 0 times
 FAIL  src/graph/useFollowActiveNode.test.ts > useFollowActiveNode — several concurrently active nodes (bounds) > recenter frames the latest bounds again
AssertionError: expected "vi.fn()" to be called with arguments: [ …(2) ]
      Tests  2 failed | 17 passed (19)
```

- [x] Step 13: Implement. Replace the whole of `dashboard/src/graph/constants.ts` (lines 1-4) with:

```ts
/** Readable default zoom level for the execution graph (within minZoom/maxZoom). */
export const DEFAULT_ZOOM = 0.9
/** Animation duration (ms) for follow-the-active-node viewport moves. */
export const FOLLOW_DURATION_MS = 400
/** fitBounds padding when the viewport frames several concurrently active nodes. */
export const FOLLOW_BOUNDS_PADDING = 0.25
```

Replace the whole of `dashboard/src/graph/useFollowActiveNode.ts` (lines 1-69) with:

```ts
import { useCallback, useEffect, useRef, useState } from 'react'
import { useReactFlow } from '@xyflow/react'
import { DEFAULT_ZOOM, FOLLOW_BOUNDS_PADDING, FOLLOW_DURATION_MS } from './constants'

interface AnchorPosition {
  x: number
  y: number
}

/** Flow-space rectangle enclosing every concurrently active node. */
export interface FollowBounds {
  x: number
  y: number
  width: number
  height: number
}

interface UseFollowActiveNodeResult {
  following: boolean
  onMoveStart: (event: unknown) => void
  recenter: () => void
}

function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return false
  }
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function sameBounds(a: FollowBounds | null, b: FollowBounds | null): boolean {
  if (a === b) return true
  if (!a || !b) return false
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
}

/**
 * Keeps the viewport on the active work while the run is live.
 *
 * With ONE active node the viewport follows that node (setCenter). With
 * several concurrently active nodes (`bounds` set — parallel branches, workflow
 * tasks, independent work) it frames ALL of them (fitBounds): following a
 * single node would pan the other concurrent paths off screen, hiding exactly
 * the parallelism the view exists to show.
 */
export function useFollowActiveNode(
  anchor: AnchorPosition | null,
  isRunning: boolean,
  bounds: FollowBounds | null = null,
): UseFollowActiveNodeResult {
  const { setCenter, getZoom, fitBounds } = useReactFlow()
  const [following, setFollowing] = useState(true)

  // Remember the anchor/bounds we last moved to so the first render does not
  // animate (mount-centering in GraphView handles the initial view) and so we
  // only animate when the target actually changes.
  const prevAnchorRef = useRef<AnchorPosition | null>(null)
  const prevBoundsRef = useRef<FollowBounds | null>(null)

  useEffect(() => {
    // Only auto-pan for a running instance with an anchor. A completed/stopped
    // instance must not pan on a late anchor change (trailing trace/layout
    // update); `following` stays user-controllable for the running case.
    if (!anchor || !isRunning) return
    const prev = prevAnchorRef.current
    // First time we see any anchor: record it, do not animate.
    if (prev === null) {
      prevAnchorRef.current = anchor
      prevBoundsRef.current = bounds
      return
    }
    // Unchanged target: nothing to do.
    if (prev.x === anchor.x && prev.y === anchor.y && sameBounds(prevBoundsRef.current, bounds)) return

    prevAnchorRef.current = anchor
    prevBoundsRef.current = bounds
    if (!following) return

    const duration = prefersReducedMotion() ? 0 : FOLLOW_DURATION_MS
    if (bounds) {
      void fitBounds(bounds, { padding: FOLLOW_BOUNDS_PADDING, duration })
      return
    }
    setCenter(anchor.x, anchor.y, { zoom: getZoom(), duration })
  }, [anchor, bounds, isRunning, following, setCenter, getZoom, fitBounds])

  const onMoveStart = useCallback((event: unknown) => {
    if (event != null) setFollowing(false)
  }, [])

  const recenter = useCallback(() => {
    setFollowing(true)
    if (prevBoundsRef.current) {
      void fitBounds(prevBoundsRef.current, { padding: FOLLOW_BOUNDS_PADDING })
      return
    }
    if (prevAnchorRef.current) {
      const a = prevAnchorRef.current
      setCenter(a.x, a.y, { zoom: DEFAULT_ZOOM })
    }
  }, [setCenter, fitBounds])

  return { following, onMoveStart, recenter }
}
```

- [x] Step 14: Run it, confirm it passes.

```bash
cd dashboard && npx vitest run src/graph/useFollowActiveNode.test.ts src/routes/GraphView.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  2 passed (2)
```

- [x] Step 15: Commit.

Cycle 3: confirmed 2 new bounds tests red with the old helper, then all 33 helper/GraphView tests and TypeScript green. Reviewed unchanged single-anchor behavior, bounds changes, pause/recenter and reduced-motion behavior; Cycle 5 wires real laid-out active bounds.

```bash
git add dashboard/src/graph/constants.ts dashboard/src/graph/useFollowActiveNode.ts dashboard/src/graph/useFollowActiveNode.test.ts
git commit -m "feat(dashboard): follow helper frames every concurrently active node" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 4 — `GroupPanel`

- [x] Step 16: Write the failing tests `dashboard/src/components/GroupPanel.test.tsx`:

```tsx
import { render, screen, fireEvent, within } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { GatherReport, GroupPanel as GroupPanelData, GroupPanelTask } from '../api/types'
import GroupPanel, { outstandingTasks } from './GroupPanel'

function task(key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask {
  return {
    key, kind: 'branch', origin: key, childInstanceId: `child-${key}`, liveStatus: 'running', frozenStatus: '',
    blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
  }
}

function panel(over: Partial<GroupPanelData> = {}): GroupPanelData {
  return {
    groupRunId: 'run-1', instanceId: 'root', nodeId: 'g1', form: 'branches', completionRule: 'all_succeed',
    unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running',
    tasks: [task('a'), task('b')],
    progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
    waitingReasons: ['Waiting for tasks a, b'],
    ...over,
  }
}

const show = (p: GroupPanelData | null, onOpenSession = vi.fn()) =>
  render(
    <MemoryRouter>
      <GroupPanel panel={p} label="Review in parallel" onOpenSession={onOpenSession} />
    </MemoryRouter>,
  )

const row = (key: string) => screen.getByTestId(`group-task-row-${key}`)

describe('GroupPanel — configuration and ordered tasks (happy path)', () => {
  it('shows rule, unfinished behaviour, gather mode, form and state', () => {
    show(panel())
    expect(screen.getByText('Review in parallel')).toBeInTheDocument()
    expect(screen.getByText('All succeed')).toBeInTheDocument()
    expect(screen.getByText('cancel unfinished')).toBeInTheDocument()
    expect(screen.getByText('results')).toBeInTheDocument()
    expect(screen.getByText('branches')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-panel')).getAllByText('running').length).toBeGreaterThan(0)
  })

  it('lists tasks in author order with links to their child runs', () => {
    show(panel({ tasks: [task('zeta'), task('alpha')] }))
    const keys = [...document.querySelectorAll('[data-testid^="group-task-row-"]')].map((r) => r.getAttribute('data-testid'))
    expect(keys).toEqual(['group-task-row-zeta', 'group-task-row-alpha'])
    expect(within(row('zeta')).getByRole('link', { name: 'open run' }).getAttribute('href')).toBe('/instances/child-zeta')
  })

  it('shows progress and the outstanding blocking tasks', () => {
    show(panel({ tasks: [task('a', { liveStatus: 'completed' }), task('b')], progress: { total: 2, completed: 1, failed: 0, cancelled: 0, active: 1, notStarted: 0 } }))
    expect(screen.getByText('1/2 done · 1 active')).toBeInTheDocument()
    expect(screen.getByText('1 outstanding blocking task: b')).toBeInTheDocument()
    expect(screen.getByText('Waiting for tasks a, b')).toBeInTheDocument()
  })
})

describe('GroupPanel — success: winner, frozen outcome, gather history', () => {
  it('shows the winner and the frozen decision with each task\'s gathered status', () => {
    show(panel({
      completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'b',
      tasks: [
        task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
        task('b', { liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
      ],
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: [],
      decision: {
        outcome: 'success', rule: 'first_success', behavior: 'finish_independently', winnerKey: 'b', reason: 'task b succeeded first',
        decidedAt: '2026-10-16T10:00:00Z',
        tasks: [
          { key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'child-a', status: 'unfinished', cancelRequested: false, winner: false },
          { key: 'b', kind: 'branch', origin: 'b1', childInstanceId: 'child-b', status: 'completed', result: { verdict: 'ok' }, cancelRequested: false, winner: true },
        ],
      },
      finalOutput: { winner: 'b' },
    }))
    expect(screen.getByText('Winner:')).toBeInTheDocument()
    expect(screen.getByText('task b succeeded first', { exact: false })).toBeInTheDocument()
    expect(screen.queryByTestId('group-waiting')).toBeNull()
    expect(screen.getByText('Gathered output')).toBeInTheDocument()
  })

  // A former unfinished non-winner can read COMPLETED live while the immutable
  // gathered snapshot still says UNFINISHED; both must be visible, side by side.
  it('keeps the live status separate from the frozen gathered status', () => {
    show(panel({
      state: 'done',
      tasks: [task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true })],
      waitingReasons: [],
    }))
    const r = row('a')
    expect(within(r).getByText('completed')).toBeInTheDocument()
    expect(within(r).getByText('unfinished')).toBeInTheDocument()
    expect(within(r).getByText('non-blocking')).toBeInTheDocument()
  })

  it('shows the file-merge report and the resolution history', () => {
    const gather: GatherReport = {
      mode: 'results_and_files', state: 'applied', mergeIndex: 2,
      tasks: [{ key: 'a', merged: true, reason: 'clean' }, { key: 'b', merged: true, reason: 'resolved', conflictPaths: ['src/x.go'] }],
      attempts: [
        { mergeIndex: 1, attempt: 1, taskKey: 'b', conflictPaths: ['src/x.go'], outcome: 'still_conflicted', startedAt: '2026-10-16T10:00:00Z', endedAt: '2026-10-16T10:05:00Z' },
        { mergeIndex: 1, attempt: 2, taskKey: 'b', conflictPaths: ['src/x.go'], outcome: 'resolved', startedAt: '2026-10-16T10:06:00Z' },
      ],
    }
    show(panel({ gatherMode: 'results_and_files', state: 'done', waitingReasons: [], gather }))
    expect(screen.getByText('results + files')).toBeInTheDocument()
    const history = screen.getByTestId('group-resolution-history')
    expect(within(history).getByText('merge 2, attempt 1 (task b): still conflicted')).toBeInTheDocument()
    expect(within(history).getByText('merge 2, attempt 2 (task b): resolved')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-gather')).getByText('conflicts: src/x.go')).toBeInTheDocument()
  })
})

describe('GroupPanel — failure details', () => {
  it('shows frozen failure details and a launch error, and styles cancelled work distinctly', () => {
    const { container } = show(panel({
      state: 'done', waitingReasons: [],
      tasks: [
        task('a', { liveStatus: 'failed', frozenStatus: 'failed', blocking: false }),
        task('b', { liveStatus: 'cancelled', frozenStatus: 'cancelled', blocking: false, cancelRequested: true }),
        task('c', { liveStatus: 'failed', childInstanceId: null, launchError: 'launch failed: child workflow not found', blocking: false }),
      ],
      decision: {
        outcome: 'failure', rule: 'all_succeed', behavior: 'cancel_unfinished', reason: 'task a failed', decidedAt: '2026-10-16T10:00:00Z',
        tasks: [{ key: 'a', kind: 'branch', origin: 'a1', childInstanceId: 'child-a', status: 'failed', failure: { state: 'failed', failedNodeId: 'a2', error: 'exit status 1' }, cancelRequested: false, winner: false }],
      },
    }))
    expect(within(screen.getByTestId('group-decision')).getByText('failed: exit status 1 (node a2)')).toBeInTheDocument()
    expect(screen.getByText('launch failed: child workflow not found')).toBeInTheDocument()
    expect(within(row('b')).getByText('cancel requested')).toBeInTheDocument()
    expect(within(row('c')).getByText('not launched')).toBeInTheDocument()
    expect(container.querySelector('[class*="statusCancelled"]')).not.toBeNull()
  })

  it('shows a failed gather error', () => {
    show(panel({ gatherMode: 'results_and_files', state: 'done', waitingReasons: [], gather: { mode: 'results_and_files', state: 'rolled_back', mergeIndex: 0, tasks: [], attempts: [], rollbackStatus: 'rolled_back', error: 'merge aborted' } }))
    expect(within(screen.getByTestId('group-gather')).getByText('merge aborted')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-gather')).getByText('rolled back · rolled back')).toBeInTheDocument()
  })
})

describe('GroupPanel — waiting with zero outstanding tasks', () => {
  // Zero outstanding task dependencies does NOT mean the gather is ready.
  it('shows why it waits on resolution although no task is outstanding, and links the session', () => {
    const onOpen = vi.fn()
    const session = { groupRunId: 'run-1', mergeIndex: 1, attempt: 1, instanceId: 'root', nodeId: 'g1' }
    show(panel({
      gatherMode: 'results_and_files', state: 'resolving_conflicts',
      tasks: [task('a', { liveStatus: 'completed', blocking: false }), task('b', { liveStatus: 'completed', blocking: false })],
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: ['Resolving merge conflicts for task b (attempt 1)'],
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: session },
    }), onOpen)
    expect(screen.getByText('0 outstanding blocking tasks')).toBeInTheDocument()
    expect(within(screen.getByTestId('group-waiting')).getByText('Resolving merge conflicts for task b (attempt 1)')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open resolution session' }))
    expect(onOpen).toHaveBeenCalledWith(session)
  })

  it('without a reason from the server it still says the group has not continued', () => {
    show(panel({
      state: 'gathering',
      tasks: [task('a', { liveStatus: 'completed', blocking: false })],
      progress: { total: 1, completed: 1, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      waitingReasons: [],
    }))
    expect(screen.getByText('No tasks outstanding — the group has not continued yet (gathering).')).toBeInTheDocument()
  })
})

describe('GroupPanel — redaction and edge cases', () => {
  it('shows a hidden dependency for a redacted task, without identifiers or links', () => {
    show(panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] }))
    expect(within(row('b')).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(row('b')).queryByRole('link')).toBeNull()
    expect(within(row('a')).getByRole('link')).toBeInTheDocument()
  })

  it('renders an empty state for a group that has not started', () => {
    show(null)
    expect(screen.getByText('This group has not started yet.')).toBeInTheDocument()
  })

  it('outstandingTasks ignores released and terminal tasks', () => {
    const p = panel({ tasks: [task('a'), task('b', { blocking: false }), task('c', { liveStatus: 'completed' }), task('d', { liveStatus: 'queued' })] })
    expect(outstandingTasks(p).map((t) => t.key)).toEqual(['a', 'd'])
  })
})
```

- [x] Step 17: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/components/GroupPanel.test.tsx
```
Expected:
```
 FAIL  src/components/GroupPanel.test.tsx [ src/components/GroupPanel.test.tsx ]
Error: Failed to resolve import "./GroupPanel" from "src/components/GroupPanel.test.tsx". Does the file exist?
```

- [x] Step 18: Implement. Create `dashboard/src/components/GroupPanel.tsx`:

```tsx
import { Link } from 'react-router-dom'
import type {
  FrozenTask,
  GroupPanel as GroupPanelData,
  GroupPanelTask,
  ResolutionSessionRef,
} from '../api/types'
import { gatherText, progressText, ruleText, stateText, unfinishedText } from '../graph/groupText'
import { localDateTime } from '../utils/time'
import styles from './GroupPanel.module.css'

interface GroupPanelProps {
  /** The group run's panel; null when the group has not started (no run yet). */
  panel: GroupPanelData | null
  /** Display name of the group node. */
  label?: string
  /** Opens the interactive conflict-resolution session (the parent's session at the group node). */
  onOpenSession?: (session: ResolutionSessionRef) => void
}

const TERMINAL_TASK = new Set(['completed', 'failed', 'cancelled', 'unfinished'])

const STATUS_CLASS: Record<string, string> = {
  not_started: styles.statusPending,
  queued: styles.statusQueued,
  running: styles.statusRunning,
  waiting: styles.statusWaiting,
  paused: styles.statusWaiting,
  completed: styles.statusCompleted,
  failed: styles.statusFailed,
  cancelled: styles.statusCancelled,
  unfinished: styles.statusCancelled,
  unknown: styles.statusPending,
}

function Status({ value }: { value: string }) {
  return <span className={`${styles.status} ${STATUS_CLASS[value] ?? styles.statusPending}`}>{stateText(value)}</span>
}

/** Blocking tasks whose live status is not terminal yet. */
export function outstandingTasks(panel: GroupPanelData): GroupPanelTask[] {
  return panel.tasks.filter((t) => t.blocking && !TERMINAL_TASK.has(t.liveStatus))
}

function failureText(f: Record<string, unknown> | undefined): string | null {
  if (!f) return null
  const err = typeof f.error === 'string' ? f.error : f.error != null ? JSON.stringify(f.error) : ''
  const node = typeof f.failedNodeId === 'string' && f.failedNodeId ? ` (node ${f.failedNodeId})` : ''
  const state = typeof f.state === 'string' ? f.state : ''
  const text = [state, err].filter(Boolean).join(': ')
  return text ? `${text}${node}` : null
}

function TaskRow({ task, index, frozen }: { task: GroupPanelTask; index: number; frozen: FrozenTask | undefined }) {
  const gathered = task.frozenStatus || frozen?.status || ''
  return (
    <tr data-testid={`group-task-row-${task.key}`} className={task.continuingIndependently ? styles.rowIndependent : undefined}>
      <td className={styles.cellMuted}>{index + 1}</td>
      <td className={styles.cellKey}>{task.key}</td>
      <td className={styles.cellMuted}>{task.kind}</td>
      <td>
        <Status value={task.liveStatus} />
      </td>
      <td>{gathered ? <Status value={gathered} /> : <span className={styles.cellMuted}>—</span>}</td>
      <td className={styles.flags}>
        {task.continuingIndependently && <span className={`${styles.badge} ${styles.badgeIndependent}`}>non-blocking</span>}
        {task.blocking && !TERMINAL_TASK.has(task.liveStatus) && <span className={styles.badge}>blocking</span>}
        {task.cancelRequested && <span className={`${styles.badge} ${styles.badgeCancel}`}>cancel requested</span>}
        {task.launchError && <span className={styles.error}>{task.launchError}</span>}
      </td>
      <td>
        {task.redacted ? (
          <span className={styles.hidden}>hidden dependency</span>
        ) : task.childInstanceId ? (
          <Link to={`/instances/${task.childInstanceId}`} className={styles.link}>
            open run
          </Link>
        ) : (
          <span className={styles.cellMuted}>not launched</span>
        )}
      </td>
    </tr>
  )
}

/**
 * GroupPanel shows one group run: completion rule, unfinished behaviour,
 * gather mode and state; ordered tasks with LIVE status next to the FROZEN
 * gathered status (a former unfinished non-winner may read completed live while
 * the gathered snapshot still says unfinished); progress, outstanding blocking
 * tasks and why the gather is waiting (zero outstanding tasks is not "ready");
 * winner; frozen results/failure details; the file-merge report and resolution
 * history; and a link into the live resolution session. Redacted tasks show a
 * generic hidden dependency and no identifiers.
 */
export default function GroupPanel({ panel, label, onOpenSession }: GroupPanelProps) {
  if (!panel) {
    return (
      <div className={styles.panel} data-testid="group-panel">
        <div className={styles.header}>
          <span className={styles.title}>{label ?? 'Parallel group'}</span>
        </div>
        <div className={styles.empty}>This group has not started yet.</div>
      </div>
    )
  }

  const outstanding = outstandingTasks(panel)
  const frozenByKey = new Map((panel.decision?.tasks ?? []).map((t) => [t.key, t]))
  const gather = panel.gather
  const waiting = panel.state !== 'done'

  return (
    <div className={styles.panel} data-testid="group-panel">
      <div className={styles.header}>
        <span className={styles.title}>{label ?? panel.nodeId}</span>
        <Status value={panel.state} />
      </div>

      <dl className={styles.facts}>
        <dt>Rule</dt>
        <dd>{ruleText(panel.completionRule)}</dd>
        <dt>Unfinished tasks</dt>
        <dd>{unfinishedText(panel.unfinishedBehavior)}</dd>
        <dt>Gather</dt>
        <dd>{gatherText(panel.gatherMode)}</dd>
        <dt>Form</dt>
        <dd>{panel.form}</dd>
      </dl>

      <section className={styles.section}>
        <div className={styles.sectionTitle}>Progress</div>
        <div>{progressText(panel.progress)}</div>
        <div className={styles.cellMuted}>
          {outstanding.length} outstanding blocking {outstanding.length === 1 ? 'task' : 'tasks'}
          {outstanding.length > 0 ? `: ${outstanding.map((t) => t.key).join(', ')}` : ''}
        </div>
      </section>

      {waiting && (
        <section className={styles.section} data-testid="group-waiting">
          <div className={styles.sectionTitle}>Waiting on</div>
          {panel.waitingReasons.length > 0 ? (
            <ul className={styles.list}>
              {panel.waitingReasons.map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
          ) : (
            <div className={styles.cellMuted}>
              {outstanding.length === 0
                ? `No tasks outstanding — the group has not continued yet (${stateText(panel.state)}).`
                : 'Outstanding tasks (see below).'}
            </div>
          )}
        </section>
      )}

      {panel.winner && (
        <div className={styles.winner}>
          Winner: <strong>{panel.winner}</strong>
        </div>
      )}

      {gather?.activeSession && onOpenSession && (
        <button type="button" className={styles.sessionButton} onClick={() => onOpenSession(gather.activeSession!)}>
          Open resolution session
        </button>
      )}

      <section className={styles.section}>
        <div className={styles.sectionTitle}>Tasks</div>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>#</th>
              <th>Task</th>
              <th>Kind</th>
              <th>Live</th>
              <th>Gathered</th>
              <th />
              <th>Run</th>
            </tr>
          </thead>
          <tbody>
            {panel.tasks.map((t, i) => (
              <TaskRow key={t.key} task={t} index={i} frozen={frozenByKey.get(t.key)} />
            ))}
          </tbody>
        </table>
      </section>

      {panel.decision && (
        <section className={styles.section} data-testid="group-decision">
          <div className={styles.sectionTitle}>Gathered outcome (frozen)</div>
          <div>
            <Status value={panel.decision.outcome === 'success' ? 'completed' : 'failed'} /> {panel.decision.outcome} — {panel.decision.reason}
          </div>
          <div className={styles.cellMuted}>decided {localDateTime(panel.decision.decidedAt)}</div>
          <ul className={styles.list}>
            {panel.decision.tasks.map((t) => {
              const failure = failureText(t.failure)
              return (
                <li key={t.key}>
                  <span className={styles.cellKey}>{t.key}</span> <Status value={t.status} />
                  {t.winner && <span className={`${styles.badge} ${styles.badgeWinner}`}>winner</span>}
                  {failure && <div className={styles.error}>{failure}</div>}
                  {t.result && Object.keys(t.result).length > 0 && (
                    <details>
                      <summary>result</summary>
                      <pre className={styles.pre}>{JSON.stringify(t.result, null, 2)}</pre>
                    </details>
                  )}
                </li>
              )
            })}
          </ul>
        </section>
      )}

      {gather && gather.mode === 'results_and_files' && (
        <section className={styles.section} data-testid="group-gather">
          <div className={styles.sectionTitle}>File gathering</div>
          <div>
            {stateText(gather.state)}
            {gather.rollbackStatus ? ` · ${stateText(gather.rollbackStatus)}` : ''}
          </div>
          {gather.error && <div className={styles.error}>{gather.error}</div>}
          <ul className={styles.list}>
            {gather.tasks.map((t) => (
              <li key={t.key}>
                <span className={styles.cellKey}>{t.key}</span> {t.merged ? 'merged' : 'not merged'}
                {t.reason ? ` — ${t.reason}` : ''}
                {t.conflictPaths && t.conflictPaths.length > 0 && <div className={styles.cellMuted}>conflicts: {t.conflictPaths.join(', ')}</div>}
              </li>
            ))}
          </ul>
          {gather.attempts.length > 0 && (
            <>
              <div className={styles.sectionTitle}>Resolution history</div>
              <ul className={styles.list} data-testid="group-resolution-history">
                {gather.attempts.map((a) => (
                  <li key={`${a.mergeIndex}-${a.attempt}`}>
                    merge {a.mergeIndex + 1}, attempt {a.attempt} (task {a.taskKey}): {stateText(a.outcome)}
                    {a.conflictPaths.length > 0 && <div className={styles.cellMuted}>{a.conflictPaths.join(', ')}</div>}
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      {panel.finalOutput && (
        <details className={styles.section}>
          <summary>Gathered output</summary>
          <pre className={styles.pre}>{JSON.stringify(panel.finalOutput, null, 2)}</pre>
        </details>
      )}
    </div>
  )
}
```

Create `dashboard/src/components/GroupPanel.module.css`:

```css
.panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  height: 100%;
  overflow-y: auto;
  padding: 12px 14px;
  background: var(--bg-surface);
  border-left: 1px solid var(--border-color);
  font-size: 12px;
  color: var(--text-secondary);
}

.header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 2px 12px;
}

.facts dt {
  color: var(--text-muted);
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  align-self: center;
}

.section {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.sectionTitle {
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-muted);
}

.list {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.table {
  width: 100%;
  border-collapse: collapse;
}

.table th {
  text-align: left;
  font-size: 10px;
  color: var(--text-muted);
  font-weight: 600;
  padding: 2px 4px;
}

.table td {
  padding: 3px 4px;
  border-top: 1px solid var(--border-subtle);
  vertical-align: top;
}

.rowIndependent td {
  background: rgba(155, 89, 182, 0.08);
}

.cellKey {
  font-family: var(--font-mono);
  color: var(--text-primary);
}

.cellMuted {
  color: var(--text-muted);
}

.flags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.status {
  display: inline-block;
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  padding: 0 4px;
  border-radius: var(--radius-sm);
  border: 1px solid currentColor;
}

.statusPending {
  color: var(--text-dim);
}

.statusQueued {
  color: var(--state-queued);
  border-style: dashed;
}

.statusRunning {
  color: var(--state-running);
}

.statusWaiting {
  color: var(--state-waiting);
}

.statusCompleted {
  color: var(--state-completed);
}

.statusFailed {
  color: var(--state-failed);
}

.statusCancelled {
  color: var(--state-cancelled);
}

.badge {
  font-size: 9px;
  font-weight: 700;
  text-transform: uppercase;
  padding: 0 4px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border-color);
  color: var(--text-muted);
}

.badgeIndependent {
  color: var(--state-paused);
  border-color: var(--state-paused);
}

.badgeCancel {
  color: var(--state-cancelled);
  border-color: var(--state-cancelled);
}

.badgeWinner {
  color: var(--state-completed);
  border-color: var(--state-completed);
  margin-left: 4px;
}

.winner {
  color: var(--state-completed);
}

.hidden {
  font-style: italic;
  color: var(--text-muted);
}

.error {
  color: var(--state-failed);
}

.link {
  color: var(--accent);
}

.empty {
  color: var(--text-muted);
  font-style: italic;
}

.sessionButton {
  align-self: flex-start;
  padding: 4px 10px;
  font-size: 12px;
  background: var(--bg-hover);
  border: 1px solid var(--state-waiting);
  border-radius: var(--radius-md);
  color: var(--state-waiting);
  cursor: pointer;
}

.pre {
  font-family: var(--font-mono);
  font-size: 11px;
  white-space: pre-wrap;
  max-height: 200px;
  overflow: auto;
}
```

Waiting reasons are rendered verbatim: P4-T2 produces them as human-readable sentences (`GroupPanel.WaitingReasons []string`). When the server supplies none while the group is not `done` and no blocking task is outstanding, the panel still states that the group has not continued (design §1: zero outstanding tasks does not imply ready).

- [x] Step 19: Run it, confirm it passes.

```bash
cd dashboard && npx vitest run src/components/GroupPanel.test.tsx && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  13 passed (13)
```

- [x] Step 20: Commit.

Cycle 4: missing-module red confirmed, then all 13 panel tests and TypeScript passed. Reviewed ordered live/frozen rows, zero-dependency waiting, failure/merge details and owner-qualified session callback; Cycle 5 mounts this panel with the real aggregate hook.

```bash
git add dashboard/src/components/GroupPanel.tsx dashboard/src/components/GroupPanel.module.css dashboard/src/components/GroupPanel.test.tsx
git commit -m "feat(dashboard): group panel with live vs frozen tasks, waiting reasons, merge and resolution history" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 5 — ExecutionView integration

- [x] Step 21: Write the failing view tests `dashboard/src/routes/ExecutionView.groups.test.tsx`. The REAL `useGraphState` runs; only `api.client` (keyed by instance id and node id — a read against the wrong instance 404s), `WebSocket` (URL recorder) and React Flow (nodes rendered as clickable divs exposing `data-state`) are faked:

```tsx
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ExecutionContext, GraphState, GroupGraph, GroupPanel, GroupPanelTask, Instance, NodeExecution, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

// ---------------------------------------------------------------------------
// React Flow stub: every rendered node becomes a clickable div exposing its
// type and execution state, so the test reads what the graph would highlight.
// ---------------------------------------------------------------------------
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

// ---------------------------------------------------------------------------
// Fixture: start -> g (branches; a: a1 -> a2, b: b1) -> done / oops
// ---------------------------------------------------------------------------
const n = (id: string, type = 'llm_call', label = id.toUpperCase()) => ({ id, type, label, description: '', config: {} })
const def: WorkflowDefinition = {
  name: 'fanout', description: '', author: '',
  nodes: [n('start'), n('g', 'parallel_group', 'Fan out'), n('a1'), n('a2'), n('b1'), n('done'), n('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'a1', output: 'task', label: 'a' },
    { source: 'g', target: 'b1', output: 'task', label: 'b' },
    { source: 'a1', target: 'a2' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}
const layout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'b1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
      bodies: { a: ['a1', 'a2'], b: ['b1'] }, nested: [], successTarget: 'done', failureTarget: 'oops',
    },
  },
  owner: { a1: { groupNodeId: 'g', taskKey: 'a' }, a2: { groupNodeId: 'g', taskKey: 'a' }, b1: { groupNodeId: 'g', taskKey: 'b' } },
  order: ['g'],
}
const rootInstance = {
  id: 'root', ownerId: 'u1', workflowName: 'fanout', state: 'waiting', priority: 'normal', variables: {}, originalVariables: {},
  currentNodeId: 'g', createdAt: '2026-10-16T10:00:00Z', updatedAt: '2026-10-16T10:00:00Z', workflowSnapshot: def,
  interactiveActive: false, interactiveNodeId: null,
} as unknown as Instance

const live = (instanceId: string, nodeId: string, state: ExecutionContext['nodes'][number]['state']) => ({ instanceId, nodeId, state })
const task = (key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask => ({
  key, kind: 'branch', origin: key === 'a' ? 'a1' : 'b1', childInstanceId: `br-${key}`, liveStatus: 'running', frozenStatus: '',
  blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
})
const panel = (over: Partial<GroupPanel> = {}): GroupPanel => ({
  groupRunId: 'run-g', instanceId: 'root', nodeId: 'g', form: 'branches', completionRule: 'all_succeed',
  unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running', tasks: [task('a'), task('b')],
  progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 }, waitingReasons: ['Waiting for tasks a, b'],
  ...over,
})
function aggregate(over: Partial<GraphState> = {}): GraphState {
  return {
    rootInstanceId: 'root', version: 'v1', active: true, layout, warnings: [],
    contexts: [
      { instanceId: 'root', kind: 'root', state: 'waiting', blocking: true, redacted: false, nodes: [live('root', 'start', 'completed'), live('root', 'g', 'waiting')] },
      { instanceId: 'br-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] },
      { instanceId: 'br-b', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-b', 'b1', 'running')] },
    ],
    groups: [panel()],
    ...over,
  }
}

// ---------------------------------------------------------------------------
// API fake keyed by (instance id, node id): a read against the wrong instance
// is a 404, so a selection that traced the ROOT for a branch node cannot pass.
// ---------------------------------------------------------------------------
let currentAggregate: GraphState
const exec = (instanceId: string, nodeId: string, output: Record<string, unknown>): NodeExecution => ({
  id: `${instanceId}-${nodeId}`, instanceId, nodeId, attempt: 1, status: 'succeeded', output, startedAt: '2026-10-16T10:00:00Z',
})
const notFound = () => Promise.reject(new Error('API 404: instance not found'))
const apiMock = vi.hoisted(() => ({
  instances: {
    get: vi.fn(),
    getTrace: vi.fn(),
    getVariables: vi.fn(),
    getNodeTrace: vi.fn(),
    graphState: vi.fn(),
    pause: vi.fn(), resume: vi.fn(), cancel: vi.fn(), retry: vi.fn(), rerun: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

// WebSocket stub recording every URL the view opens.
const sockets: Array<{ url: string; emit: (e: WebSocketEvent) => void }> = []
class StubSocket {
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

import ExecutionView from './ExecutionView'

beforeEach(() => {
  sockets.length = 0
  vi.stubGlobal('WebSocket', StubSocket)
  currentAggregate = aggregate()
  apiMock.instances.get.mockImplementation((id: string) => (id === 'root' ? Promise.resolve(rootInstance) : notFound()))
  apiMock.instances.getTrace.mockImplementation((id: string) => (id === 'root' ? Promise.resolve([exec('root', 'start', {})]) : notFound()))
  apiMock.instances.getVariables.mockImplementation((id: string) =>
    ({ root: Promise.resolve({ owner: 'root' }), 'br-b': Promise.resolve({ owner: 'branch-b' }) } as Record<string, Promise<unknown>>)[id] ?? notFound(),
  )
  apiMock.instances.getNodeTrace.mockImplementation((id: string, nodeId: string) =>
    id === 'br-b' && nodeId === 'b1' ? Promise.resolve(exec('br-b', 'b1', { answer: 'from-branch-b' })) : notFound(),
  )
  apiMock.instances.graphState.mockImplementation((id: string) => (id === 'root' ? Promise.resolve(currentAggregate) : notFound()))
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function renderView() {
  return render(
    <MemoryRouter initialEntries={['/instances/root']}>
      <Routes>
        <Route path="/instances/:id" element={<ExecutionView />} />
      </Routes>
    </MemoryRouter>,
  )
}

const stateOf = (id: string) => screen.getByTestId(`node-${id}`).getAttribute('data-state')

describe('ExecutionView — aggregate graph state (happy path)', () => {
  // Provenance: the root's own trace/cursor knows nothing of the branches, so
  // only the aggregate can mark a2 AND b1 running at the same time.
  it('highlights the running nodes of BOTH concurrent branches', async () => {
    renderView()
    await waitFor(() => expect(stateOf('a2')).toBe('running'))
    expect(stateOf('b1')).toBe('running')
    expect(stateOf('a1')).toBe('completed')
    expect(stateOf('start')).toBe('completed')
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('root')
  })

  it('renders the group as header + footer instead of a plain node, and subscribes to the subtree', async () => {
    renderView()
    await screen.findByTestId('node-group:g:header')
    expect(screen.getByTestId('node-group:g:footer').getAttribute('data-type')).toBe('groupFooter')
    expect(screen.queryByTestId('node-g')).toBeNull()
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))).toBe(true)
  })
})

describe('ExecutionView — instance-qualified selection (success)', () => {
  it('selecting a branch-body node opens the OWNING branch instance\'s trace and variables', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-b1'))
    await waitFor(() => expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('br-b', 'b1'))
    expect(apiMock.instances.getNodeTrace).not.toHaveBeenCalledWith('root', 'b1')
    expect(apiMock.instances.getVariables).toHaveBeenCalledWith('br-b')
    expect(await screen.findByTestId('trace-owner')).toHaveTextContent('Trace from branch task b · br-b')
    expect(screen.getByTestId('node-b1').getAttribute('data-selected')).toBe('true')
  })

  it('header and footer both open the same group panel', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:header'))
    const fromHeader = await screen.findByTestId('group-panel')
    expect(within(fromHeader).getByText('Fan out')).toBeInTheDocument()
    expect(within(fromHeader).getByText('All succeed')).toBeInTheDocument()
    expect(within(fromHeader).getByText('Waiting for tasks a, b')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('node-group:g:footer'))
    expect(within(await screen.findByTestId('group-panel')).getByText('All succeed')).toBeInTheDocument()
    expect(screen.getByTestId('node-group:g:footer').getAttribute('data-selected')).toBe('true')
  })

  it('shows the winner, and a frozen unfinished task that is live completed', async () => {
    currentAggregate = aggregate({
      active: true,
      groups: [panel({
        completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'b', waitingReasons: [],
        tasks: [
          task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
          task('b', { liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        ],
        progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    const p = await screen.findByTestId('group-panel')
    expect(within(p).getByText('Winner:')).toBeInTheDocument()
    const rowA = within(p).getByTestId('group-task-row-a')
    expect(within(rowA).getByText('completed')).toBeInTheDocument()
    expect(within(rowA).getByText('unfinished')).toBeInTheDocument()
  })
})

describe('ExecutionView — waiting, sessions and redaction', () => {
  it('waits on resolution with zero outstanding tasks and opens the parent\'s session at the group node', async () => {
    currentAggregate = aggregate({
      groups: [panel({
        gatherMode: 'results_and_files', state: 'resolving_conflicts',
        tasks: [task('a', { liveStatus: 'completed', blocking: false }), task('b', { liveStatus: 'completed', blocking: false })],
        progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
        waitingReasons: ['Resolving merge conflicts for task b (attempt 1)'],
        gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 1, attempt: 1, instanceId: 'root', nodeId: 'g' } },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    const p = await screen.findByTestId('group-panel')
    expect(within(p).getByText('0 outstanding blocking tasks')).toBeInTheDocument()
    expect(within(p).getByText('Resolving merge conflicts for task b (attempt 1)')).toBeInTheDocument()

    fireEvent.click(within(p).getByRole('button', { name: 'Open resolution session' }))

    expect(await screen.findByText('Interactive Session')).toBeInTheDocument()
    expect(screen.getByText('Fan out')).toBeInTheDocument()
  })

  it('a nested group\'s session routes through ITS parent instance stream, not the route instance', async () => {
    currentAggregate = aggregate({
      groups: [panel({
        gatherMode: 'results_and_files', state: 'resolving_conflicts', waitingReasons: ['Resolving merge conflicts'],
        gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 0, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 0, attempt: 1, instanceId: 'br-b', nodeId: 'g' } },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    fireEvent.click(await screen.findByRole('button', { name: 'Open resolution session' }))
    expect(await screen.findByText('Conflict resolution · Fan out')).toBeInTheDocument()
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/br-b/ws'))).toBe(true)
  })

  it('shows a redacted branch as a hidden dependency in the graph and the panel', async () => {
    currentAggregate = aggregate({
      contexts: aggregate().contexts.slice(0, 2),
      groups: [panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] })],
    })
    renderView()
    await waitFor(() => expect(stateOf('b1')).toBe('unknown'))
    fireEvent.click(screen.getByTestId('node-group:g:header'))
    const row = within(await screen.findByTestId('group-panel')).getByTestId('group-task-row-b')
    expect(within(row).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(row).queryByRole('link')).toBeNull()
  })
})

describe('ExecutionView — refresh (error recovery)', () => {
  // The old handler listened for node.skipped / variables.updated /
  // instance.state_changed, which are never emitted. Now any aggregate version
  // change (event, poll or reconnect) refreshes the route instance's reads.
  it('refreshes the route instance when a subtree event changes the aggregate version', async () => {
    renderView()
    await waitFor(() => expect(stateOf('b1')).toBe('running'))
    expect(apiMock.instances.get).toHaveBeenCalledTimes(1)

    currentAggregate = aggregate({ version: 'v2' })
    const subtree = sockets.find((s) => s.url.endsWith('?subtree=1'))!
    act(() => subtree.emit({ type: 'group.task_updated', timestamp: '2026-10-16T10:01:00Z', data: {}, sourceInstanceId: 'br-b' }))

    await waitFor(() => expect(apiMock.instances.get).toHaveBeenCalledTimes(2))
  })

  it('falls back to the single-instance trace view when the aggregate is unavailable', async () => {
    apiMock.instances.graphState.mockImplementation(() => Promise.reject(new Error('API 500: boom')))
    renderView()
    await waitFor(() => expect(stateOf('g')).toBe('waiting'))
    expect(stateOf('start')).toBe('completed')
    expect(screen.queryByTestId('node-group:g:header')).toBeNull()
  })
})
```

- [x] Step 22: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/routes/ExecutionView.groups.test.tsx
```
Expected (the pre-task view only knows the root cursor and trace):
```
 FAIL  src/routes/ExecutionView.groups.test.tsx > ExecutionView — aggregate graph state (happy path) > highlights the running nodes of BOTH concurrent branches
AssertionError: expected 'pending' to be 'running' // Object.is equality
 FAIL  src/routes/ExecutionView.groups.test.tsx > ExecutionView — aggregate graph state (happy path) > renders the group as header + footer instead of a plain node, and subscribes to the subtree
TestingLibraryElementError: Unable to find an element by: [data-testid="node-group:g:header"]
 FAIL  src/routes/ExecutionView.groups.test.tsx > ExecutionView — instance-qualified selection (success) > selecting a branch-body node opens the OWNING branch instance's trace and variables
AssertionError: expected "vi.fn()" to be called with arguments: [ 'br-b', 'b1' ]
...
      Tests  9 failed | 1 passed (10)
```
(The passing one is the legacy-fallback test, a regression guard.)

- [x] Step 23: Implement. Replace the whole of `dashboard/src/routes/ExecutionView.tsx` (lines 1-393) with the version below. Relative to the current file: `GraphView` gains `graphState`/`onSelect` (aggregate path via `buildAggregateView`, legacy `buildExecutionStates` kept as the fallback), computes ALL active step nodes and passes their bounds to `useFollowActiveNode` (and frames them on mount); `ExecutionView` mounts `useGraphState(id)`, DELETES `handleWsEvent` (lines 241-282, including the `node.skipped`, `variables.updated` and `instance.state_changed` cases) together with its `useInstanceWebSocket` (line 284) and `useRefetchOnReconnect` (line 290) — the aggregate hook owns the subtree socket, polling and reconnect resync, and a version change refreshes the route instance's reads — replaces `handleNodeClick` with the instance-qualified `handleSelect`, and adds the group-panel and resolution-session branches of the right-hand panel:

```tsx
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider, ReactFlow, Background, Controls, useReactFlow } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { GraphState, Instance, NodeExecution, ResolutionSessionRef } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from '../graph/types'
import { api } from '../api/client'
import { useGraphLayout, NODE_WIDTH, NODE_HEIGHT } from '../graph/useGraphLayout'
import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'
import { DEFAULT_ZOOM, FOLLOW_BOUNDS_PADDING } from '../graph/constants'
import { useFollowActiveNode, type FollowBounds } from '../graph/useFollowActiveNode'
import { nodeTypes } from '../graph/nodes/nodeTypes'
import { edgeTypes } from '../graph/edges/edgeTypes'
import { groupFooterId, groupHeaderId } from '../graph/groupLayout'
import {
  buildAggregateView,
  findGroupPanel,
  selectionFromNode,
  type GraphSelection,
} from '../graph/aggregateState'
import { useGraphState } from '../hooks/useGraphState'
import { useInteractiveSession } from '../hooks/useInteractiveSession'
import CalloutConnector from '../graph/CalloutConnector'
import StateBadge from '../components/StateBadge'
import InstanceControls from '../components/InstanceControls'
import ResizablePanel from '../components/ResizablePanel'
import SidePanel from '../components/SidePanel'
import InteractivePanel from '../components/InteractivePanel'
import GroupPanel from '../components/GroupPanel'
import styles from './ExecutionView.module.css'

/**
 * Map trace data + instance state onto per-node execution states for the graph.
 * Legacy single-instance path: used until (or when) the aggregate graph state
 * is unavailable. With an aggregate, GraphView uses buildAggregateView instead,
 * keyed `${instanceId}:${nodeId}`.
 */
function buildExecutionStates(
  instance: Instance | null,
  trace: NodeExecution[],
  workflowDef: WorkflowDefinition | null,
): Record<string, NodeExecutionState> {
  const states: Record<string, NodeExecutionState> = {}

  if (!workflowDef) return states

  // Initialize all nodes as pending.
  for (const node of workflowDef.nodes) {
    states[node.id] = 'pending'
  }

  // Apply trace results.
  for (const exec of trace) {
    switch (exec.status) {
      case 'succeeded':
        states[exec.nodeId] = 'completed'
        break
      case 'failed':
        states[exec.nodeId] = 'failed'
        break
      case 'skipped':
        states[exec.nodeId] = 'skipped'
        break
    }
  }

  // Mark the current node as running/waiting if the instance is active.
  if (instance?.currentNodeId) {
    if (instance.state === 'running') {
      states[instance.currentNodeId] = 'running'
    } else if (instance.state === 'waiting') {
      states[instance.currentNodeId] = 'waiting'
    }
  }

  return states
}

const ACTIVE_STATES = new Set<NodeExecutionState>(['running', 'waiting'])

export function GraphView({
  instance,
  workflowDef,
  trace,
  onNodeClick,
  selectedNodeId,
  graphState = null,
  onSelect,
}: {
  instance: Instance | null
  workflowDef: WorkflowDefinition | null
  trace: NodeExecution[]
  /** Legacy click callback (definition node id). Ignored when onSelect is given. */
  onNodeClick?: (nodeId: string) => void
  /** Rendered (React Flow) id of the selected node, header, footer or card. */
  selectedNodeId: string | null
  /** Authorized aggregate of this run and its descendants (P4-T6 useGraphState). */
  graphState?: GraphState | null
  /** Instance-qualified selection: a step node with its owning instance, or a group. */
  onSelect?: (selection: GraphSelection) => void
}) {
  // Aggregate view (P4-T8): node states keyed `${instanceId}:${nodeId}`,
  // projected onto the rendered graph through Phase 1 ownership. Memoized on
  // the GraphState object, which useGraphState only replaces when its version
  // changes, so the layout does not recompute on unrelated renders.
  const aggregate = useMemo(
    () => (graphState && workflowDef ? buildAggregateView(graphState, workflowDef) : null),
    [graphState, workflowDef],
  )
  // Memoize so identity only changes when the inputs actually change. Without
  // this, every re-render (e.g. each live WebSocket event) produces a fresh
  // executionStates object, defeating useGraphLayout's memo — dagre re-runs and
  // ReactFlow re-renders every node, which reads as a per-event "flicker".
  const legacyStates = useMemo(
    () => buildExecutionStates(instance, trace, workflowDef),
    [instance, trace, workflowDef],
  )
  const executionStates = aggregate ? aggregate.executionStates : legacyStates
  const groups = aggregate ? graphState?.layout ?? null : null
  const { rfNodes, rfEdges } = useGraphLayout({
    nodes: workflowDef?.nodes ?? [],
    edges: workflowDef?.edges ?? [],
    executionStates,
    groups,
    groupLive: aggregate?.groupLive,
    nodeContexts: aggregate?.nodeContexts,
  })

  // Mark selected node. Memoized on the stable rfNodes so the array reference
  // only changes when the layout or selection changes — not on every render.
  const nodesWithSelection = useMemo(
    () => rfNodes.map((n) => ({ ...n, selected: n.id === selectedNodeId })),
    [rfNodes, selectedNodeId],
  )

  const reactFlow = useReactFlow()

  // EVERY concurrently active step node (all branches, nested tasks and
  // independent work), not just the root cursor.
  const activeIds = useMemo(
    () =>
      rfNodes
        .filter((n) => n.type === 'arbitronNode' && ACTIVE_STATES.has((n.data as { executionState: NodeExecutionState }).executionState))
        .map((n) => n.id),
    [rfNodes],
  )

  // With several active nodes the viewport frames all of them (see
  // useFollowActiveNode); following one node would hide the other paths.
  const activeBounds = useMemo<FollowBounds | null>(() => {
    if (activeIds.length < 2) return null
    const rects = rfNodes.filter((n) => activeIds.includes(n.id))
    const minX = Math.min(...rects.map((n) => n.position.x))
    const minY = Math.min(...rects.map((n) => n.position.y))
    const maxX = Math.max(...rects.map((n) => n.position.x + NODE_WIDTH))
    const maxY = Math.max(...rects.map((n) => n.position.y + NODE_HEIGHT))
    return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  }, [activeIds, rfNodes])

  // The real step node the viewport should center on for this instance state.
  // A group node never renders itself: its header stands in for it.
  const anchorId = useMemo(() => {
    if (aggregate && activeIds.length === 1) return activeIds[0]
    const id = selectAnchorNodeId(instance, workflowDef, executionStates, trace)
    if (id && groups?.groups[id]) return groupHeaderId(id)
    return id
  }, [aggregate, activeIds, instance, workflowDef, executionStates, trace, groups])

  // Resolve the anchor's node-center coordinates from the laid-out rfNodes.
  // Loop-container overlay nodes are excluded automatically: anchorId only
  // ever references a real workflowDef node id (or a group header).
  const anchorPos = useMemo(() => {
    if (activeBounds) {
      return { x: activeBounds.x + activeBounds.width / 2, y: activeBounds.y + activeBounds.height / 2 }
    }
    if (!anchorId) return null
    const node = rfNodes.find((n) => n.id === anchorId)
    if (!node) return null
    return {
      x: node.position.x + NODE_WIDTH / 2,
      y: node.position.y + NODE_HEIGHT / 2,
    }
  }, [activeBounds, anchorId, rfNodes])

  // Center once, on first mount, at the readable default zoom (or frame every
  // active node when several run concurrently). Guarded by a ref so live
  // re-renders do not re-center and fight the user. Follow-on-advance behavior
  // is added via useFollowActiveNode; this effect is the one-shot mount center.
  const didInitialCenter = useRef(false)
  useEffect(() => {
    if (didInitialCenter.current) return
    if (!anchorPos) return
    if (activeBounds) {
      void reactFlow.fitBounds(activeBounds, { padding: FOLLOW_BOUNDS_PADDING })
    } else {
      reactFlow.setCenter(anchorPos.x, anchorPos.y, { zoom: DEFAULT_ZOOM })
    }
    didInitialCenter.current = true
  }, [anchorPos, activeBounds, reactFlow])

  const isRunning = aggregate
    ? graphState?.active === true
    : instance?.state === 'running' || instance?.state === 'waiting'
  const { following, onMoveStart, recenter } = useFollowActiveNode(
    anchorPos,
    isRunning,
    activeBounds,
  )

  const fallbackInstanceId = instance?.id ?? graphState?.rootInstanceId ?? null

  return (
    <div className={styles.graphContainer}>
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodeClick={(_event, node) => {
          if (onSelect) onSelect(selectionFromNode(node, fallbackInstanceId))
          else onNodeClick?.(node.id)
        }}
        onMoveStart={(event) => onMoveStart(event)}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#2a2a4a" gap={20} size={1} />
        <Controls showInteractive={false} />
        <button
          type="button"
          className={`${styles.recenterButton} ${following ? styles.recenterButtonActive : ''}`}
          aria-pressed={following}
          title={following ? 'Following the active steps' : 'Recenter and follow the active steps'}
          onClick={recenter}
        >
          {following ? 'Following' : 'Recenter'}
        </button>
      </ReactFlow>
    </div>
  )
}

const priorityClass: Record<string, string> = {
  high: styles.priorityHigh,
  normal: styles.priorityNormal,
  low: styles.priorityLow,
}

export default function ExecutionView() {
  const { id } = useParams<{ id: string }>()
  const [instance, setInstance] = useState<Instance | null>(null)
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(null)
  const [trace, setTrace] = useState<NodeExecution[]>([])
  const [variables, setVariables] = useState<Record<string, unknown> | null>(null)
  const [selection, setSelection] = useState<GraphSelection | null>(null)
  const [selectedExec, setSelectedExec] = useState<NodeExecution | null>(null)
  const [ownerVariables, setOwnerVariables] = useState<Record<string, unknown> | null>(null)
  const [sessionTarget, setSessionTarget] = useState<ResolutionSessionRef | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Aggregate state of this run and every visible descendant: subtree events,
  // bounded polling while active, reconnect resync (P4-T6).
  const graph = useGraphState(id)

  // Interactive session state.
  const interactiveSession = useInteractiveSession(id)

  // A conflict-resolution session belongs to the GROUP'S parent instance at the
  // group node; for a nested group that is a branch instance, not this route's
  // instance. Its controls route through that instance's own stream.
  const linkedSessionId = sessionTarget && sessionTarget.instanceId !== id ? sessionTarget.instanceId : undefined
  const linkedSession = useInteractiveSession(linkedSessionId)

  // Pre-warm session state from the REST instance data so the interactive panel
  // renders immediately on page load, before interactive.history arrives over WebSocket.
  useEffect(() => {
    if (instance?.interactiveActive && instance.interactiveNodeId) {
      interactiveSession.seedFromInstance(instance.interactiveNodeId)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seedFromInstance is
    // a stable useCallback with no deps; including interactiveSession would cause
    // an infinite re-render loop since every state update creates a new object.
  }, [instance?.interactiveActive, instance?.interactiveNodeId])

  // Opening a resolution session from a group panel seeds the owning session.
  useEffect(() => {
    if (!sessionTarget) return
    if (sessionTarget.instanceId === id) interactiveSession.seedFromInstance(sessionTarget.nodeId)
    else linkedSession.seedFromInstance(sessionTarget.nodeId)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seedFromInstance is stable (see above).
  }, [sessionTarget?.instanceId, sessionTarget?.nodeId, id])

  const labelOf = useCallback(
    (nodeId: string) => workflowDef?.nodes.find((n) => n.id === nodeId)?.label || nodeId,
    [workflowDef],
  )

  // Resolve the interactive session's nodeId to a human-readable label.
  const interactiveNodeName = useMemo(() => {
    if (!interactiveSession.nodeId || !workflowDef) return 'Unknown Node'
    return labelOf(interactiveSession.nodeId)
  }, [interactiveSession.nodeId, workflowDef, labelOf])

  // Fetch the route instance's own reads (header, controls, variables).
  const fetchData = useCallback(async () => {
    if (!id) return
    try {
      const [inst, traceData, vars] = await Promise.all([
        api.instances.get(id),
        api.instances.getTrace(id),
        api.instances.getVariables(id),
      ])
      setInstance(inst)
      setTrace(traceData)
      setVariables(vars)

      if (inst.workflowSnapshot) {
        setWorkflowDef(inst.workflowSnapshot as unknown as WorkflowDefinition)
      }

      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load instance')
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  // The route instance's reads follow the aggregate: whenever its version
  // changes (a subtree event, a poll, or a reconnect resync inside
  // useGraphState) refresh them. This replaces the former per-event handler,
  // which also listened for three event types the engine never emits
  // (a skipped-node, a variables-updated and a state-changed event).
  const version = graph.state?.version
  const seenVersion = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (!version) return
    if (seenVersion.current === undefined) {
      seenVersion.current = version // the first aggregate lands alongside the initial fetch
      return
    }
    if (seenVersion.current === version) return
    seenVersion.current = version
    void fetchData()
  }, [version, fetchData])

  const refreshAll = useCallback(() => {
    void fetchData()
    void graph.refetch()
  }, [fetchData, graph])

  // Instance-qualified selection: a branch-body node opens ITS branch
  // instance's trace (and variables); header/footer/task card open the group.
  const selectSeq = useRef(0)
  const handleSelect = useCallback(
    async (sel: GraphSelection) => {
      const seq = ++selectSeq.current
      setSelection(sel)
      setSessionTarget(null)
      setSelectedExec(null)
      setOwnerVariables(null)
      if (sel.kind !== 'node' || !sel.instanceId) return
      const owner = sel.instanceId
      const [exec, vars] = await Promise.all([
        api.instances.getNodeTrace(owner, sel.nodeId).catch(() => null),
        owner === id ? Promise.resolve(null) : api.instances.getVariables(owner).catch(() => null),
      ])
      if (seq !== selectSeq.current) return // a newer selection won
      setSelectedExec(exec)
      setOwnerVariables(vars)
    },
    [id],
  )

  if (loading) {
    return <div className={styles.loading}>Loading instance...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!instance) {
    return <div className={styles.error}>Instance not found</div>
  }

  const shortId = instance.id.slice(0, 8)
  const connected = graph.connected
  const layoutGroups = graph.state?.layout?.groups ?? {}
  const selectedOwner = selection?.kind === 'node' ? selection.instanceId : null
  const ownerContext = selectedOwner && selectedOwner !== id
    ? graph.state?.contexts.find((c) => c.instanceId === selectedOwner) ?? null
    : null
  const groupPanel = selection?.kind === 'group'
    ? findGroupPanel(graph.state, selection.instanceId, selection.nodeId)
    : null
  const linkedActive = Boolean(linkedSessionId) && linkedSession.isActive
  // The resolution session sits at the group node, which renders as its footer.
  const calloutNodeId = interactiveSession.nodeId && layoutGroups[interactiveSession.nodeId]
    ? groupFooterId(interactiveSession.nodeId)
    : interactiveSession.nodeId

  let right
  if (interactiveSession.isActive) {
    right = <InteractivePanel session={interactiveSession} nodeName={interactiveNodeName} />
  } else if (linkedActive && sessionTarget) {
    right = <InteractivePanel session={linkedSession} nodeName={`Conflict resolution · ${labelOf(sessionTarget.nodeId)}`} />
  } else if (selection?.kind === 'group') {
    right = <GroupPanel panel={groupPanel} label={labelOf(selection.nodeId)} onOpenSession={setSessionTarget} />
  } else {
    right = (
      <div className={styles.sideStack}>
        {ownerContext && (
          <div className={styles.ownerBanner} data-testid="trace-owner">
            {ownerContext.redacted
              ? 'Trace hidden (hidden dependency)'
              : `Trace from ${ownerContext.kind}${ownerContext.taskKey ? ` task ${ownerContext.taskKey}` : ''} · ${ownerContext.instanceId.slice(0, 8)}`}
          </div>
        )}
        {selection?.kind === 'node' && !selection.instanceId && (
          <div className={styles.ownerBanner} data-testid="trace-owner">Not started in this run yet</div>
        )}
        <SidePanel
          execution={selectedExec}
          variables={ownerContext ? ownerVariables : variables}
          selectedNodeId={selection?.kind === 'node' ? selection.nodeId : null}
          workflowDef={workflowDef}
        />
      </div>
    )
  }

  return (
    <ReactFlowProvider>
      <div className={styles.page}>
        {/* Breadcrumb bar */}
        <div className={styles.breadcrumb}>
          <Link to="/" className={styles.breadcrumbLink}>
            Queue
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>
            {instance.workflowName}
          </span>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.instanceId}>{shortId}</span>
          <StateBadge state={instance.state} />
          <span
            className={`${styles.priorityBadge} ${priorityClass[instance.priority] ?? ''}`}
          >
            {instance.priority}
          </span>
          <div
            className={`${styles.connIndicator} ${connected ? styles.connConnected : styles.connDisconnected}`}
            title={connected ? 'WebSocket connected' : 'WebSocket disconnected'}
          />
          <div className={styles.spacer} />
          <InstanceControls
            instanceId={instance.id}
            state={instance.state}
            onAction={refreshAll}
          />
        </div>

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <GraphView
              instance={instance}
              workflowDef={workflowDef}
              trace={trace}
              graphState={graph.state}
              onSelect={handleSelect}
              selectedNodeId={selection?.rfId ?? null}
            />
          }
          right={right}
          rightWidthFraction={interactiveSession.isActive || linkedActive ? 0.6 : undefined}
        />

        {/* Connector line from interactive node to panel border */}
        {calloutNodeId && (
          <CalloutConnector
            nodeId={calloutNodeId}
            visible={interactiveSession.isActive}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}
```

Append to `dashboard/src/routes/ExecutionView.module.css` (after line 133):

```css
.sideStack {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.sideStack > :last-child {
  flex: 1;
  min-height: 0;
}

.ownerBanner {
  flex-shrink: 0;
  padding: 6px 12px;
  font-size: 11px;
  color: var(--text-muted);
  background: var(--bg-secondary);
  border-left: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
}
```

- [x] Step 24: Run the view tests, the existing GraphView suites, the typecheck and the full suite.

Cycle 5: confirmed the planned 9 failing / 1 passing view tests against the previous route. All view/legacy tests then passed. Final focused gate: 7 files / 99 tests; full dashboard: 60 files / 673 tests; TypeScript clean. `make ui` rebuilt and synchronized the embedded SPA. Non-blocking build notices: npm reports 9 dependency vulnerabilities (lockfile unchanged), and Vite reports the existing >500 kB chunk-size warning.

Self-review (required order):
1. **Spec compliance:** compared the route, projection, panel and follow behavior to the task/design. Red→green regressions closed two omissions in the supplied implementation: active workflow task cards now participate in viewport bounds (including their 96px height), and selecting a group/linked session takes precedence over an unrelated active root conversation. Kept existing typed node accents and the legacy trace fallback; no new flags/options or next-task features.
2. **Anti-green-theater:** the real `useGraphState` and layout feed rendered state, panel and viewport sinks. Strengthened owner-trace coverage to assert the actual branch output and Variables tab, and session coverage to assert received conversation text plus an abandon command sent ONLY on the selected owner's socket. Temporarily replaced production `graphState={graph.state}` with `null`: all 3 selected branch-state, owning-trace and workflow-card sink probes failed. Restored the wiring; no mutation remains.
3. **Code quality:** reviewed neighboring hooks, panels and layout metrics. Used cycle detection instead of magic traversal cutoffs. A red→green session-switch regression exposed retained conversation state when reusing a hook across owners; the linked session now has an owner/run/merge/attempt-keyed component lifetime and returns to the group panel on end. Adjusted callout/panel sizing to the actually visible session. Reran all focused tests, typecheck and the full dashboard suite after these fixes.

```bash
cd dashboard && npx vitest run src/routes/ExecutionView.groups.test.tsx src/routes/GraphView.test.tsx src/routes/GraphView.loops.test.tsx && npx tsc --noEmit && npx vitest run
```
Expected:
```
 Test Files  3 passed (3)
...
 Test Files  … passed (no failures)
```
(`GraphView.test.tsx` passes `onNodeClick` and no `graphState`; that legacy path is unchanged.)

- [x] Step 25: Confirm the never-emitted events are gone from the dashboard source:

```bash
rg -n "node\.skipped|variables\.updated|instance\.state_changed" dashboard/src --glob '!*.test.*'
```
Expected: no output.

- [x] Step 26: Commit.

The five planned cycle commits include their checked steps and TDD evidence; the final cycle commit also includes rebuilt embedded assets. Post-build verification again passed TypeScript and all 673 dashboard tests; `go test ./adapter/dashboard -count=1` and `git diff --check` are clean. No task steps are blocked.

```bash
git add dashboard/src/routes/ExecutionView.tsx dashboard/src/routes/ExecutionView.module.css dashboard/src/routes/ExecutionView.groups.test.tsx
git commit -m "feat(dashboard): run view uses the aggregate graph state, instance-qualified selection and group panels" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
