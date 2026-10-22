# Workflow Auto-Arrangement — Phase 3 Task 13: ExecutionView/GraphView uses shared renderer + independent layout polling

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.6/6.7) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** The instance execution view draws the run's snapshot through the shared `ArrangedGraph` using the revision-scoped layout it polls from `GET /api/v1/instances/{id}/layout`. Polling does not depend on the execution WebSocket. The view shows a truthful status panel with an instance Retry, follows the active node using payload node boxes, and never writes a layout.

**Architecture:**
- `ExecutionView` (default export) builds `useLayoutStatus(() => api.instances.getLayout(id), rev => api.instances.retryLayout(id, rev), id)`.
  - Until the first poll answers, it seeds from `instance.layout`.
  - It joins the view to the snapshot's `presentation.revision` so it never mixes revisions.
  - It renders `LayoutStatusPanel` and passes the joined view to `GraphView`.
- `GraphView` renders only `ArrangedGraph`, with `workflowDef` = the instance `WorkflowSnapshot` and `executionStates` from `buildExecutionStates`.
  - It feeds `useFollowActiveNode` with the payload-box centre that `ArrangedGraph` reports through `onAnchorPosition` (`nodeCenter`).
  - Its own `NODE_WIDTH`/`NODE_HEIGHT` centring and its local ReactFlow are deleted.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters, relative imports only), React 19, react-router-dom 7, @xyflow/react 12, Vitest 4 + jsdom + Testing Library (fake timers for polling).

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` (§2 instance layout keyed by the snapshot's saved token, legacy tokenless fallback, instance retry; §4 runtime styles never resize nodes; §5 independent polling, readable zoom, payload-dimension follow/callouts, out-of-order guard, availability errors keep geometry) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS, §5 Addenda 5–7.

**Depends on (all must be merged first):**
- P3T1: `types.ts` (`LayoutView`, `RetryAck`, `Geometry`), `validate.ts` (`resolveDisplayGeometry`), `testFixtures.ts`, `api.instances.getLayout/retryLayout`, and `Instance.layout`.
- P3T2: `ArrangedNode`/`RoutedEdge` and their DOM contract.
- P3T3: `ArrangedGraph` (with ★`onAnchorPosition`), `nodeCenter`, `buildArrangedFlow`, `fallbackPositions`, and `useGraphLayout` without `needsAutoSave`.
- P3T11: `useLayoutStatus`, `POLL_INTERVAL_MS`, and `LayoutStatusPanel` (with ★`displayIssues`).
- Server side: P3T9 (`GET /instances/{id}` embeds `layout`; `GET /instances/{id}/layout`; `POST /instances/{id}/layout/retry`).

**Decisions recorded here:**
1. **Retry permission (`canRetry`).** The dashboard has no client-side intervention predicate to reuse. `InstanceControls` (`dashboard/src/components/InstanceControls.tsx`) shows Pause/Cancel/Retry/Re-run to every viewer, and the server's `Engine.authorizeIntervention` decides (403).
   - A client predicate cannot be built correctly anyway: the dashboard `Instance` has no `department`, so department leads could not be recognised.
   - ExecutionView therefore follows the existing precedent: `canRetry` is `true`, and the server's `Engine.AuthorizeInstanceLayoutRetry` (P3T8, which shares `authorizeIntervention`) is the authority.
   - A 403 is shown in-band as `Retry failed: API 403: …`, and the graph is kept. This is tested.
2. **`useFollowActiveNode.ts` needs a focused correction.** Its current early return for !isRunning prevents completed-instance Recenter from ever learning the anchor. Add RED tests first, update its remembered anchor for any valid position, and gate only automatic animation on isRunning/following. P3T13 also changes what is fed into it: the payload-box centre from `ArrangedGraph`'s `onAnchorPosition`, instead of `position + NODE_WIDTH/2, NODE_HEIGHT/2`.
3. **Callouts.** `CalloutConnector` anchors to the rendered node's DOM rect. Its flow-coordinate fallback reads `node.measured?.width ?? node.width`. `buildArrangedFlow` sets `width`/`height` to the payload box, so that math can use payload dimensions once GraphView renders arranged nodes. Its global DOM lookup must be scoped/escaped if the two-graph regression exposes the wrong node; this focused correction belongs here. Task 13.4 adds the provenance test that pins this (`CalloutConnector.arranged.test.tsx`, a new test file not listed in contracts §0).
4. **Revision join.** A layout view whose `savedRevision` differs from the snapshot's `presentation.revision` (`''` for legacy snapshots) is never drawn. It surfaces as a status error, and Retry then reloads instead of posting a foreign revision.

**Interfaces (consumed — restated so this file stands alone):**
```ts
// dashboard/src/graph/arranged/types.ts (P3T1)
export interface LayoutView { savedRevision: string; jobGeneration: number; resultGeneration: number; state?: string; displayStatus: DisplayStatus; stage?: string; attemptsUsed: number; attemptLimit: number; findings: LayoutFinding[] | null; failureStage?: string; failureReason?: string; evidence?: LayoutEvidence; contract?: RendererContract; geometry?: Geometry; retained?: RetainedLayout; provenance: Provenance; unreviewed: boolean; retryEligible: boolean; available: boolean; availabilityError?: string }
export interface RetryAck { savedRevision: string; jobGeneration: number; state: string; displayStatus: DisplayStatus }
export interface Geometry { format: string; contract: RendererContract; options: Record<string, number>; nodes: NodeBox[]; edges: EdgeRoute[]; crossings: Crossing[]; canvas: Rect }
// dashboard/src/graph/arranged/validate.ts (P3T1)
export function resolveDisplayGeometry(def: WorkflowDefinition, view: LayoutView | null): { geometry: Geometry | null; issues: string[] }
// dashboard/src/graph/arranged/testFixtures.ts (P3T1, test-only)
export function fixtureWorkflow(): WorkflowDefinition // a(script, entrypoint) → b(llm_call) → c(conditional), loop return c → b ("again"); presentation.revision "rev-1"
export function fixtureGeometry(): Geometry           // a {40,40,180,52}; b {40,140,200,70}; c {40,260,180,52}; edge 2 loop_return points (220,286)(268,286)(268,175)(240,175), label "again" at {274,220,44,21}
export function fixtureView(o?: Partial<LayoutView>): LayoutView // savedRevision "rev-1", jobGeneration 1, resultGeneration 1, state passed, displayStatus complete, geometry fixtureGeometry(), provenance arranged, retryEligible false, available true
// dashboard/src/api/client.ts (P3T1)
api.instances.getLayout(id: string): Promise<LayoutView>                          // GET  /api/v1/instances/{id}/layout
api.instances.retryLayout(id: string, expectedRevision: string): Promise<RetryAck> // POST /api/v1/instances/{id}/layout/retry {"expectedRevision": "..."}
// dashboard/src/api/types.ts (P3T1): interface Instance { …; layout?: LayoutView }
// dashboard/src/graph/types.ts (P3T1): interface WorkflowDefinition { …; presentation?: Presentation }  // Presentation.revision
// dashboard/src/graph/arranged/ArrangedGraph.tsx (P3T3) — must be inside <ReactFlowProvider>
export interface ArrangedGraphProps { workflowDef: WorkflowDefinition; view: LayoutView | null; executionStates?: Record<string, NodeExecutionState>; selectedNodeId: string | null; selectedNodeIds?: ReadonlySet<string>; onNodeClick: (nodeId: string) => void; anchorNodeId: string | null; onMoveStart?: (event: unknown) => void; onAnchorPosition?: (center: { x: number; y: number } | null) => void; onDisplayIssues?: (issues: readonly string[]) => void; fixedViewport?: { x: number; y: number; zoom: number }; children?: React.ReactNode }
export default function ArrangedGraph(props: ArrangedGraphProps): JSX.Element
// Behaviour relied on: nodesDraggable={false}; no fitView prop; centres once per displayed layout
// ("fallback" | "arranged:<rev>:<resultGeneration>") via setCenter(cx, cy, { zoom: 0.9 });
// onAnchorPosition reports nodeCenter(arranged ? view : null, nodes, anchorNodeId);
// root <div data-arranged-canvas="true" data-layout-mode="arranged"|"fallback">; arranged nodes type 'arrangedNode' with width/height = box.w/box.h; fallback nodes 'arbitronNode'.
// dashboard/src/graph/arranged/buildArrangedFlow.ts (P3T3)
export function buildArrangedFlow(geo: Geometry, nodeTypesById: Record<string, string>, executionStates: Record<string, NodeExecutionState>, graphInstanceId: string, activeSources?: Set<string>): { nodes: Node<ArrangedNodeData>[]; edges: Edge<RoutedEdgeData>[] }
// dashboard/src/hooks/useLayoutStatus.ts (P3T11)
export interface UseLayoutStatus { view: LayoutView | null; error: string | null; loading: boolean; refresh: () => void; retry: () => Promise<void>; retrying: boolean; retryError: string | null }
export function useLayoutStatus(fetcher: () => Promise<LayoutView>, retrier: (expectedRevision: string) => Promise<RetryAck>, key: string): UseLayoutStatus
export const POLL_INTERVAL_MS = 2000 // back-off after errors: 2s, 4s, 8s … capped 30s; refetch on window focus
// dashboard/src/graph/arranged/LayoutStatusPanel.tsx (P3T11)
export interface LayoutStatusPanelProps { view: LayoutView | null; error: string | null; onRetry: () => void; retrying: boolean; retryError: string | null; canRetry: boolean; displayIssues?: readonly string[] }
// Titles: pending "Arrangement pending", reviewing "Reviewing readability", complete "Layout reviewed", needs_attention "Layout needs attention",
// no view + error "Arrangement status unavailable"; legacy renders nothing; Retry button "Retry arrangement" when canRetry && (retryEligible || error);
// lines "Could not refresh arrangement status: <error>", "Retry failed: <retryError>", "This layout could not be displayed: <issue>".
// existing
export function useFollowActiveNode(anchor: { x: number; y: number } | null, isRunning: boolean): { following: boolean; onMoveStart: (event: unknown) => void; recenter: () => void } // graph/useFollowActiveNode.ts (terminal recenter correction owned here)
export function selectAnchorNodeId(instance, workflowDef, executionStates, trace): string | null // graph/selectAnchorNodeId.ts
```

**Interfaces (produced):**
```ts
// dashboard/src/routes/ExecutionView.tsx
export function GraphView(props: {
  instance: Instance | null
  workflowDef: WorkflowDefinition | null   // the instance WorkflowSnapshot, never the current workflow
  trace: NodeExecution[]
  onDisplayIssues?: (issues: readonly string[]) => void
  layoutView: LayoutView | null            // ★ new prop: revision-joined instance layout view
  onNodeClick: (nodeId: string) => void
  selectedNodeId: string | null
}): JSX.Element
export default function ExecutionView(): JSX.Element // composition root for the instance layout wiring
```

---

### Task 13.1: `GraphView` renders only through `ArrangedGraph` with payload-dimension follow

**Files:**
- Modify: `dashboard/src/routes/ExecutionView.tsx:1-24` (imports), `:70-170` (`GraphView` replaced), `:357-363` (the `<GraphView>` call gains `layoutView`)
- Modify (rewrite): `dashboard/src/routes/GraphView.test.tsx:1-519`

- [x] **Step 1: Write the failing test.** After render/rerender, await the font gate with waitFor arranged DOM before box/centering assertions, as the arranged examples below do. Import the font fake shown below; add rejection/deferred/stale epoch cases. Legacy fallback tests stay synchronous. Keep rejected/deferred fonts controlled separately, and legacy fallback assertions synchronous:

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { LayoutView } from '../graph/arranged/types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'

// ---------------------------------------------------------------------------
// Mock only the React Flow canvas (everything else is the real module). The mock
// renders each node as a keyed DOM element so we can assert a node is NOT
// remounted across an unrelated re-render (the original flicker bug), and it
// records the props the shared renderer (ArrangedGraph) hands to <ReactFlow>.
// ---------------------------------------------------------------------------

const renderCount = { value: 0 }
const setCenterSpy = vi.fn()
let lastReactFlowProps: (Record<string, unknown> & { nodes: Node[] }) | null = null

vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    ReactFlow: (
      props: Record<string, unknown> & {
        nodes: Node[]
        children?: React.ReactNode
        onMoveStart?: (event: unknown) => void
      },
    ) => {
      renderCount.value += 1
      lastReactFlowProps = props
      return (
        <div>
          <button
            data-testid="rf-fire-user-move"
            onClick={() => props.onMoveStart?.({ type: 'pointerdown' })}
          />
          {props.nodes.map((n) => (
            <div key={n.id} data-testid={`node-${n.id}`} />
          ))}
          {props.children}
        </div>
      )
    },
    ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
    Background: () => null,
    Controls: () => null,
    useReactFlow: () => ({
      flowToScreenPosition: (p: unknown) => p,
      setCenter: setCenterSpy,
      fitView: () => {},
      getZoom: () => 1,
    }),
  }
})

// Controllable unit font boundary. The real asset/face/coverage producer is exercised
// by P3T5/P3T14. Success requires nonempty actual text and the exact bundled contract;
// tests replace this method with a rejection/deferred promise for font sink cases.
const fontBoundary = vi.hoisted(() => ({ load: vi.fn(async (_doc: Document, texts: readonly string[], contract: {version: string; fontFamily: string}) => {
  if (!texts.length || !texts.every(t => typeof t === 'string') || contract.version !== 'arranged-renderer/1' || contract.fontFamily !== 'Inter, Noto Sans SC, Noto Emoji') throw new Error('invalid font request')
  return { fontsReady: true, fontFamilyResolved: true }
}) }))
vi.mock('../graph/arranged/fonts', () => ({ ensureBundledTextFonts: fontBoundary.load }))

// Import after the mock is registered.
import { GraphView } from './ExecutionView'

// Stable, module-level props so re-renders pass identical references.
const workflowDef: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
  ],
  edges: [{ source: 'a', target: 'b' }],
}

const instance = {
  currentNodeId: 'a',
  state: 'running',
} as unknown as Instance

// Loop workflow: b↔c are in a loop body; a is outside.
const loopWorkflowDef: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
    { id: 'c', type: 'llm_call', label: 'C', description: '', config: {} },
  ],
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'b', label: 'retry' },
  ],
}

const trace: NodeExecution[] = []

// Arranged-mode fixtures (P3T1): a → b → c with loop return c → b, revision rev-1.
const arrangedDef = fixtureWorkflow()
const running = (id: string) => ({ currentNodeId: id, state: 'running' }) as unknown as Instance

/** The fixture geometry with variable node sizes: a tall b and a wide, tall c. */
function tallView(): LayoutView {
  const geo = fixtureGeometryWithBoxes({
    b: { x: 40, y: 140, w: 200, h: 200 }, // centre (140, 240)
    c: { x: 40, y: 400, w: 320, h: 160 }, // centre (200, 480)
  })
  return fixtureView({ geometry: geo })
}
const TALL = tallView()

beforeEach(() => {
  renderCount.value = 0
  setCenterSpy.mockClear()
  lastReactFlowProps = null
})

// ---------------------------------------------------------------------------
// Seam: anchor selection → follow hook (Paired Review #19), legacy fallback mode.
// ---------------------------------------------------------------------------

describe('GraphView — follow animation on instance advance', () => {
  it('calls setCenter with the new anchor position when currentNodeId advances while following', async () => {
    function Harness() {
      const [inst, setInst] = useState({ currentNodeId: 'a', state: 'running' } as unknown as Instance)
      return (
        <>
          <button onClick={() => setInst({ currentNodeId: 'b', state: 'running' } as unknown as Instance)}>
            advance
          </button>
          <GraphView
            instance={inst}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    // Mount centering fires once on 'a'.
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    setCenterSpy.mockClear()

    await userEvent.click(screen.getByRole('button', { name: 'advance' }))

    // Fallback nodes carry no explicit size, so nodeCenter uses NODE_WIDTH/NODE_HEIGHT (200 x 80).
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    await waitFor(() => {
      expect(setCenterSpy).toHaveBeenCalledWith(
        bNode.position.x + 100,
        bNode.position.y + 40,
        expect.objectContaining({ duration: expect.any(Number) }),
      )
    })
  })
})

// ---------------------------------------------------------------------------
// Flicker guard (observable behavior): an unrelated parent re-render must NOT
// remount the graph nodes.
// ---------------------------------------------------------------------------

describe('GraphView — no-remount on unrelated re-render', () => {
  it('keeps node DOM element identity across an unrelated re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick {tick}</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    const before = screen.getByTestId('node-a')
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))
    const after = screen.getByTestId('node-a')
    expect(after).toBe(before)
  })

  it('still renders all workflow nodes after the re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick {tick}</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))

    expect(screen.getByTestId('node-a')).toBeTruthy()
    expect(screen.getByTestId('node-b')).toBeTruthy()
  })
})

describe('GraphView — default-zoom centering on mount (fallback mode)', () => {
  it('centers on the active node at DEFAULT_ZOOM=0.9 on mount', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position.x + 200 / 2, // NODE_WIDTH / 2
      aNode.position.y + 80 / 2, // NODE_HEIGHT / 2
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  it('does not call setCenter when there is no anchor (empty graph)', () => {
    const emptyDef = { ...workflowDef, nodes: [], edges: [] }
    render(
      <GraphView
        instance={instance}
        workflowDef={emptyDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).not.toHaveBeenCalled()
  })

  it('centers on the final node for a completed instance', () => {
    const completedInstance = { ...instance, state: 'completed', currentNodeId: undefined } as unknown as Instance
    const completedTrace: NodeExecution[] = [
      { id: 'e-a', instanceId: 'i', nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:02Z', completedAt: '2026-01-01T00:00:03Z' } as NodeExecution,
    ]
    render(
      <GraphView
        instance={completedInstance}
        workflowDef={workflowDef}
        trace={completedTrace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the failed node for a failed instance', () => {
    const failedInstance = { ...instance, state: 'failed', currentNodeId: undefined, failedNodeId: 'b' } as unknown as Instance
    const failedTrace: NodeExecution[] = [
      { id: 'e-a', instanceId: 'i', nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'failed', startedAt: '2026-01-01T00:00:02Z', completedAt: '2026-01-01T00:00:03Z' } as NodeExecution,
    ]
    render(
      <GraphView
        instance={failedInstance}
        workflowDef={workflowDef}
        trace={failedTrace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('falls back to centering on the source node when currentNodeId is not in the graph', () => {
    const ghostInstance = { currentNodeId: 'ghost-not-in-graph', state: 'running' } as unknown as Instance
    render(
      <GraphView
        instance={ghostInstance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the entry node for a static workflow preview (no instance)', () => {
    const previewDef: typeof workflowDef = {
      ...workflowDef,
      nodes: [
        { id: 'a', type: 'llm_call', label: 'A', description: '', config: {}, entrypoint: true },
        { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
      ],
    }
    render(
      <GraphView
        instance={null}
        workflowDef={previewDef}
        trace={[]}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the active step node, not the loop container, for a running workflow with a loop', () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={loopWorkflowDef}
        trace={[]}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(lastReactFlowProps!.nodes.some((n) => n.type === 'loopContainer')).toBe(true)
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })
})

describe('GraphView — Recenter / Follow control', () => {
  it('renders the control showing the following state by default', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to paused state after a user move and back after Recenter', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(btn)
    expect(screen.getByRole('button', { name: /following|recenter|follow/i }).getAttribute('aria-pressed')).toBe('true')
  })

  it('re-centers on the active node at DEFAULT_ZOOM when Recenter is clicked', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()
    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, { zoom: 0.9 })
  })
})

describe('GraphView — mount centering one-shot guard', () => {
  it('calls setCenter exactly once even after an unrelated parent re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
  })
})

// ---------------------------------------------------------------------------
// Arranged mode: the instance layout view drives positions, sizes, centring
// and follow (payload dimensions — never NODE_WIDTH/NODE_HEIGHT guesses).
// ---------------------------------------------------------------------------

describe('GraphView — arranged instance layout (shared renderer)', () => {
  it('hands React Flow the payload boxes of the instance layout, not dagre, and never drags or auto-fits', async () => {
    render(
      <GraphView
        instance={running('a')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView()}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    const nodes = lastReactFlowProps!.nodes
    expect(nodes.map((n) => n.type)).toEqual(['arrangedNode', 'arrangedNode', 'arrangedNode'])
    const b = nodes.find((n) => n.id === 'b')!
    expect(b.position).toEqual({ x: 40, y: 140 })
    expect([b.width, b.height]).toEqual([200, 70])
    expect(lastReactFlowProps!.nodesDraggable).toBe(false)
    expect(lastReactFlowProps!.fitView).toBeUndefined()
  })

  it('centres a running tall node on the centre of its payload box (not position + NODE_HEIGHT/2)', async () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={TALL}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    expect(setCenterSpy).toHaveBeenCalledWith(140, 240, { zoom: 0.9 })
    expect(setCenterSpy).not.toHaveBeenCalledWith(140, 180, expect.anything())
  })

  it('follows to the payload-box centre of the next active node', async () => {
    function Harness() {
      const [inst, setInst] = useState(running('a'))
      return (
        <>
          <button onClick={() => setInst(running('c'))}>advance</button>
          <GraphView
            instance={inst}
            workflowDef={arrangedDef}
            trace={trace}
            layoutView={TALL}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }
    render(<Harness />)
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    // Mount centre on a: box {40,40,180,52} → (130, 66).
    expect(setCenterSpy).toHaveBeenCalledWith(130, 66, { zoom: 0.9 })
    setCenterSpy.mockClear()
    await userEvent.click(screen.getByRole('button', { name: 'advance' }))
    // c: box {40,400,320,160} → (200, 480); NODE_WIDTH/NODE_HEIGHT would give (140, 440).
    await waitFor(() => {
      expect(setCenterSpy).toHaveBeenCalledWith(200, 480, expect.objectContaining({ duration: expect.any(Number) }))
    })
  })

  it('Recenter returns to the tall node payload centre at DEFAULT_ZOOM', async () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={TALL}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()
    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))
    expect(setCenterSpy).toHaveBeenCalledWith(140, 240, { zoom: 0.9 })
  })

  it('running-state styling does not change the node box handed to the renderer', async () => {
    const view = fixtureView()
    const { rerender } = render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    const runningB = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect((runningB.data as { executionState: string }).executionState).toBe('running')
    expect([runningB.width, runningB.height]).toEqual([200, 70])

    const done = { state: 'completed' } as unknown as Instance
    const doneTrace = [
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
    ]
    rerender(
      <GraphView
        instance={done}
        workflowDef={arrangedDef}
        trace={doneTrace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const completedB = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect((completedB.data as { executionState: string }).executionState).toBe('completed')
    expect([completedB.width, completedB.height]).toEqual([200, 70])
    expect(completedB.position).toEqual(runningB.position)
  })

  it("falls back to the snapshot revision's retained positions while the layout has no geometry", () => {
    const view = fixtureView({
      state: 'in_progress',
      displayStatus: 'arranging',
      geometry: undefined,
      provenance: 'retained_positions',
      retained: { source: 'layout', positions: { a: { x: 11, y: 22 }, b: { x: 11, y: 222 }, c: { x: 11, y: 422 } } },
    })
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const b = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(b.type).toBe('arbitronNode')
    expect(b.position).toEqual({ x: 11, y: 222 })
    expect(setCenterSpy).toHaveBeenCalledWith(111, 262, { zoom: 0.9 })
  })

  it('re-centres once on the payload box when the arranged result replaces the fallback', async () => {
    const completed = { state: 'completed' } as unknown as Instance
    const { rerender } = render(
      <GraphView
        instance={completed}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    rerender(
      <GraphView
        instance={completed}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView()}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    // Completed run with no trace and no sink (c → b loops) anchors on the source node a: (130, 66).
    expect(setCenterSpy).toHaveBeenCalledTimes(2)
    expect(setCenterSpy).toHaveBeenLastCalledWith(130, 66, { zoom: 0.9 })
  })

  it('does not draw a geometry whose renderer contract version this build cannot draw', () => {
    const geo = fixtureGeometry()
    geo.contract = { ...geo.contract, version: 'arranged-renderer/2' }
    render(
      <GraphView
        instance={running('a')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView({ geometry: geo })}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(lastReactFlowProps!.nodes.some((n) => n.type === 'arrangedNode')).toBe(false)
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

Execution: 7 behavioral failures, 15 passes against the original GraphView (`/tmp/p3t13-1-red.log`).

```bash
cd dashboard && npx vitest run src/routes/GraphView.test.tsx
```
Expected: the current `GraphView` ignores `layoutView` and draws dagre nodes with its own one-shot centring:
```text
 × GraphView — arranged instance layout (shared renderer) > hands React Flow the payload boxes of the instance layout, not dagre, and never drags or auto-fits
   AssertionError: expected [ 'arbitronNode', 'arbitronNode', … ] to deeply equal [ 'arrangedNode', 'arrangedNode', 'arrangedNode' ]
 × … centres a running tall node on the centre of its payload box (not position + NODE_HEIGHT/2)
 × … follows to the payload-box centre of the next active node
 × … Recenter returns to the tall node payload centre at DEFAULT_ZOOM
 × … running-state styling does not change the node box handed to the renderer
 × … falls back to the snapshot revision's retained positions while the layout has no geometry
 × … re-centres once on the payload box when the arranged result replaces the fallback
      Tests  7 failed | 15 passed (22)
```

- [x] **Step 3: Implement.** In `dashboard/src/routes/ExecutionView.tsx`, replace lines 1-24 (the import block) with:

```tsx
import { useState, useEffect, useCallback, useMemo } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { Instance, NodeExecution, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from '../graph/types'
import type { LayoutView } from '../graph/arranged/types'
import { api } from '../api/client'
import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'
import { useFollowActiveNode } from '../graph/useFollowActiveNode'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import { useInstanceWebSocket } from '../hooks/useInstanceWebSocket'
import { useRefetchOnReconnect } from '../hooks/useRefetchOnReconnect'
import { useInteractiveSession } from '../hooks/useInteractiveSession'
import CalloutConnector from '../graph/CalloutConnector'
import StateBadge from '../components/StateBadge'
import InstanceControls from '../components/InstanceControls'
import ResizablePanel from '../components/ResizablePanel'
import SidePanel from '../components/SidePanel'
import InteractivePanel from '../components/InteractivePanel'
import styles from './ExecutionView.module.css'
```

Replace lines 70-170 (the whole `GraphView` function) with:

```tsx
/** Stable empty definition so the shared renderer always mounts (before the snapshot loads). */
const EMPTY_WORKFLOW: WorkflowDefinition = { name: '', description: '', author: '', nodes: [], edges: [] }

/**
 * The instance graph. Renders ONLY through the shared ArrangedGraph:
 * - workflowDef is the instance's WorkflowSnapshot (never the current workflow);
 * - layoutView is the revision-joined instance layout (reviewed geometry, or the
 *   snapshot revision's retained fallback, or dagre);
 * - executionStates come from buildExecutionStates; runtime styling never
 *   changes the payload node boxes (ArrangedNode, P3T2).
 * Follow-active-node is fed with the anchor centre ArrangedGraph computes from
 * the payload box (nodeCenter), so variable node sizes centre correctly.
 * Nodes are never draggable and no layout is ever written.
 */
export function GraphView({
  instance,
  workflowDef,
  trace,
  layoutView,
  onDisplayIssues,
  onNodeClick,
  selectedNodeId,
}: {
  instance: Instance | null
  workflowDef: WorkflowDefinition | null
  trace: NodeExecution[]
  layoutView: LayoutView | null
  onDisplayIssues?: (issues: readonly string[]) => void
  onNodeClick: (nodeId: string) => void
  selectedNodeId: string | null
}) {
  // Memoize so identity only changes when the inputs actually change. Without
  // this, every re-render (e.g. each live WebSocket event) produces a fresh
  // executionStates object, defeating the renderer's memo — the layout is
  // rebuilt and ReactFlow re-renders every node, which reads as "flicker".
  const executionStates = useMemo(
    () => buildExecutionStates(instance, trace, workflowDef),
    [instance, trace, workflowDef],
  )

  // The real step node the viewport should center on for this instance state.
  const anchorId = useMemo(
    () => selectAnchorNodeId(instance, workflowDef, executionStates, trace),
    [instance, workflowDef, executionStates, trace],
  )

  // Anchor centre in flow coordinates, reported by ArrangedGraph (nodeCenter):
  // the reviewed payload box when geometry is drawn, the fallback node box
  // otherwise. ArrangedGraph also does the one-shot initial centring at
  // DEFAULT_ZOOM per displayed layout; this hook only follows on advance.
  const [anchorPos, setAnchorPos] = useState<{ x: number; y: number } | null>(null)

  const isRunning = instance?.state === 'running' || instance?.state === 'waiting'
  const { following, onMoveStart, recenter } = useFollowActiveNode(anchorPos, isRunning)

  return (
    <div className={styles.graphContainer}>
      <ArrangedGraph
        workflowDef={workflowDef ?? EMPTY_WORKFLOW}
        view={layoutView}
        onDisplayIssues={onDisplayIssues}
        executionStates={executionStates}
        selectedNodeId={selectedNodeId}
        onNodeClick={onNodeClick}
        anchorNodeId={anchorId}
        onMoveStart={onMoveStart}
        onAnchorPosition={setAnchorPos}
      >
        <button
          type="button"
          className={`${styles.recenterButton} ${following ? styles.recenterButtonActive : ''}`}
          aria-pressed={following}
          title={following ? 'Following the active step' : 'Recenter and follow the active step'}
          onClick={recenter}
        >
          {following ? 'Following' : 'Recenter'}
        </button>
      </ArrangedGraph>
    </div>
  )
}
```

In the default export's `<GraphView … />` call (lines 357-363), add the layout prop. Task 13.3 replaces this with the polled view. Until then the instance read's embedded `layout` (P3T9 `instanceResponse.Layout`) is the real producer:

```tsx
            <GraphView
              instance={instance}
              workflowDef={workflowDef}
              trace={trace}
              layoutView={instance.layout ?? null}
              onNodeClick={handleNodeClick}
              selectedNodeId={selectedNodeId}
            />
```

`useRef`, `ReactFlow`, `Background`, `Controls`, `useReactFlow`, `useGraphLayout`, `NODE_WIDTH`, `NODE_HEIGHT`, `DEFAULT_ZOOM`, `nodeTypes` and `edgeTypes` are no longer imported. The old `anchorPos`/`didInitialCenter` code that used them is deleted with the old `GraphView`.

- [x] **Step 4: Run it and confirm it passes, plus the type check**

Execution: 24 tests pass (including deferred/rejected/stale fonts), TypeScript passes; fixed-size/local-canvas grep is empty. The existing loops test gained its required null layout prop before its planned rewrite.

```bash
cd dashboard && npx vitest run src/routes/GraphView.test.tsx && npx tsc --noEmit && grep -n 'NODE_WIDTH\|NODE_HEIGHT\|useGraphLayout\|<ReactFlow\b' src/routes/ExecutionView.tsx
```
Expected:
```text
 Test Files  1 passed (1)
      Tests  22 passed (22)
(tsc exits 0; grep prints nothing — GraphView no longer has its own ReactFlow or fixed node sizes)
```

- [x] **Step 5: Commit**

Ordered review: the shared renderer alone draws the graph; real embedded instance.layout feeds it, tests assert payload boxes/centres and reject absent data/fonts; existing memoization and graph patterns are preserved.

```bash
git add dashboard/src/routes/ExecutionView.tsx dashboard/src/routes/GraphView.test.tsx && git commit -m "feat(dashboard): render the execution graph through the shared arranged renderer" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 13.2: `GraphView.loops.test.tsx` moves to the routed-edge DOM contract (real React Flow)

The old test captured xyflow props, expecting `loopContainer` nodes and `loopBackEdge` edges. In arranged mode those no longer exist: loop returns are `RoutedEdge` paths with `data-edge-kind="loop_return"`, drawn dashed in the contract loop colour. This test mounts the **real** React Flow (no module mock), so the assertions are on the DOM the user sees. It covers:
- arranged loop returns, which are distinct, keep their arrows, and keep node boxes in running state;
- the legacy fallback, where loop containers and `loopBackEdge` still appear when there is no geometry.

jsdom has no layout engine. `ResizeObserver`, `DOMMatrixReadOnly`, `offsetWidth/offsetHeight` and `SVGElement.getBBox` are stubbed exactly as in P3T3's `ArrangedGraph.dom.test.tsx`, which is just enough for React Flow to measure nodes and mount edges.

**Files:**
- Modify (rewrite): `dashboard/src/routes/GraphView.loops.test.tsx:1-188`

- [x] **Step 1: Rewrite the test.** Replace the whole of `dashboard/src/routes/GraphView.loops.test.tsx` with:

```tsx
// Real @xyflow/react (no module mock): asserts the DOM that GraphView actually
// renders through the shared ArrangedGraph. Arranged mode must draw loop
// returns as routed paths (data-edge-kind="loop_return"), visually distinct
// from forward edges; the legacy fallback (no geometry) keeps loop containers
// and the LoopBackEdge.
import { render, act } from '@testing-library/react'
import { describe, it, expect, beforeAll, afterAll } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { LayoutView } from '../graph/arranged/types'
import { fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'
// Controllable unit font boundary. The real asset/face/coverage producer is exercised
// by P3T5/P3T14. Success requires nonempty actual text and the exact bundled contract;
// tests replace this method with a rejection/deferred promise for font sink cases.
const fontBoundary = vi.hoisted(() => ({ load: vi.fn(async (_doc: Document, texts: readonly string[], contract: {version: string; fontFamily: string}) => {
  if (!texts.length || !texts.every(t => typeof t === 'string') || contract.version !== 'arranged-renderer/1' || contract.fontFamily !== 'Inter, Noto Sans SC, Noto Emoji') throw new Error('invalid font request')
  return { fontsReady: true, fontFamilyResolved: true }
}) }))
vi.mock('../graph/arranged/fonts', () => ({ ensureBundledTextFonts: fontBoundary.load }))

import { GraphView } from './ExecutionView'

const saved: { ro?: unknown; dm?: unknown; ow?: PropertyDescriptor; oh?: PropertyDescriptor; bbox?: unknown } = {}

beforeAll(() => {
  const g = globalThis as unknown as { ResizeObserver?: unknown; DOMMatrixReadOnly?: unknown }
  saved.ro = g.ResizeObserver
  saved.dm = g.DOMMatrixReadOnly
  saved.ow = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')
  saved.oh = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')
  saved.bbox = (SVGElement.prototype as unknown as { getBBox?: unknown }).getBBox

  class StubResizeObserver {
    private cb: ResizeObserverCallback
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb
    }
    observe(el: Element) {
      setTimeout(() => this.cb([{ target: el } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver), 0)
    }
    unobserve() {}
    disconnect() {}
  }
  class StubDOMMatrix {
    m22: number
    constructor(transform?: string) {
      const m = /scale\(([\d.]+)\)/.exec(transform ?? '')
      this.m22 = m ? Number(m[1]) : 1
    }
  }
  g.ResizeObserver = StubResizeObserver
  // Legacy smoothstep edge labels (fallback mode) measure themselves with getBBox.
  ;(SVGElement.prototype as unknown as { getBBox: () => DOMRect }).getBBox = () =>
    ({ x: 0, y: 0, width: 10, height: 10 }) as DOMRect
  g.DOMMatrixReadOnly = StubDOMMatrix
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
    configurable: true,
    get() {
      return parseFloat((this as HTMLElement).style.width) || 10
    },
  })
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get() {
      return parseFloat((this as HTMLElement).style.height) || 10
    },
  })
})

afterAll(() => {
  const g = globalThis as unknown as { ResizeObserver?: unknown; DOMMatrixReadOnly?: unknown }
  g.ResizeObserver = saved.ro
  g.DOMMatrixReadOnly = saved.dm
  ;(SVGElement.prototype as unknown as { getBBox?: unknown }).getBBox = saved.bbox
  if (saved.ow) Object.defineProperty(HTMLElement.prototype, 'offsetWidth', saved.ow)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetWidth
  if (saved.oh) Object.defineProperty(HTMLElement.prototype, 'offsetHeight', saved.oh)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).offsetHeight
})

async function mount(
  def: WorkflowDefinition,
  layoutView: LayoutView | null,
  instance: Instance | null = null,
  trace: NodeExecution[] = [],
) {
  const utils = render(
    <div style={{ width: 800, height: 600 }}>
      <ReactFlowProvider>
        <GraphView
          instance={instance}
          workflowDef={def}
          trace={trace}
          layoutView={layoutView}
          onNodeClick={() => {}}
          selectedNodeId={null}
        />
      </ReactFlowProvider>
    </div>,
  )
  // Let the stubbed ResizeObserver callbacks run so React Flow measures nodes and mounts edges.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20))
  })
  if (layoutView?.geometry) await act(async () => { await Promise.resolve(); await new Promise(r => setTimeout(r, 0)) })
  return utils
}

const runningAtB = { state: 'running', currentNodeId: 'b' } as unknown as Instance
const traceADone: NodeExecution[] = [
  { id: 'e-a', instanceId: 'i', nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
]

const arrangedNode = (container: HTMLElement, id: string) =>
  container.querySelector(`[data-arranged-node="${id}"]`) as HTMLElement

describe('GraphView — arranged loop returns (routed-edge DOM contract)', () => {
  it('draws one routed path per manifest edge and marks the loop return distinctly', async () => {
    const { container } = await mount(fixtureWorkflow(), fixtureView())
    const kinds = [0, 1, 2].map((i) =>
      container.querySelector(`path[data-edge-path="${i}"]`)!.getAttribute('data-edge-kind'),
    )
    expect(kinds).toEqual(['forward', 'forward', 'loop_return'])

    const loop = container.querySelector('path[data-edge-path="2"]') as SVGPathElement
    const forward = container.querySelector('path[data-edge-path="1"]') as SVGPathElement
    expect(loop.style.strokeDasharray).toBe('6 4')
    expect(loop.style.stroke).toBe('rgb(124, 140, 245)') // #7c8cf5
    expect(forward.style.strokeDasharray).toBe('')
    expect(forward.style.stroke).toBe('rgb(138, 138, 176)') // #8a8ab0
    // The persisted route is drawn exactly, with its own arrowhead.
    expect(loop.getAttribute('d')).toBe('M 220,286 L 268,286 L 268,175 L 240,175')
    expect(loop.getAttribute('marker-end')).toMatch(/-arranged-arrow-2\)$/)
  })

  it('draws the loop-return label at its payload label box', async () => {
    await mount(fixtureWorkflow(), fixtureView())
    const again = document.querySelector('[data-edge-label="2"]') as HTMLElement
    expect(again.textContent).toBe('again')
    expect(again.style.transform).toBe('translate(274px, 220px)')
  })

  it('uses no xyflow auto-routing and no loop container that the reviewed geometry does not include', async () => {
    const { container } = await mount(fixtureWorkflow(), fixtureView())
    expect(container.querySelectorAll('.react-flow__edge-routedEdge')).toHaveLength(3)
    expect(container.querySelector('.react-flow__edge-smoothstep')).toBeNull()
    expect(container.querySelector('.react-flow__edge-loopBackEdge')).toBeNull()
    expect(container.querySelector('.react-flow__node-loopContainer')).toBeNull()
  })

  it('running-state styling never changes the rendered node boxes', async () => {
    const { container } = await mount(fixtureWorkflow(), fixtureView(), runningAtB, traceADone)
    const b = arrangedNode(container, 'b')
    expect(b.getAttribute('data-state')).toBe('running')
    expect([b.style.width, b.style.height]).toEqual(['200px', '70px'])
    const a = arrangedNode(container, 'a')
    expect(a.getAttribute('data-state')).toBe('completed')
    expect([a.style.width, a.style.height]).toEqual(['180px', '52px'])
    const c = arrangedNode(container, 'c')
    expect(c.getAttribute('data-state')).toBe('pending')
    expect([c.style.width, c.style.height]).toEqual(['180px', '52px'])
  })

  it('animates the active edge without changing its route or arrowhead', async () => {
    const { container } = await mount(fixtureWorkflow(), fixtureView(), runningAtB, traceADone)
    // Edge 1 (b → c) leaves the running node b.
    expect(container.querySelector('[data-edge-overlay="1"]')).not.toBeNull()
    expect(container.querySelector('[data-edge-overlay="0"]')).toBeNull()
    const main = container.querySelector('path[data-edge-path="1"]') as SVGPathElement
    expect(main.getAttribute('d')).toBe('M 140,210 L 140,235 L 130,235 L 130,260')
    expect(main.getAttribute('marker-end')).toMatch(/-arranged-arrow-1\)$/)
  })

  it('renders no node as draggable', async () => {
    const { container } = await mount(fixtureWorkflow(), fixtureView(), runningAtB, traceADone)
    const wrappers = [...container.querySelectorAll('.react-flow__node')]
    expect(wrappers).toHaveLength(3)
    expect(wrappers.some((w) => w.classList.contains('draggable'))).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Legacy fallback (no geometry: pre-feature snapshot or layout not yet ready).
// ---------------------------------------------------------------------------

const loopWorkflow: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
    { id: 'c', type: 'llm_call', label: 'C', description: '', config: {} },
  ],
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'b', label: 'tests pass' },
  ],
}

const unlabeledLoopWorkflow: WorkflowDefinition = {
  ...loopWorkflow,
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'b' },
  ],
}

const nestedLoopWorkflow: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
    { id: 'c', type: 'llm_call', label: 'C', description: '', config: {} },
    { id: 'd', type: 'llm_call', label: 'D', description: '', config: {} },
    { id: 'e', type: 'llm_call', label: 'E', description: '', config: {} },
  ],
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'd', target: 'e' },
    { source: 'e', target: 'b', label: 'outer done' },
    { source: 'd', target: 'c', label: 'inner done' },
  ],
}

const loopless: WorkflowDefinition = {
  ...loopWorkflow,
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
  ],
}

describe('GraphView — legacy fallback loop rendering', () => {
  it('renders a loop container and a loopBackEdge for a looping workflow without geometry', async () => {
    const { container } = await mount(loopWorkflow, null)
    expect(container.querySelector('[data-arranged-canvas="true"]')!.getAttribute('data-layout-mode')).toBe('fallback')
    expect(container.querySelectorAll('.react-flow__node-loopContainer')).toHaveLength(1)
    expect(container.querySelectorAll('.react-flow__edge-loopBackEdge')).toHaveLength(1)
  })

  it('renders neither for a loopless workflow', async () => {
    const { container } = await mount(loopless, null)
    expect(container.querySelector('.react-flow__node-loopContainer')).toBeNull()
    expect(container.querySelector('.react-flow__edge-loopBackEdge')).toBeNull()
    expect(container.querySelectorAll('.react-flow__edge-smoothstep')).toHaveLength(2)
  })

  it('labels the container from the back-edge label in the snapshot', async () => {
    const { container } = await mount(loopWorkflow, null)
    expect(container.querySelector('.react-flow__node-loopContainer')!.textContent).toBe('Loop until tests pass')
  })

  it('falls back to "Loop" when the back-edge carries no label', async () => {
    const { container } = await mount(unlabeledLoopWorkflow, null)
    expect(container.querySelector('.react-flow__node-loopContainer')!.textContent).toBe('Loop')
  })

  it('renders two labelled containers for nested loops', async () => {
    const { container } = await mount(nestedLoopWorkflow, null)
    const labels = [...container.querySelectorAll('.react-flow__node-loopContainer')].map((n) => n.textContent).sort()
    expect(labels).toEqual(['Loop until inner done', 'Loop until outer done'])
  })
})
```

- [x] **Step 2: Run it against Task 13.1's implementation**

Execution: 11 pass. Updated the illustrative loop path to the existing fixture's bottom/top port route (not the obsolete side-port example); added the missing vi import.

```bash
cd dashboard && npx vitest run src/routes/GraphView.loops.test.tsx
```
Expected:
```text
 Test Files  1 passed (1)
      Tests  11 passed (11)
```

- [x] **Step 3: Prove it can fail (mutation check; do not commit the mutation).**

Execution: view=null produces exactly 6 failures / 5 passes; restored view yields 11 passes and TypeScript passes (`/tmp/p3t13-2-mutation.log`). In `dashboard/src/routes/ExecutionView.tsx` `GraphView`, temporarily change `view={layoutView}` to `view={null}` and re-run the Step 2 command.

Expected:
```text
 × GraphView — arranged loop returns (routed-edge DOM contract) > draws one routed path per manifest edge and marks the loop return distinctly
   TypeError: Cannot read properties of null (reading 'getAttribute')
 …
      Tests  6 failed | 5 passed (11)
```

Restore with a temporary-file backup of `dashboard/src/routes/ExecutionView.tsx` taken before the mutation, then re-run Step 2 and confirm `11 passed`.

- [x] **Step 4: Commit**

Ordered review: only the requested loop DOM slice changed; tests assert real ReactFlow path/label/marker/box sinks and the absent-input mutation fails; stubs follow the neighboring arranged DOM suite.

```bash
git add dashboard/src/routes/GraphView.loops.test.tsx && git commit -m "test(dashboard): assert routed loop-return DOM contract in the execution graph" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13.3: Wire independent instance layout polling into the real ExecutionView

**Files:** Modify `dashboard/src/routes/ExecutionView.tsx`; create
`dashboard/src/routes/ExecutionView.layout.test.tsx`. This is the missing composition-root
task for the instance view, not another GraphView-only test.

**Interfaces:** Consume `api.instances.getLayout(id): Promise<LayoutView>`,
`retryLayout(id, expectedRevision): Promise<RetryAck>` (P3T1), `useLayoutStatus` (P3T11),
`Instance.layout` (P3T9), `resolveDisplayGeometry` (P3T1), `GraphView.layoutView` and
`LayoutStatusPanel` (P3T11). Produce no new exported hook or duplicate polling loop.

- [x] **Step 1 — RED:** Mount the actual default ExecutionView under its router/provider
  with a completed instance and the snapshot fixture, no WebSocket traffic. Stub only
  network boundaries. Return pending then complete for its layout endpoint, with moved
  node boxes and distinctive branch text. Advance fake timers; assert the final rendered
  node position, routed edge/label, “Layout reviewed” panel and zero more terminal polls.
  Missing getLayout wiring or view={null} must fail these DOM assertions.
  Add separate tests for failed/needs_attention findings plus best layout and Retry;
  clicking Retry posts the **snapshot** token, accepts a higher generation and resumes
  polling, even while instance.state stays completed. Assert no workflow-layout endpoint
  or execution intervention was called.
- [x] **Step 2 — RED:** Test same-token `instance.layout` as initial seed, then newer
  polled result replaces it; embedded layout with a foreign token is ignored. Test
  historical snapshot after workflow rename/delete, legacy tokenless snapshot (own
  positions; no write), foreign-revision GET (retain snapshot fallback, issue shown,
  reload only), fetch/in-band storage outage (last same-revision geometry retained and
  backoff), 403 retry (graph retained; error shown), instance-id navigation with old
  requests in flight, and unmount (no timers or updates). Validate geometry errors are
  fed to the panel and cannot coexist with “Layout reviewed”.
- [x] **Step 3:** Run `cd dashboard && npx vitest run src/routes/ExecutionView.layout.test.tsx`;

Execution: 13 behavioral failures / 3 characterization passes against the pre-polling route (`/tmp/p3t13-3-red.log`).

  observe the behavioral failures before changing ExecutionView.
- [x] **Step 4 — GREEN:** In the default ExecutionView, call useLayoutStatus once with
  instance id as key, independently of instance state and websocket effects. Until its
  first response use only same-revision instance.layout; thereafter use the hook value.
  Join to `workflowDef.presentation?.revision ?? ''`. Pass one joined value to GraphView
  and LayoutStatusPanel, with resolveDisplayGeometry issues. Preserve existing node
  selection, execution controls, details and callout behavior. Retry with no joined view
  only refreshes; with one, calls the instance retrier. Never switch to the current
  workflow's manifest or recompute reviewed routes.
- [x] **Step 5:** Rerun to GREEN plus `npx tsc --noEmit`; temporarily remove the hook or
  drop its view prop and observe sink assertions fail. Restore and commit source/tests.

Execution (13.3): 16 real-ReactFlow route tests pass; TypeScript passes. Dropping joinedView at GraphView produces 11 sink failures / 5 passes, restored green. A loaded InstanceArrangement owns the sole hook so its real same-token snapshot seed enters the hook's existing generation guards (optional initialView input); no duplicate polling/generation algorithm. The keyed ExecutionPage fences all instance-owned state on navigation. Spec review: only snapshot/status/retry wiring added, no workflow reads or intervention. Anti-theater: node wrappers, dimensions, exact routed ink/text and panel verdict/error are asserted; missing view mutation fails. Quality: existing shared hook/renderer own lifecycle and memoization. Real-browser route acceptance remains assigned to P3T14, not claimed from the unit font boundary.

### Task 13.4: Variable-size callout anchors and final instance surface verification

**Files:** Create `dashboard/src/graph/CalloutConnector.arranged.test.tsx`; modify
CalloutConnector only if its existing DOM/flow fallback fails these tests. Modify
`dashboard/src/graph/useFollowActiveNode.ts` and its focused tests for terminal Recenter.

- [x] **Step 1:** Mount the real shared renderer and CalloutConnector with a selected
  node of width 320, height 200, known flow position and viewport transform. Assert the
  drawn connector endpoint follows that node's actual DOM boundary. Exercise the absent
  DOM fallback through ReactFlow's node width/height and transform; it must use 320/200,
  not NODE_WIDTH/NODE_HEIGHT. Move/resize the payload, assert the endpoint moves; switch
  selection, pan/zoom and refetch status without losing the callout. Unknown/deleted node
  draws no connector and does not throw. Use browser layout for actual DOM assertions
  (P3T14 Task 14.3), and deterministic client-rect stubs only for the focused unit math.
- [x] **Step 2:** Run `cd dashboard && npx vitest run src/graph/CalloutConnector.arranged.test.tsx src/routes/ExecutionView.layout.test.tsx src/routes/GraphView.test.tsx src/routes/GraphView.loops.test.tsx`.
  A characterization test may already pass; replace the width/height with legacy constants
  temporarily and require failure, restore, then refactor only if needed.
- [x] **Step 3:** Run dashboard typecheck/full Vitest and confirm no drag/layout-write
  path remains in ExecutionView. Commit tests and any required focused correction.
  P3T14 rebuilds embedded assets and performs the real workflow/instance browser gate.

## Wired-and-fed

`instance.WorkflowSnapshot` (engine trigger) → authorised instance GET + snapshot layout
(P3T8/P3T9) → api.instances (P3T1) → default ExecutionView/useLayoutStatus (Task 13.3) →
joined GraphView/ArrangedGraph and LayoutStatusPanel → actual DOM/callout endpoint.
`useFollowActiveNode` consumes the payload node centre; Task 13.4 and P3T14 browser tests
assert the sink. Runtime completion and WebSocket messages neither start nor stop layout
polling. This task is complete only when Tasks 13.1–13.4 all pass.

### Terminal-anchor and initial-seed requirements

- [x] RED: completed instance mounts with a tall reviewed box; user pans then Recenter
  targets that exact box at DEFAULT_ZOOM. A late layout moves the box while execution
  stays completed: no automatic follow, but Recenter uses the new position. Existing
  useFollowActiveNode returns before recording these anchors; observe this source failure.
- [x] GREEN: always update the remembered latest anchor (clear on missing anchor), preserve
  running-only automatic pan and user pause. Don't add a second unconditional centring effect.
- [x] P3T13 Task 13.3 must retain a compatible same-token embedded instance.layout seed
  through the hook's initial transport/in-band failure, rather than dropping it on the
  first unavailable response. Once the hook accepts data, generation guards apply to both
  seed and polls. Changing instance ID resets seed, joined status and retry/request guards.
- [x] Callout DOM lookup must be scoped to the owning graph/provider when two graphs are
  mounted, and CSS.escape handles node IDs containing quotes/brackets. A global
  document.querySelector selecting the first same-ID node is not sufficient source evidence.

**Font issue wiring (Task 13.3):** Default ExecutionView owns renderIssues state, passes its
setter as GraphView.onDisplayIssues, forwards it to ArrangedGraph, and merges the result
with resolveDisplayGeometry issues for the panel. Reset on instance/revision change; guard
stale promises by revision/geometry/font identity. Unit suites use an argument-honoring
font loader boundary and await readiness before reviewed/viewport assertions; missing-font
and deferred-font cases must assert actual fallback/panel behavior. Browser acceptance uses
real coverage assets and faces, never that unit fake.

## Execution record (Task 13, this pass)

Selection: phase/task numeric order; earlier recorded blocker gates were not re-failed. P3T12 is fully checked; P3T13 was the next independent implementation slice. No P3T14 work was started.

13.4 RED: real shared-renderer/store callout provenance found the second graph incorrectly using the first graph's DOM rect; the focused hook and GraphView both failed terminal Recenter (3 failures / 40 passes, `/tmp/p3t13-4-red.log`). GREEN: store-scoped, CSS.escape-protected lookup plus owning-page divider scope; remembered anchors update/clear for terminal runs, while only running/following anchors animate. Quote/bracket IDs are exercised in actual shared-renderer nodes. Legacy-dimension mutation fails the SVG endpoint (expected 190,140; got 130,110), restored green. Tests cover payload move/resize, viewport, selection, missing nodes, and the default ExecutionView's actual interactiveActive/nodeId producer through to the callout line.

Final ordered self-review:
1. Spec: implemented only the requested snapshot/status/retry/render/follow/callout slice. Removed an unnecessary marker-namespace change. The optional hook initialView is the real snapshot seed, not another poller, flag or synthetic producer. Retry remains server-authorized; no workflow/layout writes or current-manifest lookup were introduced.
2. Anti-green-theater: real route/API client/hook/panel/ArrangedGraph/ReactFlow DOM assertions fail with joinedView removed (11 failures), and loop sinks fail with null GraphView input (6 failures). Legacy width/height mutation fails the connector endpoint. Added actual-root interactive callout, selection retention and old Retry/font/request navigation guards. HTTP and font boundaries are explicit unit inputs, not release provenance.
3. Quality: loaded, keyed status owner feeds the snapshot seed into existing hook generation guards; navigation resets all instance-owned state. Scoped lookups use the provider's store; JSX follows neighboring route formatting. A stable existing-test store mock avoids an artificial effect loop. Font readiness is awaited before terminal viewport assertions.

Final verification after fixes: `npx tsc --noEmit`; `npx vitest run --maxWorkers=2` — 70 files / 1027 tests pass (63.87s); `make ui` passes and embeds the new route; `git diff --check` passes; ExecutionView drag/saveLayout/fixed-node/local-ReactFlow grep is empty. Logs: `/tmp/p3t13-reviewed-full.log`, `/tmp/p3t13-ui.log`. Existing Vite large-chunk and Node localstorage warnings are non-failing. Full actual-authoring → authorized HTTP → real-browser route/geometry/digest acceptance remains the explicitly assigned P3T14 gate; it is not inferred from unit font/rect stubs.
