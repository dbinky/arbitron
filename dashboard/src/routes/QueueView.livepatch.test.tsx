import { render, screen, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WebSocketEvent, EnrichedInstanceSummary, NodeEventData } from '../api/types'

let capturedHandler: ((e: WebSocketEvent) => void) | null = null

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    capturedHandler = cb
    return { connected: true }
  },
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

const runningRow: EnrichedInstanceSummary = {
  id: 'inst-1',
  ownerId: 'u1',
  workflowName: 'pr-review',
  state: 'running',
  priority: 'normal',
  createdAt: '2024-01-01T00:00:00Z',
  updatedAt: '2024-01-01T00:00:00Z',
  runName: 'run — pr-review',
  retryCount: 0,
  friendlyStep: 'Architect R1',
  currentNodeId: 'architect-r1',
  // backend 'codex' is set so we can assert a partial node event (no binding
  // fields) leaves it untouched rather than wiping it.
  backend: 'codex',
}

const terminalRow: EnrichedInstanceSummary = {
  id: 'inst-2',
  ownerId: 'u1',
  workflowName: 'pr-review',
  state: 'failed',
  priority: 'normal',
  createdAt: '2024-01-01T00:00:00Z',
  updatedAt: '2024-01-01T00:03:00Z',
  completedAt: '2024-01-01T00:03:00Z',
  runName: 'done — pr-review',
  retryCount: 0,
}

const listMock = vi.fn(async ({ state }: { state: string }) =>
  state === 'running' ? [runningRow] : state === 'failed' ? [terminalRow] : [],
)

vi.mock('../api/client', () => ({
  api: {
    instances: { list: (p: { state: string }) => listMock(p), getNodeTrace: vi.fn() },
    me: vi.fn(async () => ({ userId: 'u1', authMode: 'local' })),
  },
}))

import QueueView from './QueueView'

function renderView() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

describe('QueueView live row patch', () => {
  beforeEach(() => {
    capturedHandler = null
    listMock.mockClear()
  })

  it('node.started without binding fields keeps prior values', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    // Seed row has backend 'codex'; modelBackend cell renders it initially
    // (balanced density shows modelBackend; no model set so the cell shows just the backend).
    expect(screen.getByText('codex')).toBeInTheDocument()
    const callsAfterLoad = listMock.mock.calls.length

    const data: NodeEventData = { nodeId: '10x-r1', nodeLabel: '10x R1' }
    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'inst-1',
        data,
      })
    })

    // friendlyStep flips to the new label.
    await waitFor(() => expect(screen.getByText('10x R1')).toBeInTheDocument())
    expect(screen.queryByText('Architect R1')).not.toBeInTheDocument()
    // The event carried no binding fields (e.g. an older server), so the seed
    // value 'codex' is kept rather than wiped to undefined.
    expect(screen.getByText('codex')).toBeInTheDocument()
    // currentNodeId ('10x-r1') is patched in component state alongside friendlyStep,
    // but it has no dedicated rendered cell in the queue table; asserting it via the
    // DOM is not possible without adding new rendering. The omission is intentional.
    expect(listMock.mock.calls.length).toBe(callsAfterLoad) // no refetch
  })

  it('node.started with a full payload patches step, binding and git in one go', async () => {
    const { container } = renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    const data: NodeEventData = {
      nodeId: '10x-r1',
      nodeLabel: '10x R1',
      nodeType: 'agent_call',
      model: 'gpt-6.1-sol',
      effort: 'high',
      backend: 'codex',
      provider: 'litellm',
      gitRef: 'feat/live',
    }
    act(() => {
      capturedHandler?.({ type: 'node.started', timestamp: '2024-01-01T00:01:00Z', instanceId: 'inst-1', data })
    })

    await waitFor(() => expect(screen.getByText('gpt-6.1-sol (high) · codex · litellm')).toBeInTheDocument())
    expect(screen.getByText('10x R1')).toBeInTheDocument()
    const row = container.querySelector('tr[data-row-id="inst-1"]') as HTMLElement
    expect(row.querySelector('td[data-col="git"]')?.textContent).toBe('feat/live')
    expect(listMock.mock.calls.length).toBe(callsAfterLoad)
  })

  it('a script node clears the model cell to its type', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('codex')).toBeInTheDocument())
    const data: NodeEventData = { nodeId: 'init', nodeLabel: 'Init', nodeType: 'script', model: '', effort: '', backend: '', provider: '', gitRef: '' }
    act(() => {
      capturedHandler?.({ type: 'node.started', timestamp: '2024-01-01T00:01:00Z', instanceId: 'inst-1', data })
    })
    await waitFor(() => expect(screen.getByText('script')).toBeInTheDocument())
    expect(screen.queryByText('codex')).not.toBeInTheDocument()
  })

  it('node.failed patches the step only, never the binding', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('codex')).toBeInTheDocument())
    // node.failed's legacy backend field ignores the profile; it must not clobber the row.
    const data: NodeEventData = { nodeId: 'architect-r1', nodeLabel: 'Architect R1 (failed)', backend: 'claude-code' }
    act(() => {
      capturedHandler?.({ type: 'node.failed', timestamp: '2024-01-01T00:01:00Z', instanceId: 'inst-1', data })
    })
    await waitFor(() => expect(screen.getByText('Architect R1 (failed)')).toBeInTheDocument())
    expect(screen.getByText('codex')).toBeInTheDocument()
    expect(screen.queryByText('claude-code')).not.toBeInTheDocument()
  })

  it('ignores a node event for an instance not in the list', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    const data: NodeEventData = { nodeId: 'x', nodeLabel: 'X' }
    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'unknown-inst',
        data,
      })
    })

    expect(screen.getByText('Architect R1')).toBeInTheDocument()
    expect(listMock.mock.calls.length).toBe(callsAfterLoad)
  })

  it('still refetches on a lifecycle event', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'instance.completed',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'inst-1',
        data: {},
      })
    })

    await waitFor(() => expect(listMock.mock.calls.length).toBeGreaterThan(callsAfterLoad))
  })
})

describe('QueueView clock', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('advances a running row and leaves a terminal row frozen, with no events', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(new Date('2024-01-01T00:10:00Z'))
    const { container } = renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())

    const elapsedOf = (id: string) =>
      container.querySelector(`tr[data-row-id="${id}"] td[data-col="elapsed"]`)?.textContent
    expect(elapsedOf('inst-1')).toBe('10m 0s')
    expect(elapsedOf('inst-2')).toBe('3m 0s')

    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(elapsedOf('inst-1')).toBe('10m 5s')
    expect(elapsedOf('inst-2')).toBe('3m 0s')
  })
})
