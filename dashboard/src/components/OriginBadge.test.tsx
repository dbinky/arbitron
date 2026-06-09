import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import OriginBadge from './OriginBadge'

describe('OriginBadge', () => {
  it('renders a "manual" label for manual runs', () => {
    render(<OriginBadge triggerSource="manual" />)
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
  })

  it('renders a "scheduled" label for schedule runs', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="sched-1" />)
    expect(screen.getByText(/scheduled/i)).toBeInTheDocument()
  })

  it('renders a "run now" label for run_now runs', () => {
    render(<OriginBadge triggerSource="run_now" scheduleId="sched-1" />)
    expect(screen.getByText(/run.?now/i)).toBeInTheDocument()
  })

  it('defaults a missing triggerSource to manual', () => {
    render(<OriginBadge />)
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
  })

  it('exposes the originating schedule id for schedule-originated runs', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="sched-42" />)
    // The schedule reference is surfaced (title attribute or visible text).
    expect(screen.getByTitle(/sched-42/)).toBeInTheDocument()
  })

  it('renders no schedule reference for manual runs', () => {
    render(<OriginBadge triggerSource="manual" />)
    expect(screen.queryByTitle(/sched/)).toBeNull()
  })

  it('renders NO mutation controls (read-only)', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="s1" />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
