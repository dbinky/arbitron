import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import { runDuration } from '../utils/time'
import { formatBinding } from './formatBinding'

/** A single queue-list column declaration. */
export interface ColumnDef {
  /** Stable key identifying the column. */
  key: string
  /** Header label shown in the table. */
  header: string
  /** Value accessor over an enriched row, returning a display string. */
  accessor: (row: EnrichedInstanceSummary) => string
  /**
   * Optional sort key accessor. When present, sorting compares this value
   * (a number for date/numeric columns, e.g. a timestamp or a count) instead of
   * the display string. Defaults to `accessor` (string compare) when omitted.
   */
  sortAccessor?: (row: EnrichedInstanceSummary) => string | number
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
    key: 'git',
    header: 'Git',
    accessor: (r) => r.gitRef || '—',
    sortable: true,
    densities: ['balanced', 'expanded'],
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
    sortAccessor: (r) => new Date(r.createdAt).getTime(),
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
    // Sort by how long the run has taken (frozen at completion for terminal
    // rows). The clock is read at sort time; the cell itself ticks via useNow.
    sortAccessor: (r) => runDuration(r, Date.now()),
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'queuePosition',
    header: 'Queue',
    accessor: (r) => (r.queuePosition && r.queuePosition > 0 ? String(r.queuePosition) : '—'),
    // Absent/zero position sorts last, not first.
    sortAccessor: (r) => r.queuePosition ?? Number.MAX_SAFE_INTEGER,
    sortable: true,
    densities: ALL_DENSITIES,
  },
  {
    key: 'modelBackend',
    header: 'Model',
    accessor: (r) => formatBinding(r),
    sortable: false,
    densities: ['balanced', 'expanded'],
  },
  {
    key: 'retryCount',
    header: 'Retries',
    accessor: (r) => String(r.retryCount ?? 0),
    sortAccessor: (r) => r.retryCount ?? 0,
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