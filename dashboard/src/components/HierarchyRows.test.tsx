import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { EnrichedInstanceSummary } from '../api/types'
import { COLUMNS } from './queueColumns'

const listMock = vi.fn()
vi.mock('../api/client', () => ({
  api: { instances: { list: (...a: unknown[]) => listMock(...a), getNodeTrace: vi.fn() } },
}))

import HierarchyRows from './HierarchyRows'
import { useRunHierarchy } from '../hooks/useRunHierarchy'

function row(id: string, overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id,
    ownerId: 'u1',
    workflowName: `wf-${id}`,
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: `run — ${id}`,
    retryCount: 0,
    ...overrides,
  }
}

// Harness: renders HierarchyRows inside a real table with a real hierarchy hook.
function Harness({ rows }: { rows: EnrichedInstanceSummary[] }) {
  const hierarchy = useRunHierarchy()
  return (
    <MemoryRouter>
      <table>
        <HierarchyRows
          rows={rows}
          columns={COLUMNS}
          depth={0}
          timeMode="elapsed"
          authMode="local"
          hierarchy={hierarchy}
        />
      </table>
    </MemoryRouter>
  )
}

beforeEach(() => {
  listMock.mockReset()
})

// ---------- happy path ----------

it('renders an expander only on rows reporting children', () => {
  render(<Harness rows={[row('p1', { childCount: 2 }), row('p2')]} />)

  expect(screen.getByLabelText('Show child runs of run — p1')).toBeTruthy()
  expect(screen.queryByLabelText('Show child runs of run — p2')).toBeNull()
})

it('expanding a row fetches and renders its children nested', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))

  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1' })
})

// ---------- success ----------

it('expanding a child renders grandchildren, one level per request', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) => {
    if (parent === 'p1') return Promise.resolve([row('c1', { parentInstanceId: 'p1', childCount: 1 })])
    if (parent === 'c1') return Promise.resolve([row('g1', { parentInstanceId: 'c1' })])
    return Promise.resolve([])
  })
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  fireEvent.click(screen.getByLabelText('Show child runs of run — c1'))
  await waitFor(() => expect(screen.getByText('wf-g1')).toBeTruthy())

  expect(listMock).toHaveBeenNthCalledWith(1, { parent: 'p1' })
  expect(listMock).toHaveBeenNthCalledWith(2, { parent: 'c1' })
})

it('nests visually by depth', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  const parentRow = document.querySelector('tr[data-row-id="p1"]') as HTMLElement
  const childRow = document.querySelector('tr[data-row-id="c1"]') as HTMLElement
  expect(parentRow.getAttribute('data-depth')).toBe('0')
  expect(childRow.getAttribute('data-depth')).toBe('1')
})

// A child row keeps the SAME action set as a root row — this is what delivers
// the spec's outcome that operators inspect/retry/resume/cancel at the CHILD
// level instead of restarting a larger process.
it('a child row exposes the same actions as a root row', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1', state: 'failed' })])
  render(<Harness rows={[row('p1', { childCount: 1, state: 'failed' })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  const parentRow = document.querySelector('tr[data-row-id="p1"]') as HTMLElement
  const childRow = document.querySelector('tr[data-row-id="c1"]') as HTMLElement
  // Compare the ACTIONS cell only (the trailing <td>). The whole row is not
  // comparable: the parent also renders the hierarchy toggle in its first cell,
  // and the child — reporting no children — renders the aligning spacer
  // instead, so a row-wide button comparison could never be equal.
  const actionLabels = (el: HTMLElement) => {
    const cells = el.querySelectorAll('td')
    const actions = cells[cells.length - 1]
    return Array.from(actions.querySelectorAll('button'))
      .map((b) => b.getAttribute('aria-label'))
      .sort()
  }
  expect(actionLabels(childRow)).toEqual(actionLabels(parentRow))
  expect(actionLabels(childRow).length).toBeGreaterThan(0)
})

it('collapsing hides the children again', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  fireEvent.click(screen.getByLabelText('Hide child runs of run — p1'))
  await waitFor(() => expect(screen.queryByText('wf-c1')).toBeNull())
})

// ---------- failure / error ----------

it('confines an expansion failure to its own row', async () => {
  listMock.mockImplementation(({ parent }: { parent: string }) =>
    parent === 'p1'
      ? Promise.reject(new Error('API 500: boom'))
      : Promise.resolve([row('c2', { parentInstanceId: parent })]),
  )
  render(<Harness rows={[row('p1', { childCount: 1 }), row('p2', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText(/boom/)).toBeTruthy())

  fireEvent.click(screen.getByLabelText('Show child runs of run — p2'))
  await waitFor(() => expect(screen.getByText('wf-c2')).toBeTruthy())
  expect(document.querySelector('tr[data-row-id="p2"]')).toBeTruthy()
})

// ---------- edge cases ----------

it('a run with no children renders no expander', () => {
  render(<Harness rows={[row('solo')]} />)
  expect(screen.queryByLabelText('Show child runs of run — solo')).toBeNull()
})

it('a zero child count renders no expander (no existence leak)', () => {
  render(<Harness rows={[row('hidden', { childCount: 0 })]} />)
  expect(screen.queryByLabelText('Show child runs of run — hidden')).toBeNull()
})

it('an expander whose level comes back empty renders no child rows and no error', async () => {
  listMock.mockResolvedValue([])
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByLabelText('Hide child runs of run — p1')).toBeTruthy())
  expect(screen.queryByText(/Failed to load/)).toBeNull()
})
