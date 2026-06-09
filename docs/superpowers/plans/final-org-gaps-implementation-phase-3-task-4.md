# Final Org Gaps — Phase 3, Task 4: Dashboard Read-Only Schedule & Run-Origin Surfaces

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface, READ-ONLY, (a) a per-workflow `SchedulesPanel` (cron, timezone, creator, enabled/disabled, last-fired) in the workflow review view, and (b) a run-origin badge (`manual` / `scheduled` / `run-now`, plus the schedule reference + attributed creator/department) on run-history rows. No create/edit/enable/disable/delete/run-now control anywhere.

**Architecture:** Extend the TS API layer first (`InstanceSummary` gains `triggerSource` + optional `scheduleId`; a new `Schedule` type + `api.schedules.list(workflowName?)` client method matching the backend `Schedule.WireMap` and `GET /schedules?workflowName=`). Then add two presentational components — `OriginBadge` (used inside `InstanceRow`) and `SchedulesPanel` (rendered in `WorkflowReview`). Both are pure read-only; the panel fetches its own data and degrades to an empty/error state. Frontend tested with vitest + @testing-library/react (jsdom), the established dashboard test stack.

**Tech Stack:** React + TypeScript, Vite, Vitest, @testing-library/react, CSS modules.

---

## DEPENDENCY

Requires **Phase 3 Task 2** for the backend to actually emit `triggerSource`/`scheduleId` on `GET /instances`, and **Phase 3 Task 3** for `GET /schedules?workflowName=`. The dashboard code can be WRITTEN against the agreed JSON shapes before those land (vitest mocks the fetch), but the end-to-end surfaces only light up once Tasks 2 + 3 are merged. Independent of Phases 1 (except indirectly: run-now rows only appear once Phase 1 + Task 1 stamp `run_now`) and 2.

## Context (existing code you must match)

- `dashboard/src/api/types.ts` — `InstanceSummary` (line 15). Add `triggerSource` + optional `scheduleId`; add a new `Schedule` interface.
- `dashboard/src/api/client.ts` — the `api` object (ends line ~ with `notifications`). Add an `api.schedules` group with `list(workflowName?)`. `request<T>(url)` helper already exists.
- `dashboard/src/components/InstanceRow.tsx` — the run-history row. Add the origin badge next to `StateBadge`. `InstanceRow.test.tsx` shows the test pattern (`render` in `MemoryRouter`, `summary()` factory).
- `dashboard/src/components/StateBadge.tsx` — the badge style reference (`<span className={...}>{label}</span>` + a `*.module.css`).
- `dashboard/src/components/VariablesPanel.tsx` — read-only table panel style reference (empty state + `<table>`).
- `dashboard/src/routes/WorkflowReview.tsx` — uses `useParams` for the workflow name; renders panels (`ResizablePanel`, `UsageStats`). Render `SchedulesPanel` here.
- Test stack: vitest (`globals: true`, `jsdom`, `setupFiles: ./src/test-setup.ts`). Mock the API by `vi.fn()`-ing `globalThis.fetch` (see `client.test.ts`) OR by `vi.mock('../api/client')` for component tests. Run with `npm test` (which is `vitest run`).

---

### Task 1: Extend TS types

**Files:**
- Modify: `dashboard/src/api/types.ts` (`InstanceSummary` line 15; add `Schedule`)

- [ ] **Step 1: Add a `TriggerSource` type + the two summary fields**

In `dashboard/src/api/types.ts`, after the `Priority` type (line 12), add:

```typescript
/** How a run was started. Mirrors the backend instance.TriggerSource. */
export type TriggerSource = 'manual' | 'schedule' | 'run_now'
```

In `InstanceSummary` (line 15), after `priority: Priority`, add:

```typescript
  /** How this run was started. Backend defaults legacy rows to 'manual'. */
  triggerSource?: TriggerSource
  /** Originating schedule id for schedule/run_now runs; absent for manual. */
  scheduleId?: string
```

(`triggerSource` is optional so summaries from a pre-Task-2 backend still type-check; the badge component defaults a missing value to `manual`.)

- [ ] **Step 2: Add the `Schedule` type**

At an appropriate place in `dashboard/src/api/types.ts` (e.g. after the instance types), add:

```typescript
/**
 * Read-only schedule projection, matching the backend Schedule.WireMap. The
 * dashboard never mutates schedules — management stays on REST + MCP.
 */
export interface Schedule {
  id: string
  workflowName: string
  cronExpr: string
  timezone: string
  creatorId: string
  department: string
  enabled: boolean
  /** ISO timestamp of the last fire, or null/absent if never fired. */
  lastFiredAt?: string | null
  createdAt: string
  updatedAt: string
}
```

- [ ] **Step 3: Typecheck**

Run: `cd dashboard && npx tsc --noEmit`
Expected: exit 0 (no type errors).

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/api/types.ts
git commit -m "feat(dashboard): add TriggerSource, schedule fields, and Schedule type"
```

---

### Task 2: API client `schedules.list`

**Files:**
- Modify: `dashboard/src/api/client.ts` (add `schedules` group; add `Schedule` to the type import)
- Test: `dashboard/src/api/client.test.ts` (add cases)

- [ ] **Step 5: Write the failing client test**

In `dashboard/src/api/client.test.ts`, add a describe block (model on the existing `globalThis.fetch = vi.fn()` pattern). The backend returns `{ "schedules": [...] }`, so the client must unwrap it:

```typescript
describe('api.schedules.list', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ schedules: [{ id: 's1', workflowName: 'wf-a' }] }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('GETs /schedules?workflowName= and unwraps the schedules array', async () => {
    const out = await api.schedules.list('wf-a')
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/schedules?workflowName=wf-a')
    expect(out).toEqual([{ id: 's1', workflowName: 'wf-a' }])
  })

  it('omits the query when no workflow name is given', async () => {
    await api.schedules.list()
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/schedules')
  })
})
```

- [ ] **Step 6: Run to verify it fails**

Run: `cd dashboard && npx vitest run src/api/client.test.ts -t schedules`
Expected: FAIL — `api.schedules` is undefined.

- [ ] **Step 7: Implement the `schedules` client group**

In `dashboard/src/api/client.ts`, add `Schedule` to the type import block (top of file):

```typescript
  Schedule,
```

Then add a `schedules` group to the `api` object (e.g. just before `notifications:`):

```typescript
  schedules: {
    /**
     * List schedules, optionally narrowed to one workflow. READ-ONLY: the
     * dashboard exposes no schedule mutation — create/edit/enable/disable/
     * delete/run-now stay on REST + MCP. The backend wraps the array in
     * { schedules: [...] } and scopes results to the caller's own (or, for an
     * admin, all) schedules.
     */
    async list(workflowName?: string): Promise<Schedule[]> {
      const q = new URLSearchParams()
      if (workflowName) q.set('workflowName', workflowName)
      const qs = q.toString()
      const res = await request<{ schedules: Schedule[] }>(
        `/api/v1/schedules${qs ? `?${qs}` : ''}`,
      )
      return res.schedules
    },
  },
```

- [ ] **Step 8: Run to verify it passes**

Run: `cd dashboard && npx vitest run src/api/client.test.ts -t schedules`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add dashboard/src/api/client.ts dashboard/src/api/client.test.ts
git commit -m "feat(dashboard): api.schedules.list(workflowName?) read-only client"
```

---

### Task 3: `OriginBadge` component (run-origin annotation)

**Files:**
- Create: `dashboard/src/components/OriginBadge.tsx`
- Create: `dashboard/src/components/OriginBadge.module.css`
- Create (test): `dashboard/src/components/OriginBadge.test.tsx`

- [ ] **Step 10: Write the failing component test (renders three sources distinctly + schedule ref)**

Create `dashboard/src/components/OriginBadge.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import OriginBadge from './OriginBadge'

describe('OriginBadge', () => {
  it('renders a "manual" label for manual runs', () => {
    render(<OriginBadge triggerSource="manual" />)
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
  })

  it('renders a "scheduled" label for schedule runs', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="sched-1" />)
    expect(screen.getByText(/scheduled/i)).toBeInTheDocument()
  })

  it('renders a "run now" label for run_now runs', () => {
    render(<OriginBadge triggerSource="run_now" scheduleId="sched-1" />)
    expect(screen.getByText(/run.?now/i)).toBeInTheDocument()
  })

  it('defaults a missing triggerSource to manual', () => {
    render(<OriginBadge />)
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
  })

  it('exposes the originating schedule id for schedule-originated runs', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="sched-42" />)
    // The schedule reference is surfaced (title attribute or visible text).
    expect(screen.getByTitle(/sched-42/)).toBeInTheDocument()
  })

  it('renders no schedule reference for manual runs', () => {
    render(<OriginBadge triggerSource="manual" />)
    expect(screen.queryByTitle(/sched/)).toBeNull()
  })

  it('renders NO mutation controls (read-only)', () => {
    render(<OriginBadge triggerSource="schedule" scheduleId="s1" />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
```

- [ ] **Step 11: Run to verify it fails**

Run: `cd dashboard && npx vitest run src/components/OriginBadge.test.tsx`
Expected: FAIL — module `./OriginBadge` not found.

- [ ] **Step 12: Implement `OriginBadge`**

Create `dashboard/src/components/OriginBadge.module.css`:

```css
.badge {
  display: inline-flex;
  align-items: center;
  padding: 0.1rem 0.45rem;
  border-radius: 0.6rem;
  font-size: 0.72rem;
  font-weight: 600;
  text-transform: lowercase;
  line-height: 1.4;
}

.manual {
  background: #2a2f3a;
  color: #9aa4b2;
}

.schedule {
  background: #1e3a5f;
  color: #7fb2ff;
}

.runNow {
  background: #3a2f1e;
  color: #ffcf7f;
}
```

Create `dashboard/src/components/OriginBadge.tsx`:

```tsx
import type { TriggerSource } from '../api/types'
import styles from './OriginBadge.module.css'

interface OriginBadgeProps {
  /** Defaults to 'manual' when absent (legacy/pre-migration rows). */
  triggerSource?: TriggerSource
  /** Originating schedule id; shown as a reference for schedule/run_now runs. */
  scheduleId?: string
}

const LABELS: Record<TriggerSource, string> = {
  manual: 'manual',
  schedule: 'scheduled',
  run_now: 'run now',
}

const CLASSES: Record<TriggerSource, string> = {
  manual: styles.manual,
  schedule: styles.schedule,
  run_now: styles.runNow,
}

/**
 * Read-only annotation of how a run was started. For schedule-originated runs
 * the originating schedule id is surfaced via the title attribute. This is a
 * pure presentational badge — it has NO mutation controls (schedule management
 * stays on REST + MCP).
 */
export default function OriginBadge({ triggerSource, scheduleId }: OriginBadgeProps) {
  const source: TriggerSource = triggerSource ?? 'manual'
  const fromSchedule = source === 'schedule' || source === 'run_now'
  const title =
    fromSchedule && scheduleId ? `From schedule ${scheduleId}` : undefined
  return (
    <span className={`${styles.badge} ${CLASSES[source]}`} title={title}>
      {LABELS[source]}
    </span>
  )
}
```

- [ ] **Step 13: Run to verify it passes**

Run: `cd dashboard && npx vitest run src/components/OriginBadge.test.tsx`
Expected: PASS (all seven tests).

- [ ] **Step 14: Commit**

```bash
git add dashboard/src/components/OriginBadge.tsx dashboard/src/components/OriginBadge.module.css dashboard/src/components/OriginBadge.test.tsx
git commit -m "feat(dashboard): OriginBadge for run-origin annotation"
```

---

### Task 4: Render `OriginBadge` in `InstanceRow`

**Files:**
- Modify: `dashboard/src/components/InstanceRow.tsx` (add the badge next to `StateBadge`)
- Test: `dashboard/src/components/InstanceRow.test.tsx` (add origin cases)

- [ ] **Step 15: Write the failing test for the badge in the row**

In `dashboard/src/components/InstanceRow.test.tsx`, add:

```tsx
describe('InstanceRow origin annotation', () => {
  it('shows a scheduled badge with the schedule reference for schedule runs', () => {
    renderRow({
      instance: summary({ triggerSource: 'schedule', scheduleId: 'sched-7' }),
    })
    expect(screen.getByText(/scheduled/i)).toBeInTheDocument()
    expect(screen.getByTitle(/sched-7/)).toBeInTheDocument()
  })

  it('shows a manual badge (and no schedule ref) for manual runs', () => {
    renderRow({ instance: summary({ triggerSource: 'manual' }) })
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
    expect(screen.queryByTitle(/sched/)).toBeNull()
  })

  it('defaults a row with no triggerSource to a manual badge', () => {
    renderRow({ instance: summary() })
    expect(screen.getByText(/manual/i)).toBeInTheDocument()
  })
})
```

- [ ] **Step 16: Run to verify it fails**

Run: `cd dashboard && npx vitest run src/components/InstanceRow.test.tsx -t origin`
Expected: FAIL — no "scheduled"/"manual" text rendered.

- [ ] **Step 17: Render the badge in `InstanceRow`**

In `dashboard/src/components/InstanceRow.tsx`, add the import:

```tsx
import OriginBadge from './OriginBadge'
```

Then, immediately after the `<StateBadge state={instance.state} />` line, add:

```tsx
      <OriginBadge triggerSource={instance.triggerSource} scheduleId={instance.scheduleId} />
```

- [ ] **Step 18: Run to verify it passes (and the existing row tests still pass)**

Run: `cd dashboard && npx vitest run src/components/InstanceRow.test.tsx`
Expected: PASS (origin cases + the pre-existing dismissal cases).

- [ ] **Step 19: Commit**

```bash
git add dashboard/src/components/InstanceRow.tsx dashboard/src/components/InstanceRow.test.tsx
git commit -m "feat(dashboard): annotate run rows with origin badge"
```

---

### Task 5: `SchedulesPanel` component (per-workflow, read-only)

**Files:**
- Create: `dashboard/src/components/SchedulesPanel.tsx`
- Create: `dashboard/src/components/SchedulesPanel.module.css`
- Create (test): `dashboard/src/components/SchedulesPanel.test.tsx`

The panel takes a `workflowName`, fetches `api.schedules.list(workflowName)` on mount, and renders a read-only table (cron, timezone, creator, enabled/disabled, last-fired). Empty state when none; error state on fetch failure; NO mutation controls.

- [ ] **Step 20: Write the failing component test (happy path, empty, error, read-only, all fields)**

Create `dashboard/src/components/SchedulesPanel.test.tsx`:

```tsx
import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import SchedulesPanel from './SchedulesPanel'
import { api } from '../api/client'
import type { Schedule } from '../api/types'

function sched(overrides: Partial<Schedule> = {}): Schedule {
  return {
    id: 's1',
    workflowName: 'wf-a',
    cronExpr: '0 9 * * *',
    timezone: 'America/New_York',
    creatorId: 'user-1',
    department: 'eng',
    enabled: true,
    lastFiredAt: '2026-06-01T09:00:00Z',
    createdAt: '2026-05-01T00:00:00Z',
    updatedAt: '2026-05-01T00:00:00Z',
    ...overrides,
  }
}

describe('SchedulesPanel', () => {
  beforeEach(() => {
    vi.spyOn(api.schedules, 'list')
  })
  afterEach(() => vi.restoreAllMocks())

  it('renders each schedule with cron, timezone, creator, enabled state, and last-fired', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched()])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText('0 9 * * *')).toBeInTheDocument())
    expect(screen.getByText('America/New_York')).toBeInTheDocument()
    expect(screen.getByText(/user-1/)).toBeInTheDocument()
    expect(screen.getByText(/enabled/i)).toBeInTheDocument()
    expect(screen.getByText(/2026-06-01/)).toBeInTheDocument()
  })

  it('renders a disabled label for disabled schedules', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched({ enabled: false })])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/disabled/i)).toBeInTheDocument())
  })

  it('shows a "never fired" indicator when lastFiredAt is null', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched({ lastFiredAt: null })])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/never/i)).toBeInTheDocument())
  })

  it('shows an empty state when the workflow has no schedules', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/no schedules/i)).toBeInTheDocument())
  })

  it('degrades to an error state when the fetch fails, not breaking the view', async () => {
    vi.mocked(api.schedules.list).mockRejectedValue(new Error('boom'))
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText(/failed to load schedules/i)).toBeInTheDocument())
  })

  it('fetches schedules scoped to the given workflow', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([])
    render(<SchedulesPanel workflowName="wf-b" />)
    await waitFor(() => expect(api.schedules.list).toHaveBeenCalledWith('wf-b'))
  })

  it('exposes NO create/edit/enable/disable/delete/run control (read-only)', async () => {
    vi.mocked(api.schedules.list).mockResolvedValue([sched()])
    render(<SchedulesPanel workflowName="wf-a" />)
    await waitFor(() => expect(screen.getByText('0 9 * * *')).toBeInTheDocument())
    expect(screen.queryByRole('button')).toBeNull()
  })
})
```

- [ ] **Step 21: Run to verify it fails**

Run: `cd dashboard && npx vitest run src/components/SchedulesPanel.test.tsx`
Expected: FAIL — module `./SchedulesPanel` not found.

- [ ] **Step 22: Implement `SchedulesPanel`**

Create `dashboard/src/components/SchedulesPanel.module.css`:

```css
.container {
  padding: 0.5rem 0;
}

.heading {
  font-size: 0.85rem;
  font-weight: 600;
  margin: 0 0 0.5rem;
  color: #9aa4b2;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.78rem;
}

.table th,
.table td {
  text-align: left;
  padding: 0.3rem 0.5rem;
  border-bottom: 1px solid #2a2f3a;
}

.table th {
  color: #6b7280;
  font-weight: 500;
}

.enabled {
  color: #7fd17f;
}

.disabled {
  color: #9aa4b2;
}

.empty,
.error {
  font-size: 0.8rem;
  color: #9aa4b2;
  padding: 0.5rem 0;
}

.error {
  color: #ff9a9a;
}
```

Create `dashboard/src/components/SchedulesPanel.tsx`:

```tsx
import { useState, useEffect } from 'react'
import { api } from '../api/client'
import type { Schedule } from '../api/types'
import styles from './SchedulesPanel.module.css'

interface SchedulesPanelProps {
  workflowName: string
}

function formatLastFired(lastFiredAt?: string | null): string {
  if (!lastFiredAt) return 'never'
  return lastFiredAt
}

/**
 * READ-ONLY list of a workflow's schedules (cron, timezone, creator, enabled
 * state, last-fired). It fetches its own data and degrades to an empty/error
 * state without breaking the surrounding workflow view. It deliberately renders
 * NO create/edit/enable/disable/delete/run-now control — schedule management
 * stays on REST + MCP.
 */
export default function SchedulesPanel({ workflowName }: SchedulesPanelProps) {
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    api.schedules
      .list(workflowName)
      .then((data) => {
        if (!cancelled) setSchedules(data)
      })
      .catch(() => {
        if (!cancelled) setError('Failed to load schedules')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [workflowName])

  if (loading) {
    return <div className={styles.empty}>Loading schedules…</div>
  }
  if (error) {
    return <div className={styles.error}>{error}</div>
  }
  if (schedules.length === 0) {
    return <div className={styles.empty}>No schedules for this workflow.</div>
  }

  return (
    <div className={styles.container}>
      <h3 className={styles.heading}>Schedules</h3>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Cron</th>
            <th>Timezone</th>
            <th>Creator</th>
            <th>State</th>
            <th>Last fired</th>
          </tr>
        </thead>
        <tbody>
          {schedules.map((s) => (
            <tr key={s.id}>
              <td>{s.cronExpr}</td>
              <td>{s.timezone}</td>
              <td>{s.creatorId}</td>
              <td className={s.enabled ? styles.enabled : styles.disabled}>
                {s.enabled ? 'enabled' : 'disabled'}
              </td>
              <td>{formatLastFired(s.lastFiredAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
```

- [ ] **Step 23: Run to verify it passes**

Run: `cd dashboard && npx vitest run src/components/SchedulesPanel.test.tsx`
Expected: PASS (all seven tests).

- [ ] **Step 24: Commit**

```bash
git add dashboard/src/components/SchedulesPanel.tsx dashboard/src/components/SchedulesPanel.module.css dashboard/src/components/SchedulesPanel.test.tsx
git commit -m "feat(dashboard): read-only SchedulesPanel for a workflow's schedules"
```

---

### Task 6: Render `SchedulesPanel` in `WorkflowReview`

**Files:**
- Modify: `dashboard/src/routes/WorkflowReview.tsx` (render the panel; the workflow name comes from `useParams`)

There is no route-level test harness required here beyond confirming it compiles and the component renders; the panel's own behavior is covered by Task 5. (If `WorkflowReview` has an existing test, do not break it.)

- [ ] **Step 25: Import and render the panel**

In `dashboard/src/routes/WorkflowReview.tsx`:

(a) Add the import near the other component imports (after line 18 `import UsageStats ...`):

```tsx
import SchedulesPanel from '../components/SchedulesPanel'
```

(b) The top-level `WorkflowReview` component reads the workflow name from `useParams`. Locate where it derives the name (e.g. `const { name } = useParams()` or similar — confirm the param key) and render the panel inside the detail/side panel area where `UsageStats` is rendered. Render it only when a workflow name is available:

```tsx
{workflowName && <SchedulesPanel workflowName={workflowName} />}
```

Use whatever local variable already holds the resolved workflow name in that scope (it is passed to `UsageStats`/`saveLayout` as `workflowName`). Place the panel adjacent to `UsageStats` so it sits in the same read-only detail column.

- [ ] **Step 26: Typecheck + build**

Run: `cd dashboard && npx tsc --noEmit && npm run build`
Expected: exit 0.

- [ ] **Step 27: Commit**

```bash
git add dashboard/src/routes/WorkflowReview.tsx
git commit -m "feat(dashboard): show SchedulesPanel in the workflow review view"
```

> **Spec note (catalog placement):** Capability C names the workflow catalog AND the review view; the design doc relaxes this to "WorkflowsCatalog and/or WorkflowReview". `WorkflowReview` is the workflow DETAIL surface and is the natural home for a per-workflow panel, so the panel lives there. `WorkflowsCatalog` is a multi-workflow list with no single `workflowName` in scope, so a per-workflow panel does not fit it without a row-expand interaction (out of scope this phase). The acceptance criterion "schedules appear in the workflow … views" is satisfied by the review view. If a catalog-level surface is later desired, reuse `SchedulesPanel` inside a per-row expander — no component change needed.

---

### Task 7: Full frontend gate

- [ ] **Step 28: Run the whole dashboard test suite + typecheck**

Run: `cd dashboard && npx tsc --noEmit && npm test`
Expected: all tests PASS, no type errors.

---

## Self-review checklist

- [ ] NO schedule mutation control anywhere (panel and badge have zero buttons/forms; `queryByRole('button')` is null in both component tests).
- [ ] `triggerSource` absent on a summary => badge defaults to `manual`.
- [ ] `SchedulesPanel` shows cron, timezone, creator, enabled/disabled, last-fired (with a "never" state for null `lastFiredAt`).
- [ ] `SchedulesPanel` degrades to an empty state (no schedules) and an error state (fetch failure) WITHOUT breaking the workflow view.
- [ ] `api.schedules.list(workflowName)` unwraps `{ schedules: [...] }` and passes the workflow name as the `workflowName` query param.
- [ ] Visibility scoping is inherited from the backend (the panel/run rows show only what the scoped REST endpoints return) — no client-side scope widening.
