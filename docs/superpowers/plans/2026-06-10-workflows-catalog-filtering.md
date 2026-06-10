# Workflows Catalog Filtering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add scope / visibility / department filtering to the dashboard Workflows tab, performed client-side, with URL-persisted state and a mode-aware default view.

**Architecture:** The list endpoint (`GET /api/v1/workflows`) already returns `scope`/`visibility`/`publisher`/`department` per entry; the dashboard's TypeScript type drops them and the component ignores them. We widen the type, filter in-memory in `WorkflowsCatalog.tsx` (same pattern as the existing name search), persist filter state in the URL via react-router `useSearchParams`, and choose the default view from a new `authMode` field on `/me`. Filter controls and columns are shown only when the loaded data contains the relevant facet (data-driven adaptive chrome, mirroring the existing `DepartmentSelector`).

**Tech Stack:** Go (chi, `net/http`, `httptest`) for the `/me` change; React + TypeScript, react-router-dom v7 (`useSearchParams`), Vitest + `@testing-library/react` (`MemoryRouter`) for the dashboard.

---

## File Structure

| File | Responsibility | Action |
|------|----------------|--------|
| `adapter/http/me_handler.go` | Serialize `authMode` on `/me`; handler holds the configured mode | Modify |
| `adapter/http/me_handler_test.go` | Assert `authMode` for local & org identities | Modify |
| `adapter/http/server.go` | Pass `d.Config.Auth.Mode` into `NewMeHandler` at composition root | Modify (line ~249) |
| `dashboard/src/api/types.ts` | Add `scope`/`visibility`/`publisher`/`department` to `WorkflowSummary`; add `authMode` to `Me` | Modify |
| `dashboard/src/routes/WorkflowsCatalog.tsx` | Filter state (URL-backed), in-memory filter chain, adaptive chrome | Modify |
| `dashboard/src/routes/WorkflowsCatalog.module.css` | Segmented-control, select, and badge classes | Modify |
| `dashboard/src/routes/WorkflowsCatalog.test.tsx` | Component behavior tests | Create |

Test commands:
- Go: `go test ./adapter/http/ -run TestMeHandler -v`
- Dashboard unit: `cd dashboard && npx vitest run src/routes/WorkflowsCatalog.test.tsx`
- Dashboard type-check: `cd dashboard && npx tsc --noEmit`

---

## Task 1: Expose `authMode` on `/me`

**Files:**
- Modify: `adapter/http/me_handler.go`
- Modify: `adapter/http/server.go` (line ~249)
- Test: `adapter/http/me_handler_test.go`

- [ ] **Step 1: Write the failing test**

Add to `adapter/http/me_handler_test.go`:

```go
// TestMeHandler_AuthMode verifies the handler serializes the configured operating
// mode so the client can pick a mode-aware default view. An unknown/empty mode
// normalizes to "local".
func TestMeHandler_AuthMode(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want string
	}{
		{"local", "local", "local"},
		{"org", "org", "org"},
		{"edge", "edge", "edge"},
		{"empty normalizes to local", "", "local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := identity.Identity{
				UserID:      uuid.New(),
				Username:    "u",
				Role:        identity.RoleAdmin,
				Departments: []string{"default"},
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			req = req.WithContext(identity.WithIdentity(req.Context(), id))
			NewMeHandler(tc.mode).Me(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var got struct {
				AuthMode string `json:"authMode"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.AuthMode != tc.want {
				t.Errorf("authMode = %q, want %q", got.AuthMode, tc.want)
			}
		})
	}
}
```

Note: the existing `TestMeHandler_LocalModeIdentity` calls `NewMeHandler()` with no args — it will no longer compile after Step 3. Update that one call site to `NewMeHandler("local")` in this same step (it asserts nothing about authMode, so no other change is needed).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/http/ -run TestMeHandler -v`
Expected: compile error — `not enough arguments in call to NewMeHandler` (and once the call sites are fixed, `TestMeHandler_AuthMode` FAILs because `authMode` is absent / empty).

- [ ] **Step 3: Implement the change**

Edit `adapter/http/me_handler.go`. Add the `domain/identity` import (already imported), change the struct, constructor, DTO, and handler:

```go
// MeHandler serves GET /api/v1/me, the whoami endpoint. It reads the caller's
// already-resolved identity from context (the auth middleware populated it) and
// returns the projection that drives client gating, the dashboard department
// selector, and the workflows-catalog default view. It performs no DB read.
type MeHandler struct {
	// mode is the configured operating mode ("local"|"org"|"edge"), normalized.
	mode string
}

// NewMeHandler builds the stateless me handler. mode is the configured auth mode
// (config.AuthModeLocal/Org/Edge); an unknown/empty value normalizes to "local".
func NewMeHandler(mode string) *MeHandler {
	return &MeHandler{mode: string(identity.ModeFromString(mode))}
}
```

Add the field to `meView` (after `IsAdmin`):

```go
	IsAdmin        bool     `json:"isAdmin"`
	// AuthMode is the server operating mode ("local"|"org"|"edge"). Non-secret;
	// drives the dashboard's mode-aware default view.
	AuthMode       string   `json:"authMode"`
```

Set it in the `WriteJSON(...)` call inside `Me`:

```go
	WriteJSON(w, http.StatusOK, meView{
		UserID:         id.UserID.String(),
		Username:       id.Username,
		Role:           string(id.Role),
		Departments:    depts,
		LedDepartments: led,
		IsAdmin:        id.IsAdmin(),
		AuthMode:       h.mode,
	})
```

Then edit `adapter/http/server.go` at the `meHandler := NewMeHandler()` line (~249):

```go
		meHandler := NewMeHandler(d.Config.Auth.Mode)
```

(`d.Config` is always non-nil here — `NewRouterFromDeps` seeds one from `Config` when absent.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./adapter/http/ -run TestMeHandler -v`
Expected: PASS (all `TestMeHandler_AuthMode` subtests and the existing `TestMeHandler_LocalModeIdentity`).

Then the full package: `go test ./adapter/http/` → `ok`.

- [ ] **Step 5: Commit**

```bash
git add adapter/http/me_handler.go adapter/http/me_handler_test.go adapter/http/server.go
git commit -m "feat(me): expose authMode on /me for mode-aware client defaults

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Widen dashboard API types

**Files:**
- Modify: `dashboard/src/api/types.ts`

This is a type-only change (no runtime behavior), verified by `tsc` and exercised by the Task 3 component tests. No standalone test.

- [ ] **Step 1: Extend `WorkflowSummary`**

In `dashboard/src/api/types.ts`, the `WorkflowSummary` interface currently is:

```ts
export interface WorkflowSummary {
  name: string
  description: string
  author: string
  nodeCount?: number
}
```

Replace it with (fields optional, matching the Go `omitempty`):

```ts
export interface WorkflowSummary {
  name: string
  description: string
  author: string
  nodeCount?: number
  /** "private" (caller's own) or "shared" (published). */
  scope?: 'private' | 'shared'
  /** Publishing user's id for shared entries; empty for private. */
  publisher?: string
  /** "public" or "department" for shared entries; empty for private. */
  visibility?: 'public' | 'department'
  /** Owning department for department-visibility shared entries. */
  department?: string
}
```

- [ ] **Step 2: Add `authMode` to `Me`**

In the `Me` interface, add after `isAdmin`:

```ts
  /** Server operating mode; drives the workflows-catalog default view. */
  authMode: 'local' | 'org' | 'edge'
```

- [ ] **Step 3: Verify it type-checks**

Run: `cd dashboard && npx tsc --noEmit`
Expected: no errors. (If any test fixture constructs a `Me` literal without `authMode`, it will surface here — fix those fixtures by adding `authMode: 'local'`. Likely fixture: the `Me`-building helper in `dashboard/src/hooks/useDashboardStats.test.ts` near line 26 that returns `{ userId: 'u1', ... }` — add `authMode: 'local'` to its returned object. Grep for other `role: 'admin'` / `isAdmin:` literals if `tsc` flags more.)

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/hooks/useDashboardStats.test.ts
git commit -m "feat(types): surface workflow scope/visibility/department and authMode

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Scope filter + URL state + mode-aware default

Builds the scope tab control, the always-on Scope column, the `me` fetch, URL-backed filter state, and the in-memory filter chain. Visibility/department are added in Task 4.

**Files:**
- Modify: `dashboard/src/routes/WorkflowsCatalog.tsx`
- Modify: `dashboard/src/routes/WorkflowsCatalog.module.css`
- Test: `dashboard/src/routes/WorkflowsCatalog.test.tsx` (create)

- [ ] **Step 1: Write the failing tests**

Create `dashboard/src/routes/WorkflowsCatalog.test.tsx`:

```tsx
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import type { WorkflowSummary, Me } from '../api/types'

const { listFn, meFn } = vi.hoisted(() => ({ listFn: vi.fn(), meFn: vi.fn() }))
vi.mock('../api/client', () => ({
  api: { workflows: { list: listFn }, me: meFn },
}))

import WorkflowsCatalog from './WorkflowsCatalog'

function makeMe(over: Partial<Me> = {}): Me {
  return { userId: 'u1', username: 'local', role: 'admin', departments: ['default'], ledDepartments: [], isAdmin: true, authMode: 'local', ...over }
}

const WF: WorkflowSummary[] = [
  { name: 'my-draft', description: '', author: 'dbinky', nodeCount: 2, scope: 'private' },
  { name: 'feature-pipeline', description: '', author: 'arbitron', nodeCount: 23, scope: 'shared', visibility: 'public' },
  { name: 'pr-review', description: '', author: 'arbitron', nodeCount: 9, scope: 'shared', visibility: 'public' },
]

function renderAt(path = '/workflows') {
  return render(<MemoryRouter initialEntries={[path]}><WorkflowsCatalog /></MemoryRouter>)
}

describe('WorkflowsCatalog scope filtering', () => {
  beforeEach(() => {
    listFn.mockReset(); meFn.mockReset()
    listFn.mockResolvedValue(WF)
    meFn.mockResolvedValue(makeMe())
  })

  it('defaults to All in local mode (shows private + shared)', async () => {
    renderAt()
    expect(await screen.findByText('my-draft')).toBeInTheDocument()
    expect(screen.getByText('feature-pipeline')).toBeInTheDocument()
  })

  it('defaults to Shared in org mode (hides private)', async () => {
    meFn.mockResolvedValue(makeMe({ authMode: 'org' }))
    renderAt()
    expect(await screen.findByText('feature-pipeline')).toBeInTheDocument()
    expect(screen.queryByText('my-draft')).not.toBeInTheDocument()
  })

  it('honors an explicit scope URL param over the mode default', async () => {
    renderAt('/workflows?scope=mine')
    expect(await screen.findByText('my-draft')).toBeInTheDocument()
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
  })

  it('clicking a scope tab filters and writes the URL param', async () => {
    renderAt()
    await screen.findByText('my-draft')
    fireEvent.click(screen.getByRole('tab', { name: 'Shared' }))
    await waitFor(() => expect(screen.queryByText('my-draft')).not.toBeInTheDocument())
    expect(screen.getByText('feature-pipeline')).toBeInTheDocument()
  })

  it('filters by name search alongside scope', async () => {
    renderAt('/workflows?scope=all')
    await screen.findByText('my-draft')
    fireEvent.change(screen.getByPlaceholderText('Search workflows...'), { target: { value: 'pr-' } })
    await waitFor(() => expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument())
    expect(screen.getByText('pr-review')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd dashboard && npx vitest run src/routes/WorkflowsCatalog.test.tsx`
Expected: FAIL — current component does not call `api.me()` (mock unused), has no scope tabs (`getByRole('tab')` throws), and renders all rows regardless of scope.

- [ ] **Step 3: Implement the component**

Replace the entire contents of `dashboard/src/routes/WorkflowsCatalog.tsx` with:

```tsx
import { useState, useEffect, useMemo, useCallback } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import type { WorkflowSummary, Me } from '../api/types'
import { api } from '../api/client'
import styles from './WorkflowsCatalog.module.css'

type Scope = 'mine' | 'shared' | 'all'

/** Mode-aware default scope: local mode shows everything, org/edge default to the
 * shared catalog. Used when no explicit ?scope= URL param is present. */
function defaultScope(me: Me | null): Scope {
  return me && me.authMode !== 'local' ? 'shared' : 'all'
}

const SCOPE_LABELS: Record<Scope, string> = { mine: 'Mine', shared: 'Shared', all: 'All' }

export default function WorkflowsCatalog() {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const [workflows, setWorkflows] = useState<WorkflowSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      try {
        const [wf, meResp] = await Promise.all([api.workflows.list(), api.me()])
        if (!cancelled) {
          setWorkflows(wf)
          setMe(meResp)
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : 'Failed to load workflows')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  // URL-backed filter state. An unrecognized value falls back to the default.
  const search = searchParams.get('q') ?? ''
  const scopeParam = searchParams.get('scope')
  const scope: Scope =
    scopeParam === 'mine' || scopeParam === 'shared' || scopeParam === 'all'
      ? scopeParam
      : defaultScope(me)

  // Update one query param; omit it when it equals its default to keep URLs clean.
  const setParam = useCallback(
    (key: string, value: string, defaultValue: string) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (!value || value === defaultValue) next.delete(key)
          else next.set(key, value)
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return workflows.filter((w) => {
      if (scope === 'mine' && w.scope !== 'private') return false
      if (scope === 'shared' && w.scope !== 'shared') return false
      if (q && !w.name.toLowerCase().includes(q)) return false
      return true
    })
  }, [workflows, scope, search])

  const handleRowClick = useCallback((name: string) => navigate(`/workflows/${name}`), [navigate])

  if (loading) return <div className={styles.loading}>Loading workflows...</div>
  if (error) return <div className={styles.error}>{error}</div>

  const hasFilters = !!search || scope !== defaultScope(me)

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Workflows</h1>
        <div className={styles.controls}>
          <div className={styles.segmented} role="tablist" aria-label="Scope">
            {(['mine', 'shared', 'all'] as Scope[]).map((s) => (
              <button
                key={s}
                type="button"
                role="tab"
                aria-selected={scope === s}
                className={scope === s ? styles.segmentActive : styles.segment}
                onClick={() => setParam('scope', s, defaultScope(me))}
              >
                {SCOPE_LABELS[s]}
              </button>
            ))}
          </div>
          <input
            className={styles.searchInput}
            type="text"
            placeholder="Search workflows..."
            value={search}
            onChange={(e) => setParam('q', e.target.value, '')}
          />
        </div>
      </div>

      {filtered.length === 0 ? (
        <div className={styles.empty}>
          {hasFilters ? 'No workflows match your filters.' : 'No workflows found.'}
        </div>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Description</th>
              <th>Scope</th>
              <th>Author</th>
              <th style={{ textAlign: 'center' }}>Nodes</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((w) => (
              <tr
                key={`${w.scope ?? ''}:${w.name}`}
                className={styles.row}
                onClick={() => handleRowClick(w.name)}
              >
                <td className={styles.nameCell}>{w.name}</td>
                <td>{w.description || '--'}</td>
                <td>
                  <span className={styles.badge}>{w.scope ?? '--'}</span>
                </td>
                <td>{w.author || '--'}</td>
                <td className={styles.nodeCountCell}>{w.nodeCount ?? '--'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
```

- [ ] **Step 4: Add the CSS classes**

Append to `dashboard/src/routes/WorkflowsCatalog.module.css`:

```css
.controls {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.segmented {
  display: inline-flex;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 6px;
  overflow: hidden;
}

.segment,
.segmentActive {
  padding: 0.35rem 0.75rem;
  font-size: 0.85rem;
  background: transparent;
  border: none;
  cursor: pointer;
  color: var(--text, inherit);
}

.segmentActive {
  background: var(--accent, #0969da);
  color: #fff;
}

.select {
  padding: 0.35rem 0.5rem;
  font-size: 0.85rem;
  border: 1px solid var(--border, #d0d7de);
  border-radius: 6px;
  background: transparent;
  color: var(--text, inherit);
}

.badge {
  display: inline-block;
  padding: 0.1rem 0.45rem;
  font-size: 0.75rem;
  border-radius: 999px;
  background: var(--badge-bg, #eaeef2);
  color: var(--badge-fg, #24292f);
  text-transform: lowercase;
}
```

(The `.select` class is added now though it is first used in Task 4; keeping the style block in one commit avoids churn.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd dashboard && npx vitest run src/routes/WorkflowsCatalog.test.tsx`
Expected: PASS (all 5 tests).

Then type-check: `cd dashboard && npx tsc --noEmit` → no errors.

- [ ] **Step 6: Commit**

```bash
git add dashboard/src/routes/WorkflowsCatalog.tsx dashboard/src/routes/WorkflowsCatalog.module.css dashboard/src/routes/WorkflowsCatalog.test.tsx
git commit -m "feat(dashboard): scope filter + mode-aware default on workflows tab

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Adaptive visibility + department filters

Adds the visibility and department filters and columns, each shown only when the loaded data contains that facet.

**Files:**
- Modify: `dashboard/src/routes/WorkflowsCatalog.tsx`
- Test: `dashboard/src/routes/WorkflowsCatalog.test.tsx`

- [ ] **Step 1: Write the failing tests**

Append these tests inside `WorkflowsCatalog.test.tsx`. First add a richer fixture at the top of the file (after `WF`):

```tsx
const WF_ORG: WorkflowSummary[] = [
  { name: 'my-draft', description: '', author: 'dbinky', nodeCount: 2, scope: 'private' },
  { name: 'feature-pipeline', description: '', author: 'arbitron', nodeCount: 23, scope: 'shared', visibility: 'public' },
  { name: 'billing-close', description: '', author: 'fin', nodeCount: 5, scope: 'shared', visibility: 'department', department: 'finance' },
  { name: 'hr-onboard', description: '', author: 'hr', nodeCount: 4, scope: 'shared', visibility: 'department', department: 'people' },
]
```

Then add a new describe block:

```tsx
describe('WorkflowsCatalog adaptive visibility + department', () => {
  beforeEach(() => {
    listFn.mockReset(); meFn.mockReset()
    meFn.mockResolvedValue(makeMe({ authMode: 'org', departments: ['finance', 'people'] }))
  })

  it('hides department chrome when data has no departments (visibility still shows for shared)', async () => {
    listFn.mockResolvedValue(WF) // all shared entries are public, no department
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('feature-pipeline')
    // Visibility shows (there are shared rows); department must not (no dept in data).
    expect(screen.getByLabelText('Visibility')).toBeInTheDocument()
    expect(screen.queryByLabelText('Department')).not.toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: 'Department' })).not.toBeInTheDocument()
  })

  it('shows visibility + department chrome when data has departments', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.getByLabelText('Visibility')).toBeInTheDocument()
    expect(screen.getByLabelText('Department')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Department' })).toBeInTheDocument()
  })

  it('filters by visibility=department', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all&vis=department']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
    expect(screen.getByText('hr-onboard')).toBeInTheDocument()
  })

  it('filters by a selected department', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all&dept=finance']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    expect(screen.queryByText('hr-onboard')).not.toBeInTheDocument()
    expect(screen.queryByText('feature-pipeline')).not.toBeInTheDocument()
  })

  it('populates the department dropdown from distinct departments in the data', async () => {
    listFn.mockResolvedValue(WF_ORG)
    render(<MemoryRouter initialEntries={['/workflows?scope=all']}><WorkflowsCatalog /></MemoryRouter>)
    await screen.findByText('billing-close')
    const select = screen.getByLabelText('Department') as HTMLSelectElement
    const opts = Array.from(select.options).map((o) => o.value)
    expect(opts).toEqual(['', 'finance', 'people'])
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd dashboard && npx vitest run src/routes/WorkflowsCatalog.test.tsx`
Expected: the new block FAILs — `getByLabelText('Visibility'|'Department')` throws (controls don't exist yet) and `vis`/`dept` params have no effect.

- [ ] **Step 3: Implement the additions**

In `dashboard/src/routes/WorkflowsCatalog.tsx`, make these four edits:

(a) Add the visibility-filter type alias after `type Scope = ...`:

```tsx
type VisFilter = 'all' | 'public' | 'department'
```

(b) After the `scope` derivation, add `vis` and `dept` derivations:

```tsx
  const visParam = searchParams.get('vis')
  const vis: VisFilter = visParam === 'public' || visParam === 'department' ? visParam : 'all'
  const dept = searchParams.get('dept') ?? ''
```

(c) After the `filtered` `useMemo`, derive the facets present in the data and extend the filter. Replace the existing `filtered` `useMemo` with the version below and add the two facet memos before it:

```tsx
  // Adaptive chrome is driven by what the loaded data actually contains.
  const anyShared = useMemo(() => workflows.some((w) => w.scope === 'shared'), [workflows])
  const departments = useMemo(
    () =>
      Array.from(
        new Set(workflows.map((w) => w.department).filter((d): d is string => !!d)),
      ).sort(),
    [workflows],
  )
  const showVisibility = anyShared
  const showDepartment = departments.length > 0

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return workflows.filter((w) => {
      if (scope === 'mine' && w.scope !== 'private') return false
      if (scope === 'shared' && w.scope !== 'shared') return false
      if (vis !== 'all' && w.visibility !== vis) return false
      if (dept && w.department !== dept) return false
      if (q && !w.name.toLowerCase().includes(q)) return false
      return true
    })
  }, [workflows, scope, vis, dept, search])
```

Also extend the `hasFilters` line to account for the new filters:

```tsx
  const hasFilters = !!search || scope !== defaultScope(me) || vis !== 'all' || !!dept
```

(d) Render the two controls inside `.controls`, between the segmented control and the search input:

```tsx
          {showVisibility && (
            <select
              className={styles.select}
              aria-label="Visibility"
              value={vis}
              onChange={(e) => setParam('vis', e.target.value, 'all')}
            >
              <option value="all">All visibility</option>
              <option value="public">Public</option>
              <option value="department">Department</option>
            </select>
          )}
          {showDepartment && (
            <select
              className={styles.select}
              aria-label="Department"
              value={dept}
              onChange={(e) => setParam('dept', e.target.value, '')}
            >
              <option value="">All departments</option>
              {departments.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          )}
```

(e) Add the conditional columns. In `<thead>`, after the Scope `<th>`:

```tsx
              <th>Scope</th>
              {showVisibility && <th>Visibility</th>}
              {showDepartment && <th>Department</th>}
              <th>Author</th>
```

And in the row body, after the Scope `<td>`:

```tsx
                <td>
                  <span className={styles.badge}>{w.scope ?? '--'}</span>
                </td>
                {showVisibility && (
                  <td>{w.visibility ? <span className={styles.badge}>{w.visibility}</span> : '—'}</td>
                )}
                {showDepartment && <td>{w.department || '—'}</td>}
                <td>{w.author || '--'}</td>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd dashboard && npx vitest run src/routes/WorkflowsCatalog.test.tsx`
Expected: PASS (all tests from Task 3 and Task 4).

Then type-check: `cd dashboard && npx tsc --noEmit` → no errors.

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/routes/WorkflowsCatalog.tsx dashboard/src/routes/WorkflowsCatalog.test.tsx
git commit -m "feat(dashboard): adaptive visibility + department filters on workflows tab

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Full-suite gate, rebuild, redeploy, manual check

**Files:** none (verification + deploy).

- [ ] **Step 1: Run the full Go + dashboard suites**

Run: `make test-all`
Expected: Go suite `ok` across packages AND the dashboard suite (tsc + vitest) green. If anything fails, fix before proceeding.

- [ ] **Step 2: Rebuild the binary**

The dashboard SPA is embedded in the Go binary, so the UI change requires a rebuild that includes the freshly built assets.

Run:
```bash
make ui && make build
```
Expected: `make ui` rebuilds the dashboard into the embedded `dist`; `make build` writes `./arbitron` with exit 0.

- [ ] **Step 3: Redeploy via launchd (stop, swap, start)**

```bash
UID_NUM=$(id -u)
launchctl bootout gui/$UID_NUM/dev.arbitron 2>/dev/null; sleep 1
cp arbitron ~/.arbitron/bin/arbitron
launchctl bootstrap gui/$UID_NUM ~/Library/LaunchAgents/dev.arbitron.plist
for i in $(seq 1 20); do lsof -iTCP:8080 -sTCP:LISTEN >/dev/null 2>&1 && break; sleep 1; done
curl -s -o /dev/null -w "health HTTP %{http_code}\n" http://127.0.0.1:8080/api/v1/health
```
Expected: `health HTTP 200`.

- [ ] **Step 4: Manual verification in the browser**

Open the dashboard, go to the Workflows tab. In local mode confirm:
- The `Mine | Shared | All` tabs render; default selection is **All** (both `my-*` private and the seeded shared samples visible).
- A **Scope** column shows `private`/`shared` badges.
- No Visibility/Department controls or columns appear (seeded samples are public, no department).
- Clicking **Shared** updates the URL to `/workflows?scope=shared` and hides private entries; reloading the page preserves the view.

- [ ] **Step 5: Final commit (if any fixups were needed)**

If Steps 1–4 required no code changes, nothing to commit. Otherwise:
```bash
git add -A
git commit -m "fix(dashboard): workflows filtering follow-ups from verification

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Notes / Known Limitations

- **Detail-route ambiguity (pre-existing, out of scope):** a private and a shared workflow can share a name; both appear (distinguished by scope), but the row click navigates to `/workflows/{name}` without disambiguating scope. This is existing behavior and not addressed here; flag for a future task if it becomes a problem.
- **First-paint default:** until `/me` resolves, `defaultScope(null)` returns `all`; once `me` loads, the derived `scope` updates. Acceptable per design review.
