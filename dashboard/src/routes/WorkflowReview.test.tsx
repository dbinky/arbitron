import { render, screen, fireEvent, act, cleanup } from '@testing-library/react'
import type { ReactNode } from 'react'
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { Me } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { Geometry, LayoutFinding, LayoutView, RetryAck } from '../graph/arranged/types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'
import { POLL_INTERVAL_MS } from '../hooks/useLayoutStatus'
import { RENDERER_CONTRACT } from '../graph/arranged/validate'

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
  if (!texts.length || !texts.every(t => typeof t === 'string') || contract.version !== 'arranged-renderer/2' || contract.fontFamily !== 'Inter, Noto Sans SC, Noto Emoji') throw new Error('invalid font request')
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
    json: async () => structuredClone(body),
  } as Response
}

function isDeferred<T>(r: Reply<T>): r is Deferred<T> {
  return typeof r === 'object' && r !== null && 'promise' in r && 'resolve' in r
}

async function serve<T>(key: string, replies: Array<Reply<T>>): Promise<Response> {
  const n = server.served.get(key) ?? 0
  server.served.set(key, n + 1)
  // Bound runaway mutations while preserving fresh objects from every HTTP read.
  if (n >= 30) return json({ error: `request budget exhausted for ${key}` }, 500)
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
  if (method === 'GET' && url === '/api/v1/workflows/other') return json({ ...fixtureWorkflow(), name: 'other' })
  if (method === 'GET' && url === '/api/v1/workflows/other/layout') return json(fixtureView({ geometry: geometryWithB(88, 99) }))
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
      <Link to="/workflows/other">Other workflow</Link>
      <Link to="/workflows/wf">Original workflow</Link>
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
    expect(fontBoundary.load).toHaveBeenCalledWith(document, ['Start', 'script', 'Draft a very long label that must wrap', 'llm call', 'Check', 'conditional', 'yes', 'again'], RENDERER_CONTRACT)
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
    geo.contract = { ...geo.contract, version: 'arranged-renderer/1' }
    server.layout = [fixtureView({ geometry: geo })]
    await open()
    expect(mode()).toBe('fallback')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).toHaveTextContent('Layout needs attention')
    expect(status()).toHaveTextContent(
      'This layout could not be displayed: renderer contract version "arranged-renderer/1" does not match "arranged-renderer/2"',
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
    expect(status()).toHaveTextContent('Arrangement status unavailable')
    expect(retryButton()).toBeInTheDocument()
    expect(flow.history.some((frame) => frame.some((n) => n.type === 'arrangedNode'))).toBe(false)
    fireEvent.click(retryButton()!)
    await settle()
    expect(writes()).toEqual([])
    expect(count('GET', WF_URL)).toBe(3)
    expect(count('GET', LAYOUT_URL)).toBe(3)
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
    // A same-revision metadata refresh cannot rely on mismatch reconciliation.
    server.workflow.push(withRevision(fixtureWorkflow(), 'rev-2', { author: 'http-bot' }))
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(screen.getByText('http-bot')).toBeInTheDocument()
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
describe('WorkflowReview — async input fences', () => {
  it('retains geometry during an in-band outage, shows availability and backs off', async () => {
    server.layout = [fixtureView({ geometry: geometryWithB(321, 654) }), fixtureView({
      available: false, availabilityError: 'status store unavailable', geometry: undefined,
      jobGeneration: 0, resultGeneration: 0,
    }), fixtureView({ available: false, availabilityError: 'status store unavailable', geometry: undefined }),
      fixtureView({ geometry: geometryWithB(987, 765) })]
    await open()
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(pos('b')).toEqual({ x: 321, y: 654 })
    expect(status()).toHaveTextContent('status store unavailable')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(retryButton()).toBeInTheDocument()
    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(3)
    expect(pos('b')).toEqual({ x: 321, y: 654 })
    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(3)
    expect(status()).not.toHaveTextContent('Layout reviewed')
    await settle(POLL_INTERVAL_MS)
    expect(count('GET', LAYOUT_URL)).toBe(4)
    expect(pos('b')).toEqual({ x: 987, y: 765 })
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('does not let an old terminal GET undo a RetryAck or stop new-generation polling', async () => {
    server.layout = [needsAttentionView(), fixtureView({ geometry: geometryWithB(11, 12) }),
      fixtureView({ jobGeneration: 2, resultGeneration: 2, geometry: geometryWithB(123, 456) })]
    server.retry = [{ savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }]
    await open()
    fireEvent.click(retryButton()!)
    await settle()
    expect(status()).toHaveTextContent('Arrangement pending')
    expect(drewArrangedB(11, 12)).toBe(false)
    expect(pos('b')).toEqual({ x: 40, y: 140 })
    await settle(POLL_INTERVAL_MS)
    expect(pos('b')).toEqual({ x: 123, y: 456 })
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('drops an old manifest response after navigation A → B → A', async () => {
    const old = deferred<WorkflowDefinition>()
    server.workflow = [old, { ...fixtureWorkflow(), author: 'new A' }]
    await open()
    fireEvent.click(screen.getByText('Other workflow'))
    await settle()
    expect(pos('b')).toEqual({ x: 88, y: 99 })
    fireEvent.click(screen.getByText('Original workflow'))
    await settle()
    expect(screen.getByText('new A')).toBeInTheDocument()
    await act(async () => old.resolve({ ...fixtureWorkflow(), author: 'stale A' }))
    await settle()
    expect(screen.queryByText('stale A')).toBeNull()
    expect(screen.getByText('new A')).toBeInTheDocument()
    expect(pos('b')).toEqual({ x: 40, y: 140 })
  })

  it('drops an old layout response after navigation A → B → A', async () => {
    const old = deferred<LayoutView>()
    server.layout = [old, fixtureView({ geometry: geometryWithB(123, 456) })]
    await open()
    fireEvent.click(screen.getByText('Other workflow'))
    await settle()
    expect(pos('b')).toEqual({ x: 88, y: 99 })
    fireEvent.click(screen.getByText('Original workflow'))
    await settle()
    await act(async () => old.resolve(fixtureView({ geometry: geometryWithB(11, 12) })))
    await settle()
    expect(pos('b')).toEqual({ x: 123, y: 456 })
    expect(drewArrangedB(11, 12)).toBe(false)
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('finishes initial loading when a newer focus read wins the first manifest race', async () => {
    const old = deferred<WorkflowDefinition>()
    server.workflow = [old, { ...fixtureWorkflow(), author: 'newest' }]
    await open()
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(screen.getByText('newest')).toBeInTheDocument()
    expect(pos('b')).toEqual({ x: 40, y: 140 })
    await act(async () => old.resolve({ ...fixtureWorkflow(), author: 'stale' }))
    await settle()
    expect(screen.queryByText('stale')).toBeNull()
  })

  it('reconciles each distinct revision pair only once even if an earlier mismatch recurs', async () => {
    server.layout = [fixtureView({ savedRevision: 'rev-2' }), fixtureView({ savedRevision: 'rev-2' }),
      fixtureView({ savedRevision: 'rev-3' }), fixtureView({ savedRevision: 'rev-3' }),
      fixtureView({ savedRevision: 'rev-2' })]
    await open()
    expect(count('GET', WF_URL)).toBe(2)
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(count('GET', WF_URL)).toBe(4)
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(count('GET', WF_URL)).toBe(5)
    expect(count('GET', LAYOUT_URL)).toBe(5)
    expect(mode()).toBe('fallback')
    expect(status()).toHaveTextContent('Arrangement status unavailable')
    expect(retryButton()).toBeInTheDocument()
    expect(flow.history.some(frame => frame.some(n => n.type === 'arrangedNode'))).toBe(false)
  })

  it('keeps the newest manifest and selection when focus reads resolve out of order or fail', async () => {
    const old = deferred<WorkflowDefinition>()
    server.workflow = [fixtureWorkflow(), old, { ...fixtureWorkflow(), author: 'newest' }, new HttpError(503, 'unavailable')]
    await open()
    fireEvent.click(screen.getByTestId('node-b'))
    window.dispatchEvent(new Event('focus'))
    await settle()
    window.dispatchEvent(new Event('focus'))
    await settle()
    await act(async () => old.resolve({ ...fixtureWorkflow(), author: 'stale' }))
    await settle()
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(screen.getByText('newest')).toBeInTheDocument()
    expect(screen.queryByText('stale')).toBeNull()
    expect(screen.getByTestId('node-b')).toHaveAttribute('data-selected', 'true')
    expect(pos('b')).toEqual({ x: 40, y: 140 })
  })

  it('suppresses reviewed wording until fonts are ready, then draws the fetched geometry', async () => {
    const fonts = deferred<{ fontsReady: boolean; fontFamilyResolved: boolean }>()
    fontBoundary.load.mockImplementationOnce(() => fonts.promise)
    await open()
    expect(mode()).toBe('loading')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).toHaveTextContent('Loading reviewed fonts')
    await act(async () => fonts.resolve({ fontsReady: true, fontFamilyResolved: true }))
    await settle()
    expect(mode()).toBe('arranged')
    expect(pos('b')).toEqual({ x: 40, y: 140 })
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it.each(['missing bundled font', 'unsupported glyph/cmap'])('shows fallback and the font issue: %s', async (issue) => {
    fontBoundary.load.mockRejectedValueOnce(new Error(issue))
    await open()
    expect(mode()).toBe('fallback')
    expect(rf().nodes.every(n => n.type !== 'arrangedNode')).toBe(true)
    expect(status()).toHaveTextContent(issue)
    expect(status()).not.toHaveTextContent('Layout reviewed')
  })

  it('offers a fresh font epoch, not a server Retry, when a passed row cannot load fonts (G8)', async () => {
    fontBoundary.load.mockRejectedValueOnce(new Error('font asset unavailable'))
    server.me = [me({ isAdmin: true, role: 'admin' })]
    const reload = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, reload } })
    try {
      await open()
      expect(mode()).toBe('fallback')
      // The valid passed result stays non-retryable on the server; the client offers its own recovery.
      expect(screen.queryByRole('button', { name: 'Retry arrangement' })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Reload reviewed fonts' }))
      expect(reload).toHaveBeenCalledTimes(1)
      expect(status()).not.toHaveTextContent('Layout reviewed')
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original })
    }
  })

  it('ignores a stale font success when the new revision cannot load its fonts', async () => {
    const old = deferred<{ fontsReady: boolean; fontFamilyResolved: boolean }>()
    fontBoundary.load.mockImplementationOnce(() => old.promise).mockRejectedValueOnce(new Error('new font unavailable'))
    server.workflow = [fixtureWorkflow(), withRevision(fixtureWorkflow(), 'rev-2')]
    server.layout = [fixtureView(), fixtureView({ savedRevision: 'rev-2', geometry: geometryWithB(901, 902) })]
    await open()
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(mode()).toBe('fallback')
    await act(async () => old.resolve({ fontsReady: true, fontFamilyResolved: true }))
    await settle()
    expect(mode()).toBe('fallback')
    expect(drewArrangedB(901, 902)).toBe(false)
    expect(status()).toHaveTextContent('new font unavailable')
    expect(status()).not.toHaveTextContent('Layout reviewed')
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
