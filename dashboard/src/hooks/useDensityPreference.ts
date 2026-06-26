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