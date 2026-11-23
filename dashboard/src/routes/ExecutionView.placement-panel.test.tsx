import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { GraphState, Instance, NodeExecution } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

vi.mock('@xyflow/react', async () => ({
  ...(await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')),
  ReactFlow: (props: {
    nodes: Array<{ id: string; data: Record<string, unknown> }>
    edges: Array<{ id: string; source: string; target: string; label?: unknown }>
    onNodeClick?: (e: unknown, n: unknown) => void
    children?: React.ReactNode
  }) => (
    <div>
      {props.nodes.map((n) => {
        const mark = n.data.runnerMark as { runnerId: string | null } | undefined
        return <div key={n.id} data-testid={`node-${n.id}`} data-runner={mark ? (mark.runnerId ?? 'server') : ''} onClick={() => props.onNodeClick?.({}, n)} />
      })}
      {props.edges.map((e) => (
        <div key={e.id} data-testid={`edge-${e.source}-${e.target}`}>
          {typeof e.label === 'string' ? e.label : ''}
        </div>
      ))}
      {props.children}
    </div>
  ),
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useStoreApi: () => ({ getState: () => ({ domNode: document.body }) }),
  useReactFlow: () => ({ flowToScreenPosition: (p: unknown) => p, setCenter: vi.fn(), getZoom: () => 1, fitBounds: vi.fn(() => Promise.resolve(true)), fitView: vi.fn() }),
}))

const n = (id: string, type: string) => ({ id, type, label: id.toUpperCase(), description: '', config: {} })
const def: WorkflowDefinition = {
  name: 'review-flow',
  description: '',
  author: '',
  nodes: [n('plan', 'llm_call'), n('gate', 'conditional'), n('review', 'agent_call'), n('later', 'script')],
  edges: [{ source: 'plan', target: 'gate' }, { source: 'gate', target: 'review', label: 'yes' }, { source: 'review', target: 'later' }],
}
// instance.RecoveryNoticeText (P3 T10)
const NOTICE = 'interrupted work may repeat external effects; exactly-once effects are not guaranteed'
const run = {
  id: 'run-1', ownerId: 'u1', workflowName: 'review-flow', state: 'running', priority: 'normal', variables: {}, originalVariables: {},
  currentNodeId: 'review', createdAt: '2026-11-20T09:00:00Z', updatedAt: '2026-11-20T10:00:00Z', workflowSnapshot: def,
  interactiveActive: false, interactiveNodeId: null,
  workspaceMoves: [{ from: 'builtin', to: 'mac-a', reason: 'builtin subscription exhausted', bytes: 4096, generation: 2, at: '2026-11-20T09:30:00Z' }],
} as unknown as Instance
const exec = (nodeId: string, over: Partial<NodeExecution> = {}): NodeExecution => ({ id: nodeId, instanceId: 'run-1', nodeId, attempt: 1, status: 'succeeded', startedAt: '2026-11-20T09:10:00Z', ...over })
const reviewExec = exec('review', {
  attempt: 2,
  startedAt: '2026-11-20T09:31:00Z',
  runnerId: 'mac-a',
  sourceId: 'mac-a/anthropic_subscription/-',
  placementReason: 'workspace home',
  recovery: [{ nodeId: 'review', jobId: 'j', runnerId: 'builtin', reason: 'runner_lost', at: '2026-11-20T09:25:00Z', text: NOTICE }],
  placementHistory: [
    { jobId: 'j', runnerId: 'builtin', sourceId: 'builtin/anthropic_subscription/-', reason: 'only eligible runner', outcome: 'exhausted', generation: 1, at: '2026-11-20T09:20:00Z' },
    { jobId: 'j', runnerId: 'mac-a', sourceId: 'mac-a/anthropic_subscription/-', reason: 'workspace home', outcome: '', generation: 2, at: '2026-11-20T09:31:00Z' },
  ],
})
const aggregate: GraphState = {
  rootInstanceId: 'run-1', version: 'v1', active: true, warnings: [], groups: [],
  contexts: [{ instanceId: 'run-1', kind: 'root', state: 'running', blocking: true, redacted: false, nodes: [{ instanceId: 'run-1', nodeId: 'plan', state: 'completed' }, { instanceId: 'run-1', nodeId: 'review', state: 'running' }] }],
}
const notFound = () => Promise.reject(new Error('API 404: not found'))
const apiMock = vi.hoisted(() => ({
  me: vi.fn(() => Promise.reject(new Error('API 401: unauthenticated'))),
  instances: {
    get: vi.fn(), getTrace: vi.fn(), getVariables: vi.fn(), getNodeTrace: vi.fn(), graphState: vi.fn(),
    getLayout: vi.fn(() => Promise.resolve({ savedRevision: '', displayStatus: 'pending', available: false })),
    retryLayout: vi.fn(), pause: vi.fn(), resume: vi.fn(), cancel: vi.fn(), retry: vi.fn(), rerun: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

class StubSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) {}
  addEventListener() {}
  send() {}
  close() {}
}

import ExecutionView from './ExecutionView'

beforeEach(() => {
  vi.stubGlobal('WebSocket', StubSocket)
  apiMock.instances.get.mockImplementation((id: string) => (id === 'run-1' ? Promise.resolve(run) : notFound()))
  apiMock.instances.getTrace.mockImplementation((id: string) => (id === 'run-1' ? Promise.resolve([exec('plan', { runnerId: 'builtin' }), reviewExec]) : notFound()))
  apiMock.instances.getVariables.mockImplementation(() => Promise.resolve({}))
  apiMock.instances.getNodeTrace.mockImplementation((id: string, nodeId: string) => (id === 'run-1' && nodeId === 'review' ? Promise.resolve(reviewExec) : notFound()))
  apiMock.instances.graphState.mockImplementation((id: string) => (id === 'run-1' ? Promise.resolve(aggregate) : notFound()))
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function renderView() {
  return render(
    <MemoryRouter initialEntries={['/instances/run-1']}>
      <Routes>
        <Route path="/instances/:id" element={<ExecutionView />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('ExecutionView — runner placement', () => {
  it('the node panel shows placement reason, source, attempts, home history and the durable recovery notice', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-review'))
    expect(await screen.findByRole('link', { name: 'mac-a' })).toHaveAttribute('href', '/pool/runners/mac-a')
    const panel = screen.getByTestId('placement-panel')
    expect(panel).toHaveTextContent('workspace home')
    expect(panel).toHaveTextContent('mac-a/anthropic_subscription/-')
    expect(panel).toHaveTextContent('builtin · builtin/anthropic_subscription/- · only eligible runner · subscription exhausted')
    expect(panel).toHaveTextContent('builtin → mac-a · builtin subscription exhausted')
    expect(panel).toHaveTextContent(NOTICE)
    expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('run-1', 'review')
  })

  it('uses a child workflow definition rather than a same-named root node type', async () => {
    apiMock.instances.graphState.mockResolvedValue({ ...aggregate, contexts: [
      ...aggregate.contexts,
      { instanceId: 'child', parentInstanceId: 'run-1', kind: 'workflow', state: 'running', blocking: true, redacted: false,
        graph: { ...def, nodes: [n('review', 'conditional'), n('g', 'parallel_group')], edges: [] },
        layout: { groups: { g: { nodeId: 'g', config: { form: 'workflows', tasks: [], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' }, bodies: {}, nested: [], successTarget: '', failureTarget: '' } }, owner: {}, order: ['g'] },
        nodes: [{ instanceId: 'child', nodeId: 'review', state: 'running' }] },
    ] })
    renderView()
    fireEvent.click(await screen.findByTestId(id => id.startsWith('node-') && id.endsWith(btoa(JSON.stringify(['tile', 'child', 'review'])).replace(/=+$/, ''))))
    await waitFor(() => expect(screen.getByTestId('placement-panel')).toHaveTextContent('Runs on the server'))
    expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('child', 'review')
    expect(screen.getByTestId('placement-panel')).not.toHaveTextContent('builtin subscription exhausted')
  })

  it('a server-side node says it runs on the server', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-gate'))
    await waitFor(() => expect(screen.getByTestId('placement-panel')).toHaveTextContent('Runs on the server'))
  })
})
