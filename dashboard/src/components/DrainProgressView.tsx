import { Link } from 'react-router-dom'
import type { DrainProgress } from '../api/types'
import { plural } from './poolModel'
import styles from './DrainProgressView.module.css'

/** Drain progress with the capacity blocking completion (P3 T12 DrainProgress). */
export default function DrainProgressView({ draining, drain }: { draining: boolean; drain: DrainProgress | null }) {
  if (!draining && !drain?.complete) return null
  if (!drain) {
    return (
      <div className={styles.drain} data-drain="unknown">
        Draining · progress unavailable
      </div>
    )
  }
  if (drain.complete) {
    return (
      <div className={styles.drain} data-drain="complete">
        Drain complete
      </div>
    )
  }
  return (
    <div className={styles.drain} data-drain="active">
      <span>
        Draining · {plural(drain.activeSteps, 'active step')} · {plural(drain.homedWorkspaces, 'homed workspace')} · {plural(drain.sessions, 'session')}
      </span>
      {drain.blocked.length > 0 && (
        <ul className={styles.blockers} aria-label="Capacity blocking the drain">
          {drain.blocked.map((b, i) => (
            <li key={`${b.kind}:${b.runId ?? 'hidden'}:${i}`} data-drain-blocker={b.kind}>
              {b.kind} blocked: {b.reason}
              {b.group ? ` (${b.group})` : ''}
              {b.runId && (
                <>
                  {' '}
                  <Link to={`/instances/${encodeURIComponent(b.runId)}`}>Open run</Link>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
