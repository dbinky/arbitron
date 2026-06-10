import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'
import type { DashboardStats, Me } from './types'

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('api.me', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  it('GETs /api/v1/me and returns the projection', async () => {
    const me: Me = {
      userId: 'u1', username: 'alice', role: 'user',
      departments: ['engineering'], ledDepartments: [], isAdmin: false, authMode: 'local' as const,
    }
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(me))
    const result = await api.me()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/me', undefined)
    expect(result).toEqual(me)
  })
})

describe('api.dashboard.stats', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  const empty: DashboardStats = {
    departments: [],
    queue: { running: 0, queued: 0, waiting: 0, paused: 0, failedNeedingAttention: 0 },
    throughput: [], outcomes: { totalRuns: 0, successRate: 0, failureRate: 0, avgDurationSeconds: 0, p95DurationSeconds: 0 },
    perWorkflow: [],
  }

  it('omits the departments param when departments is null (union default)', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(empty))
    await api.dashboard.stats(null, '24h')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/dashboard/stats?window=24h', undefined)
  })

  it('joins departments with commas and includes the window', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse(empty))
    await api.dashboard.stats(['engineering', 'sales'], '6h')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/dashboard/stats?departments=engineering%2Csales&window=6h',
      undefined,
    )
  })

  it('returns the parsed envelope', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse({ ...empty, departments: ['engineering'] }))
    const result = await api.dashboard.stats(['engineering'], '24h')
    expect(result.departments).toEqual(['engineering'])
  })
})

// ---------------------------------------------------------------------------
// FA #23 Pass 1: error and edge cases
// ---------------------------------------------------------------------------

describe('api.dashboard.stats — failure/error/edge', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  it('throws on non-2xx API response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ message: 'Unauthorized' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await expect(api.dashboard.stats(null, '24h')).rejects.toThrow('401')
  })

  it('omits departments param when passed an empty array (same as null — union default)', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      jsonResponse({ departments: [], queue: { running: 0, queued: 0, waiting: 0, paused: 0, failedNeedingAttention: 0 }, throughput: [], outcomes: { totalRuns: 0, successRate: 0, failureRate: 0, avgDurationSeconds: 0, p95DurationSeconds: 0 }, perWorkflow: [] }),
    )
    await api.dashboard.stats([], '24h')
    // Empty array → length === 0 → no departments param, only window
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/dashboard/stats?window=24h', undefined)
  })
})

// ---------------------------------------------------------------------------
// FA #23 Pass 2: api.me error robustness
// ---------------------------------------------------------------------------

describe('api.me — failure/error', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  it('throws on non-2xx from /api/v1/me', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ message: 'Forbidden' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await expect(api.me()).rejects.toThrow('403')
  })

  it('uses the server message when the response body includes one', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ message: 'session expired' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await expect(api.me()).rejects.toThrow('session expired')
  })
})
