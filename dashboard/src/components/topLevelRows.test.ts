import { it, expect } from 'vitest'
import type { EnrichedInstanceSummary } from '../api/types'
import { topLevelRows } from './topLevelRows'

function r(id: string, parentInstanceId?: string): EnrichedInstanceSummary {
  return {
    id,
    ownerId: 'u1',
    workflowName: `wf-${id}`,
    state: 'running',
    priority: 'normal',
    createdAt: '2024-01-01T00:00:00Z',
    updatedAt: '2024-01-01T00:00:00Z',
    runName: `run — ${id}`,
    retryCount: 0,
    parentInstanceId,
  }
}

// ---------- happy path ----------

it('suppresses a child whose parent is in the same section', () => {
  const rows = [r('p1'), r('c1', 'p1')]
  expect(topLevelRows(rows).map((x) => x.id)).toEqual(['p1'])
})

// ---------- success ----------

// THE VISIBILITY GUARD, and the single most important assertion in this file.
// A fire-and-forget parent normally COMPLETES while its child keeps running, so
// the two land in different state sections. Suppressing the child here would
// erase it from the Running section entirely — it would only be reachable by
// expanding a completed parent inside the `since`-windowed Completed section,
// which is exactly the "losing visibility" the spec rules out.
it('keeps a child whose parent is NOT in the same section', () => {
  const rows = [r('c1', 'p-elsewhere'), r('c2', 'p-also-elsewhere')]
  expect(topLevelRows(rows).map((x) => x.id)).toEqual(['c1', 'c2'])
})

it('keeps every root', () => {
  const rows = [r('a'), r('b'), r('c')]
  expect(topLevelRows(rows)).toHaveLength(3)
})

it('preserves the incoming order', () => {
  const rows = [r('z'), r('y', 'absent'), r('x')]
  expect(topLevelRows(rows).map((x) => x.id)).toEqual(['z', 'y', 'x'])
})

it('suppresses a grandchild only when its OWN parent is present', () => {
  // g's parent c is absent from this section, so g stays visible even though its
  // grandparent p is present. Nesting is one level; the rule must not walk up.
  const rows = [r('p'), r('g', 'c')]
  expect(topLevelRows(rows).map((x) => x.id)).toEqual(['p', 'g'])
})

// ---------- edge cases ----------

it('an empty section is empty', () => {
  expect(topLevelRows([])).toEqual([])
})

it('a self-referential link does not erase the row', () => {
  const rows = [r('self', 'self')]
  expect(topLevelRows(rows).map((x) => x.id)).toEqual(['self'])
})
