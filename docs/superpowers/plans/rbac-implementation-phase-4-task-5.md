# RBAC Phase 4 — Task 5: Dashboard tab + route (before Queue)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a **Dashboard** nav tab positioned **before** the Queue tab, a `/dashboard` route, and a minimal `DashboardView` shell component that the later tasks flesh out.

**Architecture:** A new `NavLink to="/dashboard"` rendered first in `NavBar.tsx`; a new `<Route path="/dashboard" element={<DashboardView/>} />` in `App.tsx`; a minimal `routes/DashboardView.tsx` that renders an identifiable heading (so the route + nav are testable now, before the data/tiles arrive in Task 7).

**Tech Stack:** React 19 + TypeScript, react-router-dom, Vitest + Testing Library.

**Dependencies:** None (independent of Tasks 1-4). Task 7 replaces the `DashboardView` body; the route + nav established here remain. Do this early so the navigation shell is in place.

---

## Context You Need

- `dashboard/src/components/NavBar.tsx`: tabs are `NavLink`s inside `<div className={styles.navLinks}>`. The Queue link is `to="/"` with the `end` prop. The Dashboard link must come **before** it in source order so it renders first.
- `dashboard/src/App.tsx`: routes live in `<Routes>`; `<Route path="/" element={<QueueView />} />` is the Queue route. Add the dashboard route alongside.
- `dashboard/src/components/NavBar.module.css` already defines `.navLink` / `.navLinkActive`; reuse them — no new CSS needed.
- Component test precedent: `dashboard/src/components/InstanceRow.test.tsx` wraps in `<MemoryRouter>` and uses Testing Library queries.

## File Structure

- Create `dashboard/src/routes/DashboardView.tsx` — minimal shell (Task 7 expands it).
- Modify `dashboard/src/components/NavBar.tsx` — add the Dashboard `NavLink` first.
- Modify `dashboard/src/App.tsx` — import `DashboardView`, add the route.
- Create `dashboard/src/components/NavBar.test.tsx` — assert tab presence + ordering.

---

### Task 5a: Minimal DashboardView shell

**Files:**
- Create: `dashboard/src/routes/DashboardView.tsx`

- [ ] **Step 1: Create the shell**

Create `dashboard/src/routes/DashboardView.tsx`:

```tsx
// DashboardView renders the department operational dashboard. This is the Task 5
// shell; Task 7 replaces the body with the stats tiles, per-workflow table, the
// department selector (Task 6), and live websocket refresh.
export default function DashboardView() {
  return (
    <div style={{ padding: '16px 0' }}>
      <h1 style={{ padding: '0 16px', fontSize: 20 }}>Dashboard</h1>
    </div>
  )
}
```

- [ ] **Step 2: Type-check**

Run: `cd dashboard && npx tsc --noEmit`
Expected: exit 0

---

### Task 5b: NavBar tab (before Queue)

**Files:**
- Modify: `dashboard/src/components/NavBar.tsx`
- Test: `dashboard/src/components/NavBar.test.tsx`

- [ ] **Step 3: Write the failing nav test**

Create `dashboard/src/components/NavBar.test.tsx`:

```tsx
import { render, screen, within } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import NavBar from './NavBar'

function renderNav() {
  return render(
    <MemoryRouter>
      <NavBar />
    </MemoryRouter>,
  )
}

describe('NavBar', () => {
  it('renders a Dashboard tab linking to /dashboard', () => {
    renderNav()
    const link = screen.getByRole('link', { name: 'Dashboard' })
    expect(link).toHaveAttribute('href', '/dashboard')
  })

  it('orders Dashboard before Queue', () => {
    const { container } = renderNav()
    const links = within(container).getAllByRole('link').map((a) => a.textContent)
    const dashIdx = links.indexOf('Dashboard')
    const queueIdx = links.indexOf('Queue')
    expect(dashIdx).toBeGreaterThanOrEqual(0)
    expect(queueIdx).toBeGreaterThanOrEqual(0)
    expect(dashIdx).toBeLessThan(queueIdx)
  })
})
```

- [ ] **Step 4: Run it to verify it fails**

Run: `cd dashboard && npx vitest run src/components/NavBar.test.tsx`
Expected: FAIL — no link named "Dashboard".

- [ ] **Step 5: Add the Dashboard NavLink first**

In `dashboard/src/components/NavBar.tsx`, inside `<div className={styles.navLinks}>`, add the Dashboard link as the **first** child (immediately after the opening `<div>` tag, before the Queue `NavLink to="/"`):

```tsx
        <NavLink
          to="/dashboard"
          className={({ isActive }) =>
            `${styles.navLink} ${isActive ? styles.navLinkActive : ''}`
          }
        >
          Dashboard
        </NavLink>
```

- [ ] **Step 6: Run the nav test to verify it passes**

Run: `cd dashboard && npx vitest run src/components/NavBar.test.tsx`
Expected: PASS (both scenarios green)

---

### Task 5c: App route

**Files:**
- Modify: `dashboard/src/App.tsx`

- [ ] **Step 7: Import DashboardView and add the route**

In `dashboard/src/App.tsx`, add the import after `import QueueView from './routes/QueueView'`:

```tsx
import DashboardView from './routes/DashboardView'
```

Inside `<Routes>`, add the route (placement among routes does not affect matching since paths are exact; put it above the `/` route for readability):

```tsx
            <Route path="/dashboard" element={<DashboardView />} />
```

- [ ] **Step 8: Build the frontend bundle to confirm the route compiles**

Run: `cd dashboard && npx tsc --noEmit && npm run build`
Expected: exit 0 (Vite build succeeds).

- [ ] **Step 9: Run the frontend test suite**

Run: `cd dashboard && npx vitest run`
Expected: PASS (existing tests + the new NavBar tests green)

- [ ] **Step 10: Commit**

```bash
git add dashboard/src/routes/DashboardView.tsx dashboard/src/components/NavBar.tsx dashboard/src/components/NavBar.test.tsx dashboard/src/App.tsx
git commit -m "feat(dashboard): Dashboard tab (before Queue) + /dashboard route shell

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec/design coverage: "React Dashboard tab BEFORE Queue (NavBar + App route /dashboard)" — tab added first; ordering pinned by a test; route added. The `end` prop on the Queue link keeps it from matching `/dashboard`.
- TDD categories: happy-path (tab links to /dashboard) + ordering (Dashboard before Queue) are the meaningful behaviors here; this is a structural change so there is no error/edge surface to drive.
- The `DashboardView` body is intentionally a placeholder; Task 7 owns the real content. Keeping the route/nav as a separate task lets it land and stay stable while the data layer is built.
