import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

const captured: { nodes?: Array<{ type?: string }>; edges?: Array<{ type?: string }> } = {}

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ nodes, edges }: { nodes: Array<{ type?: string }>; edges: Array<{ type?: string }> }) => {
    captured.nodes = nodes
    captured.edges = edges
    return <div data-testid="rf" />
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: () => {},
    getZoom: () => 1,
  }),
}))

import { GraphView } from './ExecutionView'

const loopWorkflow: WorkflowDefinition = {
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
    { source: 'c', target: 'b', label: 'tests pass' },
  ],
}

const unlabeledLoopWorkflow: WorkflowDefinition = {
  ...loopWorkflow,
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'b' },  // no label — container should fall back to "Loop"
  ],
}

const loopless: WorkflowDefinition = {
  ...loopWorkflow,
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
  ],
}

const renderView = (def: WorkflowDefinition) =>
  render(
    <GraphView
      instance={null as unknown as Instance}
      workflowDef={def}
      trace={[] as NodeExecution[]}
      onNodeClick={() => {}}
      selectedNodeId={null}
    />,
  )

describe('GraphView — loop rendering integration', () => {
  it('renders a loopContainer node and a loopBackEdge for a looping workflow', () => {
    renderView(loopWorkflow)
    expect(captured.nodes!.some((n) => n.type === 'loopContainer')).toBe(true)
    expect(captured.edges!.some((e) => e.type === 'loopBackEdge')).toBe(true)
  })

  it('renders neither for a loopless workflow', () => {
    renderView(loopless)
    expect(captured.nodes!.some((n) => n.type === 'loopContainer')).toBe(false)
    expect(captured.edges!.every((e) => e.type === 'smoothstep')).toBe(true)
  })

  // Domain end-to-end: the back-edge label in the WorkflowDefinition must flow
  // through detection → loopContainerLabel → useGraphLayout → ReactFlow node data.
  // Verifying the label content here pins the full propagation chain.
  it('container label reflects the back-edge label from the workflow definition', () => {
    renderView(loopWorkflow)
    const container = captured.nodes!.find((n) => n.type === 'loopContainer') as
      | { data?: { label?: string } }
      | undefined
    expect(container?.data?.label).toBe('Loop until tests pass')
  })

  it('container falls back to "Loop" when the back-edge carries no label', () => {
    renderView(unlabeledLoopWorkflow)
    const container = captured.nodes!.find((n) => n.type === 'loopContainer') as
      | { data?: { label?: string } }
      | undefined
    expect(container?.data?.label).toBe('Loop')
  })
})
