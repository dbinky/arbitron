# Phase 3 — Task 1: Column-definition module (single source of truth)

**Goal:** Add the `columnDefs` module that declares each queue-list column once — key, header label, value accessor over the enriched row, sortable flag, and per-density visibility — so every other component reads columns from here and adding/reordering a column is one edit.

**Architecture:** A pure TypeScript module under `dashboard/src/components/queueColumns.ts` with no React imports — just data and accessor functions over the `EnrichedInstanceSummary` type. The type is added to `dashboard/src/api/types.ts` (mirroring the backend `enrichedSummaryDTO`). Three densities (`balanced`, `dense`, `expanded`) drive visibility. Every later component (Task 4's table, Task 5's detail panel) imports from here.

**Tech Stack:** TypeScript, Vitest. Tests with `vitest` + pure function assertions (no React rendering needed for this module).

---

### Task 1: Column definitions

**Files:**
- Modify: `dashboard/src/api/types.ts:18-34` (add `EnrichedInstanceSummary`)
- Modify: `dashboard/src/api/client.ts:84` (switch `instances.list` return type to `EnrichedInstanceSummary[]`)
- Create: `dashboard/src/components/queueColumns.ts`
- Create: `dashboard/src/components/queueColumns.test.ts`

- [ ] Step 1: Add the `EnrichedInstanceSummary` type

In `dashboard/src/api/types.ts`, first add `ownerId` to the existing `InstanceSummary` interface (after `id`, line 19) — the backend now includes it (Phase 1 Task 4):

```ts
export interface InstanceSummary {
  id: string
  ownerId: string
  workflowName: string
  ...
}
```

Then, add after the `InstanceSummary` interface (after line 34):

```ts
/** Density views for the queue list. */
export type QueueDensity = 'balanced' | 'dense' | 'expanded'

/**
 * Enriched instance row returned by the list endpoint (Phase 1+2). Extends
 * InstanceSummary with the additive fields the richer queue table shows.
 * Every enriched field is best-effort: a missing field renders as a
 * placeholder cell.
 */
export interface EnrichedInstanceSummary extends InstanceSummary {
  /** Always present (derived server-side). */
  runName: string
  /** Resolved owner display name; empty when user lookup degraded. */
  submitterName?: string
  /** Account identity (email/subject) for shared-mode row details. */
  submitterIdentity?: string
  /** Friendly label of the current/failed node; empty when none. */
  friendlyStep?: string
  /** Latest LLM model used; empty for queued/script-only runs. */
  model?: string
  /** Configured agent backend of the relevant node; empty when unset. */
  backend?: string
  /** Max retry attempt across the instance's node executions. */
  retryCount: number
  /** 1-based priority-aware position for queued rows; 0/absent otherwise. */
  queuePosition?: number
  /** Failed node id, for the detail panel's lazy last-error load. Absent for non-failed rows. */
  failedNodeId?: string
}
```

- [ ] Step 1b: Switch the API client's `instances.list` to return enriched rows

The list endpoint now returns the enriched shape (Phase 1 Task 4 switched the HTTP
handler), so the TS client must expose it or `QueueView` (Task 6) cannot read
`runName`/`retryCount`/etc. and `tsc --noEmit` fails (`InstanceSummary[]` is not
assignable to the `EnrichedInstanceSummary[]` state). In
`dashboard/src/api/client.ts`, change the `instances.list` return type (line 84):

```ts
    list(params?: ListInstancesParams): Promise<EnrichedInstanceSummary[]> {
```

and add `EnrichedInstanceSummary` to the type import from `./types` at the top of
`client.ts`. The runtime body is unchanged (the response JSON is a superset). The
lean `InstanceSummary` remains the base type; callers that only need lean fields
still compile (enriched extends it).

- [ ] Step 2: Write the failing test for the column definitions

Create `dashboard/src/components/queueColumns.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { COLUMNS, visibleColumns, getColumn } from './queueColumns'
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

describe('COLUMNS', () => {
  it('declares all spec columns', () => {
    const keys = COLUMNS.map((c) => c.key)
    expect(keys).toEqual(
      expect.arrayContaining([
        'workflow',
        'runName',
        'submitter',
        'submittedAt',
        'status',
        'friendlyStep',
        'elapsed',
        'queuePosition',
        'modelBackend',
        'retryCount',
      ]),
    )
  })

  it('every column has a key, header, accessor, sortable flag, and density visibility', () => {
    for (const c of COLUMNS) {
      expect(c.key).toBeTruthy()
      expect(c.header).toBeTruthy()
      expect(typeof c.accessor).toBe('function')
      expect(typeof c.sortable).toBe('boolean')
      expect(c.densities).toBeDefined()
    }
  })
})

describe('visibleColumns', () => {
  it('balanced density shows the full set minus only secondary detail columns', () => {
    const cols = visibleColumns('balanced' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).toContain('runName')
    expect(keys).toContain('status')
    expect(keys).toContain('friendlyStep')
    expect(keys).toContain('submitter')
  })

  it('dense density hides secondary columns to fit more rows', () => {
    const cols = visibleColumns('dense' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).not.toContain('submittedAt')
    expect(keys).not.toContain('friendlyStep')
    expect(keys).toContain('runName')
    expect(keys).toContain('status')
  })

  it('expanded density shows every column including secondary detail', () => {
    const cols = visibleColumns('expanded' as QueueDensity)
    const keys = cols.map((c) => c.key)
    expect(keys).toContain('submittedAt')
    expect(keys).toContain('modelBackend')
    expect(keys).toContain('retryCount')
  })
})

describe('accessors', () => {
  it('runName accessor returns the run name', () => {
    const c = getColumn('runName')!
    expect(c.accessor(row({ runName: 'My — wf' }))).toBe('My — wf')
  })

  it('submitter accessor returns the submitter name', () => {
    const c = getColumn('submitter')!
    expect(c.accessor(row({ submitterName: 'alice' }))).toBe('alice')
  })

  it('queuePosition accessor returns the position as a string', () => {
    const c = getColumn('queuePosition')!
    expect(c.accessor(row({ queuePosition: 3 }))).toBe('3')
  })

  it('queuePosition accessor returns placeholder for absent position', () => {
    const c = getColumn('queuePosition')!
    expect(c.accessor(row({ queuePosition: undefined }))).toBe('—')
  })

  it('modelBackend accessor combines model and backend', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row({ model: 'gpt-4o', backend: 'claude-code' }))).toBe('gpt-4o · claude-code')
  })

  it('modelBackend accessor returns model only when backend unset', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row({ model: 'gpt-4o' }))).toBe('gpt-4o')
  })

  it('modelBackend accessor returns placeholder when both empty', () => {
    const c = getColumn('modelBackend')!
    expect(c.accessor(row())).toBe('—')
  })
})
```

- [ ] Step 3: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/components/queueColumns.test.ts
```

Expected: failure — `Failed to resolve import "./queueColumns"`.

- [ ] Step 4: Implement the column definitions module

Create `dashboard/src/components/queueColumns.ts`:

```ts
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'

/** A single queue-list column declaration. */
export interface ColumnDef {
  /** Stable key identifying the column. */
  key: string
  /** Header label shown in the table. */
  header: string
  /** Value accessor over an enriched row, returning a display string. */
  accessor: (row: EnrichedInstanceSummary) => string
  /** Whether the user can sort by this column. */
  sortable: boolean
  /** Densities in which this column is visible. */
  densities: QueueDensity[]
}

const ALL_DENSITIES: QueueDensity[] = ['balanced', 'dense', 'expanded']

/** COLUMNS is the single source of truth for the queue-list columns. */
export const COLUMNS: ColumnDef[] = [
  {
    key: 'workflow',
    header: 'Workflow',
    accessor: (r) => r.workflowName,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'runName',
    header: 'Run',
    accessor: (r) => r.runName,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'submitter',
    header: 'Submitter',
    accessor: (r) => r.submitterName ?? '—',
    sortable: true,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'submittedAt',
    header: 'Submitted',
    accessor: (r) => r.createdAt,
    sortable: true,
    densities: ['expanded'],
  },
  {
    key: 'status',
    header: 'Status',
    accessor: (r) => r.state,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'friendlyStep',
    header: 'Step',
    accessor: (r) => r.friendlyStep ?? '—',
    sortable: false,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'elapsed',
    header: 'Elapsed',
    accessor: (r) => r.createdAt,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'queuePosition',
    header: 'Queue',
    accessor: (r) => (r.queuePosition && r.queuePosition > 0 ? String(r.queuePosition) : '—'),
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'modelBackend',
    header: 'Model / Backend',
    accessor: (r) => {
      const parts: string[] = []
      if (r.model) parts.push(r.model)
      if (r.backend) parts.push(r.backend)
      return parts.length > 0 ? parts.join(' · ') : '—'
    },
    sortable: false,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'retryCount',
    header: 'Retries',
    accessor: (r) => String(r.retryCount ?? 0),
    sortable: true,
    densities: ['expanded'],
  },
]

/** visibleColumns returns the columns visible in the given density, in order. */
export function visibleColumns(density: QueueDensity): ColumnDef[] {
  return COLUMNS.filter((c) => c.densities.includes(density))
}

/** getColumn returns the column def for a key, or undefined. */
export function getColumn(key: string): ColumnDef | undefined {
  return COLUMNS.find((c) => c.key === key)
}
```

- [ ] Step 5: Run the test, confirm it passes

```bash
cd dashboard && npx vitest run src/components/queueColumns.test.ts
```

Expected: all assertions pass.

- [ ] Step 6: Run typecheck

```bash
cd dashboard && npx tsc --noEmit
```

Expected: no type errors.

- [ ] Step 7: Commit

```bash
git add dashboard/src/api/types.ts dashboard/src/api/client.ts dashboard/src/components/queueColumns.ts dashboard/src/components/queueColumns.test.ts
git commit -m "feat(dashboard): add queueColumns single-source-of-truth column definitions"
```