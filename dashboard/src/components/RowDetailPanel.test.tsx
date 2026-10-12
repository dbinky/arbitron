import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import RowDetailPanel from './RowDetailPanel'
import type { EnrichedInstanceSummary } from '../api/types'

function row(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 2,
    failedNodeId: 'fail-node',
    ...overrides,
  }
}

// RowDetailPanel is now a pure render component: the parent (QueueSection) owns
// the trace fetch and passes errorText/errorLoading in. It imports no API client.
describe('RowDetailPanel', () => {
  it('renders secondary fields (submitter identity in shared mode)', () => {
    render(
      <RowDetailPanel
        row={row({ submitterIdentity: 'alice@example.com', submitterName: 'alice' })}
        authMode="org"
      />,
    )
    expect(screen.getByText('alice@example.com')).toBeInTheDocument()
  })

  it('hides submitter identity in local mode', () => {
    render(
      <RowDetailPanel row={row({ submitterIdentity: 'alice@example.com' })} authMode="local" />,
    )
    expect(screen.queryByText('alice@example.com')).toBeNull()
  })

  it('renders the last error text passed in via the errorText prop', () => {
    render(
      <RowDetailPanel
        row={row({ failedNodeId: 'fail-node' })}
        authMode="local"
        errorText="boom: connection refused"
      />,
    )
    expect(screen.getByText(/connection refused/i)).toBeInTheDocument()
  })

  it('shows a loading state while errorLoading is true', () => {
    render(
      <RowDetailPanel
        row={row({ failedNodeId: 'fail-node' })}
        authMode="local"
        errorLoading
      />,
    )
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  it('shows the inline error fallback when there is no errorText', () => {
    render(<RowDetailPanel row={row({ failedNodeId: 'fail-node' })} authMode="local" />)
    expect(screen.getByText(/couldn't load details/i)).toBeInTheDocument()
  })

  it('shows no error section when failedNodeId is absent', () => {
    render(<RowDetailPanel row={row({ failedNodeId: undefined, state: 'completed' })} authMode="local" />)
    expect(screen.queryByText(/last error/i)).toBeNull()
  })

  it('renders the formatted binding and retry detail', () => {
    render(
      <RowDetailPanel
        row={row({ model: 'gpt-4o', effort: 'high', backend: 'claude-code', provider: 'claude-cli', retryCount: 3 })}
        authMode="local"
      />,
    )
    expect(screen.getByText('gpt-4o (high) · claude-code · claude-cli')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('renders the full submitted timestamp', () => {
    render(<RowDetailPanel row={row({ createdAt: '2024-06-25T12:00:00Z' })} authMode="local" />)
    expect(screen.getByText(/2024-06-25/)).toBeInTheDocument()
  })
})
