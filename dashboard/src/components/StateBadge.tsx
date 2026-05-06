import type { InstanceState } from '../api/types'
import styles from './StateBadge.module.css'

interface StateBadgeProps {
  state: InstanceState
}

const stateClass: Record<InstanceState, string> = {
  running: styles.running,
  completed: styles.completed,
  failed: styles.failed,
  waiting: styles.waiting,
  queued: styles.queued,
  paused: styles.paused,
  cancelled: styles.cancelled,
}

export default function StateBadge({ state }: StateBadgeProps) {
  return (
    <span className={`${styles.badge} ${stateClass[state]}`}>
      {state}
    </span>
  )
}
