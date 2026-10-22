import { render, waitFor } from '@testing-library/react'
import { beforeAll, afterAll, describe, expect, it, vi } from 'vitest'
import { ReactFlowProvider } from '@xyflow/react'
import ArrangedGraph from './ArrangedGraph'
import v2 from './groupGeometry.v2.json'
import type { Geometry, LayoutView } from './types'
import type { GroupGraph } from '../../api/types'
import type { WorkflowDefinition } from '../types'
import { fixtureView } from './testFixtures'
import { buildRoutedPath } from './routedPath'
import { buildGroupProbeCases } from '../../render/groupProbes'
import { buildSurfaceModel, RenderSurface } from '../../render/RenderSurface'

vi.mock('./fonts', () => ({ ensureBundledTextFonts: async (_doc: Document, texts: readonly string[]) => {
  if (!texts.some(t => t.startsWith('Parallel '))) throw new Error('group font input missing')
  return { fontsReady: true, fontFamilyResolved: true }
} }))
const descriptors = new Map<string, PropertyDescriptor | undefined>()
beforeAll(() => {
  class Resize { constructor(private cb: ResizeObserverCallback) {} observe(target: Element) { setTimeout(() => this.cb([{ target } as ResizeObserverEntry], this as unknown as ResizeObserver), 0) } unobserve() {} disconnect() {} }
  vi.stubGlobal('ResizeObserver', Resize)
  vi.stubGlobal('DOMMatrixReadOnly', class { m22 = 1 })
  descriptors.set('getBBox', Object.getOwnPropertyDescriptor(SVGElement.prototype, 'getBBox'))
  Object.defineProperty(SVGElement.prototype, 'getBBox', { configurable: true, value: () => ({ x: 0, y: 0, width: 10, height: 10 }) })
  for (const [key, style] of [['offsetWidth', 'width'], ['offsetHeight', 'height']] as const) {
    descriptors.set(key, Object.getOwnPropertyDescriptor(HTMLElement.prototype, key))
    Object.defineProperty(HTMLElement.prototype, key, { configurable: true, get() { return parseFloat((this as HTMLElement).style[style]) || 10 } })
  }
})
afterAll(() => {
  vi.unstubAllGlobals()
  for (const [key, descriptor] of descriptors) {
    const proto = key === 'getBBox' ? SVGElement.prototype : HTMLElement.prototype
    if (descriptor) Object.defineProperty(proto, key, descriptor); else Reflect.deleteProperty(proto, key)
  }
})

describe('ArrangedGraph actual group DOM', () => {
  it('feeds captured contexts through RenderSurface into real ownership indicators', async () => {
    const f = structuredClone(v2[1]) as unknown as { definition: WorkflowDefinition; groups: GroupGraph; geometry: Geometry }
    const model = buildSurfaceModel({ mode: 'render', revision: 'surface-context', attempt: 1, geometry: f.geometry,
      workflowGroups: f.groups, workflowNodeTypes: Object.fromEntries(f.definition.nodes.map(n => [n.id, n.type])), states: [] })
    const cases = buildGroupProbeCases(f.groups)
    const { container, rerender } = render(<RenderSurface {...model} probe={cases.find(c => c.name === 'group:badges')!} />)
    await waitFor(() => expect(container.querySelector('[data-arranged-node][aria-label="continuing independently"]')).not.toBeNull())
    rerender(<RenderSurface {...model} probe={cases.find(c => c.name === 'group:redacted')!} />)
    await waitFor(() => expect(container.querySelector('[data-arranged-node][aria-label="hidden dependency"]')).not.toBeNull())
    expect(container.querySelector('[data-arranged-node][aria-label="continuing independently"]')).toBeNull()
  })
  it('feeds probe task keys/status and redaction into visible rows without moving geometry', async () => {
    const f = structuredClone(v2[2]) as unknown as { definition: WorkflowDefinition; groups: GroupGraph; geometry: Geometry }
    f.definition.presentation = { revision: 'probe-dom' } as WorkflowDefinition['presentation']
    const view = fixtureView({ savedRevision: 'probe-dom', groups: f.groups, geometry: f.geometry })
    const cases = buildGroupProbeCases(f.groups)
    const mount = (name: string) => {
      const probe = cases.find(c => c.name === name)!
      return <div style={{ width: 1500, height: 1500 }}><ReactFlowProvider><ArrangedGraph
        workflowDef={f.definition} view={view} groupLive={probe.groupLive} nodeContexts={probe.nodeContexts}
        selectedNodeIds={probe.selectedNodeIds} selectedNodeId={null} onNodeClick={() => {}} anchorNodeId={null}
        fixedViewport={{ x: 0, y: 0, zoom: 1 }} /></ReactFlowProvider></div>
    }
    const { container, rerender } = render(mount('group:task:unfinished'))
    const texts = () => [...container.querySelectorAll('[data-group-text]')].map(el => el.textContent)
    await waitFor(() => expect(texts()).toContain('unfinished'))
    expect(texts()).toContain('gathered as unfinished')
    const boxes = () => [...container.querySelectorAll<HTMLElement>('[data-arranged-group-element]')].map(el => [el.style.width, el.style.height])
    const before = boxes()
    rerender(mount('group:badges'))
    await waitFor(() => expect(texts()).toContain('cancel requested'))
    expect(texts()).toContain('non-blocking')
    expect(texts()).toContain('winner')
    rerender(mount('group:redacted'))
    await waitFor(() => expect(texts()).toContain('hidden dependency'))
    for (const e of f.geometry.groupElements!.filter(e => e.role === 'task')) {
      const title = e.texts.find(t => JSON.parse(t.key)[2] === 'title')!
      expect(texts()).not.toContain(title.text)
    }
    expect(boxes()).toEqual(before)
    const winner = cases.find(c => c.name.startsWith('["group-probe","winner"'))!
    rerender(mount(winner.name))
    await waitFor(() => expect(texts()).toContain(`winner: ${Object.values(winner.groupLive).find(l => l.winner)!.winner}`))
  })
  it.each([1, 2, 3, 4])('paints every element/text/real and synthetic route from fixture %i', async index => {
    const f = structuredClone(v2[index]) as unknown as { definition: WorkflowDefinition; groups: GroupGraph; geometry: Geometry }
    f.definition.presentation = { revision: 'group-dom' } as WorkflowDefinition['presentation']
    const view: LayoutView = fixtureView({ savedRevision: 'group-dom', groups: f.groups, geometry: f.geometry })
    const { container } = render(<div style={{ width: 1500, height: 1500 }}><ReactFlowProvider><ArrangedGraph
      workflowDef={f.definition} view={view} selectedNodeId={null} onNodeClick={() => {}} anchorNodeId={null}
      fixedViewport={{ x: 0, y: 0, zoom: 1 }} /></ReactFlowProvider></div>)
    await waitFor(() => expect(container.querySelectorAll('[data-arranged-group-element]')).toHaveLength(f.geometry.groupElements!.length))
    await waitFor(() => expect(container.querySelectorAll('path[data-group-connector]')).toHaveLength(f.geometry.groupConnectors!.length))
    expect(container.querySelectorAll('[data-arranged-node]')).toHaveLength(f.geometry.nodes.length)
    for (const e of f.geometry.groupElements!) {
      const el = [...container.querySelectorAll<HTMLElement>('[data-arranged-group-element]')].find(el => el.dataset.arrangedGroupElement === e.id)!
      expect(el.style.width).toBe(`${e.box.w}px`)
      expect(el.style.height).toBe(`${e.box.h}px`)
      expect(el.style.borderWidth).toBe(`${f.geometry.contract.nodeBorderPx}px`)
      for (const t of e.texts) {
        const text = [...el.querySelectorAll('[data-group-text]')].find(el => el.getAttribute('data-group-text') === t.key)!
        expect(text.textContent).toBe(t.text)
        expect((text as HTMLElement).style.textOverflow).not.toBe('ellipsis')
      }
    }
    const routes = [...f.geometry.edges.map(e => ({ key: `edge:${e.ordinal}`, points: e.points, selector: 'data-edge-path', id: String(e.ordinal), pattern: e.kind === 'forward' ? '' : f.geometry.contract.loopReturnDash })), ...f.geometry.groupConnectors!.map(c => ({ key: c.id, points: c.points, selector: 'data-group-connector', id: c.id, pattern: f.geometry.contract.groupConnectorDash }))]
    const crossings = [...f.geometry.crossings.map(c => ({ ...c, over: `edge:${c.over}`, under: `edge:${c.under}` })), ...(f.geometry.groupCrossings ?? [])]
    for (const route of routes) {
      const path = [...container.querySelectorAll<SVGPathElement>(`path[${route.selector}]`)].find(el => el.getAttribute(route.selector) === route.id)!
      expect(path).toBeDefined()
      const gaps = crossings.filter(c => c.under === route.key).map(c => c.point)
      expect(path.getAttribute('d')).toBe(buildRoutedPath(route.points, gaps, f.geometry.contract.crossingGapPx, route.pattern ? { onPx: parseFloat(route.pattern), overs: crossings.filter(c => c.over === route.key).map(c => c.point) } : undefined))
      const marker = path.getAttribute('marker-end')!.match(/#([^)]*)/)![1]
      expect([...container.querySelectorAll('marker')].filter(m => m.id === marker)).toHaveLength(1)
    }
  })
})
