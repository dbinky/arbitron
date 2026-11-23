import type { RunnerMark } from '../runnerMarks'
import styles from './RunnerStripe.module.css'

export default function RunnerStripe({ mark }: { mark: RunnerMark | undefined }) {
  if (!mark) return null
  const label = mark.runnerId ? `Ran on ${mark.label}` : 'Runs on the server'
  return <span className={`${styles.stripe} ${mark.moved ? styles.moved : ''}`}
    data-runner={mark.runnerId ?? 'server'} data-color={mark.color} data-moved={mark.moved ? 'true' : undefined}
    style={{ background: mark.color }} role="img" aria-label={label}
    title={mark.moved ? `${label} · workspace moved ${mark.moved.from} → ${mark.moved.to}` : label} />
}
