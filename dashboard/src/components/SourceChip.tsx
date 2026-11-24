import type { RunnerSource } from '../api/types'
import { formatCountdown } from '../utils/countdown'
import { credentialLabel, sourceStatus, STATUS_TEXT } from './poolModel'
import styles from './SourceChip.module.css'

/** One capacity source: kind, family, server-reported status and reset countdown. */
export default function SourceChip({ source, now }: { source: RunnerSource; now: number }) {
  const status = sourceStatus(source)
  const ref = source.credentialRef && source.kind !== 'anthropic_subscription' && source.kind !== 'openai_subscription' ? ` (${source.credentialRef})` : ''
  const families = source.families.length > 0 ? source.families.join('/') : 'no model family'
  const countdown = source.health.capacity === 'exhausted' ? ` · ${formatCountdown(source.health.resetAt, now)}` : ''
  return (
    <span className={`${styles.chip} ${styles[status] ?? ''}`} data-source={source.id} data-status={status} title={source.id}>
      {credentialLabel(source.kind)}
      {ref} · {families} · {STATUS_TEXT[status]}
      {source.unavailableReason && ` · ${source.unavailableReason}`}
      {countdown}
    </span>
  )
}
