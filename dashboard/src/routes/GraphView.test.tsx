import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

// ---------------------------------------------------------------------------
// Mock @xyflow/react: ReactFlow records the `nodes` prop reference it receives
// on each render so we can assert referential stability across re-renders.
// ---------------------------------------------------------------------------

const capturedNodes: unknown[][] = []

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ nodes }: { nodes: unknown[] }) => {
    capturedNodes.push(nodes)
    return null
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  // Used by useGraphLayout when building edges.
  MarkerType: { ArrowClosed: 'arrowclosed' },
  // Referenced (not called) by CalloutConnector, imported transitively.
  useNodes: () => [],
  useReactFlow: () => ({ flowToScreenPosition: (p: unknown) => p }),
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

const trace: NodeExecution[] = []

beforeEach(() => {
  capturedNodes.length = 0
})

// ---------------------------------------------------------------------------
// Flicker guard: an unrelated parent re-render must NOT churn node identities.
// ReactFlow re-renders every node when it receives a new `nodes` array, which
// is the visible per-second flicker we're fixing.
// ---------------------------------------------------------------------------

describe('GraphView — referential stability', () => {
  it('passes the same nodes array reference across an unrelated re-render', async () => {
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
    expect(capturedNodes.length).toBe(1)

    // Force a parent re-render that changes nothing GraphView depends on.
    await userEvent.click(screen.getByRole('button'))
    expect(capturedNodes.length).toBe(2)

    // Same reference => ReactFlow sees no change => no flicker.
    expect(capturedNodes[1]).toBe(capturedNodes[0])
  })

  it('produces a new nodes array when the selected node changes', async () => {
    function Harness() {
      const [selected, setSelected] = useState<string | null>(null)
      return (
        <>
          <button onClick={() => setSelected('a')}>select</button>
          <GraphView
            instance={instance}
            workflowDef={workflowDef}
            trace={trace}
            onNodeClick={() => {}}
            selectedNodeId={selected}
          />
        </>
      )
    }

    render(<Harness />)
    await userEvent.click(screen.getByRole('button'))

    // Selection genuinely changed, so a fresh array is expected.
    expect(capturedNodes[1]).not.toBe(capturedNodes[0])
  })
})
