import { Fragment, type ReactNode } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import type { ColumnDef } from './queueColumns'
import { CHILD_PAGE_LIMIT, type RunHierarchy } from '../hooks/useRunHierarchy'
import QueueTableRow from './QueueTableRow'
import styles from './HierarchyRows.module.css'

interface HierarchyRowsProps {
  rows: EnrichedInstanceSummary[]
  columns: ColumnDef[]
  depth: number
  timeMode: 'elapsed' | 'relative'
  authMode: 'local' | 'org' | 'edge'
  hierarchy: RunHierarchy
  detailExpandedId?: string | null
  onDetailExpand?: (id: string) => void
  onDismiss?: (id: string) => void
  onRestore?: (id: string) => void
  renderDetail?: (row: EnrichedInstanceSummary) => ReactNode
  /** QueueSection's detail-row class names, passed through so the existing
   *  framing survives the move out of QueueSection. */
  detailRowClassName?: string
  detailCellClassName?: string
}

/**
 * HierarchyRows renders one level of the run tree and recurses into any
 * expanded row's children.
 *
 * Recursion lives here (rather than in QueueSection) so the section stays a
 * table-shell component. Depth is passed down purely for indentation; nothing
 * in the fetch or render path is depth-aware, so the tree opens to arbitrary
 * depth.
 *
 * Child rows are the SAME QueueTableRow as roots, so the per-row action set —
 * which is what lets operators inspect, retry, resume, or cancel at the CHILD
 * level rather than restarting a larger process — needs no per-level
 * special-casing.
 */
export default function HierarchyRows({
  rows,
  columns,
  depth,
  timeMode,
  authMode,
  hierarchy,
  detailExpandedId,
  onDetailExpand,
  onDismiss,
  onRestore,
  renderDetail,
  detailRowClassName,
  detailCellClassName,
}: HierarchyRowsProps) {
  return (
    <>
      {rows.map((inst) => {
        const total = inst.childCount ?? 0
        const hasChildren = total > 0
        const expanded = hierarchy.isExpanded(inst.id)
        const error = hierarchy.errorOf(inst.id)
        const loading = hierarchy.isLoading(inst.id)
        const shown = hierarchy.childrenOf(inst.id).length

        return (
          // A plain Fragment, not a <tbody> — this block recurses into its own
          // children's rows, and nesting a <tbody> inside another <tbody> is
          // invalid HTML that real browsers do not auto-correct when the DOM
          // is built via the JS API (only HTML-text parsing gets that fixup).
          // The single owning <tbody> lives once, at the call site in
          // QueueSection.
          <Fragment key={inst.id}>
            <QueueTableRow
              row={inst}
              columns={columns}
              timeMode={timeMode}
              authMode={authMode}
              dimmed={inst.dismissed === true}
              expanded={detailExpandedId === inst.id}
              onExpand={onDetailExpand}
              onDismiss={onDismiss}
              onRestore={onRestore}
              depth={depth}
              hierarchyExpanded={expanded}
              // A row reporting no visible children renders NO expander, so a
              // parent whose children are all invisible to this caller leaks no
              // existence.
              onToggleHierarchy={hasChildren ? () => void hierarchy.toggle(inst.id) : undefined}
            />

            {detailExpandedId === inst.id && renderDetail && (
              <tr className={detailRowClassName}>
                <td colSpan={columns.length + 1} className={detailCellClassName}>
                  {renderDetail(inst)}
                </td>
              </tr>
            )}

            {expanded && loading && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelStatus}>
                  Loading child runs…
                </td>
              </tr>
            )}

            {/* An expansion failure is confined to THIS row: siblings and the
                rest of the page are unaffected. */}
            {expanded && error && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelError}>
                  {error}
                </td>
              </tr>
            )}

            {/* TRUNCATION IS STATED, NOT CLAMPED. The row's childCount comes
                from the server and counts every child the caller may see; the
                expansion is ONE page. When they disagree, say so and say by how
                much — clamping the badge down to the page size would make the
                two agree by LYING about how many children exist, and the user
                would never learn that more are there. */}
            {expanded && !loading && !error && shown < total && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelTruncated}>
                  Showing {shown} of {total} child runs
                  {shown >= CHILD_PAGE_LIMIT ? ' (first page)' : ''}
                </td>
              </tr>
            )}

            {expanded && !loading && !error && (
              <HierarchyRows
                rows={hierarchy.childrenOf(inst.id)}
                columns={columns}
                depth={depth + 1}
                timeMode={timeMode}
                authMode={authMode}
                hierarchy={hierarchy}
                detailExpandedId={detailExpandedId}
                onDetailExpand={onDetailExpand}
                onDismiss={onDismiss}
                onRestore={onRestore}
                renderDetail={renderDetail}
                detailRowClassName={detailRowClassName}
                detailCellClassName={detailCellClassName}
              />
            )}
          </Fragment>
        )
      })}
    </>
  )
}
