import { render } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { describe, it, expect, vi } from 'vitest'
import type { EdgeRoute, Point } from './types'
import { fixtureGeometry } from './testFixtures'
import { RENDERER_CONTRACT } from './validate'
import { buildRoutedPath } from './routedPath'

// EdgeLabelRenderer portals into React Flow's label layer; in isolation portal
// into document.body so labels render as HTML outside the <svg>.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => createPortal(children, document.body),
  }
})

import RoutedEdge, { edgeLabelClassName } from './RoutedEdge'

type Props = Parameters<typeof RoutedEdge>[0]

function edgeProps(route: EdgeRoute, opts: { gaps?: Point[]; overs?: Point[]; active?: boolean } = {}): Props {
  return {
    id: `routed-${route.ordinal}`,
    source: route.source,
    target: route.target,
    // Deliberately far from the route: the component must ignore xyflow's handle coordinates.
    sourceX: 999,
    sourceY: 999,
    targetX: 888,
    targetY: 888,
    sourcePosition: 'bottom',
    targetPosition: 'top',
    data: { route, gaps: opts.gaps ?? [], overs: opts.overs ?? [], contract: RENDERER_CONTRACT, active: opts.active ?? false, graphInstanceId: 'test-graph' },
  } as unknown as Props
}

function renderEdge(p: Props) {
  return render(
    <svg>
      <RoutedEdge {...p} />
    </svg>,
  )
}

const geo = fixtureGeometry()
const forward = geo.edges[1] // b -> c, label "yes"
const loopReturn = geo.edges[2] // c -> b, label "again"

describe('RoutedEdge — draws the persisted route', () => {
  it('builds d from route.points and ignores xyflow sourceX/targetX', () => {
    const { container } = renderEdge(edgeProps(forward))
    const path = container.querySelector(`[data-edge-path="${forward.ordinal}"]`) as SVGPathElement
    // Its 10px jog halves both rounded corners to 5px.
    expect(path.getAttribute('d')).toBe('M 140,210 L 140,230 A 5,5 0 0 1 135,235 A 5,5 0 0 0 130,240 L 130,260')
    expect(path.getAttribute('d')).not.toContain('999')
    expect(path.getAttribute('d')).not.toContain('888')
  })

  it('tags the path with its ordinal and kind', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('data-edge-path')).toBe('2')
    expect(path.getAttribute('data-edge-kind')).toBe('loop_return')
  })

  it('renders crossing gaps as extra M subpaths inside the SAME path', () => {
    const gap = { x: 140, y: 222 }
    const { container } = renderEdge(edgeProps(forward, { gaps: [gap] }))
    const paths = container.querySelectorAll(`[data-edge-path="${forward.ordinal}"]`)
    expect(paths).toHaveLength(1)
    const d = paths[0].getAttribute('d')!
    expect(d).toBe(buildRoutedPath(forward.points, [gap], RENDERER_CONTRACT.crossingGapPx, undefined, { radiusPx: RENDERER_CONTRACT.cornerRadiusPx, overs: [], arrowPx: RENDERER_CONTRACT.arrowPx }))
    // The gap ends 8px above the bend, clear of that 5px corner.
    expect(d).toBe('M 140,210 L 140,217 M 140,227 L 140,230 A 5,5 0 0 1 135,235 A 5,5 0 0 0 130,240 L 130,260')
  })
})

describe('RoutedEdge — dash restarts at crossings', () => {
  it('restarts a loop return\'s dash across a crossing it passes over', () => {
    const over = { x: 200, y: 330 }
    const { container } = renderEdge(edgeProps(loopReturn, { overs: [over] }))
    const d = container.querySelector(`[data-edge-path="${loopReturn.ordinal}"]`)!.getAttribute('d')!
    expect(d).toBe(buildRoutedPath(loopReturn.points, [], RENDERER_CONTRACT.crossingGapPx, { onPx: 6, overs: [over] }, { radiusPx: RENDERER_CONTRACT.cornerRadiusPx, overs: [over], arrowPx: RENDERER_CONTRACT.arrowPx }))
    expect(d).toContain('L 194,330 M 194,330 L 200,330 M 200,330 L 262,330 A 6,6 0 0 0 268,324')
  })

  it('restarts a loop return\'s dash one dash before its own gap', () => {
    const gap = { x: 268, y: 200 }
    const { container } = renderEdge(edgeProps(loopReturn, { gaps: [gap] }))
    const d = container.querySelector(`[data-edge-path="${loopReturn.ordinal}"]`)!.getAttribute('d')!
    expect(d).toContain('L 268,211 M 268,211 L 268,205 M 268,195')
  })

  it('leaves solid forward paths unsplit at crossings they pass over', () => {
    const over = { x: 135, y: 235 }
    const { container } = renderEdge(edgeProps(forward, { overs: [over] }))
    const d = container.querySelector(`[data-edge-path="${forward.ordinal}"]`)!.getAttribute('d')!
    expect(d).toBe('M 140,210 L 140,235 L 130,235 L 130,260')
  })
})

describe('RoutedEdge — stroke, dash and arrowhead', () => {
  it('draws forward edges solid in the contract forward colour', () => {
    const { container } = renderEdge(edgeProps(forward))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.style.stroke).toBe('rgb(138, 138, 176)') // #8a8ab0
    expect(path.style.strokeWidth).toBe('2')
    expect(path.style.strokeDasharray).toBe('')
  })

  it('draws loop returns dashed in the contract loop colour', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.style.stroke).toBe('rgb(124, 140, 245)') // #7c8cf5
    expect(path.style.strokeDasharray).toBe('6 4')
  })

  it('draws self loops dashed like loop returns', () => {
    const self: EdgeRoute = { ...loopReturn, ordinal: 5, kind: 'self_loop', source: 'b', target: 'b' }
    const { container } = renderEdge(edgeProps(self))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('data-edge-kind')).toBe('self_loop')
    expect(path.style.strokeDasharray).toBe('6 4')
  })

  it('defines its own arrow marker <graphInstanceId>-arranged-arrow-<ordinal> and references it from marker-end', () => {
    const { container } = renderEdge(edgeProps(forward))
    const marker = container.querySelector('defs > marker#test-graph-arranged-arrow-1') as SVGMarkerElement
    expect(marker).not.toBeNull()
    expect(marker.getAttribute('markerWidth')).toBe('10')
    expect(marker.getAttribute('markerHeight')).toBe('10')
    expect(marker.getAttribute('orient')).toBe('auto')
    expect(marker.getAttribute('markerUnits')).toBe('userSpaceOnUse')
    expect(marker.querySelector('path')!.getAttribute('fill')).toBe('#8a8ab0')
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('marker-end')).toBe('url(#test-graph-arranged-arrow-1)')
  })

  it('colours a loop-return arrowhead with the loop colour', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    expect(container.querySelector('marker#test-graph-arranged-arrow-2 path')!.getAttribute('fill')).toBe('#7c8cf5')
  })
})

describe('RoutedEdge — active source animation', () => {
  it('adds a separate overlay path with the same d and no arrowhead when active', () => {
    const gap = { x: 140, y: 222 }
    const { container } = renderEdge(edgeProps(forward, { gaps: [gap], active: true }))
    const main = container.querySelector('[data-edge-path]') as SVGPathElement
    const overlay = container.querySelector('[data-edge-overlay]') as SVGPathElement
    expect(overlay).not.toBeNull()
    expect(overlay.getAttribute('class')).toContain('activeOverlay')
    expect(overlay.getAttribute('d')).toBe(main.getAttribute('d'))
    expect(overlay.getAttribute('marker-end')).toBeNull()
    // The edge itself keeps its arrow, solid stroke and gaps.
    expect(main.getAttribute('marker-end')).toBe('url(#test-graph-arranged-arrow-1)')
    expect(main.style.strokeDasharray).toBe('')
  })

  it('renders no overlay when the source is not running', () => {
    const { container } = renderEdge(edgeProps(forward))
    expect(container.querySelector('[data-edge-overlay]')).toBeNull()
  })
})

describe('RoutedEdge — label box', () => {
  it('places the label at its reviewed box with contract padding', () => {
    renderEdge(edgeProps(forward))
    const label = document.body.querySelector('[data-edge-label="1"]') as HTMLElement
    expect(label.textContent).toBe('yes')
    expect(label.className).toBe(edgeLabelClassName)
    expect(label.style.transform).toBe('translate(146px, 222px)')
    expect(label.style.width).toBe('30px')
    expect(label.style.height).toBe('21px')
    expect(label.style.padding).toBe('3px 6px')
    expect(label.style.color).toBe('rgb(224, 224, 224)') // #e0e0e0
  })

  it('uses the loop label colour for loop-return labels', () => {
    renderEdge(edgeProps(loopReturn))
    const label = document.body.querySelector('[data-edge-label="2"]') as HTMLElement
    expect(label.textContent).toBe('again')
    expect(label.style.color).toBe('rgb(154, 166, 255)') // #9aa6ff
  })

  it('renders no label element when the route has no text', () => {
    renderEdge(edgeProps(geo.edges[0]))
    expect(document.body.querySelector('[data-edge-label]')).toBeNull()
  })
})

describe('RoutedEdge — data and marker identity', () => {
  it('keeps two same-ordinal edges scoped after removing the first graph', () => {
    const first = edgeProps({ ...forward, ordinal: 0 })
    const second = edgeProps({ ...loopReturn, ordinal: 0 })
    second.data!.graphInstanceId = 'second-graph'
    const { container, rerender } = render(<svg><g key="a"><RoutedEdge {...first} /></g><g key="b"><RoutedEdge {...second} /></g></svg>)
    const ids = [...container.querySelectorAll('marker')].map((m) => m.id)
    expect(new Set(ids).size).toBe(2)
    rerender(<svg><g key="b"><RoutedEdge {...second} /></g></svg>)
    const path = container.querySelector('[data-edge-path]')!
    expect(path.getAttribute('marker-end')).toBe('url(#second-graph-arranged-arrow-0)')
    expect(container.querySelector('#second-graph-arranged-arrow-0 path')!.getAttribute('fill')).toBe(RENDERER_CONTRACT.loopReturnColor)
    expect(container.querySelector('#test-graph-arranged-arrow-0')).toBeNull()
  })

  it.each(['', undefined])('rejects missing graph namespace %s', (id) => {
    const p = edgeProps(forward)
    p.data!.graphInstanceId = id as string
    expect(() => renderEdge(p)).toThrow('graphInstanceId is required')
  })

  it('changes the complete path, label and marker with changed persisted input', () => {
    const route = { ...forward, points: [{ x: 5, y: 7 }, { x: 5, y: 93 }], text: {
      text: '重試\n' + '界'.repeat(60), box: { x: 9, y: 20, w: 160, h: 85 }, lines: 4,
    } }
    const p = edgeProps(route)
    p.data!.contract = { ...RENDERER_CONTRACT, arrowPx: 12, strokePx: 3 }
    const { container } = renderEdge(p)
    expect(container.querySelector('[data-edge-path]')!.getAttribute('d')).toBe('M 5,7 L 5,93')
    expect(container.querySelector('marker')!.getAttribute('markerWidth')).toBe('12')
    const label = document.body.querySelector('[data-edge-label]') as HTMLElement
    expect(label.textContent).toBe(route.text.text)
    expect(label.style.transform).toBe('translate(9px, 20px)')
    expect(label.style.height).toBe('85px')
  })
})

describe('RoutedEdge — defensive', () => {
  it('renders nothing when edge data is missing', () => {
    const p = { ...edgeProps(forward), data: undefined } as unknown as Props
    const { container } = renderEdge(p)
    expect(container.querySelector('path')).toBeNull()
  })
})
