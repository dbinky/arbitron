import { useEffect, useState } from 'react'
import type { EnrichedInstanceSummary } from '../api/types'
import { api } from '../api/client'
import { localDateTime } from '../utils/time'
import styles from './RowDetailPanel.module.css'

interface RowDetailPanelProps {
  row: EnrichedInstanceSummary
  authMode: 'local' | 'org' | 'edge'
}

/**
 * RowDetailPanel renders secondary fields and lazy-loads the last error via
 * the trace endpoint only on expand. A fetch failure shows an inline error
 * state and never affects the table. A row with no failedNodeId shows no
 * error section.
 */
export default function RowDetailPanel({ row: rowVal, authMode }: RowDetailPanelProps) {
  const [errorText, setErrorText] = useState<string | null>(null)
  const [errorLoading, setErrorLoading] = useState(false)

  useEffect(() => {
    if (!rowVal.failedNodeId) return
    setErrorLoading(true)
    api.instances
      .getNodeTrace(rowVal.id, rowVal.failedNodeId)
      .then((exec) => {
        const msg = (exec.error as { error?: unknown } | undefined)?.error
        setErrorText(typeof msg === 'string' ? msg : msg ? JSON.stringify(msg) : null)
      })
      .catch(() => setErrorText(null))
      .finally(() => setErrorLoading(false))
  }, [rowVal.id, rowVal.failedNodeId])

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