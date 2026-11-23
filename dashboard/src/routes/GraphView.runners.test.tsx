import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import type { Node } from '@xyflow/react'
import type { GraphState, Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'
import RunnerStripe from '../graph/nodes/RunnerStripe'

const mocks = vi.hoisted(() => ({ get: vi.fn(), getTrace: vi.fn(), getLayout: vi.fn(async () => ({ savedRevision: '', available: false, displayStatus: 'pending' })) }))
vi.mock('../api/client', () => ({ api: { instances: mocks } }))
vi.mock('../hooks/useInstanceWebSocket', () => ({ useInstanceWebSocket: () => ({ connected: false }) }))
vi.mock('@xyflow/react', async () => ({
  ...await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react'),
  ReactFlow: ({ nodes }: { nodes: Node[] }) => <div>{nodes.map(n => <div key={n.id} data-testid={n.id}><RunnerStripe mark={n.data.runnerMark as Parameters<typeof RunnerStripe>[0]['mark']} /></div>)}</div>,
  Background: () => null, Controls: () => null,
  useReactFlow: () => ({ setCenter: vi.fn(), fitBounds: vi.fn(), getZoom: () => 1 }),
}))
import { GraphView } from './ExecutionView'
const def: WorkflowDefinition = { name: 'wf', author: '', description: '', nodes: [{ id: 'a', type: 'llm_call', label: 'A', description: '', config: {} }], edges: [] }
const root = { id: 'root', state: 'running' } as Instance
const execution = (owner: string, runnerId: string): NodeExecution => ({ id: owner, instanceId: owner, nodeId: 'a', attempt: 1, status: 'succeeded', startedAt: '', runnerId })
const trace = [execution('root', 'builtin')]
const graph: GraphState = { rootInstanceId: 'root', version: '1', active: true, groups: [], contexts: [
  { instanceId: 'root', kind: 'root', state: 'running', blocking: true, redacted: false, nodes: [] },
  { instanceId: 'child', parentInstanceId: 'root', kind: 'workflow', state: 'running', blocking: true, redacted: false, nodes: [], graph: def as unknown as Record<string, unknown>, layout: { order: ['g'], groups: {}, owner: {} } },
] }
it('feeds root and same-named child stripes through the actual graph and clears unauthorized colours', async () => {
  mocks.get.mockImplementation(async (id: string) => ({ id, workflowSnapshot: def }))
  mocks.getTrace.mockImplementation(async (id: string) => [execution(id, 'remote')])
  const view = (state: GraphState) => <MemoryRouter><GraphView instance={root} workflowDef={def} trace={trace} selectedNodeId={null} graphState={state} /></MemoryRouter>
  const { container, rerender } = render(view(graph))
  await waitFor(() => expect(container.querySelector('[data-runner="builtin"]')).not.toBeNull())
  await waitFor(() => expect(container.querySelector('[data-runner="remote"]')).not.toBeNull())
  expect(mocks.getTrace).toHaveBeenCalledWith('child')
  mocks.getTrace.mockRejectedValue(new Error('API 403: forbidden'))
  rerender(view({ ...graph, version: '2' }))
  await waitFor(() => expect(screen.getByText(/Runner placement unavailable for child/)).toHaveTextContent('403'))
  expect(container.querySelector('[data-runner="remote"]')).toBeNull()
  expect(container.querySelector('[data-runner="builtin"]')).not.toBeNull()
})
