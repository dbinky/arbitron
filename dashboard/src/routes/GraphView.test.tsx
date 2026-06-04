import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

// ---------------------------------------------------------------------------
// Mock @xyflow/react. SF4 (PR #18, comment 3350533468): the manager retracted
// the prop-identity / useMemo assertion (testing memo identity couples the test
// to the current optimization and breaks on a correct refactor). Instead we
// assert OBSERVABLE behavior: the mock renders each node as a real, keyed DOM
// element so the test can verify a node's DOM element is NOT remounted across an
// unrelated re-render (the actual flicker bug was nodes remounting on every
// WebSocket event). We also spy on the layout input array length as the
// acceptable middle ground.
// ---------------------------------------------------------------------------

const renderCount = { value: 0 }
const setCenterSpy = vi.fn()
let lastReactFlowProps: { nodes: Array<{ id: string; position?: { x: number; y: number } }> } | null = null

vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: Array<{ id: string; position?: { x: number; y: number } }>
    children?: React.ReactNode
    onMoveStart?: (event: unknown) => void
  }) => {
    renderCount.value += 1
    lastReactFlowProps = props
    return (
      <div>
        <button
          data-testid="rf-fire-user-move"
          onClick={() => props.onMoveStart?.({ type: 'pointerdown' })}
        />
        {props.nodes.map((n) => (
          // Keyed by stable node id: React preserves the DOM element across
          // re-renders unless the identity/key changes (a remount).
          <div key={n.id} data-testid={`node-${n.id}`} />
        ))}
        {props.children}
      </div>
    )
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  // Used by useGraphLayout when building edges.
  MarkerType: { ArrowClosed: 'arrowclosed' },
  // Referenced (not called) by CalloutConnector, imported transitively.
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: setCenterSpy,
    getZoom: () => 1,
  }),
}))

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

beforeEach(() => {
  renderCount.value = 0
  setCenterSpy.mockClear()
  lastReactFlowProps = null
})

// ---------------------------------------------------------------------------
// Flicker guard (observable behavior): an unrelated parent re-render — e.g. the
// per-second WebSocket events that drove the original flicker — must NOT remount
// the graph nodes. We assert the node's DOM element keeps its identity.
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
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    const before = screen.getByTestId('node-a')

    // Force a parent re-render that changes nothing GraphView depends on
    // (simulates a live WebSocket event arriving on the page).
    await userEvent.click(screen.getByRole('button', { name: /tick/i }))

    const after = screen.getByTestId('node-a')
    // Same DOM element instance => the node was not remounted => no flicker.
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

describe('GraphView — default-zoom centering on mount', () => {
  it('centers on the active node at DEFAULT_ZOOM=0.9 on mount', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )

    // Active node is 'a' (instance.currentNodeId === 'a', state 'running').
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    const expectedX = aNode.position!.x + 200 / 2 // NODE_WIDTH / 2
    const expectedY = aNode.position!.y + 80 / 2 // NODE_HEIGHT / 2

    expect(setCenterSpy).toHaveBeenCalledTimes(1)
    expect(setCenterSpy).toHaveBeenCalledWith(
      expectedX,
      expectedY,
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
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    expect(setCenterSpy).not.toHaveBeenCalled()
  })

  it('centers on the final node for a completed instance', () => {
    const completedInstance = {
      ...instance,
      state: 'completed',
      currentNodeId: undefined,
    } as unknown as Instance
    const completedTrace: NodeExecution[] = [
      {
        id: 'e-a',
        instanceId: 'i',
        nodeId: 'a',
        attempt: 1,
        status: 'succeeded',
        startedAt: '2026-01-01T00:00:00Z',
        completedAt: '2026-01-01T00:00:01Z',
      } as NodeExecution,
      {
        id: 'e-b',
        instanceId: 'i',
        nodeId: 'b',
        attempt: 1,
        status: 'succeeded',
        startedAt: '2026-01-01T00:00:02Z',
        completedAt: '2026-01-01T00:00:03Z',
      } as NodeExecution,
    ]
    render(
      <GraphView
        instance={completedInstance}
        workflowDef={workflowDef}
        trace={completedTrace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      bNode.position!.x + 100,
      bNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  it('centers on the failed node for a failed instance', () => {
    const failedInstance = {
      ...instance,
      state: 'failed',
      currentNodeId: undefined,
      failedNodeId: 'b',
    } as unknown as Instance
    const failedTrace: NodeExecution[] = [
      {
        id: 'e-a',
        instanceId: 'i',
        nodeId: 'a',
        attempt: 1,
        status: 'succeeded',
        startedAt: '2026-01-01T00:00:00Z',
        completedAt: '2026-01-01T00:00:01Z',
      } as NodeExecution,
      {
        id: 'e-b',
        instanceId: 'i',
        nodeId: 'b',
        attempt: 1,
        status: 'failed',
        startedAt: '2026-01-01T00:00:02Z',
        completedAt: '2026-01-01T00:00:03Z',
      } as NodeExecution,
    ]
    render(
      <GraphView
        instance={failedInstance}
        workflowDef={workflowDef}
        trace={failedTrace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      bNode.position!.x + 100,
      bNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  it('falls back to centering on the source node when currentNodeId is not in the graph', () => {
    // selectAnchorNodeId returns the source node when currentNodeId is missing from
    // workflowDef (e.g. schema mismatch, deleted node mid-run). The integration chain
    // must propagate this fallback all the way to setCenter without crashing.
    const ghostInstance = {
      currentNodeId: 'ghost-not-in-graph',
      state: 'running',
    } as unknown as Instance
    render(
      <GraphView
        instance={ghostInstance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    // 'a' has no incoming edge in workflowDef, so it is the source-node fallback.
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position!.x + 100,
      aNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
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
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position!.x + 100,
      aNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })

  // Domain: design doc says "loop-container overlay nodes are excluded from
  // anchor candidates." The container IS present in rfNodes (the exclusion is not
  // because containers are absent), so we must verify the centering targets the
  // step node specifically. Without this test, a bug routing the anchor to the
  // container would still pass all other tests.
  it('centers on the active step node, not the loop container, for a running workflow with a loop', () => {
    const runningInLoop = {
      currentNodeId: 'b',  // 'b' is inside the loop body
      state: 'running',
    } as unknown as Instance
    render(
      <GraphView
        instance={runningInLoop}
        workflowDef={loopWorkflowDef}
        trace={[]}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const bNode = lastReactFlowProps!.nodes.find((n) => n.id === 'b')!
    // The loop container IS present alongside the step nodes.
    expect(
      lastReactFlowProps!.nodes.some(
        (n) => (n as { type?: string }).type === 'loopContainer',
      ),
    ).toBe(true)
    // Centering must target the step node 'b', not the container.
    expect(setCenterSpy).toHaveBeenCalledWith(
      bNode.position!.x + 100,
      bNode.position!.y + 40,
      expect.objectContaining({ zoom: 0.9 }),
    )
  })
})

describe('GraphView — Recenter / Follow control', () => {
  it('renders the control showing the following state by default', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn).toBeTruthy()
    // Default state is "following" — surfaced via aria-pressed.
    expect(btn.getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to paused state after a user move and back after Recenter', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    // Simulate a user pan/zoom via the mock's forwarded onMoveStart.
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('false')

    // Re-engage via the control.
    fireEvent.click(btn)
    expect(
      screen
        .getByRole('button', { name: /following|recenter|follow/i })
        .getAttribute('aria-pressed'),
    ).toBe('true')
  })

  it('re-centers on the active node at DEFAULT_ZOOM when Recenter is clicked', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    // Pause via a user move, then clear the mount-center call.
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()

    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))

    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position!.x + 100, // NODE_WIDTH / 2
      aNode.position!.y + 40, // NODE_HEIGHT / 2
      { zoom: 0.9 },
    )
  })
})

describe('GraphView — mount centering one-shot guard', () => {
  it('calls setCenter exactly once even after an unrelated parent re-render', async () => {
    // The didInitialCenter ref in GraphView must prevent a second setCenter call
    // when a parent re-renders (e.g. a live WebSocket event arriving on the page).
    function Harness() {
      const [tick, setTick] = useState(0)
      return (
        <>
          <button onClick={() => setTick(tick + 1)}>tick</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            onNodeClick={() => {}}
            selectedNodeId={null}
          />
        </>
      )
    }

    render(<Harness />)
    expect(setCenterSpy).toHaveBeenCalledTimes(1)

    await userEvent.click(screen.getByRole('button', { name: /tick/i }))

    // Must remain at 1 — the ref guard blocks the re-fire.
    expect(setCenterSpy).toHaveBeenCalledTimes(1)
  })
})
