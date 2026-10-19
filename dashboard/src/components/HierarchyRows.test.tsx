import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { EnrichedInstanceSummary } from '../api/types'
import { COLUMNS } from './queueColumns'

const listMock = vi.fn()
vi.mock('../api/client', () => ({
  api: { instances: { list: (...a: unknown[]) => listMock(...a), getNodeTrace: vi.fn() } },
}))

import HierarchyRows from './HierarchyRows'
import { CHILD_PAGE_LIMIT, useRunHierarchy, type RunHierarchy } from '../hooks/useRunHierarchy'

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

// Harness: renders HierarchyRows the way QueueSection actually does — a
// single <tbody> owned by the table, wrapping the rows HierarchyRows produces.
function Harness({ rows }: { rows: EnrichedInstanceSummary[] }) {
  const hierarchy = useRunHierarchy()
  return (
    <MemoryRouter>
      <table>
        <tbody>
          <HierarchyRows
            rows={rows}
            columns={COLUMNS}
            depth={0}
            timeMode="elapsed"
            authMode="local"
            hierarchy={hierarchy}
          />
        </tbody>
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
  expect(listMock).toHaveBeenCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
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

  expect(listMock).toHaveBeenNthCalledWith(1, { parent: 'p1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
  expect(listMock).toHaveBeenNthCalledWith(2, { parent: 'c1', limit: CHILD_PAGE_LIMIT, includeDismissed: true })
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

// Each row's <tbody> must sit ALONGSIDE its children's <tbody>s under the
// table, not wrap them — nesting one <tbody> inside another is invalid HTML
// (React itself flags it: "This will cause a hydration error") and real
// browsers do not auto-correct a DOM tree built via the JS API the way they
// correct parsed HTML text, so a real user would see this exact malformed
// structure, not just a dev-console warning.
it('expanding a row to show its children does not nest one <tbody> inside another', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  render(<Harness rows={[row('p1', { childCount: 1 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  const nestingWarning = errorSpy.mock.calls.find((c) =>
    String(c[0]).includes('cannot be a child of'),
  )
  errorSpy.mockRestore()
  expect(nestingWarning).toBeUndefined()
})

// ---------- truncation ----------

// TRUNCATION IS STATED, NOT CLAMPED. The row's childCount comes from the server
// and counts every child the caller may see; one expansion is ONE page. When
// they disagree the UI must say so and say by how much — clamping the badge
// down to the page size would make them agree by LYING about how many children
// exist, and the user would never learn the rest are there.
it('states how many children are shown when the page does not cover them all', async () => {
  listMock.mockResolvedValue([row('c1', { parentInstanceId: 'p1' })])
  render(<Harness rows={[row('p1', { childCount: 4 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  expect(screen.getByText(/Showing 1 of 4 child runs/)).toBeTruthy()
})

// No truncation row when the page covers everything — the common case must not
// carry a permanent "there might be more" caveat.
it('shows no truncation row when every child is on the page', async () => {
  listMock.mockResolvedValue([
    row('c1', { parentInstanceId: 'p1' }),
    row('c2', { parentInstanceId: 'p1' }),
  ])
  render(<Harness rows={[row('p1', { childCount: 2 })]} />)

  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  expect(screen.queryByText(/Showing/)).toBeNull()
})

// ---------- refresh & load-more (P4-T10) ----------

function ControlledHarness({ rows, onHierarchy }: { rows: EnrichedInstanceSummary[]; onHierarchy: (h: RunHierarchy) => void }) {
  const hierarchy = useRunHierarchy()
  onHierarchy(hierarchy)
  return (
    <MemoryRouter>
      <table>
        <tbody>
          <HierarchyRows rows={rows} columns={COLUMNS} depth={0} timeMode="elapsed" authMode="local" hierarchy={hierarchy} />
        </tbody>
      </table>
    </MemoryRouter>
  )
}

// Stale cache fix without a flash: every queue refetch refreshes the open
// levels, so hiding children while their level reloads made an open tree
// blink (and lose the user's place) on every lifecycle event.
it('keeps showing the cached children while their level refreshes', async () => {
  listMock.mockResolvedValueOnce([row('c1', { parentInstanceId: 'p1' })])
  let h!: RunHierarchy
  render(<ControlledHarness rows={[row('p1', { childCount: 1 })]} onHierarchy={(x) => { h = x }} />)
  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  await waitFor(() => expect(screen.getByText('wf-c1')).toBeTruthy())

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  act(() => { h.invalidate() })

  expect(screen.getByText('wf-c1')).toBeTruthy()
  expect(screen.queryByText('Loading child runs…')).toBeNull()

  await act(async () => { resolve([row('c1', { parentInstanceId: 'p1', workflowName: 'wf-c1-v2' })]) })
  await waitFor(() => expect(screen.getByText('wf-c1-v2')).toBeTruthy())
})

it('renders load-more only while more children exist, disabled while its page is in flight', async () => {
  listMock.mockResolvedValueOnce(Array.from({ length: CHILD_PAGE_LIMIT }, (_, i) => row(`c${i}`, { parentInstanceId: 'p1' })))
  render(<Harness rows={[row('p1', { childCount: CHILD_PAGE_LIMIT + 1 })]} />)
  fireEvent.click(screen.getByLabelText('Show child runs of run — p1'))
  const more = await screen.findByRole('button', { name: 'Load more child runs' })

  let resolve!: (v: EnrichedInstanceSummary[]) => void
  listMock.mockReturnValueOnce(new Promise<EnrichedInstanceSummary[]>((r) => { resolve = r }))
  fireEvent.click(more)

  expect(await screen.findByRole('button', { name: 'Loading more child runs…' })).toBeDisabled()
  expect(listMock).toHaveBeenLastCalledWith({ parent: 'p1', limit: CHILD_PAGE_LIMIT, offset: CHILD_PAGE_LIMIT, includeDismissed: true })

  await act(async () => { resolve([row('c100', { parentInstanceId: 'p1', workflowName: 'wf-c100' })]) })
  await waitFor(() => expect(screen.getByText('wf-c100')).toBeTruthy())
  expect(screen.queryByRole('button', { name: /Load more/ })).toBeNull()
  expect(screen.queryByText(/Showing/)).toBeNull()
})
