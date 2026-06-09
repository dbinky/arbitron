import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import SchedulesPanel from './SchedulesPanel'
import { api } from '../api/client'
import { relativeTime } from '../utils/time'
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

describe('SchedulesPanel', () => {
  beforeEach(() => {
    vi.spyOn(api.schedules, 'list')
  })
  afterEach(() => vi.restoreAllMocks())

  it('renders each schedule with cron, timezone, creator, enabled state, and last-fired', async () => {
    const fired = '2026-06-01T09:00:00Z'
    vi.mocked(api.schedules.list).mockResolvedValue([sched({ lastFiredAt: fired })])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText('0 9 * * *')).toBeInTheDocument())
    expect(screen.getByText('America/New_York')).toBeInTheDocument()
    expect(screen.getByText(/user-1/)).toBeInTheDocument()
    expect(screen.getByText(/enabled/i)).toBeInTheDocument()
    // last-fired is rendered as a human-readable relative time, not the raw ISO.
    expect(screen.getByText(relativeTime(fired))).toBeInTheDocument()
  })

  it('labels the creator column "Creator ID" (it carries an id, not a display name)', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched()])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText('0 9 * * *')).toBeInTheDocument())
    expect(screen.getByRole('columnheader', { name: 'Creator ID' })).toBeInTheDocument()
  })

  it('renders a disabled label for disabled schedules', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched({ enabled: false })])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/disabled/i)).toBeInTheDocument())
  })

  it('shows a "never fired" indicator when lastFiredAt is null', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched({ lastFiredAt: null })])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/never/i)).toBeInTheDocument())
  })

  it('shows an empty state when the workflow has no schedules', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/no schedules/i)).toBeInTheDocument())
  })

  it('degrades to an error state when the fetch fails, not breaking the view', async () => {
    vi.mocked(api.schedules.list).mockRejectedValue(new Error('boom'))
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/failed to load schedules/i)).toBeInTheDocument())
  })

  it('fetches schedules scoped to the given workflow', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    render(<SchedulesPanel workflowName="wf-b" />)
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-b'))
  })

  it('re-fetches when workflowName prop changes', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    const { rerender } = render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-a'))

    rerender(<SchedulesPanel workflowName="wf-b" />)
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-b'))
  })

  it('exposes NO create/edit/enable/disable/delete/run control (read-only)', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched()])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText('0 9 * * *')).toBeInTheDocument())
    expect(screen.queryByRole('button')).toBeNull()
  })
})
