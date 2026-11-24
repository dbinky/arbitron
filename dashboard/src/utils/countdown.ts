import { formatDuration } from './time'

/**
 * Countdown to a source's next capacity reset, computed client-side from the
 * server's normalized ABSOLUTE timestamp (never from elapsed time alone). A
 * passed reset does not mean the source is back: the server confirms recovery
 * with a probe, so the label clamps at "awaiting confirmation" and this
 * helper never reads or changes source health.
 */
export function formatCountdown(resetAt: string | null | undefined, now: number): string {
  if (!resetAt || !Number.isFinite(now)) return 'reset time unknown'
  const at = Date.parse(resetAt)
  if (!Number.isFinite(at)) return 'reset time unknown'
  const left = at - now
  if (left <= 0) return 'reset due · awaiting confirmation'
  return `resets in ${formatDuration(left)}`
}
