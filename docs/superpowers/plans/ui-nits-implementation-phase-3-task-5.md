# UI Nits Phase 3 — Task 5: Frontend — dismiss/restore controls, Dismiss-All, Show/Hide toggle

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: failing test → run (expect FAIL) → minimal impl → run (expect PASS) → commit. **Depends on Tasks 1-4 (the dismiss/restore/dismiss-all endpoints and the `dismissed` flag on list responses).**

**Goal:** In the dashboard, each terminal (Completed/Failed) row gets a `[x]` dismiss control that removes it from the user's view; each Failed/Completed section gets a `[Dismiss All]` control; a global bottom toggle reveals dismissed items (dimmed, in chronological place) with a `↺` restore control and flips its label between `[Show Dismissed]` / `[Hide Dismissed]`.

**Tech Stack:** React 19 / TypeScript, Vitest + @testing-library/react (`dashboard/package.json` → `"test": "vitest run"`; full type+test gate: `make test-ui` = `npx tsc --noEmit && npx vitest run`).

---

### Task 5: API client methods + types + QueueView toggle + QueueSection Dismiss-All + InstanceRow controls

**Context for the implementer (read before starting):**

- API client: `dashboard/src/api/client.ts`. `api.instances` already has `list(params)`, `retry`, `rerun`, etc. `request<T>` / `post<T>` helpers handle fetch + error. `ListInstancesParams` (lines 16-23) holds the list query options. `post` returns parsed JSON; for endpoints returning `{ "status": "ok" }` declare the return as `Promise<{ status: string }>` (matches `pause`/`resume`).
- Types: `dashboard/src/api/types.ts` → `InstanceSummary`. Task 3 may have already added `dismissed?: boolean`; if not, add it here.
- `QueueView.tsx` fetches six lists in parallel (running/waiting/queued/paused/failed/completed), with `failed`/`completed` windowed by `since = daysAgo(5)`. It refetches (debounced) on WebSocket state-change events and on reconnect. The Show/Hide toggle adds a `showDismissed` boolean: when true, the failed/completed fetches pass `includeDismissed: true`; when false, the default fetch (server omits dismissed) is used. The toggle lives at the bottom of the returned view.
- `QueueSection.tsx` renders a section header (collapsible) + sort controls + `InstanceRow` list. Add an optional `onDismissAll?: () => void` prop; render a `[Dismiss All]` button in the header **only when provided** (Failed/Completed sections), and `stopPropagation` on its click so it does not toggle collapse. The section already receives `title`; the parent passes the matching `state`.
- `InstanceRow.tsx` is a clickable row that `navigate(...)`s to the instance on click/Enter/Space. New controls **must call `e.stopPropagation()`** so clicking `[x]`/`↺` does not navigate. Add optional props: `onDismiss?: (id) => void`, `onRestore?: (id) => void`, and `dimmed?: boolean`. Render `[x]` when the row is terminal (`state === 'failed' || state === 'completed'`) and not dimmed; render `↺` instead when `dimmed` (i.e. shown because `showDismissed` is on and the row is `dismissed`). Active rows get neither control.
- Dimming: there is a `--text-muted` / `--text-dim` token and an `opacity` usage already in the CSS modules. Add a `dimmed` class (e.g. `opacity: 0.5`) to `InstanceRow.module.css`, or apply inline `style={{ opacity: 0.5 }}` if simpler; prefer the CSS module to match the file's conventions.
- After any dismiss/restore/dismiss-all, refetch (reuse `fetchAll`) so counts and ordering stay correct; WebSocket-driven refetches continue to respect the current `showDismissed` flag (so make `fetchAll` read `showDismissed`).
- Test patterns: `dashboard/src/components/SidePanel.test.tsx` (render + `screen` + `fireEvent`, child mocks), `dashboard/src/api/client.test.ts` (mock `globalThis.fetch`, assert URL + method/body). Use `@testing-library/react` `render`, `screen.getByRole/getByText`, `fireEvent.click`, and `vi.fn()` for callbacks.

**Files:**
- `dashboard/src/api/types.ts` (modify: ensure `dismissed?: boolean` on `InstanceSummary`)
- `dashboard/src/api/client.ts` (modify: `includeDismissed` in `ListInstancesParams` + `list`; add `dismiss`, `restore`, `dismissAll`)
- `dashboard/src/api/client.test.ts` (extend: dismissal client tests)
- `dashboard/src/components/InstanceRow.tsx` + `.module.css` (modify: controls + dimming)
- `dashboard/src/components/InstanceRow.test.tsx` (new)
- `dashboard/src/components/QueueSection.tsx` (modify: `[Dismiss All]`)
- `dashboard/src/components/QueueSection.test.tsx` (new)
- `dashboard/src/routes/QueueView.tsx` (modify: `showDismissed` toggle + wiring)
- `dashboard/src/routes/QueueView.test.tsx` (new)

---

#### Step 5.1 — Failing test: client `dismiss`/`restore`/`dismissAll` + `includeDismissed`

- [ ] Extend `dashboard/src/api/client.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'

describe('api.instances dismissal', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: 'ok' }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('POSTs dismiss to /instances/{id}/dismiss', async () => {
    await api.instances.dismiss('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/dismiss',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs restore to /instances/{id}/restore', async () => {
    await api.instances.restore('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/restore',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs dismissAll with the state query param', async () => {
    await api.instances.dismissAll('failed')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/dismiss-all?state=failed',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('list includes includeDismissed=true when requested', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed', includeDismissed: true })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('includeDismissed=true')
    expect(url).toContain('state=failed')
  })

  it('list omits includeDismissed when false/absent', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed' })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).not.toContain('includeDismissed')
  })
})
```

- [ ] Run: `cd dashboard && npx vitest run src/api/client.test.ts`
- [ ] Expected: **FAIL** — `api.instances.dismiss/restore/dismissAll` undefined; `includeDismissed` not on params.

#### Step 5.2 — Minimal impl: client methods + param

- [ ] In `client.ts`, add `includeDismissed` to `ListInstancesParams`:

```ts
export interface ListInstancesParams {
  state?: InstanceState
  workflowName?: string
  since?: string
  limit?: number
  offset?: number
  includeDismissed?: boolean
}
```

- [ ] In `list`, append the param only when true:

```ts
      if (params?.includeDismissed) q.set('includeDismissed', 'true')
```

- [ ] Add the three methods to `api.instances` (after `rerun`):

```ts
    /** Dismiss a terminal instance from the acting user's view (per user). */
    dismiss(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/dismiss`)
    },

    /** Restore a previously dismissed instance for the acting user. */
    restore(id: string): Promise<{ status: string }> {
      return post(`/api/v1/instances/${id}/restore`)
    },

    /** Dismiss every currently listed instance in a terminal category. */
    dismissAll(state: 'failed' | 'completed'): Promise<{ status: string }> {
      return post(`/api/v1/instances/dismiss-all?state=${state}`)
    },
```

- [ ] Run: `cd dashboard && npx vitest run src/api/client.test.ts` → **PASS**.
- [ ] Run: `cd dashboard && npx tsc --noEmit` → **PASS**.
- [ ] Commit: `feat(dismissal-ui): client dismiss/restore/dismissAll + includeDismissed [ui-nits p3 t5]`

#### Step 5.3 — Failing test: `InstanceRow` controls (`[x]` terminal, `↺` dimmed, none on active; no navigation)

- [ ] Create `dashboard/src/components/InstanceRow.test.tsx`. Wrap in a `MemoryRouter` (the component uses `useNavigate`). Mock nothing else:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import InstanceRow from './InstanceRow'
import type { InstanceSummary } from '../api/types'

function summary(overrides: Partial<InstanceSummary> = {}): InstanceSummary {
  return {
    id: 'i1',
    workflowName: 'wf',
    state: 'failed',
    priority: 'normal',
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    ...overrides,
  }
}

function renderRow(props: Partial<React.ComponentProps<typeof InstanceRow>>) {
  return render(
    <MemoryRouter>
      <InstanceRow instance={summary()} timeMode="relative" {...props} />
    </MemoryRouter>,
  )
}

describe('InstanceRow dismissal controls', () => {
  it('shows a dismiss control on terminal rows and calls onDismiss without navigating', () => {
    const onDismiss = vi.fn()
    renderRow({ instance: summary({ state: 'failed' }), onDismiss })
    const btn = screen.getByRole('button', { name: /dismiss/i })
    fireEvent.click(btn)
    expect(onDismiss).toHaveBeenCalledWith('i1')
  })

  it('shows a restore control (not dismiss) when dimmed', () => {
    const onRestore = vi.fn()
    renderRow({ instance: summary({ state: 'completed', dismissed: true }), dimmed: true, onRestore })
    const btn = screen.getByRole('button', { name: /restore/i })
    fireEvent.click(btn)
    expect(onRestore).toHaveBeenCalledWith('i1')
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
  })

  it('shows no dismiss/restore control on active (running) rows', () => {
    renderRow({ instance: summary({ state: 'running' }), onDismiss: vi.fn() })
    expect(screen.queryByRole('button', { name: /dismiss/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restore/i })).toBeNull()
  })
})
```

- [ ] Run: `cd dashboard && npx vitest run src/components/InstanceRow.test.tsx` → **FAIL** (props/controls missing).

#### Step 5.4 — Minimal impl: `InstanceRow` controls + dimming

- [ ] Update `InstanceRow.tsx` props + render. Controls must `stopPropagation` and use accessible labels:

```tsx
interface InstanceRowProps {
  instance: InstanceSummary
  timeMode: 'elapsed' | 'relative'
  dimmed?: boolean
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
}

export default function InstanceRow({ instance, timeMode, dimmed, onDismiss, onRestore }: InstanceRowProps) {
  const navigate = useNavigate()
  const isTerminal = instance.state === 'failed' || instance.state === 'completed'
  // ...timeStr unchanged...

  return (
    <div
      className={`${styles.row} ${borderClass[instance.state] ?? ''} ${dimmed ? styles.dimmed : ''}`}
      onClick={() => navigate(`/instances/${instance.id}`)}
      role="button"
      tabIndex={0}
      onKeyDown={/* unchanged */}
    >
      {/* ...existing spans... */}
      {isTerminal && (
        dimmed
          ? (
            <button
              type="button"
              className={styles.control}
              aria-label="Restore"
              title="Restore"
              onClick={(e) => { e.stopPropagation(); onRestore?.(instance.id) }}
            >
              {'↺'}
            </button>
          )
          : onDismiss && (
            <button
              type="button"
              className={styles.control}
              aria-label="Dismiss"
              title="Dismiss"
              onClick={(e) => { e.stopPropagation(); onDismiss(instance.id) }}
            >
              {'×'}
            </button>
          )
      )}
    </div>
  )
}
```

- [ ] Add to `InstanceRow.module.css`:

```css
.dimmed {
  opacity: 0.5;
}

.control {
  background: none;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 14px;
  padding: 0 6px;
}

.control:hover {
  color: var(--text-primary);
}
```

- [ ] Run: `cd dashboard && npx vitest run src/components/InstanceRow.test.tsx` → **PASS**.
- [ ] Commit: `feat(dismissal-ui): InstanceRow [x]/↺ controls + dimming [ui-nits p3 t5]`

#### Step 5.5 — Failing test: `QueueSection` renders `[Dismiss All]` only when `onDismissAll` provided

- [ ] Create `dashboard/src/components/QueueSection.test.tsx`:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueSection from './QueueSection'

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
        {...props}
      />
    </MemoryRouter>,
  )
}

describe('QueueSection dismiss-all', () => {
  it('renders [Dismiss All] when onDismissAll is provided', () => {
    const onDismissAll = vi.fn()
    renderSection({ onDismissAll })
    const btn = screen.getByRole('button', { name: /dismiss all/i })
    fireEvent.click(btn)
    expect(onDismissAll).toHaveBeenCalled()
  })

  it('does not render [Dismiss All] when onDismissAll is absent (Active section)', () => {
    renderSection({ title: 'Active' })
    expect(screen.queryByRole('button', { name: /dismiss all/i })).toBeNull()
  })
})
```

- [ ] Run: `cd dashboard && npx vitest run src/components/QueueSection.test.tsx` → **FAIL**.

#### Step 5.6 — Minimal impl: `QueueSection` dismiss-all + propagate row callbacks

- [ ] Add props and a header button (stop propagation so it does not toggle collapse). Also thread `onDismiss`/`onRestore`/`showDismissed` to rows:

```tsx
interface QueueSectionProps {
  title: string
  instances: InstanceSummary[]
  titleColor: string
  countBg: string
  countColor: string
  subtitle?: string
  timeMode: 'elapsed' | 'relative'
  defaultCollapsed?: boolean
  onDismissAll?: () => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
}
```

- [ ] In the header JSX, after the count/subtitle, render the button when provided:

```tsx
        {onDismissAll && (
          <button
            type="button"
            className={styles.dismissAll}
            onClick={(e) => { e.stopPropagation(); onDismissAll() }}
          >
            Dismiss All
          </button>
        )}
```

- [ ] In the row map, pass the callbacks and dimmed flag (a row is dimmed when it is shown because it is dismissed):

```tsx
              <InstanceRow
                key={inst.id}
                instance={inst}
                timeMode={timeMode}
                dimmed={inst.dismissed === true}
                onDismiss={onDismiss}
                onRestore={onRestore}
              />
```

- [ ] Add a `.dismissAll` class to `QueueSection.module.css` (small button styled like `.sortButton`); reuse existing tokens.
- [ ] Run: `cd dashboard && npx vitest run src/components/QueueSection.test.tsx` → **PASS**.
- [ ] Commit: `feat(dismissal-ui): QueueSection [Dismiss All] + row callbacks [ui-nits p3 t5]`

#### Step 5.7 — Failing test: `QueueView` Show/Hide toggle wiring

- [ ] Create `dashboard/src/routes/QueueView.test.tsx`. Mock `../api/client` and the websocket hooks so the view renders deterministically:

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: () => ({ connected: true }),
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))

const list = vi.fn()
vi.mock('../api/client', () => ({
  api: { instances: { list: (...a: unknown[]) => list(...a) } },
}))

import QueueView from './QueueView'

describe('QueueView show/hide dismissed toggle', () => {
  beforeEach(() => {
    list.mockReset()
    list.mockResolvedValue([])
  })

  it('starts as [Show Dismissed] and flips to [Hide Dismissed]; passes includeDismissed when shown', async () => {
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    const toggle = await screen.findByRole('button', { name: /show dismissed/i })
    fireEvent.click(toggle)
    await screen.findByRole('button', { name: /hide dismissed/i })

    // After enabling, the failed/completed fetches request includeDismissed.
    await waitFor(() => {
      const calledWithInclude = list.mock.calls.some(
        (c) => (c[0] as { includeDismissed?: boolean } | undefined)?.includeDismissed === true,
      )
      expect(calledWithInclude).toBe(true)
    })
  })
})
```

- [ ] Run: `cd dashboard && npx vitest run src/routes/QueueView.test.tsx` → **FAIL** (no toggle).

#### Step 5.8 — Minimal impl: `QueueView` toggle + dismiss handlers

- [ ] Add `showDismissed` state and make `fetchAll` read it; failed/completed fetches pass `includeDismissed: showDismissed`:

```tsx
  const [showDismissed, setShowDismissed] = useState(false)

  const fetchAll = useCallback(async () => {
    try {
      const since = daysAgo(HISTORY_DAYS)
      const [running, waiting, queued, paused, failedRes, completedRes] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed }),
        ])
      setActive([...running, ...waiting, ...queued, ...paused])
      setFailed(failedRes)
      setCompleted(completedRes)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }, [showDismissed])
```

- [ ] Add dismiss/restore/dismiss-all handlers that call the API then refetch:

```tsx
  const handleDismiss = useCallback(async (id: string) => {
    await api.instances.dismiss(id)
    await fetchAll()
  }, [fetchAll])

  const handleRestore = useCallback(async (id: string) => {
    await api.instances.restore(id)
    await fetchAll()
  }, [fetchAll])

  const handleDismissAll = useCallback(async (state: 'failed' | 'completed') => {
    await api.instances.dismissAll(state)
    await fetchAll()
  }, [fetchAll])
```

- [ ] Wire the Failed/Completed sections (Active gets none) and add the toggle at the bottom of the returned view:

```tsx
      <QueueSection
        title="Failed"
        instances={failed}
        /* ...existing props... */
        onDismissAll={() => handleDismissAll('failed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />
      <QueueSection
        title="Completed"
        instances={completed}
        /* ...existing props... */
        onDismissAll={() => handleDismissAll('completed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />

      <div style={{ textAlign: 'center', padding: '16px 0' }}>
        <button
          type="button"
          onClick={() => setShowDismissed((s) => !s)}
        >
          {showDismissed ? 'Hide Dismissed' : 'Show Dismissed'}
        </button>
      </div>
```

- [ ] Because `fetchAll` now depends on `showDismissed`, the existing `useEffect(() => { void fetchAll() }, [fetchAll])` re-runs when the toggle flips — so toggling refetches with the new flag automatically. Verify the WebSocket debounced refetch closure also references the current `fetchAll` (it already takes `fetchAll` in its dep array), so it respects `showDismissed`.
- [ ] Run: `cd dashboard && npx vitest run src/routes/QueueView.test.tsx` → **PASS**.
- [ ] Run the full dashboard gate: `make test-ui` (`npx tsc --noEmit && npx vitest run`) → **PASS**.
- [ ] Commit: `feat(dismissal-ui): QueueView show/hide toggle + dismiss wiring [ui-nits p3 t5]`

#### Step 5.9 — Rebuild embedded dashboard + full gate

- [ ] Rebuild the embedded SPA so the Go binary serves the new UI (per the Makefile `ui` target and the reference memory): `make ui`. This runs `npm ci && npm run build` and copies `dashboard/dist` → `adapter/dashboard/dist`.
- [ ] **Commit the refreshed `adapter/dashboard/dist`** so the checked-in artifact does not drift (the Makefile comment requires this).
- [ ] Run the full Definition-of-Done gate: `make test-all` (`make test` + `make test-ui`) → **PASS**.
- [ ] Commit: `chore(dismissal-ui): rebuild embedded dashboard dist [ui-nits p3 t5]`

**Definition of done for Task 5:** client has `dismiss`/`restore`/`dismissAll` + `includeDismissed`; `InstanceSummary.dismissed?` typed; `InstanceRow` shows `[x]` (terminal), `↺` (dimmed), none (active), all stopping navigation; `QueueSection` shows `[Dismiss All]` on Failed/Completed only; `QueueView` has a bottom Show/Hide toggle that refetches with `includeDismissed` and renders dismissed rows dimmed in place; dismiss/restore/dismiss-all refetch; embedded `dist` rebuilt + committed; `make test-all` green.
