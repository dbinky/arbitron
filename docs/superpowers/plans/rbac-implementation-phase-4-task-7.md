# RBAC Phase 4 — Task 7: DashboardView — tiles, per-workflow table, selector, live refresh

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `DashboardView` shell with the real dashboard: load `/me` once for the department list, render the `DepartmentSelector`, fetch `dashboard.stats` for the active selection, render queue-health / outcomes tiles (reusing the `UsageStats` `.grid/.stat/.statValue` pattern), a throughput bar strip, and a ranked per-workflow table, with **debounced live refresh** via `useSystemWebSocket`.

**Architecture:** `DashboardView` is the stateful container. It loads `/me` once on mount, derives the selector, and refetches stats whenever the selection changes or a relevant `instance.*` websocket event arrives (debounced). Pure formatting helpers (`formatPercent`, `formatDuration`, `formatThroughput`) are extracted so they can be unit-tested without rendering. Tiles reuse the proven `UsageStats` CSS pattern via a new `DashboardView.module.css` mirroring `.grid/.stat/.statValue`.

**Tech Stack:** React 19 + TypeScript, `useSystemWebSocket` hook, the Task 4 client, the Task 6 `DepartmentSelector`, Vitest + Testing Library (mocking `api` and the websocket hook).

**Dependencies:** Task 4 (`api.me`, `api.dashboard.stats`, types), Task 5 (the route/shell this replaces), Task 6 (`DepartmentSelector`). Backend Tasks 1-3 must be deployed for live data, but the frontend tests mock `api`.

---

## Context You Need

- `dashboard/src/components/UsageStats.tsx` + `UsageStats.module.css`: the tile pattern to reuse — `.grid` (2-col), `.stat`, `.statLabel`, `.statValue`, `.statValueSuccess`, `.statValueDanger`, plus `.loading`/`.error`. Mirror these class names in a new module so the look matches.
- `dashboard/src/routes/QueueView.tsx`: the canonical live-refresh pattern — `useSystemWebSocket(useCallback(handler))`, a `STATE_CHANGE_EVENTS` set, a `debounceRef` with `DEBOUNCE_MS = 300`, and cleanup on unmount. Reuse this exact shape.
- `dashboard/src/api/types.ts`: `Me`, `DashboardStats`, `DashboardQueueHealth`, `DashboardOutcomes`, `ThroughputBucket`, `WorkflowBreakdown` (Task 4).
- `dashboard/src/api/client.ts`: `api.me()`, `api.dashboard.stats(departments, window)` (Task 4). `departments` is `string[] | null`.
- `dashboard/src/components/DepartmentSelector.tsx` (Task 6): props `{ departments, selection, onChange }`, selection `string[] | null`.
- The fixed window for v1 is `"24h"` (the design's default). No window selector is required by the spec; keep it a constant.

## File Structure

- Create `dashboard/src/routes/dashboardFormat.ts` — pure formatting helpers (unit-tested).
- Create `dashboard/src/routes/dashboardFormat.test.ts` — formatter scenarios.
- Create `dashboard/src/routes/DashboardView.module.css` — tile + table styling (mirrors `UsageStats`).
- Modify `dashboard/src/routes/DashboardView.tsx` — full container (replaces the Task 5 shell).
- Create `dashboard/src/routes/DashboardView.test.tsx` — behavior scenarios with `api` + websocket mocked.

---

### Task 7a: Pure formatting helpers

**Files:**
- Create: `dashboard/src/routes/dashboardFormat.ts`
- Test: `dashboard/src/routes/dashboardFormat.test.ts`

- [ ] **Step 1: Write the failing formatter tests**

Create `dashboard/src/routes/dashboardFormat.test.ts`:

```typescript
import { describe, it, expect } from 'vitest'
import { formatPercent, formatDuration, throughputTotal } from './dashboardFormat'
import type { ThroughputBucket } from '../api/types'

describe('formatPercent', () => {
  it('renders a 0..1 rate as a one-decimal percentage', () => {
    expect(formatPercent(0.8)).toBe('80.0%')
    expect(formatPercent(0)).toBe('0.0%')
    expect(formatPercent(0.125)).toBe('12.5%')
  })
})

describe('formatDuration', () => {
  it('renders sub-minute durations in seconds', () => {
    expect(formatDuration(12.3)).toBe('12.3s')
  })
  it('renders minute+ durations as m s', () => {
    expect(formatDuration(95)).toBe('1m 35s')
  })
  it('renders zero as 0.0s', () => {
    expect(formatDuration(0)).toBe('0.0s')
  })
})

describe('throughputTotal', () => {
  it('sums runs across buckets', () => {
    const buckets: ThroughputBucket[] = [
      { bucket: '2026-06-04T10:00:00Z', runs: 3 },
      { bucket: '2026-06-04T11:00:00Z', runs: 5 },
    ]
    expect(throughputTotal(buckets)).toBe(8)
  })
  it('returns 0 for an empty series', () => {
    expect(throughputTotal([])).toBe(0)
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd dashboard && npx vitest run src/routes/dashboardFormat.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the helpers**

Create `dashboard/src/routes/dashboardFormat.ts`:

```typescript
import type { ThroughputBucket } from '../api/types'

/** Renders a 0..1 rate as a one-decimal percentage string, e.g. 0.8 → "80.0%". */
export function formatPercent(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`
}

/** Renders a duration in seconds as "12.3s" or "1m 35s". Mirrors UsageStats. */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}m ${secs.toFixed(0)}s`
}

/** Total runs across all throughput buckets. */
export function throughputTotal(buckets: ThroughputBucket[]): number {
  return buckets.reduce((sum, b) => sum + b.runs, 0)
}
```

- [ ] **Step 4: Run the formatter tests to verify they pass**

Run: `cd dashboard && npx vitest run src/routes/dashboardFormat.test.ts`
Expected: PASS

---

### Task 7b: Tile + table CSS

**Files:**
- Create: `dashboard/src/routes/DashboardView.module.css`

- [ ] **Step 5: Create the CSS module (mirrors UsageStats)**

Create `dashboard/src/routes/DashboardView.module.css`:

```css
.container {
  padding: 16px;
}

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.sectionTitle {
  font-size: 11px;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.3px;
  margin: 16px 0 8px;
}

.grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
}

.stat {
  background: var(--bg-secondary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  padding: 12px;
}

.statLabel {
  font-size: 11px;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.3px;
  margin-bottom: 4px;
}

.statValue {
  font-size: 20px;
  font-weight: 700;
  color: var(--text-primary);
}

.statValueSuccess { color: var(--success); }
.statValueDanger { color: var(--danger); }

/* Failures-needing-attention tile is visually emphasized. */
.statEmphasis {
  border-color: var(--danger);
}

.throughput {
  display: flex;
  align-items: flex-end;
  gap: 2px;
  height: 60px;
  padding: 8px 0;
}

.bar {
  flex: 1;
  background: var(--accent);
  min-height: 2px;
  border-radius: 2px 2px 0 0;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.table th,
.table td {
  text-align: left;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border-subtle);
}

.table th {
  color: var(--text-muted);
  text-transform: uppercase;
  font-size: 10px;
  letter-spacing: 0.3px;
}

.loading,
.error {
  color: var(--text-muted);
  font-size: 12px;
  text-align: center;
  padding: 20px 0;
}

.error {
  color: var(--state-failed);
}
```

---

### Task 7c: The DashboardView container

**Files:**
- Modify: `dashboard/src/routes/DashboardView.tsx`
- Test: `dashboard/src/routes/DashboardView.test.tsx`

- [ ] **Step 6: Write the failing container tests**

Create `dashboard/src/routes/DashboardView.test.tsx`:

```tsx
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { DashboardStats, Me } from '../api/types'

// Capture the websocket handler so tests can fire synthetic events.
let wsHandler: ((event: { type: string; timestamp: string; data: unknown }) => void) | null = null
vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (onEvent: (e: { type: string; timestamp: string; data: unknown }) => void) => {
    wsHandler = onEvent
    return { connected: true }
  },
}))

const meMock = vi.fn<[], Promise<Me>>()
const statsMock = vi.fn<[string[] | null, string], Promise<DashboardStats>>()
vi.mock('../api/client', () => ({
  api: {
    me: () => meMock(),
    dashboard: { stats: (d: string[] | null, w: string) => statsMock(d, w) },
  },
}))

import DashboardView from './DashboardView'

function me(overrides: Partial<Me> = {}): Me {
  return { userId: 'u1', username: 'a', role: 'user', departments: ['engineering'], ledDepartments: [], isAdmin: false, ...overrides }
}

function stats(overrides: Partial<DashboardStats> = {}): DashboardStats {
  return {
    departments: ['engineering'],
    queue: { running: 2, queued: 4, waiting: 1, paused: 0, failedNeedingAttention: 3 },
    throughput: [{ bucket: '2026-06-04T10:00:00Z', runs: 5 }],
    outcomes: { totalRuns: 10, successRate: 0.7, failureRate: 0.3, avgDurationSeconds: 12, p95DurationSeconds: 30 },
    perWorkflow: [{ workflowName: 'triage', runs: 10, failureRate: 0.3, avgDurationSeconds: 12 }],
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  wsHandler = null
  vi.useRealTimers()
})

describe('DashboardView', () => {
  it('renders queue-health and outcome tiles from the stats response', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)

    expect(await screen.findByText('70.0%')).toBeInTheDocument()       // success rate
    expect(screen.getByText('3')).toBeInTheDocument()                   // failures needing attention
    expect(screen.getByText('triage')).toBeInTheDocument()              // per-workflow row
  })

  it('shows no department selector for a single-department user', async () => {
    meMock.mockResolvedValue(me({ departments: ['engineering'] }))
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await screen.findByText('70.0%')
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('shows a union-default selector for a multi-department user and refetches on focus change', async () => {
    meMock.mockResolvedValue(me({ departments: ['engineering', 'sales'] }))
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await screen.findByText('70.0%')

    const select = screen.getByRole('combobox')
    expect(select).toHaveValue('__all__')
    // initial load used union default ⇒ null departments
    expect(statsMock).toHaveBeenCalledWith(null, '24h')

    fireEvent.change(select, { target: { value: 'sales' } })
    await waitFor(() => expect(statsMock).toHaveBeenCalledWith(['sales'], '24h'))
  })

  it('debounces a refetch when a relevant instance websocket event arrives', async () => {
    vi.useFakeTimers()
    meMock.mockResolvedValue(me())
    statsMock.mockResolvedValue(stats())
    render(<DashboardView />)
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(1))

    // Fire 3 events rapidly; only one refetch should follow after the debounce.
    wsHandler?.({ type: 'instance.completed', timestamp: '', data: {} })
    wsHandler?.({ type: 'instance.failed', timestamp: '', data: {} })
    wsHandler?.({ type: 'instance.created', timestamp: '', data: {} })

    vi.advanceTimersByTime(300)
    await vi.waitFor(() => expect(statsMock).toHaveBeenCalledTimes(2))
  })

  it('surfaces an error when stats loading fails', async () => {
    meMock.mockResolvedValue(me())
    statsMock.mockRejectedValue(new Error('boom'))
    render(<DashboardView />)
    expect(await screen.findByText(/boom/)).toBeInTheDocument()
  })
})
```

- [ ] **Step 7: Run it to verify it fails**

Run: `cd dashboard && npx vitest run src/routes/DashboardView.test.tsx`
Expected: FAIL — the shell `DashboardView` renders only a heading; assertions miss.

- [ ] **Step 8: Implement the full container**

Replace the contents of `dashboard/src/routes/DashboardView.tsx`:

```tsx
import { useState, useEffect, useCallback, useRef } from 'react'
import type { DashboardStats, Me, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import DepartmentSelector from '../components/DepartmentSelector'
import { formatPercent, formatDuration, throughputTotal } from './dashboardFormat'
import styles from './DashboardView.module.css'

/** Fixed observation window for v1 (design default). */
const WINDOW = '24h'
const DEBOUNCE_MS = 300

/** Instance lifecycle events that warrant a stats refetch. */
const STATE_CHANGE_EVENTS = new Set([
  'instance.created',
  'instance.started',
  'instance.completed',
  'instance.failed',
  'instance.cancelled',
  'instance.paused',
  'instance.resumed',
  'instance.retrying',
  'instance.recovered',
])

export default function DashboardView() {
  const [me, setMe] = useState<Me | null>(null)
  // null ⇒ "all my departments" union default; [dept] ⇒ focused.
  const [selection, setSelection] = useState<string[] | null>(null)
  const [stats, setStats] = useState<DashboardStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Load /me once on mount for the department list + role.
  useEffect(() => {
    let cancelled = false
    api
      .me()
      .then((m) => { if (!cancelled) setMe(m) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load identity') })
    return () => { cancelled = true }
  }, [])

  const fetchStats = useCallback(async () => {
    try {
      const data = await api.dashboard.stats(selection, WINDOW)
      setStats(data)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load dashboard stats')
    } finally {
      setLoading(false)
    }
  }, [selection])

  // Fetch stats once /me is known and whenever the selection changes.
  useEffect(() => {
    if (!me) return
    void fetchStats()
  }, [me, fetchStats])

  // Debounced live refresh on relevant websocket events.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (!STATE_CHANGE_EVENTS.has(event.type)) return
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => { void fetchStats() }, DEBOUNCE_MS)
      },
      [fetchStats],
    ),
  )
  useEffect(() => () => { if (debounceRef.current) clearTimeout(debounceRef.current) }, [])

  if (error) {
    return <div className={styles.container}><div className={styles.error}>{error}</div></div>
  }
  if (loading || !stats) {
    return <div className={styles.container}><div className={styles.loading}>Loading dashboard...</div></div>
  }

  const { queue, outcomes, throughput, perWorkflow } = stats
  const maxRuns = Math.max(1, ...throughput.map((b) => b.runs))

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <span className={styles.title}>Dashboard</span>
        {me && (
          <DepartmentSelector departments={me.departments} selection={selection} onChange={setSelection} />
        )}
      </div>

      <div className={styles.sectionTitle}>Queue Health</div>
      <div className={styles.grid}>
        <Tile label="Running" value={queue.running} />
        <Tile label="Queued / Backlog" value={queue.queued} />
        <Tile label="Waiting / Paused" value={queue.waiting + queue.paused} />
        <Tile label="Failures Needing Attention" value={queue.failedNeedingAttention} emphasis danger />
      </div>

      <div className={styles.sectionTitle}>Throughput ({throughputTotal(throughput)} runs)</div>
      <div className={styles.throughput}>
        {throughput.map((b) => (
          <div
            key={b.bucket}
            className={styles.bar}
            style={{ height: `${(b.runs / maxRuns) * 100}%` }}
            title={`${b.bucket}: ${b.runs}`}
          />
        ))}
      </div>

      <div className={styles.sectionTitle}>Outcomes</div>
      <div className={styles.grid}>
        <Tile label="Total Runs" value={outcomes.totalRuns} />
        <Tile label="Success Rate" valueText={formatPercent(outcomes.successRate)} success />
        <Tile label="Failure Rate" valueText={formatPercent(outcomes.failureRate)} danger />
        <Tile
          label="Avg / p95 Duration"
          valueText={`${formatDuration(outcomes.avgDurationSeconds)} / ${formatDuration(outcomes.p95DurationSeconds)}`}
        />
      </div>

      <div className={styles.sectionTitle}>Per-Workflow</div>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Workflow</th>
            <th>Runs</th>
            <th>Failure Rate</th>
            <th>Avg Duration</th>
          </tr>
        </thead>
        <tbody>
          {perWorkflow.map((wf) => (
            <tr key={wf.workflowName}>
              <td>{wf.workflowName}</td>
              <td>{wf.runs}</td>
              <td>{formatPercent(wf.failureRate)}</td>
              <td>{formatDuration(wf.avgDurationSeconds)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

interface TileProps {
  label: string
  value?: number
  valueText?: string
  success?: boolean
  danger?: boolean
  emphasis?: boolean
}

function Tile({ label, value, valueText, success, danger, emphasis }: TileProps) {
  const valueClass = [
    styles.statValue,
    success ? styles.statValueSuccess : '',
    danger ? styles.statValueDanger : '',
  ].join(' ').trim()
  return (
    <div className={`${styles.stat} ${emphasis ? styles.statEmphasis : ''}`}>
      <div className={styles.statLabel}>{label}</div>
      <div className={valueClass}>{valueText ?? value}</div>
    </div>
  )
}
```

- [ ] **Step 9: Run the container tests to verify they pass**

Run: `cd dashboard && npx vitest run src/routes/DashboardView.test.tsx`
Expected: PASS (all scenarios green). If the debounce test is flaky under fake timers, ensure `vi.useFakeTimers()` is set BEFORE `render` in that test (it is) and that the initial `await vi.waitFor(...)` resolves the first fetch before advancing time.

- [ ] **Step 10: Type-check, lint, build, full suite**

Run: `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build`
Expected: exit 0; all frontend tests PASS; Vite build succeeds.

- [ ] **Step 11: Commit**

```bash
git add dashboard/src/routes/dashboardFormat.ts dashboard/src/routes/dashboardFormat.test.ts \
  dashboard/src/routes/DashboardView.module.css dashboard/src/routes/DashboardView.tsx \
  dashboard/src/routes/DashboardView.test.tsx
git commit -m "feat(dashboard): DashboardView tiles + per-workflow table + selector + live refresh

Loads /me once for the department list, renders queue-health/outcome tiles
(reusing the UsageStats pattern), a throughput bar strip, and a ranked
per-workflow table; refetches the active department's stats on a debounced
instance.* websocket event.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec/design coverage: tiles (queue health with emphasized failures, throughput strip, outcomes incl. avg+p95, per-workflow ranked table) — all four design tiles present; selector driven by `/me`; debounced live refresh via `useSystemWebSocket` mirroring `QueueView`.
- TDD categories: happy-path (tiles render from typed response), edge (single-dept ⇒ no selector; union default value), authorization-ish (multi-dept selection narrows the query param; server enforces the real clamp), live (debounced refetch fires once for a burst of events), error (stats failure surfaces).
- Multi-department + admin: the admin's `/me.departments` (their full observed/effective set) flows straight into `DepartmentSelector`, satisfying "admins may pick any" without special-casing the view. If Phase 1's `/me` returns a separate admin roster field later, swap the prop source then — the selector API is unchanged.
- Window is a constant `"24h"` (design default); no window selector is in scope for this phase.
