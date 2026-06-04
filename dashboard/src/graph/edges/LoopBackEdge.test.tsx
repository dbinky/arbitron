import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'

// React Flow's BaseEdge renders an SVG <path>; in jsdom we render it directly,
// but the component must be wrapped in an <svg> for the path to be valid DOM.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    // BaseEdge in isolation needs no provider; passthrough to a plain <path>.
    BaseEdge: ({ path, style }: { path: string; style?: object }) => (
      <path data-testid="edge-path" d={path} style={style} />
    ),
    EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  }
})

import LoopBackEdge, { chooseSide, GUTTER_OFFSET } from './LoopBackEdge'

const baseProps = {
  id: 'e-c-b-3',
  source: 'c',
  target: 'b',
  sourceX: 200,
  sourceY: 400,
  targetX: 200,
  targetY: 100,
  sourcePosition: 'bottom',
  targetPosition: 'top',
  data: {
    loop: {
      id: 'loop-b-c',
      headId: 'b',
      tailId: 'c',
      bodyNodeIds: ['b', 'c'],
      backEdge: { source: 'c', target: 'b', label: 'tests pass' },
      depth: 0,
    },
    box: { x: 100, y: 80, width: 200, height: 360 },
  },
} as unknown as Parameters<typeof LoopBackEdge>[0]

describe('chooseSide', () => {
  it('routes right when endpoints are left of the box center', () => {
    const r = chooseSide({ sourceX: 120, targetX: 120, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
    expect(r.gutterX).toBe(100 + 200 + GUTTER_OFFSET)
  })

  it('routes left when endpoints are right of the box center', () => {
    const r = chooseSide({ sourceX: 280, targetX: 280, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('left')
    expect(r.gutterX).toBe(100 - GUTTER_OFFSET)
  })

  it('defaults to right on a centered tie', () => {
    const r = chooseSide({ sourceX: 200, targetX: 200, boxX: 100, boxWidth: 200 })
    expect(r.side).toBe('right')
  })

  it('averages asymmetric sourceX/targetX to determine side (diamond loops)', () => {
    // source (tail) at x=50, target (head) at x=350 → midpoint=200 = box center → right (tie rule)
    const tie = chooseSide({ sourceX: 50, targetX: 350, boxX: 100, boxWidth: 200 })
    expect(tie.side).toBe('right')

    // source at x=100, target at x=400 → midpoint=250 > box center=200 → left
    const left = chooseSide({ sourceX: 100, targetX: 400, boxX: 100, boxWidth: 200 })
    expect(left.side).toBe('left')
    expect(left.gutterX).toBe(100 - GUTTER_OFFSET)

    // source at x=0, target at x=200 → midpoint=100 < box center=200 → right
    const right = chooseSide({ sourceX: 0, targetX: 200, boxX: 100, boxWidth: 200 })
    expect(right.side).toBe('right')
    expect(right.gutterX).toBe(100 + 200 + GUTTER_OFFSET)
  })
})

describe('LoopBackEdge — fallback: endpoint-derived box when box is absent', () => {
  it('renders a path without crashing when data is entirely absent', () => {
    const noData = { ...baseProps, data: undefined } as unknown as Parameters<typeof LoopBackEdge>[0]
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...noData} />
      </svg>,
    )
    expect(getByTestId('edge-path')).toBeInTheDocument()
    expect(getByTestId('edge-path').getAttribute('d')!.length).toBeGreaterThan(0)
  })

  it('renders a path when data has a loop but box is undefined (e.g. unpositioned body nodes)', () => {
    // loopContainerBox returns null when no body node has a position; useGraphLayout
    // then sets box: undefined on the edge data. The component must use the fallback.
    const noBox = {
      ...baseProps,
      data: {
        ...(baseProps as unknown as { data: object }).data,
        box: undefined,
      },
    } as unknown as Parameters<typeof LoopBackEdge>[0]
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...noBox} />
      </svg>,
    )
    expect(getByTestId('edge-path')).toBeInTheDocument()
    expect(getByTestId('edge-path').getAttribute('d')!.length).toBeGreaterThan(0)
  })
})

describe('LoopBackEdge — rendering', () => {
  it('renders an SVG path (the routed return curve)', () => {
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    const path = getByTestId('edge-path')
    expect(path).toBeInTheDocument()
    // The path string is non-empty and contains curve commands.
    expect(path.getAttribute('d')!.length).toBeGreaterThan(0)
  })

  it('applies a dashed accent stroke distinct from forward edges', () => {
    const { getByTestId } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    const style = getByTestId('edge-path').getAttribute('style') ?? ''
    expect(style).toMatch(/dash/i) // stroke-dasharray present
    // The accent color #7c8cf5 serializes via jsdom's CSSOM to rgb(124,140,245).
    expect(style.toLowerCase()).toMatch(/#7c8cf5|rgb\(124,\s*140,\s*245\)/)
  })

  it('renders the loop label when the back-edge has one', () => {
    const { getByText } = render(
      <svg>
        <LoopBackEdge {...baseProps} />
      </svg>,
    )
    expect(getByText('tests pass')).toBeInTheDocument()
  })

  it('renders without a label when the back-edge has none', () => {
    const noLabel = {
      ...baseProps,
      data: {
        ...(baseProps as unknown as { data: { loop: { backEdge: object } } }).data,
        loop: {
          ...(baseProps as unknown as { data: { loop: object } }).data.loop,
          backEdge: { source: 'c', target: 'b' },
        },
      },
    } as typeof baseProps
    const { queryByText } = render(
      <svg>
        <LoopBackEdge {...noLabel} />
      </svg>,
    )
    expect(queryByText('tests pass')).not.toBeInTheDocument()
  })
})
