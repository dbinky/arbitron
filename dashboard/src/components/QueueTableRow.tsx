import { useNavigate } from 'react-router-dom'
import type { EnrichedInstanceSummary } from '../api/types'
import type { ColumnDef } from './queueColumns'
import { elapsed, relativeTime, localDateTime } from '../utils/time'
import styles from './QueueTableRow.module.css'

/** Horizontal indent applied per hierarchy level, in pixels. */
const INDENT_PX = 16

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
  /** Nesting depth in the run hierarchy; 0 for a root row. */
  depth?: number
  /** Whether this row's children are currently shown. */
  hierarchyExpanded?: boolean
  /** Toggles this row's children. Absent when the row reports no children. */
  onToggleHierarchy?: (id: string) => void
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
  depth = 0,
  hierarchyExpanded = false,
  onToggleHierarchy,
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
      data-depth={depth}
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
      {columns.map((col, i) => (
        <td key={col.key} className={styles.cell} data-col={col.key}>
          {i === 0 ? (
            <span className={styles.treeCell} style={{ paddingLeft: `${depth * INDENT_PX}px` }}>
              {onToggleHierarchy ? (
                <button
                  type="button"
                  className={styles.hierarchyToggle}
                  aria-label={`${hierarchyExpanded ? 'Hide' : 'Show'} child runs of ${rowVal.runName}`}
                  aria-expanded={hierarchyExpanded}
                  onClick={(e) => {
                    e.stopPropagation()
                    onToggleHierarchy(rowVal.id)
                  }}
                >
                  {hierarchyExpanded ? '▾' : '▸'}
                </button>
              ) : (
                <span className={styles.hierarchySpacer} aria-hidden="true" />
              )}
              {cellText(col)}
            </span>
          ) : (
            cellText(col)
          )}
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