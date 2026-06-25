# Phase 3 — Task 6: QueueView wiring + make ui

**Goal:** Wire the density toggle + `useDensityPreference` + `authMode` into `QueueView`, pass them down to the enriched `QueueSection` instances, fetch the `authMode` from `/me`, and rebuild the embedded SPA (`make ui`) so `adapter/dashboard/dist` reflects the new components. The `QueueView` fetch is switched to consume the enriched fields the list endpoint now returns (the same `api.instances.list` call — the response is a superset).

**Architecture:** `QueueView` already fetches active/failed/completed in parallel and renders `QueueSection` per state. This task adds: (1) a `useDensityPreference` call (keyed by the caller's userId from `/me`), (2) a `DensityToggle` in the toolbar, (3) `density` + `authMode` props threaded into each `QueueSection`, (4) the `/me` fetch for `authMode` + `userId`. The pending (queued) section is broken out as its own `QueueSection` (the design's Phase-2 policy: queued shows position ascending) rather than merged into Active. The `make ui` step rebuilds the embedded assets.

> Dependency: this task assumes `api.instances.list` already returns
> `Promise<EnrichedInstanceSummary[]>` (changed in Phase 3 Task 1, Step 1b).
> Without that return-type change the `EnrichedInstanceSummary[]` state below and
> the `tsc --noEmit` gate in Step 6 will not compile.

**Tech Stack:** TypeScript, React 19, Vitest + `@testing-library/react`, Make.

---

### Task 6: QueueView wiring + make ui

**Files:**
- Modify: `dashboard/src/routes/QueueView.tsx` (add density hook + toggle + authMode + pending section)
- Modify: `dashboard/src/routes/QueueView.test.tsx` (update for enriched rows + density + toggle)
- Run: `make ui` (rebuild embedded assets)

- [x] Step 1: Read the existing QueueView test to understand the current mock surface

```bash
cat dashboard/src/routes/QueueView.test.tsx
```

Note the existing mock setup (mocked `api.instances.list`, `useSystemWebSocket`, `useRefetchOnReconnect`). The test seeds `InstanceSummary` rows; it must now seed `EnrichedInstanceSummary` rows (with `runName` + `retryCount`) and mock `api.me`.

- [x] Step 2: Write the failing test for the wired QueueView

Append to `dashboard/src/routes/QueueView.test.tsx` (or replace the existing happy-path test):

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import QueueView from './QueueView'
import type { EnrichedInstanceSummary, Me } from '../api/types'

vi.mock('../api/client', () => ({
  api: {
    me: vi.fn(),
    instances: {
      list: vi.fn(),
      dismiss: vi.fn(),
      restore: vi.fn(),
      dismissAll: vi.fn(),
    },
  },
}))
vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: () => ({ connected: false }),
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({
  useRefetchOnReconnect: () => {},
}))

import { api } from '../api/client'

function enriched(overrides: Partial<EnrichedInstanceSummary> = {}): EnrichedInstanceSummary {
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

function mockMe(overrides: Partial<Me> = {}): Me {
  return {
    userId: 'u1',
    username: 'local',
    role: 'admin',
    departments: ['default'],
    ledDepartments: [],
    isAdmin: true,
    authMode: 'local',
    ...overrides,
  }
}

describe('QueueView — enriched + density wiring', () => {
  beforeEach(() => {
    vi.mocked(api.me).mockResolvedValue(mockMe())
    vi.mocked(api.instances.list).mockResolvedValue([])
    vi.mocked(api.instances.dismiss).mockResolvedValue({ status: 'ok' })
    vi.mocked(api.instances.restore).mockResolvedValue({ status: 'ok' })
    vi.mocked(api.instances.dismissAll).mockResolvedValue({ status: 'ok' })
  })

  it('renders the DensityToggle in the toolbar', async () => {
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /density/i })).toBeInTheDocument()
    })
  })

  it('renders the run name in a row (enriched field reaches the wire)', async () => {
    vi.mocked(api.instances.list).mockImplementation((params?: any) => {
      if (params?.state === 'running') return Promise.resolve([enriched({ id: 'r1', runName: 'MyRun — wf', state: 'running' })])
      return Promise.resolve([])
    })
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => {
      expect(screen.getByText('MyRun — wf')).toBeInTheDocument()
    })
  })

  it('renders the Pending (queued) section separately with queue positions', async () => {
    vi.mocked(api.instances.list).mockImplementation((params?: any) => {
      if (params?.state === 'queued') return Promise.resolve([enriched({ id: 'q1', state: 'queued', queuePosition: 1 })])
      return Promise.resolve([])
    })
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => {
      expect(screen.getByText(/pending/i)).toBeInTheDocument()
      expect(screen.getByText('1')).toBeInTheDocument()
    })
  })

  it('cycles density when the toggle is clicked (no refetch)', async () => {
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => expect(screen.getByRole('button', { name: /density/i })).toBeInTheDocument())
    const listCallsBefore = vi.mocked(api.instances.list).mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /density/i }))
    // Density is layout-only; it must NOT trigger a refetch.
    expect(vi.mocked(api.instances.list).mock.calls.length).toBe(listCallsBefore)
  })

  it('renders submitter as "you" in local mode', async () => {
    vi.mocked(api.instances.list).mockImplementation((params?: any) => {
      if (params?.state === 'running') return Promise.resolve([enriched({ id: 'r1', state: 'running', submitterName: 'alice' })])
      return Promise.resolve([])
    })
    vi.mocked(api.me).mockResolvedValue(mockMe({ authMode: 'local' }))
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => {
      expect(screen.getByText('you')).toBeInTheDocument()
    })
  })

  it('renders submitter as display name in shared mode', async () => {
    vi.mocked(api.instances.list).mockImplementation((params?: any) => {
      if (params?.state === 'running') return Promise.resolve([enriched({ id: 'r1', state: 'running', submitterName: 'alice' })])
      return Promise.resolve([])
    })
    vi.mocked(api.me).mockResolvedValue(mockMe({ authMode: 'org' }))
    render(<MemoryRouter><QueueView /></MemoryRouter>)
    await waitFor(() => {
      expect(screen.getByText('alice')).toBeInTheDocument()
    })
  })
})
```

Note: if `QueueView.test.tsx` already has a `describe('QueueView', ...)` block with conflicting mocks, merge the new tests into it rather than duplicating the `vi.mock` calls. Ensure the `api.me` mock is added to the existing mock factory.

- [x] Step 3: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/routes/QueueView.test.tsx
```

Expected: failure — `QueueView` does not render a `DensityToggle`, no Pending section, no `api.me` call.

- [x] Step 4: Wire `QueueView`

In `dashboard/src/routes/QueueView.tsx`:

1. Add imports:

```tsx
import { useEffect, useState, useCallback, useRef } from 'react'
import type { EnrichedInstanceSummary, Me, WebSocketEvent, QueueDensity } from '../api/types'
import { api } from '../api/client'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import { useRefetchOnReconnect } from '../hooks/useRefetchOnReconnect'
import { useDensityPreference } from '../hooks/useDensityPreference'
import QueueSection from '../components/QueueSection'
import DensityToggle from '../components/DensityToggle'
```

2. Replace the state types and add `me` + density:

```tsx
export default function QueueView() {
  const [active, setActive] = useState<EnrichedInstanceSummary[]>([])
  const [pending, setPending] = useState<EnrichedInstanceSummary[]>([])
  const [failed, setFailed] = useState<EnrichedInstanceSummary[]>([])
  const [completed, setCompleted] = useState<EnrichedInstanceSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showDismissed, setShowDismissed] = useState(false)

  const { density, cycle: cycleDensity } = useDensityPreference(me?.userId ?? null)
  const authMode = me?.authMode ?? 'local'
```

3. In `fetchAll`, add the queued fetch as a separate state bucket (not merged into active) and add the `/me` fetch:

```tsx
  const fetchAll = useCallback(async () => {
    try {
      const since = daysAgo(HISTORY_DAYS)
      const [running, waiting, queued, paused, failedRes, completedRes, meResp] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed }),
          api.me(),
        ])

      setActive([...running, ...waiting, ...paused])
      setPending(queued)
      setFailed(failedRes)
      setCompleted(completedRes)
      setMe(meResp)
      setError(null)
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setError(msg)
    } finally {
      setLoading(false)
    }
  }, [showDismissed])
```

4. In the `return`, add the `DensityToggle` to the toolbar and the Pending section, and pass `density` + `authMode` to every `QueueSection`:

```tsx
  return (
    <div style={{ padding: '16px 0' }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '0 16px 8px' }}>
        <DensityToggle density={density} onCycle={cycleDensity} />
      </div>
      <QueueSection
        title="Active"
        instances={active}
        titleColor="var(--state-running)"
        countBg="rgba(52, 152, 219, 0.2)"
        countColor="var(--state-running)"
        timeMode="elapsed"
        density={density}
        authMode={authMode}
      />
      <QueueSection
        title="Pending"
        instances={pending}
        titleColor="var(--state-queued)"
        countBg="rgba(241, 196, 15, 0.2)"
        countColor="var(--state-queued)"
        timeMode="elapsed"
        density={density}
        authMode={authMode}
      />
      <QueueSection
        title="Failed"
        instances={failed}
        titleColor="var(--state-failed)"
        countBg="rgba(231, 76, 60, 0.2)"
        countColor="var(--state-failed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="relative"
        density={density}
        authMode={authMode}
        onDismissAll={() => handleDismissAll('failed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />
      <QueueSection
        title="Completed"
        instances={completed}
        titleColor="var(--state-completed)"
        countBg="rgba(46, 204, 113, 0.2)"
        countColor="var(--state-completed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="relative"
        density={density}
        authMode={authMode}
        defaultCollapsed
        onDismissAll={() => handleDismissAll('completed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />

      <div style={{ textAlign: 'center', padding: '16px 0' }}>
        <button
          type="button"
          onClick={() => setShowDismissed((s) => !s)}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          {showDismissed ? 'Hide Dismissed' : 'Show Dismissed'}
        </button>
      </div>
    </div>
  )
```

- [x] Step 5: Run the QueueView tests, confirm they pass

```bash
cd dashboard && npx vitest run src/routes/QueueView.test.tsx
```

Expected: all assertions pass.

- [x] Step 6: Run the full dashboard test gate + typecheck

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```

Expected: no type errors; all dashboard tests pass.

- [x] Step 7: Rebuild the embedded SPA assets

```bash
make ui
```

Expected: the `dashboard/` build runs and copies assets into `adapter/dashboard/dist`. Confirm the dist updated:

```bash
git status --short adapter/dashboard/dist
```

- [x] Step 8: Confirm the Go test gate still passes (the embedded assets are consumed by the Go embed)

```bash
go test ./...
```

Expected: all Go tests pass.

- [x] Step 9: Commit

```bash
git add dashboard/src/routes/QueueView.tsx dashboard/src/routes/QueueView.test.tsx adapter/dashboard/dist
git commit -m "feat(dashboard): wire density toggle + authMode + Pending section into QueueView; rebuild embedded assets"
```