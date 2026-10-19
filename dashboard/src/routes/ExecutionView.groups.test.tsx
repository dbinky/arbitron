import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { ExecutionContext, GraphState, GroupGraph, GroupPanel, GroupPanelTask, Instance, NodeExecution, WebSocketEvent } from '../api/types'
import type { WorkflowDefinition } from '../graph/types'

const fitBoundsSpy = vi.hoisted(() => vi.fn(() => Promise.resolve(true)))

// ---------------------------------------------------------------------------
// React Flow stub: every rendered node becomes a clickable div exposing its
// type and execution state, so the test reads what the graph would highlight.
// ---------------------------------------------------------------------------
vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: Array<{ id: string; type?: string; selected?: boolean; position: { x: number; y: number }; data: Record<string, unknown> }>
    onNodeClick?: (e: unknown, n: unknown) => void
    children?: React.ReactNode
  }) => (
    <div>
      {props.nodes.map((n) => (
        <div
          key={n.id}
          data-testid={`node-${n.id}`}
          data-type={n.type}
          data-x={n.position.x}
          data-y={n.position.y}
          data-state={String(n.data.executionState ?? '')}
          data-selected={n.selected ? 'true' : 'false'}
          onClick={() => props.onNodeClick?.({}, n)}
        />
      ))}
      {props.children}
    </div>
  ),
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: vi.fn(),
    getZoom: () => 1,
    fitBounds: fitBoundsSpy,
  }),
}))

// ---------------------------------------------------------------------------
// Fixture: start -> g (branches; a: a1 -> a2, b: b1) -> done / oops
// ---------------------------------------------------------------------------
const n = (id: string, type = 'llm_call', label = id.toUpperCase()) => ({ id, type, label, description: '', config: {} })
const def: WorkflowDefinition = {
  name: 'fanout', description: '', author: '',
  nodes: [n('start'), n('g', 'parallel_group', 'Fan out'), n('a1'), n('a2'), n('b1'), n('done'), n('oops')],
  edges: [
    { source: 'start', target: 'g' },
    { source: 'g', target: 'a1', output: 'task', label: 'a' },
    { source: 'g', target: 'b1', output: 'task', label: 'b' },
    { source: 'a1', target: 'a2' },
    { source: 'g', target: 'done', output: 'success' },
    { source: 'g', target: 'oops', output: 'failure' },
  ],
}
const layout: GroupGraph = {
  groups: {
    g: {
      nodeId: 'g',
      config: { form: 'branches', tasks: [{ key: 'a', entry: 'a1' }, { key: 'b', entry: 'b1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
      bodies: { a: ['a1', 'a2'], b: ['b1'] }, nested: [], successTarget: 'done', failureTarget: 'oops',
    },
  },
  owner: { a1: { groupNodeId: 'g', taskKey: 'a' }, a2: { groupNodeId: 'g', taskKey: 'a' }, b1: { groupNodeId: 'g', taskKey: 'b' } },
  order: ['g'],
}
const rootInstance = {
  id: 'root', ownerId: 'u1', workflowName: 'fanout', state: 'waiting', priority: 'normal', variables: {}, originalVariables: {},
  currentNodeId: 'g', createdAt: '2026-10-16T10:00:00Z', updatedAt: '2026-10-16T10:00:00Z', workflowSnapshot: def,
  interactiveActive: false, interactiveNodeId: null,
} as unknown as Instance

const live = (instanceId: string, nodeId: string, state: ExecutionContext['nodes'][number]['state']) => ({ instanceId, nodeId, state })
const task = (key: string, over: Partial<GroupPanelTask> = {}): GroupPanelTask => ({
  key, kind: 'branch', origin: key === 'a' ? 'a1' : 'b1', childInstanceId: `br-${key}`, liveStatus: 'running', frozenStatus: '',
  blocking: true, continuingIndependently: false, cancelRequested: false, redacted: false, ...over,
})
const panel = (over: Partial<GroupPanel> = {}): GroupPanel => ({
  groupRunId: 'run-g', instanceId: 'root', nodeId: 'g', form: 'branches', completionRule: 'all_succeed',
  unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results', state: 'running', tasks: [task('a'), task('b')],
  progress: { total: 2, completed: 0, failed: 0, cancelled: 0, active: 2, notStarted: 0 }, waitingReasons: ['Waiting for tasks a, b'],
  ...over,
})
function aggregate(over: Partial<GraphState> = {}): GraphState {
  return {
    rootInstanceId: 'root', version: 'v1', active: true, layout, warnings: [],
    contexts: [
      { instanceId: 'root', kind: 'root', state: 'waiting', blocking: true, redacted: false, nodes: [live('root', 'start', 'completed'), live('root', 'g', 'waiting')] },
      { instanceId: 'br-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')] },
      { instanceId: 'br-b', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'b', kind: 'branch', state: 'running', blocking: true, redacted: false, nodes: [live('br-b', 'b1', 'running')] },
    ],
    groups: [panel()],
    ...over,
  }
}

// ---------------------------------------------------------------------------
// API fake keyed by (instance id, node id): a read against the wrong instance
// is a 404, so a selection that traced the ROOT for a branch node cannot pass.
// ---------------------------------------------------------------------------
let currentAggregate: GraphState
const exec = (instanceId: string, nodeId: string, output: Record<string, unknown>): NodeExecution => ({
  id: `${instanceId}-${nodeId}`, instanceId, nodeId, attempt: 1, status: 'succeeded', output, startedAt: '2026-10-16T10:00:00Z',
})
const notFound = () => Promise.reject(new Error('API 404: instance not found'))
const apiMock = vi.hoisted(() => ({
  instances: {
    get: vi.fn(),
    getTrace: vi.fn(),
    getVariables: vi.fn(),
    getNodeTrace: vi.fn(),
    graphState: vi.fn(),
    pause: vi.fn(), resume: vi.fn(), cancel: vi.fn(), retry: vi.fn(), rerun: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

// WebSocket stub recording every URL the view opens.
const sockets: Array<{ url: string; open: () => void; sent: object[]; emit: (e: WebSocketEvent) => void }> = []
class StubSocket {
  static readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: object[] = []
  constructor(readonly url: string) {
    sockets.push({
      url,
      open: () => { this.readyState = StubSocket.OPEN; this.onopen?.() },
      sent: this.sent,
      emit: (e) => this.onmessage?.({ data: JSON.stringify(e) }),
    })
  }
  addEventListener() {}
  send(data: string) { this.sent.push(JSON.parse(data)) }
  close() {}
}

import ExecutionView from './ExecutionView'

beforeEach(() => {
  sockets.length = 0
  vi.stubGlobal('WebSocket', StubSocket)
  currentAggregate = aggregate()
  apiMock.instances.get.mockImplementation((id: string) => (id === 'root' ? Promise.resolve(rootInstance) : notFound()))
  apiMock.instances.getTrace.mockImplementation((id: string) => (id === 'root' ? Promise.resolve([exec('root', 'start', {})]) : notFound()))
  apiMock.instances.getVariables.mockImplementation((id: string) =>
    ({ root: Promise.resolve({ owner: 'root' }), 'br-b': Promise.resolve({ owner: 'branch-b' }) } as Record<string, Promise<unknown>>)[id] ?? notFound(),
  )
  apiMock.instances.getNodeTrace.mockImplementation((id: string, nodeId: string) =>
    id === 'br-b' && nodeId === 'b1' ? Promise.resolve(exec('br-b', 'b1', { answer: 'from-branch-b' })) : notFound(),
  )
  apiMock.instances.graphState.mockImplementation((id: string) => (id === 'root' ? Promise.resolve(currentAggregate) : notFound()))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function renderView() {
  return render(
    <MemoryRouter initialEntries={['/instances/root']}>
      <Routes>
        <Route path="/instances/:id" element={<ExecutionView />} />
      </Routes>
    </MemoryRouter>,
  )
}

const stateOf = (id: string) => screen.getByTestId(`node-${id}`).getAttribute('data-state')

describe('ExecutionView — aggregate graph state (happy path)', () => {
  // Provenance: the root's own trace/cursor knows nothing of the branches, so
  // only the aggregate can mark a2 AND b1 running at the same time.
  it('highlights the running nodes of BOTH concurrent branches', async () => {
    renderView()
    await waitFor(() => expect(stateOf('a2')).toBe('running'))
    expect(stateOf('b1')).toBe('running')
    expect(stateOf('a1')).toBe('completed')
    expect(stateOf('start')).toBe('completed')
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('root', expect.any(AbortSignal))
  })

  it('renders the group as header + footer instead of a plain node, and subscribes to the subtree', async () => {
    renderView()
    await screen.findByTestId('node-group:g:header')
    expect(screen.getByTestId('node-group:g:footer').getAttribute('data-type')).toBe('groupFooter')
    expect(screen.queryByTestId('node-g')).toBeNull()
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))).toBe(true)
  })
})

describe('ExecutionView — all-active viewport sink', () => {
  const enclosingBounds = (ids: string[], height = 80) => {
    const rects = ids.map((id) => screen.getByTestId(`node-${id}`))
    const xs = rects.map((r) => Number(r.getAttribute('data-x')))
    const ys = rects.map((r) => Number(r.getAttribute('data-y')))
    return { x: Math.min(...xs), y: Math.min(...ys), width: Math.max(...xs) + 200 - Math.min(...xs), height: Math.max(...ys) + height - Math.min(...ys) }
  }

  it('feeds both concurrent branch positions into the real viewport helper', async () => {
    renderView()
    await waitFor(() => expect(stateOf('a2')).toBe('running'))
    // The fit runs in an effect after the live states render; wait for it rather than racing it.
    await waitFor(() => expect(fitBoundsSpy).toHaveBeenCalledWith(enclosingBounds(['a2', 'b1']), expect.objectContaining({ padding: 0.25 })))
  })

  it.each(['queued', 'paused'] as const)('frames a %s branch alongside a running branch', async (state) => {
    const gs = aggregate()
    gs.contexts[1] = { ...gs.contexts[1], state, nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', state)] }
    gs.groups[0].tasks[0] = task('a', { liveStatus: state })
    currentAggregate = gs

    renderView()
    await waitFor(() => expect(stateOf('a2')).toBe(state))
    // The fit runs in an effect after the live states render; wait for it rather than racing it.
    await waitFor(() => expect(fitBoundsSpy).toHaveBeenCalledWith(enclosingBounds(['a2', 'b1']), expect.objectContaining({ padding: 0.25 })))
  })

  // A child with no readable step history must still be framed by its live
  // task status. activeNodes alone cannot recover this case.
  it.each(['queued', 'paused'] as const)('frames %s workflow task cards without child step history', async (state) => {
    const config = { ...layout.groups.g.config, form: 'workflows' as const, tasks: ['a', 'b', 'hidden', 'finished'].map((key) => ({ key, workflow: `child-${key}` })) }
    const workflowDef = { ...def, nodes: [n('g', 'parallel_group'), n('done'), n('oops')], edges: def.edges.filter((e) => e.output === 'success' || e.output === 'failure') }
    apiMock.instances.get.mockResolvedValue({ ...rootInstance, workflowSnapshot: workflowDef })
    currentAggregate = aggregate({
      layout: { groups: { g: { ...layout.groups.g, config, bodies: {} } }, owner: {}, order: ['g'] },
      contexts: [
        aggregate().contexts[0],
        { ...aggregate().contexts[1], kind: 'workflow', state, nodes: [] },
        { ...aggregate().contexts[2], kind: 'workflow', state: 'running', nodes: [] },
        { instanceId: '00000000-0000-0000-0000-000000000000', parentInstanceId: 'root', kind: 'workflow', state: 'unknown', blocking: true, redacted: true, nodes: [] },
      ],
      groups: [panel({ form: 'workflows', tasks: [
        task('a', { kind: 'workflow', origin: 'child-a', liveStatus: state }),
        task('b', { kind: 'workflow', origin: 'child-b' }),
        task('hidden', { kind: 'workflow', origin: 'child-hidden', liveStatus: state, childInstanceId: null, redacted: true }),
        task('finished', { kind: 'workflow', origin: 'child-finished', liveStatus: 'completed', childInstanceId: null, blocking: false }),
      ], progress: { total: 4, completed: 1, failed: 0, cancelled: 0, active: 3, notStarted: 0 } })],
    })

    renderView()
    await screen.findByTestId('node-group:g:task:a')
    await waitFor(() => expect(fitBoundsSpy).toHaveBeenCalledWith(enclosingBounds(['group:g:task:a', 'group:g:task:b'], 96), expect.objectContaining({ padding: 0.25 })))
  })

  it('frames active workflow task cards, even though their child nodes are not in the root definition', async () => {
    const config = { form: 'workflows' as const, tasks: [{ key: 'a', workflow: 'child-a' }, { key: 'b', workflow: 'child-b' }], completionRule: 'all_succeed' as const, unfinishedBehavior: 'cancel_unfinished' as const, gatherMode: 'results' as const }
    const workflowDef = { ...def, nodes: [n('g', 'parallel_group'), n('done'), n('oops')], edges: def.edges.filter((e) => e.output === 'success' || e.output === 'failure') }
    apiMock.instances.get.mockResolvedValue({ ...rootInstance, workflowSnapshot: workflowDef })
    currentAggregate = aggregate({
      layout: { groups: { g: { ...layout.groups.g, config, bodies: {} } }, owner: {}, order: ['g'] },
      contexts: [
        aggregate().contexts[0],
        { ...aggregate().contexts[1], kind: 'workflow', nodes: [live('br-a', 'build', 'running')] },
        { ...aggregate().contexts[2], kind: 'workflow', nodes: [live('br-b', 'lint', 'waiting')] },
      ],
      groups: [panel({ form: 'workflows', tasks: [task('a', { kind: 'workflow', origin: 'child-a' }), task('b', { kind: 'workflow', origin: 'child-b', liveStatus: 'waiting' })] })],
    })
    renderView()
    await screen.findByTestId('node-group:g:task:a')
    await waitFor(() => expect(fitBoundsSpy).toHaveBeenCalledWith(enclosingBounds(['group:g:task:a', 'group:g:task:b'], 96), expect.objectContaining({ padding: 0.25 })))
  })
})

describe('ExecutionView — instance-qualified selection (success)', () => {
  it('selecting a branch-body node opens the OWNING branch instance\'s trace and variables', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-b1'))
    await waitFor(() => expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('br-b', 'b1'))
    expect(apiMock.instances.getNodeTrace).not.toHaveBeenCalledWith('root', 'b1')
    expect(apiMock.instances.getVariables).toHaveBeenCalledWith('br-b')
    expect(await screen.findByTestId('trace-owner')).toHaveTextContent('Trace from branch task b · br-b')
    expect(screen.getByTestId('node-b1').getAttribute('data-selected')).toBe('true')
    expect(await screen.findByText(/"answer": "from-branch-b"/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Variables' }))
    expect(await screen.findByText(/branch-b/)).toBeInTheDocument()
  })

  it('header and footer both open the same group panel', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:header'))
    const fromHeader = await screen.findByTestId('group-panel')
    expect(within(fromHeader).getByText('Fan out')).toBeInTheDocument()
    expect(within(fromHeader).getByText('All succeed')).toBeInTheDocument()
    expect(within(fromHeader).getByText('Waiting for tasks a, b')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('node-group:g:footer'))
    expect(within(await screen.findByTestId('group-panel')).getByText('All succeed')).toBeInTheDocument()
    expect(screen.getByTestId('node-group:g:footer').getAttribute('data-selected')).toBe('true')
  })

  it('shows the winner, and a frozen unfinished task that is live completed', async () => {
    currentAggregate = aggregate({
      active: true,
      groups: [panel({
        completionRule: 'first_success', unfinishedBehavior: 'finish_independently', state: 'done', winner: 'b', waitingReasons: [],
        tasks: [
          task('a', { liveStatus: 'completed', frozenStatus: 'unfinished', blocking: false, continuingIndependently: true }),
          task('b', { liveStatus: 'completed', frozenStatus: 'completed', blocking: false }),
        ],
        progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    const p = await screen.findByTestId('group-panel')
    expect(within(p).getByText('Winner:')).toBeInTheDocument()
    const rowA = within(p).getByTestId('group-task-row-a')
    expect(within(rowA).getByText('completed')).toBeInTheDocument()
    expect(within(rowA).getByText('unfinished')).toBeInTheDocument()
  })
})

describe('ExecutionView — waiting, sessions and redaction', () => {
  it('shows a done group whose continuation is still pending', async () => {
    currentAggregate = aggregate({ groups: [panel({
      state: 'done', waitingReasons: ['continuation_pending'],
      tasks: ['a', 'b'].map((key) => task(key, { liveStatus: 'completed', blocking: false })),
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
    })] })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    expect(within(await screen.findByTestId('group-waiting')).getByText('Waiting for parent continuation')).toBeInTheDocument()
  })

  it('waits on resolution with zero outstanding tasks and opens the parent\'s session at the group node', async () => {
    currentAggregate = aggregate({
      groups: [panel({
        gatherMode: 'results_and_files', state: 'resolving_conflicts',
        tasks: [task('a', { liveStatus: 'completed', blocking: false }), task('b', { liveStatus: 'completed', blocking: false })],
        progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
        waitingReasons: ['Resolving merge conflicts for task b (attempt 1)'],
        gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 1, attempt: 1, instanceId: 'root', nodeId: 'g' } },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    const p = await screen.findByTestId('group-panel')
    expect(within(p).getByText('0 outstanding blocking tasks')).toBeInTheDocument()
    expect(within(p).getByText('Resolving merge conflicts for task b (attempt 1)')).toBeInTheDocument()

    fireEvent.click(within(p).getByRole('button', { name: 'Open resolution session' }))

    // Seeded as a group resolution (no history frame reaches an already-open socket),
    // so the header and Abandon are present rather than the plain interactive panel.
    expect(await screen.findByText('Gather Conflict Resolution')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Abandon gather' })).toBeInTheDocument()
    expect(screen.getByText('Fan out')).toBeInTheDocument()
  })

  it('reopens a fresh attempt of the route-owned session after the previous attempt ended', async () => {
    const resolving = (attempt: number, version: string) => aggregate({
      version,
      groups: [panel({
        gatherMode: 'results_and_files', state: 'resolving_conflicts',
        waitingReasons: [`Resolving merge conflicts for task b (attempt ${attempt})`],
        gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 1, attempt, instanceId: 'root', nodeId: 'g' } },
      })],
    })
    currentAggregate = resolving(1, 'v1')
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    fireEvent.click(within(await screen.findByTestId('group-panel')).getByRole('button', { name: 'Open resolution session' }))
    expect(await screen.findByText('Gather Conflict Resolution')).toBeInTheDocument()

    // Ending attempt 1 while still conflicted opens attempt 2; no history frame
    // reaches the already-open route socket.
    const routeSocket = sockets.find((s) => s.url.endsWith('/root/ws'))!
    act(() => routeSocket.emit({ type: 'interactive.message', timestamp: '', data: { role: 'assistant', content: 'attempt one transcript' } }))
    expect(await screen.findByText('attempt one transcript')).toBeInTheDocument()
    act(() => routeSocket.emit({ type: 'interactive.ended', timestamp: '', data: { reason: 'user_ended', sessionKind: 'group_resolution' } }))
    act(() => routeSocket.emit({ type: 'interactive.message', timestamp: '', data: { role: 'assistant', content: 'attempt two autonomous turn' } }))
    expect(await screen.findByTestId('group-panel')).toBeInTheDocument()
    currentAggregate = resolving(2, 'v2')
    act(() => sockets.find((s) => s.url.endsWith('?subtree=1'))!.emit({ type: 'group.conflict', timestamp: '', data: {} }))
    const p = await screen.findByTestId('group-panel')
    await waitFor(() => expect(within(p).getByText('Resolving merge conflicts for task b (attempt 2)')).toBeInTheDocument())

    fireEvent.click(within(p).getByRole('button', { name: 'Open resolution session' }))
    expect(await screen.findByText('Gather Conflict Resolution')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Abandon gather' })).toBeInTheDocument()
    expect(screen.queryByTestId('group-panel')).toBeNull()
    expect(screen.getByText('attempt two autonomous turn')).toBeInTheDocument()
    expect(screen.queryByText('attempt one transcript')).toBeNull()
  })

  it('a nested group\'s session routes through ITS parent instance stream, not the route instance', async () => {
    currentAggregate = aggregate({
      groups: [panel({
        gatherMode: 'results_and_files', state: 'resolving_conflicts', waitingReasons: ['Resolving merge conflicts'],
        gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 0, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 0, attempt: 1, instanceId: 'br-b', nodeId: 'g' } },
      })],
    })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    fireEvent.click(await screen.findByRole('button', { name: 'Open resolution session' }))
    expect(await screen.findByText('Conflict resolution · Fan out')).toBeInTheDocument()
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/br-b/ws'))).toBe(true)
  })

  it('shows a redacted branch as a hidden dependency in the graph and the panel', async () => {
    currentAggregate = aggregate({
      contexts: aggregate().contexts.slice(0, 2),
      groups: [panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] })],
    })
    renderView()
    await waitFor(() => expect(stateOf('b1')).toBe('unknown'))
    fireEvent.click(screen.getByTestId('node-group:g:header'))
    const row = within(await screen.findByTestId('group-panel')).getByTestId('group-task-row-b')
    expect(within(row).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(row).queryByRole('link')).toBeNull()
  })
})

describe('ExecutionView — selected session owner is the control sink', () => {
  it('does not carry conversation data into another owning instance when switching resolution sessions', async () => {
    const resolving = (owner: string) => panel({
      state: 'resolving_conflicts', gatherMode: 'results_and_files',
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 0, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 0, attempt: 1, instanceId: owner, nodeId: 'g' } },
    })
    currentAggregate = aggregate({ groups: [resolving('br-b')] })
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:g:footer'))
    fireEvent.click(await screen.findByRole('button', { name: 'Open resolution session' }))
    const firstSocket = sockets.find((s) => s.url.endsWith('/br-b/ws'))!
    act(() => firstSocket.emit({ type: 'interactive.history', timestamp: '', data: { nodeId: 'g', messages: [{ role: 'assistant', content: 'branch b private conversation' }] } }))
    expect(await screen.findByText('branch b private conversation')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('node-group:g:footer'))
    currentAggregate = aggregate({ version: 'v2', groups: [resolving('br-a')] })
    act(() => sockets.find((s) => s.url.endsWith('?subtree=1'))!.emit({ type: 'group.task_updated', timestamp: '', data: {} }))
    await waitFor(() => expect(apiMock.instances.get).toHaveBeenCalledTimes(2))
    fireEvent.click(await screen.findByRole('button', { name: 'Open resolution session' }))
    await waitFor(() => expect(sockets.some((s) => s.url.endsWith('/br-a/ws'))).toBe(true))
    expect(screen.queryByText('branch b private conversation')).toBeNull()
  })

  it('opens a nested session even while the route instance has an active session, and sends controls only to the selected owner', async () => {
    currentAggregate = aggregate({ groups: [panel({
      state: 'resolving_conflicts', gatherMode: 'results_and_files',
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 0, tasks: [], attempts: [], activeSession: { groupRunId: 'run-g', mergeIndex: 0, attempt: 1, instanceId: 'br-b', nodeId: 'g' } },
    })] })
    renderView()
    await screen.findByTestId('node-group:g:footer')
    const routeSocket = sockets.find((s) => s.url.endsWith('/root/ws'))!
    act(() => routeSocket.emit({ type: 'interactive.history', timestamp: '', data: { nodeId: 'root-chat', messages: [{ role: 'assistant', content: 'route conversation' }] } }))
    expect(await screen.findByText('route conversation')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('node-group:g:footer'))
    fireEvent.click(await screen.findByRole('button', { name: 'Open resolution session' }))
    const childSocket = sockets.find((s) => s.url.endsWith('/br-b/ws'))!
    act(() => {
      childSocket.open()
      childSocket.emit({ type: 'interactive.history', timestamp: '', data: { nodeId: 'g', sessionKind: 'group_resolution', messages: [{ role: 'assistant', content: 'branch resolution conversation' }] } })
    })
    expect(await screen.findByText('branch resolution conversation')).toBeInTheDocument()
    expect(screen.queryByText('route conversation')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Abandon gather' }))
    expect(childSocket.sent).toEqual([{ type: 'interactive.abandon' }])
    expect(routeSocket.sent).toEqual([])
    act(() => childSocket.emit({ type: 'interactive.ended', timestamp: '', data: {} }))
    expect(await screen.findByTestId('group-panel')).toBeInTheDocument()
  })
})

describe('ExecutionView — refresh (error recovery)', () => {
  it('keeps the legacy graph live after graph-state returns 501', async () => {
    vi.useFakeTimers()
    apiMock.instances.graphState.mockRejectedValue(Object.assign(new Error('API 501: unavailable'), { status: 501 }))
    renderView()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(stateOf('g')).toBe('waiting')
    apiMock.instances.get.mockResolvedValue({ ...rootInstance, state: 'completed', currentNodeId: undefined })
    apiMock.instances.getTrace.mockResolvedValue([exec('root', 'start', {}), exec('root', 'g', {})])

    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })

    expect(stateOf('g')).toBe('completed')
    expect(screen.getByText('completed')).toBeInTheDocument()
    expect(apiMock.instances.graphState).toHaveBeenCalledTimes(1)
  })

  // The old handler listened for node.skipped / variables.updated /
  // instance.state_changed, which are never emitted. Now any aggregate version
  // change (event, poll or reconnect) refreshes the route instance's reads.
  it('refreshes the route instance when a subtree event changes the aggregate version', async () => {
    renderView()
    await waitFor(() => expect(stateOf('b1')).toBe('running'))
    expect(apiMock.instances.get).toHaveBeenCalledTimes(1)

    currentAggregate = aggregate({ version: 'v2' })
    const subtree = sockets.find((s) => s.url.endsWith('?subtree=1'))!
    act(() => subtree.emit({ type: 'group.task_updated', timestamp: '2026-10-16T10:01:00Z', data: {}, sourceInstanceId: 'br-b' }))

    await waitFor(() => expect(apiMock.instances.get).toHaveBeenCalledTimes(2))
  })

  it('falls back to the single-instance trace view when the aggregate is unavailable', async () => {
    apiMock.instances.graphState.mockImplementation(() => Promise.reject(new Error('API 500: boom')))
    renderView()
    await waitFor(() => expect(stateOf('g')).toBe('waiting'))
    expect(stateOf('start')).toBe('completed')
    expect(screen.queryByTestId('node-group:g:header')).toBeNull()
  })
})

describe('ExecutionView — groups nested inside a child workflow', () => {
  // Root group `g` (workflows form) runs child-a as instance wf-a. wf-a's own
  // graph holds a branches group `h` (task x: x1 -> x2), run by branch br-x.
  // The child's node ids deliberately collide with the root's (`start`).
  const childDef: WorkflowDefinition = {
    name: 'child-a', description: '', author: '',
    nodes: [n('start'), n('h', 'parallel_group', 'Nested fan'), n('x1'), n('x2'), n('after')],
    edges: [
      { source: 'start', target: 'h' },
      { source: 'h', target: 'x1', output: 'task', label: 'x' },
      { source: 'x1', target: 'x2' },
      { source: 'h', target: 'after', output: 'success' },
    ],
  }
  const childLayout: GroupGraph = {
    groups: {
      h: {
        nodeId: 'h',
        config: { form: 'branches', tasks: [{ key: 'x', entry: 'x1' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' },
        bodies: { x: ['x1', 'x2'] }, nested: [], successTarget: 'after', failureTarget: '',
      },
    },
    owner: { x1: { groupNodeId: 'h', taskKey: 'x' }, x2: { groupNodeId: 'h', taskKey: 'x' } },
    order: ['h'],
  }
  const rootWorkflowDef = { ...def, nodes: [n('start'), n('g', 'parallel_group', 'Fan out'), n('done'), n('oops')], edges: def.edges.filter((e) => e.output === 'success' || e.output === 'failure').concat({ source: 'start', target: 'g' }) }
  const rootWorkflowLayout: GroupGraph = {
    groups: { g: { ...layout.groups.g, config: { form: 'workflows', tasks: [{ key: 'a', workflow: 'child-a' }], completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results' }, bodies: {} } },
    owner: {}, order: ['g'],
  }
  const nestedAggregate = (): GraphState => ({
    rootInstanceId: 'root', version: 'v1', active: true, layout: rootWorkflowLayout, warnings: [],
    contexts: [
      { instanceId: 'root', kind: 'root', state: 'waiting', blocking: true, redacted: false, nodes: [live('root', 'start', 'completed'), live('root', 'g', 'waiting')] },
      { instanceId: 'wf-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'workflow', workflowName: 'child-a', state: 'waiting', blocking: true, redacted: false,
        nodes: [live('wf-a', 'start', 'completed'), live('wf-a', 'h', 'waiting')], graph: childDef as unknown as Record<string, unknown>, layout: childLayout },
      { instanceId: 'br-x', parentInstanceId: 'wf-a', groupRunId: 'run-h', taskKey: 'x', kind: 'branch', state: 'running', blocking: true, redacted: false,
        nodes: [live('br-x', 'x1', 'completed'), live('br-x', 'x2', 'running')] },
    ],
    groups: [
      panel({ form: 'workflows', tasks: [task('a', { kind: 'workflow', origin: 'child-a', childInstanceId: 'wf-a', liveStatus: 'running' })], progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 } }),
      panel({ groupRunId: 'run-h', instanceId: 'wf-a', nodeId: 'h', tasks: [task('x', { origin: 'x1', childInstanceId: 'br-x' })], progress: { total: 1, completed: 0, failed: 0, cancelled: 0, active: 1, notStarted: 0 }, waitingReasons: ['Waiting for task x'] }),
    ],
  })

  beforeEach(() => {
    apiMock.instances.get.mockImplementation((id: string) => (id === 'root' ? Promise.resolve({ ...rootInstance, workflowSnapshot: rootWorkflowDef }) : notFound()))
    currentAggregate = nestedAggregate()
  })

  it('lays out the nested group of the child workflow and shows its live node states', async () => {
    renderView()
    await screen.findByTestId('node-group:g:header')
    expect(await screen.findByTestId('node-group:wf-a:h:header')).toBeInTheDocument()
    expect(screen.getByTestId('node-group:wf-a:h:footer').getAttribute('data-type')).toBe('groupFooter')
    expect(stateOf('wf-a:x2')).toBe('running')
    expect(stateOf('wf-a:x1')).toBe('completed')
    // The child's `start` does not alias the root's `start`.
    expect(stateOf('wf-a:start')).toBe('completed')
    expect(stateOf('start')).toBe('completed')
  })

  it('selecting the nested header opens ITS panel (child instance, original node id), not the root group', async () => {
    renderView()
    fireEvent.click(await screen.findByTestId('node-group:wf-a:h:header'))
    const p = await screen.findByTestId('group-panel')
    expect(within(p).getByText('Nested fan')).toBeInTheDocument()
    expect(within(p).getByText('Waiting for task x')).toBeInTheDocument()
    expect(screen.getByTestId('node-group:wf-a:h:header').getAttribute('data-selected')).toBe('true')
  })

  it('selecting a nested body node opens the branch instance\'s trace with the original node id', async () => {
    apiMock.instances.getNodeTrace.mockImplementation((id: string, nodeId: string) =>
      id === 'br-x' && nodeId === 'x2' ? Promise.resolve(exec('br-x', 'x2', { answer: 'nested' })) : notFound(),
    )
    apiMock.instances.getVariables.mockImplementation((id: string) => (id === 'root' || id === 'br-x' ? Promise.resolve({ owner: id }) : notFound()))
    renderView()
    fireEvent.click(await screen.findByTestId('node-wf-a:x2'))
    await waitFor(() => expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('br-x', 'x2'))
    expect(await screen.findByText(/"answer": "nested"/)).toBeInTheDocument()
  })

  it('a hidden child workflow contributes no nested graph', async () => {
    const a = nestedAggregate()
    a.contexts[1] = { instanceId: 'wf-a', parentInstanceId: 'root', kind: 'workflow', state: 'unknown', blocking: true, redacted: true, nodes: [] }
    a.contexts = a.contexts.slice(0, 2)
    a.groups = a.groups.slice(0, 1)
    currentAggregate = a
    renderView()
    await screen.findByTestId('node-group:g:header')
    expect(screen.queryByTestId('node-group:wf-a:h:header')).toBeNull()
  })
})

// Paired seam 17: a branch task opened as the route root (hierarchy row or task
// card "open run"). The payload mirrors what the real launcher + graph-state
// produce (pinned by cmd/arbitron TestGraphStateE2E_LaunchedBranchTaskOwnsItsBodyAsRootAndAsNestedContext):
// kind 'root', the PARENT's definition and layout, only the branch's own body
// nodes, and no panel for the parent-owned group.
describe('ExecutionView — a launched branch task opened as the route root', () => {
  const branchInstance = {
    ...rootInstance, id: 'br-a', state: 'running', currentNodeId: 'a2', parentInstanceId: 'root',
  } as unknown as Instance
  const branchRoot = (): GraphState => ({
    rootInstanceId: 'br-a', version: 'b1', active: true, layout, warnings: [],
    contexts: [{
      instanceId: 'br-a', parentInstanceId: 'root', groupRunId: 'run-g', taskKey: 'a', kind: 'root', state: 'running',
      blocking: true, redacted: false, graph: def as unknown as Record<string, unknown>,
      nodes: [live('br-a', 'a1', 'completed'), live('br-a', 'a2', 'running')],
    }],
    groups: [],
  })

  function renderBranchRoot() {
    return render(
      <MemoryRouter initialEntries={['/instances/br-a']}>
        <Routes>
          <Route path="/instances/:id" element={<ExecutionView />} />
        </Routes>
      </MemoryRouter>,
    )
  }

  beforeEach(() => {
    apiMock.instances.get.mockImplementation((id: string) => (id === 'br-a' ? Promise.resolve(branchInstance) : notFound()))
    apiMock.instances.getTrace.mockImplementation((id: string) => (id === 'br-a' ? Promise.resolve([exec('br-a', 'a1', {})]) : notFound()))
    apiMock.instances.getVariables.mockImplementation((id: string) => (id === 'br-a' ? Promise.resolve({ owner: 'branch-a' }) : notFound()))
    apiMock.instances.graphState.mockImplementation((id: string) => (id === 'br-a' ? Promise.resolve(branchRoot()) : notFound()))
    apiMock.instances.getNodeTrace.mockImplementation((id: string, nodeId: string) =>
      id === 'br-a' && nodeId === 'a2' ? Promise.resolve(exec('br-a', 'a2', { answer: 'from-branch-a' })) : notFound(),
    )
  })

  it('renders its own body from its cursor and trace, never pending', async () => {
    renderBranchRoot()
    await waitFor(() => expect(stateOf('a2')).toBe('running'))
    expect(stateOf('a1')).toBe('completed')
    expect(stateOf('b1')).toBe('pending')
    expect(stateOf('start')).toBe('pending')
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/br-a/ws?subtree=1'))).toBe(true)
  })

  it('selecting a body node traces the branch instance itself', async () => {
    renderBranchRoot()
    fireEvent.click(await screen.findByTestId('node-a2'))
    await waitFor(() => expect(apiMock.instances.getNodeTrace).toHaveBeenCalledWith('br-a', 'a2'))
    expect(await screen.findByText(/"answer": "from-branch-a"/)).toBeInTheDocument()
  })
})
