// Real @xyflow/react (no module mock): asserts the DOM that GraphView actually
// renders through the shared ArrangedGraph. Arranged mode must draw loop
// returns as routed paths (data-edge-kind="loop_return"), visually distinct
// from forward edges; the legacy fallback (no geometry) keeps loop containers
// and the LoopBackEdge.
import { render, act } from '@testing-library/react'
import { describe, it, expect, beforeAll, afterAll, vi } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { LayoutView } from '../graph/arranged/types'
import { fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'
// Controllable unit font boundary. The real asset/face/coverage producer is exercised
// by P3T5/P3T14. Success requires nonempty actual text and the exact bundled contract;
// tests replace this method with a rejection/deferred promise for font sink cases.
const fontBoundary = vi.hoisted(() => ({ load: vi.fn(async (_doc: Document, texts: readonly string[], contract: {version: string; fontFamily: string}) => {
  if (!texts.length || !texts.every(t => typeof t === 'string') || contract.version !== 'arranged-renderer/2' || contract.fontFamily !== 'Inter, Noto Sans SC, Noto Emoji') throw new Error('invalid font request')
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
    expect(loop.getAttribute('d')).toBe('M 130,312 L 130,330 L 268,330 L 268,116 L 140,116 L 140,140')
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
