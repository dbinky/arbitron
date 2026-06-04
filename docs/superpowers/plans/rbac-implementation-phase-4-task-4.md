# RBAC Phase 4 — Task 4: Frontend API types + client methods (`me`, `dashboard.stats`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add typed interfaces (`Me`, `DashboardStats` and its parts) to `api/types.ts`, and two client methods: `api.me()` → `GET /api/v1/me` and `api.dashboard.stats(departments, window)` → `GET /api/v1/dashboard/stats`.

**Architecture:** Pure TypeScript additions to the existing typed REST client. The new types mirror the Go JSON shapes from Tasks 1-3 (`dashboard.Stats`) and the Phase 1 `/me` envelope. `dashboard.stats` builds query params the same way `instances.list` does. No UI in this task.

**Tech Stack:** TypeScript, the existing `request<T>` helper in `api/client.ts`, Vitest for the client method test.

**Dependencies:** Backend Task 3 (`/dashboard/stats` JSON shape) and Phase 1's `/me` endpoint (assumed present, shape `{ userId, username, role, departments[], ledDepartments[], isAdmin }`). No dependency on the React component tasks, which import these types.

---

## Context You Need

- `dashboard/src/api/types.ts` holds all shared interfaces; `WorkflowStats` there is the closest precedent for a stats shape.
- `dashboard/src/api/client.ts`:
  - `request<T>(url, init?)` throws on non-2xx and returns parsed JSON.
  - `api.instances.list` shows the `URLSearchParams` query-building pattern.
  - `api.workflows.getStats(name, from, to)` is the precedent for a stats GET.
- The Go envelope from Task 1 serializes as:
  ```json
  { "departments": ["a"], "queue": {...}, "throughput": [...], "outcomes": {...}, "perWorkflow": [...] }
  ```
- Vitest is configured (`"test": "vitest run"`); tests use `vi.fn()` to stub `global.fetch`.

## File Structure

- Modify `dashboard/src/api/types.ts` — add `Me`, `DashboardQueueHealth`, `ThroughputBucket`, `DashboardOutcomes`, `WorkflowBreakdown`, `DashboardStats`.
- Modify `dashboard/src/api/client.ts` — add `api.me()` and the `api.dashboard.stats(...)` namespace.
- Create `dashboard/src/api/dashboardClient.test.ts` — Vitest scenarios for the new client methods.

---

### Task 4a: Types

**Files:**
- Modify: `dashboard/src/api/types.ts`

- [ ] **Step 1: Add the new interfaces**

In `dashboard/src/api/types.ts`, after the `WorkflowStats` interface (around line 129), add:

```typescript
/** The authenticated caller's identity projection from GET /api/v1/me. */
export interface Me {
  userId: string
  username: string
  role: 'user' | 'admin'
  /** Effective department membership (claim-derived ∪ led), normalized. */
  departments: string[]
  /** Subset of departments the caller leads. */
  ledDepartments: string[]
  isAdmin: boolean
}

/** Current point-in-time queue counts for the department dashboard. */
export interface DashboardQueueHealth {
  running: number
  queued: number
  waiting: number
  paused: number
  failedNeedingAttention: number
}

/** One time bucket of run volume. `bucket` is an RFC3339 timestamp. */
export interface ThroughputBucket {
  bucket: string
  runs: number
}

/** Aggregate success/failure and duration over the dashboard window. */
export interface DashboardOutcomes {
  totalRuns: number
  /** 0.0..1.0 */
  successRate: number
  /** 0.0..1.0 */
  failureRate: number
  avgDurationSeconds: number
  p95DurationSeconds: number
}

/** One row of the per-workflow ranking. */
export interface WorkflowBreakdown {
  workflowName: string
  runs: number
  /** 0.0..1.0 */
  failureRate: number
  avgDurationSeconds: number
}

/** Full department dashboard envelope from GET /api/v1/dashboard/stats. */
export interface DashboardStats {
  /** The effective department set the stats were computed over. */
  departments: string[]
  queue: DashboardQueueHealth
  throughput: ThroughputBucket[]
  outcomes: DashboardOutcomes
  perWorkflow: WorkflowBreakdown[]
}
```

- [ ] **Step 2: Type-check the additions**

Run: `cd dashboard && npx tsc --noEmit`
Expected: exit 0 (no errors).

---

### Task 4b: Client methods

**Files:**
- Modify: `dashboard/src/api/client.ts`
- Test: `dashboard/src/api/dashboardClient.test.ts`

- [ ] **Step 3: Write the failing client test**

Create `dashboard/src/api/dashboardClient.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'
import type { DashboardStats, Me } from './types'

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('api.me', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  it('GETs /api/v1/me and returns the projection', async () => {
    const me: Me = {
      userId: 'u1', username: 'alice', role: 'user',
      departments: ['engineering'], ledDepartments: [], isAdmin: false,
    }
    const fetchMock = vi.spyOn(global, 'fetch').mockResolvedValue(jsonResponse(me))
    const result = await api.me()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/me', undefined)
    expect(result).toEqual(me)
  })
})

describe('api.dashboard.stats', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { vi.restoreAllMocks() })

  const empty: DashboardStats = {
    departments: [],
    queue: { running: 0, queued: 0, waiting: 0, paused: 0, failedNeedingAttention: 0 },
    throughput: [], outcomes: { totalRuns: 0, successRate: 0, failureRate: 0, avgDurationSeconds: 0, p95DurationSeconds: 0 },
    perWorkflow: [],
  }

  it('omits the departments param when departments is null (union default)', async () => {
    const fetchMock = vi.spyOn(global, 'fetch').mockResolvedValue(jsonResponse(empty))
    await api.dashboard.stats(null, '24h')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/dashboard/stats?window=24h', undefined)
  })

  it('joins departments with commas and includes the window', async () => {
    const fetchMock = vi.spyOn(global, 'fetch').mockResolvedValue(jsonResponse(empty))
    await api.dashboard.stats(['engineering', 'sales'], '6h')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/dashboard/stats?departments=engineering%2Csales&window=6h',
      undefined,
    )
  })

  it('returns the parsed envelope', async () => {
    vi.spyOn(global, 'fetch').mockResolvedValue(jsonResponse({ ...empty, departments: ['engineering'] }))
    const result = await api.dashboard.stats(['engineering'], '24h')
    expect(result.departments).toEqual(['engineering'])
  })
})
```

- [ ] **Step 4: Run it to verify it fails**

Run: `cd dashboard && npx vitest run src/api/dashboardClient.test.ts`
Expected: FAIL — `api.me is not a function` / `api.dashboard is undefined`.

- [ ] **Step 5: Add the imports and client methods**

In `dashboard/src/api/client.ts`, add `DashboardStats` and `Me` to the type import block at the top:

```typescript
import type {
  DashboardStats,
  Instance,
  InstanceSummary,
  InstanceState,
  Me,
  NodeExecution,
  NotificationListResponse,
  Priority,
  QueueConfigUpdate,
  SystemConfig,
  TeamsConfigUpdate,
  WorkflowStats,
  WorkflowSummary,
} from './types'
```

Then, inside the `export const api = { ... }` object, add a top-level `me` method and a `dashboard` namespace. Place `me` right after the opening `{` (before `instances:`) and `dashboard` after the `workflows` namespace:

```typescript
  /** Get the authenticated caller's identity projection. */
  me(): Promise<Me> {
    return request('/api/v1/me')
  },
```

```typescript
  dashboard: {
    /**
     * Get department-scoped operational stats. Pass `departments` = null to use
     * the caller's union default (the backend clamps to the caller's allowed
     * set regardless). `window` is a Go duration string, e.g. "24h" or "6h".
     */
    stats(departments: string[] | null, window: string): Promise<DashboardStats> {
      const q = new URLSearchParams()
      if (departments && departments.length > 0) q.set('departments', departments.join(','))
      q.set('window', window)
      return request(`/api/v1/dashboard/stats?${q.toString()}`)
    },
  },
```

> NOTE: `URLSearchParams` percent-encodes the comma in the joined departments value to `%2C` — the test asserts `engineering%2Csales`, and the Go handler's `strings.Split(..., ",")` decodes it correctly because chi/net-url URL-decodes query values before the handler sees them.

- [ ] **Step 6: Run the client test to verify it passes**

Run: `cd dashboard && npx vitest run src/api/dashboardClient.test.ts`
Expected: PASS (all scenarios green)

- [ ] **Step 7: Type-check and lint**

Run: `cd dashboard && npx tsc --noEmit && npm run lint`
Expected: exit 0 (if `npm run lint` is not defined, skip it).

- [ ] **Step 8: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/api/client.ts dashboard/src/api/dashboardClient.test.ts
git commit -m "feat(dashboard): api.me() + api.dashboard.stats() client methods and types

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec/design coverage: "New API client methods: api.me() → /api/v1/me; api.dashboard.stats(depts, window) → /api/v1/dashboard/stats. Typed interfaces added to api/types.ts." — all present.
- TDD categories: happy-path (both methods return parsed data), edge (null departments omits the param), and the query-construction contract is pinned by exact-URL assertions. Error propagation is already covered by the shared `request<T>` helper (throws on non-2xx) and not re-tested here.
- Type names are prefixed `Dashboard*` where they would otherwise collide with existing types (`WorkflowStats`), and `ThroughputBucket`/`WorkflowBreakdown` are unique.
