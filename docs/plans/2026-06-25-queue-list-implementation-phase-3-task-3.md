# Phase 3 — Task 3: Density toggle button

**Goal:** Add a compact icon button that cycles the three density views via `useDensityPreference`. Presentation only — it reads the hook and renders the current density label + a cycle affordance.

**Architecture:** A small presentational component under `dashboard/src/components/DensityToggle.tsx` that takes the `density` and `cycle` from the parent (which calls the hook) and renders a button. Keeping the hook call in the parent (`QueueView`) lets the density value flow to both the toggle and the table (Task 4) from one source. CSS module reuses the existing `QueueSection` button styling variables.

**Tech Stack:** TypeScript, React 19, Vitest + `@testing-library/react`.

---

### Task 3: Density toggle button

**Files:**
- Create: `dashboard/src/components/DensityToggle.tsx`
- Create: `dashboard/src/components/DensityToggle.module.css`
- Create: `dashboard/src/components/DensityToggle.test.tsx`

- [x] Step 1: Write the failing test for the toggle

Create `dashboard/src/components/DensityToggle.test.tsx`:

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import DensityToggle from './DensityToggle'
import type { QueueDensity } from '../api/types'

function renderToggle(props: Partial<React.ComponentProps<typeof DensityToggle>> = {}) {
  return render(
    <DensityToggle
      density="balanced"
      onCycle={vi.fn()}
      {...props}
    />,
  )
}

describe('DensityToggle', () => {
  it('renders a button with the current density label', () => {
    renderToggle({ density: 'balanced' as QueueDensity })
    const btn = screen.getByRole('button', { name: /density/i })
    expect(btn).toBeInTheDocument()
    expect(btn.textContent?.toLowerCase()).toContain('balanced')
  })

  it('calls onCycle when clicked', () => {
    const onCycle = vi.fn()
    renderToggle({ onCycle })
    fireEvent.click(screen.getByRole('button', { name: /density/i }))
    expect(onCycle).toHaveBeenCalledTimes(1)
  })

  it('reflects the dense label when density is dense', () => {
    renderToggle({ density: 'dense' as QueueDensity })
    expect(screen.getByRole('button', { name: /density/i }).textContent?.toLowerCase()).toContain('dense')
  })

  it('reflects the expanded label when density is expanded', () => {
    renderToggle({ density: 'expanded' as QueueDensity })
    expect(screen.getByRole('button', { name: /density/i }).textContent?.toLowerCase()).toContain('expanded')
  })

  it('has an accessible aria-label', () => {
    renderToggle()
    expect(screen.getByLabelText(/cycle density/i)).toBeInTheDocument()
  })
})
```

- [x] Step 2: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/components/DensityToggle.test.tsx
```

Expected: failure — `Failed to resolve import "./DensityToggle"`.

- [x] Step 3: Implement the component

Create `dashboard/src/components/DensityToggle.tsx`:

```tsx
import type { QueueDensity } from '../api/types'
import styles from './DensityToggle.module.css'

interface DensityToggleProps {
  density: QueueDensity
  onCycle: () => void
}

const DENSITY_ICON: Record<QueueDensity, string> = {
  balanced: '☰',
  dense: '≣',
  expanded: '📋',
}

/**
 * DensityToggle is a compact icon button that cycles the queue-list density
 * views (balanced → dense → expanded). Presentation only — the parent owns
 * the hook and passes the current density + cycle callback.
 */
export default function DensityToggle({ density, onCycle }: DensityToggleProps) {
  return (
    <button
      type="button"
      className={styles.toggle}
      onClick={onCycle}
      aria-label={`Cycle density (current: ${density})`}
      title={`Density: ${density} — click to cycle`}
    >
      <span className={styles.icon}>{DENSITY_ICON[density]}</span>
      <span className={styles.label}>{density}</span>
    </button>
  )
}
```

Create `dashboard/src/components/DensityToggle.module.css`:

```css
.toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  color: var(--text-secondary);
  font-size: 11px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}

.toggle:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.icon {
  font-size: 14px;
  line-height: 1;
}

.label {
  text-transform: capitalize;
}
```

- [x] Step 4: Run the test, confirm it passes

```bash
cd dashboard && npx vitest run src/components/DensityToggle.test.tsx
```

Expected: all assertions pass.

- [x] Step 5: Run typecheck

```bash
cd dashboard && npx tsc --noEmit
```

Expected: no type errors.

- [x] Step 6: Commit

```bash
git add dashboard/src/components/DensityToggle.tsx dashboard/src/components/DensityToggle.module.css dashboard/src/components/DensityToggle.test.tsx
git commit -m "feat(dashboard): add DensityToggle icon button"
```