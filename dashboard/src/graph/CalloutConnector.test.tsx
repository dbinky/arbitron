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
