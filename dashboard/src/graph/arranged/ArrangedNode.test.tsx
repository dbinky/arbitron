import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { NodeExecutionState } from '../types'
import { fixtureGeometry } from './testFixtures'
import { RENDERER_CONTRACT } from './validate'

// Handle needs the React Flow store; stub it to a marker so the handles a node
// exposes can be asserted.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import ArrangedNode, { nodeTextClassNames } from './ArrangedNode'

type Props = Parameters<typeof ArrangedNode>[0]

function props(overrides: { state?: NodeExecutionState; selected?: boolean; nodeType?: string } = {}): Props {
  const nodeBox = fixtureGeometry().nodes[1] // b: 200x70, long label, typeCaption "llm call"
  return {
    id: nodeBox.id,
    data: {
      nodeBox,
      nodeType: overrides.nodeType ?? 'llm_call',
      executionState: overrides.state ?? 'pending',
      contract: RENDERER_CONTRACT,
    },
    selected: overrides.selected ?? false,
  } as unknown as Props
}

function root(container: HTMLElement): HTMLElement {
  return container.querySelector('[data-arranged-node]') as HTMLElement
}

describe('ArrangedNode — DOM contract', () => {
  it('marks the root with data-arranged-node and sizes it to the payload box', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const el = root(container)
    expect(el.getAttribute('data-arranged-node')).toBe('b')
    expect(el.style.width).toBe('200px')
    expect(el.style.height).toBe('70px')
    expect(el.className).toContain('node')
  })

  it('renders the payload text (not the manifest label) in the label element, untruncated', () => {
    const p = props()
    p.data.nodeBox = { ...p.data.nodeBox, text: 'Payload text with a deliberately long run of words to wrap' }
    const { container } = render(<ArrangedNode {...p} />)
    const label = container.querySelector('[data-node-text="label"]') as HTMLElement
    expect(label.textContent).toBe('Payload text with a deliberately long run of words to wrap')
    expect(label.className).toBe(nodeTextClassNames.label)
    expect(label.style.marginTop).toBe('2px')
  })

  it('renders the payload type caption in the type element', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const type = container.querySelector('[data-node-text="type"]') as HTMLElement
    expect(type.textContent).toBe('llm call')
    expect(type.className).toBe(nodeTextClassNames.typeCaption)
  })

  it('applies the contract padding, type-bar height and text max width', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const content = container.querySelector('[class*="content"]') as HTMLElement
    expect(content.style.padding).toBe('8px 12px')
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    expect(bar.style.height).toBe('4px')
    expect(root(container).style.getPropertyValue('--node-text-max')).toBe('240px')
  })

  it('colours only the type bar with the node-type accent', () => {
    const { container } = render(<ArrangedNode {...props({ nodeType: 'spawn_workflow' })} />)
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(46, 204, 113)')
    const type = container.querySelector('[data-node-text="type"]') as HTMLElement
    expect(type.style.color).toBe('')
  })

  it('exposes exactly the hidden top target and bottom source handles', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const handles = [...container.querySelectorAll('[data-testid="handle"]')]
    expect(handles.map((h) => [h.getAttribute('data-handle-type'), h.getAttribute('data-position')])).toEqual([
      ['target', 'top'],
      ['source', 'bottom'],
    ])
  })
})

describe('ArrangedNode — text fidelity', () => {
  it('does not reserve a caption block or gap for an empty type caption', () => {
    const p = props()
    p.data.nodeBox = { ...p.data.nodeBox, typeCaption: '', text: '測試\n' + '界'.repeat(80) }
    const { container } = render(<ArrangedNode {...p} />)
    expect(container.querySelector('[data-node-text="type"]')).toBeNull()
    const label = container.querySelector('[data-node-text="label"]') as HTMLElement
    expect(label.textContent).toBe(p.data.nodeBox.text)
    expect(label.style.marginTop).toBe('0px')
  })

  it('uses changed payload dimensions and contract box-model values, not fixture constants', () => {
    const p = props()
    p.data.nodeBox = { ...p.data.nodeBox, box: { x: 0, y: 0, w: 321, h: 185 } }
    p.data.contract = { ...RENDERER_CONTRACT, nodeBorderPx: 3, nodeTextMaxWidth: 210 }
    const { container } = render(<ArrangedNode {...p} />)
    expect(root(container).style.width).toBe('321px')
    expect(root(container).style.height).toBe('185px')
    expect(root(container).style.borderWidth).toBe('3px')
    expect(root(container).style.getPropertyValue('--node-text-max')).toBe('210px')
  })
})

describe('ArrangedNode — runtime states never resize or fade the node', () => {
  const states: NodeExecutionState[] = ['pending', 'running', 'completed', 'failed', 'waiting', 'skipped']

  it.each(states)('state %s keeps the contract border width, box size and no opacity', (state) => {
    const { container } = render(<ArrangedNode {...props({ state })} />)
    const el = root(container)
    expect(el.style.borderWidth).toBe('2px')
    expect(el.style.width).toBe('200px')
    expect(el.style.height).toBe('70px')
    expect(el.style.opacity).toBe('')
    expect(el.getAttribute('data-state')).toBe(state)
    expect(el.className).toContain(`state${state[0].toUpperCase()}${state.slice(1)}`)
  })

  it('adds the selected class without changing the box', () => {
    const { container } = render(<ArrangedNode {...props({ selected: true })} />)
    const el = root(container)
    expect(el.className).toContain('selected')
    expect(el.style.width).toBe('200px')
  })

  it('positions the state dot absolutely below the type bar (outside the text flow)', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const dot = container.querySelector('[class*="stateDot"]') as HTMLElement
    expect(dot.getAttribute('aria-hidden')).toBe('true')
    expect(dot.style.top).toBe('8px')
    // The dot is a sibling of the content block, never inside the label.
    expect(container.querySelector('[data-node-text="label"] [class*="stateDot"]')).toBeNull()
  })

  it('defaults a missing execution state to pending', () => {
    const p = props()
    ;(p.data as { executionState?: NodeExecutionState }).executionState = undefined
    const { container } = render(<ArrangedNode {...p} />)
    expect(root(container).getAttribute('data-state')).toBe('pending')
  })
})
