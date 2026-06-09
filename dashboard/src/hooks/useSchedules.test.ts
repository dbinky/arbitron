import { renderHook, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useSchedules } from './useSchedules'
import { api } from '../api/client'
import type { Schedule } from '../api/types'

function sched(overrides: Partial<Schedule> = {}): Schedule {
  return {
    id: 's1',
    workflowName: 'wf-a',
    cronExpr: '0 9 * * *',
    timezone: 'America/New_York',
    creatorId: 'user-1',
    department: 'eng',
    enabled: true,
    lastFiredAt: '2026-06-01T09:00:00Z',
    createdAt: '2026-05-01T00:00:00Z',
    updatedAt: '2026-05-01T00:00:00Z',
    ...overrides,
  }
}

describe('useSchedules', () => {
  beforeEach(() => {
    vi.spyOn(api.schedules, 'list')
  })
  afterEach(() => vi.restoreAllMocks())

  it('starts in a loading state and resolves to the fetched schedules', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched()])
    const { result } = renderHook(() => useSchedules('wf-a'))

    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.schedules).toHaveLength(1)
    expect(result.current.error).toBeNull()
  })

  it('scopes the fetch to the given workflow', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    renderHook(() => useSchedules('wf-b'))
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-b'))
  })

  it('re-fetches when the workflow name changes', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    const { rerender } = renderHook(({ w }: { w: string }) => useSchedules(w), {
      initialProps: { w: 'wf-a' },
    })
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-a'))
    rerender({ w: 'wf-b' })
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-b'))
  })

  it('degrades to an error state on a failed fetch', async () => {
    vi.mocked(api.schedules.list).mockRejectedValue(new Error('boom'))
    const { result } = renderHook(() => useSchedules('wf-a'))
    await waitFor(() => expect(result.current.error).toBe('Failed to load schedules'))
    expect(result.current.schedules).toHaveLength(0)
  })

  it('ignores a resolved fetch after the workflow changed (cancel-flag semantics)', async () => {
    // First call hangs; the prop changes before it resolves. The stale resolution
    // must not clobber the newer scope's state.
    let resolveFirst: (v: Schedule[]) => void = () => {}
    const first = new Promise<Schedule[]>((res) => { resolveFirst = res })
    vi.mocked(api.schedules.list)
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce([sched({ id: 's-second' })])

    const { result, rerender } = renderHook(({ w }: { w: string }) => useSchedules(w), {
      initialProps: { w: 'wf-a' },
    })
    rerender({ w: 'wf-b' }) // second fetch starts; first is now stale
    await waitFor(() => expect(result.current.schedules).toEqual([sched({ id: 's-second' })]))

    // Resolve the stale first fetch — it must be ignored.
    resolveFirst([sched({ id: 's-first' })])
    await Promise.resolve()
    expect(result.current.schedules).toEqual([sched({ id: 's-second' })])
  })
})
