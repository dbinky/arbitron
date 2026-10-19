import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { CHILD_PAGE_LIMIT } from '../hooks/useRunHierarchy'
import type { WebSocketEvent } from '../api/types'
import { HIDDEN_ID, independentAggregate, repeatedChildAggregate, summary } from '../graph/liveSummary.fixtures'
import { QUEUE_POLL_MS } from './queueRefresh'
import { graphStateFrom, listFrom, localAdmin, newWorld, type QueueWorld } from './queueWorld.fixtures'

let systemHandler: ((e: WebSocketEvent) => void) | null = null
vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    systemHandler = cb
    return { connected: true }
  },
}))
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
  world = newWorld()
  systemHandler = null
  apiMock.me.mockResolvedValue(localAdmin)
  apiMock.instances.list.mockImplementation((p) => listFrom(world)(p))
  apiMock.instances.graphState.mockImplementation((id: string) => graphStateFrom(world)(id))
  apiMock.instances.getNodeTrace.mockRejectedValue(new Error('API 404: not found'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

function renderQueue() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

const rowEl = (id: string) => document.querySelector<HTMLElement>(`tr[data-row-id="${id}"]`)
const cellOf = (id: string, col: string) => rowEl(id)?.querySelector(`td[data-col="${col}"]`)?.textContent
const summaryOf = (id: string) => within(rowEl(id)!).queryByTestId('hierarchy-active-summary')?.textContent ?? null

async function expandChildren(runName: string, firstChildId: string) {
  fireEvent.click(await screen.findByRole('button', { name: `Show child runs of ${runName}` }))
  await waitFor(() => expect(rowEl(firstChildId)).not.toBeNull())
}

describe('QueueView hierarchy — every fixed task is inspectable beyond the 100-row page (P4-T10)', () => {
  it('the 101st fixed task is reachable through load-more', async () => {
    world.byState.waiting = [summary({ id: 'p-big', runName: 'Big fan-out', state: 'waiting', childCount: 150 })]
    world.children['p-big'] = Array.from({ length: 150 }, (_, i) =>
      summary({ id: `c-${String(i).padStart(3, '0')}`, runName: `Task ${i}`, parentInstanceId: 'p-big', state: 'queued' }),
    )
    renderQueue()

    await expandChildren('Big fan-out', 'c-000')
    expect(rowEl('c-099')).not.toBeNull()
    expect(rowEl('c-100')).toBeNull()
    expect(screen.getByText('Showing 100 of 150 child runs')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Load more child runs' }))

    await waitFor(() => expect(rowEl('c-100')).not.toBeNull())
    expect(rowEl('c-149')).not.toBeNull()
    expect(rowEl('c-100')!.getAttribute('data-depth')).toBe('1')
    expect(apiMock.instances.list).toHaveBeenCalledWith({ parent: 'p-big', limit: CHILD_PAGE_LIMIT, offset: 100, includeDismissed: true })
    expect(screen.queryByRole('button', { name: 'Load more child runs' })).toBeNull()
    expect(screen.queryByText(/Showing \d+ of 150 child runs/)).toBeNull()
  })
})

describe('QueueView hierarchy — markers from the authorized aggregate (P4-T10)', () => {
  // Edge: duplicate node ids across repeated child workflows, paused tasks, redaction.
  it('repeated child workflows with the SAME node id each show their own state; paused work is summarised; a redacted task never renders', async () => {
    world.byState.waiting = [summary({ id: 'p-wf', runName: 'Repeat parent', state: 'waiting', childCount: 3 })]
    world.children['p-wf'] = [
      summary({ id: 'w1', runName: 'Task x', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'running' }),
      summary({ id: 'w2', runName: 'Task y', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'waiting' }),
      summary({ id: 'w3', runName: 'Task z', workflowName: 'child-wf', parentInstanceId: 'p-wf', state: 'paused' }),
    ]
    world.graphStates['p-wf'] = repeatedChildAggregate()
    renderQueue()

    await expandChildren('Repeat parent', 'w1')

    await waitFor(() => expect(summaryOf('w1')).toBe('running: Build'))
    expect(summaryOf('w2')).toBe('waiting: Build')
    expect(summaryOf('w3')).toBe('paused')
    expect(within(rowEl('w1')!).getByText('workflow task')).toBeInTheDocument()
    expect(within(rowEl('w1')!).getByText('Fan out · x')).toBeInTheDocument()
    expect(within(rowEl('w2')!).getByText('Fan out · y')).toBeInTheDocument()
    expect(within(rowEl('w3')!).getByText('Fan out · z')).toBeInTheDocument()
    expect(cellOf('w3', 'status')).toBe('paused')
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-wf')
    expect(screen.queryByText('Fan out · q')).toBeNull()
    expect(document.body.textContent).not.toContain(HIDDEN_ID)
  })

  it('a level whose aggregate is unreadable stays listed and unannotated', async () => {
    world.byState.waiting = [summary({ id: 'p-wf', runName: 'Repeat parent', state: 'waiting', childCount: 1 })]
    world.children['p-wf'] = [summary({ id: 'w1', runName: 'Task x', parentInstanceId: 'p-wf' })]
    world.graphStates['p-wf'] = new Error('API 500: aggregate down')
    renderQueue()

    await expandChildren('Repeat parent', 'w1')

    await waitFor(() => expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-wf'))
    expect(within(rowEl('w1')!).queryByTestId('hierarchy-markers')).toBeNull()
    expect(screen.queryByText(/aggregate down/)).toBeNull()
  })
})

describe('QueueView — completed ancestors that own active independent work stay discoverable (P4-T10)', () => {
  function seedPagedAncestors() {
    // Keep the page full of distinct, valid ancestors, but nest its terminal
    // intermediates under one root, as in the real-wire test. Pagination/error
    // coverage needs only the first and final roots on screen.
    world.ancestors = Array.from({ length: 501 }, (_, i) =>
      summary({
        id: `ancestor-${i}`, runName: `Ancestor ${i}`, state: 'completed',
        childCount: i === 0 ? 499 : 1,
        parentInstanceId: i > 0 && i < 500 ? 'ancestor-0' : undefined,
      }),
    )
    world.children['ancestor-0'] = world.ancestors.slice(1, 500)
  }

  it('discovers the 501st ancestor beyond the server cap', async () => {
    seedPagedAncestors()
    renderQueue()

    await waitFor(() => expect(rowEl('ancestor-500')).not.toBeNull())
    expect(screen.getByText('Active', { selector: 'span' }).parentElement).toHaveTextContent('501')
    expect(within(rowEl('ancestor-500')!).getByText('owns active work')).toBeInTheDocument()
    expect(screen.queryByTestId('ancestor-warning')).toBeNull()
  })

  it('keeps discovered rows and ordinary work when a later page fails, then retries discovery', async () => {
    seedPagedAncestors()
    world.byState.running = [summary({ id: 'live', runName: 'Live work', state: 'running' })]
    let failSecondPage = true
    apiMock.instances.list.mockImplementation((p) => {
      if (p?.activeDescendantAncestors && p.offset === 500 && failSecondPage) {
        return Promise.reject(new Error('API 503: later page unavailable'))
      }
      return listFrom(world)(p)
    })
    renderQueue()

    const warning = await screen.findByTestId('ancestor-warning')
    expect(warning).toHaveTextContent('later page unavailable')
    expect(screen.getByText('Active', { selector: 'span' }).parentElement).toHaveTextContent('501')
    expect(rowEl('live')).not.toBeNull()
    expect(rowEl('ancestor-0')).not.toBeNull()
    expect(rowEl('ancestor-500')).toBeNull()
    failSecondPage = false
    fireEvent.click(within(warning).getByRole('button', { name: 'Retry ancestor discovery' }))
    await waitFor(() => expect(rowEl('ancestor-500')).not.toBeNull())
    expect(screen.getByText('Active', { selector: 'span' }).parentElement).toHaveTextContent('502')
    expect(screen.queryByTestId('ancestor-warning')).toBeNull()
  })

  it.each(['completed', 'failed'] as const)('lists a recent %s ancestor only in Active, returning it to history when discovery stops', async (state) => {
    const parent = summary({ id: 'p-recent', runName: 'Recent parent', state, childCount: 1 })
    world.byState[state] = [parent]
    world.byState.running = [summary({ id: 'c-live', runName: 'Live child', parentInstanceId: parent.id, state: 'running' })]
    world.ancestors = [parent]
    renderQueue()

    await waitFor(() => expect(rowEl(parent.id)).not.toBeNull())
    // Completed starts collapsed: open it so a duplicate cannot hide there.
    if (state === 'completed') fireEvent.click(screen.getByText('Completed', { selector: 'span' }))
    const section = (title: string) => screen.getByText(title, { selector: 'span' }).parentElement!.parentElement!
    const rows = () => document.querySelectorAll(`tr[data-row-id="${parent.id}"]`)
    const historyTitle = state === 'completed' ? 'Completed' : 'Failed'
    expect(rows()).toHaveLength(1)
    expect(section('Active').contains(rowEl(parent.id))).toBe(true)
    expect(section(historyTitle).querySelector('tr[data-row-id]')).toBeNull()
    expect(within(rowEl(parent.id)!).getByText('owns active work')).toBeInTheDocument()

    // A failed discovery cannot suppress the ordinary history response.
    world.ancestors = new Error('API 500: discovery unavailable')
    act(() => systemHandler!({ type: 'group.task_updated', timestamp: '', instanceId: parent.id, data: {} }))
    await screen.findByTestId('ancestor-warning')
    expect(rows()).toHaveLength(1)
    expect(section(historyTitle).contains(rowEl(parent.id))).toBe(true)
    expect(rowEl('c-live')).not.toBeNull()
    expect(within(rowEl(parent.id)!).queryByText('owns active work')).toBeNull()

    // Successful discovery with no remaining active work also keeps history.
    world.ancestors = []
    world.byState.running = []
    act(() => systemHandler!({ type: 'group.completed', timestamp: '', instanceId: parent.id, data: {} }))
    await waitFor(() => expect(screen.queryByTestId('ancestor-warning')).toBeNull())
    expect(rows()).toHaveLength(1)
    expect(section(historyTitle).contains(rowEl(parent.id))).toBe(true)
    expect(section('Active').querySelector('tr[data-row-id]')).toBeNull()
  })

  // Edge: independent work after parent completion stays visible and live in
  // the queue. The completed parent is outside the Completed window/dismissed
  // (world.byState.completed is empty), so only ancestor discovery lists it.
  it('a completed parent with an active independent branch is listed, and the branch nests under it (non-root)', async () => {
    world.byState.running = [summary({ id: 'c-ind', runName: 'Independent task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'running' })]
    world.ancestors = [summary({ id: 'p-done', runName: 'Done parent', workflowName: 'fanout', state: 'completed', childCount: 2, completedAt: '2026-10-16T10:05:00Z' })]
    world.children['p-done'] = [
      summary({ id: 'c-ind', runName: 'Independent task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'running' }),
      summary({ id: 'c-win', runName: 'Winner task', workflowName: 'fanout', parentInstanceId: 'p-done', state: 'completed' }),
    ]
    world.graphStates['p-done'] = independentAggregate()
    renderQueue()

    await waitFor(() => expect(rowEl('p-done')).not.toBeNull())
    expect(apiMock.instances.list).toHaveBeenCalledWith({ activeDescendantAncestors: true, includeDismissed: true, limit: 500, offset: 0 })
    expect(cellOf('p-done', 'status')).toBe('completed')
    expect(within(rowEl('p-done')!).getByText('owns active work')).toBeInTheDocument()
    // Non-root: the branch is not a top-level row; it is reached from its parent.
    expect(rowEl('c-ind')).toBeNull()

    await expandChildren('Done parent', 'c-ind')

    const ind = rowEl('c-ind')!
    expect(ind.getAttribute('data-depth')).toBe('1')
    expect(ind.getAttribute('data-independent')).toBe('true')
    expect(within(ind).getByText('branch')).toBeInTheDocument()
    expect(within(ind).getByText('Fan out · a')).toBeInTheDocument()
    expect(within(ind).getByText('non-blocking')).toBeInTheDocument()
    await waitFor(() => expect(summaryOf('c-ind')).toBe('running: A2'))
    const win = rowEl('c-win')!
    expect(within(win).getByText('Fan out · b')).toBeInTheDocument()
    expect(within(win).queryByText('non-blocking')).toBeNull()
    expect(win.getAttribute('data-independent')).toBeNull()
    expect(summaryOf('c-win')).toBeNull()
    expect(apiMock.instances.graphState).toHaveBeenCalledWith('p-done')
  })

  // Error: discovery failing must not take the queue down; the active child is
  // never lost (with no parent row it stays at top level).
  it('an ancestor-discovery failure is reported without breaking the queue', async () => {
    world.byState.running = [summary({ id: 'c-ind', runName: 'Independent task', parentInstanceId: 'p-done', state: 'running' })]
    world.ancestors = new Error('API 500: ancestors down')
    renderQueue()

    expect(await screen.findByTestId('ancestor-warning')).toHaveTextContent(
      "Couldn't load completed runs that still own active work: API 500: ancestors down",
    )
    expect(rowEl('c-ind')).not.toBeNull()
    expect(screen.queryByText(/Failed to load instances/)).toBeNull()
  })
})

describe('QueueView — open hierarchy levels refresh (stale cache, P4-T10)', () => {
  function seedOpenLevel() {
    world.byState.waiting = [summary({ id: 'p-run', runName: 'Running parent', state: 'waiting', childCount: 1 })]
    world.children['p-run'] = [summary({ id: 'c1', runName: 'Child one', parentInstanceId: 'p-run', state: 'running' })]
  }
  function finishChild() {
    world.children['p-run'] = [
      summary({ id: 'c1', runName: 'Child one', parentInstanceId: 'p-run', state: 'completed', completedAt: '2026-10-16T10:02:00Z' }),
    ]
  }

  it('an open level refreshes on a group lifecycle event and stays expanded', async () => {
    seedOpenLevel()
    renderQueue()
    await expandChildren('Running parent', 'c1')
    expect(cellOf('c1', 'status')).toBe('running')

    finishChild()
    act(() => systemHandler!({ type: 'group.task_updated', timestamp: '2026-10-16T10:02:00Z', instanceId: 'p-run', data: {} }))

    await waitFor(() => expect(cellOf('c1', 'status')).toBe('completed'))
    expect(screen.getByRole('button', { name: 'Hide child runs of Running parent' })).toBeInTheDocument()
  })

  // Error: the final event was dropped (no event at all) — bounded polling recovers it.
  it('bounded polling refreshes an open level when the final event was dropped', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    seedOpenLevel()
    renderQueue()
    await expandChildren('Running parent', 'c1')
    expect(cellOf('c1', 'status')).toBe('running')

    finishChild()
    const before = apiMock.instances.list.mock.calls.length
    act(() => {
      vi.advanceTimersByTime(QUEUE_POLL_MS)
    })

    await waitFor(() => expect(cellOf('c1', 'status')).toBe('completed'))
    expect(apiMock.instances.list.mock.calls.length).toBeGreaterThan(before)
  })

  it('does not poll while nothing is active or pending', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    world.byState.completed = [summary({ id: 'old', runName: 'Old run', state: 'completed', completedAt: '2026-10-16T09:00:00Z' })]
    renderQueue()
    await screen.findByRole('button', { name: /show dismissed/i })
    const before = apiMock.instances.list.mock.calls.length

    act(() => {
      vi.advanceTimersByTime(QUEUE_POLL_MS * 3)
    })

    expect(apiMock.instances.list.mock.calls.length).toBe(before)
  })
})
