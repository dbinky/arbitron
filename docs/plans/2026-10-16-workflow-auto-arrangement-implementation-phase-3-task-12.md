# Workflow Auto-Arrangement — Phase 3 Task 12: WorkflowReview uses shared renderer; manual positioning removed client-side

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

**Goal:** The workflow page draws only through the shared `ArrangedGraph`, fed by the revision-joined `useLayoutStatus` view and a truthful `LayoutStatusPanel`. All drag, auto-save, `fitView` and `saveLayout` code is gone from the client.

**Architecture:** `WorkflowReview` (route `/workflows/:name`, mounted at `dashboard/src/App.tsx:22`) is the composition point in the workflow view for P3T1 (client + validation), P3T3 (`ArrangedGraph`) and P3T11 (`useLayoutStatus` + `LayoutStatusPanel`). The outer component owns the manifest (`api.workflows.get`). It refetches the manifest silently on window focus, on `visibilitychange`→visible, and when the layout reports a different saved revision. An inner `ReviewPage` (mounted only once a manifest exists) runs `useLayoutStatus(() => api.workflows.getLayout(name), rev => api.workflows.retryLayout(name, rev), name)` and joins the manifest and the layout by `presentation.revision` ⇄ `view.savedRevision`. The view reaches the renderer and the panel only when they match. `api.workflows.saveLayout` is deleted.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters), React 19, react-router-dom 7, @xyflow/react 12, Vitest 4 + jsdom + @testing-library/react 16 (+ jest-dom), CSS modules.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §2 (join manifest and layout by revision; refetch on a race), §5 (status, polling, refresh, readable zoom, no unconditional fitView, explicit fit-all), §6 (remove manual positioning) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS, §5.

**Depends on (must be merged first):**
- **P3T1:** `graph/arranged/types.ts`, `validate.ts` (`resolveDisplayGeometry`), `testFixtures.ts`, `api.workflows.getLayout/retryLayout`, `WorkflowDefinition.presentation`.
- **P3T2:** `ArrangedNode`, `RoutedEdge`, `fonts.ts`, imported by `ArrangedGraph`.
- **P3T3:** `ArrangedGraph`, `fallbackPositions`, and the `useGraphLayout` without `needsAutoSave`. P3T3 Task 3.2 already deleted WorkflowReview's `needsAutoSave` autosave effect (base lines 60-68) and changed line 46 to `const { rfNodes, rfEdges } = useGraphLayout(...)`. **This task deletes everything that remains:** `collectPositions`, the local `ReviewGraph` with its own `<ReactFlow>`, the `onNodeDragStop` handler with its `saveLayout` call, `nodesDraggable`, `fitView`/`fitViewOptions`, and `api.workflows.saveLayout`.
- **P3T11:** `useLayoutStatus`, `POLL_INTERVAL_MS`, `LayoutStatusPanel` (with `displayIssues`).
- Production data: P3T9 `GET/POST /api/v1/workflows/{name}/layout[/retry]`; `GET /api/v1/workflows/{name}` already returns `presentation` (P2T2, pinned by P3T9 Task 9.4).

**Write-permission source (reused, not invented):** the dashboard has no per-workflow permission field. The server's retry policy (`Service.AuthorizeLayoutRetry`, P3T8) is `Service.Update`'s rule (`domain/workflow/service.go:200-226,271-280`):
- the caller's private copy is writable (`Service.Load` only ever returns the caller's *own* private copy);
- a shared copy is writable only by an admin (edit in place);
- a non-admin on a shared copy gets `ErrSharedReadOnly`.

The client reproduces this from data it already has: `workflowDef.presentation.scope` (`"private"` | `"shared"`, `domain/workflow/workflow.go:268-269`) plus `api.me().isAdmin` (`dashboard/src/api/types.ts:209-219`, the same call `useWorkflowsCatalog`/`QueueView` make). A legacy manifest (no `presentation`) never gets Retry. The server re-checks every retry.

---

### Task 12.1: WorkflowReview renders through `ArrangedGraph` + `useLayoutStatus` + `LayoutStatusPanel`

**Files:**
- Modify: `dashboard/src/routes/WorkflowReview.tsx` (whole file is replaced; the content below is complete):
  - `:1-115` (base numbering) holds the imports, `collectPositions` and the local `ReviewGraph`/`<ReactFlow>`. Replace it with the new imports, `canRetryLayout` and `ReviewPage`. After P3T3 this is `:1-105`.
  - `:117-217` (`ReviewSidePanel`) is reproduced verbatim.
  - `:219-340` (default export) is replaced. After P3T3 this is `:209-330`.
- Create (test): `dashboard/src/routes/WorkflowReview.test.tsx`. No `WorkflowReview*.test.tsx` exists in the base checkout, so there is none to update.
- Unchanged: `dashboard/src/routes/WorkflowReview.module.css` (reuses `.loading`, `.graphContainer`, etc.), `dashboard/src/App.tsx:22` (route already mounts the default export).

**Interfaces**

Consumes (restated from contracts §3 and sibling plans):
```ts
// P3T1 — dashboard/src/graph/arranged/types.ts
export interface LayoutView { savedRevision: string; jobGeneration: number; resultGeneration: number; state?: string; displayStatus: DisplayStatus; stage?: string; attemptsUsed: number; attemptLimit: number; findings: LayoutFinding[] | null; failureStage?: string; failureReason?: string; contract?: RendererContract; geometry?: Geometry; retained?: RetainedLayout; provenance: Provenance; unreviewed: boolean; retryEligible: boolean; available: boolean; availabilityError?: string; evidence?: LayoutEvidence }
export interface RetryAck { savedRevision: string; jobGeneration: number; state: string; displayStatus: DisplayStatus }
export interface Presentation { revision: string; savedAt: string; reason: string; scope: string; ownerId?: string; name: string; retained: RetainedLayout }
// P3T1 — dashboard/src/graph/types.ts
interface WorkflowDefinition { /* existing */ presentation?: Presentation }
// P3T1 — dashboard/src/graph/arranged/validate.ts
export function resolveDisplayGeometry(def: WorkflowDefinition, view: LayoutView | null): { geometry: Geometry | null; issues: string[] }
// P3T1 — dashboard/src/api/client.ts
api.workflows.getLayout(name: string): Promise<LayoutView>                         // GET  /api/v1/workflows/{name}/layout
api.workflows.retryLayout(name: string, expectedRevision: string): Promise<RetryAck> // POST /api/v1/workflows/{name}/layout/retry {"expectedRevision"}
// P3T1 — dashboard/src/graph/arranged/testFixtures.ts (tests only)
fixtureWorkflow(): WorkflowDefinition   // a(entrypoint,'Start') → b('Draft a very long label that must wrap') → c, loop c→b; presentation {revision 'rev-1', scope 'private'}
fixtureGeometry(): Geometry             // a box (40,40,180,52), b (40,140,200,70), c (40,260,180,52)
fixtureView(o?: Partial<LayoutView>): LayoutView // complete/passed, savedRevision 'rev-1', geometry fixtureGeometry()
// P3T3 — dashboard/src/graph/arranged/ArrangedGraph.tsx (must be inside <ReactFlowProvider>)
export default function ArrangedGraph(props: { workflowDef: WorkflowDefinition; view: LayoutView | null; executionStates?: Record<string, NodeExecutionState>; selectedNodeId: string | null; selectedNodeIds?: ReadonlySet<string>; onNodeClick: (nodeId: string) => void; anchorNodeId: string | null; onMoveStart?: (event: unknown) => void; onAnchorPosition?: (c: { x: number; y: number } | null) => void; onDisplayIssues?: (issues: readonly string[]) => void; fixedViewport?: { x: number; y: number; zoom: number }; children?: ReactNode }): JSX.Element
//   behaviour relied on: <div data-arranged-canvas="true" data-layout-mode="arranged"|"fallback">; arranged nodes type 'arrangedNode' with draggable:false,
//   edges 'routedEdge'; fallback nodes 'arbitronNode'; ReactFlow gets nodesDraggable={false}, no fitView prop; centres once per displayed layout via
//   useReactFlow().setCenter(cx, cy, { zoom: DEFAULT_ZOOM /* 0.9 */ }); renders <button aria-label="Fit whole workflow">Fit all</button> → fitView({ padding: 0.1 })
// P3T11 — dashboard/src/hooks/useLayoutStatus.ts
export interface UseLayoutStatus { view: LayoutView | null; error: string | null; loading: boolean; refresh: () => void; retry: () => Promise<void>; retrying: boolean; retryError: string | null }
export function useLayoutStatus(fetcher: () => Promise<LayoutView>, retrier: (expectedRevision: string) => Promise<RetryAck>, key: string): UseLayoutStatus
export const POLL_INTERVAL_MS = 2000   // refresh is stable per key; retry() posts the hook's current view.savedRevision
// P3T11 — dashboard/src/graph/arranged/LayoutStatusPanel.tsx
export interface LayoutStatusPanelProps { view: LayoutView | null; error: string | null; onRetry: () => void; retrying: boolean; retryError: string | null; canRetry: boolean; displayIssues?: readonly string[] }
//   role="status"; titles 'Checking arrangement status' | 'Arrangement pending' | 'Reviewing readability' | 'Layout reviewed' | 'Layout needs attention' | 'Arrangement status unavailable' …;
//   renders null for displayStatus 'legacy'; Retry button 'Retry arrangement' when canRetry && (view.retryEligible || error != null)
// existing
export function selectAnchorNodeId(instance: Instance | null, workflowDef: WorkflowDefinition | null, executionStates: Record<string, NodeExecutionState>, trace: NodeExecution[]): string | null // graph/selectAnchorNodeId.ts — static preview: entrypoint → source → first
api.me(): Promise<Me>   // Me.isAdmin: boolean
api.workflows.get(name): Promise<WorkflowDefinition>
```

Produces: no new exported symbols. `WorkflowReview` stays the default export of `dashboard/src/routes/WorkflowReview.tsx`. Module-private helpers: `canRetryLayout(def, me)`, `ReviewPage`, `ReviewSidePanel`.

Behaviour (each line has a test below):
1. **Graph.** Rendered only by `ArrangedGraph`. `view` is the hook's view **only when** `view.savedRevision === (workflowDef.presentation?.revision ?? '')`; otherwise `null`. A legacy manifest and a legacy view both carry `''`, so they join.
2. **Mismatch.** For each distinct `(manifestRevision, viewRevision)` pair, call `onReloadWorkflow()` and `layout.refresh()` exactly once. The two calls cover a manifest read that is older and a layout read that is older. Geometry from the other revision is never drawn; the fallback of the current manifest is shown meanwhile.
3. **Panel.** Gets the same joined view, `layout.error`, `layout.retrying`, `layout.retryError`, `displayIssues = resolveDisplayGeometry(def, joinedView).issues`, and `canRetry = canRetryLayout(def, me)`. Retry with a joined view calls `layout.retry()`, which posts the manifest's revision. With no joined view it only reloads the manifest and the layout.
4. **Initial view.** The graph waits for the first layout response (`Loading layout...`), so the first centring is on the layout actually shown. The anchor is `selectAnchorNodeId(null, def, {}, [])` (entry node), at `DEFAULT_ZOOM` (inside `ArrangedGraph`). There is no `fitView` prop; fit-all is only the explicit button.
5. **Manifest refresh.** Silent reload on window `focus` and on `visibilitychange`→visible. The newest response wins, and a failed reload keeps the current manifest. A failed initial load shows the error, and the layout is never polled.
6. **Selection.** Selection state lives in `ReviewPage` and survives polls and refetches.

- [x] **Step 1: Write the failing test.** Create `dashboard/src/routes/WorkflowReview.test.tsx`:

Execution: added the route sink suite plus outage/backoff, stale RetryAck GET, A→B→A manifest/layout epochs, manifest response ordering/failure, and deferred/rejected/stale font controls (32 tests).

The test drives the real route, real API client, real `useLayoutStatus`, real `LayoutStatusPanel` and real `ArrangedGraph`. Only fetch, the controllable argument-honoring font boundary and leaf ReactFlow are faked. Font success tests assert the exact requested text/contract; reject/deferred cases probe the visible sink. The fake ReactFlow records the nodes it is handed: this is the sink.

```tsx
import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { Me } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { Geometry, LayoutFinding, LayoutView, RetryAck } from '../graph/arranged/types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'
import { POLL_INTERVAL_MS } from '../hooks/useLayoutStatus'

// ---------------------------------------------------------------------------
// Leaf fake: only <ReactFlow> (and the viewport API) is replaced. Everything
// above it (route, api client, useLayoutStatus, LayoutStatusPanel,
// ArrangedGraph, buildArrangedFlow, useGraphLayout) is real. The fake records
// every node list it is handed, which is the SINK these tests assert on.
// ---------------------------------------------------------------------------
interface FlowNode {
  id: string
  type?: string
  position: { x: number; y: number }
  selected?: boolean
  draggable?: boolean
}
interface FlowEdge {
  id: string
  type?: string
}
interface FlowProps {
  nodes: FlowNode[]
  edges: FlowEdge[]
  children?: ReactNode
  onNodeClick?: (event: unknown, node: FlowNode) => void
  [key: string]: unknown
}
interface Frame {
  id: string
  type?: string
  x: number
  y: number
}

const flow = vi.hoisted(() => ({
  props: null as FlowProps | null,
  history: [] as Frame[][],
  setCenter: vi.fn(),
  fitView: vi.fn(),
}))

vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    ReactFlowProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
    ReactFlow: (props: FlowProps) => {
      flow.props = props
      flow.history.push(props.nodes.map((n) => ({ id: n.id, type: n.type, x: n.position.x, y: n.position.y })))
      return (
        <div data-testid="rf">
          {props.nodes.map((n) => (
            <div
              key={n.id}
              data-testid={`node-${n.id}`}
              data-type={n.type}
              data-selected={String(Boolean(n.selected))}
              onClick={(e) => props.onNodeClick?.(e, n)}
            />
          ))}
          {props.children}
        </div>
      )
    },
    Background: () => null,
    Controls: () => null,
    useReactFlow: () => ({ setCenter: flow.setCenter, fitView: flow.fitView, getZoom: () => 0.9 }),
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

import WorkflowReview from './WorkflowReview'

// ---------------------------------------------------------------------------
// Fake HTTP server behind globalThis.fetch. Replies are scripted per endpoint
// by call number (the last reply repeats). Every request is recorded so tests
// can assert exactly what was read and that nothing was written.
// ---------------------------------------------------------------------------
class HttpError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}
interface Deferred<T> {
  promise: Promise<T>
  resolve: (v: T) => void
}
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
type Reply<T> = T | HttpError | Deferred<T>
interface Call {
  method: string
  url: string
  body: string | undefined
}

const server = {
  workflow: [] as Array<Reply<WorkflowDefinition>>,
  layout: [] as Array<Reply<LayoutView>>,
  retry: [] as Array<Reply<RetryAck>>,
  me: [] as Array<Reply<Me>>,
  calls: [] as Call[],
  served: new Map<string, number>(),
}

function json(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? 'OK' : 'Error',
    json: async () => body,
  } as Response
}

function isDeferred<T>(r: Reply<T>): r is Deferred<T> {
  return typeof r === 'object' && r !== null && 'promise' in r && 'resolve' in r
}

async function serve<T>(key: string, replies: Array<Reply<T>>): Promise<Response> {
  const n = server.served.get(key) ?? 0
  server.served.set(key, n + 1)
  if (replies.length === 0) return json({ error: `no reply scripted for ${key}` }, 500)
  const r = replies[Math.min(n, replies.length - 1)]
  if (r instanceof HttpError) return json({ error: r.status >= 500 ? 'internal server error' : r.message, code: r.status === 503 ? 'UNAVAILABLE' : 'ERROR' }, r.status)
  if (isDeferred(r)) return json(await r.promise)
  return json(r)
}

const WF_URL = '/api/v1/workflows/wf'
const LAYOUT_URL = '/api/v1/workflows/wf/layout'
const RETRY_URL = '/api/v1/workflows/wf/layout/retry'

const fetchSpy = vi.fn(async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = String(input)
  const method = (init?.method ?? 'GET').toUpperCase()
  server.calls.push({ method, url, body: typeof init?.body === 'string' ? init.body : undefined })
  if (method === 'GET' && url === WF_URL) return serve('workflow', server.workflow)
  if (method === 'GET' && url === LAYOUT_URL) return serve('layout', server.layout)
  if (method === 'POST' && url === RETRY_URL) return serve('retry', server.retry)
  if (method === 'GET' && url === '/api/v1/me') return serve('me', server.me)
  if (method === 'GET' && url.startsWith('/api/v1/workflows/wf/stats?')) {
    return json({ totalRuns: 0, successCount: 0, failureCount: 0, avgDurationSeconds: 0 })
  }
  if (method === 'GET' && url === '/api/v1/schedules?workflowName=wf') return json({ schedules: [] })
  return json({ error: `unexpected ${method} ${url}` }, 404)
})

const realFetch = globalThis.fetch

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
const count = (method: string, url: string) =>
  server.calls.filter((c) => c.method === method && c.url === url).length
const writes = () => server.calls.filter((c) => c.method !== 'GET')

/** Advance fake time by `ms`, then flush the promise/effect chains it releases. */
async function settle(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
  }
}

async function open() {
  render(
    <MemoryRouter initialEntries={['/workflows/wf']}>
      <Routes>
        <Route path="/workflows/:name" element={<WorkflowReview />} />
      </Routes>
    </MemoryRouter>,
  )
  await settle()
}

function rf(): FlowProps {
  if (!flow.props) throw new Error('ReactFlow was not rendered')
  return flow.props
}
const pos = (id: string) => rf().nodes.find((n) => n.id === id)?.position
const mode = () => document.querySelector('[data-arranged-canvas="true"]')?.getAttribute('data-layout-mode')
const status = () => screen.getByRole('status')
const retryButton = () => screen.queryByRole('button', { name: 'Retry arrangement' })
const drewArrangedB = (x: number, y: number) =>
  flow.history.some((frame) => frame.some((n) => n.id === 'b' && n.type === 'arrangedNode' && n.x === x && n.y === y))

function me(over: Partial<Me> = {}): Me {
  return {
    userId: 'u1',
    username: 'alice',
    role: 'user',
    departments: [],
    ledDepartments: [],
    isAdmin: false,
    authMode: 'org',
    ...over,
  }
}

function withRevision(def: WorkflowDefinition, revision: string, over: Partial<WorkflowDefinition> = {}): WorkflowDefinition {
  return { ...def, ...over, presentation: { ...def.presentation!, revision } }
}

function withScope(def: WorkflowDefinition, scope: 'private' | 'shared'): WorkflowDefinition {
  return { ...def, presentation: { ...def.presentation!, scope } }
}

/** Fixture geometry with node b moved to a distinctive (x, y). Still valid for fixtureWorkflow(). */
function geometryWithB(x: number, y: number): Geometry {
  return fixtureGeometryWithBoxes({ b: { x, y, w: 200, h: 70 } })
}

const pendingView = (over: Partial<LayoutView> = {}) =>
  fixtureView({
    state: 'pending',
    displayStatus: 'pending',
    stage: 'queued',
    attemptsUsed: 0,
    resultGeneration: 0,
    geometry: undefined,
    provenance: 'retained_positions',
    retained: { source: 'none' },
    ...over,
  })

const clipped: LayoutFinding = {
  criterion: 'text_clipped',
  severity: 'blocking',
  source: 'review',
  nodeIds: ['b'],
  evidence: 'label of "b" clipped',
}

const needsAttentionView = (over: Partial<LayoutView> = {}) =>
  fixtureView({
    state: 'needs_attention',
    displayStatus: 'needs_attention',
    attemptsUsed: 5,
    findings: [clipped],
    retryEligible: true,
    ...over,
  })

beforeEach(() => {
  vi.useFakeTimers()
  fontBoundary.load.mockClear()
  server.workflow = [fixtureWorkflow()]
  server.layout = [fixtureView()]
  server.retry = []
  server.me = [me()]
  server.calls = []
  server.served = new Map()
  flow.props = null
  flow.history = []
  flow.setCenter.mockClear()
  flow.fitView.mockClear()
  fetchSpy.mockClear()
  globalThis.fetch = fetchSpy as unknown as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  globalThis.fetch = realFetch
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — shared renderer fed by the fetched layout (provenance)', () => {
  it('draws the nodes at the geometry returned by GET /workflows/{name}/layout', async () => {
    server.layout = [fixtureView({ geometry: geometryWithB(777, 1234) })]
    await open()
    expect(count('GET', LAYOUT_URL)).toBe(1)
    expect(fontBoundary.load).toHaveBeenCalledWith(document, expect.arrayContaining(['Start', 'Draft a very long label that must wrap', 'again']), expect.objectContaining({fontFamily: 'Inter, Noto Sans SC, Noto Emoji'}))
    expect(mode()).toBe('arranged')
    expect(rf().nodes.map((n) => n.type)).toEqual(['arrangedNode', 'arrangedNode', 'arrangedNode'])
    expect(pos('b')).toEqual({ x: 777, y: 1234 })
    expect(rf().edges.map((e) => e.type)).toEqual(['routedEdge', 'routedEdge', 'routedEdge'])
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('draws the manifest fallback (never arranged geometry) while the revision has no geometry yet', async () => {
    server.layout = [pendingView()]
    await open()
    expect(mode()).toBe('fallback')
    expect(rf().nodes.some((n) => n.type === 'arrangedNode')).toBe(false)
    expect(
      rf()
        .nodes.filter((n) => n.type === 'arbitronNode')
        .map((n) => n.id)
        .sort(),
    ).toEqual(['a', 'b', 'c'])
    expect(drewArrangedB(40, 140)).toBe(false)
    expect(status()).toHaveTextContent('Arrangement pending')
  })

  it('surfaces client-side validation issues and never shows the undrawable geometry as reviewed', async () => {
    const geo = fixtureGeometry()
    geo.contract = { ...geo.contract, version: 'arranged-renderer/2' }
    server.layout = [fixtureView({ geometry: geo })]
    await open()
    expect(mode()).toBe('fallback')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(status()).toHaveTextContent(
      'This layout could not be displayed: renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"',
    )
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — status polling', () => {
  it('polls while arrangement is active, updates geometry and status together, and stops at a terminal status', async () => {
    server.layout = [
      pendingView(),
      pendingView({ state: 'in_progress', displayStatus: 'reviewing', stage: 'reviewing', attemptsUsed: 1 }),
      fixtureView({ geometry: geometryWithB(500, 900) }),
    ]
    await open()
    expect(count('GET', LAYOUT_URL)).toBe(1)
    expect(status()).toHaveTextContent('Arrangement pending')
    expect(mode()).toBe('fallback')

    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(2)
    expect(status()).toHaveTextContent('Reviewing readability')
    expect(status()).toHaveTextContent('attempt 1 of 5')
    expect(mode()).toBe('fallback')

    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(3)
    expect(status()).toHaveTextContent('Layout reviewed')
    expect(mode()).toBe('arranged')
    expect(pos('b')).toEqual({ x: 500, y: 900 })

    await settle(POLL_INTERVAL_MS * 10)
    expect(count('GET', LAYOUT_URL)).toBe(3)
    expect(count('GET', WF_URL)).toBe(1)
  })

  it('keeps the selected node selected (and its details shown) across a status update', async () => {
    server.layout = [pendingView(), fixtureView()]
    await open()
    fireEvent.click(screen.getByTestId('node-b'))
    expect(screen.getByTestId('node-b').getAttribute('data-selected')).toBe('true')
    expect(screen.getByText('Draft a very long label that must wrap')).toBeInTheDocument()

    await settle(POLL_INTERVAL_MS)
    expect(mode()).toBe('arranged')
    expect(screen.getByTestId('node-b').getAttribute('data-type')).toBe('arrangedNode')
    expect(screen.getByTestId('node-b').getAttribute('data-selected')).toBe('true')
    expect(screen.getByTestId('node-a').getAttribute('data-selected')).toBe('false')
    expect(screen.getByText('Draft a very long label that must wrap')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — truthful verdicts and Retry', () => {
  it('shows needs_attention with findings, the best layout and Retry, never as complete', async () => {
    server.layout = [needsAttentionView()]
    await open()
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).not.toHaveTextContent(/complete/i)
    expect(screen.getByText(/label of "b" clipped/)).toBeInTheDocument()
    expect(mode()).toBe('arranged')
    expect(retryButton()).toBeInTheDocument()
  })

  it("Retry posts the manifest's revision, then re-polls the new job to its terminal result", async () => {
    server.workflow = [withRevision(fixtureWorkflow(), 'rev-7')]
    server.layout = [
      needsAttentionView({ savedRevision: 'rev-7', jobGeneration: 1 }),
      fixtureView({
        savedRevision: 'rev-7',
        jobGeneration: 2,
        state: 'pending',
        displayStatus: 'pending',
        stage: 'queued',
        attemptsUsed: 0,
        provenance: 'retained_geometry',
      }),
      fixtureView({ savedRevision: 'rev-7', jobGeneration: 2, resultGeneration: 2, geometry: geometryWithB(600, 700) }),
    ]
    server.retry = [{ savedRevision: 'rev-7', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }]
    await open()
    expect(writes()).toEqual([])

    fireEvent.click(retryButton()!)
    await settle()
    expect(writes()).toEqual([{ method: 'POST', url: RETRY_URL, body: JSON.stringify({ expectedRevision: 'rev-7' }) }])
    expect(count('GET', LAYOUT_URL)).toBe(2)
    expect(status()).toHaveTextContent('Arrangement pending')

    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(3)
    expect(status()).toHaveTextContent('Layout reviewed')
    expect(pos('b')).toEqual({ x: 600, y: 700 })

    await settle(POLL_INTERVAL_MS * 5)
    expect(count('GET', LAYOUT_URL)).toBe(3)
  })

  it('keeps the layout and reports the error when the retry is rejected', async () => {
    server.layout = [needsAttentionView()]
    server.retry = [new HttpError(409, 'layout: expected revision is not current')]
    await open()
    fireEvent.click(retryButton()!)
    await settle()
    expect(screen.getByText('Retry failed: API 409: layout: expected revision is not current')).toBeInTheDocument()
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(mode()).toBe('arranged')
    expect(count('GET', LAYOUT_URL)).toBe(1)
  })

  it('hides Retry on a shared workflow for a non-admin (shared workflows are read-only)', async () => {
    server.workflow = [withScope(fixtureWorkflow(), 'shared')]
    server.layout = [needsAttentionView()]
    server.me = [me({ isAdmin: false })]
    await open()
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(screen.getByText(/label of "b" clipped/)).toBeInTheDocument()
    expect(retryButton()).toBeNull()
  })

  it('offers Retry on a shared workflow to an admin (edit-in-place policy)', async () => {
    server.workflow = [withScope(fixtureWorkflow(), 'shared')]
    server.layout = [needsAttentionView()]
    server.me = [me({ isAdmin: true, role: 'admin' })]
    await open()
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(retryButton()).toBeInTheDocument()
  })

  it('hides Retry on a shared workflow when the caller identity cannot be read', async () => {
    server.workflow = [withScope(fixtureWorkflow(), 'shared')]
    server.layout = [needsAttentionView()]
    server.me = [new HttpError(401, 'unauthenticated')]
    await open()
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(retryButton()).toBeNull()
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — manifest/layout revision join', () => {
  it("refetches the workflow once when the layout is for a newer revision, never drawing that revision's geometry against the old manifest", async () => {
    const rev2 = deferred<WorkflowDefinition>()
    server.workflow = [fixtureWorkflow(), rev2]
    server.layout = [fixtureView({ savedRevision: 'rev-2', geometry: geometryWithB(901, 902) })]
    await open()
    expect(count('GET', WF_URL)).toBe(2)
    expect(count('GET', LAYOUT_URL)).toBe(2)
    expect(mode()).toBe('fallback')
    expect(rf().nodes.some((n) => n.type === 'arrangedNode')).toBe(false)
    expect(drewArrangedB(901, 902)).toBe(false)
    expect(status()).toHaveTextContent('Arrangement status unavailable')

    await act(async () => {
      rev2.resolve(withRevision(fixtureWorkflow(), 'rev-2'))
    })
    await settle()
    expect(mode()).toBe('arranged')
    expect(pos('b')).toEqual({ x: 901, y: 902 })
    expect(status()).toHaveTextContent('Layout reviewed')

    await settle(POLL_INTERVAL_MS * 10)
    expect(count('GET', WF_URL)).toBe(2)
    expect(count('GET', LAYOUT_URL)).toBe(2)
  })

  it("refreshes the layout once when the layout read predates the manifest's revision", async () => {
    server.workflow = [withRevision(fixtureWorkflow(), 'rev-2')]
    server.layout = [
      fixtureView({ savedRevision: 'rev-1', geometry: geometryWithB(11, 12) }),
      fixtureView({ savedRevision: 'rev-2', geometry: geometryWithB(21, 22) }),
    ]
    await open()
    expect(mode()).toBe('arranged')
    expect(pos('b')).toEqual({ x: 21, y: 22 })
    expect(drewArrangedB(11, 12)).toBe(false)
    await settle(POLL_INTERVAL_MS * 10)
    expect(count('GET', WF_URL)).toBe(2)
    expect(count('GET', LAYOUT_URL)).toBe(2)
  })

  it('does not loop when the manifest and layout revisions stay different', async () => {
    server.workflow = [fixtureWorkflow()]
    server.layout = [fixtureView({ savedRevision: 'rev-2', geometry: geometryWithB(901, 902) })]
    await open()
    await settle(POLL_INTERVAL_MS * 20)
    fireEvent.click(screen.getByTestId('node-a'))
    await settle(POLL_INTERVAL_MS * 20)
    expect(count('GET', WF_URL)).toBe(2)
    expect(count('GET', LAYOUT_URL)).toBe(2)
    expect(mode()).toBe('fallback')
    expect(flow.history.some((frame) => frame.some((n) => n.type === 'arrangedNode'))).toBe(false)
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — external (HTTP/MCP) saves', () => {
  it('refetches workflow metadata and layout on window focus so an external save is shown', async () => {
    server.workflow = [fixtureWorkflow(), withRevision(fixtureWorkflow(), 'rev-2', { author: 'mcp-bot' })]
    server.layout = [fixtureView(), fixtureView({ savedRevision: 'rev-2', geometry: geometryWithB(333, 444) })]
    await open()
    expect(pos('b')).toEqual({ x: 40, y: 140 })
    expect(screen.queryByText('mcp-bot')).toBeNull()

    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    await settle()
    expect(screen.getByText('mcp-bot')).toBeInTheDocument()
    expect(mode()).toBe('arranged')
    expect(pos('b')).toEqual({ x: 333, y: 444 })
    // focus refetch (+ at most one reconcile if the two responses landed in separate renders)
    expect(count('GET', WF_URL)).toBeGreaterThanOrEqual(2)
    expect(count('GET', WF_URL)).toBeLessThanOrEqual(3)
    expect(writes()).toEqual([])
  })

  it('refetches the workflow when the document becomes visible again, not while hidden', async () => {
    await open()
    expect(count('GET', WF_URL)).toBe(1)
    let visibility: DocumentVisibilityState = 'hidden'
    const spy = vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
    try {
      await act(async () => {
        document.dispatchEvent(new Event('visibilitychange'))
      })
      await settle()
      expect(count('GET', WF_URL)).toBe(1)

      visibility = 'visible'
      await act(async () => {
        document.dispatchEvent(new Event('visibilitychange'))
      })
      await settle()
      expect(count('GET', WF_URL)).toBe(2)
      expect(count('GET', LAYOUT_URL)).toBe(2)
    } finally {
      spy.mockRestore()
    }
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — legacy workflows', () => {
  it('renders a pre-feature workflow from its own manifest positions with no writes and no status banner', async () => {
    const positions = { a: { x: 0, y: 0 }, b: { x: 0, y: 160 }, c: { x: 0, y: 320 } }
    const def = fixtureWorkflow()
    server.workflow = [
      {
        ...def,
        presentation: undefined,
        nodes: def.nodes.map((n) => ({ ...n, position: positions[n.id as 'a' | 'b' | 'c'] })),
      },
    ]
    server.layout = [
      fixtureView({
        savedRevision: '',
        jobGeneration: 0,
        resultGeneration: 0,
        state: undefined,
        displayStatus: 'legacy',
        stage: undefined,
        attemptsUsed: 0,
        attemptLimit: 0,
        findings: [],
        contract: undefined,
        geometry: undefined,
        provenance: 'legacy',
        retained: { source: 'manifest', positions },
      }),
    ]
    await open()
    expect(mode()).toBe('fallback')
    expect(pos('a')).toEqual({ x: 0, y: 0 })
    expect(pos('b')).toEqual({ x: 0, y: 160 })
    expect(pos('c')).toEqual({ x: 0, y: 320 })
    expect(screen.queryByRole('status')).toBeNull()

    await settle(POLL_INTERVAL_MS * 10)
    expect(count('GET', LAYOUT_URL)).toBe(1)
    expect(count('GET', WF_URL)).toBe(1)
    expect(writes()).toEqual([])
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — navigation, and no manual positioning', () => {
  it('starts at DEFAULT_ZOOM centred on the entry node, without fitView', async () => {
    const def = fixtureWorkflow()
    server.workflow = [{ ...def, nodes: def.nodes.map((n) => ({ ...n, entrypoint: n.id === 'b' })) }]
    await open()
    // b box: x 40, y 140, w 200, h 70 → centre (140, 175)
    expect(flow.setCenter).toHaveBeenCalledTimes(1)
    expect(flow.setCenter).toHaveBeenCalledWith(140, 175, { zoom: 0.9 })
    expect(flow.fitView).not.toHaveBeenCalled()
    expect(rf().fitView).toBeUndefined()
    expect(rf().fitViewOptions).toBeUndefined()
  })

  it('shows a layout placeholder until the first status arrives, so the first centring is on the drawn layout', async () => {
    const first = deferred<LayoutView>()
    server.layout = [first]
    await open()
    expect(screen.getByText('Loading layout...')).toBeInTheDocument()
    expect(flow.props).toBeNull()
    expect(flow.setCenter).not.toHaveBeenCalled()

    await act(async () => {
      first.resolve(fixtureView())
    })
    await settle()
    expect(mode()).toBe('arranged')
    // a (entry) box: x 40, y 40, w 180, h 52 → centre (130, 66)
    expect(flow.setCenter).toHaveBeenCalledTimes(1)
    expect(flow.setCenter).toHaveBeenCalledWith(130, 66, { zoom: 0.9 })
  })

  it('offers an explicit Fit all control', async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: 'Fit whole workflow' }))
    expect(flow.fitView).toHaveBeenCalledTimes(1)
    expect(flow.fitView).toHaveBeenCalledWith({ padding: 0.1 })
  })

  it('never lets nodes be dragged and never writes positions (no PUT, no saveLayout)', async () => {
    await open()
    expect(rf().nodesDraggable).toBe(false)
    expect(rf().onNodeDragStop).toBeUndefined()
    expect(rf().onNodeDrag).toBeUndefined()
    expect(rf().onNodeDragStart).toBeUndefined()
    expect(rf().nodes.every((n) => n.draggable === false)).toBe(true)

    fireEvent.click(screen.getByTestId('node-a'))
    fireEvent.click(screen.getByTestId('node-c'))
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    await settle(POLL_INTERVAL_MS * 5)
    expect(server.calls.some((c) => c.method === 'PUT')).toBe(false)
    expect(writes()).toEqual([])
  })
})

// ---------------------------------------------------------------------------
describe('WorkflowReview — errors', () => {
  it('shows the load error and never polls the layout when the workflow cannot be read', async () => {
    server.workflow = [new HttpError(404, 'workflow not found: wf')]
    await open()
    expect(screen.getByText('API 404: workflow not found: wf')).toBeInTheDocument()
    await settle(POLL_INTERVAL_MS * 3)
    expect(count('GET', LAYOUT_URL)).toBe(0)
  })

  it('keeps the manifest fallback when the status read fails; Retry then reloads instead of retrying another revision', async () => {
    server.layout = [new HttpError(503, 'layout: status store unavailable'), fixtureView()]
    await open()
    expect(status()).toHaveTextContent('Arrangement status unavailable')
    expect(status()).toHaveTextContent('Could not refresh arrangement status: API 503: internal server error')
    expect(mode()).toBe('fallback')

    fireEvent.click(retryButton()!)
    await settle()
    expect(writes()).toEqual([])
    expect(count('GET', LAYOUT_URL)).toBe(2)
    expect(count('GET', WF_URL)).toBe(2)
    expect(mode()).toBe('arranged')
    expect(status()).toHaveTextContent('Layout reviewed')

    await settle(POLL_INTERVAL_MS * 5)
    expect(count('GET', LAYOUT_URL)).toBe(2)
  })
})
```

- [x] **Step 2: Run it and confirm it fails** (against the post-P3T3 `WorkflowReview.tsx`, which still has its own `<ReactFlow>`, drag handler and `fitView`)

```bash
cd dashboard && npx vitest run src/routes/WorkflowReview.test.tsx
```

Expected: every test fails except "shows the load error and never polls the layout…", which the old code already satisfies. Representative failures:

```text
 FAIL  src/routes/WorkflowReview.test.tsx > WorkflowReview — shared renderer fed by the fetched layout (provenance) > draws the nodes at the geometry returned by GET /workflows/{name}/layout
AssertionError: expected +0 to be 1 // Object.is equality
 FAIL  … > WorkflowReview — navigation, and no manual positioning > never lets nodes be dragged and never writes positions (no PUT, no saveLayout)
AssertionError: expected true to be false // Object.is equality
 FAIL  … > offers an explicit Fit all control
TestingLibraryElementError: Unable to find an accessible element with the role "button" and name "Fit whole workflow"
 FAIL  … > shows needs_attention with findings, the best layout and Retry, never as complete
TestingLibraryElementError: Unable to find an accessible element with the role "status"
      Tests  22 failed | 1 passed (23)
```

- [x] **Step 3: Implement.** Replace the entire contents of `dashboard/src/routes/WorkflowReview.tsx` with the code below. `ReviewSidePanel` is unchanged from base lines 117-217. Everything else is new. `collectPositions`, the old `ReviewGraph`, `onNodeDragStop`, both `api.workflows.saveLayout` calls, `nodesDraggable`, `fitView` and `fitViewOptions` are deleted.

```tsx
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { WorkflowDefinition, WorkflowNode } from '../graph/types'
import type { Me } from '../api/types'
import { api } from '../api/client'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import LayoutStatusPanel from '../graph/arranged/LayoutStatusPanel'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'
import { useLayoutStatus } from '../hooks/useLayoutStatus'
import ResizablePanel from '../components/ResizablePanel'
import UsageStats from '../components/UsageStats'
import SchedulesPanel from '../components/SchedulesPanel'
import TriggerModal from '../components/TriggerModal'
import styles from './WorkflowReview.module.css'

/**
 * Whether the caller may retry arrangement of this workflow's saved revision.
 * Mirrors the server's retry authorisation (Service.AuthorizeLayoutRetry, which
 * shares Service.Update's policy): a private workflow is the caller's own and
 * writable; a shared workflow is writable only by an admin (edit-in-place); a
 * legacy (unstamped) workflow has no revision to retry. The server re-checks
 * every retry — this only hides a control the server would refuse.
 */
function canRetryLayout(def: WorkflowDefinition, me: Me | null): boolean {
  switch (def.presentation?.scope) {
    case 'private':
      return true
    case 'shared':
      return me?.isAdmin === true
    default:
      return false
  }
}

function ReviewSidePanel({
  workflowDef,
  selectedNode,
}: {
  workflowDef: WorkflowDefinition
  selectedNode: WorkflowNode | null
}) {
  return (
    <div className={styles.sidePanel}>
      {/* Usage Stats */}
      <div className={styles.section}>
        <UsageStats workflowName={workflowDef.name} />
      </div>

      {/* Schedules (read-only) */}
      <div className={styles.section}>
        <SchedulesPanel workflowName={workflowDef.name} />
      </div>

      {/* Input Schema */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Input Schema</div>
        {workflowDef.inputs && workflowDef.inputs.length > 0 ? (
          <div className={styles.inputList}>
            {workflowDef.inputs.map((input) => (
              <div key={input.name} className={styles.inputItem}>
                <span className={styles.inputName}>{input.name}</span>
                <span className={styles.inputType}>{input.type}</span>
                {input.required && (
                  <span className={styles.inputRequired}>required</span>
                )}
                {input.description && (
                  <span className={styles.inputDesc}>
                    - {input.description}
                  </span>
                )}
              </div>
            ))}
          </div>
        ) : (
          <div className={styles.emptySection}>No inputs defined.</div>
        )}
      </div>

      {/* Node Details */}
      <div className={styles.section}>
        <div className={styles.sectionTitle}>Node Details</div>
        {selectedNode ? (
          <>
            <div className={styles.nodeDetail}>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>ID</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.id}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Type</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.type}
                </span>
              </div>
              <div className={styles.nodeDetailRow}>
                <span className={styles.nodeDetailLabel}>Label</span>
                <span className={styles.nodeDetailValue}>
                  {selectedNode.label}
                </span>
              </div>
              {selectedNode.description && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Description</span>
                  <span className={styles.nodeDetailValue}>
                    {selectedNode.description}
                  </span>
                </div>
              )}
              {selectedNode.entrypoint && (
                <div className={styles.nodeDetailRow}>
                  <span className={styles.nodeDetailLabel}>Entrypoint</span>
                  <span className={styles.nodeDetailValue}>Yes</span>
                </div>
              )}
            </div>
            {Object.keys(selectedNode.config).length > 0 && (
              <div className={styles.configBlock}>
                <div className={styles.sectionTitle}>Config</div>
                <pre className={styles.configPre}>
                  {JSON.stringify(selectedNode.config, null, 2)}
                </pre>
              </div>
            )}
          </>
        ) : (
          <div className={styles.emptySection}>
            Click a node to view its details.
          </div>
        )}
      </div>
    </div>
  )
}

interface ReviewPageProps {
  name: string
  workflowDef: WorkflowDefinition
  /** Silently re-reads the manifest (keeps the current one on failure). Stable identity. */
  onReloadWorkflow: () => void
}

/**
 * The loaded workflow page. Owns the revision-scoped arrangement status and
 * joins it with the manifest by saved revision: geometry/status are shown only
 * for the manifest's own revision, never for another one.
 */
function ReviewPage({ name, workflowDef, onReloadWorkflow }: ReviewPageProps) {
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [showTrigger, setShowTrigger] = useState(false)
  const [me, setMe] = useState<Me | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .me()
      .then((m) => {
        if (!cancelled) setMe(m)
      })
      .catch(() => {
        // Unknown identity: admin-only Retry stays hidden; the server still decides.
      })
    return () => {
      cancelled = true
    }
  }, [])

  const layout = useLayoutStatus(
    () => api.workflows.getLayout(name),
    (expectedRevision) => api.workflows.retryLayout(name, expectedRevision),
    name,
  )
  const { view, refresh: refreshLayout, retry: retryLayout } = layout

  // Join manifest and layout by saved revision. A pre-feature manifest has no
  // presentation and its legacy view has savedRevision "", so they still join.
  const manifestRevision = workflowDef.presentation?.revision ?? ''
  const viewRevision = view?.savedRevision ?? null
  const joinedView = view != null && view.savedRevision === manifestRevision ? view : null

  // A save raced the two reads: re-read both sides once per mismatched
  // (manifest, layout) revision pair. Whichever side was stale catches up; if
  // neither changes, the pair is unchanged and nothing is fetched again.
  const reconciledPair = useRef<string | null>(null)
  useEffect(() => {
    if (viewRevision == null || viewRevision === manifestRevision) return
    const pair = `${manifestRevision} -> ${viewRevision}`
    if (reconciledPair.current === pair) return
    reconciledPair.current = pair
    onReloadWorkflow()
    refreshLayout()
  }, [manifestRevision, viewRevision, onReloadWorkflow, refreshLayout])

  const [renderIssues, setRenderIssues] = useState<readonly string[]>([])
  const displayIssues = useMemo(
    () => [...resolveDisplayGeometry(workflowDef, joinedView).issues, ...renderIssues],
    [workflowDef, joinedView, renderIssues],
  )
  const anchorNodeId = useMemo(() => selectAnchorNodeId(null, workflowDef, {}, []), [workflowDef])

  const handleNodeClick = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId)
  }, [])

  const handleRetry = useCallback(() => {
    if (joinedView) {
      // useLayoutStatus posts joinedView.savedRevision, i.e. the manifest's revision.
      void retryLayout()
      return
    }
    // No status for this manifest's revision: reload instead of retrying another revision.
    refreshLayout()
    onReloadWorkflow()
  }, [joinedView, retryLayout, refreshLayout, onReloadWorkflow])

  const selectedNode =
    workflowDef.nodes.find((n) => n.id === selectedNodeId) ?? null

  // Wait for the first status response so the first centring is on the layout
  // actually displayed (no dagre flash that then jumps to the arranged result).
  const graphReady = view != null || !layout.loading

  const errorBehavior = workflowDef.defaultErrorBehavior
    ? Object.entries(workflowDef.defaultErrorBehavior)
        .map(([k, v]) => `${k}: ${String(v)}`)
        .join(', ')
    : 'none'

  return (
    <ReactFlowProvider>
      <div className={styles.page}>
        {/* Breadcrumb / metadata bar */}
        <div className={styles.breadcrumb}>
          <Link to="/workflows" className={styles.breadcrumbLink}>
            Workflows
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>{workflowDef.name}</span>

          <span className={styles.metaItem}>
            Author: <span className={styles.metaValue}>{workflowDef.author}</span>
          </span>
          <span className={styles.metaItem}>
            Nodes:{' '}
            <span className={styles.metaValue}>
              {workflowDef.nodes.length}
            </span>
          </span>
          <span className={styles.metaItem}>
            Error:{' '}
            <span className={styles.metaValue}>{errorBehavior}</span>
          </span>

          <div className={styles.spacer} />

          <button
            className={styles.triggerButton}
            onClick={() => setShowTrigger(true)}
          >
            Trigger
          </button>
        </div>

        {/* Arrangement / review status for this saved revision */}
        <LayoutStatusPanel
          view={joinedView}
          error={joinedView ? layout.error : (viewRevision != null && viewRevision !== manifestRevision ? 'Arrangement revision changed; reload to refresh this workflow' : layout.error)}
          onRetry={handleRetry}
          retrying={layout.retrying}
          retryError={layout.retryError}
          canRetry={canRetryLayout(workflowDef, me)}
          displayIssues={displayIssues}
        />

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <div className={styles.graphContainer}>
              {graphReady ? (
                <ArrangedGraph
                  workflowDef={workflowDef}
                  view={joinedView}
                  onDisplayIssues={setRenderIssues}
                  selectedNodeId={selectedNodeId}
                  onNodeClick={handleNodeClick}
                  anchorNodeId={anchorNodeId}
                />
              ) : (
                <div className={styles.loading}>Loading layout...</div>
              )}
            </div>
          }
          right={
            <ReviewSidePanel
              workflowDef={workflowDef}
              selectedNode={selectedNode}
            />
          }
        />

        {/* Trigger modal */}
        {showTrigger && (
          <TriggerModal
            workflowName={name}
            inputs={workflowDef.inputs ?? []}
            onClose={() => setShowTrigger(false)}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}

export default function WorkflowReview() {
  const { name } = useParams<{ name: string }>()
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(
    null,
  )
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Newest response wins; responses for a previous route name are dropped.
  const mountedEpoch = useRef(0)
  const currentName = useRef(name)
  currentName.current = name
  const issued = useRef(0)
  const applied = useRef(0)

  /**
   * Initial load shows loading/error. A reload (focus, visible, revision
   * mismatch) is silent: it replaces the manifest on success and keeps the
   * current one on failure, so external HTTP/MCP saves appear without flicker.
   */
  const loadWorkflow = useCallback(
    async (initial: boolean) => {
      if (!name) return
      const seq = ++issued.current
      const epoch = mountedEpoch.current
      const stale = () => mountedEpoch.current !== epoch || currentName.current !== name || seq < applied.current
      try {
        const data = await api.workflows.get(name)
        if (stale()) return
        applied.current = seq
        setWorkflowDef(data)
        setError(null)
      } catch (err) {
        if (stale()) return
        applied.current = seq
        if (initial) {
          setError(err instanceof Error ? err.message : 'Failed to load workflow')
        }
      } finally {
        if (initial && !stale()) setLoading(false)
      }
    },
    [name],
  )

  useEffect(() => {
    ++mountedEpoch.current
    setWorkflowDef(null)
    setError(null)
    setLoading(true)
    void loadWorkflow(true)

    const onFocus = () => void loadWorkflow(false)
    const onVisibility = () => {
      if (document.visibilityState === 'visible') void loadWorkflow(false)
    }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      ++mountedEpoch.current
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [loadWorkflow])

  const reloadWorkflow = useCallback(() => {
    void loadWorkflow(false)
  }, [loadWorkflow])

  if (!name) {
    return <div className={styles.error}>Workflow not found</div>
  }

  if (loading) {
    return <div className={styles.loading}>Loading workflow...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!workflowDef) {
    return <div className={styles.error}>Workflow not found</div>
  }

  return (
    <ReviewPage
      key={name}
      name={name}
      workflowDef={workflowDef}
      onReloadWorkflow={reloadWorkflow}
    />
  )
}
```

- [x] **Step 4: Run it, the type check, and confirm it passes**

Execution: initial RED was 31 failed / 1 passed. GREEN is 34 passed; `npx tsc --noEmit` exits 0. Additional RED probes exposed recurring-pair reconciliation and focus beating the initial manifest read; the route now remembers every reconciled pair and finishes loading for the winning response. Existing legacy no-autosave/selection coverage was retained through the real API seam.

```bash
cd dashboard && npx vitest run src/routes/WorkflowReview.test.tsx && npx tsc --noEmit
```

Expected:

```text
 ✓ src/routes/WorkflowReview.test.tsx (23 tests)
 Test Files  1 passed (1)
      Tests  23 passed (23)
(tsc prints nothing and exits 0)
```

- [x] **Step 5: Mutation check (anti-green-theater; do not commit the mutations).** Apply each mutation alone, run `cd dashboard && npx vitest run src/routes/WorkflowReview.test.tsx`, confirm the named test fails, then restore with a temporary-file backup of `dashboard/src/routes/WorkflowReview.tsx` taken before the mutation:
  - `view={joinedView}` → `view={null}` on `<ArrangedGraph>` (renderer ignores the fetched view): "draws the nodes at the geometry returned by GET /workflows/{name}/layout" fails.
  - `const joinedView = view != null && view.savedRevision === manifestRevision ? view : null` → `const joinedView = view` (no revision join): "refetches the workflow once when the layout is for a newer revision…" and "does not loop when the manifest and layout revisions stay different" fail (`drewArrangedB(901, 902)` / an `arrangedNode` frame appears).
  - Delete `onReloadWorkflow()` from the reconcile effect: "refetches the workflow once…" fails (`expected 1 to be 2`).
  - Delete `if (reconciledPair.current === pair) return` **and** change `onReloadWorkflow={reloadWorkflow}` to `onReloadWorkflow={() => void loadWorkflow(false)}`: "does not loop…" fails (the GET count keeps growing).
  - `case 'shared': return me?.isAdmin === true` → `return true`: "hides Retry on a shared workflow for a non-admin" fails.
  - Delete `window.addEventListener('focus', onFocus)`: "refetches workflow metadata and layout on window focus…" fails (`mcp-bot` not found).
  - Replace `const graphReady = view != null || !layout.loading` with `const graphReady = true`: "shows a layout placeholder until the first status arrives…" fails.

- [x] **Step 6: Commit**

Ordered self-review: (1) checked this route-only slice against revision join, permissions, polling, first centring, selection and refresh requirements; (2) all seven isolated mutations failed the named consumer tests and were restored (`/tmp/p3t12-mutation-{1..7}.log`); fresh HTTP JSON objects and same-revision focus metadata prevent false positives from reference reuse or indirect reconciliation; (3) checked neighboring renderer/hook patterns, retained the side panel unchanged, and passed diff checks. Final pre-commit route/typecheck rerun: 34 passed / exit 0. Real DOM/path release parity remains assigned to P3T14, not claimed by this leaf-ReactFlow suite.

```bash
git add dashboard/src/routes/WorkflowReview.tsx dashboard/src/routes/WorkflowReview.test.tsx && git commit -m "feat(dashboard): render WorkflowReview through the shared arranged renderer with revision-joined layout status" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12.2: Remove `api.workflows.saveLayout`

**Files:**
- Modify: `dashboard/src/api/client.ts:197-204` (base numbering). P3T1 adds 28 lines above it, so after P3T1 it is `:225-232`; locate it by the comment `/** Save node positions for a workflow layout. */`. Delete the method. The `put` helper (`:60-66`) stays, because `system.updateQueueConfig`/`updateTeamsConfig` use it.
- Test: `dashboard/src/api/client.test.ts`. Append after the last `describe` (P3T1's `describe('api layout endpoints', …)`). The base file has **no** `saveLayout` tests to delete. These regression tests replace them.

**Interfaces**
- Produces: `api.workflows` = `{ list, get, trigger, getLayout, retryLayout, getStats }`. `saveLayout` no longer exists. No client code can issue `PUT /api/v1/workflows/{name}/layout`; P3T14 removes the server route.

- [x] **Step 1: Write the failing test.** Append to `dashboard/src/api/client.test.ts` (the file already imports `describe, it, expect` and `api`):

```ts

describe('api.workflows — manual layout writes removed', () => {
  it('no longer exposes saveLayout', () => {
    expect('saveLayout' in api.workflows).toBe(false)
  })

  it('exposes only read, trigger, stats and arrangement-status methods for workflows', () => {
    expect(Object.keys(api.workflows).sort()).toEqual(['get', 'getLayout', 'getStats', 'list', 'retryLayout', 'trigger'])
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

Execution: 2 failed / 30 passed; both manual-write API surface assertions fail behaviorally.

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```

Expected:

```text
 FAIL  src/api/client.test.ts > api.workflows — manual layout writes removed > no longer exposes saveLayout
AssertionError: expected true to be false // Object.is equality
 FAIL  src/api/client.test.ts > api.workflows — manual layout writes removed > exposes only read, trigger, stats and arrangement-status methods for workflows
AssertionError: expected [ 'get', 'getLayout', …(5) ] to deeply equal [ 'get', 'getLayout', …(4) ]
      Tests  2 failed | 20 passed (22)
```

- [x] **Step 3: Implement.** In `dashboard/src/api/client.ts`, delete this block (base `:197-204`) entirely, including its trailing blank line:

```ts
    /** Save node positions for a workflow layout. */
    saveLayout(
      name: string,
      positions: Record<string, { x: number; y: number }>,
    ): Promise<void> {
      return put(`/api/v1/workflows/${name}/layout`, { positions })
    },

```

After the edit, `workflows` reads (P3T1's methods included):

```ts
  workflows: {
    /** List all available workflows. */
    list(): Promise<WorkflowSummary[]> {
      return request('/api/v1/workflows')
    },

    /** Get a workflow definition by name. */
    get(name: string): Promise<WorkflowDefinition> {
      return request(`/api/v1/workflows/${name}`)
    },

    /** Trigger a new workflow instance. */
    trigger(
      name: string,
      variables?: Record<string, unknown>,
      priority?: Priority,
    ): Promise<{ instanceId: string; state: string }> {
      return post(`/api/v1/workflows/${name}/trigger`, {
        variables: variables ?? {},
        priority: priority ?? 'normal',
      })
    },

    /** Get arrangement status and best/fallback geometry for the workflow's current saved revision. */
    getLayout(name: string): Promise<LayoutView> {
      return request(`/api/v1/workflows/${name}/layout`)
    },

    /**
     * Re-run arrangement for the workflow's current saved revision. A stale
     * `expectedRevision` is rejected with 409; the server never waits for review.
     */
    retryLayout(name: string, expectedRevision: string): Promise<RetryAck> {
      return post(`/api/v1/workflows/${name}/layout/retry`, { expectedRevision })
    },

    /** Get usage stats for a workflow over a date range. */
    getStats(name: string, from: string, to: string): Promise<WorkflowStats> {
      return request(
        `/api/v1/workflows/${name}/stats?from=${from}&to=${to}`,
      )
    },
  },
```

- [x] **Step 4: Run it, the type check, and confirm it passes**

Execution: 66 tests passed across the two files; TypeScript exits 0.

```bash
cd dashboard && npx vitest run src/api/client.test.ts src/routes/WorkflowReview.test.tsx && npx tsc --noEmit
```

Expected:

```text
 ✓ src/api/client.test.ts (22 tests)
 ✓ src/routes/WorkflowReview.test.tsx (23 tests)
 Test Files  2 passed (2)
      Tests  45 passed (45)
(tsc prints nothing and exits 0. A leftover saveLayout caller would fail with "Property 'saveLayout' does not exist".)
```

- [x] **Step 5: Commit**

Ordered self-review: (1) deleted only saveLayout, preserving put and all remaining workflow APIs; (2) observed both API deletion assertions fail before removal, plus the real route asserts no position writes across selection/focus/polling; (3) checked the exact diff and typecheck for leftover callers. Focused rerun: 66 passed / TypeScript exit 0.

```bash
git add dashboard/src/api/client.ts dashboard/src/api/client.test.ts && git commit -m "refactor(dashboard): remove workflows.saveLayout; no client path writes node positions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12.3: Verify client-side removal (the precondition P3T14 Task 14.2 Step 1 checks) and the full suite

**Files:** none modified (verification only).

- [x] **Step 1: No manual-positioning code remains in production sources** (the exact grep P3T14 runs, from the repo root)

```bash
grep -rn "saveLayout\|onNodeDragStop\|needsAutoSave" dashboard/src --include=*.ts --include=*.tsx | grep -v '\.test\.'
```

Expected: **no output**. If `dashboard/src/routes/ExecutionView.tsx` prints, P3T13 is not merged or regressed; the base ExecutionView has none of these symbols.

```bash
grep -n "fitView\|nodesDraggable\|<ReactFlow\b\|useGraphLayout\|/layout\`, *{ *positions" dashboard/src/routes/WorkflowReview.tsx dashboard/src/api/client.ts
```

Expected: **no output**. WorkflowReview draws only through `ArrangedGraph`, and no client code PUTs positions.

- [x] **Step 2: Full dashboard suite and type check**

Execution: both exact removal greps produced no output. `npx tsc --noEmit && npx vitest run` passed all 990 tests / 68 files on the final full rerun (`/tmp/p3t12-full-suite-rerun.log`). The initial full run had one unrelated ArrangedGraph centring-test timing failure (989 passed); no assertion or production behavior there was changed. A concurrent verification attempt during make ui's npm dependency reinstall could not find TypeScript libraries; the successful final verification ran sequentially after make ui completed.

`make ui` passed and refreshed the embedded SPA (including the shared renderer/font chunks); generated assets are committed with this task as required by AGENTS.md. Final ordered self-review: (1) this task changes only WorkflowReview, the manual-write client API, their tests, plan and required embedded build; the actual App route and authorised GET/POST layout producer already exist and are fed; (2) real client/hook/panel/graph sink tests and restored mutations establish data feeding, not browser readability (P3T14 owns that gate); (3) diff/typecheck checks pass, side-panel behavior is preserved, no manual-positioning remnants or unrelated visuals are staged. Earlier tasks' recorded protocol/release blockers remain unchanged.

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```

Expected: `tsc` prints nothing. Vitest reports every file passed, e.g. `Test Files  <n> passed (<n>)` and `Tests  <m> passed (<m>)`, with no `failed` line. `src/routes/WorkflowReview.test.tsx (23 tests)` and `src/api/client.test.ts (22 tests)` are among them.

No commit (nothing changed).

---

## Wired-and-fed

| Item | Composition point (wiring) | Data source (producer) | Test through the real site |
|---|---|---|---|
| `ArrangedGraph` (P3T3) in the workflow view | `dashboard/src/routes/WorkflowReview.tsx:ReviewPage`, rendered by the default export, mounted at `dashboard/src/App.tsx:22` (`/workflows/:name`) | `view` = `useLayoutStatus(...).view` joined by revision ← `api.workflows.getLayout` (P3T1) ← `GET /api/v1/workflows/{name}/layout` `LayoutHandler.GetWorkflowLayout` (P3T9) ← `layout.QueryService.WorkflowLayout` (P3T8) ← `layout.Reader.View` (P2T5) over the `layout.Store` row/result written by `layout.Worker` (P2T8) with geometry from `layout.Arrange`/`Coordinator.Run` (P1T6/P1T9). `workflowDef` ← `api.workflows.get` ← `WorkflowHandler.Get` (manifest incl. `presentation` stamped by `RevisionStamper.Stamp`, P2T2/P2T3) | "draws the nodes at the geometry returned by GET /workflows/{name}/layout" (provenance probe: fails if the route ignored or hard-coded the view) |
| `useLayoutStatus` (P3T11) | same; `fetcher = () => api.workflows.getLayout(name)`, `retrier = rev => api.workflows.retryLayout(name, rev)`, `key = name` | as above; retry → `POST …/layout/retry` → `QueryService.RetryWorkflow` (P3T8) → `layout.Requester.Retry` (P2T5) | polling, Retry, error tests (counts and bodies taken from the recorded `fetch` calls) |
| `LayoutStatusPanel` (P3T11) | same; `canRetry = canRetryLayout(def, me)` | `presentation.scope` (P2T2 `Presentation.Scope`) + `api.me().isAdmin` (`GET /api/v1/me`, existing) | needs_attention / Retry visibility tests |
| `displayIssues` (P3T1 `resolveDisplayGeometry`) | same | the joined view's geometry vs this build's `contract.json` (P1T2) | "surfaces client-side validation issues…" |
| Removal of `saveLayout`/drag/auto-save | `dashboard/src/api/client.ts`, `WorkflowReview.tsx` | n/a: node positions are produced only by automatic arrangement (P2T8 worker) | "never lets nodes be dragged and never writes positions…", "renders a pre-feature workflow… with no writes", client regression tests, Task 12.3 greps |

## Notes for sibling tasks

- **P3T13 (ExecutionView):** the reusable test pattern is a `fetch` URL/method router that records calls, plus a leaf `@xyflow/react` mock that overrides only `ReactFlow`/`ReactFlowProvider`/`Background`/`Controls`/`useReactFlow` over `vi.importActual`. Tests use fake timers and an act-wrapped `advanceTimersByTimeAsync` settle loop, not `waitFor`/`findBy`, which do not see vitest fake timers. `api.workflows.saveLayout` no longer exists. P3T14's grep also fails on any `onNodeDragStop` in `ExecutionView.tsx`.
- **P3T14:** after this task, the grep in Task 14.2 Step 1 prints nothing from WorkflowReview/client (Task 12.3 Step 1 runs the identical command).

### Route integration negatives

Before GREEN add available=false after complete (geometry retained, unavailable panel,
backoff), RetryAck/new generation followed by old terminal GET, and navigation A→B→A
with old A promises still in flight. Route epoch must fence those promises even when the
name matches again. A permanently mismatched manifest/status pair displays availability
error and reload/Retry, not an endless “Checking” banner. The same joined value reaches
both renderer and panel. Assert routes draw the full routed paths/labels in P3T14; the
leaf ReactFlow fake proves data feeding only.

**Async font sink:** Add onDisplayIssues={setRenderIssues} to the real graph, merge these
issues with resolveDisplayGeometry issues and pass them to LayoutStatusPanel. Tests await
font readiness through fake timers before asserting reviewed wording; missing font/cmap
and stale-font promise controls must assert fallback and no “Layout reviewed”. Real assets
are mandatory in P3T14; the unit font fake cannot stand in for the release gate.
