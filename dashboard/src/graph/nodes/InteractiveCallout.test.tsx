import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import InteractiveCallout from './InteractiveCallout'

// ---------------------------------------------------------------------------
// Happy path: renders children and base styles
// ---------------------------------------------------------------------------

describe('InteractiveCallout — happy path', () => {
  it('renders children content', () => {
    render(<InteractiveCallout>Hello callout</InteractiveCallout>)
    expect(screen.getByText('Hello callout')).toBeInTheDocument()
  })

  it('renders without children', () => {
    const { container } = render(<InteractiveCallout />)
    expect(container.firstElementChild).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Waiting state: pulsing class toggled by prop
// ---------------------------------------------------------------------------

describe('InteractiveCallout — waiting state', () => {
  it('does not apply waiting class when waiting is false (default)', () => {
    const { container } = render(<InteractiveCallout>content</InteractiveCallout>)
    const callout = container.firstElementChild!
    expect(callout.className).not.toContain('waiting')
  })

  it('applies waiting class when waiting=true', () => {
    const { container } = render(
      <InteractiveCallout waiting>content</InteractiveCallout>,
    )
    const callout = container.firstElementChild!
    expect(callout.className).toContain('waiting')
  })

  it('removes waiting class when waiting changes from true to false', () => {
    const { container, rerender } = render(
      <InteractiveCallout waiting>content</InteractiveCallout>,
    )
    expect(container.firstElementChild!.className).toContain('waiting')

    rerender(<InteractiveCallout waiting={false}>content</InteractiveCallout>)
    expect(container.firstElementChild!.className).not.toContain('waiting')
  })
})

// ---------------------------------------------------------------------------
// Edge: complex children render correctly
// ---------------------------------------------------------------------------

describe('InteractiveCallout — edge cases', () => {
  it('renders nested JSX children', () => {
    render(
      <InteractiveCallout>
        <strong>Bold</strong> text
      </InteractiveCallout>,
    )
    expect(screen.getByText('Bold')).toBeInTheDocument()
  })
})
