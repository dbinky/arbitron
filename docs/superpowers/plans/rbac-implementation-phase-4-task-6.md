# RBAC Phase 4 — Task 6: Department selector component

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A presentational `DepartmentSelector` component that renders **nothing** for a single-department (or default-group) user, and an **"All my departments" union default + per-department dropdown** for multi-department users. It reports the current selection as `string[] | null` (null ⇒ union default ⇒ omit the `departments` param).

**Architecture:** A controlled component: parent owns the selection state (Task 7), this component renders the `<select>` and maps option changes to a typed `onChange(selection)` callback. The single-vs-multi decision is driven purely by the `departments` prop length, so all logic is unit-testable with no network.

**Tech Stack:** React 19 + TypeScript, a CSS module reusing the `UsageStats` select styling, Vitest + Testing Library.

**Dependencies:** None at build time (it is a pure component). Task 7 consumes it, passing `me.departments`. Best done before Task 7 so the selector is proven in isolation.

---

## Context You Need

- The selection contract (shared with `api.dashboard.stats` from Task 4): `null` means "all my departments" (union default → omit the param); `["engineering"]` means a single focused department.
- Design doc: "If the user has one effective department (or only the default group): no selector. If multiple: a dropdown with 'All my departments' (union, default) plus each department."
- `dashboard/src/components/UsageStats.module.css` `.rangeSelect` is the styling precedent for a compact select; create a sibling module rather than importing another component's CSS.
- A sentinel option value `__all__` represents the union default inside the `<select>` (since an option value cannot be `null`).

## File Structure

- Create `dashboard/src/components/DepartmentSelector.tsx` — the controlled component.
- Create `dashboard/src/components/DepartmentSelector.module.css` — compact select styling.
- Create `dashboard/src/components/DepartmentSelector.test.tsx` — TDD scenarios.

---

### Task 6: DepartmentSelector

**Files:**
- Create: `dashboard/src/components/DepartmentSelector.tsx`
- Create: `dashboard/src/components/DepartmentSelector.module.css`
- Test: `dashboard/src/components/DepartmentSelector.test.tsx`

- [ ] **Step 1: Write the failing tests**

Create `dashboard/src/components/DepartmentSelector.test.tsx`:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DepartmentSelector from './DepartmentSelector'

describe('DepartmentSelector', () => {
  it('renders nothing for a single-department user', () => {
    const { container } = render(
      <DepartmentSelector departments={['engineering']} selection={null} onChange={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByRole('combobox')).toBeNull()
  })

  it('renders nothing for a user in only the default group', () => {
    const { container } = render(
      <DepartmentSelector departments={['default']} selection={null} onChange={vi.fn()} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('renders a dropdown with an "All my departments" option plus each department for multi-dept users', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={vi.fn()} />,
    )
    const select = screen.getByRole('combobox')
    const options = Array.from(select.querySelectorAll('option')).map((o) => o.textContent)
    expect(options).toEqual(['All my departments', 'engineering', 'sales'])
  })

  it('defaults the dropdown to the union ("All my departments") when selection is null', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={vi.fn()} />,
    )
    expect(screen.getByRole('combobox')).toHaveValue('__all__')
  })

  it('reflects a focused single-department selection', () => {
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={['sales']} onChange={vi.fn()} />,
    )
    expect(screen.getByRole('combobox')).toHaveValue('sales')
  })

  it('emits null when the union option is chosen', () => {
    const onChange = vi.fn()
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={['engineering']} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '__all__' } })
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('emits a single-element array when a department is chosen', () => {
    const onChange = vi.fn()
    render(
      <DepartmentSelector departments={['engineering', 'sales']} selection={null} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'engineering' } })
    expect(onChange).toHaveBeenCalledWith(['engineering'])
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd dashboard && npx vitest run src/components/DepartmentSelector.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Create the CSS module**

Create `dashboard/src/components/DepartmentSelector.module.css`:

```css
.select {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  border: 1px solid var(--border-color);
  background: var(--bg-secondary);
  color: var(--text-secondary);
  cursor: pointer;
  outline: none;
}

.select:focus {
  border-color: var(--accent);
}
```

- [ ] **Step 4: Implement the component**

Create `dashboard/src/components/DepartmentSelector.tsx`:

```tsx
import styles from './DepartmentSelector.module.css'

/** Sentinel <option> value for the union ("All my departments") default. */
const ALL = '__all__'

interface DepartmentSelectorProps {
  /** The caller's effective departments (from /me). */
  departments: string[]
  /**
   * Current selection: null ⇒ union default ("all my departments"); a
   * single-element array ⇒ that department is focused.
   */
  selection: string[] | null
  /** Called with the new selection (null for union, [dept] for focused). */
  onChange: (selection: string[] | null) => void
}

/**
 * Renders a department focus dropdown. A user with a single effective
 * department (or only the default group) gets no selector at all — there is
 * nothing to choose. Multi-department users get an "All my departments" union
 * default plus one option per department.
 */
export default function DepartmentSelector({ departments, selection, onChange }: DepartmentSelectorProps) {
  if (departments.length <= 1) {
    return null
  }

  const value = selection && selection.length === 1 ? selection[0] : ALL

  return (
    <select
      className={styles.select}
      aria-label="Department"
      value={value}
      onChange={(e) => {
        const v = e.target.value
        onChange(v === ALL ? null : [v])
      }}
    >
      <option value={ALL}>All my departments</option>
      {departments.map((d) => (
        <option key={d} value={d}>
          {d}
        </option>
      ))}
    </select>
  )
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd dashboard && npx vitest run src/components/DepartmentSelector.test.tsx`
Expected: PASS (all scenarios green)

- [ ] **Step 6: Type-check and full frontend suite**

Run: `cd dashboard && npx tsc --noEmit && npx vitest run`
Expected: exit 0; all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add dashboard/src/components/DepartmentSelector.tsx dashboard/src/components/DepartmentSelector.module.css dashboard/src/components/DepartmentSelector.test.tsx
git commit -m "feat(dashboard): DepartmentSelector (no selector single-dept; union default + dropdown multi-dept)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec/design coverage: "none if single dept; else 'All my departments' union default + dropdown driven by /me"; "A user in exactly one real department, or in the default group, simply sees that group with no need to choose." — `departments.length <= 1` returns null covers both the single-real-department and default-group cases.
- TDD categories: happy-path (multi-dept dropdown + options), edge (single dept and default-group render nothing; value defaults to union), error — N/A for a pure presentational select, and authorization is enforced server-side (the selector only narrows within the caller's own `/me` departments). Admin "all observed departments" behavior is handled in Task 7 by passing the admin's full department list into this same component.
- The selection contract (`null` ⇒ omit param) exactly matches `api.dashboard.stats(departments, window)` from Task 4.
