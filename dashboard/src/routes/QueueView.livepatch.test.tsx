import { render, screen, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WebSocketEvent, EnrichedInstanceSummary } from '../api/types'

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
}

const listMock = vi.fn(async ({ state }: { state: string }) =>
  state === 'running' ? [runningRow] : [],
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

  it('patches the row Step in place on node.started without refetching', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'inst-1',
        data: { nodeId: '10x-r1', nodeLabel: '10x R1', backend: 'claude-code' },
      })
    })

    await waitFor(() => expect(screen.getByText('10x R1')).toBeInTheDocument())
    expect(listMock.mock.calls.length).toBe(callsAfterLoad) // no refetch
  })

  it('ignores a node event for an instance not in the list', async () => {
    renderView()
    await waitFor(() => expect(screen.getByText('Architect R1')).toBeInTheDocument())
    const callsAfterLoad = listMock.mock.calls.length

    act(() => {
      capturedHandler?.({
        type: 'node.started',
        timestamp: '2024-01-01T00:01:00Z',
        instanceId: 'unknown-inst',
        data: { nodeId: 'x', nodeLabel: 'X', backend: '' },
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
