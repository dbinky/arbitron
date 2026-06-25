import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'
import type { InstanceSummary } from '../api/types'

function summary(overrides: Partial<InstanceSummary> = {}): InstanceSummary {
  return {
    id: 'i1',
    ownerId: 'u1',
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

// Seam: verifies that sortInstances does NOT segregate dismissed items to the
// bottom (or top) of the list. If someone inserted a dismissed-first/last key
// into the sort comparator, a dismissed item with a more recent updatedAt
// would no longer appear before the older non-dismissed item.
describe('QueueSection — dismissed items interleaved in sort order', () => {
  it('a dismissed item with a more recent updatedAt appears before an older non-dismissed item when sorted by time', () => {
    const older = summary({ id: 'old', state: 'failed', dismissed: false, updatedAt: '2024-01-01T00:00:00Z' })
    const newer = summary({ id: 'new', state: 'failed', dismissed: true,  updatedAt: '2024-06-01T00:00:00Z' })

    renderSection({
      instances: [older, newer],  // older first in input
      timeMode: 'relative',
      onDismiss: vi.fn(),
      onRestore: vi.fn(),
    })

    // Change sort to "Time" (most-recently-updated first for relative mode).
    fireEvent.click(screen.getByRole('button', { name: /^Time$/i }))

    // Both rows render.
    const rows = screen.getAllByRole('button', { name: /dismiss|restore/i })
    // The Restore button belongs to the dismissed (newer) item; Dismiss to the older item.
    // If sort is correct, Restore should appear first (newer item first) and Dismiss second.
    expect(rows[0].getAttribute('aria-label')).toBe('Restore')
    expect(rows[1].getAttribute('aria-label')).toBe('Dismiss')
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
