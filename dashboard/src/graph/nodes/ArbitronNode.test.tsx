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
