import { render, screen, within } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import NavBar from './NavBar'

function renderNav() {
  return render(
    <MemoryRouter>
      <NavBar />
    </MemoryRouter>,
  )
}

describe('NavBar', () => {
  it('renders a Dashboard tab linking to /dashboard', () => {
    renderNav()
    const link = screen.getByRole('link', { name: 'Dashboard' })
    expect(link).toHaveAttribute('href', '/dashboard')
  })

  it('orders Dashboard before Queue', () => {
    const { container } = renderNav()
    const links = within(container).getAllByRole('link').map((a) => a.textContent)
    const dashIdx = links.indexOf('Dashboard')
    const queueIdx = links.indexOf('Queue')
    expect(dashIdx).toBeGreaterThanOrEqual(0)
    expect(queueIdx).toBeGreaterThanOrEqual(0)
    expect(dashIdx).toBeLessThan(queueIdx)
  })
})
