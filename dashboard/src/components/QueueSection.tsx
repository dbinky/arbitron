import { useState, useMemo } from 'react'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import { visibleColumns, type ColumnDef } from './queueColumns'
import QueueTableRow from './QueueTableRow'
import RowDetailPanel from './RowDetailPanel'
import styles from './QueueSection.module.css'

interface QueueSectionProps {
  title: string
  instances: EnrichedInstanceSummary[]
  titleColor: string
  countBg: string
  countColor: string
  subtitle?: string
  timeMode: 'elapsed' | 'relative'
  density: QueueDensity
  authMode: 'local' | 'org' | 'edge'
  defaultCollapsed?: boolean
  onDismissAll?: () => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
}

const priorityOrder: Record<string, number> = { high: 0, normal: 1, low: 2 }

/** defaultSortKey picks the initial sort column from the section's state mix. */
function defaultSortKey(instances: EnrichedInstanceSummary[]): string {
  const states = new Set(instances.map((i) => i.state))
  const isQueued = states.has('queued') && instances.every((i) => i.state === 'queued')
  const isActive = instances.some((i) => ['running', 'waiting', 'paused'].includes(i.state))
  if (isQueued) return 'queuePosition'
  if (isActive) return 'elapsed'
  return 'updatedAt' // terminal: most-recent first
}

function compareRows(a: EnrichedInstanceSummary, b: EnrichedInstanceSummary, col: ColumnDef): number {
  const va = col.accessor(a)
  const vb = col.accessor(b)
  if (col.key === 'elapsed' || col.key === 'updatedAt' || col.key === 'submittedAt') {
    return new Date(va).getTime() - new Date(vb).getTime()
  }
  if (col.key === 'queuePosition') {
    const pa = a.queuePosition ?? Number.MAX_SAFE_INTEGER
    const pb = b.queuePosition ?? Number.MAX_SAFE_INTEGER
    return pa - pb
  }
  if (col.key === 'retryCount') {
    return (a.retryCount ?? 0) - (b.retryCount ?? 0)
  }
  if (col.key === 'priority') {
    return (priorityOrder[a.priority] ?? 1) - (priorityOrder[b.priority] ?? 1)
  }
  return va.localeCompare(vb)
}

export default function QueueSection({
  title,
  instances,
  titleColor,
  countBg,
  countColor,
  subtitle,
  timeMode,
  density,
  authMode,
  defaultCollapsed = false,
  onDismissAll,
  onDismiss,
  onRestore,
}: QueueSectionProps) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc')
  const [expandedId, setExpandedId] = useState<string | null>(null)

  // In local mode every row renders "you" for the submitter, so sorting by the
  // real submitterName would appear to do nothing — mark the column non-sortable
  // there. In org/edge mode the real name is shown, so it stays sortable.
  const columns = useMemo(() => {
    const cols = visibleColumns(density)
    if (authMode !== 'local') return cols
    return cols.map((c) => (c.key === 'submitter' ? { ...c, sortable: false } : c))
  }, [density, authMode])

  const sorted = useMemo(() => {
    if (sortKey === null) {
      const key = defaultSortKey(instances)
      const sortedCopy = [...instances]
      if (key === 'updatedAt') {
        sortedCopy.sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
        return sortedCopy
      }
      if (key === 'queuePosition') {
        sortedCopy.sort(
          (a, b) =>
            (a.queuePosition ?? Number.MAX_SAFE_INTEGER) - (b.queuePosition ?? Number.MAX_SAFE_INTEGER),
        )
        return sortedCopy
      }
      sortedCopy.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime())
      return sortedCopy
    }
    const col = columns.find((c) => c.key === sortKey)
    if (!col) return instances
    const sortedCopy = [...instances]
    sortedCopy.sort((a, b) => {
      const cmp = compareRows(a, b, col)
      return sortDir === 'asc' ? cmp : -cmp
    })
    return sortedCopy
  }, [instances, sortKey, sortDir, columns])

  function handleHeaderClick(col: ColumnDef) {
    if (!col.sortable) return
    if (sortKey === col.key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(col.key)
      setSortDir('asc')
    }
  }

  return (
    <div className={styles.section}>
      <div
        className={styles.header}
        onClick={() => setCollapsed((c) => !c)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setCollapsed((c) => !c)
          }
        }}
      >
        <span className={`${styles.arrow} ${collapsed ? '' : styles.arrowExpanded}`}>
          {'▶'}
        </span>
        <span className={styles.title} style={{ color: titleColor }}>{title}</span>
        <span className={styles.count} style={{ background: countBg, color: countColor }}>
          {instances.length}
        </span>
        {subtitle && <span className={styles.subtitle}>{subtitle}</span>}
        {onDismissAll && (
          <button
            type="button"
            className={styles.dismissAll}
            onClick={(e) => { e.stopPropagation(); onDismissAll() }}
          >
            Dismiss All
          </button>
        )}
      </div>

      {!collapsed && (
        <div className={styles.body}>
          {sorted.length === 0 ? (
            <div className={styles.empty}>No instances</div>
          ) : (
            <table className={styles.table}>
              <thead>
                <tr>
                  {columns.map((col) => (
                    <th
                      key={col.key}
                      className={`${styles.th} ${col.sortable ? styles.sortable : ''} ${sortKey === col.key ? styles.sortActive : ''}`}
                      onClick={() => handleHeaderClick(col)}
                      role="columnheader"
                      aria-sort={sortKey === col.key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
                    >
                      {col.header}
                      {sortKey === col.key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}
                    </th>
                  ))}
                  <th className={styles.th} aria-label="expand" />
                </tr>
              </thead>
              {sorted.map((inst) => (
                <tbody key={inst.id}>
                  <QueueTableRow
                    row={inst}
                    columns={columns}
                    timeMode={timeMode}
                    authMode={authMode}
                    dimmed={inst.dismissed === true}
                    expanded={expandedId === inst.id}
                    onExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                    onDismiss={onDismiss}
                    onRestore={onRestore}
                  />
                  {expandedId === inst.id && (
                    <tr className={styles.detailRow}>
                      <td colSpan={columns.length + 1} className={styles.detailCell}>
                        <RowDetailPanel row={inst} authMode={authMode} />
                      </td>
                    </tr>
                  )}
                </tbody>
              ))}
            </table>
          )}
        </div>
      )}
    </div>
  )
}