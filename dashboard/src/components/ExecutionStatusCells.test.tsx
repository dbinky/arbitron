import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { T0, execStatus, iso } from '../api/runners.fixtures'
import ExecutionStatusBadge from './ExecutionStatusBadge'
import RunnerCell from './RunnerCell'

describe('RunnerCell', () => {
  it('shows the chat runner distinct from the workspace home', () => {
    const { container } = render(<RunnerCell execution={[execStatus({ runnerId: 'mac-b', homeRunnerId: 'mac-a' })]} />)
    expect(container.querySelector('[data-exec-runner="mac-b"]')).toHaveTextContent('mac-b')
    expect(container.querySelector('[data-home-runner="mac-a"]')).toHaveTextContent('home mac-a')
  })

  it('keeps simultaneous assignments distinct and labels each with its node', () => {
    const { container } = render(
      <RunnerCell execution={[execStatus({ jobId: 'j1', nodeId: 'lint', runnerId: 'mac-a' }), execStatus({ jobId: 'j2', nodeId: 'test', runnerId: 'mac-b' })]} />,
    )
    const runners = [...container.querySelectorAll('[data-exec-runner]')].map((e) => e.textContent)
    expect(runners).toEqual(['mac-a (lint)', 'mac-b (test)'])
  })

  it('shows the interactive pin and unassigned waits', () => {
    const { container } = render(<RunnerCell execution={[execStatus({ state: 'waiting_capacity', runnerId: undefined, sessionPinRunnerId: 'mac-c' })]} />)
    expect(container.querySelector('[data-exec-runner=""]')).toHaveTextContent('unassigned')
    expect(container.querySelector('[data-pin-runner="mac-c"]')).toHaveTextContent('pinned mac-c')
  })

  it('renders a dash without execution data', () => {
    const { container } = render(<RunnerCell execution={undefined} />)
    expect(container).toHaveTextContent('—')
  })
})

describe('ExecutionStatusBadge', () => {
  it('renders the capacity wait with reason and countdown', () => {
    render(<ExecutionStatusBadge status={execStatus({ state: 'waiting_capacity', reason: 'all anthropic subscriptions exhausted', nextReset: iso(65_000) })} now={T0} showNode={false} />)
    const badge = screen.getByText(/waiting · capacity/)
    expect(badge.getAttribute('data-exec-badge')).toBe('waiting_capacity')
    expect(badge).toHaveTextContent('all anthropic subscriptions exhausted · model claude-opus-4-1 · resets in 1m 5s')
  })

  it('names the node when several operations share a row', () => {
    render(<ExecutionStatusBadge status={execStatus({ nodeId: 'test', state: 'queued_slot' })} now={T0} showNode />)
    expect(screen.getByText('test: queued · slot')).toBeInTheDocument()
  })

  it('renders nothing for a running operation', () => {
    const { container } = render(<ExecutionStatusBadge status={execStatus()} now={T0} showNode={false} />)
    expect(container).toBeEmptyDOMElement()
  })
})
