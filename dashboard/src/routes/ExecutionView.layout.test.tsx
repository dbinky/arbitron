// Real route, API client, polling hook, panel, shared renderer and React Flow.
// Only HTTP/WebSocket/font boundaries and jsdom layout primitives are controlled.
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import type { GraphState, GroupGraph, GroupPanel, Instance, Me } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import v2 from '../graph/arranged/groupGeometry.v2.json'
import type { Geometry } from '../graph/arranged/types'
import { buildRoutedPath } from '../graph/arranged/routedPath'
import type { LayoutView, RetryAck } from '../graph/arranged/types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'
import { POLL_INTERVAL_MS } from '../hooks/useLayoutStatus'
import { getViewportForBounds } from '@xyflow/react'
import { FOLLOW_BOUNDS_PADDING } from '../graph/constants'

const fonts = vi.hoisted(() => ({ load: vi.fn(async (doc: Document, texts: readonly string[], contract: { version: string }) => {
  if (doc !== document || !texts.includes('Start') || !texts.includes('again') || contract.version !== 'arranged-renderer/2') throw new Error('wrong font input')
  return { fontsReady: true, fontFamilyResolved: true }
}) }))
vi.mock('../graph/arranged/fonts', () => ({ ensureBundledTextFonts: fonts.load }))
import ExecutionView from './ExecutionView'
import WorkflowReview from './WorkflowReview'

// Each test mounts the real route and React Flow for up to three tiles: ~2s on
// an idle machine, past the 5s default under load. A timed-out test keeps
// advancing the next test's fake timers and fails the rest of the file.
vi.setConfig({ testTimeout: 30_000 })

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
type Reply<T> = T | Error | Promise<T>
const calls: { url: string; method: string; body?: string; signal?: AbortSignal | null }[] = []
let instances: Record<string, Reply<Instance>[]>
let layouts: Record<string, Reply<LayoutView>[]>
let retries: Reply<RetryAck>[]
let caller: Reply<Me>
let graphState: GraphState | null
let staticWorkflow: WorkflowDefinition | null
let staticLayout: LayoutView | null
const served = new Map<string, number>()
const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body }) as Response
async function reply<T>(key: string, replies: Reply<T>[] | undefined) {
  const index = served.get(key) ?? 0
  served.set(key, index + 1)
  if (!replies?.length) return json({ error: `unscripted ${key}` }, 404)
  const value = await replies[Math.min(index, replies.length - 1)]
  return value instanceof Error ? json({ error: value.message }, value.message.includes('forbidden') ? 403 : 503) : json(value)
}
const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input), method = init?.method ?? 'GET'
  calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined, signal: init?.signal })
  if (url === '/api/v1/me') return reply(url, [caller])
  if (url === '/api/v1/workflows/wf') return json(staticWorkflow)
  if (url === '/api/v1/workflows/wf/layout') return json(staticLayout)
  if (url.startsWith('/api/v1/workflows/wf/stats')) return json({})
  if (url.startsWith('/api/v1/schedules')) return json({ schedules: [] })
  const match = /^\/api\/v1\/instances\/([^/]+)(.*)$/.exec(url)
  if (!match) return json({ error: `unexpected ${url}` }, 404)
  const [, id, suffix] = match
  if (suffix === '') return reply(url, instances[id])
  if (suffix === '/layout') return reply(url, layouts[id])
  if (suffix === '/layout/retry' && method === 'POST') return reply(url, retries)
  if (suffix === '/graph-state' && graphState) return json(graphState)
  if (suffix.startsWith('/trace/')) return json({ instanceId: id, nodeId: suffix.slice(7), status: 'succeeded', output: { owner: id } })
  if (suffix === '/trace') return json([])
  if (suffix === '/variables') return json({})
  return json({ error: 'no execution yet' }, 404)
})
function inst(id = 'i1', layout?: LayoutView): Instance {
  return { id, ownerId: 'owner-1', department: 'engineering', workflowName: 'deleted-or-renamed', state: 'completed', priority: 'normal',
    workflowSnapshot: fixtureWorkflow(), layout } as unknown as Instance
}
const moved = (x: number, generation = 1) => fixtureView({ jobGeneration: generation, resultGeneration: generation,
  geometry: fixtureGeometryWithBoxes({ b: { x, y: 800, w: 320, h: 200 } }) })
const pending = () => fixtureView({ geometry: undefined, resultGeneration: 0, displayStatus: 'pending', state: 'pending',
  retained: { source: 'none' }, provenance: 'retained_positions', unreviewed: true })
const attention = () => fixtureView({ displayStatus: 'needs_attention', state: 'needs_attention', retryEligible: true,
  findings: [{ criterion: 'text_clipped', severity: 'blocking', source: 'review', nodeIds: ['b'], evidence: 'distinct clipped label' }] })
const count = (id = 'i1') => calls.filter(c => c.url === `/api/v1/instances/${id}/layout`).length
const mode = () => document.querySelector('[data-arranged-canvas]')?.getAttribute('data-layout-mode')
const node = (id = 'b') => document.querySelector(`[data-arranged-node="${id}"]`) as HTMLElement | null
const pos = () => (node()?.closest('.react-flow__node') as HTMLElement | null)?.style.transform
const status = () => screen.getByRole('status')
const retry = () => fireEvent.click(screen.getByRole('button', { name: 'Retry arrangement' }))
async function settle(ms = 0) {
  for (let i = 0; i < 10; i++) await act(async () => { await vi.advanceTimersByTimeAsync(i === 0 ? ms : 10) })
}
function Navigate() {
  const navigate = useNavigate()
  return <button onClick={() => navigate('/instances/i2')}>next instance</button>
}
async function open() {
  const utils = render(<MemoryRouter initialEntries={['/instances/i1']}><Navigate /><Routes>
    <Route path="/instances/:id" element={<ExecutionView />} />
  </Routes></MemoryRouter>)
  await settle()
  return utils
}
const descriptors = new Map<string, PropertyDescriptor | undefined>()
beforeAll(() => {
  for (const key of ['offsetWidth', 'offsetHeight']) descriptors.set(key, Object.getOwnPropertyDescriptor(HTMLElement.prototype, key))
  descriptors.set('getBBox', Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getBBox'))
  for (const [key, style] of [['offsetWidth', 'width'], ['offsetHeight', 'height']] as const) {
    Object.defineProperty(HTMLElement.prototype, key, { configurable: true, get() { return parseFloat((this as HTMLElement).style[style]) || 10 } })
  }
  Object.defineProperty(SVGElement.prototype, 'getBBox', { configurable: true, value: () => ({ x: 0, y: 0, width: 10, height: 10 }) })
})
afterAll(() => {
  for (const [key, descriptor] of descriptors) {
    const proto = key === 'getBBox' ? SVGElement.prototype : HTMLElement.prototype
    if (descriptor) Object.defineProperty(proto, key, descriptor)
    else Reflect.deleteProperty(proto, key)
  }
})
beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', fetcher)
  // No websocket event is ever emitted (including open).
  vi.stubGlobal('WebSocket', class { close() {} addEventListener() {} })
  vi.stubGlobal('ResizeObserver', class {
    constructor(private cb: ResizeObserverCallback) {}
    observe(target: Element) { setTimeout(() => this.cb([{ target } as ResizeObserverEntry], this as unknown as ResizeObserver), 0) }
    unobserve() {} disconnect() {}
  })
  vi.stubGlobal('DOMMatrixReadOnly', class { m22: number; constructor(transform?: string) { this.m22 = Number(/scale\(([\d.]+)\)/.exec(transform ?? '')?.[1] ?? 1) } })
  instances = { i1: [inst()], i2: [inst('i2')] }
  layouts = { i1: [fixtureView()], i2: [moved(222)] }
  caller = { userId: 'owner-1', username: 'owner', role: 'user', isAdmin: false,
    departments: ['engineering'], ledDepartments: [], authMode: 'org' }
  retries = []; graphState = null; staticWorkflow = null; staticLayout = null
  calls.length = 0; served.clear(); fonts.load.mockClear()
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

function grouped(id: string, index = 2) {
  const f = structuredClone(v2[index]) as unknown as { definition: WorkflowDefinition; groups: GroupGraph; geometry: Geometry }
  f.definition.presentation = { revision: id } as WorkflowDefinition['presentation']
  return { ...f, view: fixtureView({ savedRevision: id, groups: f.groups, geometry: f.geometry }) }
}
function groupPanel(id: string, f: ReturnType<typeof grouped>, status: GroupPanel['tasks'][number]['liveStatus'] = 'running'): GroupPanel {
  const scope = Object.values(f.groups.groups)[0]
  return { instanceId: id, nodeId: scope.nodeId, groupRunId: `${id}-run`, form: scope.config.form,
    completionRule: scope.config.completionRule, unfinishedBehavior: scope.config.unfinishedBehavior, gatherMode: scope.config.gatherMode,
    state: 'running', winner: scope.config.tasks[0].key,
    tasks: scope.config.tasks.map((t, i) => ({ key: t.key, kind: 'workflow', origin: t.workflow ?? t.entry ?? '', childInstanceId: `${id}-task-${i}`, liveStatus: status,
      frozenStatus: 'unfinished', blocking: false, continuingIndependently: true, cancelRequested: true, redacted: false,
      launchError: `full launch detail ${'λ'.repeat(150)}` })),
    progress: { total: scope.config.tasks.length, completed: 0, failed: 0, cancelled: 0, active: scope.config.tasks.length, notStarted: 0 },
    waitingReasons: [`full waiting detail ${'漢'.repeat(150)}`] }
}
function groupedRun() {
  const root = grouped('i1'), a = grouped('child-a'), b = grouped('child-b')
  instances.i1 = [{ ...inst(), workflowSnapshot: root.definition } as unknown as Instance]
  layouts.i1 = [root.view]; layouts['child-a'] = [{ ...a.view, displayStatus: 'pending' }, a.view]; layouts['child-b'] = [{ ...b.view, displayStatus: 'reviewing' }, b.view]
  const children = [a, b]
  graphState = { rootInstanceId: 'i1', version: 'groups-1', active: false, layout: root.groups,
    contexts: [{ instanceId: 'i1', kind: 'root', state: 'completed', blocking: false, redacted: false, nodes: [] },
      ...children.map((f, i) => ({ instanceId: `child-${i ? 'b' : 'a'}`, kind: 'workflow' as const, parentInstanceId: 'i1', state: 'completed' as const, blocking: false, redacted: false,
        graph: f.definition as unknown as Record<string, unknown>, layout: f.groups, nodes: [] }))],
    groups: [groupPanel('i1', root), groupPanel('child-a', a, 'queued'), groupPanel('child-b', b, 'waiting')] }
  return { root, a, b }
}
const scoped = (id: string, selector: string) => [...document.querySelectorAll<HTMLElement>(selector)].filter(el => el.closest<HTMLElement>('[data-layout-instance]')?.dataset.layoutInstance === id)

describe('ExecutionView grouped snapshot tiles through real routes', () => {
  it('mounts static WorkflowReview from joined saved ownership without execution or current group lookup', async () => {
    const f = grouped('static')
    staticWorkflow = f.definition; staticLayout = f.view
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    render(<MemoryRouter initialEntries={['/workflows/wf']}><Routes><Route path="/workflows/:name" element={<WorkflowReview />} /></Routes></MemoryRouter>)
    await settle()
    expect(mode()).toBe('arranged')
    expect(document.querySelectorAll('[data-arranged-group-element]')).toHaveLength(f.geometry.groupElements!.length)
    const header = f.geometry.groupElements!.find(e => e.role === 'header')!
    const element = [...document.querySelectorAll<HTMLElement>('[data-arranged-group-element]')].find(el => el.dataset.arrangedGroupElement === header.id)!
    fireEvent.click(element)
    expect(screen.getByTestId('review-group-details')).toHaveTextContent(f.groups.groups[header.groupNodeId].config.tasks[0].key)
    expect(document.querySelectorAll('[data-overlay-row] [data-group-text]')).toHaveLength(0)
    expect(calls.some(c => c.url.includes('/instances/'))).toBe(false)
  })
  it.each([['i1', 'height'], ['child-a', 'height'], ['child-a', 'width']])('rejects oversized client overlay %s/%s metrics and removes its reviewed badge', async (affected, dimension) => {
    groupedRun()
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    const original = window.getComputedStyle.bind(window)
    vi.stubGlobal('getComputedStyle', (el: Element) => {
      const css = original(el)
      if (el.hasAttribute('data-row-measure') && el.closest<HTMLElement>('[data-layout-instance]')?.dataset.layoutInstance === affected) {
        return new Proxy(css, { get: (target, key) => key === dimension ? '2000px' : Reflect.get(target, key) })
      }
      return css
    })
    await open(); await settle(POLL_INTERVAL_MS)
    expect(scoped(affected, '[data-arranged-group-element]')).toHaveLength(0)
    expect(scoped(affected, '[data-layout-mode="fallback"]').length).toBeGreaterThan(0)
    const panel = affected === 'i1' ? screen.getAllByRole('status').find(el => !el.closest('[data-layout-status-instance]'))!
      : document.querySelector(`[data-layout-status-instance="${affected}"]`)!
    expect(panel).toHaveTextContent('Client group overlay exceeds reviewed reservation')
    expect(panel).not.toHaveTextContent('Layout reviewed')
    expect(screen.getAllByText('Layout reviewed')).toHaveLength(2)
    for (const id of ['i1', 'child-a', 'child-b'].filter(id => id !== affected)) {
      expect(scoped(id, '[data-arranged-group-element]').length).toBeGreaterThan(0)
    }
    expect(calls.filter(c => c.method === 'POST')).toHaveLength(0)
  })
  it('feeds live statuses and independently polled child geometry into actual scoped DOM, paths, markers and selection', async () => {
    const { root, a, b } = groupedRun()
    const activity = `full active node ${'活動'.repeat(100)}`
    graphState!.contexts.push({ instanceId: 'child-a-task-0', parentInstanceId: 'child-a', kind: 'workflow', state: 'running', blocking: false, redacted: false,
      graph: { nodes: [{ id: 'busy', label: activity }], edges: [] }, nodes: [{ instanceId: 'child-a-task-0', nodeId: 'busy', state: 'running' }] })
    fonts.load.mockImplementation(async (_doc, texts, contract) => {
      if (!texts.some(t => t.startsWith('Parallel ')) || contract.version !== 'arranged-renderer/2') throw new Error('missing group texts')
      return { fontsReady: true, fontFamilyResolved: true }
    })
    await open(); await settle(POLL_INTERVAL_MS)
    for (const [i, [id, f]] of [['i1', root], ['child-a', a], ['child-b', b]].entries()) {
      const tileId = id as string, fixture = f as ReturnType<typeof grouped>
      expect(scoped(tileId, '[data-arranged-group-element]')).toHaveLength(fixture.geometry.groupElements!.length)
      const dy = i * (fixture.geometry.canvas.h + fixture.geometry.contract.groupRowGapPx)
      const route = fixture.geometry.edges[0]
      expect(scoped(tileId, '[data-edge-path]')[0].getAttribute('d')).toBe(buildRoutedPath(route.points.map(p => ({ ...p, y: p.y + dy })), [], fixture.geometry.contract.crossingGapPx))
      const status = i === 0 ? 'running' : i === 1 ? 'queued' : 'waiting'
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain(status)
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain(`winner: ${Object.values(fixture.groups.groups)[0].config.tasks[0].key}`)
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain('winner')
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain('cancel requested')
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain('non-blocking')
      expect(scoped(tileId, '[data-group-text]').map(el => el.textContent)).toContain('gathered as unfinished')
    }
    const paths = [...document.querySelectorAll<SVGPathElement>('path[data-edge-path],path[data-group-connector]')]
    const markerIDs = paths.map(p => p.getAttribute('marker-end')!.match(/#([^)]*)/)![1])
    expect(new Set(markerIDs).size).toBe(paths.length)
    for (const marker of markerIDs) expect(document.getElementById(marker)).not.toBeNull()
    expect(count('child-a')).toBe(2); expect(count('child-b')).toBe(2)
    expect(screen.getAllByText('Layout reviewed')).toHaveLength(3)
    const box = scoped('child-a', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupRole === 'task') ?? scoped('child-a', '[data-arranged-group-element]')[1]
    fireEvent.click(box)
    expect(screen.getByTestId('group-panel')).toHaveTextContent(activity)
    expect(screen.getByTestId('group-panel')).toHaveTextContent('full launch detail')
    expect(screen.getByTestId('group-panel')).toHaveTextContent('full waiting detail')
    expect(screen.getAllByRole('link', { name: 'open run' }).map(el => el.getAttribute('href'))).toContain('/instances/child-a-task-0')
    expect(document.querySelector('[data-arranged-canvas]')).not.toHaveTextContent('full launch detail')
    const rail = screen.getByRole('navigation', { name: 'Child arrangements' })
    expect(rail.closest('.react-flow')).toBeNull()
  })
  it('selects a branch body by the original execution owner, distinct from the reviewed tile owner', async () => {
    const root = grouped('i1', 1), panel = groupPanel('i1', root)
    const scope = Object.values(root.groups.groups)[0]
    panel.tasks.forEach(t => { t.kind = 'branch' })
    instances.i1 = [{ ...inst(), workflowSnapshot: root.definition } as unknown as Instance]; layouts.i1 = [root.view]
    graphState = { rootInstanceId: 'i1', version: 'branches', active: false, layout: root.groups, groups: [panel], contexts: [
      { instanceId: 'i1', kind: 'root', state: 'completed', blocking: false, redacted: false, nodes: [] },
      ...panel.tasks.map(t => ({ instanceId: t.childInstanceId!, parentInstanceId: 'i1', kind: 'branch' as const, state: 'waiting' as const, blocking: false, redacted: false,
        nodes: scope.bodies[t.key].map(nodeId => ({ instanceId: t.childInstanceId!, nodeId, state: 'waiting' as const })) })),
    ] }
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open()
    const lane = root.geometry.groupElements!.find(e => e.role === 'lane')!
    fireEvent.click(scoped('i1', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === lane.id)!)
    expect(screen.getByTestId('group-panel')).toHaveTextContent('full waiting detail')
    const nodeId = scope.bodies[panel.tasks[0].key][0]
    fireEvent.click(scoped('i1', '[data-arranged-node]').find(el => el.dataset.arrangedNode === nodeId)!)
    await settle()
    expect(calls.some(c => c.url === `/api/v1/instances/${panel.tasks[0].childInstanceId}/trace/${nodeId}`)).toBe(true)
    expect(calls.some(c => c.url === `/api/v1/instances/i1/trace/${nodeId}`)).toBe(false)
    expect(screen.getByTestId('trace-owner')).toHaveTextContent(panel.tasks[0].childInstanceId!.slice(0, 8))
  })
  it.each(['nil', 'v1', 'wrong ownership'] as const)('shows explicit %s group fallback without a reviewed badge', async bad => {
    const root = grouped('i1')
    instances.i1 = [{ ...inst(), workflowSnapshot: root.definition } as unknown as Instance]
    const view = structuredClone(root.view)
    if (bad === 'nil') view.geometry = undefined
    if (bad === 'v1') view.geometry!.format = 'arbitron.layout/v1'
    if (bad === 'wrong ownership') view.groups = { order: [], groups: {}, owner: {} }
    layouts.i1 = [view]
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open()
    expect(mode()).toBe('fallback'); expect(screen.queryByText('Layout reviewed')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Layout needs attention')
    expect(document.querySelectorAll('[data-arranged-group-element]')).toHaveLength(0)
  })
  it('preserves reviewed child route paint layers even when only the root is fallback', async () => {
    const { a } = groupedRun()
    layouts.i1 = [{ ...layouts.i1[0] as LayoutView, geometry: undefined, displayStatus: 'pending', unreviewed: true }]
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open(); await settle(POLL_INTERVAL_MS)
    expect(mode()).toBe('fallback')
    const paths = scoped('child-a', 'path[data-edge-path],path[data-group-connector]')
    expect(paths.length).toBe(a.geometry.edges.length + a.geometry.groupConnectors!.length)
    const header = scoped('child-a', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === a.geometry.groupElements!.find(e => e.role === 'header')!.id)!
    const headerLayer = Number(header.closest<HTMLElement>('.react-flow__node')!.style.zIndex)
    for (const p of paths) expect(Number(p.closest('svg')!.style.zIndex)).toBeLessThan(headerLayer)
  })
  it('a child font failure retains reviewed root/other child and marks only that tile unreviewed', async () => {
    const { a } = groupedRun()
    a.definition.nodes[0].label = 'Child-only font text'; a.geometry.nodes[0].text = 'Child-only font text'
    fonts.load.mockImplementation(async (_doc, texts) => {
      if (texts.includes('Child-only font text')) throw new Error('font failed')
      return { fontsReady: true, fontFamilyResolved: true }
    })
    await open(); await settle(POLL_INTERVAL_MS)
    expect(scoped('i1', '[data-arranged-group-element]').length).toBeGreaterThan(0)
    expect(scoped('child-b', '[data-arranged-group-element]').length).toBeGreaterThan(0)
    expect(scoped('child-a', '[data-arranged-group-element]')).toHaveLength(0)
    expect(scoped('child-a', '[data-layout-mode="fallback"]').length).toBeGreaterThan(0)
    expect(document.querySelector('[data-layout-status-instance="child-a"]')).toHaveTextContent('Layout needs attention')
    expect(screen.getAllByText('Layout reviewed')).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Reload reviewed fonts' })).toBeInTheDocument()
  })
  it('recenter frames every active qualified task across root and two children using actual ReactFlow viewport and sizes', async () => {
    groupedRun(); graphState!.active = true
    // Exercise the real viewport without animation clocks cached before fake timers.
    vi.stubGlobal('matchMedia', (media: string) => ({ media, matches: media === '(prefers-reduced-motion: reduce)', addEventListener() {}, removeEventListener() {} }))
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    const width = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')!
    const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')!
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get() { return (this as HTMLElement).classList.contains('react-flow__renderer') ? 800 : width.get!.call(this) } })
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get() { return (this as HTMLElement).classList.contains('react-flow__renderer') ? 600 : height.get!.call(this) } })
    try {
      await open(); await settle(POLL_INTERVAL_MS)
      const cards = [...document.querySelectorAll<HTMLElement>('.react-flow__node-groupTaskCard')]
      expect(cards).toHaveLength(6)
      const rects = cards.map(el => {
        const [x, y] = /translate\(([-\d.]+)px,\s*([-\d.]+)px\)/.exec(el.style.transform)!.slice(1).map(Number)
        return { x, y, width: parseFloat(el.style.width), height: parseFloat(el.style.height) }
      })
      const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y))
      const bounds = { x, y, width: Math.max(...rects.map(r => r.x + r.width)) - x, height: Math.max(...rects.map(r => r.y + r.height)) - y }
      const expected = getViewportForBounds(bounds, 800, 600, 0.2, 2, FOLLOW_BOUNDS_PADDING)
      const assertViewport = () => {
        const transform = document.querySelector<HTMLElement>('.react-flow__viewport')!.style.transform
        const actual = /translate\(([-\d.e]+)px,\s*([-\d.e]+)px\) scale\(([-\d.e]+)\)/.exec(transform)!.slice(1).map(Number)
        expect(actual[0]).toBeCloseTo(expected.x); expect(actual[1]).toBeCloseTo(expected.y); expect(actual[2]).toBeCloseTo(expected.zoom)
      }
      await settle(500)
      assertViewport()
      fireEvent.click(screen.getByRole('button', { name: 'Following' }))
      await settle(100)
      assertViewport()
    } finally {
      Object.defineProperty(HTMLElement.prototype, 'offsetWidth', width)
      Object.defineProperty(HTMLElement.prototype, 'offsetHeight', height)
    }
  })
  it('anchors the real linked child session callout to its qualified footer and actual dimensions, not the root namesake', async () => {
    const { a } = groupedRun()
    const panel = graphState!.groups.find(g => g.instanceId === 'child-a')!
    panel.gather = { mode: 'results_and_files', state: 'resolving', tasks: [], mergeIndex: 0, attempts: [], activeSession: { instanceId: 'child-a', nodeId: panel.nodeId, groupRunId: panel.groupRunId, mergeIndex: 0, attempt: 1 } }
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open(); await settle(POLL_INTERVAL_MS)
    const footer = a.geometry.groupElements!.find(e => e.role === 'footer')!
    const child = scoped('child-a', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === footer.id)!
    const wrapper = child.closest<HTMLElement>('.react-flow__node')!
    const w = parseFloat(wrapper.style.width), h = parseFloat(wrapper.style.height)
    wrapper.getBoundingClientRect = () => ({ x: 50, y: 100, left: 50, top: 100, right: 50 + w / 2, bottom: 100 + h / 2, width: w / 2, height: h / 2 }) as DOMRect
    const divider = document.querySelector<HTMLElement>('[class*="divider"]')!
    divider.getBoundingClientRect = () => ({ left: 1000 }) as DOMRect
    const header = scoped('child-a', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === a.geometry.groupElements!.find(e => e.role === 'header')!.id)!
    fireEvent.click(header); fireEvent.click(screen.getByRole('button', { name: 'Open resolution session' }))
    await settle()
    const circle = document.querySelector('svg[class*="overlay"] circle')
    expect(circle).not.toBeNull()
    expect(circle!.getAttribute('cx')).toBe(String(50 + w / 2))
    expect(circle!.getAttribute('cy')).toBe(String(100 + h / 4))
  })
  it('keeps selection and other tile status during retry, then aborts/forgets a removed child with GET and retry pending', async () => {
    const { a } = groupedRun()
    graphState!.active = true
    layouts['child-a'] = [{ ...a.view, displayStatus: 'needs_attention', retryEligible: true }]
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open(); await settle(POLL_INTERVAL_MS)
    const ack = deferred<RetryAck>(), get = deferred<LayoutView>()
    retries = [ack.promise]
    const rail = document.querySelector('[data-layout-status-instance="child-a"]')!
    fireEvent.click(rail.querySelector('button')!)
    await settle()
    expect(rail).toHaveTextContent('Retrying…')
    const header = scoped('child-a', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === a.geometry.groupElements!.find(e => e.role === 'header')!.id)!
    fireEvent.click(header)
    expect(screen.getByTestId('group-panel')).toHaveTextContent('full waiting detail')
    expect(screen.getAllByText('Layout reviewed')).toHaveLength(2)
    layouts['child-a'] = [get.promise]
    served.delete('/api/v1/instances/child-a/layout')
    act(() => { window.dispatchEvent(new Event('focus')) }); await settle()
    const pending = [...calls].reverse().find(c => c.url === '/api/v1/instances/child-a/layout')!
    expect(pending.signal?.aborted).toBe(false)
    graphState = { ...graphState!, version: 'removed', contexts: graphState!.contexts.filter(c => c.instanceId !== 'child-a'), groups: graphState!.groups.filter(g => g.instanceId !== 'child-a') }
    await settle(5000)
    expect(pending.signal?.aborted).toBe(true)
    expect(scoped('child-a', '[data-arranged-group-element]')).toHaveLength(0)
    expect(document.querySelector('[data-layout-status-instance="child-a"]')).toBeNull()
    const reads = count('child-a')
    await act(async () => { get.resolve(a.view); ack.resolve({ savedRevision: 'child-a', jobGeneration: 2, displayStatus: 'pending', state: 'pending' }) })
    await settle(POLL_INTERVAL_MS * 2)
    expect(count('child-a')).toBe(reads)
    expect(scoped('child-a', '[data-arranged-group-element]')).toHaveLength(0)
    expect(document.querySelector('a[href="/instances/child-a"]')).toBeNull()
  })
  it.each(['queued', 'running', 'waiting', 'paused', 'unfinished'] as const)('preserves fixed geometry for %s and redacts hidden child identities', async status => {
    const { root } = groupedRun()
    graphState!.groups[0] = groupPanel('i1', root, status)
    graphState!.groups[0].tasks[1].redacted = true; graphState!.groups[0].tasks[1].childInstanceId = undefined
    graphState!.contexts[2] = { ...graphState!.contexts[2], instanceId: 'hidden-child', redacted: true }
    fonts.load.mockResolvedValue({ fontsReady: true, fontFamilyResolved: true })
    await open(); await settle(POLL_INTERVAL_MS)
    const texts = scoped('i1', '[data-group-text]').map(el => el.textContent)
    expect(texts).toContain(status); expect(texts).toContain('hidden dependency')
    expect(document.body).not.toHaveTextContent('hidden-child')
    expect(calls.some(c => c.url.includes('hidden-child/layout'))).toBe(false)
    for (const e of root.geometry.groupElements!) {
      const element = scoped('i1', '[data-arranged-group-element]').find(el => el.dataset.arrangedGroupElement === e.id)!
      expect(element.style.width).toBe(`${e.box.w}px`); expect(element.style.height).toBe(`${e.box.h}px`)
    }
  })
})

describe('ExecutionView layout composition and independent completed-run polling', () => {
  it('polls without websocket traffic through actual moved node/path/label/status sinks then stops', async () => {
    layouts.i1 = [pending(), moved(777)]
    await open()
    expect(mode()).toBe('fallback')
    expect(status()).toHaveTextContent('Arrangement pending')
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(777px,800px)')
    expect([node()!.style.width, node()!.style.height]).toEqual(['320px', '200px'])
    expect(document.querySelector('[data-edge-path="1"]')!.getAttribute('d')).toContain('M 937,1000')
    expect(document.querySelector('[data-edge-label="2"]')!.textContent).toBe('again')
    expect(status()).toHaveTextContent('Layout reviewed')
    const terminal = count()
    await settle(POLL_INTERVAL_MS * 5)
    expect(count()).toBe(terminal)
    expect(calls.every(c => c.method === 'GET' && (c.url.startsWith('/api/v1/instances/i1') || c.url === '/api/v1/me'))).toBe(true)
  })

  it.each(['failed', 'needs_attention'] as const)('shows %s best geometry/findings and retries the snapshot revision, not execution', async state => {
    layouts.i1 = [fixtureView({ ...attention(), state, displayStatus: state }),
      fixtureView({ ...pending(), jobGeneration: 2 }), moved(888, 2)]
    retries = [{ savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }]
    await open()
    expect(mode()).toBe('arranged')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).toHaveTextContent('distinct clipped label')
    retry(); await settle()
    expect(calls.filter(c => c.method !== 'GET')).toEqual([{ url: '/api/v1/instances/i1/layout/retry', method: 'POST', body: '{"expectedRevision":"rev-1"}' }])
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(888px,800px)')
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('seeds only the snapshot token then accepts a newer poll', async () => {
    const first = deferred<LayoutView>()
    instances.i1 = [inst('i1', moved(111))]; layouts.i1 = [first.promise]
    await open()
    expect(pos()).toBe('translate(111px,800px)')
    first.resolve(moved(999, 2)); await settle()
    expect(pos()).toBe('translate(999px,800px)')
  })

  it('ignores a foreign embedded layout', async () => {
    instances.i1 = [inst('i1', fixtureView({ savedRevision: 'foreign' }))]
    layouts.i1 = [pending()]
    await open()
    expect(mode()).toBe('fallback'); expect(node()).toBeNull()
  })

  it('never draws or retries foreign GET geometry; Retry refreshes only', async () => {
    layouts.i1 = [fixtureView({ savedRevision: 'foreign' })]
    await open()
    expect(mode()).toBe('fallback'); expect(node()).toBeNull()
    expect(status()).toHaveTextContent('revision')
    retry(); await settle()
    expect(count()).toBe(2)
    expect(calls.filter(c => c.method !== 'GET')).toEqual([])
  })

  it('keeps tokenless snapshot positions with no status or writes', async () => {
    const snapshot = fixtureWorkflow(); delete snapshot.presentation
    snapshot.nodes = snapshot.nodes.map((n, i) => ({ ...n, position: { x: 12, y: i * 200 } }))
    instances.i1 = [{ ...inst(), workflowSnapshot: snapshot } as unknown as Instance]
    layouts.i1 = [fixtureView({ savedRevision: '', geometry: undefined, displayStatus: 'legacy', retained: { source: 'manifest' } })]
    await open()
    expect(mode()).toBe('fallback')
    expect((document.querySelector('.react-flow__node[data-id="b"]') as HTMLElement).style.transform).toBe('translate(12px,200px)')
    expect(screen.queryByRole('status')).toBeNull()
    expect(calls.filter(c => c.method !== 'GET')).toEqual([])
  })

  it.each(['transport', 'in-band'] as const)('retains seed through initial %s outage and rejects older generations on recovery', async kind => {
    instances.i1 = [inst('i1', moved(555, 5))]
    layouts.i1 = [kind === 'transport' ? new Error('offline') : fixtureView({ available: false, availabilityError: 'store offline', geometry: undefined, jobGeneration: 0, resultGeneration: 0 }),
      moved(111, 1), moved(666, 6)]
    await open()
    expect(pos()).toBe('translate(555px,800px)')
    expect(status()).not.toHaveTextContent('Layout reviewed')
    expect(status()).toHaveTextContent('Could not refresh')
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(555px,800px)')
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(666px,800px)')
  })

  it('keeps accepted geometry during repeated outages and backs off until recovery', async () => {
    layouts.i1 = [moved(333), new Error('offline'), new Error('offline'), moved(444, 2)]
    await open()
    fireEvent(window, new Event('focus')); await settle()
    expect(pos()).toBe('translate(333px,800px)'); expect(status()).toHaveTextContent('Could not refresh')
    await settle(POLL_INTERVAL_MS)
    const n = count(); await settle(POLL_INTERVAL_MS)
    expect(count()).toBe(n)
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(444px,800px)')
  })

  it('retains the graph and shows a server rejection if permission changed after loading', async () => {
    layouts.i1 = [attention()]; retries = [new Error('forbidden')]
    await open(); retry(); await settle()
    expect(mode()).toBe('arranged')
    expect(status()).toHaveTextContent('Retry failed: API 403: forbidden')
  })

  it.each([
    ['department viewer', { userId: 'peer' }, 'engineering', false],
    ['public viewer', { userId: 'viewer', departments: [] }, '', false],
    ['owner', {}, 'engineering', true],
    ['admin', { userId: 'admin', role: 'admin', isAdmin: true }, 'engineering', true],
    ['run department lead', { userId: 'lead', ledDepartments: ['engineering'] }, 'engineering', true],
    ['another department lead', { userId: 'lead', ledDepartments: ['sales'] }, 'engineering', false],
    ['lead without a run department', { userId: 'lead', ledDepartments: ['engineering'] }, undefined, false],
  ] as const)('offers instance layout Retry only with intervention permission: %s', async (_, identity, department, allowed) => {
    caller = { ...(caller as Me), ...identity } as Me
    instances.i1 = [{ ...inst(), department } as Instance]
    layouts.i1 = [attention()]
    retries = [{ savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' }]
    await open()
    expect(mode()).toBe('arranged')
    expect(status()).toHaveTextContent('distinct clipped label')
    const button = screen.queryByRole('button', { name: 'Retry arrangement' })
    if (allowed) {
      expect(button).toBeInTheDocument()
      fireEvent.click(button!); await settle()
      expect(calls.filter(c => c.method === 'POST')).toEqual([
        { url: '/api/v1/instances/i1/layout/retry', method: 'POST', body: '{"expectedRevision":"rev-1"}' },
      ])
    } else {
      expect(button).toBeNull()
      expect(calls.filter(c => c.method === 'POST')).toEqual([])
    }
  })

  it('keeps Retry hidden while identity is loading, then exposes it to the owner', async () => {
    const identity = deferred<Me>()
    const owner = caller as Me
    caller = identity.promise
    layouts.i1 = [attention()]
    await open()
    expect(mode()).toBe('arranged')
    expect(screen.queryByRole('button', { name: 'Retry arrangement' })).toBeNull()
    identity.resolve(owner); await settle()
    expect(screen.getByRole('button', { name: 'Retry arrangement' })).toBeInTheDocument()
  })

  it('keeps the graph and hides Retry if identity cannot be loaded', async () => {
    caller = new Error('identity unavailable')
    layouts.i1 = [attention()]
    await open()
    expect(mode()).toBe('arranged')
    expect(status()).toHaveTextContent('distinct clipped label')
    expect(screen.queryByRole('button', { name: 'Retry arrangement' })).toBeNull()
    expect(calls.filter(c => c.method === 'POST')).toEqual([])
  })

  it('rejects bad geometry in the panel, never alongside reviewed', async () => {
    const geometry = fixtureGeometry(); geometry.contract.version = 'wrong'
    layouts.i1 = [fixtureView({ geometry })]
    await open()
    expect(mode()).toBe('fallback')
    expect(status()).toHaveTextContent('This layout could not be displayed')
    expect(status()).not.toHaveTextContent('Layout reviewed')
  })

  it('reports rejected and deferred fonts, never a false reviewed verdict', async () => {
    fonts.load.mockRejectedValueOnce(new Error('missing reviewed font'))
    await open()
    expect(mode()).toBe('fallback'); expect(status()).toHaveTextContent('missing reviewed font')
    expect(status()).not.toHaveTextContent('Layout reviewed')
  })

  it('offers a fresh font epoch, not a server Retry, when a passed row cannot load fonts (G8)', async () => {
    fonts.load.mockRejectedValueOnce(new Error('font asset unavailable'))
    const reload = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, reload } })
    try {
      await open()
      expect(mode()).toBe('fallback')
      expect(screen.queryByRole('button', { name: 'Retry arrangement' })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'Reload reviewed fonts' }))
      expect(reload).toHaveBeenCalledTimes(1)
      expect(status()).not.toHaveTextContent('Layout reviewed')
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original })
    }
  })

  it('guards deferred fonts across a new geometry epoch', async () => {
    const gate = deferred<{ fontsReady: boolean; fontFamilyResolved: boolean }>()
    fonts.load.mockImplementationOnce(() => gate.promise)
    layouts.i1 = [fixtureView(), moved(444, 2)]
    await open()
    expect(mode()).toBe('loading'); expect(status()).not.toHaveTextContent('Layout reviewed')
    fireEvent(window, new Event('focus')); await settle()
    expect(pos()).toBe('translate(444px,800px)')
    gate.resolve({ fontsReady: true, fontFamilyResolved: true }); await settle()
    expect(pos()).toBe('translate(444px,800px)')
  })

  it('does not let old instance requests, Retry or fonts cross id navigation', async () => {
    const old = deferred<LayoutView>(), oldInstance = deferred<Instance>()
    layouts.i1 = [old.promise]; instances.i1 = [oldInstance.promise]
    await open()
    fireEvent.click(screen.getByText('next instance')); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    old.resolve(moved(999)); oldInstance.resolve(inst()); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    expect(screen.getByText('i2')).toBeInTheDocument()
  })

  it('does not apply a previous instance retry acknowledgement', async () => {
    const ack = deferred<RetryAck>()
    layouts.i1 = [attention()]; retries = [ack.promise]
    await open(); retry(); await settle()
    fireEvent.click(screen.getByText('next instance')); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    ack.resolve({ savedRevision: 'rev-1', jobGeneration: 9, state: 'pending', displayStatus: 'pending' }); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    expect(status()).toHaveTextContent('Layout reviewed')
    expect(count('i1')).toBe(1)
  })

  it('ignores a deferred font completion from the previous instance', async () => {
    const gate = deferred<{ fontsReady: boolean; fontFamilyResolved: boolean }>()
    fonts.load.mockImplementationOnce(() => gate.promise)
    await open()
    expect(mode()).toBe('loading')
    fireEvent.click(screen.getByText('next instance')); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    gate.resolve({ fontsReady: true, fontFamilyResolved: true }); await settle()
    expect(pos()).toBe('translate(222px,800px)')
    expect(status()).toHaveTextContent('Layout reviewed')
  })

  it('keeps selection while polling changes the completed snapshot layout', async () => {
    layouts.i1 = [fixtureView({ ...pending(), retained: { source: 'manifest', positions: { a: { x: 40, y: 40 }, b: { x: 40, y: 140 }, c: { x: 40, y: 260 } } } }), moved(777)]
    await open()
    fireEvent.click(document.querySelector('.react-flow__node[data-id="b"]')!)
    await settle(POLL_INTERVAL_MS)
    expect(pos()).toBe('translate(777px,800px)')
    expect(node()!.closest('.react-flow__node')).toHaveClass('selected')
    expect(screen.getByText('Draft a very long label that must wrap', { selector: 'span' })).toBeInTheDocument()
  })

  it('feeds an interactive instance node through the actual root to the callout endpoint', async () => {
    instances.i1 = [{ ...inst(), state: 'waiting', interactiveActive: true, interactiveNodeId: 'b' } as Instance]
    layouts.i1 = [moved(40)]
    const rects = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.classList.contains('react-flow__node') && this.getAttribute('data-id') === 'b') {
        return { left: 80, top: 90, right: 240, bottom: 190, width: 160, height: 100, x: 80, y: 90, toJSON() {} }
      }
      return { left: 700, top: 0, right: 704, bottom: 600, width: 4, height: 600, x: 700, y: 0, toJSON() {} }
    })
    try {
      await open(); await settle(100)
      const line = document.querySelector('[data-execution-view] > svg line')
      expect(line).not.toBeNull()
      expect([line!.getAttribute('x1'), line!.getAttribute('y1'), line!.getAttribute('x2')]).toEqual(['240', '140', '692'])
    } finally { rects.mockRestore() }
  })

  it('unmounts with no polling updates after in-flight completion', async () => {
    const late = deferred<LayoutView>(); layouts.i1 = [late.promise]
    const utils = await open(); utils.unmount()
    const before = calls.length
    late.resolve(pending()); await settle(POLL_INTERVAL_MS * 10)
    expect(calls.length).toBe(before)
    expect(vi.getTimerCount()).toBe(0)
  })
})
