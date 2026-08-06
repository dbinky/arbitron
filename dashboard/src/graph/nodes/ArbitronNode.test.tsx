import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'

// Handle needs the React Flow store context; in isolation we stub it to a marker
// div so we can assert which handles a node exposes.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    Handle: ({ id, type, position }: { id?: string; type: string; position: string }) => (
      <div data-testid="handle" data-handle-id={id ?? ''} data-handle-type={type} data-position={position} />
    ),
  }
})

import ArbitronNode from './ArbitronNode'

const props = {
  data: { label: 'Init', nodeType: 'script', executionState: 'pending' },
  selected: false,
} as unknown as Parameters<typeof ArbitronNode>[0]

describe('ArbitronNode — handles', () => {
  it('exposes ONLY the default top (target) and bottom (source) handles', () => {
    // No side handles: extra source/target handles make React Flow pull forward
    // edges to a node side instead of routing them cleanly bottom→top.
    const { container } = render(<ArbitronNode {...props} />)
    const handles = [...container.querySelectorAll('[data-testid="handle"]')]
    expect(handles).toHaveLength(2)

    const top = handles.find((h) => h.getAttribute('data-position') === 'top')
    const bottom = handles.find((h) => h.getAttribute('data-position') === 'bottom')
    expect(top?.getAttribute('data-handle-type')).toBe('target')
    expect(bottom?.getAttribute('data-handle-type')).toBe('source')

    const sides = handles.filter((h) => ['left', 'right'].includes(h.getAttribute('data-position') ?? ''))
    expect(sides).toHaveLength(0)
  })
})

describe('ArbitronNode — type accent color', () => {
  it('renders the spawn_workflow accent (#2ecc71) on the type bar and label', () => {
    const spawnProps = {
      data: { label: 'Spawn child', nodeType: 'spawn_workflow', executionState: 'pending' },
      selected: false,
    } as unknown as Parameters<typeof ArbitronNode>[0]
    const { container } = render(<ArbitronNode {...spawnProps} />)

    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    const label = container.querySelector('[class*="typeLabel"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(46, 204, 113)')
    expect(label.style.color).toBe('rgb(46, 204, 113)')
    expect(label.textContent).toBe('spawn workflow')
  })

  it('falls back to the default grey accent for an unmapped node type', () => {
    // Guards the `?? '#888888'` fallback: a typo'd or removed typeColors entry
    // must degrade to the default, not crash or render an empty style.
    const unknownProps = {
      data: { label: 'Mystery', nodeType: 'totally_unknown_type', executionState: 'pending' },
      selected: false,
    } as unknown as Parameters<typeof ArbitronNode>[0]
    const { container } = render(<ArbitronNode {...unknownProps} />)

    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(136, 136, 136)')
  })
})
