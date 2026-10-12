import type { EnrichedInstanceSummary } from '../api/types'

type BindingFields = Pick<EnrichedInstanceSummary, 'model' | 'effort' | 'backend' | 'provider' | 'nodeType'> & {
  state: string
}

/**
 * formatBinding renders what a run is running on NOW as
 * "model (effort) · harness · provider", omitting empty parts. A non-model
 * node shows its type (e.g. "script"); completed/cancelled runs and rows with
 * nothing known show "—". Shared by the queue column and the row detail panel.
 */
export function formatBinding(row: BindingFields): string {
  if (row.state === 'completed' || row.state === 'cancelled') return '—'
  const parts: string[] = []
  if (row.model) parts.push(row.effort ? `${row.model} (${row.effort})` : row.model)
  if (row.backend) parts.push(row.backend)
  if (row.provider) parts.push(row.provider)
  if (parts.length > 0) return parts.join(' · ')
  return row.nodeType || '—'
}
