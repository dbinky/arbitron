import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WorkflowSummary, Me } from '../api/types'

const { listFn, meFn } = vi.hoisted(() => ({ listFn: vi.fn(), meFn: vi.fn() }))
vi.mock('../api/client', () => ({
  api: { workflows: { list: listFn }, me: meFn },
}))

import WorkflowsCatalog from './WorkflowsCatalog'

function makeMe(over: Partial<Me> = {}): Me {
  return { userId: 'u1', username: 'local', role: 'admin', departments: ['default'], ledDepartments: [], isAdmin: true, authMode: 'local', ...over }
}

const WF: WorkflowSummary[] = [
  { name: 'my-draft', description: '', author: 'dbinky', nodeCount: 2, scope: 'private' },
  { name: 'feature-pipeline', description: '', author: 'arbitron', nodeCount: 23, scope: 'shared', visibility: 'public' },
  { name: 'pr-review', description: '', author: 'arbitron', nodeCount: 9, scope: 'shared', visibility: 'public' },
]

function renderAt(path = '/workflows') {
  return render(<MemoryRouter initialEntries={[path]}><WorkflowsCatalog /></MemoryRouter>)
}

describe('WorkflowsCatalog scope filtering', () => {
  beforeEach(() => {
    listFn.mockReset(); meFn.mockReset()
    listFn.mockResolvedValue(WF)
    meFn.mockResolvedValue(makeMe())
  })

  it('defaults to All in local mode (shows private + shared)', async () => {
    renderAt()
    expect(await screen.findByText('my-draft')).toBeInTheDocument()
    expect(screen.getByText('feature-pipeline')).toBeInTheDocument()
  })

  it('defaults to Shared in org mode (hides private)', async () => {
    meFn.mockResolvedValue(makeMe({ authMode: 'org' }))
    renderAt()
    expect(await screen.findByText('feature-pipeline')).toBeInTheDocument()
    expect(screen.queryByText('my-draft')).not.toBeInTheDocument()
  })

  it('honors an explicit scope URL param over the mode default', async () => {
    renderAt('/workflows?scope=mine')
    expect(await screen.findByText('my-draft')).toBeInTheDocument()
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
  })

  it('clicking a scope tab filters and writes the URL param', async () => {
    renderAt()
    await screen.findByText('my-draft')
    fireEvent.click(screen.getByRole('tab', { name: 'Shared' }))
    await waitFor(() => expect(screen.queryByText('my-draft')).not.toBeInTheDocument())
    expect(screen.getByText('feature-pipeline')).toBeInTheDocument()
  })

  it('filters by name search alongside scope', async () => {
    renderAt('/workflows?scope=all')
    await screen.findByText('my-draft')
    fireEvent.change(screen.getByPlaceholderText('Search workflows...'), { target: { value: 'pr-' } })
    await waitFor(() => expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument())
    expect(screen.getByText('pr-review')).toBeInTheDocument()
  })
})
