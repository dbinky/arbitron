import { Fragment, type ReactNode } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import type { HierarchyMarker } from '../graph/liveSummary'
import type { ColumnDef } from './queueColumns'
import type { RunHierarchy } from '../hooks/useRunHierarchy'
import QueueTableRow from './QueueTableRow'
import styles from './HierarchyRows.module.css'

interface HierarchyRowsProps {
  rows: EnrichedInstanceSummary[]
  columns: ColumnDef[]
  depth: number
  timeMode: 'elapsed' | 'relative'
  /** Page clock (ms) from useNow, passed down to rows. */
  now?: number
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
  /** Markers of THIS level's rows (keyed by row id), built by the parent level
   *  from its authorized aggregate. Absent at the top level. */
  markers?: Record<string, HierarchyMarker>
  /** Terminal rows listed only because they still own active work (queue
   *  ancestor discovery); passed down so an intermediate ancestor is marked too. */
  ownsActiveWork?: ReadonlySet<string>
}

const KIND_TEXT: Partial<Record<HierarchyMarker['kind'], string>> = { branch: 'branch', workflow: 'workflow task' }

/**
 * The row's hierarchy markers. Task keys and labels are author-controlled
 * strings and render only as escaped JSX text.
 */
function RowMarkers({ marker, ownsActive }: { marker: HierarchyMarker | undefined; ownsActive: boolean }) {
  if (!marker && !ownsActive) return null
  const kindText = marker ? KIND_TEXT[marker.kind] : undefined
  return (
    <span className={styles.markers} data-testid="hierarchy-markers">
      {ownsActive && <span className={`${styles.marker} ${styles.markerOwner}`}>owns active work</span>}
      {marker && kindText && (
        <span className={`${styles.marker} ${marker.kind === 'branch' ? styles.markerBranch : styles.markerWorkflow}`}>
          {kindText}
        </span>
      )}
      {marker?.taskKey && (
        <span className={styles.marker}>{`${marker.groupLabel ?? marker.groupNodeId ?? 'group'} · ${marker.taskKey}`}</span>
      )}
      {marker?.independent && <span className={`${styles.marker} ${styles.markerIndependent}`}>non-blocking</span>}
      {marker?.activeSummary && (
        <span className={styles.markerActive} data-testid="hierarchy-active-summary">
          {marker.activeSummary}
        </span>
      )}
    </span>
  )
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
 * special-casing. A child row additionally shows what it is to its parent
 * (branch / workflow task, group · task key, non-blocking) and what it is doing
 * now (active-state summary), from the parent's authorized aggregate.
 */
export default function HierarchyRows({
  rows,
  columns,
  depth,
  timeMode,
  now,
  authMode,
  hierarchy,
  detailExpandedId,
  onDetailExpand,
  onDismiss,
  onRestore,
  renderDetail,
  detailRowClassName,
  detailCellClassName,
  markers,
  ownsActiveWork,
}: HierarchyRowsProps) {
  return (
    <>
      {rows.map((inst) => {
        const total = inst.childCount ?? 0
        const hasChildren = total > 0
        const expanded = hierarchy.isExpanded(inst.id)
        const error = hierarchy.errorOf(inst.id)
        const loading = hierarchy.isLoading(inst.id)
        const loadingMore = hierarchy.isLoadingMore(inst.id)
        const more = hierarchy.hasMore(inst.id)
        const children = hierarchy.childrenOf(inst.id)
        const shown = children.length
        const marker = markers?.[inst.id]

        return (
          // A plain Fragment, not a <tbody> — this block recurses into its own
          // children's rows, and nesting a <tbody> inside another <tbody> is
          // invalid HTML that real browsers do not auto-correct when the DOM
          // is built via the JS API. The single owning <tbody> lives once, at
          // the call site in QueueSection.
          <Fragment key={inst.id}>
            <QueueTableRow
              row={inst}
              columns={columns}
              timeMode={timeMode}
              now={now}
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
              badges={<RowMarkers marker={marker} ownsActive={ownsActiveWork?.has(inst.id) === true} />}
              independent={marker?.independent === true}
            />

            {detailExpandedId === inst.id && renderDetail && (
              <tr className={detailRowClassName}>
                <td colSpan={columns.length + 1} className={detailCellClassName}>
                  {renderDetail(inst)}
                </td>
              </tr>
            )}

            {/* First load only: a refresh keeps the cached rows on screen. */}
            {expanded && loading && shown === 0 && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelStatus}>
                  Loading child runs…
                </td>
              </tr>
            )}

            {/* A failure is confined to THIS row: siblings and the rest of the
                page are unaffected, and the last good rows stay listed. */}
            {expanded && error && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelError}>
                  {error}
                </td>
              </tr>
            )}

            {expanded && shown > 0 && (
              <HierarchyRows
                rows={children}
                columns={columns}
                depth={depth + 1}
                timeMode={timeMode}
                now={now}
                authMode={authMode}
                hierarchy={hierarchy}
                detailExpandedId={detailExpandedId}
                onDetailExpand={onDetailExpand}
                onDismiss={onDismiss}
                onRestore={onRestore}
                renderDetail={renderDetail}
                detailRowClassName={detailRowClassName}
                detailCellClassName={detailCellClassName}
                markers={hierarchy.markersOf(inst.id)}
                ownsActiveWork={ownsActiveWork}
              />
            )}

            {/* TRUNCATION IS STATED, NOT CLAMPED. The row's childCount comes
                from the server and counts every child the caller may see; the
                expansion is paged. When they disagree, say so and say by how
                much — clamping the badge would make them agree by LYING. */}
            {expanded && !loading && !error && shown < total && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.levelTruncated}>
                  Showing {shown} of {total} child runs
                </td>
              </tr>
            )}

            {/* Every fixed task must be inspectable: after a full page, offer
                the next one (also after a failed page, to retry it). */}
            {expanded && more && (
              <tr>
                <td colSpan={columns.length + 1} className={styles.loadMore}>
                  <button
                    type="button"
                    className={styles.loadMoreButton}
                    disabled={loading || loadingMore}
                    onClick={() => void hierarchy.loadMore(inst.id)}
                  >
                    {loadingMore ? 'Loading more child runs…' : 'Load more child runs'}
                  </button>
                </td>
              </tr>
            )}
          </Fragment>
        )
      })}
    </>
  )
}
