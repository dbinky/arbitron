# Phase 3 — Task 4: Sortable table (extends QueueSection) + QueueTableRow

**Goal:** Replace the current `InstanceRow` grid with a real sortable table: `QueueSection` renders a `<table>` whose visible columns come from `queueColumns` for the current density, with sortable column headers and the Phase-2 default ordering policy (active by elapsed desc, queued by position asc, terminal by updated desc) until the user picks a column. `QueueTableRow` renders one enriched row's visible cells (run name, submitter, status, step, elapsed, position, model/backend, retry) plus an expand affordance. Row click still navigates to `/instances/{id}`. The existing dismissal seam is preserved.

**Architecture:** `QueueSection` is extended (not replaced) — it keeps its collapse + dismissal seams and adds the table rendering. `QueueTableRow` is a new component replacing `InstanceRow` for the queue list. Both read columns from `queueColumns`. The submitter renders as "you" in local mode (from `authMode` prop) and the display name in shared mode. Elapsed is computed client-side `now − createdAt`. The default ordering policy from Phase 2 is implemented as the initial sort; user re-sort overrides it.

**Tech Stack:** TypeScript, React 19, CSS modules, Vitest + `@testing-library/react`.

---

### Task 4: Sortable table + QueueTableRow

**Files:**
- Modify: `dashboard/src/utils/time.ts` (add `localDateTime` viewer-local formatter)
- Create: `dashboard/src/components/QueueTableRow.tsx`
- Create: `dashboard/src/components/QueueTableRow.module.css`
- Create: `dashboard/src/components/QueueTableRow.test.tsx`
- Modify: `dashboard/src/components/QueueSection.tsx` (switch to table rendering using `queueColumns` + `QueueTableRow`)
- Modify: `dashboard/src/components/QueueSection.module.css` (table styles)
- Modify: `dashboard/src/components/QueueSection.test.tsx` (update for table + density)

- [ ] Step 0: Add the `localDateTime` viewer-local formatter

The spec calls out a "submitted **local** time" column; the value on the wire is
a UTC ISO timestamp, so the dashboard must render it in the viewer's local tz
(the design's "rendered in viewer-local tz client-side"). Append to
`dashboard/src/utils/time.ts`:

```ts
/**
 * Formats an ISO timestamp as a compact, viewer-local "YYYY-MM-DD HH:mm"
 * string (local tz via Date's local getters). Returns the input unchanged if
 * it is not a parseable date. Used for the "submitted local time" column and
 * the row-detail "Submitted" field.
 */
export function localDateTime(dateStr: string): string {
  const d = new Date(dateStr)
  if (Number.isNaN(d.getTime())) return dateStr
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
```

Add a quick unit test to `dashboard/src/utils/time.test.ts` (create if absent):

```ts
import { describe, it, expect } from 'vitest'
import { localDateTime } from './time'

describe('localDateTime', () => {
  it('formats an ISO timestamp as YYYY-MM-DD HH:mm', () => {
    expect(localDateTime('2024-06-25T12:00:00Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
  it('returns the input unchanged for an unparseable value', () => {
    expect(localDateTime('not-a-date')).toBe('not-a-date')
  })
})
```

- [ ] Step 1: Write the failing test for `QueueTableRow`

Create `dashboard/src/components/QueueTableRow.test.tsx`:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, it, expect, vi } from 'vitest'
import QueueTableRow from './QueueTableRow'
import { visibleColumns } from './queueColumns'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'

function row(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
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

describe('QueueTableRow', () => {
  it('renders a cell for each visible column', () => {
    renderRow()
    const cells = screen.getAllByRole('cell')
    expect(cells.length).toBe(visibleColumns('balanced').length)
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
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('renders placeholder for absent queue position', () => {
    renderRow({ row: row({ queuePosition: undefined }) })
    expect(screen.getByText('—')).toBeInTheDocument()
  })

  it('navigates to /instances/{id} on row click', () => {
    const onExpand = vi.fn()
    renderRow({ row: row({ id: 'abc-123' }), onExpand })
    fireEvent.click(screen.getByRole('row'))
    // Navigation is handled by react-router; we assert the row is clickable.
    // The expand affordance must NOT trigger navigation (stopPropagation).
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
```

- [ ] Step 2: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/components/QueueTableRow.test.tsx
```

Expected: failure — `Failed to resolve import "./QueueTableRow"`.

- [ ] Step 3: Implement `QueueTableRow`

Create `dashboard/src/components/QueueTableRow.tsx`:

```tsx
import { useNavigate } from 'react-router-dom'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import type { ColumnDef } from './queueColumns'
import { elapsed, relativeTime, localDateTime } from '../utils/time'
import styles from './QueueTableRow.module.css'

interface QueueTableRowProps {
  row: EnrichedInstanceSummary
  columns: ColumnDef[]
  timeMode: 'elapsed' | 'relative'
  authMode: 'local' | 'org' | 'edge'
  dimmed?: boolean
  expanded?: boolean
  onExpand?: (id: string) => void
}

/**
 * QueueTableRow renders one enriched row's visible cells as a <tr>. The
 * submitter renders "you" in local mode and the display name in shared mode.
 * Elapsed is computed client-side (now − createdAt). Row click navigates to
 * the execution view; the expand affordance stops propagation so it does not
 * also navigate.
 */
export default function QueueTableRow({
  row: rowVal,
  columns,
  timeMode,
  authMode,
  dimmed,
  expanded,
  onExpand,
}: QueueTableRowProps) {
  const navigate = useNavigate()

  function cellText(col: ColumnDef): string {
    if (col.key === 'submitter') {
      if (authMode === 'local') return 'you'
      return rowVal.submitterName ?? '—'
    }
    if (col.key === 'elapsed') {
      return timeMode === 'elapsed' ? elapsed(rowVal.createdAt) : relativeTime(rowVal.updatedAt)
    }
    if (col.key === 'submittedAt') {
      // Spec: "submitted local time" — render the createdAt in the viewer's
      // local tz, not the raw UTC ISO. The column accessor keeps the raw ISO
      // so client-side sorting stays a stable date comparison.
      return localDateTime(rowVal.createdAt)
    }
    return col.accessor(rowVal)
  }

  return (
    <tr
      className={`${styles.row} ${dimmed ? styles.dimmed : ''}`}
      onClick={() => navigate(`/instances/${rowVal.id}`)}
      role="row"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          navigate(`/instances/${rowVal.id}`)
        }
      }}
    >
      {columns.map((col) => (
        <td key={col.key} className={styles.cell} data-col={col.key}>
          {cellText(col)}
        </td>
      ))}
      <td className={styles.cell}>
        <button
          type="button"
          className={styles.expand}
          aria-label={expanded ? 'Collapse' : 'Expand'}
          aria-expanded={expanded}
          onClick={(e) => {
            e.stopPropagation()
            onExpand?.(rowVal.id)
          }}
        >
          {expanded ? '▾' : '▸'}
        </button>
      </td>
    </tr>
  )
}
```

Create `dashboard/src/components/QueueTableRow.module.css`:

```css
.row {
  cursor: pointer;
  transition: background 0.15s;
}

.row:hover {
  background: var(--bg-hover);
}

.dimmed {
  opacity: 0.5;
}

.cell {
  padding: 8px 12px;
  font-size: 12px;
  color: var(--text-secondary);
  border-bottom: 1px solid var(--border-subtle);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 200px;
}

.expand {
  border: none;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 12px;
  padding: 2px 4px;
}

.expand:hover {
  color: var(--text-primary);
}
```

- [ ] Step 4: Run the QueueTableRow test, confirm it passes

```bash
cd dashboard && npx vitest run src/components/QueueTableRow.test.tsx
```

Expected: all assertions pass.

- [ ] Step 5: Write the failing test for the extended `QueueSection` (table + density + default ordering)

Replace `dashboard/src/components/QueueSection.test.tsx` with:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'
import type { EnrichedInstanceSummary } from '../api/types'

function summary(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
  return {
    id: 'i1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: 'run — wf',
    retryCount: 0,
    ...overrides,
  }
}

function renderSection(props: Partial<React.ComponentProps<typeof QueueSection>>) {
  return render(
    <MemoryRouter>
      <QueueSection
        title="Failed"
        instances={[]}
        titleColor="red"
        countBg="red"
        countColor="red"
        timeMode="relative"
        density="balanced"
        authMode="local"
        {...props}
      />
    </MemoryRouter>,
  )
}

describe('QueueSection dismiss-all', () => {
  it('renders [Dismiss All] when onDismissAll is provided', () => {
    const onDismissAll = vi.fn()
    renderSection({ onDismissAll })
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss All' }))
    expect(onDismissAll).toHaveBeenCalled()
  })

  it('does not render [Dismiss All] when onDismissAll is absent (Active section)', () => {
    renderSection({ title: 'Active' })
    expect(screen.queryByRole('button', { name: /dismiss all/i })).toBeNull()
  })
})

describe('QueueSection table rendering', () => {
  it('renders a table with column headers from queueColumns', () => {
    renderSection({ instances: [summary()] })
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: /run/i })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: /status/i })).toBeInTheDocument()
  })

  it('renders one row per instance', () => {
    renderSection({
      instances: [summary({ id: 'a' }), summary({ id: 'b' })],
    })
    const rows = screen.getAllByRole('row')
    // 1 header row + 2 body rows
    expect(rows.length).toBe(3)
  })

  it('empty section shows the empty state', () => {
    renderSection({ instances: [] })
    expect(screen.getByText(/no instances/i)).toBeInTheDocument()
  })

  it('default order for active section is elapsed desc (oldest createdAt first)', () => {
    const older = summary({ id: 'old', state: 'running', createdAt: '2024-01-01T00:00:00Z' })
    const newer = summary({ id: 'new', state: 'running', createdAt: '2024-06-01T00:00:00Z' })
    renderSection({ instances: [newer, older], timeMode: 'elapsed' })
    const bodyRows = screen.getAllByRole('row').slice(1) // skip header
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('old')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('new')
  })

  it('default order for queued section is queue position ascending', () => {
    const pos3 = summary({ id: 'p3', state: 'queued', queuePosition: 3 })
    const pos1 = summary({ id: 'p1', state: 'queued', queuePosition: 1 })
    renderSection({ instances: [pos3, pos1], timeMode: 'elapsed' })
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('p1')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('p3')
  })

  it('default order for terminal section is updatedAt desc (most-recent first)', () => {
    const older = summary({ id: 'old', state: 'completed', updatedAt: '2024-01-01T00:00:00Z' })
    const newer = summary({ id: 'new', state: 'completed', updatedAt: '2024-06-01T00:00:00Z' })
    renderSection({ title: 'Completed', instances: [older, newer], timeMode: 'relative' })
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('new')
    expect(bodyRows[1].getAttribute('data-row-id')).toBe('old')
  })

  it('user re-sort by clicking a sortable header overrides the default order', () => {
    const a = summary({ id: 'a', runName: 'A — wf', state: 'running' })
    const b = summary({ id: 'b', runName: 'B — wf', state: 'running' })
    renderSection({ instances: [a, b], timeMode: 'elapsed' })
    fireEvent.click(screen.getByRole('columnheader', { name: /run/i }))
    const bodyRows = screen.getAllByRole('row').slice(1)
    // Ascending by runName: A before B
    expect(bodyRows[0].getAttribute('data-row-id')).toBe('a')
  })

  it('density change hides dense-excluded columns', () => {
    renderSection({ instances: [summary()], density: 'dense' })
    // 'friendlyStep' is excluded in dense; its header should be absent
    expect(screen.queryByRole('columnheader', { name: /step/i })).toBeNull()
  })
})

describe('QueueSection — dismissed row rendering', () => {
  it('renders a restore control for a dismissed terminal instance', () => {
    const onRestore = vi.fn()
    renderSection({
      instances: [summary({ id: 'i1', state: 'completed', dismissed: true })],
      onDismiss: vi.fn(),
      onRestore,
    })
    const btn = screen.getByRole('button', { name: /restore/i })
    fireEvent.click(btn)
    expect(onRestore).toHaveBeenCalledWith('i1')
  })

  it('renders a dismiss control for a non-dismissed terminal instance', () => {
    const onDismiss = vi.fn()
    renderSection({
      instances: [summary({ id: 'i2', state: 'failed', dismissed: false })],
      onDismiss,
      onRestore: vi.fn(),
    })
    const btn = screen.getByRole('button', { name: /dismiss/i })
    fireEvent.click(btn)
    expect(onDismiss).toHaveBeenCalledWith('i2')
  })

  it('does not render a dismiss or restore control for active (running) instances', () => {
    renderSection({
      instances: [summary({ state: 'running' })],
      onDismiss: vi.fn(),
      onRestore: vi.fn(),
    })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })
})
```

- [ ] Step 6: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/components/QueueSection.test.tsx
```

Expected: failure — `QueueSection` still renders the old grid (`InstanceRow`), no `<table>`, no column headers.

- [ ] Step 7: Rewrite `QueueSection` to render the table

Replace `dashboard/src/components/QueueSection.tsx` with:

```tsx
import { useState, useMemo } from 'react'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import { visibleColumns, type ColumnDef } from './queueColumns'
import QueueTableRow from './QueueTableRow'
import styles from './QueueSection.module.css'

interface QueueSectionProps {
  title: string
  instances: EnrichedInstanceSummary[]
  titleColor: string
  countBg: string
  countColor: string
  subtitle?: string
  timeMode: 'elapsed' | 'relative'
  density: QueueDensity
  authMode: 'local' | 'org' | 'edge'
  defaultCollapsed?: boolean
  onDismissAll?: () => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
}

const priorityOrder: Record<string, number> = { high: 0, normal: 1, low: 2 }

/** defaultSortKey picks the initial sort column from the section's state mix. */
function defaultSortKey(instances: EnrichedInstanceSummary[]): string {
  const states = new Set(instances.map((i) => i.state))
  const isQueued = states.has('queued') && instances.every((i) => i.state === 'queued')
  const isActive = instances.some((i) => ['running', 'waiting', 'paused'].includes(i.state))
  if (isQueued) return 'queuePosition'
  if (isActive) return 'elapsed'
  return 'updatedAt' // terminal: most-recent first
}

function compareRows(a: EnrichedInstanceSummary, b: EnrichedInstanceSummary, col: ColumnDef): number {
  const va = col.accessor(a)
  const vb = col.accessor(b)
  if (col.key === 'elapsed' || col.key === 'updatedAt' || col.key === 'submittedAt') {
    return new Date(va).getTime() - new Date(vb).getTime()
  }
  if (col.key === 'queuePosition') {
    const pa = a.queuePosition ?? Number.MAX_SAFE_INTEGER
    const pb = b.queuePosition ?? Number.MAX_SAFE_INTEGER
    return pa - pb
  }
  if (col.key === 'retryCount') {
    return (a.retryCount ?? 0) - (b.retryCount ?? 0)
  }
  if (col.key === 'priority') {
    return (priorityOrder[a.priority] ?? 1) - (priorityOrder[b.priority] ?? 1)
  }
  return va.localeCompare(vb)
}

export default function QueueSection({
  title,
  instances,
  titleColor,
  countBg,
  countColor,
  subtitle,
  timeMode,
  density,
  authMode,
  defaultCollapsed = false,
  onDismissAll,
  onDismiss,
  onRestore,
}: QueueSectionProps) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc')
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const columns = useMemo(() => visibleColumns(density), [density])

  const sorted = useMemo(() => {
    if (sortKey === null) {
      // Default policy (Phase-2): active by elapsed desc (oldest createdAt
      // first), pending by queue position asc (next-to-run on top), terminal
      // by updatedAt desc (most-recent first). Computed directly, NOT via a
      // visible column — the terminal key ('updatedAt') has no column, and the
      // ordering must hold in every density (dense hides several columns).
      const key = defaultSortKey(instances)
      const sortedCopy = [...instances]
      if (key === 'updatedAt') {
        sortedCopy.sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
        return sortedCopy
      }
      if (key === 'queuePosition') {
        sortedCopy.sort(
          (a, b) =>
            (a.queuePosition ?? Number.MAX_SAFE_INTEGER) - (b.queuePosition ?? Number.MAX_SAFE_INTEGER),
        )
        return sortedCopy
      }
      // 'elapsed' (active): oldest createdAt first (longest-running on top).
      sortedCopy.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime())
      return sortedCopy
    }
    const col = columns.find((c) => c.key === sortKey)
    if (!col) return instances
    const sortedCopy = [...instances]
    sortedCopy.sort((a, b) => {
      const cmp = compareRows(a, b, col)
      return sortDir === 'asc' ? cmp : -cmp
    })
    return sortedCopy
  }, [instances, sortKey, sortDir, columns])

  function handleHeaderClick(col: ColumnDef) {
    if (!col.sortable) return
    if (sortKey === col.key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(col.key)
      setSortDir('asc')
    }
  }

  return (
    <div className={styles.section}>
      <div
        className={styles.header}
        onClick={() => setCollapsed((c) => !c)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setCollapsed((c) => !c)
          }
        }}
      >
        <span className={`${styles.arrow} ${collapsed ? '' : styles.arrowExpanded}`}>
          {'▶'}
        </span>
        <span className={styles.title} style={{ color: titleColor }}>{title}</span>
        <span className={styles.count} style={{ background: countBg, color: countColor }}>
          {instances.length}
        </span>
        {subtitle && <span className={styles.subtitle}>{subtitle}</span>}
        {onDismissAll && (
          <button
            type="button"
            className={styles.dismissAll}
            onClick={(e) => { e.stopPropagation(); onDismissAll() }}
          >
            Dismiss All
          </button>
        )}
      </div>

      {!collapsed && (
        <div className={styles.body}>
          {sorted.length === 0 ? (
            <div className={styles.empty}>No instances</div>
          ) : (
            <table className={styles.table}>
              <thead>
                <tr>
                  {columns.map((col) => (
                    <th
                      key={col.key}
                      className={`${styles.th} ${col.sortable ? styles.sortable : ''} ${sortKey === col.key ? styles.sortActive : ''}`}
                      onClick={() => handleHeaderClick(col)}
                      role="columnheader"
                      aria-sort={sortKey === col.key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
                    >
                      {col.header}
                      {sortKey === col.key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}
                    </th>
                  ))}
                  <th className={styles.th} aria-label="expand" />
                </tr>
              </thead>
              <tbody>
                {sorted.map((inst) => (
                  <QueueTableRow
                    key={inst.id}
                    row={inst}
                    columns={columns}
                    timeMode={timeMode}
                    authMode={authMode}
                    dimmed={inst.dismissed === true}
                    expanded={expandedId === inst.id}
                    onExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}
```

Note: the dismissal controls (dismiss/restore buttons) were previously rendered inside `InstanceRow`. In the table layout, they move into the expand/detail panel (Task 5) OR remain as a trailing cell. To preserve the existing test (which looks for dismiss/restore buttons by role), add a trailing dismiss/restore button to `QueueTableRow` when the instance is terminal. Update `QueueTableRow.tsx` to accept `onDismiss`/`onRestore` props and render the control in the trailing cell (next to the expand affordance) for terminal states. Add to `QueueTableRowProps`:

```tsx
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
```

and in the trailing `<td>`, before the expand button:

```tsx
        {isTerminal &&
          (dimmed ? (
            <button
              type="button"
              className={styles.expand}
              aria-label="Restore"
              title="Restore"
              onClick={(e) => { e.stopPropagation(); onRestore?.(rowVal.id) }}
            >
              {'↺'}
            </button>
          ) : (
            onDismiss && (
              <button
                type="button"
                className={styles.expand}
                aria-label="Dismiss"
                title="Dismiss"
                onClick={(e) => { e.stopPropagation(); onDismiss(rowVal.id) }}
              >
                {'×'}
              </button>
            )
          ))}
```

where `isTerminal` is computed at the top of the component:

```tsx
  const isTerminal = rowVal.state === 'failed' || rowVal.state === 'completed'
```

Thread `onDismiss`/`onRestore` from `QueueSection` into each `QueueTableRow`. Add `data-row-id={inst.id}` to the `<tr>` in `QueueTableRow` so the sort-order tests can assert row identity:

```tsx
    <tr
      data-row-id={rowVal.id}
      ...
```

- [ ] Step 8: Add the table styles to `QueueSection.module.css`

Append to `dashboard/src/components/QueueSection.module.css`:

```css
.table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
}

.th {
  padding: 6px 12px;
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border-color);
  user-select: none;
}

.sortable {
  cursor: pointer;
}

.sortable:hover {
  color: var(--text-primary);
}

.sortActive {
  color: var(--text-primary);
}
```

- [ ] Step 9: Run the QueueSection tests, confirm they pass

```bash
cd dashboard && npx vitest run src/components/QueueSection.test.tsx src/components/QueueTableRow.test.tsx
```

Expected: all assertions pass.

- [ ] Step 10: Run typecheck

```bash
cd dashboard && npx tsc --noEmit
```

Expected: no type errors.

- [ ] Step 11: Commit

```bash
git add dashboard/src/utils/time.ts dashboard/src/utils/time.test.ts dashboard/src/components/QueueTableRow.tsx dashboard/src/components/QueueTableRow.module.css dashboard/src/components/QueueTableRow.test.tsx dashboard/src/components/QueueSection.tsx dashboard/src/components/QueueSection.module.css dashboard/src/components/QueueSection.test.tsx
git commit -m "feat(dashboard): replace InstanceRow grid with sortable density-aware queue table"
```