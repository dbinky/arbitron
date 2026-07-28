# Phase 5 — Task 6: dashboard run hierarchy rendering and the spawn node accent

**Goal:** Render the run list as an expandable tree — roots at the top level, an expander on rows with children, nested child rows to arbitrary depth, each retaining the full per-row action set — and give `spawn_workflow` an accent colour in the graph view.

**Architecture:** A new `HierarchyRows` component owns the recursive rendering so `QueueSection` stays a table-shell component and does not grow unwieldy. Child rows are the SAME `QueueTableRow`, so the per-row actions (inspect, retry, resume, cancel) that deliver the spec's operator outcome need no per-level special-casing. `QueueView` switches its top-level fetches to `parent: 'roots'`. The graph editor needs no new node component: `nodeTypes.ts` maps every Arbitron node kind onto the single `ArbitronNode`, so `spawn_workflow` only needs an accent entry.

**Tech Stack:** React 19, TypeScript, CSS modules, Vitest + `@testing-library/react`, Vite.

---

### Task 6: hierarchy rendering + node accent

**Files:**
- Create: `dashboard/src/components/HierarchyRows.tsx`
- Create: `dashboard/src/components/HierarchyRows.module.css`
- Modify: `dashboard/src/components/QueueTableRow.tsx` (indent + hierarchy expander)
- Modify: `dashboard/src/components/QueueTableRow.module.css`
- Modify: `dashboard/src/components/QueueSection.tsx` (delegate row rendering)
- Modify: `dashboard/src/routes/QueueView.tsx:56-70` (roots-only fetch)
- Modify: `dashboard/src/graph/nodes/ArbitronNode.tsx:8-16` (accent colour)
- Test:   `dashboard/src/components/HierarchyRows.test.tsx`
- Test:   `dashboard/src/routes/QueueView.hierarchy.test.tsx`

---

- [ ] **Step 1: Write the failing tests**

Create `dashboard/src/components/HierarchyRows.test.tsx`:

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
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
  const buttonLabels = (el: HTMLElement) =>
    Array.from(el.querySelectorAll('button')).map((b) => b.getAttribute('aria-label')).sort()
  expect(buttonLabels(childRow)).toEqual(buttonLabels(parentRow))
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
```

Create `dashboard/src/routes/QueueView.hierarchy.test.tsx`:

```tsx
import { render, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'

const listMock = vi.fn()
const meMock = vi.fn()
vi.mock('../api/client', () => ({
  api: {
    instances: { list: (...a: unknown[]) => listMock(...a), getNodeTrace: vi.fn() },
    me: () => meMock(),
  },
}))
vi.mock('../hooks/useSystemWebSocket', () => ({ useSystemWebSocket: () => ({ connected: true }) }))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))

import QueueView from './QueueView'

beforeEach(() => {
  listMock.mockReset().mockResolvedValue([])
  meMock.mockReset().mockResolvedValue({ userId: 'u1', authMode: 'local' })
})

// The top-level list returns ROOTS ONLY: child runs must appear nested under
// their parent, never as separate top-level rows.
it('requests roots only for every top-level section', async () => {
  render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )

  await waitFor(() => expect(listMock).toHaveBeenCalled())
  const calls = listMock.mock.calls.map((c) => c[0] as Record<string, unknown>)
  expect(calls.length).toBeGreaterThanOrEqual(6)
  for (const c of calls) {
    expect(c.parent).toBe('roots')
  }
})
```

- [ ] **Step 2: Run them, confirm they fail**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work/dashboard
npx vitest run src/components/HierarchyRows.test.tsx src/routes/QueueView.hierarchy.test.tsx
```

Expected output:

```
Error: Failed to load url ./HierarchyRows (resolved id: .../src/components/HierarchyRows)
 FAIL  src/components/HierarchyRows.test.tsx
 FAIL  src/routes/QueueView.hierarchy.test.tsx
   → expected undefined to be 'roots'
Test Files  2 failed (2)
```

- [ ] **Step 3: Add the hierarchy expander to `QueueTableRow`**

In `dashboard/src/components/QueueTableRow.tsx`, extend the props:

```tsx
interface QueueTableRowProps {
  row: EnrichedInstanceSummary
  columns: ColumnDef[]
  timeMode: 'elapsed' | 'relative'
  authMode: 'local' | 'org' | 'edge'
  dimmed?: boolean
  expanded?: boolean
  onExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  /** Nesting depth in the run hierarchy; 0 for a root row. */
  depth?: number
  /** Whether this row's children are currently shown. */
  hierarchyExpanded?: boolean
  /** Toggles this row's children. Absent when the row reports no children. */
  onToggleHierarchy?: (id: string) => void
}
```

Destructure the three new props and render the tree affordance inside the FIRST
cell, indented by depth:

```tsx
export default function QueueTableRow({
  row: rowVal,
  columns,
  timeMode,
  authMode,
  dimmed,
  expanded,
  onExpand,
  onDismiss,
  onRestore,
  depth = 0,
  hierarchyExpanded = false,
  onToggleHierarchy,
}: QueueTableRowProps) {
```

```tsx
    <tr
      data-row-id={rowVal.id}
      data-depth={depth}
      className={`${styles.row} ${dimmed ? styles.dimmed : ''}`}
      ...
    >
      {columns.map((col, i) => (
        <td key={col.key} className={styles.cell} data-col={col.key}>
          {i === 0 ? (
            <span className={styles.treeCell} style={{ paddingLeft: `${depth * 16}px` }}>
              {onToggleHierarchy ? (
                <button
                  type="button"
                  className={styles.hierarchyToggle}
                  aria-label={`${hierarchyExpanded ? 'Hide' : 'Show'} child runs of ${rowVal.runName}`}
                  aria-expanded={hierarchyExpanded}
                  onClick={(e) => {
                    e.stopPropagation()
                    onToggleHierarchy(rowVal.id)
                  }}
                >
                  {hierarchyExpanded ? '▾' : '▸'}
                </button>
              ) : (
                <span className={styles.hierarchySpacer} aria-hidden="true" />
              )}
              {cellText(col)}
            </span>
          ) : (
            cellText(col)
          )}
        </td>
      ))}
```

Append to `dashboard/src/components/QueueTableRow.module.css`:

```css
/* Tree affordance in the first cell: a fixed-width toggle (or an equal-width
   spacer, so rows without children still align) followed by the cell text. */
.treeCell {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.hierarchyToggle {
  background: none;
  border: none;
  cursor: pointer;
  padding: 0;
  width: 14px;
  font-size: 11px;
  line-height: 1;
  color: inherit;
  opacity: 0.7;
}

.hierarchyToggle:hover {
  opacity: 1;
}

.hierarchySpacer {
  display: inline-block;
  width: 14px;
}
```

- [ ] **Step 4: Create the recursive row renderer**

Create `dashboard/src/components/HierarchyRows.tsx`:

```tsx
import type { EnrichedInstanceSummary } from '../api/types'
import type { ColumnDef } from './queueColumns'
import type { RunHierarchy } from '../hooks/useRunHierarchy'
import QueueTableRow from './QueueTableRow'
import styles from './HierarchyRows.module.css'

interface HierarchyRowsProps {
  rows: EnrichedInstanceSummary[]
  columns: ColumnDef[]
  depth: number
  timeMode: 'elapsed' | 'relative'
  authMode: 'local' | 'org' | 'edge'
  hierarchy: RunHierarchy
  detailExpandedId?: string | null
  onDetailExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  renderDetail?: (row: EnrichedInstanceSummary) => React.ReactNode
}

/**
 * HierarchyRows renders one level of the run tree and recurses into any
 * expanded row's children.
 *
 * Recursion lives here (rather than in QueueSection) so the section stays a
 * table-shell component. Depth is passed down purely for indentation; nothing
 * in the fetch or render path is depth-aware, so the tree opens to arbitrary
 * depth.
 *
 * Child rows are the SAME QueueTableRow as roots, so the per-row action set —
 * which is what lets operators inspect, retry, resume, or cancel at the CHILD
 * level rather than restarting a larger process — needs no per-level
 * special-casing.
 */
export default function HierarchyRows({
  rows,
  columns,
  depth,
  timeMode,
  authMode,
  hierarchy,
  detailExpandedId,
  onDetailExpand,
  onDismiss,
  onRestore,
  renderDetail,
}: HierarchyRowsProps) {
  return (
    <>
      {rows.map((inst) => {
        const hasChildren = (inst.childCount ?? 0) > 0
        const expanded = hierarchy.isExpanded(inst.id)
        const error = hierarchy.errorOf(inst.id)
        const loading = hierarchy.isLoading(inst.id)

        return (
          <tbody key={inst.id}>
            <QueueTableRow
              row={inst}
              columns={columns}
              timeMode={timeMode}
              authMode={authMode}
              dimmed={inst.dismissed === true}
              expanded={detailExpandedId === inst.id}
              onExpand={onDetailExpand}
              onDismiss={onDismiss}
              onRestore={onRestore}
              depth={depth}
              hierarchyExpanded={expanded}
              // A row reporting no visible children renders NO expander, so a
              // parent whose children are all invisible to this caller leaks no
              // existence.
              onToggleHierarchy={hasChildren ? () => void hierarchy.toggle(inst.id) : undefined}
            />

            {detailExpandedId === inst.id && renderDetail && (
              <tr>
                <td colSpan={columns.length + 1}>{renderDetail(inst)}</td>
              </tr>
            )}

            {expanded && loading && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelStatus}>
                  Loading child runs…
                </td>
              </tr>
            )}

            {/* An expansion failure is confined to THIS row: siblings and the
                rest of the page are unaffected. */}
            {expanded && error && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelError}>
                  {error}
                </td>
              </tr>
            )}

            {expanded && !loading && !error && (
              <HierarchyRows
                rows={hierarchy.childrenOf(inst.id)}
                columns={columns}
                depth={depth + 1}
                timeMode={timeMode}
                authMode={authMode}
                hierarchy={hierarchy}
                detailExpandedId={detailExpandedId}
                onDetailExpand={onDetailExpand}
                onDismiss={onDismiss}
                onRestore={onRestore}
                renderDetail={renderDetail}
              />
            )}
          </tbody>
        )
      })}
    </>
  )
}
```

Create `dashboard/src/components/HierarchyRows.module.css`:

```css
.levelStatus {
  padding: 6px 10px;
  font-size: 12px;
  opacity: 0.7;
}

.levelError {
  padding: 6px 10px;
  font-size: 12px;
  color: #d64545;
}
```

- [ ] **Step 5: Delegate row rendering from `QueueSection`**

In `dashboard/src/components/QueueSection.tsx`:

1. Add the imports:

```tsx
import HierarchyRows from './HierarchyRows'
import { useRunHierarchy } from '../hooks/useRunHierarchy'
```

2. Create the hierarchy inside the component, beside the other state:

```tsx
  const [errorLoading, setErrorLoading] = useState(false)
  const hierarchy = useRunHierarchy()
```

3. Replace the whole `{sorted.map((inst) => ( <tbody key={inst.id}> ... </tbody> ))}`
   block with:

```tsx
              <HierarchyRows
                rows={sorted}
                columns={columns}
                depth={0}
                timeMode={timeMode}
                authMode={authMode}
                hierarchy={hierarchy}
                detailExpandedId={expandedId}
                onDetailExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                onDismiss={onDismiss}
                onRestore={onRestore}
                renderDetail={(inst) => (
                  <RowDetailPanel
                    row={inst}
                    authMode={authMode}
                    errorText={errorText}
                    errorLoading={errorLoading}
                  />
                )}
              />
```

- [ ] **Step 6: Fetch roots only in `QueueView`**

In `dashboard/src/routes/QueueView.tsx`, add `parent: 'roots'` to every
`api.instances.list` call in `fetchAll`:

```tsx
      const [running, waiting, queued, paused, failedRes, completedRes, meResp] =
        await Promise.all([
          // The top-level list returns ROOTS ONLY; child runs appear nested under
          // their parent via the per-row expander, never as separate top-level
          // rows.
          api.instances.list({ state: 'running', parent: 'roots' }),
          api.instances.list({ state: 'waiting', parent: 'roots' }),
          api.instances.list({ state: 'queued', parent: 'roots' }),
          api.instances.list({ state: 'paused', parent: 'roots' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed, parent: 'roots' }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed, parent: 'roots' }),
          api.me(),
        ])
```

- [ ] **Step 7: Give the spawn node an accent colour**

In `dashboard/src/graph/nodes/ArbitronNode.tsx`, add to `typeColors`:

```ts
const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
}
```

No new node component is needed: `nodeTypes.ts` maps every Arbitron node kind
onto the single `ArbitronNode`, which already renders the type label from
`data.nodeType` (`spawn workflow`) and a source/target handle.

- [ ] **Step 8: Run the dashboard gate, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work/dashboard
npx tsc --noEmit && npx vitest run
```

Expected output (the new files plus every pre-existing suite):

```
 ✓ src/components/HierarchyRows.test.tsx (10)
 ✓ src/routes/QueueView.hierarchy.test.tsx (1)
 ✓ src/components/QueueSection.test.tsx (...)
 ✓ src/components/QueueTableRow.test.tsx (...)
 ✓ src/routes/QueueView.test.tsx (...)
 ✓ src/graph/nodes/ArbitronNode.test.tsx (...)
Test Files  N passed (N)
```

> If `QueueSection.test.tsx` / `QueueView.test.tsx` fail because they assert the
> old flat row structure or the old `list()` arguments, update those assertions
> to the new shape (rows now carry `data-depth`; list calls now carry
> `parent: 'roots'`). Do NOT weaken the new tests to accommodate them.

- [ ] **Step 9: Rebuild the embedded SPA**

`adapter/dashboard/dist` is embedded by the Go binary via `//go:embed`, so it
must be rebuilt and committed whenever dashboard source changes.

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
make ui
```

Expected output ends with a successful Vite build and the copy into
`adapter/dashboard/dist`:

```
vite v8.x.x building for production...
✓ built in ...
```

- [ ] **Step 10: Full gate**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
make test-all
```

Expected: the Go suite prints `ok` for every package and the dashboard suite
reports all files passed.

- [ ] **Step 11: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add dashboard/src/components/HierarchyRows.tsx dashboard/src/components/HierarchyRows.module.css dashboard/src/components/HierarchyRows.test.tsx dashboard/src/components/QueueTableRow.tsx dashboard/src/components/QueueTableRow.module.css dashboard/src/components/QueueSection.tsx dashboard/src/routes/QueueView.tsx dashboard/src/routes/QueueView.hierarchy.test.tsx dashboard/src/graph/nodes/ArbitronNode.tsx adapter/dashboard/dist
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(dashboard): expandable run hierarchy and spawn_workflow node accent"
```
