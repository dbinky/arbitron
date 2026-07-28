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
 * A self-referential link (corrupt data) never erases its own row.
 */
export function topLevelRows(rows: EnrichedInstanceSummary[]): EnrichedInstanceSummary[] {
  const present = new Set(rows.map((r) => r.id))
  return rows.filter(
    (r) => !r.parentInstanceId || r.parentInstanceId === r.id || !present.has(r.parentInstanceId),
  )
}
