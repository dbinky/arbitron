import type { TriggerSource } from '../api/types'
import styles from './OriginBadge.module.css'

interface OriginBadgeProps {
  /** Defaults to 'manual' when absent (legacy/pre-migration rows). */
  triggerSource?: TriggerSource
  /** Originating schedule id; shown as a reference for schedule/run_now runs. */
  scheduleId?: string
}

const LABELS: Record<TriggerSource, string> = {
  manual: 'manual',
  schedule: 'scheduled',
  run_now: 'run now',
}

const CLASSES: Record<TriggerSource, string> = {
  manual: styles.manual,
  schedule: styles.schedule,
  run_now: styles.runNow,
}

/**
 * Read-only annotation of how a run was started. For schedule-originated runs
 * the originating schedule id is surfaced via the title attribute. This is a
 * pure presentational badge — it has NO mutation controls (schedule management
 * stays on REST + MCP).
 */
export default function OriginBadge({ triggerSource, scheduleId }: OriginBadgeProps) {
  const source: TriggerSource = triggerSource ?? 'manual'
  const fromSchedule = source === 'schedule' || source === 'run_now'
  const title =
    fromSchedule && scheduleId ? `From schedule ${scheduleId}` : undefined
  return (
    <span className={`${styles.badge} ${CLASSES[source]}`} title={title}>
      {LABELS[source]}
    </span>
  )
}
