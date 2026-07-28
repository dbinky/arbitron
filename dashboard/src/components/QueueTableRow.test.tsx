import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import QueueTableRow from './QueueTableRow'
import { visibleColumns } from './queueColumns'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }))
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateMock }
})

function row(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    ownerId: 'u1',
    workflowName: 'wf',
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 0,
    ...overrides,
  }
}

function renderRow(props: Partial<React.ComponentProps<typeof QueueTableRow>> = {}) {
  return render(
    <MemoryRouter>
      <QueueTableRow
        row={row()}
        columns={visibleColumns('balanced' as QueueDensity)}
        timeMode="elapsed"
        authMode="local"
        onExpand={vi.fn()}
        {...props}
      />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  navigateMock.mockClear()
})

describe('QueueTableRow', () => {
  it('renders a cell for each visible column', () => {
    renderRow()
    const cells = screen.getAllByRole('cell')
    // N visible columns + 1 trailing action cell (expand / dismiss-restore).
    expect(cells.length).toBe(visibleColumns('balanced').length + 1)
  })

  it('renders the run name in the runName cell', () => {
    renderRow({ row: row({ runName: 'My — wf' }) })
    expect(screen.getByText('My — wf')).toBeInTheDocument()
  })

  it('renders submitter as "you" in local mode', () => {
    renderRow({ row: row({ submitterName: 'alice' }), authMode: 'local' })
    expect(screen.getByText('you')).toBeInTheDocument()
  })

  it('renders submitter as display name in shared mode', () => {
    renderRow({ row: row({ submitterName: 'alice' }), authMode: 'org' })
    expect(screen.getByText('alice')).toBeInTheDocument()
  })

  it('renders queue position when present', () => {
    renderRow({ row: row({ queuePosition: 3 }) })
    const cell = screen.getByRole('cell', { name: '3' })
    expect(cell.getAttribute('data-col')).toBe('queuePosition')
  })

  it('renders placeholder for absent queue position', () => {
    renderRow({ row: row({ queuePosition: undefined }) })
    const cells = screen.getAllByRole('cell')
    const queueCell = cells.find((c) => c.getAttribute('data-col') === 'queuePosition')
    expect(queueCell?.textContent).toBe('—')
  })

  it('navigates to /instances/{id} on row click', () => {
    const onExpand = vi.fn()
    renderRow({ row: row({ id: 'abc-123' }), onExpand })
    fireEvent.click(screen.getByRole('row'))
    expect(navigateMock).toHaveBeenCalledWith('/instances/abc-123')
  })

  it('calls onExpand when the expand affordance is clicked, not navigation', () => {
    const onExpand = vi.fn()
    renderRow({ onExpand })
    const expandBtn = screen.getByRole('button', { name: /expand/i })
    fireEvent.click(expandBtn)
    expect(onExpand).toHaveBeenCalledTimes(1)
  })

  it('renders a dimmed class when dimmed', () => {
    renderRow({ dimmed: true })
    expect(screen.getByRole('row').className).toMatch(/dimmed/i)
  })
})

// The hierarchy toggle sits inside the row's <tr onClick={navigate}> — its own
// onClick calls stopPropagation specifically so expanding/collapsing children
// never also navigates away from the queue view.
describe('QueueTableRow — hierarchy toggle', () => {
  it('clicking the hierarchy toggle expands children without navigating (stopPropagation)', () => {
    const onToggleHierarchy = vi.fn()
    renderRow({ onToggleHierarchy, hierarchyExpanded: false })
    fireEvent.click(screen.getByLabelText('Show child runs of run — wf'))
    expect(onToggleHierarchy).toHaveBeenCalledWith('i1')
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('reflects hierarchyExpanded in the toggle label and aria-expanded', () => {
    renderRow({ onToggleHierarchy: vi.fn(), hierarchyExpanded: true })
    const toggle = screen.getByLabelText('Hide child runs of run — wf')
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
  })

  it('renders an aligning spacer, not a toggle, when the row has no children', () => {
    renderRow()
    expect(screen.queryByLabelText(/child runs of run — wf/)).toBeNull()
    const firstCell = screen.getAllByRole('cell')[0]
    expect(firstCell.querySelector('span[aria-hidden="true"]')).not.toBeNull()
  })

  it('indents the row proportionally to depth', () => {
    renderRow({ depth: 2 })
    const firstCell = screen.getAllByRole('cell')[0]
    const treeCell = firstCell.firstElementChild as HTMLElement
    expect(treeCell.style.paddingLeft).toBe('32px')
  })
})