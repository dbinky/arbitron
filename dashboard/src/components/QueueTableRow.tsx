import { useNavigate } from 'react-router-dom'
import type { EnrichedInstanceSummary } from '../api/types'
import type { ColumnDef } from './queueColumns'
import { elapsed, relativeTime, localDateTime } from '../utils/time'
import styles from './QueueTableRow.module.css'

interface QueueTableRowProps {
  row: EnrichedInstanceSummary
  columns: ColumnDef[]
  timeMode: 'elapsed' | 'relative'
  authMode: 'local' | 'org' | 'edge'
  dimmed?: boolean
  expanded?: boolean
  onExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
}

/**
 * QueueTableRow renders one enriched row's visible cells as a <tr>. The
 * submitter renders "you" in local mode and the display name in shared mode.
 * Elapsed is computed client-side (now − createdAt). Row click navigates to
 * the execution view; the expand affordance stops propagation so it does not
 * also navigate.
 */
export default function QueueTableRow({
  row: rowVal,
  columns,
  timeMode,
  authMode,
  dimmed,
  expanded,
  onExpand,
  onDismiss,
  onRestore,
}: QueueTableRowProps) {
  const navigate = useNavigate()
  const isTerminal = rowVal.state === 'failed' || rowVal.state === 'completed'

  function cellText(col: ColumnDef): string {
    if (col.key === 'submitter') {
      if (authMode === 'local') return 'you'
      return rowVal.submitterName ?? '—'
    }
    if (col.key === 'elapsed') {
      return timeMode === 'elapsed' ? elapsed(rowVal.createdAt) : relativeTime(rowVal.updatedAt)
    }
    if (col.key === 'submittedAt') {
      return localDateTime(rowVal.createdAt)
    }
    return col.accessor(rowVal)
  }

  return (
    <tr
      data-row-id={rowVal.id}
      className={`${styles.row} ${dimmed ? styles.dimmed : ''}`}
      onClick={() => navigate(`/instances/${rowVal.id}`)}
      role="row"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          navigate(`/instances/${rowVal.id}`)
        }
      }}
    >
      {columns.map((col) => (
        <td key={col.key} className={styles.cell} data-col={col.key}>
          {cellText(col)}
        </td>
      ))}
      <td className={styles.cell}>
        {isTerminal &&
          (dimmed ? (
            <button
              type="button"
              className={styles.expand}
              aria-label="Restore"
              title="Restore"
              onClick={(e) => {
                e.stopPropagation()
                onRestore?.(rowVal.id)
              }}
            >
              {'↺'}
            </button>
          ) : (
            onDismiss && (
              <button
                type="button"
                className={styles.expand}
                aria-label="Dismiss"
                title="Dismiss"
                onClick={(e) => {
                  e.stopPropagation()
                  onDismiss(rowVal.id)
                }}
              >
                {'×'}
              </button>
            )
          ))}
        <button
          type="button"
          className={styles.expand}
          aria-label={expanded ? 'Collapse' : 'Expand'}
          aria-expanded={expanded}
          onClick={(e) => {
            e.stopPropagation()
            onExpand?.(rowVal.id)
          }}
        >
          {expanded ? '▾' : '▸'}
        </button>
      </td>
    </tr>
  )
}