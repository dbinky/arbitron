import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'

/** A single queue-list column declaration. */
export interface ColumnDef {
  /** Stable key identifying the column. */
  key: string
  /** Header label shown in the table. */
  header: string
  /** Value accessor over an enriched row, returning a display string. */
  accessor: (row: EnrichedInstanceSummary) => string
  /** Whether the user can sort by this column. */
  sortable: boolean
  /** Densities in which this column is visible. */
  densities: QueueDensity[]
}

const ALL_DENSITIES: QueueDensity[] = ['balanced', 'dense', 'expanded']

/** COLUMNS is the single source of truth for the queue-list columns. */
export const COLUMNS: ColumnDef[] = [
  {
    key: 'workflow',
    header: 'Workflow',
    accessor: (r) => r.workflowName,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'runName',
    header: 'Run',
    accessor: (r) => r.runName,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'submitter',
    header: 'Submitter',
    accessor: (r) => r.submitterName ?? '—',
    sortable: true,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'submittedAt',
    header: 'Submitted',
    accessor: (r) => r.createdAt,
    sortable: true,
    densities: ['expanded'],
  },
  {
    key: 'status',
    header: 'Status',
    accessor: (r) => r.state,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'friendlyStep',
    header: 'Step',
    accessor: (r) => r.friendlyStep ?? '—',
    sortable: false,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'elapsed',
    header: 'Elapsed',
    accessor: (r) => r.createdAt,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'queuePosition',
    header: 'Queue',
    accessor: (r) => (r.queuePosition && r.queuePosition > 0 ? String(r.queuePosition) : '—'),
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'modelBackend',
    header: 'Model / Backend',
    accessor: (r) => {
      const parts: string[] = []
      if (r.model) parts.push(r.model)
      if (r.backend) parts.push(r.backend)
      return parts.length > 0 ? parts.join(' · ') : '—'
    },
    sortable: false,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'retryCount',
    header: 'Retries',
    accessor: (r) => String(r.retryCount ?? 0),
    sortable: true,
    densities: ['expanded'],
  },
]

/** visibleColumns returns the columns visible in the given density, in order. */
export function visibleColumns(density: QueueDensity): ColumnDef[] {
  return COLUMNS.filter((c) => c.densities.includes(density))
}

/** getColumn returns the column def for a key, or undefined. */
export function getColumn(key: string): ColumnDef | undefined {
  return COLUMNS.find((c) => c.key === key)
}