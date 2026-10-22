import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import type { ReactFlowProps, Node } from '@xyflow/react'
import type { RendererContract } from './types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from './testFixtures'
import { RENDERER_CONTRACT } from './validate'
import { DEFAULT_ZOOM } from '../constants'
import ArrangedNode from './ArrangedNode'
import RoutedEdge from './RoutedEdge'

const port = vi.hoisted(() => ({
  leaf: null as ReactFlowProps | null,
  setCenter: vi.fn(), fitView: vi.fn(),
  texts: [] as string[],
  load: vi.fn<() => Promise<{ fontsReady: boolean; fontFamilyResolved: boolean }>>(),
}))
vi.mock('@xyflow/react', async () => ({
  ...await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react'),
  useReactFlow: () => ({ setCenter: port.setCenter, fitView: port.fitView }),
  Background: () => <div data-testid="background" />,
  Controls: () => <div data-testid="controls" />,
  ReactFlow: (props: ReactFlowProps) => {
    port.leaf = props
    return <div data-testid="flow"><button onClick={() => props.onNodeClick?.({} as never, props.nodes![1])}>node b</button>{props.children as ReactNode}</div>
  },
}))
vi.mock('./fonts', async () => {
  const { default: contract } = await import('./contract.json')
  return { ensureBundledTextFonts: async (doc: Document, texts: readonly string[], actual: RendererContract) => {
    if (doc !== document || JSON.stringify(actual) !== JSON.stringify(contract)) throw new Error('invalid font contract')
    if (JSON.stringify(texts) !== JSON.stringify(port.texts)) throw new Error('incomplete font text input')
    if (texts.some(t => t.includes('\u0378'))) throw new Error('unsupported text')
    return port.load()
  } }
})
import ArrangedGraph, { nodeCenter, type ArrangedGraphProps } from './ArrangedGraph'

type Ready = { fontsReady: boolean; fontFamilyResolved: boolean }
const ready: Ready = { fontsReady: true, fontFamilyResolved: true }
function deferred() {
  let resolve!: (v: Ready) => void
  let reject!: (e: Error) => void
  const promise = new Promise<Ready>((yes, no) => { resolve = yes; reject = no })
  // Keep deliberate rejection tests handled even against a non-consuming RED stub.
  void promise.catch(() => {})
  return { promise, resolve, reject }
}
function props(overrides: Partial<ArrangedGraphProps> = {}): ArrangedGraphProps {
  return { workflowDef: fixtureWorkflow(), view: fixtureView(), selectedNodeId: null,
    onNodeClick: vi.fn(), anchorNodeId: 'b', ...overrides }
}
function mode(container: HTMLElement) { return container.querySelector('[data-arranged-canvas]')?.getAttribute('data-layout-mode') }
async function settled(container: HTMLElement, expected = 'arranged') {
  await waitFor(() => expect(mode(container)).toBe(expected))
  // DOM commit precedes passive viewport/anchor effects; observe those too.
  await act(async () => {})
}
beforeEach(() => {
  port.leaf = null
  port.setCenter.mockReset(); port.fitView.mockReset(); port.load.mockReset()
  port.load.mockResolvedValue(ready)
  port.texts = fixtureGeometry().nodes.flatMap(n => [n.text, n.typeCaption]).concat(['yes', 'again'])
})

describe('ArrangedGraph', () => {
  it('feeds exact geometry, registered types, state, gaps and callbacks to the leaf', async () => {
    const view = fixtureView()
    // Proper crossing: edge 0 horizontal at y116 and the return vertical at x268.
    view.geometry!.edges[0].points = [{ x: 130, y: 92 }, { x: 130, y: 100 }, { x: 300, y: 100 }, { x: 300, y: 130 }, { x: 140, y: 130 }, { x: 140, y: 140 }]
    view.geometry!.crossings = [{ point: { x: 268, y: 130 }, over: 0, under: 2 }]
    const p = props({ view, executionStates: { b: 'running' }, selectedNodeIds: new Set(['a']), selectedNodeId: 'b',
      children: <span>child control</span>, onMoveStart: vi.fn(), onAnchorPosition: vi.fn() })
    const { container, getByText } = render(<ArrangedGraph {...p} />)
    await settled(container)
    const leaf = port.leaf!
    expect(leaf.nodeTypes!.arrangedNode).toBe(ArrangedNode)
    expect(leaf.edgeTypes!.routedEdge).toBe(RoutedEdge)
    expect(leaf.nodes!.map(n => n.position)).toEqual(view.geometry!.nodes.map(n => ({ x: n.box.x, y: n.box.y })))
    expect(leaf.nodes![1]).toMatchObject({ width: 200, height: 70, selected: true, data: { nodeType: 'llm_call', executionState: 'running' } })
    expect(leaf.nodes![0].selected).toBe(true)
    expect(leaf.edges!.map(e => e.data!.route)).toEqual(view.geometry!.edges)
    expect(leaf.edges!.map(e => e.data!.gaps)).toEqual([[], [], [{ x: 268, y: 130 }]])
    expect(leaf.edges!.map(e => e.data!.overs)).toEqual([[{ x: 268, y: 130 }], [], []])
    expect(leaf.edges!.map(e => e.data!.active)).toEqual([false, true, false])
    fireEvent.click(getByText('node b'))
    expect(p.onNodeClick).toHaveBeenCalledWith('b', expect.objectContaining({ id: 'b', data: expect.objectContaining({ definitionNodeId: 'b' }) }))
    expect(getByText('child control')).toBeTruthy()
    act(() => leaf.onMoveStart?.(null, { x: 0, y: 0, zoom: 1 }))
    expect(p.onMoveStart).toHaveBeenCalledWith(null)
    expect(p.onAnchorPosition).toHaveBeenLastCalledWith({ x: 140, y: 175 })
    expect(port.setCenter).toHaveBeenCalledExactlyOnceWith(140, 175, { zoom: DEFAULT_ZOOM })
    expect(leaf.nodesDraggable).toBe(false)
    expect(leaf.nodesConnectable).toBe(false)
    expect(leaf.edgesFocusable).toBe(false)
    expect(leaf.minZoom).toBe(0.2); expect(leaf.maxZoom).toBe(2)
    expect(leaf).not.toHaveProperty('fitView')
    expect(leaf).not.toHaveProperty('onNodeDragStop')
  })
  it('moves payload boxes AND paths/attachments when geometry changes, centring once per result', async () => {
    const p = props()
    const { container, rerender } = render(<ArrangedGraph {...p} />)
    await settled(container)
    rerender(<ArrangedGraph {...p} selectedNodeId="a" executionStates={{ c: 'failed' }} />)
    expect(port.setCenter).toHaveBeenCalledTimes(1)
    const geometry = fixtureGeometryWithBoxes({ b: { x: 400, y: 160, w: 300, h: 100 } })
    rerender(<ArrangedGraph {...p} view={fixtureView({ geometry, resultGeneration: 2 })} />)
    await settled(container)
    expect(port.leaf!.nodes![1].position).toEqual({ x: 400, y: 160 })
    expect(port.leaf!.edges![1].data!.route).toEqual(geometry.edges[1])
    expect(port.setCenter).toHaveBeenLastCalledWith(550, 210, { zoom: DEFAULT_ZOOM })
    expect(port.setCenter).toHaveBeenCalledTimes(2)
  })
  it.each(['null', 'missing', 'empty', 'version', 'foreign', 'contract', 'type'])(
    'falls back for %s geometry without using foreign retained positions', async kind => {
      const p = props()
      p.workflowDef.nodes[0].position = { x: 10, y: 20 }
      p.view!.retained = { source: 'layout', positions: { a: { x: 901, y: 902 } } }
      if (kind === 'null') p.view = null
      if (kind === 'missing') p.view!.geometry = undefined
      if (kind === 'empty') p.view!.geometry!.nodes = []
      if (kind === 'version') p.view!.geometry!.contract.version = 'other'
      if (kind === 'foreign') { p.view!.savedRevision = 'other'; p.view!.geometry = undefined }
      if (kind === 'contract') p.view!.geometry!.contract.nodeLabelFontPx++
      if (kind === 'type') { p.workflowDef.nodes[0].type = ''; p.view!.geometry!.nodes[0].typeCaption = '' }
      const issues = vi.fn()
      const { container } = render(<ArrangedGraph {...p} onDisplayIssues={issues} />)
      await settled(container, 'fallback')
      expect(port.leaf!.nodes!.find(n => n.id === 'a')!.position).toEqual(
        ['foreign', 'null'].includes(kind) ? { x: 10, y: 20 } : { x: 901, y: 902 },
      )
      expect(port.leaf!.nodes!.every(n => n.type !== 'arrangedNode')).toBe(true)
      if (!['null', 'missing'].includes(kind)) expect(issues.mock.lastCall![0].length).toBeGreaterThan(0)
    },
  )
  it('merges retained/manifest positions and stacks missing nodes; null view uses manifest then dagre', async () => {
    const p = props({ view: fixtureView({ geometry: undefined, retained: { source: 'layout', positions: { a: { x: 20, y: 30 } } } }) })
    p.workflowDef.nodes[1].position = { x: 50, y: 300 }
    const { container, rerender } = render(<ArrangedGraph {...p} />)
    await settled(container, 'fallback')
    expect(port.leaf!.nodes!.find(n => n.id === 'c')!.position).toEqual({ x: 20, y: 420 })
    rerender(<ArrangedGraph {...p} view={null} />)
    expect(port.leaf!.nodes!.find(n => n.id === 'a')!.position).toEqual({ x: 50, y: 420 })
    rerender(<ArrangedGraph {...p} view={null} workflowDef={fixtureWorkflow()} />)
    expect(port.leaf!.nodes!.find(n => n.id === 'a')!.position).not.toEqual({ x: 50, y: 420 })
  })
  it('waits for fonts without premature fallback centring, then clears the issue', async () => {
    const load = deferred(); port.load.mockReturnValue(load.promise)
    const issues = vi.fn()
    const { container, getByText } = render(<ArrangedGraph {...props()} onDisplayIssues={issues} />)
    expect(mode(container)).toBe('loading')
    expect(getByText(/Loading reviewed fonts/)).toBeTruthy()
    expect(port.setCenter).not.toHaveBeenCalled()
    expect(port.leaf).toBeNull()
    expect(issues).toHaveBeenLastCalledWith(['Loading reviewed fonts'])
    await act(async () => load.resolve(ready))
    await settled(container)
    expect(issues).toHaveBeenLastCalledWith([])
    expect(port.setCenter).toHaveBeenCalledTimes(1)
  })
  it.each(['unsupported bundled-font scalar', 'font asset unavailable', 'font subset not loaded'])(
    'fails closed on %s, reporting fallback issues', async error => {
      port.load.mockRejectedValue(new Error(error))
      const issues = vi.fn()
      const { container } = render(<ArrangedGraph {...props()} onDisplayIssues={issues} />)
      await settled(container, 'fallback')
      expect(port.leaf!.nodes!.every(n => n.type !== 'arrangedNode')).toBe(true)
      expect(issues.mock.lastCall![0].join(' ')).toContain(error)
    },
  )
  it('offers a fresh-page font reload only when reviewed fonts failed for valid geometry', async () => {
    const reload = vi.fn()
    const original = window.location
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, reload } })
    try {
      port.load.mockRejectedValue(new Error('font asset unavailable'))
      const { container, getByRole } = render(<ArrangedGraph {...props()} />)
      await settled(container, 'fallback')
      fireEvent.click(getByRole('button', { name: 'Reload reviewed fonts' }))
      expect(reload).toHaveBeenCalledTimes(1)
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original })
    }
  })
  it('offers no font reload while loading, when arranged, without geometry, or on a fixed canvas', async () => {
    const load = deferred(); port.load.mockReturnValueOnce(load.promise)
    const p = props()
    const { container, queryByRole, rerender } = render(<ArrangedGraph {...p} />)
    expect(queryByRole('button', { name: 'Reload reviewed fonts' })).toBeNull()
    await act(async () => load.resolve(ready))
    await settled(container)
    expect(queryByRole('button', { name: 'Reload reviewed fonts' })).toBeNull()
    rerender(<ArrangedGraph {...p} view={fixtureView({ geometry: undefined })} />)
    await settled(container, 'fallback')
    expect(queryByRole('button', { name: 'Reload reviewed fonts' })).toBeNull()
    port.load.mockRejectedValue(new Error('font asset unavailable'))
    rerender(<ArrangedGraph {...p} view={fixtureView({ geometry: fixtureGeometryWithBoxes({ a: { x: 60, y: 40, w: 190, h: 70 } }) })} fixedViewport={{ x: 0, y: 0, zoom: 0.9 }} />)
    await settled(container, 'fallback')
    expect(queryByRole('button', { name: 'Reload reviewed fonts' })).toBeNull()
  })
  it('rejects unsupported text rather than drawing reviewed geometry', async () => {
    const p = props(); p.workflowDef.nodes[0].label = '\u0378'; p.view!.geometry!.nodes[0].text = '\u0378'
    port.texts[0] = '\u0378'
    const issues = vi.fn()
    const { container } = render(<ArrangedGraph {...p} onDisplayIssues={issues} />)
    await settled(container, 'fallback')
    expect(issues.mock.lastCall![0].join(' ')).toContain('unsupported text')
  })
  it('rejects readiness false, then retries for new geometry', async () => {
    port.load.mockResolvedValueOnce({ fontsReady: true, fontFamilyResolved: false })
    const p = props(); const { container, rerender } = render(<ArrangedGraph {...p} />)
    await settled(container, 'fallback')
    rerender(<ArrangedGraph {...p} view={fixtureView({ geometry: fixtureGeometryWithBoxes({ a: { x: 60, y: 40, w: 190, h: 70 } }) })} />)
    await settled(container)
  })
  it.each(['revision', 'digest'])('ignores old font promise after %s changes', async change => {
    const old = deferred(); const current = deferred()
    port.load.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const p = props(); const issues = vi.fn()
    const { container, rerender } = render(<ArrangedGraph {...p} onDisplayIssues={issues} />)
    const next = props()
    if (change === 'revision') { next.workflowDef.presentation!.revision = 'rev-2'; next.view!.savedRevision = 'rev-2' }
    else next.view!.geometry = fixtureGeometryWithBoxes({ a: { x: 60, y: 40, w: 190, h: 70 } })
    rerender(<ArrangedGraph {...next} onDisplayIssues={issues} />)
    await act(async () => current.reject(new Error('new font failure')))
    await settled(container, 'fallback')
    await act(async () => old.resolve(ready))
    expect(mode(container)).toBe('fallback')
    expect(issues.mock.lastCall![0].join(' ')).toContain('new font failure')
  })
  it('does not let an older rejected font load replace newer ready geometry', async () => {
    const old = deferred()
    port.load.mockReturnValueOnce(old.promise).mockResolvedValueOnce(ready)
    const p = props(); const issues = vi.fn()
    const { container, rerender } = render(<ArrangedGraph {...p} onDisplayIssues={issues} />)
    const geometry = fixtureGeometryWithBoxes({ b: { x: 400, y: 160, w: 300, h: 100 } })
    rerender(<ArrangedGraph {...p} view={fixtureView({ geometry, resultGeneration: 2 })} onDisplayIssues={issues} />)
    await settled(container)
    await act(async () => old.reject(new Error('old font failure')))
    expect(mode(container)).toBe('arranged')
    expect(port.leaf!.nodes![1].position).toEqual({ x: 400, y: 160 })
    expect(issues).toHaveBeenLastCalledWith([])
  })
  it('never centres a fixed canvas, including first ready layout and later viewport changes', async () => {
    const p = props({ fixedViewport: { x: 0, y: 0, zoom: 0.9 } })
    const { container, rerender } = render(<ArrangedGraph {...p} />)
    await settled(container)
    expect(port.setCenter).not.toHaveBeenCalled()
    rerender(<ArrangedGraph {...p} fixedViewport={{ x: 20, y: 40, zoom: 1 }} />)
    expect(port.leaf!.viewport).toEqual({ x: 20, y: 40, zoom: 1 })
    expect(port.setCenter).not.toHaveBeenCalled()
  })
  it('reports an unknown anchor as null and never centres it', async () => {
    const onAnchorPosition = vi.fn()
    const { container } = render(<ArrangedGraph {...props({ anchorNodeId: 'unknown', onAnchorPosition })} />)
    await settled(container)
    expect(onAnchorPosition).toHaveBeenLastCalledWith(null)
    expect(port.setCenter).not.toHaveBeenCalled()
  })
  it('centres fallback → arranged once, resets on saved and legacy navigation, not selection', async () => {
    const p = props({ view: null })
    const { container, rerender } = render(<ArrangedGraph {...p} />)
    expect(port.setCenter).toHaveBeenCalledTimes(1)
    rerender(<ArrangedGraph {...p} view={fixtureView()} />)
    await settled(container)
    expect(port.setCenter).toHaveBeenCalledTimes(2)
    const legacy = fixtureWorkflow(); delete legacy.presentation
    rerender(<ArrangedGraph {...p} workflowDef={legacy} />)
    expect(port.setCenter).toHaveBeenCalledTimes(3)
    rerender(<ArrangedGraph {...p} workflowDef={{ ...legacy, name: 'other workflow' }} />)
    expect(port.setCenter).toHaveBeenCalledTimes(4)
    rerender(<ArrangedGraph {...p} workflowDef={{ ...legacy, name: 'other workflow' }} selectedNodeId="c" />)
    expect(port.setCenter).toHaveBeenCalledTimes(4)
  })
  it('fits only on explicit request, pins fixed viewport and disables interaction/chrome', async () => {
    const p = props(); const { container, getByRole, queryByTestId, queryByRole, rerender } = render(<ArrangedGraph {...p} />)
    await settled(container)
    expect(port.fitView).not.toHaveBeenCalled()
    fireEvent.click(getByRole('button', { name: 'Fit whole workflow' }))
    expect(port.fitView).toHaveBeenCalledExactlyOnceWith({ padding: 0.1 })
    port.setCenter.mockClear()
    const fixedViewport = { x: 0, y: 0, zoom: RENDERER_CONTRACT.practicalZoom }
    rerender(<ArrangedGraph {...p} fixedViewport={fixedViewport} />)
    expect(port.leaf!.defaultViewport).toEqual(fixedViewport)
    expect(port.leaf!.viewport).toEqual(fixedViewport)
    for (const key of ['panOnDrag', 'zoomOnScroll', 'zoomOnPinch', 'zoomOnDoubleClick'] as const) expect(port.leaf![key]).toBe(false)
    expect(queryByTestId('background')).toBeNull(); expect(queryByTestId('controls')).toBeNull()
    expect(queryByRole('button', { name: 'Fit whole workflow' })).toBeNull()
    expect(port.setCenter).not.toHaveBeenCalled()
  })
})

describe('nodeCenter', () => {
  it('uses payload dimensions, sized fallback nodes, legacy constants, then null', () => {
    const nodes = [{ id: 'b', position: { x: 700, y: 800 }, width: 300, height: 100, data: {} }] as Node[]
    expect(nodeCenter(fixtureView(), nodes, 'b')).toEqual({ x: 140, y: 175 })
    expect(nodeCenter(null, nodes, 'b')).toEqual({ x: 850, y: 850 })
    delete nodes[0].width; delete nodes[0].height
    expect(nodeCenter(null, nodes, 'b')).toEqual({ x: 800, y: 840 })
    expect(nodeCenter(null, nodes, 'unknown')).toBeNull()
  })
})
