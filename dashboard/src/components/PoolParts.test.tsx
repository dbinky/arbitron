import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { T0, health, iso, source } from '../api/runners.fixtures'
import DrainProgressView from './DrainProgressView'
import SourceChip from './SourceChip'

describe('SourceChip', () => {
  it('shows kind, family and a healthy status', () => {
    const { container } = render(<SourceChip source={source()} now={T0} />)
    const chip = container.querySelector('[data-source="builtin/anthropic_subscription/-"]')!
    expect(chip.getAttribute('data-status')).toBe('healthy')
    expect(chip).toHaveTextContent('Anthropic subscription · anthropic · healthy')
  })

  it('shows the countdown whenever capacity is exhausted, including while logged out', () => {
    const { container, rerender } = render(<SourceChip source={source({ health: health({ capacity: 'exhausted', resetAt: iso(65_000) }) })} now={T0} />)
    expect(container.firstElementChild).toHaveTextContent('exhausted · resets in 1m 5s')
    rerender(<SourceChip source={source({ health: health({ auth: 'logged_out', capacity: 'exhausted', resetAt: iso(65_000) }) })} now={T0 + 5_000} />)
    expect(container.firstElementChild).toHaveTextContent('logged out · resets in 1m 0s')
  })

  it('renders LunaRoute congestion as pressure with its ref', () => {
    const lr = source({ id: 'mac-a/lunaroute/lr', kind: 'lunaroute', credentialRef: 'lr', families: ['openai'], health: health({ congested: true }) })
    const { container } = render(<SourceChip source={lr} now={T0} />)
    expect(container.firstElementChild?.getAttribute('data-status')).toBe('pressure')
    expect(container.firstElementChild).toHaveTextContent('LunaRoute (lr) · openai · busy · pressure')
    expect(container.firstElementChild).not.toHaveTextContent('exhausted')
  })
})

describe('DrainProgressView', () => {
  const blocked = { kind: 'session', runId: 'run-4', group: 'anthropic_subscription:anthropic', reason: 'no runner with anthropic capacity' }

  it('renders nothing when the runner is not draining', () => {
    const { container } = render(<DrainProgressView draining={false} drain={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows remaining work and each blocking capacity with a run link', () => {
    render(
      <MemoryRouter>
        <DrainProgressView draining drain={{ activeSteps: 2, homedWorkspaces: 1, sessions: 1, complete: false, blocked: [blocked] }} />
      </MemoryRouter>,
    )
    expect(screen.getByText('Draining · 2 active steps · 1 homed workspace · 1 session')).toBeInTheDocument()
    const item = document.querySelector('[data-drain-blocker="session"]')!
    expect(item).toHaveTextContent('session blocked: no runner with anthropic capacity (anthropic_subscription:anthropic)')
    expect(screen.getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/run-4')
  })

  it('reports completion and an absent progress read explicitly', () => {
    const { rerender } = render(<DrainProgressView draining drain={{ activeSteps: 0, homedWorkspaces: 0, sessions: 0, complete: true, blocked: [] }} />)
    expect(screen.getByText('Drain complete')).toBeInTheDocument()
    rerender(<DrainProgressView draining drain={null} />)
    expect(screen.getByText('Draining · progress unavailable')).toBeInTheDocument()
  })

  it('a hidden blocker run renders without a link', () => {
    render(
      <MemoryRouter>
        <DrainProgressView draining drain={{ activeSteps: 0, homedWorkspaces: 1, sessions: 0, complete: false, blocked: [{ ...blocked, runId: null }] }} />
      </MemoryRouter>,
    )
    expect(screen.queryByRole('link')).toBeNull()
  })
})
