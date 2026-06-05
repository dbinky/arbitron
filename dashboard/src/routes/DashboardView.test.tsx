import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { DashboardStats, Me } from '../api/types'

// Capture the websocket handler so tests can fire synthetic events.
let wsHandler: ((event: { type: string; timestamp: string; data: unknown }) => void) | null = null
vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (onEvent: (e: { type: string; timestamp: string; data: unknown }) => void) => {
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

import DashboardView from './DashboardView'

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

describe('DashboardView', () => {
  it('renders queue-health and outcome tiles from the stats response', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)

    expect(await screen.findByText('70.0%')).toBeInTheDocument()       // success rate
    expect(screen.getByText('3')).toBeInTheDocument()                   // failures needing attention
    expect(screen.getByText('triage')).toBeInTheDocument()              // per-workflow row
  })

  it('shows no department selector for a single-department user', async () => {
    meMock.mockResolvedValue(me({ departments: ['engineering'] }))
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await screen.findByText('70.0%')
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('shows a union-default selector for a multi-department user and refetches on focus change', async () => {
    meMock.mockResolvedValue(me({ departments: ['engineering', 'sales'] }))
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await screen.findByText('70.0%')

    const select = screen.getByRole('combobox')
    expect(select).toHaveValue('__all__')
    // initial load used union default ⇒ null departments
    expect(statsMock).toHaveBeenCalledWith(null, '24h')

    fireEvent.change(select, { target: { value: 'sales' } })
    await waitFor(() => expect(statsMock).toHaveBeenCalledWith(['sales'], '24h'))
  })

  it('debounces a refetch when a relevant instance websocket event arrives', async () => {
    vi.useFakeTimers()
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(1))

    // Fire 3 events rapidly; only one refetch should follow after the debounce.
    wsHandler?.({ type: 'instance.completed', timestamp: '', data: {} })
    wsHandler?.({ type: 'instance.failed', timestamp: '', data: {} })
    wsHandler?.({ type: 'instance.created', timestamp: '', data: {} })

    vi.advanceTimersByTime(300)
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(2))
  })

  it('surfaces an error when stats loading fails', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockRejectedValue(new Error('boom'))
    render(<DashboardView />)
    expect(await screen.findByText(/boom/)).toBeInTheDocument()
  })

  // ---------------------------------------------------------------------------
  // FA #24 Pass 1: per-workflow table columns + throughput header
  // ---------------------------------------------------------------------------

  it('renders per-workflow table with runs and failure rate columns', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats({
      perWorkflow: [
        { workflowName: 'triage', runs: 10, failureRate: 0.3, avgDurationSeconds: 12 },
        { workflowName: 'onboard', runs: 5, failureRate: 0.0, avgDurationSeconds: 4 },
      ],
    }))
    render(<DashboardView />)
    await screen.findByText('triage')

    // Both workflow names appear
    expect(screen.getByText('onboard')).toBeInTheDocument()
    // Runs column values — '10' appears in both the outcome tile and the table;
    // use getAllByText and verify at least two occurrences (tile + table row).
    expect(screen.getAllByText('10').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('5')).toBeInTheDocument()   // onboard runs
    // Failure rate column (formatted) — 30.0% also appears in the outcomes tile;
    // verify at least two occurrences.
    expect(screen.getAllByText('30.0%').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('0.0%')).toBeInTheDocument()   // onboard failure rate
  })

  it('shows the throughput run total in the section heading', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats({
      throughput: [
        { bucket: '2026-06-04T10:00:00Z', runs: 3 },
        { bucket: '2026-06-04T11:00:00Z', runs: 7 },
      ],
    }))
    render(<DashboardView />)
    await screen.findByText(/10 runs/)  // throughputTotal(buckets) = 10
  })

  // ---------------------------------------------------------------------------
  // FA #24 Pass 2: robustness — empty throughput + zero data
  // ---------------------------------------------------------------------------

  it('renders without crashing when throughput is empty', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats({ throughput: [] }))
    render(<DashboardView />)
    await screen.findByText(/0 runs/)  // throughputTotal([]) = 0
    // No bars — section heading still present
    expect(screen.getByText(/Throughput/)).toBeInTheDocument()
  })

  it('renders without crashing when perWorkflow is empty', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats({ perWorkflow: [] }))
    render(<DashboardView />)
    await screen.findByText('70.0%')  // outcomes still render
    // Table headers present, no rows
    expect(screen.getByText('Workflow')).toBeInTheDocument()
  })
})
