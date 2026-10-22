// Real React Flow. Only jsdom's missing layout primitives and the font port are stubbed.
import { act, render, waitFor } from '@testing-library/react'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import ArrangedGraph from './ArrangedGraph'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from './testFixtures'
import type { RendererContract } from './types'
import type { WorkflowDefinition } from '../types'

vi.mock('./fonts', async () => {
  const { default: contract } = await import('./contract.json')
  return { ensureBundledTextFonts: async (doc: Document, texts: readonly string[], actual: RendererContract) => {
    const expected = ['Start', 'script', 'Draft a very long label that must wrap', 'llm call', 'Check', 'conditional', 'yes', 'again']
    if (doc !== document || JSON.stringify(actual) !== JSON.stringify(contract) ||
        JSON.stringify([...texts].sort()) !== JSON.stringify(expected.sort())) throw new Error('invalid fixture font request')
    return { fontsReady: true, fontFamilyResolved: true }
  } }
})
const saved: { ow?: PropertyDescriptor; oh?: PropertyDescriptor; bbox?: PropertyDescriptor } = {}
beforeAll(() => {
  saved.ow = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth')
  saved.oh = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')
  saved.bbox = Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getBBox')
  class StubResizeObserver {
    constructor(private cb: ResizeObserverCallback) {}
    observe(el: Element) { setTimeout(() => this.cb([{ target: el } as ResizeObserverEntry], this as unknown as ResizeObserver), 0) }
    unobserve() {}
    disconnect() {}
  }
  class StubDOMMatrix {
    m22: number
    constructor(transform?: string) { this.m22 = Number(/scale\(([\d.]+)\)/.exec(transform ?? '')?.[1] ?? 1) }
  }
  vi.stubGlobal('ResizeObserver', StubResizeObserver)
  vi.stubGlobal('DOMMatrixReadOnly', StubDOMMatrix)
  Object.defineProperty(SVGElement.prototype, 'getBBox', { configurable: true, value: () => ({ x: 0, y: 0, width: 10, height: 10 }) })
  for (const [key, style] of [['offsetWidth', 'width'], ['offsetHeight', 'height']] as const) {
    Object.defineProperty(HTMLElement.prototype, key, { configurable: true, get() { return parseFloat((this as HTMLElement).style[style]) || 10 } })
  }
})
afterAll(() => {
  vi.unstubAllGlobals()
  for (const [proto, key, descriptor] of [
    [HTMLElement.prototype, 'offsetWidth', saved.ow], [HTMLElement.prototype, 'offsetHeight', saved.oh],
    [SVGElement.prototype, 'getBBox', saved.bbox],
  ] as const) {
    if (descriptor) Object.defineProperty(proto, key, descriptor)
    else Reflect.deleteProperty(proto, key)
  }
})
function graph(view = fixtureView(), def: WorkflowDefinition = fixtureWorkflow()) {
  return <div style={{ width: 800, height: 600 }}><ReactFlowProvider>
    <ArrangedGraph workflowDef={def} view={view} selectedNodeId="b" executionStates={{ b: 'running' }}
      onNodeClick={() => {}} anchorNodeId="a" fixedViewport={{ x: 0, y: 0, zoom: 0.9 }} />
  </ReactFlowProvider></div>
}
async function pathsReady(container: HTMLElement, count = 3) {
  await act(async () => { await new Promise(r => setTimeout(r, 30)) })
  await waitFor(() => expect(container.querySelectorAll('[data-edge-path]')).toHaveLength(count))
}
describe('ArrangedGraph DOM contract', () => {
  it('centres the actual interactive viewport at readable zoom after initialisation', async () => {
    const { container } = render(<div style={{ width: 800, height: 600 }}><ReactFlowProvider>
      <ArrangedGraph workflowDef={fixtureWorkflow()} view={fixtureView()} selectedNodeId={null}
        onNodeClick={() => {}} anchorNodeId="b" />
    </ReactFlowProvider></div>)
    await pathsReady(container)
    // The dimension shim reads the flow's 100% width/height as 100×100.
    await waitFor(() => expect((container.querySelector('.react-flow__viewport') as HTMLElement).style.transform).toBe('translate(-76px,-107.5px) scale(0.9)'))
  })
  it('mounts real registered nodes, exact boxes/text/positions, routes, labels and active overlay', async () => {
    const { container } = render(graph())
    await pathsReady(container)
    expect(container.querySelector('[data-arranged-canvas="true"] .react-flow')).not.toBeNull()
    const geo = fixtureGeometry()
    for (const n of geo.nodes) {
      const root = container.querySelector(`[data-arranged-node="${n.id}"]`) as HTMLElement
      expect(root.style.width).toBe(`${n.box.w}px`)
      expect(root.style.height).toBe(`${n.box.h}px`)
      expect(root.querySelector('[data-node-text="label"]')!.textContent).toBe(n.text)
      expect(root.querySelector('[data-node-text="type"]')!.textContent).toBe(n.typeCaption)
      const wrapper = root.closest('.react-flow__node') as HTMLElement
      expect(wrapper.style.transform).toBe(`translate(${n.box.x}px,${n.box.y}px)`)
      expect(wrapper.classList.contains('draggable')).toBe(false)
    }
    expect(container.querySelector('[data-arranged-node="b"]')!.getAttribute('data-state')).toBe('running')
    expect(container.querySelector('[data-edge-overlay="1"]')).not.toBeNull()
    expect(container.querySelector('[data-edge-overlay="0"]')).toBeNull()
    expect(container.querySelector('[data-edge-path="1"]')!.getAttribute('d')).toBe('M 140,210 L 140,235 L 130,235 L 130,260')
    for (const route of geo.edges) {
      const path = container.querySelector(`[data-edge-path="${route.ordinal}"]`)!
      expect(path.getAttribute('data-edge-kind')).toBe(route.kind)
      const id = path.getAttribute('marker-end')!.slice(5, -1)
      expect(id).toMatch(new RegExp(`-arranged-arrow-${route.ordinal}$`))
      expect(path.closest('g')!.querySelector(`[id="${id}"]`)).not.toBeNull()
      if (route.text) {
        const label = container.querySelector(`[data-edge-label="${route.ordinal}"]`) as HTMLElement
        expect(label.textContent).toBe(route.text.text)
        expect(label.style.transform).toBe(`translate(${route.text.box.x}px, ${route.text.box.y}px)`)
      }
    }
  })
  it('moves actual node wrappers and edge paths with a changed geometry payload', async () => {
    const geometry = fixtureGeometryWithBoxes({ b: { x: 400, y: 160, w: 300, h: 100 } })
    const { container } = render(graph(fixtureView({ geometry })))
    await pathsReady(container)
    expect((container.querySelector('[data-arranged-node="b"]')!.closest('.react-flow__node') as HTMLElement).style.transform).toBe('translate(400px,160px)')
    expect(container.querySelector('[data-edge-path="1"]')!.getAttribute('d')).toContain('M 550,260')
  })
  it('uses legacy components and no arranged DOM when geometry is absent', async () => {
    const { container } = render(graph(fixtureView({ geometry: undefined })))
    await waitFor(() => expect(container.querySelectorAll('.react-flow__node-arbitronNode')).toHaveLength(3))
    expect(container.querySelector('[data-layout-mode="fallback"]')).not.toBeNull()
    expect(container.querySelector('[data-arranged-node]')).toBeNull()
    expect(container.querySelector('[data-edge-path]')).toBeNull()
  })
  it('namespaces two same-ordinal graph markers and retains the second arrow after first unmounts', async () => {
    const def = fixtureWorkflow(); def.edges = [def.edges[2], def.edges[0], def.edges[1]]
    const geometry = fixtureGeometry(); geometry.edges = [geometry.edges[2], geometry.edges[0], geometry.edges[1]].map((e, ordinal) => ({ ...e, ordinal }))
    const second = graph(fixtureView({ geometry }), def)
    const { container, rerender } = render(<><section key="first">{graph()}</section><section key="second">{second}</section></>)
    await pathsReady(container, 6)
    const sections = container.querySelectorAll('section')
    const firstPath = sections[0].querySelector('[data-edge-path="0"]')!
    const secondPath = sections[1].querySelector('[data-edge-path="0"]')!
    const id = secondPath.getAttribute('marker-end')!.slice(5, -1)
    expect(firstPath.getAttribute('marker-end')).not.toBe(secondPath.getAttribute('marker-end'))
    expect(sections[0].querySelector('marker path')!.getAttribute('fill')).toBe('#8a8ab0')
    expect(sections[1].querySelector('marker path')!.getAttribute('fill')).toBe('#7c8cf5')
    const markers = [...container.querySelectorAll('marker')].map(m => m.id)
    expect(new Set(markers).size).toBe(6)
    rerender(<><section key="second">{second}</section></>)
    await pathsReady(container)
    expect(container.querySelector('[data-edge-path="0"]')!.getAttribute('marker-end')).toBe(`url(#${id})`)
    const marker = container.querySelector(`[id="${id}"]`)!
    expect(marker.querySelector('path')!.getAttribute('fill')).toBe('#7c8cf5')
    expect(marker.getAttribute('markerUnits')).toBe('userSpaceOnUse')
  })
})
