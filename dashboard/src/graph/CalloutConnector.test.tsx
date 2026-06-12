import { render } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { Node } from '@xyflow/react'

// ---------------------------------------------------------------------------
// Mock @xyflow/react before importing the component
// ---------------------------------------------------------------------------

const mockNodes: Node[] = []
const mockFlowToScreen = vi.fn()

vi.mock('@xyflow/react', () => ({
  useNodes: () => mockNodes,
  useReactFlow: () => ({ flowToScreenPosition: mockFlowToScreen }),
}))

// Import after mock is registered
import CalloutConnector from './CalloutConnector'

function makeNode(id: string, x: number, y: number, width = 200, height = 80): Node {
  return {
    id,
    type: 'interactive',
    position: { x, y },
    data: {},
    measured: { width, height },
  } as Node
}

beforeEach(() => {
  vi.clearAllMocks()
  mockNodes.length = 0
  // Default: screen position maps flow coords to screen coords 1:1
  mockFlowToScreen.mockImplementation(({ x, y }: { x: number; y: number }) => ({ x, y }))
})

// ---------------------------------------------------------------------------
// Happy path: renders SVG when visible and node found
// ---------------------------------------------------------------------------

describe('CalloutConnector — happy path', () => {
  it('renders SVG connector when visible=true and node exists', () => {
    mockNodes.push(makeNode('interactive-1', 100, 100))
    // Source right-center: x=100+200=300, y=100+40=140
    // Target: no divider in jsdom, falls back to window.innerWidth - 380
    // window.innerWidth = 0 in jsdom, so targetX = -380, which is < sourceX - 20
    // This means the guard kicks in and nothing is rendered... Let's mock window.innerWidth

    Object.defineProperty(window, 'innerWidth', { value: 1200, writable: true })

    const { container } = render(
      <CalloutConnector nodeId="interactive-1" visible />,
    )
    // targetX = 1200 - 380 = 820, sourceX = 300 → 300 < 820 - 20 = 800, renders
    expect(container.querySelector('svg')).toBeInTheDocument()
    expect(container.querySelector('line')).toBeInTheDocument()
    expect(container.querySelector('circle')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Anchor: source circle sits on the node's RENDERED right edge, not a width
// fallback. Regression for the orange dot landing in the middle of a node whose
// real width exceeds the 200px fallback (measured width unavailable at compute).
// ---------------------------------------------------------------------------

describe('CalloutConnector — anchors to rendered node right edge', () => {
  it('places the source circle at the node DOM right edge, not position.x + fallback width', () => {
    // measured width is a small/stale 200, but the node actually renders ~320 wide.
    mockNodes.push(makeNode('interactive-1', 100, 100, 200, 80))
    Object.defineProperty(window, 'innerWidth', { value: 1200, writable: true })

    // The real rendered node element: right edge at 420, vertical center at 140.
    const el = document.createElement('div')
    el.className = 'react-flow__node'
    el.setAttribute('data-id', 'interactive-1')
    el.getBoundingClientRect = () =>
      ({
        x: 100, y: 100, left: 100, top: 100, right: 420, bottom: 180,
        width: 320, height: 80, toJSON: () => {},
      }) as DOMRect
    document.body.appendChild(el)

    const { container } = render(
      <CalloutConnector nodeId="interactive-1" visible />,
    )
    const circle = container.querySelector('circle')
    expect(circle).toBeInTheDocument()
    // Right edge = 420. The old width-fallback path would give 100 + 200 = 300.
    expect(circle?.getAttribute('cx')).toBe('420')
    expect(circle?.getAttribute('cy')).toBe('140')

    document.body.removeChild(el)
  })
})

// ---------------------------------------------------------------------------
// Error path: returns null when visible=false
// ---------------------------------------------------------------------------

describe('CalloutConnector — not visible', () => {
  it('renders nothing when visible=false', () => {
    mockNodes.push(makeNode('interactive-1', 100, 100))
    const { container } = render(
      <CalloutConnector nodeId="interactive-1" visible={false} />,
    )
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Edge: returns null when node is not found in the graph
// ---------------------------------------------------------------------------

describe('CalloutConnector — missing node', () => {
  it('renders nothing when the nodeId is not in the nodes list', () => {
    // No nodes added — empty graph
    const { container } = render(
      <CalloutConnector nodeId="nonexistent" visible />,
    )
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Boundary: returns null when source is at or beyond target (node off-screen right)
// ---------------------------------------------------------------------------

describe('CalloutConnector — geometry guard', () => {
  it('renders nothing when sourceX >= targetX - 20 (node hidden beyond panel)', () => {
    Object.defineProperty(window, 'innerWidth', { value: 400, writable: true })
    // targetX = 400 - 380 = 20
    // Node at x=0, width=200 → sourceX = 200, sourceX (200) >= targetX - 20 (0) → guard triggers
    mockNodes.push(makeNode('interactive-1', 0, 0))
    const { container } = render(
      <CalloutConnector nodeId="interactive-1" visible />,
    )
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Integration: dynamic visible transitions (panel takeover and snap-back)
// ---------------------------------------------------------------------------

describe('CalloutConnector — integration (visible transitions)', () => {
  it('removes SVG when visible transitions from true to false (snap-back)', () => {
    // Verifies that when the interactive session ends, the connector disappears.
    mockNodes.push(makeNode('interactive-1', 100, 100))
    Object.defineProperty(window, 'innerWidth', { value: 1200, writable: true })

    const { container, rerender } = render(
      <CalloutConnector nodeId="interactive-1" visible />,
    )
    expect(container.querySelector('svg')).toBeInTheDocument()

    // Session ends → isActive=false → visible=false → connector must disappear.
    rerender(<CalloutConnector nodeId="interactive-1" visible={false} />)
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })

  it('shows SVG when visible transitions from false to true (panel takeover)', () => {
    // Verifies that when an interactive session starts, the connector appears.
    mockNodes.push(makeNode('interactive-1', 100, 100))
    Object.defineProperty(window, 'innerWidth', { value: 1200, writable: true })

    const { container, rerender } = render(
      <CalloutConnector nodeId="interactive-1" visible={false} />,
    )
    expect(container.querySelector('svg')).not.toBeInTheDocument()

    // Interactive session starts → isActive=true → visible=true → connector must appear.
    rerender(<CalloutConnector nodeId="interactive-1" visible />)
    expect(container.querySelector('svg')).toBeInTheDocument()
  })
})
