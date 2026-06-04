import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'
import type { InstanceSummary } from '../api/types'

function summary(overrides: Partial<InstanceSummary> = {}): InstanceSummary {
  return {
    id: 'i1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    ...overrides,
  }
}

function renderSection(props: Partial<React.ComponentProps<typeof QueueSection>>) {
  return render(
    <MemoryRouter>
      <QueueSection
        title="Failed"
        instances={[]}
        titleColor="red"
        countBg="red"
        countColor="red"
        timeMode="relative"
        {...props}
      />
    </MemoryRouter>,
  )
}

describe('QueueSection dismiss-all', () => {
  it('renders [Dismiss All] when onDismissAll is provided', () => {
    const onDismissAll = vi.fn()
    renderSection({ onDismissAll })
    const btn = screen.getByRole('button', { name: 'Dismiss All' })
    fireEvent.click(btn)
    expect(onDismissAll).toHaveBeenCalled()
  })

  it('does not render [Dismiss All] when onDismissAll is absent (Active section)', () => {
    renderSection({ title: 'Active' })
    expect(screen.queryByRole('button', { name: /dismiss all/i })).toBeNull()
  })
})

describe('QueueSection — dismissed row rendering', () => {
  it('renders a restore control (not dismiss) for a dismissed terminal instance', () => {
    const onRestore = vi.fn()
    renderSection({
      instances: [summary({ id: 'i1', state: 'completed', dismissed: true })],
      onDismiss: vi.fn(),
      onRestore,
    })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    const btn = screen.getByRole('button', { name: /restore/i })
    fireEvent.click(btn)
    expect(onRestore).toHaveBeenCalledWith('i1')
  })

  it('renders a dismiss control for a non-dismissed terminal instance', () => {
    const onDismiss = vi.fn()
    renderSection({
      instances: [summary({ id: 'i2', state: 'failed', dismissed: false })],
      onDismiss,
      onRestore: vi.fn(),
    })
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
    const btn = screen.getByRole('button', { name: /dismiss/i })
    fireEvent.click(btn)
    expect(onDismiss).toHaveBeenCalledWith('i2')
  })

  it('does not render a dismiss or restore control for active (running) instances', () => {
    renderSection({
      instances: [summary({ state: 'running' })],
      onDismiss: vi.fn(),
      onRestore: vi.fn(),
    })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })
})
