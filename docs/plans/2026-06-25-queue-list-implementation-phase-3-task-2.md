# Phase 3 — Task 2: useDensityPreference hook

**Goal:** Add the `useDensityPreference` hook that owns the density value (`balanced` | `dense` | `expanded`), defaulting to `balanced` on first use and persisting each change to `localStorage` under a per-user-scoped key. Pure view state; no network. Corrupt/absent/unreadable storage falls back to `balanced` without throwing; a write failure is swallowed.

**Architecture:** A self-contained React hook under `dashboard/src/hooks/useDensityPreference.ts`. The storage key is per-user-id when a userId is provided, else a global fallback key. Internals (storage key, serialization) can change without consumers caring — they get `{ density, cycle }`. `cycle()` advances balanced → dense → expanded → balanced. No refetch on density change (density is layout-only).

**Tech Stack:** TypeScript, React 19, Vitest + `@testing-library/react` (the hook is tested with `renderHook`).

---

### Task 2: useDensityPreference

**Files:**
- Create: `dashboard/src/hooks/useDensityPreference.ts`
- Create: `dashboard/src/hooks/useDensityPreference.test.ts`

- [x] Step 1: Write the failing test for the hook

Create `dashboard/src/hooks/useDensityPreference.test.ts`:

```ts
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { useDensityPreference } from './useDensityPreference'

describe('useDensityPreference', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('defaults to balanced on first use', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
  })

  it('cycles balanced -> dense -> expanded -> balanced', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    act(() => result.current.cycle())
    expect(result.current.density).toBe('dense')
    act(() => result.current.cycle())
    expect(result.current.density).toBe('expanded')
    act(() => result.current.cycle())
    expect(result.current.density).toBe('balanced')
  })

  it('persists to localStorage under a per-user key', () => {
    const { result } = renderHook(() => useDensityPreference('u1'))
    act(() => result.current.cycle())
    expect(localStorage.getItem('arbitron.queueDensity.u1')).toBe('dense')
  })

  it('reloads the persisted value on next mount', () => {
    localStorage.setItem('arbitron.queueDensity.u1', 'expanded')
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('expanded')
  })

  it('uses a global fallback key when userId is absent', () => {
    const { result } = renderHook(() => useDensityPreference(null))
    act(() => result.current.cycle())
    expect(localStorage.getItem('arbitron.queueDensity')).toBe('dense')
  })

  it('falls back to balanced when stored value is corrupt', () => {
    localStorage.setItem('arbitron.queueDensity.u1', 'garbage')
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
  })

  it('falls back to balanced when localStorage throws on read', () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(result.current.density).toBe('balanced')
    spy.mockRestore()
  })

  it('does not throw when localStorage throws on write', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage full')
    })
    const { result } = renderHook(() => useDensityPreference('u1'))
    expect(() => act(() => result.current.cycle())).not.toThrow()
    spy.mockRestore()
  })
})
```

- [x] Step 2: Run it, confirm it fails

```bash
cd dashboard && npx vitest run src/hooks/useDensityPreference.test.ts
```

Expected: failure — `Failed to resolve import "./useDensityPreference"`.

- [x] Step 3: Implement the hook

Create `dashboard/src/hooks/useDensityPreference.ts`:

```ts
import { useCallback, useState } from 'react'
import type { QueueDensity } from '../api/types'

const DENSITIES: QueueDensity[] = ['balanced', 'dense', 'expanded']
const STORAGE_PREFIX = 'arbitron.queueDensity'

function storageKey(userId: string | null): string {
  return userId ? `${STORAGE_PREFIX}.${userId}` : STORAGE_PREFIX
}

function readDensity(key: string): QueueDensity {
  try {
    const raw = localStorage.getItem(key)
    if (raw && DENSITIES.includes(raw as QueueDensity)) {
      return raw as QueueDensity
    }
  } catch {
    // unreadable/corrupt/absent -> balanced, never throw
  }
  return 'balanced'
}

function writeDensity(key: string, density: QueueDensity): void {
  try {
    localStorage.setItem(key, density)
  } catch {
    // write failure (storage disabled/full) -> swallow; view still works
  }
}

/**
 * useDensityPreference owns the queue-list density view state. It defaults to
 * 'balanced' on first use, persists each change to localStorage under a
 * per-user-scoped key (or a global fallback when userId is null), and never
 * throws on storage errors. Returns `{ density, cycle }` where cycle advances
 * balanced -> dense -> expanded -> balanced.
 */
export function useDensityPreference(userId: string | null): {
  density: QueueDensity
  cycle: () => void
} {
  const key = storageKey(userId)
  const [density, setDensity] = useState<QueueDensity>(() => readDensity(key))

  const cycle = useCallback(() => {
    setDensity((prev) => {
      const idx = DENSITIES.indexOf(prev)
      const next = DENSITIES[(idx + 1) % DENSITIES.length]
      writeDensity(key, next)
      return next
    })
  }, [key])

  return { density, cycle }
}
```

- [x] Step 4: Run the test, confirm it passes

```bash
cd dashboard && npx vitest run src/hooks/useDensityPreference.test.ts
```

Expected: all assertions pass.

- [x] Step 5: Run typecheck

```bash
cd dashboard && npx tsc --noEmit
```

Expected: no type errors.

- [x] Step 6: Commit

```bash
git add dashboard/src/hooks/useDensityPreference.ts dashboard/src/hooks/useDensityPreference.test.ts
git commit -m "feat(dashboard): add useDensityPreference hook (localStorage-backed)"
```