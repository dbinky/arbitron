import { renderHook, waitFor, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { DashboardStats, Me, WebSocketEvent } from '../api/types'

// Capture the websocket handler so tests can fire synthetic events.
let wsHandler: ((event: WebSocketEvent) => void) | null = null
vi.mock('./useSystemWebSocket', () => ({
  useSystemWebSocket: (onEvent: (e: WebSocketEvent) => void) => {
    wsHandler = onEvent
    return { connected: true }
  },
}))

const meMock = vi.fn<() => Promise<Me>>()
const statsMock = vi.fn<(d: string[] | null, w: string) => Promise<DashboardStats>>()
vi.mock('../api/client', () => ({
  api: {
    me: () => meMock(),
    dashboard: { stats: (d: string[] | null, w: string) => statsMock(d, w) },
  },
}))

import { useDashboardStats } from './useDashboardStats'

function me(overrides: Partial<Me> = {}): Me {
  return { userId: 'u1', username: 'a', role: 'user', departments: ['engineering'], ledDepartments: [], isAdmin: false, ...overrides }
}

function stats(overrides: Partial<DashboardStats> = {}): DashboardStats {
  return {
    departments: ['engineering'],
    queue: { running: 2, queued: 4, waiting: 1, paused: 0, failedNeedingAttention: 3 },
    throughput: [{ bucket: '2026-06-04T10:00:00Z', runs: 5 }],
    outcomes: { totalRuns: 10, successRate: 0.7, failureRate: 0.3, avgDurationSeconds: 12, p95DurationSeconds: 30 },
    perWorkflow: [{ workflowName: 'triage', runs: 10, failureRate: 0.3, avgDurationSeconds: 12 }],
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  wsHandler = null
  vi.useRealTimers()
})

describe('useDashboardStats', () => {
  it('loads identity and stats, exposing them with loading resolved', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())

    const { result } = renderHook(() => useDashboardStats(null))

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.me?.username).toBe('a')
    expect(result.current.stats?.outcomes.successRate).toBe(0.7)
    expect(result.current.error).toBeNull()
    // initial load used the union default ⇒ null departments
    expect(statsMock).toHaveBeenCalledWith(null, '24h')
  })

  it('refetches with the new selection when it changes', async () => {
    meMock.mockResolvedValue(me({ departments: ['engineering', 'sales'] }))
    statsMock.mockResolvedValue(stats())

    const { result, rerender } = renderHook(({ sel }: { sel: string[] | null }) => useDashboardStats(sel), {
      initialProps: { sel: null as string[] | null },
    })
    await waitFor(() => expect(result.current.loading).toBe(false))

    rerender({ sel: ['sales'] })
    await waitFor(() => expect(statsMock).toHaveBeenCalledWith(['sales'], '24h'))
  })

  it('debounces a refetch when a relevant instance websocket event arrives', async () => {
    vi.useFakeTimers()
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())

    const { result } = renderHook(() => useDashboardStats(null))
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(1))

    act(() => {
      wsHandler?.({ type: 'instance.completed', timestamp: '', data: {} })
      wsHandler?.({ type: 'instance.failed', timestamp: '', data: {} })
      wsHandler?.({ type: 'instance.created', timestamp: '', data: {} })
    })

    await act(async () => { vi.advanceTimersByTime(300) })
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(2))
    expect(result.current).toBeTruthy()
  })

  it('throttles a sustained event stream to fire at the max-wait ceiling', async () => {
    vi.useFakeTimers()
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())

    renderHook(() => useDashboardStats(null))
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(1))

    // Fire an event every 200ms (< the 300ms debounce) so the plain debounce
    // would keep resetting and never fire. Over 2000ms (the max-wait), the
    // trailing-edge throttle must still fire exactly once.
    for (let elapsed = 0; elapsed < 2000; elapsed += 200) {
      act(() => { wsHandler?.({ type: 'instance.completed', timestamp: '', data: {} }) })
      await act(async () => { vi.advanceTimersByTime(200) })
    }
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(2))
  })

  it('ignores websocket events that are not state changes', async () => {
    vi.useFakeTimers()
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())

    renderHook(() => useDashboardStats(null))
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(1))

    act(() => { wsHandler?.({ type: 'system.heartbeat', timestamp: '', data: {} }) })
    await act(async () => { vi.advanceTimersByTime(300) })
    expect(statsMock).toHaveBeenCalledTimes(1)
  })

  it('surfaces an error when stats loading fails', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockRejectedValue(new Error('boom'))

    const { result } = renderHook(() => useDashboardStats(null))
    await waitFor(() => expect(result.current.error).toMatch(/boom/))
  })
})
