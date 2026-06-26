import type { EnrichedInstanceSummary } from '../api/types'
import { localDateTime } from '../utils/time'
import styles from './RowDetailPanel.module.css'

interface RowDetailPanelProps {
  row: EnrichedInstanceSummary
  authMode: 'local' | 'org' | 'edge'
  /** Resolved last-error text (loadError), provided by the parent's trace fetch. */
  errorText?: string | null
  /** Whether the parent's trace fetch is in flight (loadError loading). */
  errorLoading?: boolean
}

/**
 * RowDetailPanel is a pure render component for a row's secondary fields and its
 * last-error section. It owns no data fetching: the parent (QueueSection) lazy-
 * loads the trace on expand and passes errorText/errorLoading in. A row with no
 * failedNodeId shows no error section.
 */
export default function RowDetailPanel({
  row: rowVal,
  authMode,
  errorText = null,
  errorLoading = false,
}: RowDetailPanelProps) {
  return (
    <div className={styles.panel} data-testid="row-detail-panel">
      <div className={styles.section}>
        <div className={styles.field}>
          <span className={styles.label}>Submitted</span>
          <span className={styles.value}>{localDateTime(rowVal.createdAt)}</span>
        </div>
        {authMode !== 'local' && rowVal.submitterIdentity && (
          <div className={styles.field}>
            <span className={styles.label}>Account</span>
            <span className={styles.value}>{rowVal.submitterIdentity}</span>
          </div>
        )}
        {(rowVal.model || rowVal.backend) && (
          <div className={styles.field}>
            <span className={styles.label}>Model / Backend</span>
            <span className={styles.value}>
              {rowVal.model && <span>{rowVal.model}</span>}
              {rowVal.model && rowVal.backend && ' · '}
              {rowVal.backend && <span>{rowVal.backend}</span>}
            </span>
          </div>
        )}
        <div className={styles.field}>
          <span className={styles.label}>Retries</span>
          <span className={styles.value}>{rowVal.retryCount ?? 0}</span>
        </div>
      </div>
      {rowVal.failedNodeId && (
        <div className={styles.section}>
          <span className={styles.label}>Last error</span>
          {errorLoading ? (
            <span className={styles.value}>Loading…</span>
          ) : errorText ? (
            <pre className={styles.errorText}>{errorText}</pre>
          ) : (
            <span className={styles.value}>couldn't load details</span>
          )}
        </div>
      )}
    </div>
  )
}
