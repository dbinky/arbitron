import { useState, useMemo, useEffect } from 'react'
import type { EnrichedInstanceSummary, QueueDensity } from '../api/types'
import { api } from '../api/client'
import { visibleColumns, type ColumnDef } from './queueColumns'
import HierarchyRows from './HierarchyRows'
import { topLevelRows } from './topLevelRows'
import { useRunHierarchy, type RunHierarchy } from '../hooks/useRunHierarchy'
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
  /** Page clock (ms) from useNow, passed down to rows. */
  now?: number
  density: QueueDensity
  authMode: 'local' | 'org' | 'edge'
  defaultCollapsed?: boolean
  onDismissAll?: () => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  /** Bumped by the caller on every top-level refetch; invalidates the run
   *  hierarchy's cached child levels so an already-open row doesn't keep
   *  showing a stale child state (or miss a newly spawned one). */
  refreshSignal?: number
  /** Registers this section's hierarchy row patcher with the caller, which
   *  feeds it live node events so expanded children update without a refetch.
   *  Returns the unregister function. */
  registerRowPatcher?: (patch: RunHierarchy['patchRow']) => () => void
}

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
  // A column's sortAccessor (when declared) yields the comparable value — a
  // number for date/numeric columns, otherwise the display string accessor.
  const get = col.sortAccessor ?? col.accessor
  const va = get(a)
  const vb = get(b)
  if (typeof va === 'number' && typeof vb === 'number') {
    return va - vb
  }
  return String(va).localeCompare(String(vb))
}

export default function QueueSection({
  title,
  instances,
  titleColor,
  countBg,
  countColor,
  subtitle,
  timeMode,
  now,
  density,
  authMode,
  defaultCollapsed = false,
  onDismissAll,
  onDismiss,
  onRestore,
  refreshSignal,
  registerRowPatcher,
}: QueueSectionProps) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc')
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)
  const [errorLoading, setErrorLoading] = useState(false)
  const hierarchy = useRunHierarchy()

  // A top-level refetch replaces `instances` wholesale but leaves any
  // already-expanded hierarchy level's cache untouched, so it would otherwise
  // keep showing a child's pre-refetch state (or hide one that just
  // appeared) until the user collapses and re-expands it by hand.
  const { invalidate } = hierarchy
  useEffect(() => {
    invalidate()
  }, [refreshSignal, invalidate])

  const { patchRow } = hierarchy
  useEffect(() => registerRowPatcher?.(patchRow), [registerRowPatcher, patchRow])

  // Lazy-load the last error for the expanded row via the trace endpoint. The
  // fetch lives here (not in RowDetailPanel) so the detail panel stays a pure
  // render component. A failure shows an inline error state and never affects
  // the table.
  const expandedRow = expandedId ? instances.find((i) => i.id === expandedId) : undefined
  const expandedFailedNodeId = expandedRow?.failedNodeId
  useEffect(() => {
    if (!expandedId || !expandedFailedNodeId) {
      setErrorText(null)
      setErrorLoading(false)
      return
    }
    let cancelled = false
    setErrorText(null)
    setErrorLoading(true)
    api.instances
      .getNodeTrace(expandedId, expandedFailedNodeId)
      .then((exec) => {
        if (cancelled) return
        const msg = (exec.error as { error?: unknown } | undefined)?.error
        setErrorText(typeof msg === 'string' ? msg : msg ? JSON.stringify(msg) : null)
      })
      .catch(() => {
        if (!cancelled) setErrorText(null)
      })
      .finally(() => {
        if (!cancelled) setErrorLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [expandedId, expandedFailedNodeId])

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

  // Rows whose parent is ALSO in this section render nested under it instead of
  // at the top level; everything else stays visible where it is.
  const roots = useMemo(() => topLevelRows(sorted), [sorted])

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
              <tbody>
                <HierarchyRows
                  rows={roots}
                  columns={columns}
                  depth={0}
                  timeMode={timeMode}
                  now={now}
                  authMode={authMode}
                  hierarchy={hierarchy}
                  detailExpandedId={expandedId}
                  onDetailExpand={(id) => setExpandedId((prev) => (prev === id ? null : id))}
                  onDismiss={onDismiss}
                  onRestore={onRestore}
                  detailRowClassName={styles.detailRow}
                  detailCellClassName={styles.detailCell}
                  renderDetail={(inst) => (
                    <RowDetailPanel
                      row={inst}
                      authMode={authMode}
                      errorText={errorText}
                      errorLoading={errorLoading}
                    />
                  )}
                />
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}