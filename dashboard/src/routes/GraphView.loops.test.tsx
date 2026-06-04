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

// Two back-edges producing nested loops: inner (d→c, depth=1) inside outer (e→b, depth=0).
const nestedLoopWorkflow: WorkflowDefinition = {
  name: 'wf',
  description: '',
  author: '',
  nodes: [
    { id: 'a', type: 'llm_call', label: 'A', description: '', config: {} },
    { id: 'b', type: 'llm_call', label: 'B', description: '', config: {} },
    { id: 'c', type: 'llm_call', label: 'C', description: '', config: {} },
    { id: 'd', type: 'llm_call', label: 'D', description: '', config: {} },
    { id: 'e', type: 'llm_call', label: 'E', description: '', config: {} },
  ],
  edges: [
    { source: 'a', target: 'b' },
    { source: 'b', target: 'c' },
    { source: 'c', target: 'd' },
    { source: 'd', target: 'e' },
    { source: 'e', target: 'b', label: 'outer done' },
    { source: 'd', target: 'c', label: 'inner done' },
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

  // Domain end-to-end nesting: a workflow with two nested back-edges must produce two
  // containers where the inner carries depth=1 and the outer depth=0. The depth value
  // drives both the visual inset and z-index ordering for the "nested containers" spec.
  it('renders two containers for a workflow with nested loops', () => {
    renderView(nestedLoopWorkflow)
    const containers = captured.nodes!.filter((n) => n.type === 'loopContainer')
    expect(containers).toHaveLength(2)
  })

  it('inner container has depth=1 and outer has depth=0 for nested loops', () => {
    renderView(nestedLoopWorkflow)
    type ContainerNode = { data?: { label?: string; depth?: number } }
    const containers = captured.nodes!.filter((n) => n.type === 'loopContainer') as ContainerNode[]
    const inner = containers.find((c) => c.data?.label === 'Loop until inner done')
    const outer = containers.find((c) => c.data?.label === 'Loop until outer done')
    expect(inner?.data?.depth).toBe(1)
    expect(outer?.data?.depth).toBe(0)
  })
})
