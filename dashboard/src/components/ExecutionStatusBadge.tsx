import type { ExecutionStatus } from '../api/types'
import { executionBadgeLabel } from './executionStatus'
import styles from './ExecutionStatus.module.css'

/** Execution-status badge beside the run state (capacity/slot/move/recovery are not InstanceState values). */
export default function ExecutionStatusBadge({ status, now, showNode }: { status: ExecutionStatus; now: number; showNode: boolean }) {
  const label = executionBadgeLabel(status, now)
  if (label === null) return null
  return (
    <span className={`${styles.badge} ${styles[status.state] ?? ''}`} data-exec-badge={status.state} title={status.reason ?? label}>
      {showNode ? `${status.nodeId}: ${label}` : label}
    </span>
  )
}
