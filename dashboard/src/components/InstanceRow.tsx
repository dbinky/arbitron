import { useNavigate } from 'react-router-dom'
import type { InstanceSummary } from '../api/types'
import { relativeTime, elapsed } from '../utils/time'
import StateBadge from './StateBadge'
import styles from './InstanceRow.module.css'

interface InstanceRowProps {
  instance: InstanceSummary
  timeMode: 'elapsed' | 'relative'
}

const borderClass: Record<string, string> = {
  running: styles.borderRunning,
  completed: styles.borderCompleted,
  failed: styles.borderFailed,
  waiting: styles.borderWaiting,
  queued: styles.borderQueued,
  paused: styles.borderPaused,
  cancelled: styles.borderCancelled,
}

const priorityClass: Record<string, string> = {
  high: styles.priorityHigh,
  normal: styles.priorityNormal,
  low: styles.priorityLow,
}

export default function InstanceRow({ instance, timeMode }: InstanceRowProps) {
  const navigate = useNavigate()

  const timeStr =
    timeMode === 'elapsed'
      ? elapsed(instance.createdAt)
      : relativeTime(instance.updatedAt)

  return (
    <div
      className={`${styles.row} ${borderClass[instance.state] ?? ''}`}
      onClick={() => navigate(`/instances/${instance.id}`)}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          navigate(`/instances/${instance.id}`)
        }
      }}
    >
      <span className={styles.workflowName}>{instance.workflowName}</span>
      <StateBadge state={instance.state} />
      <span className={styles.currentNode}>
        {instance.currentNodeId ?? '—'}
      </span>
      <span className={`${styles.priority} ${priorityClass[instance.priority] ?? ''}`}>
        {instance.priority}
      </span>
      <span className={styles.time}>{timeStr}</span>
      <span className={styles.owner}>
        {instance.state === 'failed' ? (
          <span className={styles.attention}>
            {'⚠'} Needs attention
          </span>
        ) : (
          'local'
        )}
      </span>
    </div>
  )
}
