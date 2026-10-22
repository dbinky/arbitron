import { act, render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Node } from '@xyflow/react'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import type { LayoutView } from '../graph/arranged/types'
import { fixtureGeometry, fixtureGeometryWithBoxes, fixtureView, fixtureWorkflow } from '../graph/arranged/testFixtures'

// ---------------------------------------------------------------------------
// Mock only the React Flow canvas (everything else is the real module). The mock
// renders each node as a keyed DOM element so we can assert a node is NOT
// remounted across an unrelated re-render (the original flicker bug), and it
// records the props the shared renderer (ArrangedGraph) hands to <ReactFlow>.
// ---------------------------------------------------------------------------

const renderCount = { value: 0 }
const setCenterSpy = vi.fn()
let lastReactFlowProps: (Record<string, unknown> & { nodes: Node[] }) | null = null

vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    ReactFlow: (
      props: Record<string, unknown> & {
        nodes: Node[]
        children?: React.ReactNode
        onMoveStart?: (event: unknown) => void
      },
    ) => {
      renderCount.value += 1
      lastReactFlowProps = props
      return (
        <div>
          <button
            data-testid="rf-fire-user-move"
            onClick={() => props.onMoveStart?.({ type: 'pointerdown' })}
          />
          {props.nodes.map((n) => (
            <div key={n.id} data-testid={`node-${n.id}`} />
          ))}
          {props.children}
        </div>
      )
    },
    ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
    Background: () => null,
    Controls: () => null,
    useReactFlow: () => ({
      flowToScreenPosition: (p: unknown) => p,
      setCenter: setCenterSpy,
      fitView: () => {},
      getZoom: () => 1,
    }),
  }
})

// Controllable unit font boundary. The real asset/face/coverage producer is exercised
// by P3T5/P3T14. Success requires nonempty actual text and the exact bundled contract;
// tests replace this method with a rejection/deferred promise for font sink cases.
const fontBoundary = vi.hoisted(() => ({ load: vi.fn(async (_doc: Document, texts: readonly string[], contract: {version: string; fontFamily: string}) => {
  if (!texts.length || !texts.every(t => typeof t === 'string') || contract.version !== 'arranged-renderer/2' || contract.fontFamily !== 'Inter, Noto Sans SC, Noto Emoji') throw new Error('invalid font request')
  return { fontsReady: true, fontFamilyResolved: true }
}) }))
vi.mock('../graph/arranged/fonts', () => ({ ensureBundledTextFonts: fontBoundary.load }))

// Import after the mock is registered.
import { GraphView } from './ExecutionView'

// Stable, module-level props so re-renders pass identical references.
const workflowDef: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
  ],
  edges: [{ source: 'a', target: 'b' }],
}

const instance = {
  currentNodeId: 'a',
  state: 'running',
} as unknown as Instance

// Loop workflow: b↔c are in a loop body; a is outside.
const loopWorkflowDef: WorkflowDefinition = {
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
    { source: 'c', target: 'b', label: 'retry' },
  ],
}

const trace: NodeExecution[] = []

// Arranged-mode fixtures (P3T1): a → b → c with loop return c → b, revision rev-1.
const arrangedDef = fixtureWorkflow()
const running = (id: string) => ({ currentNodeId: id, state: 'running' }) as unknown as Instance

/** The fixture geometry with variable node sizes: a tall b and a wide, tall c. */
function tallView(): LayoutView {
  const geo = fixtureGeometryWithBoxes({
    b: { x: 40, y: 140, w: 200, h: 200 }, // centre (140, 240)
    c: { x: 40, y: 400, w: 320, h: 160 }, // centre (200, 480)
  })
  return fixtureView({ geometry: geo })
}
const TALL = tallView()

beforeEach(() => {
  renderCount.value = 0
  setCenterSpy.mockClear()
  lastReactFlowProps = null
})

// ---------------------------------------------------------------------------
// Seam: anchor selection → follow hook (Paired Review #19), legacy fallback mode.
// ---------------------------------------------------------------------------

describe('GraphView — follow animation on instance advance', () => {
  it('calls setCenter with the new anchor position when currentNodeId advances while following', async () => {
    function Harness() {
      const [inst, setInst] = useState({ currentNodeId: 'a', state: 'running' } as unknown as Instance)
      return (
        <>
          <button onClick={() => setInst({ currentNodeId: 'b', state: 'running' } as unknown as Instance)}>
            advance
          </button>
          <GraphView
            instance={inst}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    // Mount centering fires once on 'a'.
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    setCenterSpy.mockClear()

    await userEvent.click(screen.getByRole('button', { name: 'advance' }))

    // Fallback nodes carry no explicit size, so nodeCenter uses NODE_WIDTH/NODE_HEIGHT (200 x 80).
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    await waitFor(() => {
      expect(setCenterSpy).toHaveBeenCalledWith(
        bNode.position.x + 100,
        bNode.position.y + 40,
        expect.objectContaining({ duration: expect.any(Number) }),
      )
    })
  })
})

// ---------------------------------------------------------------------------
// Flicker guard (observable behavior): an unrelated parent re-render must NOT
// remount the graph nodes.
// ---------------------------------------------------------------------------

describe('GraphView — no-remount on unrelated re-render', () => {
  it('keeps node DOM element identity across an unrelated re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick {tick}</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    const before = screen.getByTestId('node-a')
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))
    const after = screen.getByTestId('node-a')
    expect(after).toBe(before)
  })

  it('still renders all workflow nodes after the re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick {tick}</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))

    expect(screen.getByTestId('node-a')).toBeTruthy()
    expect(screen.getByTestId('node-b')).toBeTruthy()
  })
})

describe('GraphView — default-zoom centering on mount (fallback mode)', () => {
  it('centers on the active node at DEFAULT_ZOOM=0.9 on mount', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position.x + 200 / 2, // NODE_WIDTH / 2
      aNode.position.y + 80 / 2, // NODE_HEIGHT / 2
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  it('does not call setCenter when there is no anchor (empty graph)', () => {
    const emptyDef = { ...workflowDef, nodes: [], edges: [] }
    render(
      <GraphView
        instance={instance}
        workflowDef={emptyDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).not.toHaveBeenCalled()
  })

  it('centers on the final node for a completed instance', () => {
    const completedInstance = { ...instance, state: 'completed', currentNodeId: undefined } as unknown as Instance
    const completedTrace: NodeExecution[] = [
      { id: 'e-a', instanceId: 'i', nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:02Z', completedAt: '2026-01-01T00:00:03Z' } as NodeExecution,
    ]
    render(
      <GraphView
        instance={completedInstance}
        workflowDef={workflowDef}
        trace={completedTrace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the failed node for a failed instance', () => {
    const failedInstance = { ...instance, state: 'failed', currentNodeId: undefined, failedNodeId: 'b' } as unknown as Instance
    const failedTrace: NodeExecution[] = [
      { id: 'e-a', instanceId: 'i', nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'failed', startedAt: '2026-01-01T00:00:02Z', completedAt: '2026-01-01T00:00:03Z' } as NodeExecution,
    ]
    render(
      <GraphView
        instance={failedInstance}
        workflowDef={workflowDef}
        trace={failedTrace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('falls back to centering on the source node when currentNodeId is not in the graph', () => {
    const ghostInstance = { currentNodeId: 'ghost-not-in-graph', state: 'running' } as unknown as Instance
    render(
      <GraphView
        instance={ghostInstance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the entry node for a static workflow preview (no instance)', () => {
    const previewDef: typeof workflowDef = {
      ...workflowDef,
      nodes: [
        { id: 'a', type: 'llm_call', label: 'A', description: '', config: {}, entrypoint: true },
        { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
      ],
    }
    render(
      <GraphView
        instance={null}
        workflowDef={previewDef}
        trace={[]}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })

  it('centers on the active step node, not the loop container, for a running workflow with a loop', () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={loopWorkflowDef}
        trace={[]}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(lastReactFlowProps!.nodes.some((n) => n.type === 'loopContainer')).toBe(true)
    expect(setCenterSpy).toHaveBeenCalledWith(bNode.position.x + 100, bNode.position.y + 40, expect.objectContaining({ zoom: 0.9 }))
  })
})

describe('GraphView — Recenter / Follow control', () => {
  it('renders the control showing the following state by default', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to paused state after a user move and back after Recenter', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(btn)
    expect(screen.getByRole('button', { name: /following|recenter|follow/i }).getAttribute('aria-pressed')).toBe('true')
  })

  it('re-centers on the active node at DEFAULT_ZOOM when Recenter is clicked', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()
    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(aNode.position.x + 100, aNode.position.y + 40, { zoom: 0.9 })
  })
})

describe('GraphView — mount centering one-shot guard', () => {
  it('calls setCenter exactly once even after an unrelated parent re-render', async () => {
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            layoutView={null}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
  })
})

// ---------------------------------------------------------------------------
// Arranged mode: the instance layout view drives positions, sizes, centring
// and follow (payload dimensions — never NODE_WIDTH/NODE_HEIGHT guesses).
// ---------------------------------------------------------------------------

describe('GraphView — font readiness sinks', () => {
  it('keeps deferred fonts out of the arranged sink and ignores their stale completion', async () => {
    let release!: () => void
    fontBoundary.load.mockImplementationOnce(() => new Promise(resolve => {
      release = () => resolve({ fontsReady: true, fontFamilyResolved: true })
    }))
    const props = { instance: running('b'), workflowDef: arrangedDef, trace, onNodeClick: () => {}, selectedNodeId: null }
    const { rerender } = render(<GraphView {...props} layoutView={TALL} />)
    expect(document.querySelector('[data-layout-mode="arranged"]')).toBeNull()
    expect(setCenterSpy).not.toHaveBeenCalled()
    rerender(<GraphView {...props} layoutView={null} />)
    expect(document.querySelector('[data-layout-mode="fallback"]')).not.toBeNull()
    release()
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).toBeNull())
  })

  it('reports rejected fonts and draws only fallback nodes', async () => {
    fontBoundary.load.mockRejectedValueOnce(new Error('missing reviewed font'))
    const issues = vi.fn()
    render(<GraphView instance={running('b')} workflowDef={arrangedDef} trace={trace}
      layoutView={TALL} onNodeClick={() => {}} selectedNodeId={null} onDisplayIssues={issues} />)
    await waitFor(() => expect(issues).toHaveBeenCalledWith(['Error: missing reviewed font']))
    expect(document.querySelector('[data-layout-mode="fallback"]')).not.toBeNull()
    expect(lastReactFlowProps!.nodes.every(n => n.type !== 'arrangedNode')).toBe(true)
    expect(fontBoundary.load).toHaveBeenCalledWith(document,
      expect.arrayContaining(['Start', 'again']), expect.objectContaining({ version: 'arranged-renderer/2' }))
  })
})

describe('GraphView — terminal payload Recenter', () => {
  it('recentres the completed tall node and remembers late box changes without automatic follow', async () => {
    const doneTrace = [{ id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'succeeded',
      startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution]
    const props = { instance: { state: 'completed' } as Instance, workflowDef: arrangedDef,
      trace: doneTrace, onNodeClick: () => {}, selectedNodeId: null }
    const { rerender } = render(<GraphView {...props} layoutView={TALL} />)
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    fireEvent.click(screen.getByTestId('rf-fire-user-move')); setCenterSpy.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Recenter' }))
    expect(setCenterSpy).toHaveBeenCalledWith(140, 240, { zoom: 0.9 })
    setCenterSpy.mockClear()
    const late = fixtureView({ geometry: fixtureGeometryWithBoxes({ b: { x: 80, y: 200, w: 320, h: 200 } }) })
    rerender(<GraphView {...props} layoutView={late} />)
    await waitFor(() => {
      expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull()
      expect(lastReactFlowProps!.nodes.find(n => n.id === 'b')?.position).toEqual({ x: 80, y: 200 })
    })
    expect(setCenterSpy).not.toHaveBeenCalled()
    await act(async () => { await Promise.resolve() })
    fireEvent.click(screen.getByRole('button', { name: 'Following' }))
    expect(setCenterSpy).toHaveBeenCalledWith(240, 300, { zoom: 0.9 })
  })
})

describe('GraphView — arranged instance layout (shared renderer)', () => {
  it('hands React Flow the payload boxes of the instance layout, not dagre, and never drags or auto-fits', async () => {
    render(
      <GraphView
        instance={running('a')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView()}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    const nodes = lastReactFlowProps!.nodes
    expect(nodes.map((n) => n.type)).toEqual(['arrangedNode', 'arrangedNode', 'arrangedNode'])
    const b = nodes.find((n) => n.id === 'b')!
    expect(b.position).toEqual({ x: 40, y: 140 })
    expect([b.width, b.height]).toEqual([200, 70])
    expect(lastReactFlowProps!.nodesDraggable).toBe(false)
    expect(lastReactFlowProps!.fitView).toBeUndefined()
  })

  it('centres a running tall node on the centre of its payload box (not position + NODE_HEIGHT/2)', async () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={TALL}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    expect(setCenterSpy).toHaveBeenCalledWith(140, 240, { zoom: 0.9 })
    expect(setCenterSpy).not.toHaveBeenCalledWith(140, 180, expect.anything())
  })

  it('follows to the payload-box centre of the next active node', async () => {
    function Harness() {
      const [inst, setInst] = useState(running('a'))
      return (
        <>
          <button onClick={() => setInst(running('c'))}>advance</button>
          <GraphView
            instance={inst}
            workflowDef={arrangedDef}
            trace={trace}
            layoutView={TALL}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }
    render(<Harness />)
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    // Mount centre on a: box {40,40,180,52} → (130, 66).
    expect(setCenterSpy).toHaveBeenCalledWith(130, 66, { zoom: 0.9 })
    setCenterSpy.mockClear()
    await userEvent.click(screen.getByRole('button', { name: 'advance' }))
    // c: box {40,400,320,160} → (200, 480); NODE_WIDTH/NODE_HEIGHT would give (140, 440).
    await waitFor(() => {
      expect(setCenterSpy).toHaveBeenCalledWith(200, 480, expect.objectContaining({ duration: expect.any(Number) }))
    })
  })

  it('Recenter returns to the tall node payload centre at DEFAULT_ZOOM', async () => {
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={TALL}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()
    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))
    expect(setCenterSpy).toHaveBeenCalledWith(140, 240, { zoom: 0.9 })
  })

  it('running-state styling does not change the node box handed to the renderer', async () => {
    const view = fixtureView()
    const { rerender } = render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    const runningB = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect((runningB.data as { executionState: string }).executionState).toBe('running')
    expect([runningB.width, runningB.height]).toEqual([200, 70])

    const done = { state: 'completed' } as unknown as Instance
    const doneTrace = [
      { id: 'e-b', instanceId: 'i', nodeId: 'b', attempt: 1, status: 'succeeded', startedAt: '2026-01-01T00:00:00Z', completedAt: '2026-01-01T00:00:01Z' } as NodeExecution,
    ]
    rerender(
      <GraphView
        instance={done}
        workflowDef={arrangedDef}
        trace={doneTrace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const completedB = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect((completedB.data as { executionState: string }).executionState).toBe('completed')
    expect([completedB.width, completedB.height]).toEqual([200, 70])
    expect(completedB.position).toEqual(runningB.position)
  })

  it("falls back to the snapshot revision's retained positions while the layout has no geometry", () => {
    const view = fixtureView({
      state: 'in_progress',
      displayStatus: 'arranging',
      geometry: undefined,
      provenance: 'retained_positions',
      retained: { source: 'layout', positions: { a: { x: 11, y: 22 }, b: { x: 11, y: 222 }, c: { x: 11, y: 422 } } },
    })
    render(
      <GraphView
        instance={running('b')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={view}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const b = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(b.type).toBe('arbitronNode')
    expect(b.position).toEqual({ x: 11, y: 222 })
    expect(setCenterSpy).toHaveBeenCalledWith(111, 262, { zoom: 0.9 })
  })

  it('re-centres once on the payload box when the arranged result replaces the fallback', async () => {
    const completed = { state: 'completed' } as unknown as Instance
    const { rerender } = render(
      <GraphView
        instance={completed}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={null}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    rerender(
      <GraphView
        instance={completed}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView()}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    await waitFor(() => expect(document.querySelector('[data-layout-mode="arranged"]')).not.toBeNull())
    // Completed run with no trace and no sink (c → b loops) anchors on the source node a: (130, 66).
    expect(setCenterSpy).toHaveBeenCalledTimes(2)
    expect(setCenterSpy).toHaveBeenLastCalledWith(130, 66, { zoom: 0.9 })
  })

  it('does not draw a geometry whose renderer contract version this build cannot draw', () => {
    const geo = fixtureGeometry()
    geo.contract = { ...geo.contract, version: 'arranged-renderer/1' }
    render(
      <GraphView
        instance={running('a')}
        workflowDef={arrangedDef}
        trace={trace}
        layoutView={fixtureView({ geometry: geo })}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(lastReactFlowProps!.nodes.some((n) => n.type === 'arrangedNode')).toBe(false)
  })
})
