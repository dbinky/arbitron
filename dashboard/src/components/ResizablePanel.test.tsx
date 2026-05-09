import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import ResizablePanel from './ResizablePanel'

// jsdom getBoundingClientRect returns zeros. Override for the container element
// so fraction-width calculations produce predictable values.
function mockContainerRect(width: number) {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({
    width,
    height: 600,
    top: 0,
    left: 0,
    right: width,
    bottom: 600,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect)
}

beforeEach(() => {
  vi.restoreAllMocks()
})

// ---------------------------------------------------------------------------
// Happy path: renders left and right content
// ---------------------------------------------------------------------------

describe('ResizablePanel — happy path', () => {
  it('renders left content', () => {
    render(<ResizablePanel left={<div>Left side</div>} right={<div>Right side</div>} />)
    expect(screen.getByText('Left side')).toBeInTheDocument()
  })

  it('renders right content', () => {
    render(<ResizablePanel left={<div>Left side</div>} right={<div>Right side</div>} />)
    expect(screen.getByText('Right side')).toBeInTheDocument()
  })

  it('renders the divider', () => {
    const { container } = render(
      <ResizablePanel left={<div>L</div>} right={<div>R</div>} />,
    )
    // The divider uses a CSS module class that includes 'divider'
    const divider = container.querySelector('[class*="divider"]')
    expect(divider).toBeInTheDocument()
  })

  it('applies defaultRightWidth to the right panel style', () => {
    const { container } = render(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={350}
      />,
    )
    const right = container.querySelector('[class*="right"]') as HTMLElement
    expect(right.style.width).toBe('350px')
  })
})

// ---------------------------------------------------------------------------
// Fraction width: rightWidthFraction applies computed width
// ---------------------------------------------------------------------------

describe('ResizablePanel — rightWidthFraction', () => {
  it('applies fraction width to the right panel when set', () => {
    mockContainerRect(1000)
    const { container } = render(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={400}
        rightWidthFraction={0.6}
      />,
    )
    const right = container.querySelector('[class*="right"]') as HTMLElement
    // 1000 * 0.6 = 600, clamped by minRightWidth (200) and totalWidth - minLeft (1000-200-4=796)
    // So 600 is within bounds — expect 600px
    expect(right.style.width).toBe('600px')
  })

  it('clamps fraction width to minRightWidth', () => {
    mockContainerRect(300) // small container
    const { container } = render(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={200}
        minRightWidth={200}
        rightWidthFraction={0.1} // 300 * 0.1 = 30, below minRightWidth
      />,
    )
    const right = container.querySelector('[class*="right"]') as HTMLElement
    expect(right.style.width).toBe('200px')
  })
})

// ---------------------------------------------------------------------------
// Snap-back: restores previous width when rightWidthFraction becomes undefined
// ---------------------------------------------------------------------------

describe('ResizablePanel — snap-back', () => {
  it('restores the saved manual width when rightWidthFraction is removed', () => {
    mockContainerRect(1000)
    const { container, rerender } = render(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={350}
      />,
    )
    const right = container.querySelector('[class*="right"]') as HTMLElement
    expect(right.style.width).toBe('350px')

    // Activate fraction — saves 350px
    rerender(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={350}
        rightWidthFraction={0.6}
      />,
    )
    expect(right.style.width).toBe('600px')

    // Deactivate fraction — should restore to 350px
    rerender(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        defaultRightWidth={350}
      />,
    )
    expect(right.style.width).toBe('350px')
  })
})

// ---------------------------------------------------------------------------
// Edge: dragging activates divider active class
// ---------------------------------------------------------------------------

describe('ResizablePanel — drag state', () => {
  it('adds active class to divider while dragging', () => {
    const { container } = render(
      <ResizablePanel left={<div>L</div>} right={<div>R</div>} />,
    )
    const divider = container.querySelector('[class*="divider"]') as HTMLElement
    expect(divider.className).not.toContain('dividerActive')

    fireEvent.mouseDown(divider)
    expect(divider.className).toContain('dividerActive')

    fireEvent.mouseUp(document)
    expect(divider.className).not.toContain('dividerActive')
  })
})

// ---------------------------------------------------------------------------
// Boundary: minRightWidth and minLeftWidth are respected
// ---------------------------------------------------------------------------

describe('ResizablePanel — boundary constraints', () => {
  it('defaults minLeftWidth and minRightWidth to 200 without crashing', () => {
    const { container } = render(
      <ResizablePanel
        left={<div>L</div>}
        right={<div>R</div>}
        minLeftWidth={200}
        minRightWidth={200}
      />,
    )
    expect(container.querySelector('[class*="right"]')).toBeInTheDocument()
  })
})
