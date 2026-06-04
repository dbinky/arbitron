import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import LoopContainerNode from './LoopContainerNode'

// React Flow node components receive NodeProps; we only read data here.
const renderNode = (data: { label: string; depth: number }) =>
  render(
    // @ts-expect-error — partial NodeProps is sufficient for this unit test.
    <LoopContainerNode data={data} />,
  )

describe('LoopContainerNode', () => {
  it('renders the provided label text', () => {
    renderNode({ label: 'Loop until tests pass', depth: 0 })
    expect(screen.getByText('Loop until tests pass')).toBeInTheDocument()
  })

  it('renders the bare "Loop" label when that is provided', () => {
    renderNode({ label: 'Loop', depth: 1 })
    expect(screen.getByText('Loop')).toBeInTheDocument()
  })
})
