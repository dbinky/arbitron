import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DensityToggle from './DensityToggle'
import type { QueueDensity } from '../api/types'

function renderToggle(props: Partial<React.ComponentProps<typeof DensityToggle>> = {}) {
  return render(
    <DensityToggle
      density="balanced"
      onCycle={vi.fn()}
      {...props}
    />,
  )
}

describe('DensityToggle', () => {
  it('renders a button with the current density label', () => {
    renderToggle({ density: 'balanced' as QueueDensity })
    const btn = screen.getByRole('button', { name: /density/i })
    expect(btn).toBeInTheDocument()
    expect(btn.textContent?.toLowerCase()).toContain('balanced')
  })

  it('calls onCycle when clicked', () => {
    const onCycle = vi.fn()
    renderToggle({ onCycle })
    fireEvent.click(screen.getByRole('button', { name: /density/i }))
    expect(onCycle).toHaveBeenCalledTimes(1)
  })

  it('reflects the dense label when density is dense', () => {
    renderToggle({ density: 'dense' as QueueDensity })
    expect(screen.getByRole('button', { name: /density/i }).textContent?.toLowerCase()).toContain('dense')
  })

  it('reflects the expanded label when density is expanded', () => {
    renderToggle({ density: 'expanded' as QueueDensity })
    expect(screen.getByRole('button', { name: /density/i }).textContent?.toLowerCase()).toContain('expanded')
  })

  it('has an accessible aria-label', () => {
    renderToggle()
    expect(screen.getByLabelText(/cycle density/i)).toBeInTheDocument()
  })
})