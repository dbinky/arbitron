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

const WF_ORG: WorkflowSummary[] = [
  { name: 'my-draft', description: '', author: 'dbinky', nodeCount: 2, scope: 'private' },
  { name: 'feature-pipeline', description: '', author: 'arbitron', nodeCount: 23, scope: 'shared', visibility: 'public' },
  { name: 'billing-close', description: '', author: 'fin', nodeCount: 5, scope: 'shared', visibility: 'department', department: 'finance' },
  { name: 'hr-onboard', description: '', author: 'hr', nodeCount: 4, scope: 'shared', visibility: 'department', department: 'people' },
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
    fireEvent.click(screen.getByRole('button', { name: 'Shared' }))
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

describe('WorkflowsCatalog adaptive visibility + department', () => {
  beforeEach(() => {
    listFn.mockReset(); meFn.mockReset()
    meFn.mockResolvedValue(makeMe({ authMode: 'org', departments: ['finance', 'people'] }))
  })

  it('hides department chrome when data has no departments (visibility still shows for shared)', async () => {
    listFn.mockResolvedValue(WF) // all shared entries are public, no department
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('feature-pipeline')
    // Visibility shows (there are shared rows); department must not (no dept in data).
    expect(screen.getByLabelText('Visibility')).toBeInTheDocument()
    expect(screen.queryByLabelText('Department')).not.toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: 'Department' })).not.toBeInTheDocument()
  })

  it('shows visibility + department chrome when data has departments', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.getByLabelText('Visibility')).toBeInTheDocument()
    expect(screen.getByLabelText('Department')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Department' })).toBeInTheDocument()
  })

  it('filters by visibility=department', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all&vis=department']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
    expect(screen.getByText('hr-onboard')).toBeInTheDocument()
  })

  it('filters by a selected department', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all&dept=finance']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.queryByText('hr-onboard')).not.toBeInTheDocument()
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
  })

  it('populates the department dropdown from distinct departments in the data', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    const select = screen.getByLabelText('Department') as HTMLSelectElement
    const opts = Array.from(select.options).map((o) => o.value)
    expect(opts).toEqual(['', 'finance', 'people'])
  })

  it('ignores a stale dept param when the data has no departments', async () => {
    listFn.mockResolvedValue(WF) // no departments in data -> dept chrome hidden
    render(<MemoryRouter initialEntries={['/workflows?scope=all&dept=finance']}><WorkflowsCatalog /></MemoryRouter>)
    // dept=finance must NOT filter everything out, since no department facet exists.
    expect(await screen.findByText('feature-pipeline')).toBeInTheDocument()
    expect(screen.queryByLabelText('Department')).not.toBeInTheDocument()
  })
})
