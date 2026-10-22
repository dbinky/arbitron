import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { fanoutAggregate, fanoutContexts, independentAggregate, nestedChildGroupAggregate, panel, repeatedChildAggregate, summary, task } from '../graph/liveSummary.fixtures'
import { graphStateFrom, listFrom, localAdmin, newWorld, sockets, StubSocket, type QueueWorld } from './queueWorld.fixtures'

const fitBoundsSpy = vi.hoisted(() => vi.fn(() => Promise.resolve(true)))

// React Flow stub: every rendered node becomes a clickable div exposing its
// type and execution state, so the test reads what the graph would highlight.
vi.mock('@xyflow/react', async () => ({
  ...await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react'),
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
          data-definition-node={String(n.data.definitionNodeId ?? n.id)}
          data-layout-instance={n.data.layoutInstanceId as string | undefined}
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

vi.mock('../hooks/useSystemWebSocket', () => ({ useSystemWebSocket: () => ({ connected: true }) }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const apiMock = vi.hoisted(() => ({
  me: vi.fn(),
  instances: {
    list: vi.fn(),
    graphState: vi.fn(),
    getNodeTrace: vi.fn(),
    dismiss: vi.fn(),
    restore: vi.fn(),
    dismissAll: vi.fn(),
  },
}))
vi.mock('../api/client', () => ({ api: apiMock }))

import QueueView from './QueueView'

let world: QueueWorld

beforeEach(() => {
  sockets.length = 0
  vi.stubGlobal('WebSocket', StubSocket)
  world = newWorld()
  // The fan-out run waits at its group while both branches run. The branch
  // runs are ALSO in the running list (the per-state lists are not
  // parent-filtered); topLevelRows nests them under the root, so the root is
  // the only top-level row of the Active section.
  world.byState.waiting = [summary({ id: 'root', runName: 'Fan-out run', workflowName: 'fanout', state: 'waiting', childCount: 2 })]
  world.byState.running = [
    summary({ id: 'br-a', runName: 'Branch a', workflowName: 'fanout', parentInstanceId: 'root' }),
    summary({ id: 'br-b', runName: 'Branch b', workflowName: 'fanout', parentInstanceId: 'root' }),
  ]
  world.graphStates.root = fanoutAggregate()
  apiMock.me.mockResolvedValue(localAdmin)
  apiMock.instances.list.mockImplementation((p) => listFrom(world)(p))
  apiMock.instances.graphState.mockImplementation((id: string) => graphStateFrom(world)(id))
  apiMock.instances.getNodeTrace.mockRejectedValue(new Error('API 404: not found'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

function Location() {
  return <span data-testid="location">{useLocation().pathname}</span>
}

function renderQueue() {
  return render(
    <MemoryRouter>
      <QueueView />
      <Location />
    </MemoryRouter>,
  )
}

const rowEl = (id: string) => document.querySelector<HTMLElement>(`tr[data-row-id="${id}"]`)

/** Opens the queue row's detail — the only place the live-run pane is mounted. */
async function openLivePane(id: string): Promise<HTMLElement> {
  await waitFor(() => expect(rowEl(id)).not.toBeNull())
  fireEvent.click(within(rowEl(id)!).getByRole('button', { name: 'Expand' }))
  return screen.findByTestId('run-live-pane')
}

const stateIn = (pane: HTMLElement, nodeId: string) => within(pane).getByTestId(`node-${nodeId}`).getAttribute('data-state')
const activeItems = (pane: HTMLElement) => within(pane).getAllByTestId('run-live-active-item').map((li) => li.textContent)

describe('QueueView live-run pane — aggregate graph (happy path + provenance)', () => {
  it.each(['queued', 'paused'] as const)('lists and frames a %s branch alongside running work', async (state) => {
    const gs = fanoutAggregate()
    gs.contexts[1] = { ...gs.contexts[1], state, nodes: [{ instanceId: 'br-a', nodeId: 'a2', state }] }
    gs.groups[0].tasks[0] = task('a', { liveStatus: state })
    world.graphStates.root = gs
    renderQueue()
    const pane = await openLivePane('root')

    await waitFor(() => expect(stateIn(pane, 'a2')).toBe(state))
    expect(activeItems(pane)).toEqual(['Fan out · waiting', `A2 · ${state} · task a`, 'B1 · running · task b'])
    const a = within(pane).getByTestId('node-a2')
    const b = within(pane).getByTestId('node-b1')
    const ax = Number(a.getAttribute('data-x')), ay = Number(a.getAttribute('data-y'))
    const bx = Number(b.getAttribute('data-x')), by = Number(b.getAttribute('data-y'))
    await waitFor(() => expect(fitBoundsSpy).toHaveBeenCalledWith({
      x: Math.min(ax, bx), y: Math.min(ay, by), width: Math.abs(ax - bx) + 200, height: Math.abs(ay - by) + 80,
    }, expect.objectContaining({ padding: 0.25 })))
  })

  // The root's own cursor knows nothing of its branches: only the aggregate
  // can show a2 AND b1 running at the same time.
  it('shows BOTH concurrently running branch nodes as running in the queue view', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    await waitFor(() => expect(stateIn(pane, 'a2')).toBe('running'))
    expect(stateIn(pane, 'b1')).toBe('running')
    expect(stateIn(pane, 'a1')).toBe('completed')
    expect(within(pane).getByTestId('node-group:g:header')).toBeInTheDocument()
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a', 'B1 · running · task b'])
  })

  // Provenance: fails if the pane is not mounted in the route's row detail, or
  // if useGraphState is fed an empty or wrong id (the fake 404s every other id,
  // and useGraphState('') never fetches or subscribes).
  it("feeds the pane the expanded row's instance id: its graph-state and its SUBTREE socket", async () => {
    renderQueue()
    const pane = await openLivePane('root')
    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('running'))

    expect(apiMock.instances.graphState).toHaveBeenCalledWith('root', expect.any(AbortSignal))
    expect(apiMock.instances.graphState.mock.calls.every(([id]) => id === 'root')).toBe(true)
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))).toBe(true)
    expect(within(pane).getByRole('link', { name: 'Open run view' })).toHaveAttribute('href', '/instances/root')
  })
})

describe('QueueView live-run pane — instance-qualified selection and group panel (success)', () => {
  it('shows a done group whose continuation is still pending in the expanded row', async () => {
    world.graphStates.root = fanoutAggregate({ groups: [panel({
      state: 'done', waitingReasons: ['continuation_pending'],
      tasks: ['a', 'b'].map((key) => task(key, { liveStatus: 'completed', blocking: false })),
      progress: { total: 2, completed: 2, failed: 0, cancelled: 0, active: 0, notStarted: 0 },
    })] })
    renderQueue()
    const pane = await openLivePane('root')
    fireEvent.click(await within(pane).findByTestId('node-group:g:footer'))
    expect(within(await within(pane).findByTestId('group-waiting')).getByText('Waiting for parent continuation')).toBeInTheDocument()
  })

  it('selecting a branch-body node points at the OWNING branch run, not the queue row', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-b1'))

    const sel = await within(pane).findByTestId('run-live-selection')
    expect(sel).toHaveTextContent('B1')
    expect(sel).toHaveTextContent('branch task b')
    expect(within(sel).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/br-b')
    expect(within(pane).getByTestId('node-b1').getAttribute('data-selected')).toBe('true')
  })

  // Branch rows are non-root: they are linked from their group.
  it('header and footer open the same group panel, which links each branch run from its group', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-group:g:header'))
    const fromHeader = await within(pane).findByTestId('group-panel')
    expect(within(fromHeader).getByText('Fan out')).toBeInTheDocument()
    expect(within(fromHeader).getByText('All succeed')).toBeInTheDocument()
    expect(within(fromHeader).getByText('Waiting for tasks a, b')).toBeInTheDocument()
    expect(within(within(fromHeader).getByTestId('group-task-row-a')).getByRole('link', { name: 'open run' })).toHaveAttribute('href', '/instances/br-a')
    expect(within(within(fromHeader).getByTestId('group-task-row-b')).getByRole('link', { name: 'open run' })).toHaveAttribute('href', '/instances/br-b')

    fireEvent.click(within(pane).getByTestId('node-group:g:footer'))
    expect(within(await within(pane).findByTestId('group-panel')).getByText('All succeed')).toBeInTheDocument()
  })
})

describe('QueueView live-run pane — redaction and errors', () => {
  it('discovers a retried run and its new branches in the same open pane without an event', async () => {
    vi.useFakeTimers()
    const gs = fanoutAggregate()
    world.graphStates.root = {
      ...gs, active: false, version: 'stopped',
      // Failure before the group launched: Retry can now create its roster.
      contexts: [{ ...gs.contexts[0], state: 'failed', nodes: [
        { instanceId: 'root', nodeId: 'start', state: 'failed' },
        { instanceId: 'root', nodeId: 'g', state: 'pending' },
      ] }],
      groups: [],
    }
    renderQueue()
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    fireEvent.click(within(rowEl('root')!).getByRole('button', { name: 'Expand' }))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    const pane = screen.getByTestId('run-live-pane')
    expect(stateIn(pane, 'start')).toBe('failed')
    expect(stateIn(pane, 'b1')).toBe('pending')
    // A retry in another view launches the group; its socket event is lost.
    world.graphStates.root = fanoutAggregate({ version: 'retried' })
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(screen.getByTestId('run-live-pane')).toBe(pane)
    expect(stateIn(pane, 'b1')).toBe('running')
    expect(activeItems(pane)).toContain('B1 · running · task b')
  })

  it('a redacted branch is a hidden dependency: unknown state, not listed as active, its id never rendered', async () => {
    world.byState.running = [summary({ id: 'br-a', runName: 'Branch a', workflowName: 'fanout', parentInstanceId: 'root' })]
    world.graphStates.root = fanoutAggregate({
      contexts: fanoutContexts().slice(0, 2),
      groups: [panel({ tasks: [task('a'), task('b', { redacted: true, childInstanceId: null, liveStatus: 'unknown' })] })],
    })
    renderQueue()
    const pane = await openLivePane('root')

    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('unknown'))
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a'])
    fireEvent.click(within(pane).getByTestId('node-group:g:header'))
    const rowB = within(await within(pane).findByTestId('group-panel')).getByTestId('group-task-row-b')
    expect(within(rowB).getByText('hidden dependency')).toBeInTheDocument()
    expect(within(rowB).queryByRole('link')).toBeNull()
    expect(pane.textContent).not.toContain('br-b')
  })

  it('a graph-state failure is confined to the pane: the message shows and the queue rows stay', async () => {
    world.graphStates.root = new Error('API 500: boom')
    renderQueue()
    const pane = await openLivePane('root')

    expect(await within(pane).findByTestId('run-live-error')).toHaveTextContent('Live view unavailable: API 500: boom')
    expect(within(pane).queryByTestId('node-a2')).toBeNull()
    expect(rowEl('root')).not.toBeNull()
  })

  it('an unreadable root snapshot shows the active nodes without drawing an empty graph', async () => {
    const [root, ...rest] = fanoutContexts()
    world.graphStates.root = fanoutAggregate({ contexts: [{ ...root, graph: undefined }, ...rest], warnings: ['workflow snapshot of run root could not be read'] })
    renderQueue()
    const pane = await openLivePane('root')

    expect(await within(pane).findByText('Workflow graph unavailable for this run.')).toBeInTheDocument()
    expect(within(pane).getByText('workflow snapshot of run root could not be read')).toBeInTheDocument()
    expect(within(pane).getByTestId('run-live-active')).toHaveTextContent('Active now (3)')
    expect(within(pane).queryByTestId('node-a2')).toBeNull()
  })
})

describe('QueueView live-run pane — descendant and independent work', () => {
  it('feeds a nested row detail its own instance, not the top-level parent', async () => {
    world.children.root = [summary({ id: 'br-a', runName: 'Branch a', parentInstanceId: 'root' })]
    world.graphStates['br-a'] = fanoutAggregate({
      rootInstanceId: 'br-a', version: 'branch', contexts: [fanoutContexts()[1]], groups: [], layout: undefined,
    })
    renderQueue()
    fireEvent.click(await screen.findByRole('button', { name: 'Show child runs of Fan-out run' }))
    const pane = await openLivePane('br-a')
    await waitFor(() => expect(activeItems(pane)).toEqual(['a2 · running · task a']))
    expect(rowEl('br-a')).toHaveAttribute('data-depth', '1')
    expect(within(pane).getByRole('link', { name: 'Open run view' })).toHaveAttribute('href', '/instances/br-a')
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/br-a/ws?subtree=1'))).toBe(true)
  })

  it('lists repeated workflow nodes separately, including waiting and paused tasks', async () => {
    world.byState.waiting = [summary({ id: 'p-wf', runName: 'Repeated workflow', state: 'waiting' })]
    world.byState.running = []
    world.graphStates['p-wf'] = repeatedChildAggregate()
    renderQueue()
    const pane = await openLivePane('p-wf')
    await waitFor(() => expect(activeItems(pane)).toEqual([
      'Fan out · waiting', 'Build · running · task x', 'Build · waiting · task y', 'Build · paused · task z',
    ]))
  })

  it('keeps a completed discovered parent live while its non-blocking branch runs', async () => {
    world.byState.waiting = []
    world.byState.running = []
    world.ancestors = [summary({ id: 'p-done', runName: 'Completed parent', state: 'completed' })]
    world.graphStates['p-done'] = independentAggregate()
    renderQueue()
    const pane = await openLivePane('p-done')
    await waitFor(() => expect(activeItems(pane)).toEqual(['A2 · running · task a · non-blocking']))
    expect(stateIn(pane, 'a2')).toBe('running')
    expect(sockets.some((s) => s.url.endsWith('/api/v1/instances/p-done/ws?subtree=1'))).toBe(true)
  })
})

describe('QueueView live-run pane — live refresh', () => {
  it('a subtree event refreshes the pane: a finished branch flips while the other keeps running', async () => {
    renderQueue()
    const pane = await openLivePane('root')
    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('running'))

    world.graphStates.root = fanoutAggregate({
      version: 'v2',
      contexts: fanoutContexts('completed'),
      groups: [panel({ tasks: [task('a'), task('b', { liveStatus: 'completed' })] })],
    })
    const subtree = sockets.find((s) => s.url.endsWith('/api/v1/instances/root/ws?subtree=1'))!
    act(() => subtree.emit({ type: 'group.task_updated', timestamp: '2026-10-16T10:01:00Z', data: {}, sourceInstanceId: 'br-b' }))

    await waitFor(() => expect(stateIn(pane, 'b1')).toBe('completed'))
    expect(stateIn(pane, 'a2')).toBe('running')
    expect(activeItems(pane)).toEqual(['Fan out · waiting', 'A2 · running · task a'])
  })
})

describe('QueueView live-run pane — resolution session click-through', () => {
  const sessionOf = (instanceId: string) => ({ groupRunId: 'run-g', mergeIndex: 1, attempt: 1, instanceId, nodeId: 'g' })
  const resolving = (instanceId: string) =>
    panel({
      gatherMode: 'results_and_files', state: 'resolving_conflicts', waitingReasons: [],
      gather: { mode: 'results_and_files', state: 'resolving', mergeIndex: 1, tasks: [], attempts: [], activeSession: sessionOf(instanceId) },
    })

  it("opens the session's parent run from the queue pane", async () => {
    world.graphStates.root = fanoutAggregate({ groups: [resolving('root')] })
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-group:g:header'))
    fireEvent.click(await within(pane).findByRole('button', { name: 'Open resolution session' }))

    expect(screen.getByTestId('location')).toHaveTextContent('/instances/root')
  })

  // The session belongs to the instance that owns the group, which need not be
  // the expanded row: a nested owner must be opened, not the queue row.
  it('opens a nested owner, not the expanded queue row', async () => {
    world.graphStates.root = fanoutAggregate({ groups: [resolving('br-a')] })
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-group:g:header'))
    fireEvent.click(await within(pane).findByRole('button', { name: 'Open resolution session' }))

    expect(screen.getByTestId('location')).toHaveTextContent('/instances/br-a')
  })

  it('offers no session button while no resolution session is active', async () => {
    renderQueue()
    const pane = await openLivePane('root')

    fireEvent.click(await within(pane).findByTestId('node-group:g:header'))

    await within(pane).findByTestId('group-panel')
    expect(within(pane).queryByRole('button', { name: 'Open resolution session' })).toBeNull()
    expect(screen.getByTestId('location')).toHaveTextContent('/')
  })
})

describe('QueueView live-run pane — groups nested inside a child workflow', () => {
  beforeEach(() => {
    world.byState.waiting = [summary({ id: 'p-nest', runName: 'Nested run', workflowName: 'repeat', state: 'waiting', childCount: 1 })]
    world.byState.running = []
    world.graphStates['p-nest'] = nestedChildGroupAggregate()
  })

  it("draws the child workflow's own group and its live nodes beside the root group, ids qualified by instance", async () => {
    renderQueue()
    const pane = await openLivePane('p-nest')

    const node = (instanceId: string, nodeId: string, type = 'arbitronNode') => pane.querySelector<HTMLElement>(`[data-layout-instance="${instanceId}"][data-definition-node="${nodeId}"][data-type="${type}"]`)
    await waitFor(() => expect(node('p-nest', 'g', 'groupHeader')).toBeInTheDocument())
    expect(node('wf-x', 'g', 'groupHeader')).toBeInTheDocument()
    await waitFor(() => expect(node('wf-x', 'a2')!.dataset.state).toBe('running'))
    expect(node('wf-x', 'b1')!.dataset.state).toBe('running')
    expect(node('wf-x', 'a1')!.dataset.state).toBe('completed')
  })

  it("selecting the nested header opens the CHILD's panel, and the active list names its running nodes", async () => {
    renderQueue()
    const pane = await openLivePane('p-nest')

    await waitFor(() => expect(pane.querySelector('[data-layout-instance="wf-x"][data-definition-node="g"][data-type="groupHeader"]')).toBeInTheDocument())
    fireEvent.click(pane.querySelector('[data-layout-instance="wf-x"][data-definition-node="g"][data-type="groupHeader"]')!)
    const p = await within(pane).findByTestId('group-panel')
    expect(within(p).getByText('Fan out')).toBeInTheDocument()
    expect(within(p).getByTestId('group-task-row-a')).toBeInTheDocument()
    expect(activeItems(pane)).toContain('A2 · running · task a')
  })
})
