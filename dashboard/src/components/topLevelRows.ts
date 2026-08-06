import type { EnrichedInstanceSummary } from '../api/types'

/**
 * topLevelRows decides which rows of ONE queue section render at the top level.
 *
 * A row is suppressed at top level ONLY when its parent is present in the SAME
 * section — because that is the only case where the row is genuinely reachable
 * by expanding its parent's row.
 *
 * A blanket roots-only rule would be wrong here. QueueView renders one section
 * PER STATE, and the normal fire-and-forget shape is a parent that COMPLETES
 * while its child keeps running: parent and child land in different sections. A
 * roots-only Running section would drop that child entirely, leaving it
 * reachable only by expanding a completed parent inside the `since`-windowed
 * Completed section — the "losing visibility" the spec rules out, and the
 * opposite of the operator outcome (inspect/retry/resume/cancel at the child
 * level) the hierarchy exists to serve.
 *
 * A row on a cycle of corrupt/hand-edited links — self-referential, or looping
 * through two or more rows — is never erased. Each parentInstanceId points to
 * at most one row, so a cycle has no member outside it to expand and reveal
 * the rest; suppressing every member would erase the whole cycle from the
 * section with no way to bring it back into view.
 */
export function topLevelRows(rows: EnrichedInstanceSummary[]): EnrichedInstanceSummary[] {
  const byId = new Map(rows.map((r) => [r.id, r]))

  function isOnCycle(id: string): boolean {
    let currentId = id
    const seen = new Set<string>()
    for (;;) {
      const parentId = byId.get(currentId)?.parentInstanceId
      if (!parentId || !byId.has(parentId)) return false
      if (parentId === id) return true
      if (seen.has(parentId)) return false // a different cycle, not involving id
      seen.add(parentId)
      currentId = parentId
    }
  }

  return rows.filter((r) => !r.parentInstanceId || !byId.has(r.parentInstanceId) || isOnCycle(r.id))
}
