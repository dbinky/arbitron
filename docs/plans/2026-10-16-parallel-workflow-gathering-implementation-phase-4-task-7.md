# Parallel Workflow Gathering — Phase 4 Task 7: Group layout, `GroupContainerNode` and `useGraphLayout` integration

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Render every parallel group as a nested container box with a fan-out header and a gather footer (synthetic presentation ids mapped back to the group's definition node), ordered task cards for the workflows form, and presentation-only sink connectors from each branch body into its gather — alongside, and without disturbing, the existing loop clusters.
**Architecture:** A pure planner, `graph/groupLayout.ts`, derives presentation nodes/edges, dagre compound clusters and box membership ONLY from the Phase 1 `GroupGraph` (owner/bodies/nested) plus the manifest edges. `useGraphLayout` gains three optional inputs (`groups`, `groupLive`, `nodeContexts`); with no drawn group its output is byte-for-byte the old output. New React Flow node types (`groupContainer`, `groupHeader`, `groupFooter`, `groupTaskCard`) live in `graph/nodes/GroupContainerNode.tsx`.
**Tech Stack:** React 19, `@xyflow/react` 12, `dagre` 0.8 (compound graphs), TypeScript 6, Vitest 4 + @testing-library/react 16, CSS modules.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("The workflow UI clearly displays fan-out paths, gathers, nested groups, and child workflow relationships"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-4.md` §4 "Graph rendering and individual run view" (Phase 1 ownership, nested `groupContainer` boxes alongside loop clusters, header/footer synthetic ids, task edges leave the header, success/failure leave the footer, presentation-only sink connectors, workflow-form ordered task cards) and Data Provenance row "Layout metrics and loop clusters".

**Interfaces**
- Consumes (existing code):
  - `dashboard/src/graph/useGraphLayout.ts:35-190` `useGraphLayout({ nodes, edges, executionStates? }): { rfNodes, rfEdges, needsAutoSave }` (dagre compound graph, loop clusters `cluster-${loop.id}`, `loop-container-*` overlay nodes at `zIndex -10 + depth`, edge ids `e-${source}-${target}-${i}`).
  - `dashboard/src/graph/loops.ts:108` `detectLoops(edges)`, `dashboard/src/graph/loopLayout.ts:26` `loopContainerBox`, `:16` `loopContainerLabel` (CONTAINER_PAD 24), `dashboard/src/graph/loopEdgeRouting.ts` `chooseLoopSide`.
  - `dashboard/src/graph/nodes/nodeTypes.ts:1-7` `nodeTypes`.
  - `dashboard/src/graph/types.ts:3-18` `WorkflowNode`, `WorkflowEdge`.
- Consumes (earlier tasks): P4-T6 `dashboard/src/api/types.ts` `GroupGraph`, `GroupScope`, `GroupProgress` (mirrors of P1-T2 `workflow.GroupGraph` and P4-T2 `engine.GroupProgress`).
- Produces (exact exported symbols later tasks rely on):
  - `dashboard/src/graph/groupLayout.ts`: `groupHeaderId(groupNodeId: string): string` (= `group:<id>:header`), `groupFooterId` (= `group:<id>:footer`), `groupBoxId` (= `group:<id>:box`), `groupTaskCardId(groupNodeId, taskKey)` (= `group:<id>:task:<key>`); `interface GroupLiveTask`, `interface GroupLiveSummary`, `interface NodeContextInfo`, `interface GroupEndpointData`, `interface GroupTaskCardData`, `interface GroupContainerData`; `immediateMembers(graph, nodes, groupNodeId, taskKey): string[]`; `bodySinks(graph, nodes, edges, groupNodeId, taskKey): string[]`; `planGroupLayout(nodes, edges, graph): GroupLayoutPlan`; `toDefinitionPositions(rfNodes: Node[]): Record<string, {x,y}>`; `definitionNodeIdOf(n: Pick<Node,'id'|'data'>): string`; constants `GROUP_PAD`, `GROUP_HEADER_HEIGHT`, `GROUP_FOOTER_HEIGHT`, `TASK_CARD_HEIGHT`.
  - `dashboard/src/graph/groupText.ts`: `ruleText`, `unfinishedText`, `gatherText`, `progressText(p: GroupProgress)`, `stateText`.
  - `dashboard/src/graph/useGraphLayout.ts`: input gains `groups?: GroupGraph | null`, `groupLive?: Record<string, GroupLiveSummary>` (keyed by group node id), `nodeContexts?: Record<string, NodeContextInfo>` (keyed by definition node id; stamped onto node data as `instanceId`, `independent`, `hiddenDependency`).
  - `dashboard/src/graph/nodes/GroupContainerNode.tsx`: default `GroupContainerNode`; named `GroupHeaderNode`, `GroupFooterNode`, `GroupTaskCardNode`; registered in `nodeTypes` as `groupContainer`, `groupHeader`, `groupFooter`, `groupTaskCard`.
  - `dashboard/src/graph/groupLayout.fixtures.ts`: `node`, `scope`, `branchNodes/branchEdges/branchGraph` (two-level branches nesting), `mixedNodes/mixedEdges/mixedGraph` (workflows group nested in a branches group), `EMPTY_GRAPH` — reused by P4-T8/P4-T9 tests.
  - Rendered-graph contract later tasks rely on: a group node never renders as an `arbitronNode`; its header/footer/cards carry `data.definitionNodeId`; presentation edges carry `data.presentationOnly === true` and `data.kind` (`sink` | `fanout` | `gather`); manifest edge ids are unchanged; with no drawn group the output equals the pre-task output.

**Wiring & data provenance**
- Composition root (live UI): `useGraphLayout` is called by `dashboard/src/routes/ExecutionView.tsx:GraphView` (P4-T8 passes `graphState.layout`, `groupLive`, `nodeContexts`), by the queue live pane through the same `GraphView` (P4-T10), and by `dashboard/src/routes/WorkflowReview.tsx:ReviewGraph` (P4-T9 passes the static layout). `nodeTypes` is already handed to `<ReactFlow nodeTypes>` at `ExecutionView.tsx:148` and `WorkflowReview.tsx:99`, so registering the new types here makes them renderable in both.
- Producers:
  - `groups` (GroupGraph) ← live: `GraphState.layout` from `GET /api/v1/instances/{id}/graph-state` (`api.instances.graphState`, P4-T6) ← `domain/engine/graph_state.go:GraphStateReader.GraphState` (P4-T1) = `workflow.AnalyzeGroups` of the root `Instance.WorkflowSnapshot`; static: `GET /api/v1/workflows/{name}/group-layout` (`api.workflows.groupLayout`, P4-T6) ← `adapter/http` handler (P4-T3) ← `domain/workflow/group_ownership.go:AnalyzeGroups` (P1-T2).
  - `nodes` / `edges` ← `Instance.workflowSnapshot` (`api.instances.get`, existing) or `api.workflows.get` (existing); task edges carry `output: "task"` and `label: <task key>` as written by `domain/workflow/group_ops.go:Service.UpsertGroup` (P1-T6).
  - `groupLive` / `nodeContexts` ← built by P4-T8 (`dashboard/src/graph/aggregateState.ts`) from `GraphState.groups` / `GraphState.contexts` (P4-T2 / P4-T1).
- Provenance probes: every test in `useGraphLayout.groups.test.ts` except the two regression guards fails against the pre-task `useGraphLayout` (verified: 15 of 18 fail), i.e. they fail if the `groups` input is not consumed; `lays out sibling task bodies without overlap` fails if the task clusters are not applied to dagre (verified by removing `clusterOf` parenting).

### Task 7: Group layout, group node components and `useGraphLayout` integration
**Files:**
- Create: `dashboard/src/graph/groupLayout.ts`
- Create: `dashboard/src/graph/groupLayout.fixtures.ts`
- Create: `dashboard/src/graph/groupText.ts`
- Create: `dashboard/src/graph/nodes/GroupContainerNode.tsx`
- Create: `dashboard/src/graph/nodes/GroupContainerNode.module.css`
- Modify: `dashboard/src/graph/useGraphLayout.ts:1-190` (whole file)
- Modify: `dashboard/src/graph/nodes/nodeTypes.ts:1-7` (whole file)
- Test: `dashboard/src/graph/groupLayout.test.ts`
- Test: `dashboard/src/graph/useGraphLayout.groups.test.ts`
- Test: `dashboard/src/graph/nodes/GroupContainerNode.test.tsx`

#### Cycle 1 — pure planner (`groupLayout.ts`)

- [x] Step 1: Create the shared fixtures `dashboard/src/graph/groupLayout.fixtures.ts` (a two-level branches nesting, and a workflows-form group nested inside a branches group; ownership is the Phase 1 `GroupGraph` shape, with `owner` = IMMEDIATE owner):

```ts
/**
 * Shared group-layout fixtures (Phase 1 GroupGraph shapes) used by the layout,
 * ExecutionView and WorkflowReview tests. Not imported by application code.
 */
import type { GroupGraph, GroupScope } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'

export const node = (id: string, type = 'llm_call', position?: { x: number; y: number }): WorkflowNode => ({
  id,
  type,
  label: id.toUpperCase(),
  description: '',
  config: {},
  ...(position ? { position } : {}),
})

export function scope(over: Partial<GroupScope> & Pick<GroupScope, 'nodeId' | 'config'>): GroupScope {
  return { bodies: {}, nested: [], successTarget: '', failureTarget: '', ...over }
}

/*
 * Two-level BRANCHES nesting:
 *   start -> g1
 *   g1 task a: a1 -> a2
 *   g1 task b: g2 (nested branches group), whose success -> b2, failure -> b3
 *     g2 task x: x1
 *     g2 task y: y1 -> y2
 *   g1 success -> done, failure -> oops
 */
export const branchNodes: WorkflowNode[] = [
  node('start'),
  node('g1', 'parallel_group'),
  node('a1'),
  node('a2'),
  node('g2', 'parallel_group'),
  node('x1'),
  node('y1'),
  node('y2'),
  node('b2'),
  node('b3'),
  node('done'),
  node('oops'),
]
export const branchEdges: WorkflowEdge[] = [
  { source: 'start', target: 'g1' },
  { source: 'g1', target: 'a1', output: 'task', label: 'a' },
  { source: 'g1', target: 'g2', output: 'task', label: 'b' },
  { source: 'a1', target: 'a2' },
  { source: 'g2', target: 'x1', output: 'task', label: 'x' },
  { source: 'g2', target: 'y1', output: 'task', label: 'y' },
  { source: 'y1', target: 'y2' },
  { source: 'g2', target: 'b2', output: 'success' },
  { source: 'g2', target: 'b3', output: 'failure' },
  { source: 'g1', target: 'done', output: 'success' },
  { source: 'g1', target: 'oops', output: 'failure' },
]
export const branchGraph: GroupGraph = {
  groups: {
    g1: scope({
      nodeId: 'g1',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'g2' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
      bodies: { a: ['a1', 'a2'], b: ['g2', 'x1', 'y1', 'y2', 'b2', 'b3'] },
      nested: ['g2'],
      successTarget: 'done',
      failureTarget: 'oops',
    }),
    g2: scope({
      nodeId: 'g2',
      config: { form: 'branches', tasks: [{ key: 'x', entry: 'x1' }, { key: 'y', entry: 'y1' }], completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results' },
      parent: { groupNodeId: 'g1', taskKey: 'b' },
      bodies: { x: ['x1'], y: ['y1', 'y2'] },
      successTarget: 'b2',
      failureTarget: 'b3',
    }),
  },
  owner: {
    a1: { groupNodeId: 'g1', taskKey: 'a' },
    a2: { groupNodeId: 'g1', taskKey: 'a' },
    g2: { groupNodeId: 'g1', taskKey: 'b' },
    b2: { groupNodeId: 'g1', taskKey: 'b' },
    b3: { groupNodeId: 'g1', taskKey: 'b' },
    x1: { groupNodeId: 'g2', taskKey: 'x' },
    y1: { groupNodeId: 'g2', taskKey: 'y' },
    y2: { groupNodeId: 'g2', taskKey: 'y' },
  },
  order: ['g1', 'g2'],
}

/*
 * Two-level MIXED nesting (both forms):
 *   start -> g1 (branches)
 *   g1 task a: a1
 *   g1 task b: w1 (WORKFLOWS group: t1 child-a, t2 child-b, t3 child-a) success -> b2, failure -> b3
 *   g1 success -> done, failure -> oops
 */
export const mixedNodes: WorkflowNode[] = [
  node('start'),
  node('g1', 'parallel_group'),
  node('a1'),
  node('w1', 'parallel_group'),
  node('b2'),
  node('b3'),
  node('done'),
  node('oops'),
]
export const mixedEdges: WorkflowEdge[] = [
  { source: 'start', target: 'g1' },
  { source: 'g1', target: 'a1', output: 'task', label: 'a' },
  { source: 'g1', target: 'w1', output: 'task', label: 'b' },
  { source: 'w1', target: 'b2', output: 'success' },
  { source: 'w1', target: 'b3', output: 'failure' },
  { source: 'g1', target: 'done', output: 'success' },
  { source: 'g1', target: 'oops', output: 'failure' },
]
export const mixedGraph: GroupGraph = {
  groups: {
    g1: scope({
      nodeId: 'g1',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'w1' }], completionRule: 'all_succeed', unfinishedBehavior: 'wait_for_remaining', gatherMode: 'results' },
      bodies: { a: ['a1'], b: ['w1', 'b2', 'b3'] },
      nested: ['w1'],
      successTarget: 'done',
      failureTarget: 'oops',
    }),
    w1: scope({
      nodeId: 'w1',
      config: {
        form: 'workflows',
        tasks: [{ key: 't1', workflow: 'child-a' }, { key: 't2', workflow: 'child-b' }, { key: 't3', workflow: 'child-a' }],
        completionRule: 'first_success',
        unfinishedBehavior: 'cancel_unfinished',
        gatherMode: 'results',
      },
      parent: { groupNodeId: 'g1', taskKey: 'b' },
      successTarget: 'b2',
      failureTarget: 'b3',
    }),
  },
  owner: {
    a1: { groupNodeId: 'g1', taskKey: 'a' },
    w1: { groupNodeId: 'g1', taskKey: 'b' },
    b2: { groupNodeId: 'g1', taskKey: 'b' },
    b3: { groupNodeId: 'g1', taskKey: 'b' },
  },
  order: ['g1', 'w1'],
}

export const EMPTY_GRAPH: GroupGraph = { groups: {}, owner: {}, order: [] }
```

Write the failing planner tests `dashboard/src/graph/groupLayout.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import type { Node } from '@xyflow/react'
import {
  bodySinks,
  definitionNodeIdOf,
  groupBoxId,
  groupContainerLabel,
  groupFooterId,
  groupHeaderId,
  groupTaskCardId,
  immediateMembers,
  planGroupLayout,
  toDefinitionPositions,
} from './groupLayout'
import { branchEdges, branchGraph, branchNodes, mixedEdges, mixedGraph, mixedNodes, node } from './groupLayout.fixtures'
import type { WorkflowEdge } from './types'

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

describe('group presentation ids', () => {
  it('derives stable synthetic ids from the definition node id', () => {
    expect(groupHeaderId('g1')).toBe('group:g1:header')
    expect(groupFooterId('g1')).toBe('group:g1:footer')
    expect(groupBoxId('g1')).toBe('group:g1:box')
    expect(groupTaskCardId('w1', 't2')).toBe('group:w1:task:t2')
  })

  it('maps a rendered node back to its definition node id', () => {
    expect(definitionNodeIdOf({ id: 'group:g1:footer', data: { definitionNodeId: 'g1' } })).toBe('g1')
    expect(definitionNodeIdOf({ id: 'a1', data: { label: 'A1' } })).toBe('a1')
  })

  it('labels a container by form and rule', () => {
    expect(groupContainerLabel(branchGraph.groups.g1)).toBe('Parallel branches · all succeed')
    expect(groupContainerLabel(mixedGraph.groups.w1)).toBe('Parallel workflows · first success')
  })
})

describe('immediateMembers / bodySinks — ownership comes from GroupGraph.owner', () => {
  it('lists only IMMEDIATE members: a nested group is a member, its body nodes are not', () => {
    expect(immediateMembers(branchGraph, branchNodes, 'g1', 'b')).toEqual(['g2', 'b2', 'b3'])
    expect(immediateMembers(branchGraph, branchNodes, 'g2', 'y')).toEqual(['y1', 'y2'])
  })

  it('finds the sinks of each body (no outgoing edge, nested groups excluded)', () => {
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g1', 'a')).toEqual(['a2'])
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g1', 'b')).toEqual(['b2', 'b3'])
    expect(bodySinks(branchGraph, branchNodes, branchEdges, 'g2', 'x')).toEqual(['x1'])
  })

  it('falls back to the task entry when a body has no sink', () => {
    const cyclic: WorkflowEdge[] = [...branchEdges, { source: 'x1', target: 'x1' }]
    expect(bodySinks(branchGraph, branchNodes, cyclic, 'g2', 'x')).toEqual(['x1'])
  })

  it('plans header/footer per group and sink connectors only for branches', () => {
    const plan = planGroupLayout(mixedNodes, mixedEdges, mixedGraph)
    expect(plan.groups).toEqual(['g1', 'w1'])
    expect(plan.depth).toEqual({ g1: 0, w1: 1 })
    expect(plan.presentationEdges.filter((e) => e.kind === 'sink').map((e) => e.groupNodeId)).toEqual(['g1', 'g1', 'g1'])
    expect(plan.presentationEdges.filter((e) => e.kind === 'fanout').map((e) => e.taskKey)).toEqual(['t1', 't2', 't3'])
  })

  it('ignores groups that are not in the node list (stale layout) without throwing', () => {
    const plan = planGroupLayout([node('a')], [], branchGraph)
    expect(plan.groups).toEqual([])
    expect(plan.presentationNodes).toEqual([])
  })
})

describe('toDefinitionPositions', () => {
  it('saves a header as its group node and drops footers, cards and boxes', () => {
    const rf = [
      { id: 'a1', type: 'arbitronNode', position: { x: 1, y: 2 }, data: {} },
      { id: groupHeaderId('w1'), type: 'groupHeader', position: { x: 10, y: 20 }, data: { definitionNodeId: 'w1' } },
      { id: groupFooterId('w1'), type: 'groupFooter', position: { x: 10, y: 400 }, data: { definitionNodeId: 'w1' } },
      { id: groupTaskCardId('w1', 't1'), type: 'groupTaskCard', position: { x: 0, y: 200 }, data: { definitionNodeId: 'w1' } },
      { id: groupBoxId('w1'), type: 'groupContainer', position: { x: -30, y: -30 }, data: { definitionNodeId: 'w1' } },
    ] as Node[]
    expect(toDefinitionPositions(rf)).toEqual({ a1: { x: 1, y: 2 }, w1: { x: 10, y: 20 } })
  })

  it('passes loop containers and step nodes through unchanged (pre-existing behaviour)', () => {
    const rf = [
      { id: 'a', type: 'arbitronNode', position: { x: 1, y: 2 }, data: {} },
      { id: 'loop-container-x', type: 'loopContainer', position: { x: 3, y: 4 }, data: {} },
    ] as Node[]
    expect(toDefinitionPositions(rf)).toEqual({ a: { x: 1, y: 2 }, 'loop-container-x': { x: 3, y: 4 } })
  })
})
```

- [x] Step 2: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/groupLayout.test.ts
```
Expected:
```
 FAIL  src/graph/groupLayout.test.ts [ src/graph/groupLayout.test.ts ]
Error: Failed to resolve import "./groupLayout" from "src/graph/groupLayout.test.ts". Does the file exist?
 Test Files  1 failed (1)
```

- [x] Step 3: Implement `dashboard/src/graph/groupLayout.ts`:

```ts
import type { Node } from '@xyflow/react'
import type { GroupGraph, GroupProgress, GroupScope } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'

// Geometry. Presentation nodes share the step-node footprint (200 wide) so the
// existing anchor/centering maths (NODE_WIDTH / 2) holds for them too.
const NODE_WIDTH = 200
const NODE_HEIGHT = 80
export const GROUP_HEADER_HEIGHT = 80
export const GROUP_FOOTER_HEIGHT = 80
export const TASK_CARD_HEIGHT = 96
/** Padding between a group box and its members; larger than loopLayout's 24 so a loop box inside a body stays inside the group box. */
export const GROUP_PAD = 32
/** Vertical gap between header, task-card row and footer in the stored-position layout. */
export const GROUP_GAP_Y = 48
/** Horizontal gap between task cards in the stored-position layout. */
export const TASK_CARD_GAP_X = 24

/** Synthetic presentation id of a group's fan-out header. */
export function groupHeaderId(groupNodeId: string): string {
  return `group:${groupNodeId}:header`
}
/** Synthetic presentation id of a group's gather footer. */
export function groupFooterId(groupNodeId: string): string {
  return `group:${groupNodeId}:footer`
}
/** Synthetic presentation id of a group's container box. */
export function groupBoxId(groupNodeId: string): string {
  return `group:${groupNodeId}:box`
}
/** Synthetic presentation id of a workflows-form task card. */
export function groupTaskCardId(groupNodeId: string, taskKey: string): string {
  return `group:${groupNodeId}:task:${taskKey}`
}

export type GroupPresentationRole = 'header' | 'footer' | 'task'

/** Live overlay of one task (built by P4-T8 from GraphState; absent in static views). */
export interface GroupLiveTask {
  key: string
  /** Live status (GroupTaskLiveStatus). */
  status: string
  /** Status frozen into the decision; '' while undecided. */
  frozenStatus: string
  childInstanceId: string | null
  blocking: boolean
  continuingIndependently: boolean
  cancelRequested: boolean
  redacted: boolean
  launchError?: string
  winner: boolean
  /** Labels of the running/waiting nodes in this task's child run and its descendants. */
  activeNodes: string[]
}

/** Live overlay of one group run, keyed by group node id in useGraphLayout's input. */
export interface GroupLiveSummary {
  /** Instance executing the group node; null when unknown. */
  instanceId: string | null
  groupRunId: string | null
  /** GroupPanelState, or 'not_started' when the group has no run yet. */
  state: string
  progress: GroupProgress | null
  winner?: string
  /** Human text of file-gathering / resolution activity, e.g. "Resolving conflicts (attempt 2)". */
  activity?: string
  /** First waiting reason, if the gather is waiting. */
  waitingReason?: string
  tasks: Record<string, GroupLiveTask>
}

/** Which instance executes a rendered definition node, and how to style it. */
export interface NodeContextInfo {
  /** Owning instance; null when not launched yet, unknown or redacted. */
  instanceId: string | null
  /** The owning task no longer blocks its group (continuing independently). */
  independent: boolean
  /** The owning context is invisible to the caller. */
  hiddenDependency: boolean
}

/** data of groupHeader / groupFooter nodes. */
export interface GroupEndpointData {
  definitionNodeId: string
  role: 'header' | 'footer'
  label: string
  form: GroupScope['config']['form']
  completionRule: GroupScope['config']['completionRule']
  unfinishedBehavior: GroupScope['config']['unfinishedBehavior']
  gatherMode: GroupScope['config']['gatherMode']
  taskKeys: string[]
  instanceId: string | null
  live: GroupLiveSummary | null
  [key: string]: unknown
}

/** data of groupTaskCard nodes (workflows form). */
export interface GroupTaskCardData {
  definitionNodeId: string
  role: 'task'
  taskKey: string
  /** Author order, 0-based. */
  index: number
  workflow: string
  instanceId: string | null
  live: GroupLiveTask | null
  [key: string]: unknown
}

/** data of groupContainer nodes. */
export interface GroupContainerData {
  definitionNodeId: string
  label: string
  depth: number
  form: GroupScope['config']['form']
  [key: string]: unknown
}

export interface PresentationNode {
  id: string
  role: GroupPresentationRole
  groupNodeId: string
  taskKey?: string
  width: number
  height: number
}

export interface PresentationEdge {
  id: string
  source: string
  target: string
  /** sink: branch body sink -> footer; fanout: header -> task card; gather: task card -> footer. */
  kind: 'sink' | 'fanout' | 'gather'
  groupNodeId: string
  taskKey: string
}

export interface GroupCluster {
  id: string
  parent?: string
}

/**
 * Everything the layout needs to draw groups, derived ONLY from the Phase 1
 * ownership analysis (GroupGraph) plus the manifest edges — never from a
 * separate reachability interpretation.
 */
export interface GroupLayoutPlan {
  /** Group node ids drawn, in GroupGraph.order. */
  groups: string[]
  scopes: Record<string, GroupScope>
  /** Nesting depth: 0 for a main-path group. */
  depth: Record<string, number>
  /** Definition ids replaced by presentation nodes (the group nodes). */
  hidden: Set<string>
  presentationNodes: PresentationNode[]
  presentationEdges: PresentationEdge[]
  /** dagre compound clusters, parents first. */
  clusters: GroupCluster[]
  /** Rendered node id -> its immediate dagre cluster. */
  clusterOf: Record<string, string>
  /** Task cluster id per "groupNodeId\u0000taskKey" (branches form). */
  taskCluster: Record<string, string>
  /** Immediate rendered members per group box (nested groups by group id). */
  members: Record<string, string[]>
}

function groupClusterId(g: string): string {
  return `cluster-group:${g}`
}
function taskClusterId(g: string, key: string): string {
  return `cluster-group:${g}:task:${key}`
}
function ownerKey(g: string, key: string): string {
  return `${g}\u0000${key}`
}

/** Group node ids of `graph` that are present in `nodes`, in GroupGraph.order. */
function drawnGroups(graph: GroupGraph, nodeIds: Set<string>): string[] {
  return graph.order.filter((g) => nodeIds.has(g) && graph.groups[g] != null)
}

/**
 * Immediate members of (group, task): every node whose IMMEDIATE owner is that
 * pair, in the task's Bodies order first, then any remaining owned node in
 * definition order. Nested groups' body nodes belong to the nested group.
 */
export function immediateMembers(graph: GroupGraph, nodes: WorkflowNode[], groupNodeId: string, taskKey: string): string[] {
  const owned = (id: string) => {
    const o = graph.owner[id]
    return o != null && o.groupNodeId === groupNodeId && o.taskKey === taskKey
  }
  const out: string[] = []
  const seen = new Set<string>()
  for (const id of graph.groups[groupNodeId]?.bodies[taskKey] ?? []) {
    if (owned(id) && !seen.has(id)) {
      seen.add(id)
      out.push(id)
    }
  }
  for (const n of nodes) {
    if (owned(n.id) && !seen.has(n.id)) {
      seen.add(n.id)
      out.push(n.id)
    }
  }
  return out
}

/**
 * Sinks of a branch body: immediate members (not nested groups) with no
 * outgoing manifest edge. A body is closed, so these are where the branch ends.
 * Falls back to the task entry when a body has no such node.
 */
export function bodySinks(
  graph: GroupGraph,
  nodes: WorkflowNode[],
  edges: WorkflowEdge[],
  groupNodeId: string,
  taskKey: string,
): string[] {
  const hasOutgoing = new Set(edges.map((e) => e.source))
  const members = immediateMembers(graph, nodes, groupNodeId, taskKey)
  const sinks = members.filter((id) => graph.groups[id] == null && !hasOutgoing.has(id))
  if (sinks.length > 0) return sinks
  const entry = graph.groups[groupNodeId]?.config.tasks.find((t) => t.key === taskKey)?.entry
  return entry ? [entry] : []
}

/** Rendered id an edge endpoint maps to: a group node becomes its header (incoming) or footer (outgoing non-task). */
export function renderedSource(graph: GroupGraph | null, e: WorkflowEdge): string {
  if (!graph || graph.groups[e.source] == null) return e.source
  return e.output === 'task' ? groupHeaderId(e.source) : groupFooterId(e.source)
}
export function renderedTarget(graph: GroupGraph | null, e: WorkflowEdge): string {
  if (!graph || graph.groups[e.target] == null) return e.target
  return groupHeaderId(e.target)
}

/** Rendered id of a definition node that is a member of a body (a nested group renders via its footer). */
function renderedMemberSink(graph: GroupGraph, id: string): string {
  return graph.groups[id] != null ? groupFooterId(id) : id
}

export function groupContainerLabel(scope: GroupScope): string {
  const form = scope.config.form === 'workflows' ? 'Parallel workflows' : 'Parallel branches'
  const rule = scope.config.completionRule === 'first_success' ? 'first success' : 'all succeed'
  return `${form} · ${rule}`
}

/** Builds the presentation plan for every drawn group. */
export function planGroupLayout(nodes: WorkflowNode[], edges: WorkflowEdge[], graph: GroupGraph): GroupLayoutPlan {
  const nodeIds = new Set(nodes.map((n) => n.id))
  const groups = drawnGroups(graph, nodeIds)
  const drawn = new Set(groups)
  const scopes: Record<string, GroupScope> = {}
  for (const g of groups) scopes[g] = graph.groups[g]

  const depth: Record<string, number> = {}
  const depthOf = (g: string, guard = 0): number => {
    if (depth[g] != null) return depth[g]
    const parent = scopes[g]?.parent?.groupNodeId
    const d = parent && drawn.has(parent) && guard < 64 ? depthOf(parent, guard + 1) + 1 : 0
    depth[g] = d
    return d
  }
  for (const g of groups) depthOf(g)

  const presentationNodes: PresentationNode[] = []
  const presentationEdges: PresentationEdge[] = []
  const clusters: GroupCluster[] = []
  const clusterOf: Record<string, string> = {}
  const taskCluster: Record<string, string> = {}
  const members: Record<string, string[]> = {}

  // Parents before children so dagre sees every parent cluster first.
  const byDepth = [...groups].sort((a, b) => depth[a] - depth[b])
  for (const g of byDepth) {
    const scope = scopes[g]
    const parent = scope.parent && drawn.has(scope.parent.groupNodeId)
      ? taskCluster[ownerKey(scope.parent.groupNodeId, scope.parent.taskKey)]
      : undefined
    const cluster = groupClusterId(g)
    clusters.push(parent ? { id: cluster, parent } : { id: cluster })

    const header = groupHeaderId(g)
    const footer = groupFooterId(g)
    presentationNodes.push({ id: header, role: 'header', groupNodeId: g, width: NODE_WIDTH, height: GROUP_HEADER_HEIGHT })
    presentationNodes.push({ id: footer, role: 'footer', groupNodeId: g, width: NODE_WIDTH, height: GROUP_FOOTER_HEIGHT })
    clusterOf[header] = cluster
    clusterOf[footer] = cluster
    const boxMembers = [header, footer]

    if (scope.config.form === 'workflows') {
      for (const task of scope.config.tasks) {
        const card = groupTaskCardId(g, task.key)
        presentationNodes.push({ id: card, role: 'task', groupNodeId: g, taskKey: task.key, width: NODE_WIDTH, height: TASK_CARD_HEIGHT })
        clusterOf[card] = cluster
        boxMembers.push(card)
        presentationEdges.push({ id: `group:${g}:fanout:${task.key}`, source: header, target: card, kind: 'fanout', groupNodeId: g, taskKey: task.key })
        presentationEdges.push({ id: `group:${g}:gather:${task.key}`, source: card, target: footer, kind: 'gather', groupNodeId: g, taskKey: task.key })
      }
    } else {
      for (const task of scope.config.tasks) {
        const body = immediateMembers(graph, nodes, g, task.key).filter((id) => nodeIds.has(id))
        if (body.length > 0) {
          const tc = taskClusterId(g, task.key)
          clusters.push({ id: tc, parent: cluster })
          taskCluster[ownerKey(g, task.key)] = tc
          for (const id of body) {
            if (graph.groups[id] == null) clusterOf[id] = tc
          }
        }
        boxMembers.push(...body)
        for (const sink of bodySinks(graph, nodes, edges, g, task.key)) {
          const source = renderedMemberSink(graph, sink)
          presentationEdges.push({ id: `group:${g}:sink:${task.key}:${sink}`, source, target: footer, kind: 'sink', groupNodeId: g, taskKey: task.key })
        }
      }
    }
    members[g] = boxMembers
  }

  return {
    groups,
    scopes,
    depth,
    hidden: new Set(groups),
    presentationNodes,
    presentationEdges,
    clusters,
    clusterOf,
    taskCluster,
    members,
  }
}

/** The task cluster that owns a definition node (for parenting a loop cluster inside a branch body). */
export function ownerCluster(plan: GroupLayoutPlan, graph: GroupGraph, nodeId: string): string | undefined {
  const o = graph.owner[nodeId]
  if (!o) return undefined
  return plan.taskCluster[ownerKey(o.groupNodeId, o.taskKey)]
}

export interface Box {
  x: number
  y: number
  width: number
  height: number
}

function sizeOf(plan: GroupLayoutPlan, id: string): { width: number; height: number } {
  const p = plan.presentationNodes.find((n) => n.id === id)
  return p ? { width: p.width, height: p.height } : { width: NODE_WIDTH, height: NODE_HEIGHT }
}

function boundsOf(rects: Box[]): Box | null {
  if (rects.length === 0) return null
  const minX = Math.min(...rects.map((r) => r.x))
  const minY = Math.min(...rects.map((r) => r.y))
  const maxX = Math.max(...rects.map((r) => r.x + r.width))
  const maxY = Math.max(...rects.map((r) => r.y + r.height))
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
}

function memberRect(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>, boxes: Record<string, Box>, id: string): Box | null {
  if (plan.scopes[id] != null) return boxes[id] ?? null
  const p = positions[id]
  if (!p) return null
  const s = sizeOf(plan, id)
  return { x: p.x, y: p.y, width: s.width, height: s.height }
}

/**
 * Stored-position layout: the header takes the group node's saved position;
 * workflow task cards form an author-ordered row under it; the footer sits
 * under the cards (workflows) or under the lowest body member (branches).
 * Innermost groups first, so an enclosing footer clears nested boxes.
 */
export function placeGroupsFromStoredPositions(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): void {
  const boxes: Record<string, Box> = {}
  const innermostFirst = [...plan.groups].sort((a, b) => plan.depth[b] - plan.depth[a])
  for (const g of innermostFirst) {
    const scope = plan.scopes[g]
    const head = positions[g] ?? { x: 0, y: 0 }
    positions[groupHeaderId(g)] = { x: head.x, y: head.y }
    let bottom = head.y + GROUP_HEADER_HEIGHT
    if (scope.config.form === 'workflows') {
      const n = scope.config.tasks.length
      const rowWidth = n * NODE_WIDTH + Math.max(0, n - 1) * TASK_CARD_GAP_X
      const startX = head.x + NODE_WIDTH / 2 - rowWidth / 2
      const y = head.y + GROUP_HEADER_HEIGHT + GROUP_GAP_Y
      scope.config.tasks.forEach((task, i) => {
        positions[groupTaskCardId(g, task.key)] = { x: startX + i * (NODE_WIDTH + TASK_CARD_GAP_X), y }
      })
      if (n > 0) bottom = y + TASK_CARD_HEIGHT
    } else {
      for (const id of plan.members[g]) {
        if (id === groupHeaderId(g) || id === groupFooterId(g)) continue
        const r = memberRect(plan, positions, boxes, id)
        if (r) bottom = Math.max(bottom, r.y + r.height)
      }
    }
    positions[groupFooterId(g)] = { x: head.x, y: bottom + GROUP_GAP_Y }
    const box = computeGroupBox(plan, positions, boxes, g)
    if (box) boxes[g] = box
  }
}

function computeGroupBox(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>, boxes: Record<string, Box>, g: string): Box | null {
  const rects: Box[] = []
  for (const id of plan.members[g]) {
    const r = memberRect(plan, positions, boxes, id)
    if (r) rects.push(r)
  }
  const b = boundsOf(rects)
  if (!b) return null
  return { x: b.x - GROUP_PAD, y: b.y - GROUP_PAD, width: b.width + GROUP_PAD * 2, height: b.height + GROUP_PAD * 2 }
}

/** Container boxes for every drawn group, innermost first so an enclosing box wraps its nested boxes. */
export function groupContainerBoxes(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): Record<string, Box> {
  const boxes: Record<string, Box> = {}
  const innermostFirst = [...plan.groups].sort((a, b) => plan.depth[b] - plan.depth[a])
  for (const g of innermostFirst) {
    const box = computeGroupBox(plan, positions, boxes, g)
    if (box) boxes[g] = box
  }
  return boxes
}

/**
 * After dagre, re-assign workflow task-card x positions in author order (dagre
 * may permute nodes within a rank to reduce crossings; task order is
 * author-defined and must read left to right).
 */
export function orderTaskCards(plan: GroupLayoutPlan, positions: Record<string, { x: number; y: number }>): void {
  for (const g of plan.groups) {
    const scope = plan.scopes[g]
    if (scope.config.form !== 'workflows') continue
    const ids = scope.config.tasks.map((t) => groupTaskCardId(g, t.key)).filter((id) => positions[id] != null)
    const xs = ids.map((id) => positions[id].x).sort((a, b) => a - b)
    ids.forEach((id, i) => {
      positions[id] = { x: xs[i], y: positions[id].y }
    })
  }
}

/**
 * Maps laid-out React Flow nodes back to DEFINITION positions for
 * saveLayout: a group header saves as its group node; footers, task cards and
 * container boxes are derived presentation and are never persisted.
 */
export function toDefinitionPositions(rfNodes: Node[]): Record<string, { x: number; y: number }> {
  const out: Record<string, { x: number; y: number }> = {}
  for (const n of rfNodes) {
    if (n.type === 'groupFooter' || n.type === 'groupTaskCard' || n.type === 'groupContainer') continue
    if (n.type === 'groupHeader') {
      const def = (n.data as { definitionNodeId?: unknown }).definitionNodeId
      if (typeof def === 'string') out[def] = { x: n.position.x, y: n.position.y }
      continue
    }
    out[n.id] = { x: n.position.x, y: n.position.y }
  }
  return out
}

/** The definition node a rendered React Flow node stands for. */
export function definitionNodeIdOf(n: Pick<Node, 'id' | 'data'>): string {
  const def = (n.data as { definitionNodeId?: unknown } | undefined)?.definitionNodeId
  return typeof def === 'string' ? def : n.id
}
```

Design notes the implementer must keep:
- Ownership is read from `GroupGraph.owner` (immediate owner) and `GroupGraph.groups[*].bodies` (order) only — no reachability walk. A nested group node is a member of its enclosing task; its own body nodes are members of the nested group.
- Presentation ids use the `group:<id>:…` scheme; consumers map back with `data.definitionNodeId` (`definitionNodeIdOf`), never by parsing the id (definition node ids are unconstrained strings).
- `GROUP_PAD` (32) is larger than `loopLayout.ts`'s `CONTAINER_PAD` (24), so a loop box inside a branch body stays inside the group box.

- [x] Step 4: Run it, confirm it passes.

```bash
cd dashboard && npx vitest run src/graph/groupLayout.test.ts && npx tsc --noEmit
```
Expected:
```
 Test Files  1 passed (1)
      Tests  10 passed (10)
```

- [x] Step 5: Commit.

```bash
git add dashboard/src/graph/groupLayout.ts dashboard/src/graph/groupLayout.fixtures.ts dashboard/src/graph/groupLayout.test.ts
git commit -m "feat(dashboard): group layout planner from Phase 1 ownership" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — `useGraphLayout` integration

- [x] Step 6: Write the failing integration tests `dashboard/src/graph/useGraphLayout.groups.test.ts` (two-level nesting in both forms, bodies not overlapping, sink connectors present and absent from the manifest, stored positions, live overlays, and the loop regression guards):

```ts
import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import type { Edge, Node } from '@xyflow/react'
import type { GroupGraph } from '../api/types'
import type { WorkflowEdge, WorkflowNode } from './types'
import { useGraphLayout } from './useGraphLayout'
import {
  GROUP_PAD,
  groupBoxId,
  groupFooterId,
  groupHeaderId,
  groupTaskCardId,
  toDefinitionPositions,
  type GroupLiveSummary,
  type NodeContextInfo,
} from './groupLayout'
import {
  EMPTY_GRAPH,
  branchEdges,
  branchGraph,
  branchNodes,
  mixedEdges,
  mixedGraph,
  mixedNodes,
  node,
  scope,
} from './groupLayout.fixtures'

type Rect = { x: number; y: number; width: number; height: number }

const W = 200
function rectOf(n: Node | undefined, h = 80): Rect {
  if (!n) throw new Error('node missing')
  if (n.width != null && n.height != null) return { x: n.position.x, y: n.position.y, width: n.width, height: n.height }
  return { x: n.position.x, y: n.position.y, width: W, height: h }
}
function union(rs: Rect[]): Rect {
  const x = Math.min(...rs.map((r) => r.x))
  const y = Math.min(...rs.map((r) => r.y))
  return { x, y, width: Math.max(...rs.map((r) => r.x + r.width)) - x, height: Math.max(...rs.map((r) => r.y + r.height)) - y }
}
function overlaps(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height
}
function contains(outer: Rect, inner: Rect): boolean {
  return inner.x >= outer.x && inner.y >= outer.y && inner.x + inner.width <= outer.x + outer.width && inner.y + inner.height <= outer.y + outer.height
}

function layout(nodes: WorkflowNode[], edges: WorkflowEdge[], groups: GroupGraph | null, extra: { groupLive?: Record<string, GroupLiveSummary>; nodeContexts?: Record<string, NodeContextInfo> } = {}) {
  const { result } = renderHook(() => useGraphLayout({ nodes, edges, groups, ...extra }))
  const byId = new Map(result.current.rfNodes.map((n) => [n.id, n]))
  return { ...result.current, byId }
}

const edgeFrom = (edges: Edge[], source: string, target: string) => edges.find((e) => e.source === source && e.target === target)

// ---------------------------------------------------------------------------
// useGraphLayout with groups — branches form, two levels
// ---------------------------------------------------------------------------

describe('useGraphLayout — branches groups nested two levels', () => {
  it('replaces each group node with a header and a footer mapped back to the definition node', () => {
    const { rfNodes, byId } = layout(branchNodes, branchEdges, branchGraph)
    expect(byId.has('g1')).toBe(false)
    expect(byId.has('g2')).toBe(false)
    for (const g of ['g1', 'g2']) {
      expect(byId.get(groupHeaderId(g))?.type).toBe('groupHeader')
      expect(byId.get(groupFooterId(g))?.type).toBe('groupFooter')
      expect((byId.get(groupHeaderId(g))!.data as { definitionNodeId: string }).definitionNodeId).toBe(g)
      expect((byId.get(groupFooterId(g))!.data as { definitionNodeId: string }).definitionNodeId).toBe(g)
    }
    // Every non-group definition node is still a step node.
    expect(rfNodes.filter((n) => n.type === 'arbitronNode').map((n) => n.id).sort()).toEqual(
      ['a1', 'a2', 'b2', 'b3', 'done', 'oops', 'start', 'x1', 'y1', 'y2'],
    )
  })

  it('routes task edges out of the header and success/failure out of the footer', () => {
    const { rfEdges } = layout(branchNodes, branchEdges, branchGraph)
    expect(edgeFrom(rfEdges, 'start', groupHeaderId('g1'))).toBeDefined()
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), 'a1')?.label).toBe('a')
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), groupHeaderId('g2'))?.label).toBe('b')
    expect(edgeFrom(rfEdges, groupFooterId('g1'), 'done')?.label).toBe('success')
    expect(edgeFrom(rfEdges, groupFooterId('g1'), 'oops')?.label).toBe('failure')
    expect(edgeFrom(rfEdges, groupFooterId('g2'), 'b2')?.label).toBe('success')
    expect(rfEdges.some((e) => e.source === 'g1' || e.target === 'g1')).toBe(false)
  })

  it('adds presentation-only sink connectors from every branch body sink into its footer', () => {
    const { rfEdges } = layout(branchNodes, branchEdges, branchGraph)
    const sinks = rfEdges.filter((e) => (e.data as { kind?: string } | undefined)?.kind === 'sink')
    expect(sinks.map((e) => `${e.source}->${e.target}`).sort()).toEqual(
      [
        `a2->${groupFooterId('g1')}`,
        `b2->${groupFooterId('g1')}`,
        `b3->${groupFooterId('g1')}`,
        `x1->${groupFooterId('g2')}`,
        `y2->${groupFooterId('g2')}`,
      ].sort(),
    )
    for (const e of sinks) {
      expect((e.data as { presentationOnly?: boolean }).presentationOnly).toBe(true)
      expect(e.selectable).toBe(false)
      expect((e.style as { strokeDasharray?: string }).strokeDasharray).toBe('4 4')
    }
  })

  it('never adds a connector to the manifest edges it was given', () => {
    const edges = branchEdges.map((e) => ({ ...e }))
    const snapshot = JSON.parse(JSON.stringify(edges))
    layout(branchNodes, edges, branchGraph)
    expect(edges).toEqual(snapshot)
  })

  it('draws a container box per group, the nested one inside the outer one and above it', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const outer = byId.get(groupBoxId('g1'))!
    const inner = byId.get(groupBoxId('g2'))!
    expect(outer.type).toBe('groupContainer')
    expect(outer.selectable).toBe(false)
    expect(outer.draggable).toBe(false)
    expect(contains(rectOf(outer), rectOf(inner))).toBe(true)
    expect(inner.zIndex!).toBeGreaterThan(outer.zIndex!)
    expect((outer.data as { label: string }).label).toBe('Parallel branches · all succeed')
  })

  it('keeps every body inside its own box with the header on top and the footer at the bottom', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const box = rectOf(byId.get(groupBoxId('g1')))
    for (const id of ['a1', 'a2', 'b2', 'b3', groupHeaderId('g1'), groupFooterId('g1')]) {
      expect(contains(box, rectOf(byId.get(id)))).toBe(true)
    }
    const header = byId.get(groupHeaderId('g1'))!.position.y
    const footer = byId.get(groupFooterId('g1'))!.position.y
    for (const id of ['a1', 'a2', 'b2', 'b3', 'x1', 'y1', 'y2']) {
      expect(byId.get(id)!.position.y).toBeGreaterThan(header)
      expect(byId.get(id)!.position.y).toBeLessThan(footer)
    }
    const inner = rectOf(byId.get(groupBoxId('g2')))
    for (const id of ['x1', 'y1', 'y2']) expect(contains(inner, rectOf(byId.get(id)))).toBe(true)
    // Nodes outside the group stay outside its box.
    for (const id of ['start', 'done', 'oops']) expect(overlaps(box, rectOf(byId.get(id)))).toBe(false)
  })

  it('lays out sibling task bodies without overlap at both levels', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const bodyA = union(['a1', 'a2'].map((id) => rectOf(byId.get(id))))
    const bodyB = union([rectOf(byId.get(groupBoxId('g2'))), ...['b2', 'b3'].map((id) => rectOf(byId.get(id)))])
    expect(overlaps(bodyA, bodyB)).toBe(false)
    const bodyX = rectOf(byId.get('x1'))
    const bodyY = union(['y1', 'y2'].map((id) => rectOf(byId.get(id))))
    expect(overlaps(bodyX, bodyY)).toBe(false)
  })

  it('pads the box by GROUP_PAD around its members', () => {
    const { byId } = layout(branchNodes, branchEdges, branchGraph)
    const box = rectOf(byId.get(groupBoxId('g2')))
    const members = union([groupHeaderId('g2'), groupFooterId('g2'), 'x1', 'y1', 'y2'].map((id) => rectOf(byId.get(id))))
    expect(box.x).toBe(members.x - GROUP_PAD)
    expect(box.y).toBe(members.y - GROUP_PAD)
    expect(box.width).toBe(members.width + GROUP_PAD * 2)
    expect(box.height).toBe(members.height + GROUP_PAD * 2)
  })
})

// ---------------------------------------------------------------------------
// useGraphLayout with groups — workflows form nested in branches
// ---------------------------------------------------------------------------

describe('useGraphLayout — workflows group nested inside a branches group', () => {
  it('renders ordered task cards inside the workflows box, linked header -> card -> footer', () => {
    const { byId, rfEdges } = layout(mixedNodes, mixedEdges, mixedGraph)
    const cards = ['t1', 't2', 't3'].map((k) => byId.get(groupTaskCardId('w1', k))!)
    for (const c of cards) expect(c.type).toBe('groupTaskCard')
    expect(cards.map((c) => (c.data as { workflow: string }).workflow)).toEqual(['child-a', 'child-b', 'child-a'])
    expect(cards.map((c) => (c.data as { index: number }).index)).toEqual([0, 1, 2])
    // Author order reads left to right.
    expect(cards[0].position.x).toBeLessThan(cards[1].position.x)
    expect(cards[1].position.x).toBeLessThan(cards[2].position.x)
    const box = rectOf(byId.get(groupBoxId('w1')))
    for (const c of cards) expect(contains(box, rectOf(c, 96))).toBe(true)
    for (const k of ['t1', 't2', 't3']) {
      const fan = edgeFrom(rfEdges, groupHeaderId('w1'), groupTaskCardId('w1', k))!
      const gather = edgeFrom(rfEdges, groupTaskCardId('w1', k), groupFooterId('w1'))!
      expect((fan.data as { presentationOnly: boolean }).presentationOnly).toBe(true)
      expect((gather.data as { presentationOnly: boolean }).presentationOnly).toBe(true)
    }
  })

  it('nests the workflows box inside the branches box and wires its ports from the footer', () => {
    const { byId, rfEdges } = layout(mixedNodes, mixedEdges, mixedGraph)
    expect(contains(rectOf(byId.get(groupBoxId('g1'))), rectOf(byId.get(groupBoxId('w1'))))).toBe(true)
    expect(edgeFrom(rfEdges, groupHeaderId('g1'), groupHeaderId('w1'))?.label).toBe('b')
    expect(edgeFrom(rfEdges, groupFooterId('w1'), 'b2')?.label).toBe('success')
    // A workflows group has no body sinks of its own.
    expect(rfEdges.some((e) => (e.data as { kind?: string } | undefined)?.kind === 'sink' && e.target === groupFooterId('w1'))).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Stored positions
// ---------------------------------------------------------------------------

describe('useGraphLayout — groups with stored positions', () => {
  const positioned = mixedNodes.map((n, i) => ({ ...n, position: { x: 0, y: i * 200 } }))
  const at = (id: string, x: number, y: number) => positioned.find((n) => n.id === id)!.position = { x, y }
  at('start', 0, 0)
  at('g1', 0, 150)
  at('a1', -300, 330)
  at('w1', 300, 330)
  at('b2', 200, 800)
  at('b3', 450, 800)
  at('done', 0, 1100)
  at('oops', 300, 1100)

  it('keeps saved positions, puts the header at the group node and needs no auto-save', () => {
    const { byId, needsAutoSave } = layout(positioned, mixedEdges, mixedGraph)
    expect(needsAutoSave).toBe(false)
    expect(byId.get('a1')!.position).toEqual({ x: -300, y: 330 })
    expect(byId.get(groupHeaderId('g1'))!.position).toEqual({ x: 0, y: 150 })
    expect(byId.get(groupHeaderId('w1'))!.position).toEqual({ x: 300, y: 330 })
  })

  it('places task cards in author order under the header and footers below their content', () => {
    const { byId } = layout(positioned, mixedEdges, mixedGraph)
    const xs = ['t1', 't2', 't3'].map((k) => byId.get(groupTaskCardId('w1', k))!.position.x)
    expect(xs[0]).toBeLessThan(xs[1])
    expect(xs[1]).toBeLessThan(xs[2])
    const cardBottom = byId.get(groupTaskCardId('w1', 't1'))!.position.y + 96
    expect(byId.get(groupFooterId('w1'))!.position.y).toBeGreaterThan(cardBottom)
    // The outer footer clears the lowest member of its body (b2/b3 at y=800).
    expect(byId.get(groupFooterId('g1'))!.position.y).toBeGreaterThan(880)
    expect(contains(rectOf(byId.get(groupBoxId('g1'))), rectOf(byId.get(groupBoxId('w1'))))).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Live overlays and owning-instance stamping
// ---------------------------------------------------------------------------

describe('useGraphLayout — live overlays', () => {
  const live: Record<string, GroupLiveSummary> = {
    w1: {
      instanceId: 'branch-b',
      groupRunId: 'run-w1',
      state: 'running',
      progress: { total: 3, completed: 1, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
      tasks: {
        t2: { key: 't2', status: 'running', frozenStatus: '', childInstanceId: 'child-t2', blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, winner: false, activeNodes: ['Build'] },
      },
    },
  }
  const contexts: Record<string, NodeContextInfo> = {
    a1: { instanceId: 'branch-a', independent: true, hiddenDependency: false },
    w1: { instanceId: 'branch-b', independent: false, hiddenDependency: false },
  }

  it('hands each header/footer its group summary and each card its task overlay', () => {
    const { byId } = layout(mixedNodes, mixedEdges, mixedGraph, { groupLive: live, nodeContexts: contexts })
    const footer = byId.get(groupFooterId('w1'))!.data as { live: GroupLiveSummary | null; instanceId: string | null }
    expect(footer.live?.groupRunId).toBe('run-w1')
    expect(footer.instanceId).toBe('branch-b')
    const card = byId.get(groupTaskCardId('w1', 't2'))!.data as { live: { childInstanceId: string } | null }
    expect(card.live?.childInstanceId).toBe('child-t2')
    expect((byId.get(groupTaskCardId('w1', 't1'))!.data as { live: unknown }).live).toBeNull()
    expect((byId.get(groupHeaderId('g1'))!.data as { live: unknown }).live).toBeNull()
  })

  it('stamps the owning instance and independence onto step nodes', () => {
    const { byId } = layout(mixedNodes, mixedEdges, mixedGraph, { groupLive: live, nodeContexts: contexts })
    expect(byId.get('a1')!.data).toMatchObject({ instanceId: 'branch-a', independent: true, hiddenDependency: false })
    expect(byId.get('start')!.data).not.toHaveProperty('instanceId')
  })
})

// ---------------------------------------------------------------------------
// Regression: graphs without groups and existing loop layouts are unaffected
// ---------------------------------------------------------------------------

describe('useGraphLayout — existing loop layouts unaffected', () => {
  const loopNodes = [node('a'), node('b'), node('c'), node('d')]
  const loopEdges: WorkflowEdge[] = [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ]

  it('produces identical nodes and edges for a loop graph with an empty or null group layout', () => {
    const plain = layout(loopNodes, loopEdges, null)
    const empty = layout(loopNodes, loopEdges, EMPTY_GRAPH)
    const unrelated = layout(loopNodes, loopEdges, branchGraph) // groups not in this graph
    expect(empty.rfNodes).toEqual(plain.rfNodes)
    expect(empty.rfEdges).toEqual(plain.rfEdges)
    expect(unrelated.rfNodes).toEqual(plain.rfNodes)
    expect(unrelated.rfEdges).toEqual(plain.rfEdges)
    expect(plain.rfNodes.some((n) => n.type?.startsWith('group'))).toBe(false)
  })

  it('keeps a loop inside a branch body inside that group box', () => {
    const nodes = [node('start'), node('g', 'parallel_group'), node('l1'), node('l2'), node('l3'), node('k1'), node('done'), node('oops')]
    const edges: WorkflowEdge[] = [
      { source: 'start', target: 'g' },
      { source: 'g', target: 'l1', output: 'task', label: 'loop' },
      { source: 'g', target: 'k1', output: 'task', label: 'plain' },
      { source: 'l1', target: 'l2' },
      { source: 'l2', target: 'l1', label: 'again' },
      { source: 'l2', target: 'l3' },
      { source: 'g', target: 'done', output: 'success' },
      { source: 'g', target: 'oops', output: 'failure' },
    ]
    const graph: GroupGraph = {
      groups: {
        g: scope({
          nodeId: 'g',
          config: { form: 'branches', tasks: [{ key: 'loop', entry: 'l1' }, { key: 'plain', entry: 'k1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
          bodies: { loop: ['l1', 'l2', 'l3'], plain: ['k1'] },
          successTarget: 'done',
          failureTarget: 'oops',
        }),
      },
      owner: { l1: { groupNodeId: 'g', taskKey: 'loop' }, l2: { groupNodeId: 'g', taskKey: 'loop' }, l3: { groupNodeId: 'g', taskKey: 'loop' }, k1: { groupNodeId: 'g', taskKey: 'plain' } },
      order: ['g'],
    }
    const { byId, rfEdges } = layout(nodes, edges, graph)
    const loopBox = [...byId.values()].find((n) => n.type === 'loopContainer')!
    expect(loopBox).toBeDefined()
    expect(contains(rectOf(byId.get(groupBoxId('g'))), rectOf(loopBox))).toBe(true)
    expect(rfEdges.find((e) => e.source === 'l2' && e.target === 'l1')?.type).toBe('loopBackEdge')
    expect(loopBox.zIndex!).toBeGreaterThan(byId.get(groupBoxId('g'))!.zIndex!)
  })

  it('keeps rfNodes/rfEdges referentially stable across re-renders with groups', () => {
    const { result, rerender } = renderHook(() => useGraphLayout({ nodes: branchNodes, edges: branchEdges, groups: branchGraph }))
    const first = result.current
    rerender()
    expect(result.current.rfNodes).toBe(first.rfNodes)
    expect(result.current.rfEdges).toBe(first.rfEdges)
  })
})

// ---------------------------------------------------------------------------
// toDefinitionPositions over a real layout (saveLayout mapping)
// ---------------------------------------------------------------------------

describe('toDefinitionPositions over a laid-out group graph', () => {
  it('saves a header as its group node and drops footers, cards and boxes', () => {
    const { rfNodes } = layout(mixedNodes, mixedEdges, mixedGraph)
    const out = toDefinitionPositions(rfNodes)
    expect(Object.keys(out).sort()).toEqual(['a1', 'b2', 'b3', 'done', 'g1', 'oops', 'start', 'w1'])
    const header = rfNodes.find((n) => n.id === groupHeaderId('w1'))!
    expect(out.w1).toEqual(header.position)
  })

})
```

- [x] Step 7: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/useGraphLayout.groups.test.ts
```
Expected (the two regression guards already pass; everything that needs the `groups` input fails):
```
 FAIL  src/graph/useGraphLayout.groups.test.ts > useGraphLayout — branches groups nested two levels > replaces each group node with a header and a footer mapped back to the definition node
AssertionError: expected true to be false // Object.is equality
 FAIL  src/graph/useGraphLayout.groups.test.ts > useGraphLayout — branches groups nested two levels > routes task edges out of the header and success/failure out of the footer
AssertionError: expected undefined to be defined
 FAIL  src/graph/useGraphLayout.groups.test.ts > useGraphLayout — branches groups nested two levels > adds presentation-only sink connectors from every branch body sink into its footer
AssertionError: expected [] to deeply equal [ 'a2->group:g1:footer', …(4) ]
...
      Tests  15 failed | 3 passed (18)
```

- [x] Step 8: Replace the whole of `dashboard/src/graph/useGraphLayout.ts` (lines 1-190) with the version below. Relative to the current file it (a) imports the planner, (b) adds the three optional inputs with stable empty defaults, (c) in the dagre branch skips hidden group nodes, adds presentation nodes, group/task clusters (parented before loop clusters, and a loop cluster inside a branch body is parented to that task cluster), seeds remapped manifest edges plus zero-weight presentation edges (so bodies hug their header instead of being pulled toward the footer), then re-orders task cards by author order, (d) in the stored-positions branch calls `placeGroupsFromStoredPositions`, (e) emits group boxes first, then loop containers, then step nodes, then header/footer/card nodes, and (f) remaps manifest edge endpoints and appends the presentation-only edges. Every line of the no-group path is unchanged:

```ts
import { useMemo } from 'react'
import dagre from 'dagre'
import type { Node, Edge } from '@xyflow/react'
import { MarkerType } from '@xyflow/react'
import type { GroupGraph } from '../api/types'
import type { WorkflowNode, WorkflowEdge, NodeExecutionState } from './types'
import type { ArbitronNodeData } from './nodes/ArbitronNode'
import { detectLoops, type Loop } from './loops'
import { loopContainerBox, loopContainerLabel } from './loopLayout'
import { chooseLoopSide } from './loopEdgeRouting'
import {
  groupContainerBoxes,
  groupContainerLabel,
  groupBoxId,
  orderTaskCards,
  ownerCluster,
  placeGroupsFromStoredPositions,
  planGroupLayout,
  renderedSource,
  renderedTarget,
  type GroupContainerData,
  type GroupEndpointData,
  type GroupLayoutPlan,
  type GroupLiveSummary,
  type GroupTaskCardData,
  type NodeContextInfo,
} from './groupLayout'

export const NODE_WIDTH = 200
export const NODE_HEIGHT = 80

// Stable empty-states reference. Used as the default so that callers which omit
// executionStates (e.g. the static WorkflowReview view) don't pass a fresh `{}`
// each render, which would be a new dependency every time and defeat the useMemo
// below — recomputing the layout on every render and making rfNodes/rfEdges
// referentially unstable. That instability drives WorkflowReview's
// setNodes(rfNodes) sync-effect into an infinite loop (React error #185) on the
// first node selection. Must never be mutated.
const EMPTY_EXECUTION_STATES: Record<string, NodeExecutionState> = {}
// Same reasoning for the group overlays (P4-T7): stable empty defaults.
const EMPTY_GROUP_LIVE: Record<string, GroupLiveSummary> = {}
const EMPTY_NODE_CONTEXTS: Record<string, NodeContextInfo> = {}

interface UseGraphLayoutInput {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
  executionStates?: Record<string, NodeExecutionState>
  /**
   * Phase 1 ownership analysis of this graph. When it names at least one group
   * present in `nodes`, each group renders as a container box with a fan-out
   * header and a gather footer (plus task cards for the workflows form).
   */
  groups?: GroupGraph | null
  /** Live group overlays keyed by group node id (absent in static views). */
  groupLive?: Record<string, GroupLiveSummary>
  /** Owning-instance info keyed by definition node id (absent in static views). */
  nodeContexts?: Record<string, NodeContextInfo>
}

interface UseGraphLayoutOutput {
  rfNodes: Node[]
  rfEdges: Edge[]
  needsAutoSave: boolean
}

const PRESENTATION_EDGE_STYLE = { stroke: '#5b6b8c', strokeDasharray: '4 4' }

export function useGraphLayout({
  nodes,
  edges,
  executionStates = EMPTY_EXECUTION_STATES,
  groups = null,
  groupLive = EMPTY_GROUP_LIVE,
  nodeContexts = EMPTY_NODE_CONTEXTS,
}: UseGraphLayoutInput): UseGraphLayoutOutput {
  return useMemo(() => {
    if (nodes.length === 0) {
      return { rfNodes: [], rfEdges: [], needsAutoSave: false }
    }

    const detection = detectLoops(edges)
    const plan: GroupLayoutPlan | null =
      groups && groups.order.length > 0 ? planGroupLayout(nodes, edges, groups) : null
    const activePlan = plan && plan.groups.length > 0 ? plan : null
    const graph = activePlan ? groups : null
    const isHidden = (id: string) => activePlan?.hidden.has(id) === true

    // All-or-nothing rule: use stored positions only when every node has one.
    const allHavePositions = nodes.every(
      (n) => n.position != null && n.position.x != null && n.position.y != null,
    )

    let needsAutoSave = false
    let positionMap: Record<string, { x: number; y: number }>

    if (allHavePositions) {
      positionMap = {}
      for (const n of nodes) {
        positionMap[n.id] = { x: n.position!.x, y: n.position!.y }
      }
      if (activePlan) placeGroupsFromStoredPositions(activePlan, positionMap)
    } else {
      // Run dagre for the entire graph.
      needsAutoSave = true
      const g = new dagre.graphlib.Graph({ compound: true })
      g.setDefaultEdgeLabel(() => ({}))
      g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 80 })

      for (const n of nodes) {
        if (isHidden(n.id)) continue
        g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT })
      }

      if (activePlan) {
        for (const p of activePlan.presentationNodes) {
          g.setNode(p.id, { width: p.width, height: p.height })
        }
        // Group and task clusters (parents first), then each member's cluster.
        for (const c of activePlan.clusters) {
          g.setNode(c.id, {})
        }
        for (const c of activePlan.clusters) {
          if (c.parent) g.setParent(c.id, c.parent)
        }
        for (const [nodeId, clusterId] of Object.entries(activePlan.clusterOf)) {
          g.setParent(nodeId, clusterId)
        }
      }

      // Compound clusters: group each loop body so dagre keeps it in one column.
      // Innermost loop (largest depth) wins for nodes shared across loops.
      const clusterByNode: Record<string, { clusterId: string; depth: number }> = {}
      for (const loop of detection.loops) {
        const clusterId = `cluster-${loop.id}`
        g.setNode(clusterId, {})
        // A loop inside a branch body stays inside that body's task cluster.
        if (activePlan && graph) {
          const parent = ownerCluster(activePlan, graph, loop.headId)
          if (parent) g.setParent(clusterId, parent)
        }
        for (const id of loop.bodyNodeIds) {
          const existing = clusterByNode[id]
          if (!existing || loop.depth > existing.depth) {
            clusterByNode[id] = { clusterId, depth: loop.depth }
          }
        }
      }
      for (const [nodeId, { clusterId }] of Object.entries(clusterByNode)) {
        g.setParent(nodeId, clusterId)
      }

      // Seed only forward edges so back-edges do not distort rank assignment.
      for (const e of edges) {
        if (detection.backEdgeKeys.has(`${e.source} ${e.target}`)) continue
        g.setEdge(renderedSource(graph, e), renderedTarget(graph, e))
      }
      if (activePlan) {
        for (const pe of activePlan.presentationEdges) {
          g.setEdge(pe.source, pe.target, { weight: 0 })
        }
      }

      dagre.layout(g)

      positionMap = {}
      for (const n of nodes) {
        if (isHidden(n.id)) continue
        const dagreNode = g.node(n.id)
        positionMap[n.id] = {
          x: dagreNode.x - NODE_WIDTH / 2,
          y: dagreNode.y - NODE_HEIGHT / 2,
        }
      }
      if (activePlan) {
        for (const p of activePlan.presentationNodes) {
          const dn = g.node(p.id)
          positionMap[p.id] = { x: dn.x - p.width / 2, y: dn.y - p.height / 2 }
        }
        orderTaskCards(activePlan, positionMap)
      }
    }

    const contextData = (id: string): Record<string, unknown> => {
      const c = nodeContexts[id]
      if (!c) return {}
      return { instanceId: c.instanceId, independent: c.independent, hiddenDependency: c.hiddenDependency }
    }

    const rfNodes: Node<ArbitronNodeData>[] = nodes
      .filter((n) => !isHidden(n.id))
      .map((n) => ({
        id: n.id,
        type: 'arbitronNode',
        position: positionMap[n.id],
        data: {
          label: n.label || n.id,
          nodeType: n.type,
          executionState: executionStates[n.id] ?? 'pending',
          ...contextData(n.id),
        },
      }))

    const groupNodes: Node[] = []
    const groupContainers: Node<GroupContainerData>[] = []
    if (activePlan) {
      const byId = new Map(nodes.map((n) => [n.id, n]))
      const boxes = groupContainerBoxes(activePlan, positionMap)
      for (const gid of activePlan.groups) {
        const scope = activePlan.scopes[gid]
        const def = byId.get(gid)
        const live = groupLive[gid] ?? null
        const instanceId = nodeContexts[gid]?.instanceId ?? live?.instanceId ?? null
        const box = boxes[gid]
        if (box) {
          groupContainers.push({
            id: groupBoxId(gid),
            type: 'groupContainer',
            position: { x: box.x, y: box.y },
            width: box.width,
            height: box.height,
            data: { definitionNodeId: gid, label: groupContainerLabel(scope), depth: activePlan.depth[gid], form: scope.config.form },
            selectable: false,
            focusable: false,
            draggable: false,
            // Below loop containers (-10 + depth) and step nodes; nested groups above enclosing ones.
            zIndex: -20 + activePlan.depth[gid],
          })
        }
        const endpoint = (role: 'header' | 'footer'): GroupEndpointData => ({
          definitionNodeId: gid,
          role,
          label: def?.label || gid,
          form: scope.config.form,
          completionRule: scope.config.completionRule,
          unfinishedBehavior: scope.config.unfinishedBehavior,
          gatherMode: scope.config.gatherMode,
          taskKeys: scope.config.tasks.map((t) => t.key),
          instanceId,
          live,
        })
        for (const p of activePlan.presentationNodes) {
          if (p.groupNodeId !== gid) continue
          if (p.role === 'task') {
            const task = scope.config.tasks.find((t) => t.key === p.taskKey)
            const data: GroupTaskCardData = {
              definitionNodeId: gid,
              role: 'task',
              taskKey: p.taskKey ?? '',
              index: scope.config.tasks.findIndex((t) => t.key === p.taskKey),
              workflow: task?.workflow ?? '',
              instanceId,
              live: (p.taskKey && live?.tasks[p.taskKey]) || null,
            }
            groupNodes.push({ id: p.id, type: 'groupTaskCard', position: positionMap[p.id], data })
          } else {
            groupNodes.push({
              id: p.id,
              type: p.role === 'header' ? 'groupHeader' : 'groupFooter',
              position: positionMap[p.id],
              data: endpoint(p.role),
            })
          }
        }
      }
    }

    const containerNodes: Node<{ label: string; depth: number }>[] = []
    for (const loop of detection.loops) {
      const box = loopContainerBox(loop, positionMap)
      if (!box) continue
      containerNodes.push({
        id: `loop-container-${loop.id}`,
        type: 'loopContainer',
        position: { x: box.x, y: box.y },
        width: box.width,
        height: box.height,
        data: { label: loopContainerLabel(loop), depth: loop.depth },
        selectable: false,
        focusable: false,
        draggable: false,
        // Below step nodes; deeper loops sit slightly above shallower ones.
        zIndex: -10 + loop.depth,
      })
    }

    const loopByBackEdge = new Map<string, Loop>()
    for (const loop of detection.loops) {
      loopByBackEdge.set(`${loop.tailId} ${loop.headId}`, loop)
    }

    const boxByLoopId = new Map<string, ReturnType<typeof loopContainerBox>>()
    for (const loop of detection.loops) {
      boxByLoopId.set(loop.id, loopContainerBox(loop, positionMap))
    }

    const rfEdges: Edge[] = edges.map((e, i) => {
      const key = `${e.source} ${e.target}`
      const loop = loopByBackEdge.get(key)
      const isBack = detection.backEdgeKeys.has(key)

      // For a back-edge, pick the side to route on from where the loop body nodes
      // actually sit (they often bulge to one side; route on the open side so the
      // edge doesn't cut through them). Positions are known here.
      let data: Record<string, unknown> | undefined = loop
        ? { loop, box: boxByLoopId.get(loop.id) ?? undefined }
        : undefined
      if (isBack && loop) {
        const spineX = (positionMap[loop.tailId]?.x ?? 0) + NODE_WIDTH / 2
        const bodyLefts: number[] = []
        const bodyRights: number[] = []
        for (const id of loop.bodyNodeIds) {
          const px = positionMap[id]?.x
          if (px == null) continue
          bodyLefts.push(px)
          bodyRights.push(px + NODE_WIDTH)
        }
        if (bodyLefts.length > 0) {
          const side = chooseLoopSide({ spineX, bodyLefts, bodyRights })
          data = { loop, box: boxByLoopId.get(loop.id) ?? undefined, side }
        }
      }

      return {
        id: `e-${e.source}-${e.target}-${i}`,
        // Group endpoints: task edges leave the header; success/failure leave
        // the footer; edges into a group enter its header.
        source: renderedSource(graph, e),
        target: renderedTarget(graph, e),
        label: e.label ?? e.output ?? undefined,
        type: isBack ? 'loopBackEdge' : 'smoothstep',
        animated: executionStates[e.source] === 'running',
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
        style: { stroke: isBack ? '#7c8cf5' : '#444466' },
        labelStyle: { fill: '#888888', fontSize: 11 },
        data,
        zIndex: isBack ? 5 : undefined,
      }
    })

    // Presentation-only connectors (sink -> gather, header -> card -> gather).
    // They exist only in the rendered edge list — never in the manifest, which
    // would break closed bodies or run branches in the parent cursor.
    if (activePlan) {
      for (const pe of activePlan.presentationEdges) {
        rfEdges.push({
          id: pe.id,
          source: pe.source,
          target: pe.target,
          type: 'smoothstep',
          animated: false,
          selectable: false,
          focusable: false,
          markerEnd: { type: MarkerType.ArrowClosed, width: 12, height: 12 },
          style: PRESENTATION_EDGE_STYLE,
          data: { presentationOnly: true, kind: pe.kind, groupNodeId: pe.groupNodeId, taskKey: pe.taskKey },
        })
      }
    }

    return {
      rfNodes: [...groupContainers, ...containerNodes, ...rfNodes, ...groupNodes],
      rfEdges,
      needsAutoSave,
    }
  }, [nodes, edges, executionStates, groups, groupLive, nodeContexts])
}
```

- [x] Step 9: Run the integration tests plus every existing graph/route test (loop layouts, GraphView, loops) — they must all stay green.

```bash
cd dashboard && npx vitest run src/graph src/routes && npx tsc --noEmit
```
Expected:
```
 Test Files  … passed (no failures)
```
and in particular `src/graph/useGraphLayout.test.ts` (unchanged, 19 tests), `src/routes/GraphView.loops.test.tsx` and `src/graph/useGraphLayout.groups.test.ts` (18 tests) pass.

- [x] Step 10: Commit.

```bash
git add dashboard/src/graph/useGraphLayout.ts dashboard/src/graph/useGraphLayout.groups.test.ts
git commit -m "feat(dashboard): nested group boxes, header/footer endpoints and sink connectors in the graph layout" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 3 — group node components

- [x] Step 11: Write the failing component tests `dashboard/src/graph/nodes/GroupContainerNode.test.tsx` (same `Handle` stub idiom as `ArbitronNode.test.tsx:6-14`):

```tsx
import { render, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { GroupEndpointData, GroupLiveSummary, GroupLiveTask, GroupTaskCardData } from '../groupLayout'

// Handle needs the React Flow store; stub it to a marker so handles are assertable.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import GroupContainerNode, { GroupFooterNode, GroupHeaderNode, GroupTaskCardNode } from './GroupContainerNode'
import { nodeTypes } from './nodeTypes'

// React Flow passes NodeProps; only data/selected are read.
const headerProps = (data: GroupEndpointData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupHeaderNode>
const footerProps = (data: GroupEndpointData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupFooterNode>
const cardProps = (data: GroupTaskCardData) =>
  ({ data, selected: false }) as unknown as React.ComponentProps<typeof GroupTaskCardNode>

const endpoint = (over: Partial<GroupEndpointData> = {}): GroupEndpointData => ({
  definitionNodeId: 'g1',
  role: 'footer',
  label: 'Review in parallel',
  form: 'branches',
  completionRule: 'first_success',
  unfinishedBehavior: 'finish_independently',
  gatherMode: 'results_and_files',
  taskKeys: ['a', 'b', 'c'],
  instanceId: 'root-1',
  live: null,
  ...over,
})

const liveTask = (over: Partial<GroupLiveTask> = {}): GroupLiveTask => ({
  key: 't1',
  status: 'running',
  frozenStatus: '',
  childInstanceId: 'child-1',
  blocking: true,
  continuingIndependently: false,
  cancelRequested: false,
  redacted: false,
  winner: false,
  activeNodes: [],
  ...over,
})

const card = (over: Partial<GroupTaskCardData> = {}): GroupTaskCardData => ({
  definitionNodeId: 'w1',
  role: 'task',
  taskKey: 't1',
  index: 0,
  workflow: 'child-a',
  instanceId: 'root-1',
  live: null,
  ...over,
})

function renderIn(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>)
}

describe('nodeTypes registry', () => {
  it('registers the group presentation node types alongside the existing ones', () => {
    expect(Object.keys(nodeTypes).sort()).toEqual(
      ['arbitronNode', 'groupContainer', 'groupFooter', 'groupHeader', 'groupTaskCard', 'loopContainer'],
    )
  })
})

describe('GroupContainerNode', () => {
  it('renders the container label', () => {
    // @ts-expect-error — partial NodeProps is sufficient for this unit test.
    render(<GroupContainerNode data={{ definitionNodeId: 'g1', label: 'Parallel branches · all succeed', depth: 0, form: 'branches' }} />)
    expect(screen.getByText('Parallel branches · all succeed')).toBeInTheDocument()
  })
})

describe('GroupHeaderNode', () => {
  it('shows the fan-out label, task count and form, with one top target and one bottom source handle', () => {
    const { container } = renderIn(<GroupHeaderNode {...headerProps(endpoint({ role: 'header' }))} />)
    expect(screen.getByText('parallel group · fan-out')).toBeInTheDocument()
    expect(screen.getByText('Review in parallel')).toBeInTheDocument()
    expect(screen.getByText('3 tasks · branches')).toBeInTheDocument()
    const handles = [...container.querySelectorAll('[data-testid="handle"]')]
    expect(handles.map((h) => `${h.getAttribute('data-handle-type')}@${h.getAttribute('data-position')}`)).toEqual(['target@top', 'source@bottom'])
  })
})

describe('GroupFooterNode', () => {
  it('statically shows rule, unfinished behaviour and gather mode', () => {
    renderIn(<GroupFooterNode {...footerProps(endpoint())} />)
    expect(screen.getByText('gather · First success')).toBeInTheDocument()
    expect(screen.getByText('finish independently · results + files')).toBeInTheDocument()
  })

  it('shows live state, progress, winner and file/resolution activity', () => {
    const live: GroupLiveSummary = {
      instanceId: 'root-1',
      groupRunId: 'run-1',
      state: 'resolving_conflicts',
      progress: { total: 3, completed: 2, failed: 1, cancelled: 0, active: 0, notStarted: 0 },
      winner: 'b',
      activity: 'Resolving conflicts (attempt 2)',
      tasks: {},
    }
    renderIn(<GroupFooterNode {...footerProps(endpoint({ live }))} />)
    expect(screen.getByText('resolving conflicts')).toBeInTheDocument()
    expect(screen.getByText('3/3 done · 1 failed')).toBeInTheDocument()
    expect(screen.getByText('winner: b')).toBeInTheDocument()
    expect(screen.getByText('Resolving conflicts (attempt 2)')).toBeInTheDocument()
  })

  it('falls back to the first waiting reason when there is no activity', () => {
    const live: GroupLiveSummary = {
      instanceId: 'root-1', groupRunId: 'run-1', state: 'running',
      progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 },
      waitingReason: 'Waiting for tasks a, b', tasks: {},
    }
    renderIn(<GroupFooterNode {...footerProps(endpoint({ live }))} />)
    expect(screen.getByText('Waiting for tasks a, b')).toBeInTheDocument()
    expect(screen.getByText('0/2 done · 2 active')).toBeInTheDocument()
  })
})

describe('GroupTaskCardNode', () => {
  it('statically links the child workflow definition', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card())} />)
    expect(screen.getByText('task 1 · t1')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
  })

  it('live: links the child run, shows its status and active nodes', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ activeNodes: ['Build', 'Test'] }) }))} />)
    expect(screen.getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/instances/child-1')
    expect(screen.getByText('running')).toBeInTheDocument()
    expect(screen.getByText('active: Build, Test')).toBeInTheDocument()
  })

  it('distinguishes a non-blocking independent task and the winner', () => {
    const { container } = renderIn(
      <GroupTaskCardNode {...cardProps(card({ live: liveTask({ continuingIndependently: true, blocking: false, winner: true }) }))} />,
    )
    expect(screen.getByText('non-blocking')).toBeInTheDocument()
    expect(screen.getByText('winner')).toBeInTheDocument()
    expect(container.querySelector('[class*="cardIndependent"]')).not.toBeNull()
  })

  it('shows the frozen gathered status next to a different live status', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'completed', frozenStatus: 'unfinished' }) }))} />)
    expect(screen.getByText('completed')).toBeInTheDocument()
    expect(screen.getByText('gathered as unfinished')).toBeInTheDocument()
  })

  it('styles queued and cancelled tasks distinctly', () => {
    const queued = renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'queued', childInstanceId: null }) }))} />)
    expect(queued.container.querySelector('[class*="statusQueued"]')).not.toBeNull()
    queued.unmount()
    const cancelled = renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'cancelled', cancelRequested: true }) }))} />)
    expect(cancelled.container.querySelector('[class*="statusCancelled"]')).not.toBeNull()
    expect(screen.getByText('cancel requested')).toBeInTheDocument()
  })

  it('a redacted task shows a hidden dependency and no link or identifiers', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ redacted: true, childInstanceId: null }) }))} />)
    expect(screen.getByText('hidden dependency')).toBeInTheDocument()
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.queryByText('child-a')).toBeNull()
  })

  it('shows a launch error', () => {
    renderIn(<GroupTaskCardNode {...cardProps(card({ live: liveTask({ status: 'failed', childInstanceId: null, launchError: 'child workflow not found' }) }))} />)
    expect(screen.getByText('child workflow not found')).toBeInTheDocument()
  })
})
```

- [x] Step 12: Run it, confirm it fails.

```bash
cd dashboard && npx vitest run src/graph/nodes/GroupContainerNode.test.tsx
```
Expected:
```
 FAIL  src/graph/nodes/GroupContainerNode.test.tsx [ src/graph/nodes/GroupContainerNode.test.tsx ]
Error: Failed to resolve import "./GroupContainerNode" from "src/graph/nodes/GroupContainerNode.test.tsx". Does the file exist?
```

- [x] Step 13: Implement. Create `dashboard/src/graph/groupText.ts`:

```ts
import type { GroupProgress } from '../api/types'

const RULE_TEXT: Record<string, string> = {
  all_succeed: 'All succeed',
  first_success: 'First success',
}
const UNFINISHED_TEXT: Record<string, string> = {
  wait_for_remaining: 'wait for remaining',
  cancel_unfinished: 'cancel unfinished',
  finish_independently: 'finish independently',
}
const GATHER_TEXT: Record<string, string> = {
  results: 'results',
  results_and_files: 'results + files',
}

/** Human text for a completion rule ("All succeed" / "First success"). */
export function ruleText(rule: string): string {
  return RULE_TEXT[rule] ?? rule
}

/** Human text for an unfinished-task behaviour. */
export function unfinishedText(behavior: string): string {
  return UNFINISHED_TEXT[behavior] ?? behavior
}

/** Human text for a gather mode. */
export function gatherText(mode: string): string {
  return GATHER_TEXT[mode] ?? mode
}

/** "2/3 done · 1 active · 1 failed" from a group progress record. */
export function progressText(p: GroupProgress): string {
  const finished = p.completed + p.failed + p.cancelled
  const parts = [`${finished}/${p.total} done`]
  if (p.active > 0) parts.push(`${p.active} active`)
  if (p.notStarted > 0) parts.push(`${p.notStarted} not started`)
  if (p.failed > 0) parts.push(`${p.failed} failed`)
  return parts.join(' · ')
}

/** "resolving_conflicts" -> "resolving conflicts". */
export function stateText(state: string): string {
  return state.replace(/_/g, ' ')
}
```

Create `dashboard/src/graph/nodes/GroupContainerNode.tsx`:

```tsx
import { memo } from 'react'
import { Link } from 'react-router-dom'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type {
  GroupContainerData,
  GroupEndpointData,
  GroupLiveTask,
  GroupTaskCardData,
} from '../groupLayout'
import { gatherText, progressText, ruleText, stateText, unfinishedText } from '../groupText'
import styles from './GroupContainerNode.module.css'

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

function statusClass(status: string): string {
  return STATUS_CLASS[status] ?? styles.statusPending
}

type GroupContainerNodeType = Node<GroupContainerData, 'groupContainer'>

/** Non-interactive box around a group's header, bodies/task cards and footer. */
function GroupContainerNode({ data }: NodeProps<GroupContainerNodeType>) {
  return (
    <div className={`${styles.container} ${data.form === 'workflows' ? styles.containerWorkflows : ''}`}>
      <span className={styles.containerLabel}>{data.label}</span>
    </div>
  )
}

type GroupHeaderNodeType = Node<GroupEndpointData, 'groupHeader'>

/** Fan-out header: the group's entry point; task edges leave it. */
function GroupHeaderNodeImpl({ data, selected }: NodeProps<GroupHeaderNodeType>) {
  return (
    <div className={`${styles.endpoint} ${selected ? styles.selected : ''}`} data-testid={`group-header-${data.definitionNodeId}`}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>parallel group · fan-out</div>
      <div className={styles.title}>{data.label}</div>
      <div className={styles.meta}>
        {data.taskKeys.length} {data.taskKeys.length === 1 ? 'task' : 'tasks'} · {data.form}
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

type GroupFooterNodeType = Node<GroupEndpointData, 'groupFooter'>

/** Gather footer: rule, progress, winner and file/resolution activity; success/failure leave it. */
function GroupFooterNodeImpl({ data, selected }: NodeProps<GroupFooterNodeType>) {
  const live = data.live
  return (
    <div className={`${styles.endpoint} ${styles.footer} ${selected ? styles.selected : ''}`} data-testid={`group-footer-${data.definitionNodeId}`}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>gather · {ruleText(data.completionRule)}</div>
      <div className={styles.meta}>
        {unfinishedText(data.unfinishedBehavior)} · {gatherText(data.gatherMode)}
      </div>
      {live && (
        <div className={styles.liveRow}>
          <span className={`${styles.status} ${statusClass(live.state)}`}>{stateText(live.state)}</span>
          {live.progress && <span className={styles.meta}>{progressText(live.progress)}</span>}
        </div>
      )}
      {live?.winner && <div className={styles.winner}>winner: {live.winner}</div>}
      {live?.activity && <div className={styles.activity}>{live.activity}</div>}
      {!live?.activity && live?.waitingReason && <div className={styles.activity}>{live.waitingReason}</div>}
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

function TaskBadges({ live }: { live: GroupLiveTask }) {
  return (
    <div className={styles.badges}>
      {live.winner && <span className={`${styles.badge} ${styles.badgeWinner}`}>winner</span>}
      {live.continuingIndependently && <span className={`${styles.badge} ${styles.badgeIndependent}`}>non-blocking</span>}
      {live.cancelRequested && <span className={`${styles.badge} ${styles.badgeCancel}`}>cancel requested</span>}
    </div>
  )
}

type GroupTaskCardNodeType = Node<GroupTaskCardData, 'groupTaskCard'>

/**
 * One workflows-form task, in author order. Static views link the child
 * workflow definition; live views link the child run and show its active nodes.
 * A redacted task shows a generic hidden dependency and no identifiers.
 */
function GroupTaskCardNodeImpl({ data, selected }: NodeProps<GroupTaskCardNodeType>) {
  const live = data.live
  const hidden = live?.redacted === true
  const classes = [
    styles.card,
    live ? statusClass(live.status) : '',
    live?.continuingIndependently ? styles.cardIndependent : '',
    selected ? styles.selected : '',
  ].filter(Boolean).join(' ')
  return (
    <div className={classes} data-testid={`group-task-${data.definitionNodeId}-${data.taskKey}`}>
      <Handle type="target" position={Position.Top} />
      <div className={styles.typeLabel}>task {data.index + 1} · {data.taskKey}</div>
      {hidden ? (
        <div className={styles.hidden}>hidden dependency</div>
      ) : (
        <>
          <div className={styles.title}>
            {live?.childInstanceId ? (
              <Link to={`/instances/${live.childInstanceId}`} className={styles.link} onClick={(e) => e.stopPropagation()}>
                {data.workflow || 'child run'}
              </Link>
            ) : data.workflow ? (
              <Link to={`/workflows/${data.workflow}`} className={styles.link} onClick={(e) => e.stopPropagation()}>
                {data.workflow}
              </Link>
            ) : (
              data.taskKey
            )}
          </div>
          {live && (
            <div className={styles.liveRow}>
              <span className={`${styles.status} ${statusClass(live.status)}`}>{stateText(live.status)}</span>
              {live.frozenStatus && live.frozenStatus !== live.status && (
                <span className={styles.meta}>gathered as {live.frozenStatus}</span>
              )}
            </div>
          )}
          {live && live.activeNodes.length > 0 && (
            <div className={styles.activeNodes}>active: {live.activeNodes.join(', ')}</div>
          )}
          {live?.launchError && <div className={styles.error}>{live.launchError}</div>}
        </>
      )}
      {live && <TaskBadges live={live} />}
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}

export const GroupHeaderNode = memo(GroupHeaderNodeImpl)
export const GroupFooterNode = memo(GroupFooterNodeImpl)
export const GroupTaskCardNode = memo(GroupTaskCardNodeImpl)
export default memo(GroupContainerNode)
```

Create `dashboard/src/graph/nodes/GroupContainerNode.module.css`:

```css
.container {
  width: 100%;
  height: 100%;
  box-sizing: border-box;
  border: 1px solid rgba(46, 204, 113, 0.45);
  border-radius: var(--radius-lg, 8px);
  background: rgba(46, 204, 113, 0.04);
  pointer-events: none;
}

.containerWorkflows {
  border-style: dashed;
}

.containerLabel {
  position: absolute;
  top: -10px;
  left: 12px;
  padding: 0 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  color: var(--state-completed, #2ecc71);
  background: var(--bg-primary, #1a1a2e);
  pointer-events: none;
}

.endpoint,
.card {
  width: 200px;
  box-sizing: border-box;
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  border: 2px solid rgba(46, 204, 113, 0.6);
  padding: 6px 10px;
  cursor: pointer;
  overflow: hidden;
}

.endpoint {
  min-height: 80px;
}

.card {
  min-height: 96px;
  border-color: var(--border-color);
}

/* Read-only viewer: handles are anchors only. */
.endpoint :global(.react-flow__handle),
.card :global(.react-flow__handle) {
  opacity: 0;
  pointer-events: none;
  border: none;
  min-width: 0;
  min-height: 0;
}

.footer {
  border-style: double;
  border-width: 3px;
}

.selected {
  border-color: var(--accent) !important;
  box-shadow: 0 0 0 1px var(--accent);
}

.typeLabel {
  font-size: 9px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 1px;
  color: var(--state-completed, #2ecc71);
  margin-bottom: 2px;
}

.title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.meta {
  font-size: 11px;
  color: var(--text-muted);
}

.liveRow {
  display: flex;
  gap: 6px;
  align-items: center;
  margin-top: 2px;
}

.status {
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  padding: 0 4px;
  border-radius: var(--radius-sm);
}

.statusPending {
  color: var(--text-dim);
}

.statusQueued {
  color: var(--state-queued);
  border: 1px dashed var(--state-queued);
}

.statusRunning {
  color: var(--state-running);
  border-color: var(--state-running);
}

.statusWaiting {
  color: var(--state-waiting);
  border-color: var(--state-waiting);
}

.statusCompleted {
  color: var(--state-completed);
  border-color: var(--state-completed);
}

.statusFailed {
  color: var(--state-failed);
  border-color: var(--state-failed);
}

.statusCancelled {
  color: var(--state-cancelled);
  border-color: var(--state-cancelled);
  text-decoration: line-through;
}

.cardIndependent {
  border-style: dashed;
  opacity: 0.85;
}

.winner {
  font-size: 11px;
  font-weight: 600;
  color: var(--state-completed);
}

.activity {
  font-size: 11px;
  color: var(--state-waiting);
}

.activeNodes {
  font-size: 11px;
  color: var(--state-running);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.badges {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  margin-top: 2px;
}

.badge {
  font-size: 9px;
  font-weight: 700;
  text-transform: uppercase;
  padding: 0 4px;
  border-radius: var(--radius-sm);
  border: 1px solid currentColor;
}

.badgeWinner {
  color: var(--state-completed);
}

.badgeIndependent {
  color: var(--state-paused);
}

.badgeCancel {
  color: var(--state-cancelled);
}

.hidden {
  font-size: 12px;
  font-style: italic;
  color: var(--text-muted);
}

.link {
  color: var(--accent);
}

.error {
  font-size: 11px;
  color: var(--state-failed);
}
```

Replace the whole of `dashboard/src/graph/nodes/nodeTypes.ts` (lines 1-7) with:

```ts
import ArbitronNode from './ArbitronNode'
import LoopContainerNode from './LoopContainerNode'
import GroupContainerNode, { GroupFooterNode, GroupHeaderNode, GroupTaskCardNode } from './GroupContainerNode'

export const nodeTypes = {
  arbitronNode: ArbitronNode,
  loopContainer: LoopContainerNode,
  groupContainer: GroupContainerNode,
  groupHeader: GroupHeaderNode,
  groupFooter: GroupFooterNode,
  groupTaskCard: GroupTaskCardNode,
}
```

- [x] Step 14: Run the component tests, the full suite and the typecheck.

```bash
cd dashboard && npx vitest run src/graph/nodes/GroupContainerNode.test.tsx && npx tsc --noEmit && npx vitest run
```
Expected:
```
 Test Files  1 passed (1)
      Tests  13 passed (13)
...
 Test Files  … passed (no failures)
```
(`GraphView.test.tsx` mocks `@xyflow/react` without `Handle`; it never renders node components, so importing the new node module through `nodeTypes` is safe — the full run confirms it.)

- [x] Step 15: Commit.

```bash
git add dashboard/src/graph/groupText.ts dashboard/src/graph/nodes/GroupContainerNode.tsx dashboard/src/graph/nodes/GroupContainerNode.module.css dashboard/src/graph/nodes/nodeTypes.ts dashboard/src/graph/nodes/GroupContainerNode.test.tsx
git commit -m "feat(dashboard): group container, header, footer and task-card nodes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

## Execution record

- Observed all three prescribed red stages: missing planner, 15 failing group-layout integration tests (3 regression tests passed), and missing node components.
- Self-review, in order:
  1. Spec compliance: implemented this task's planner, hook integration and registered node components only. Real route data feeding is explicitly assigned to P4-T8/P4-T9; embedded bundle refresh is P4-T10. Those integration tasks remain required before shipping.
  2. Anti-green-theater: added a composed hook-to-registered-renderer test asserting DOM progress, child-run URL and active-node labels. Temporarily discarding the hook's live input made this test fail at the footer progress sink; restored the production input and confirmed green.
  3. Code quality: checked against neighboring ArbitronNode, LoopContainerNode and loopLayout patterns; retained immutable manifest edges, stable defaults and definition-id mapping.
- Final verification: planner 10/10; group-layout integration 18/18; component tests 14/14; graph/route suite 232/232 before component additions; TypeScript clean; final full dashboard suite 57 files / 614 tests passed. `git diff --check` clean.
