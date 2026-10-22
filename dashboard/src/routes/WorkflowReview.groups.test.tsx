import { render, screen, fireEvent, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { Edge } from '@xyflow/react'
import wireFixtures from '../graph/testdata/group-layout-wire.json'
import type { ComponentType } from 'react'
import type { GroupGraph } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import { mixedEdges, mixedGraph, mixedNodes } from '../graph/groupLayout.fixtures'

// Render the real node components so the assertions reach the visible static
// cards and links, not just the layout hook's intermediate output.
const rendered = vi.hoisted(() => ({ nodes: [] as RFNode[], edges: [] as Edge[] }))
type RFNode = { id: string; type?: string; selected?: boolean; position: { x: number; y: number }; data: Record<string, unknown> }
vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: RFNode[]
    edges: Edge[]
    nodeTypes: Record<string, ComponentType<{ data: unknown; selected?: boolean }>>
    onNodeClick?: (e: unknown, n: RFNode) => void
    onNodeDragStop?: (e: unknown, n: RFNode, nodes: RFNode[]) => void
    children?: React.ReactNode
  }) => {
    rendered.nodes = props.nodes
    rendered.edges = props.edges
    return (
      <div>
        {props.nodes.map((n) => {
          const C = props.nodeTypes[n.type ?? 'arbitronNode']
          return (
            <div key={n.id} data-testid={`node-${n.id}`} data-type={n.type} data-selected={n.selected ? 'true' : 'false'} onClick={() => props.onNodeClick?.({}, n)}>
              <C data={n.data} selected={n.selected} />
              <button type="button" data-testid={`drag-${n.id}`} onClick={(e) => { e.stopPropagation(); props.onNodeDragStop?.({}, { ...n, position: { x: 999, y: 111 } }, []) }} />
            </div>
          )
        })}
        {props.children}
      </div>
    )
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  useReactFlow: () => ({ setCenter: () => {}, fitView: () => {}, fitBounds: () => Promise.resolve(true), getZoom: () => 1 }),
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom', Left: 'left', Right: 'right' },
  MarkerType: { ArrowClosed: 'arrowclosed' },
}))

const apiMock = vi.hoisted(() => ({
  // Arrangement status (#77): no reviewed geometry, so groups use the standard layout.
  me: vi.fn(() => Promise.reject(new Error('API 401: unauthenticated'))),
  workflows: {
    get: vi.fn(), groupLayout: vi.fn(), getStats: vi.fn(), trigger: vi.fn(),
    getLayout: vi.fn(() => Promise.resolve({ savedRevision: '', displayStatus: 'pending', available: false })),
    retryLayout: vi.fn(),
  },
  schedules: { list: vi.fn() },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import WorkflowReview from './WorkflowReview'

const mixedDef: WorkflowDefinition = { name: 'wf-mixed', description: '', author: 'ann', nodes: mixedNodes, edges: mixedEdges }
const plainDef: WorkflowDefinition = {
  name: 'wf-plain', description: '', author: 'ann',
  nodes: [{ id: 'a', type: 'llm_call', label: 'A', description: '', config: {} }, { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} }],
  edges: [{ source: 'a', target: 'b' }],
}
const EMPTY: GroupGraph = { groups: {}, owner: {}, order: [] }

let defs: Record<string, WorkflowDefinition>
let layouts: Record<string, GroupGraph | Error>

beforeEach(() => {
  defs = { 'wf-mixed': mixedDef, 'wf-plain': plainDef }
  layouts = { 'wf-mixed': mixedGraph, 'wf-plain': EMPTY }
  apiMock.workflows.get.mockImplementation((name: string) => (defs[name] ? Promise.resolve(defs[name]) : Promise.reject(new Error('API 404: workflow not found'))))
  apiMock.workflows.groupLayout.mockImplementation((name: string) => {
    const l = layouts[name]
    if (!l) return Promise.reject(new Error('API 404: workflow not found'))
    return l instanceof Error ? Promise.reject(l) : Promise.resolve(l)
  })
  apiMock.workflows.getStats.mockResolvedValue({ totalRuns: 0, successCount: 0, failureCount: 0, avgDurationSeconds: 0 })
  apiMock.schedules.list.mockResolvedValue([])
})
afterEach(() => vi.clearAllMocks())

function renderReview(name: string) {
  return render(
    <MemoryRouter initialEntries={[`/workflows/${name}`]}>
      <Routes>
        <Route path="/workflows/:name" element={<WorkflowReview />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('WorkflowReview — static group layout (happy path)', () => {
  it('fetches the workflow\'s group layout and renders nested boxes with header, footer and task cards', async () => {
    renderReview('wf-mixed')
    expect(await screen.findByTestId('node-group:g1:header')).toBeInTheDocument()
    expect(apiMock.workflows.groupLayout).toHaveBeenCalledWith('wf-mixed', expect.any(AbortSignal))
    expect(screen.getByTestId('node-group:g1:box').getAttribute('data-type')).toBe('groupContainer')
    expect(screen.getByTestId('node-group:w1:box').getAttribute('data-type')).toBe('groupContainer')
    expect(screen.getByTestId('node-group:w1:footer').getAttribute('data-type')).toBe('groupFooter')
    expect(screen.queryByTestId('node-g1')).toBeNull()
    expect(screen.queryByTestId('node-w1')).toBeNull()
    expect(screen.getAllByText('Parallel branches · all succeed').length).toBe(1)
    expect(screen.getAllByText('Parallel workflows · first success').length).toBe(1)
  })

  it('renders the workflows-form tasks as ordered cards linking their child workflows', async () => {
    renderReview('wf-mixed')
    const cards = await screen.findAllByTestId(/^node-group:w1:task:/)
    expect(cards.map((c) => c.getAttribute('data-testid'))).toEqual([
      'node-group:w1:task:t1',
      'node-group:w1:task:t2',
      'node-group:w1:task:t3',
    ])
    expect(within(cards[0]).getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
    expect(within(cards[1]).getByRole('link', { name: 'child-b' }).getAttribute('href')).toBe('/workflows/child-b')
    expect(within(cards[2]).getByRole('link', { name: 'child-a' }).getAttribute('href')).toBe('/workflows/child-a')
    expect(within(cards[0]).queryByText('running')).toBeNull()
    expect(document.querySelector('a[href^="/instances/"]')).toBeNull()
  })
})

describe('WorkflowReview — selection and group details (success)', () => {
  it('header and footer both select the group node and show its ordered tasks with child links', async () => {
    renderReview('wf-mixed')
    fireEvent.click(await screen.findByTestId('node-group:w1:footer'))
    const details = await screen.findByTestId('review-group-details')
    expect(within(details).getByText('First success')).toBeInTheDocument()
    expect(within(details).getByText('cancel unfinished')).toBeInTheDocument()
    expect(within(details).getByText('child workflows')).toBeInTheDocument()
    expect(within(details).getByText('results')).toBeInTheDocument()
    expect(within(details).getByText('b2')).toBeInTheDocument()
    expect(within(details).getByText('b3')).toBeInTheDocument()
    expect(within(details).getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual([
      '/workflows/child-a', '/workflows/child-b', '/workflows/child-a',
    ])
    const items = within(screen.getByTestId('review-group-tasks')).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual(['t1child-a', 't2child-b', 't3child-a'])
    expect(screen.getByTestId('node-group:w1:header').getAttribute('data-selected')).toBe('true')
    expect(screen.getByTestId('node-group:w1:footer').getAttribute('data-selected')).toBe('true')
    expect(screen.getByTestId('node-group:w1:box').getAttribute('data-selected')).toBe('false')

    fireEvent.click(screen.getByTestId('node-group:g1:header'))
    const branches = await screen.findByTestId('review-group-tasks')
    expect(within(branches).getByText('entry a1')).toBeInTheDocument()
    expect(within(branches).getByText('entry w1')).toBeInTheDocument()
  })

  // Auto-save and drag persistence were removed with workflow-auto-arrangement
  // (#77): positions come only from the server's reviewed arrangement, and the
  // dashboard never writes a layout.
})

describe('WorkflowReview — failure and edge cases', () => {
  it('still renders the workflow, with a notice, when the group layout cannot be loaded', async () => {
    layouts['wf-mixed'] = new Error('API 500: analysis failed')
    renderReview('wf-mixed')
    expect(await screen.findByTestId('node-g1')).toBeInTheDocument()
    // The arrangement panel is a status region too; select the group notice.
    expect(screen.getAllByRole('status').some((el) => el.textContent?.includes('Group layout unavailable: API 500: analysis failed'))).toBe(true)
    expect(screen.queryByTestId('node-group:g1:header')).toBeNull()
  })

  it('a workflow without groups renders exactly as before and shows no notice', async () => {
    renderReview('wf-plain')
    expect(await screen.findByTestId('node-a')).toBeInTheDocument()
    expect(screen.getByTestId('node-b')).toBeInTheDocument()
    expect(screen.queryByText(/Group layout unavailable/)).toBeNull()
    expect(document.querySelector('[data-type^="group"]')).toBeNull()
  })
})

// Keep the client real here: a request that ignores AbortSignal (or stalls while
// reading the response body) must still release the page at the deadline.
describe('WorkflowReview — stalled layout request', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('renders the plain graph at the deadline and ignores a late layout response', async () => {
    const { api: realApi } = await vi.importActual<typeof import('../api/client')>('../api/client')
    let finishLayout!: (value: GroupGraph) => void
    let layoutSignal: AbortSignal | undefined
    const layoutBody = new Promise<GroupGraph>((resolve) => { finishLayout = resolve })
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/v1/workflows/wf-mixed/group-layout') {
        layoutSignal = init?.signal as AbortSignal | undefined
        return Promise.resolve({ ok: true, json: () => layoutBody })
      }
      if (url === '/api/v1/workflows/wf-mixed') {
        return Promise.resolve({ ok: true, json: async () => mixedDef })
      }
      throw new Error(`Unexpected request: ${url}`)
    }))
    apiMock.workflows.get.mockImplementation(realApi.workflows.get)
    apiMock.workflows.groupLayout.mockImplementation(realApi.workflows.groupLayout)
    vi.useFakeTimers()
    const view = renderReview('wf-mixed')
    await act(async () => { await vi.advanceTimersByTimeAsync(14_999) })
    // The workflow is loaded; its graph waits for the group analysis or its deadline.
    expect(screen.getByText('Loading layout...')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    console.log('DBG after 15000', !!screen.queryByTestId('node-g1'), screen.queryByText('Loading layout...') ? 'loading' : '')
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    console.log('DBG after flush', !!screen.queryByTestId('node-g1'))
    await act(async () => { await vi.advanceTimersByTimeAsync(100) })
    console.log('DBG after 15100', !!screen.queryByTestId('node-g1'))
    expect(screen.getByTestId('node-g1')).toBeInTheDocument()
    expect(screen.getAllByRole('status').some((el) => /Group layout unavailable:.*timed out/i.test(el.textContent ?? ''))).toBe(true)
    expect(layoutSignal?.aborted).toBe(true)
    await act(async () => { finishLayout(mixedGraph) })
    expect(screen.queryByTestId('node-group:g1:header')).toBeNull()
    expect(screen.getByTestId('node-g1')).toBeInTheDocument()
    view.unmount()
  })

  it('aborts a pending layout request and clears its deadline on unmount', async () => {
    const { api: realApi } = await vi.importActual<typeof import('../api/client')>('../api/client')
    let layoutSignal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/v1/workflows/wf-mixed/group-layout') {
        layoutSignal = init?.signal as AbortSignal | undefined
        return new Promise(() => {})
      }
      throw new Error(`Unexpected request: ${url}`)
    }))
    apiMock.workflows.groupLayout.mockImplementation(realApi.workflows.groupLayout)
    vi.useFakeTimers()
    const view = renderReview('wf-mixed')
    await act(async () => {})
    view.unmount()
    expect(layoutSignal?.aborted).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})

// These bodies are Go-produced HTTP JSON, pinned by
// TestWorkflowHandler_GroupLayout_ReviewWireFixtures. Keep transport parsing,
// normalization, route loading, layout and node components real.
describe('WorkflowReview — partial ownership wire fixtures', () => {
  afterEach(() => vi.unstubAllGlobals())

  for (const fixture of wireFixtures) {
    for (const stored of [false, true]) {
      it(`${fixture.name} keeps unique nodes and connected edges (${stored ? 'stored' : 'dagre'})`, async () => {
        const { api: realApi } = await vi.importActual<typeof import('../api/client')>('../api/client')
        const definition = structuredClone(fixture.definition)
        if (stored) {
          definition.nodes.forEach((n, i) => Object.assign(n, { position: { x: i * 250, y: i * 200 } }))
        }
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
          if (url === `/api/v1/workflows/${fixture.name}`) return new Response(JSON.stringify(definition))
          if (url === `/api/v1/workflows/${fixture.name}/group-layout`) return new Response(JSON.stringify(fixture.layout))
          throw new Error(`Unexpected request: ${url}`)
        }))
        apiMock.workflows.get.mockImplementation(realApi.workflows.get)
        apiMock.workflows.groupLayout.mockImplementation(realApi.workflows.groupLayout)
        renderReview(fixture.name)
        const groupId = fixture.name === 'nested-branches' ? 'g1' : 'g'
        fireEvent.click(await screen.findByTestId(`node-group:${groupId}:header`))
        expect(await screen.findByTestId('review-group-details')).toBeInTheDocument()
        const ids = rendered.nodes.map((n) => n.id)
        expect(new Set(ids).size).toBe(ids.length)
        for (const n of rendered.nodes) {
          expect(Number.isFinite(n.position.x), n.id).toBe(true)
          expect(Number.isFinite(n.position.y), n.id).toBe(true)
        }
        const connectors = [...rendered.edges].sort(
          (a, b) => Number(!!b.data?.presentationOnly) - Number(!!a.data?.presentationOnly),
        )
        for (const e of connectors) {
          expect(ids, `${e.id} source`).toContain(e.source)
          expect(ids, `${e.id} target`).toContain(e.target)
        }
        if (fixture.name === 'duplicate-workflows') {
          const cards = screen.getAllByTestId(/^node-group:g:task:/)
          expect(cards.map((c) => c.getAttribute('data-testid'))).toEqual(['node-group:g:task:dup', 'node-group:g:task:last'])
          expect(within(cards[0]).getByRole('link', { name: 'child-a' })).toHaveAttribute('href', '/workflows/child-a')
          expect(within(cards[1]).getByRole('link', { name: 'child-c' })).toHaveAttribute('href', '/workflows/child-c')
        }
        if (fixture.name === 'shared-body') {
          // The skipped task must not acquire the other task's entry as a sink.
          expect(rendered.edges.filter((e) => e.data?.kind === 'sink').map((e) => e.data?.taskKey)).toEqual(['a'])
        }
      })
    }
  }
})
