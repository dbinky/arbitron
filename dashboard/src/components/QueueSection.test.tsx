import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'

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
