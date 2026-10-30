// Actual shared renderer/ReactFlow store -> CalloutConnector SVG endpoint.
// Client rects are deterministic unit inputs; real browser layout is P3T14.
import { act, cleanup, render } from '@testing-library/react'
import { ReactFlowProvider } from '@xyflow/react'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import ArrangedGraph from './arranged/ArrangedGraph'
import { fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from './arranged/testFixtures'
import CalloutConnector from './CalloutConnector'

vi.mock('./arranged/fonts', () => ({ ensureBundledTextFonts: async (doc: Document, texts: readonly string[], contract: { version: string }) => {
  if (doc !== document || !texts.includes('Start') || !texts.includes('again') || contract.version !== 'arranged-renderer/3') throw new Error('wrong font input')
  return { fontsReady: true, fontFamilyResolved: true }
} }))
const saved = new Map<string, PropertyDescriptor | undefined>()
beforeAll(() => {
  for (const key of ['offsetWidth', 'offsetHeight']) saved.set(key, Object.getOwnPropertyDescriptor(HTMLElement.prototype, key))
  saved.set('getBBox', Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getBBox'))
  for (const [key, style] of [['offsetWidth', 'width'], ['offsetHeight', 'height']] as const) {
    Object.defineProperty(HTMLElement.prototype, key, { configurable: true, get() { return parseFloat((this as HTMLElement).style[style]) || 10 } })
  }
  Object.defineProperty(SVGElement.prototype, 'getBBox', { configurable: true, value: () => ({ x: 0, y: 0, width: 10, height: 10 }) })
})
afterAll(() => {
  for (const [key, descriptor] of saved) {
    const proto = key === 'getBBox' ? SVGElement.prototype : HTMLElement.prototype
    if (descriptor) Object.defineProperty(proto, key, descriptor); else Reflect.deleteProperty(proto, key)
  }
})
beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('ResizeObserver', class {
    constructor(private cb: ResizeObserverCallback) {}
    observe(target: Element) { setTimeout(() => this.cb([{ target } as ResizeObserverEntry], this as unknown as ResizeObserver), 0) }
    unobserve() {} disconnect() {}
  })
  vi.stubGlobal('DOMMatrixReadOnly', class { m22 = 1 })
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const scope = this.closest('[data-test-graph]')
    const second = scope?.getAttribute('data-test-graph') === 'second'
    const top = second ? 90 : 30, right = second ? 240 : 120
    const width = second ? 160 : 80, height = second ? 100 : 40
    if (this.classList.contains('react-flow__node')) return { x: right - width, y: top, left: right - width, top, right, bottom: top + height, width, height, toJSON() {} }
    if (this.className === 'divider') return { x: 700, y: 0, left: 700, right: 704, top: 0, bottom: 600, width: 4, height: 600, toJSON() {} }
    return { x: 0, y: 0, left: 0, top: 0, right: 800, bottom: 600, width: 800, height: 600, toJSON() {} }
  })
})
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
async function settle() { for (let i = 0; i < 8; i++) await act(async () => { await vi.advanceTimersByTimeAsync(30) }) }
function graph(scope: string, { x = 40, w = 320, h = 200, selected = 'b', zoom = 0.5, generation = 1 } = {}) {
  return <div data-test-graph={scope} data-execution-view="true" style={{ width: 800, height: 600 }}><ReactFlowProvider>
    <ArrangedGraph workflowDef={fixtureWorkflow()}
      view={fixtureView({ resultGeneration: generation, geometry: fixtureGeometryWithBoxes({ b: { x, y: 140, w, h } }) })}
      selectedNodeId={selected} anchorNodeId="b" onNodeClick={() => {}} fixedViewport={{ x: 10, y: 20, zoom }} />
    <div className="divider" />
    <CalloutConnector nodeId={selected} visible />
  </ReactFlowProvider></div>
}
const circle = (scope: string) => document.querySelector(`[data-test-graph="${scope}"] circle`)
const endpoint = (scope: string) => [circle(scope)?.getAttribute('cx'), circle(scope)?.getAttribute('cy')]
const hideDOM = (scope: string) => document.querySelector(`[data-test-graph="${scope}"] .react-flow__node[data-id="b"]`)!.setAttribute('data-id', 'hidden-from-dom-lookup')

describe('arranged callout provenance', () => {
  it('scopes the actual DOM boundary to its owning graph when IDs overlap', async () => {
    render(<>{graph('first')}{graph('second')}</>); await settle()
    expect(endpoint('first')).toEqual(['120', '50'])
    expect(endpoint('second')).toEqual(['240', '140'])
    expect(document.querySelector('[data-test-graph="second"] line')!.getAttribute('x2')).toBe('692')
  })

  it('uses payload dimensions and viewport for absent DOM, and follows moved/resized/refetched boxes', async () => {
    const utils = render(graph('second')); await settle(); hideDOM('second'); await settle()
    // (40 + 320, 140 + 200/2) * .5 + (10,20)
    expect(endpoint('second')).toEqual(['190', '140'])
    utils.rerender(graph('second', { x: 80, w: 400, h: 240, zoom: 1, generation: 2 })); await settle()
    hideDOM('second'); await settle()
    expect(endpoint('second')).toEqual(['490', '280'])
    utils.rerender(graph('second', { selected: 'a', zoom: 1, generation: 3 })); await settle()
    expect(circle('second')).not.toBeNull()
    utils.rerender(graph('second', { selected: 'unknown' })); await settle()
    expect(circle('second')).toBeNull()
  })

  it('escapes quote/bracket IDs within the owning store DOM, without selecting another graph', async () => {
    const special = 'node"[odd]'
    const def = fixtureWorkflow(); def.nodes[1].id = special
    def.edges = def.edges.map(e => ({ ...e, source: e.source === 'b' ? special : e.source, target: e.target === 'b' ? special : e.target }))
    const geometry = fixtureGeometryWithBoxes({ b: { x: 40, y: 140, w: 320, h: 200 } })
    geometry.nodes[1].id = special
    geometry.edges = geometry.edges.map(e => ({ ...e, source: e.source === 'b' ? special : e.source, target: e.target === 'b' ? special : e.target,
      sourceAttach: { ...e.sourceAttach, nodeId: e.sourceAttach.nodeId === 'b' ? special : e.sourceAttach.nodeId },
      targetAttach: { ...e.targetAttach, nodeId: e.targetAttach.nodeId === 'b' ? special : e.targetAttach.nodeId } }))
    render(<div data-test-graph="second" data-execution-view="true"><ReactFlowProvider>
      <ArrangedGraph workflowDef={def} view={fixtureView({ geometry })} selectedNodeId={special} onNodeClick={() => {}} anchorNodeId={special} fixedViewport={{ x: 10, y: 20, zoom: .5 }} />
      <div className="divider" /><CalloutConnector nodeId={special} visible />
    </ReactFlowProvider></div>)
    await settle()
    expect(endpoint('second')).toEqual(['240', '140'])
  })
})
