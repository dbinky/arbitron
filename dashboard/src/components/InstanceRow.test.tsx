import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import InstanceRow from './InstanceRow'
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

function renderRow(props: Partial<React.ComponentProps<typeof InstanceRow>>) {
  return render(
    <MemoryRouter>
      <InstanceRow instance={summary()} timeMode="relative" {...props} />
    </MemoryRouter>,
  )
}

describe('InstanceRow dismissal controls', () => {
  it('shows a dismiss control on terminal rows and calls onDismiss without navigating', () => {
    const onDismiss = vi.fn()
    renderRow({ instance: summary({ state: 'failed' }), onDismiss })
    const btn = screen.getByRole('button', { name: /dismiss/i })
    fireEvent.click(btn)
    expect(onDismiss).toHaveBeenCalledWith('i1')
  })

  it('shows a restore control (not dismiss) when dimmed', () => {
    const onRestore = vi.fn()
    renderRow({ instance: summary({ state: 'completed', dismissed: true }), dimmed: true, onRestore })
    const btn = screen.getByRole('button', { name: /restore/i })
    fireEvent.click(btn)
    expect(onRestore).toHaveBeenCalledWith('i1')
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
  })

  it('shows no dismiss/restore control on active (running) rows', () => {
    renderRow({ instance: summary({ state: 'running' }), onDismiss: vi.fn() })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })

  // Edge: cancelled is terminal in the engine sense but intentionally excluded from
  // dismissal per spec ("only completed/failed items can be dismissed"). isTerminal
  // in InstanceRow is deliberately narrower — this test pins that design boundary.
  it('shows no dismiss/restore control on cancelled rows', () => {
    renderRow({ instance: summary({ state: 'cancelled' }), onDismiss: vi.fn(), onRestore: vi.fn() })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })

  // Edge: spec explicitly names "waiting (interactive)" as non-dismissible alongside
  // "active" and "cancelled". Pin this so widening isTerminal to include waiting is visible.
  it('shows no dismiss/restore control on waiting rows', () => {
    renderRow({ instance: summary({ state: 'waiting' }), onDismiss: vi.fn(), onRestore: vi.fn() })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })

  it('shows no dismiss/restore control on paused rows', () => {
    renderRow({ instance: summary({ state: 'paused' }), onDismiss: vi.fn(), onRestore: vi.fn() })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })
})
