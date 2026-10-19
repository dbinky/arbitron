import { render, renderHook, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { ComponentType, ReactNode } from 'react'
import type { Instance, NodeExecution, NodeType } from '../api/types'
import type { WorkflowDefinition } from './types'

type CapturedNode = { id: string; type?: string; data: Record<string, unknown> }

const captured: { nodes?: CapturedNode[]; nodeTypes?: Record<string, unknown> } = {}

// Render every node GraphView hands React Flow through the component React Flow
// would choose for its `type`, so an unregistered type or a throwing node
// component fails the test (the real ReactFlow needs a measured DOM).
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    ReactFlow: (props: {
      nodes: CapturedNode[]
      nodeTypes: Record<string, ComponentType<{ id: string; data: unknown; selected: boolean }>>
    }) => {
      captured.nodes = props.nodes
      captured.nodeTypes = props.nodeTypes
      return (
        <div data-testid="rf">
          {props.nodes.map((n) => {
            const C = props.nodeTypes[n.type ?? '']
            return C ? (
              <C key={n.id} id={n.id} data={n.data} selected={false} />
            ) : (
              <div key={n.id} data-testid="unregistered-node" />
            )
          })}
        </div>
      )
    },
    ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
    Background: () => null,
    Controls: () => null,
    useNodes: () => [],
    useReactFlow: () => ({ flowToScreenPosition: (p: unknown) => p, setCenter: () => {}, getZoom: () => 1 }),
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import { GraphView } from '../routes/ExecutionView'
import { useGraphLayout } from './useGraphLayout'
import ArbitronNode from './nodes/ArbitronNode'

// The shape the backend stores in instance.workflowSnapshot for a nested,
// two-form group workflow (P1 contract: config.group, task/success/failure ports).
const groupSnapshot: WorkflowDefinition = {
  name: 'parallel-review',
  description: '',
  author: '',
  nodes: [
    {
      id: 'fanout', type: 'parallel_group', label: 'Lint and audit', description: '', entrypoint: true,
      config: {
        group: {
          form: 'branches',
          tasks: [{ key: 'lint', entry: 'lint-run' }, { key: 'audit', entry: 'audit-fanout' }],
          completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results',
        },
      },
    },
    { id: 'lint-run', type: 'tool_call', label: 'Lint', description: '', config: { command: 'make' } },
    {
      id: 'audit-fanout', type: 'parallel_group', label: '', description: '',
      config: {
        group: {
          form: 'workflows',
          tasks: [{ key: 'security', workflow: 'pr-audit', inputs: { focus: 'security' } }],
          completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results',
        },
      },
    },
    { id: 'audit-ok', type: 'transform', label: 'Audit ok', description: '', config: {} },
    { id: 'report', type: 'transform', label: 'Report', description: '', config: {} },
    { id: 'report-failure', type: 'transform', label: 'Report failure', description: '', config: {} },
  ],
  edges: [
    { source: 'fanout', target: 'lint-run', output: 'task', label: 'lint' },
    { source: 'fanout', target: 'audit-fanout', output: 'task', label: 'audit' },
    { source: 'fanout', target: 'report', output: 'success' },
    { source: 'fanout', target: 'report-failure', output: 'failure' },
    { source: 'audit-fanout', target: 'audit-ok', output: 'success' },
  ],
}

describe('parallel_group tolerance (Phase 1)', () => {
  it('NodeType includes parallel_group', () => {
    const t: NodeType = 'parallel_group'
    expect(t).toBe('parallel_group')
  })

  it('renders an instance snapshot containing parallel_group nodes without throwing', () => {
    const inst = {
      id: 'i1', state: 'running', currentNodeId: 'fanout', workflowSnapshot: groupSnapshot,
    } as unknown as Instance
    expect(() =>
      render(
        <GraphView
          instance={inst}
          workflowDef={groupSnapshot}
          trace={[] as NodeExecution[]}
          onNodeClick={() => {}}
          selectedNodeId={null}
        />,
      ),
    ).not.toThrow()

    const groups = captured.nodes!.filter((n) => n.data?.nodeType === 'parallel_group')
    expect(groups.map((n) => n.id).sort()).toEqual(['audit-fanout', 'fanout'])
    for (const n of groups) expect(n.type).toBe('arbitronNode')
    for (const n of captured.nodes!) expect(Object.keys(captured.nodeTypes!)).toContain(n.type)
    expect(screen.queryAllByTestId('unregistered-node')).toHaveLength(0)
    expect(screen.getAllByText('parallel group')).toHaveLength(2)
    expect(screen.getByText('Lint and audit')).toBeInTheDocument()
    expect(screen.getByText('audit-fanout')).toBeInTheDocument() // empty label falls back to the id
  })

  it('lays out task, success and failure edges with their labels', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes: groupSnapshot.nodes, edges: groupSnapshot.edges }))
    const labels = result.current.rfEdges.map((e) => e.label)
    expect(labels).toEqual(['lint', 'audit', 'success', 'failure', 'success'])
    expect(result.current.rfNodes.some((n) => n.type === 'loopContainer')).toBe(false)
    expect(result.current.rfNodes.filter((n) => n.type === 'arbitronNode')).toHaveLength(6)
  })

  it('gives parallel_group its own accent instead of the unknown-type grey', () => {
    const props = {
      data: { label: 'Fan out', nodeType: 'parallel_group', executionState: 'running' },
      selected: false,
    } as unknown as Parameters<typeof ArbitronNode>[0]
    const { container } = render(<ArbitronNode {...props} />)
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    const label = container.querySelector('[class*="typeLabel"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(52, 152, 219)')
    expect(label.textContent).toBe('parallel group')
  })
})
