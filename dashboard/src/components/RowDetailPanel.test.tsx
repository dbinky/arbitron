import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import RowDetailPanel from './RowDetailPanel'
import type { EnrichedInstanceSummary } from '../api/types'

vi.mock('../api/client', () => ({
  api: {
    instances: {
      getNodeTrace: vi.fn(),
    },
  },
}))

import { api } from '../api/client'

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

describe('RowDetailPanel', () => {
  beforeEach(() => {
    vi.mocked(api.instances.getNodeTrace).mockReset()
    vi.mocked(api.instances.getNodeTrace).mockResolvedValue({
      id: 'e0', instanceId: 'i1', nodeId: 'fail-node', attempt: 0, status: 'failed',
      error: undefined, startedAt: '2024-01-01T00:00:00Z',
    } as any)
  })

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
      <RowDetailPanel
        row={row({ submitterIdentity: 'alice@example.com' })}
        authMode="local"
      />,
    )
    expect(screen.queryByText('alice@example.com')).toBeNull()
  })

  it('lazy-loads last error via getNodeTrace(failedNodeId) on expand', async () => {
    vi.mocked(api.instances.getNodeTrace).mockResolvedValue({
      id: 'e1', instanceId: 'i1', nodeId: 'fail-node', attempt: 2, status: 'failed',
      error: { error: 'boom: connection refused' },
      startedAt: '2024-01-01T00:00:00Z',
    } as any)
    render(<RowDetailPanel row={row({ failedNodeId: 'fail-node' })} authMode="local" />)
    await waitFor(() => {
      expect(api.instances.getNodeTrace).toHaveBeenCalledWith('i1', 'fail-node')
    })
    expect(screen.getByText(/connection refused/i)).toBeInTheDocument()
  })

  it('shows inline error state when the trace fetch rejects', async () => {
    vi.mocked(api.instances.getNodeTrace).mockRejectedValue(new Error('404'))
    render(<RowDetailPanel row={row({ failedNodeId: 'fail-node' })} authMode="local" />)
    await waitFor(() => {
      expect(screen.getByText(/couldn't load details/i)).toBeInTheDocument()
    })
  })

  it('shows no error section when failedNodeId is absent', () => {
    render(<RowDetailPanel row={row({ failedNodeId: undefined, state: 'completed' })} authMode="local" />)
    expect(screen.queryByText(/last error/i)).toBeNull()
    expect(api.instances.getNodeTrace).not.toHaveBeenCalled()
  })

  it('renders model/backend and retry detail', () => {
    render(
      <RowDetailPanel
        row={row({ model: 'gpt-4o', backend: 'claude-code', retryCount: 3 })}
        authMode="local"
      />,
    )
    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
    expect(screen.getByText('claude-code')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('renders the full submitted timestamp', () => {
    render(<RowDetailPanel row={row({ createdAt: '2024-06-25T12:00:00Z' })} authMode="local" />)
    expect(screen.getByText(/2024-06-25/)).toBeInTheDocument()
  })
})