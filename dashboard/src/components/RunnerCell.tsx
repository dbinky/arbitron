import type { ExecutionStatus } from '../api/types'
import { runnerSummary } from './executionStatus'
import styles from './ExecutionStatus.module.css'

/**
 * The active operation's runner first (one line per simultaneous operation),
 * then the workspace home and the interactive pin as separate labels.
 */
export default function RunnerCell({ execution }: { execution: ExecutionStatus[] | undefined }) {
  const { active, home, pin } = runnerSummary(execution)
  if (active.length === 0 && !home && !pin) return <>—</>
  const many = active.length > 1
  return (
    <span className={styles.runnerCell}>
      {active.map((a) => (
        <span key={a.jobId} data-exec-runner={a.runnerId ?? ''} data-node={a.nodeId} title={`${a.nodeId}: ${a.state}`}>
          {a.runnerId ?? 'unassigned'}
          {many ? ` (${a.nodeId})` : ''}
        </span>
      ))}
      {home && (
        <span className={styles.secondary} data-home-runner={home}>
          home {home}
        </span>
      )}
      {pin && (
        <span className={styles.secondary} data-pin-runner={pin}>
          pinned {pin}
        </span>
      )}
    </span>
  )
}
