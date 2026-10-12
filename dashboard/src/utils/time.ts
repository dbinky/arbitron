/**
 * Returns a human-readable relative time string (e.g. "3m ago", "2h ago").
 * For dates in the future, returns "just now".
 */
export function relativeTime(dateStr: string, now: number = Date.now()): string {
  const then = new Date(dateStr).getTime()
  const diffMs = now - then

  if (diffMs < 0) return 'just now'

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s ago`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`

  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

/**
 * Returns the elapsed duration since the given date as a compact string
 * (e.g. "1m 23s", "2h 5m"). `now` is injectable so a page-level clock
 * (useNow) drives re-rendering and tests stay deterministic.
 */
export function elapsed(dateStr: string, now: number = Date.now()): string {
  return formatDuration(now - new Date(dateStr).getTime())
}

/**
 * Formats a millisecond duration compactly ("45s", "1m 23s", "2h 5m",
 * "1d 3h"). Negative or non-finite input formats as "0s".
 */
export function formatDuration(ms: number): string {
  const diffMs = Number.isFinite(ms) ? Math.max(0, ms) : 0

  const totalSeconds = Math.floor(diffMs / 1000)

  if (totalSeconds < 60) return `${totalSeconds}s`

  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60

  if (minutes < 60) return `${minutes}m ${seconds}s`

  const hours = Math.floor(minutes / 60)
  const remainMinutes = minutes % 60

  if (hours < 24) return `${hours}h ${remainMinutes}m`

  const days = Math.floor(hours / 24)
  const remainHours = hours % 24
  return `${days}d ${remainHours}h`
}

const TERMINAL_STATES = new Set(['completed', 'failed', 'cancelled'])

/**
 * How long a run has taken, in ms: created → end for a terminal run (frozen),
 * created → now otherwise. A terminal run ends at completedAt, else at
 * updatedAt — cancellation writes only state/updated_at, never completed_at, so
 * updatedAt is the cancelled row's terminal timestamp. A terminal row with
 * neither parseable falls back to now. Never negative; unparseable createdAt → 0.
 */
export function runDuration(
  row: { state: string; createdAt: string; completedAt?: string; updatedAt?: string },
  now: number,
): number {
  const start = new Date(row.createdAt).getTime()
  if (!Number.isFinite(start)) return 0
  let end = now
  if (TERMINAL_STATES.has(row.state)) {
    for (const ts of [row.completedAt, row.updatedAt]) {
      const done = ts ? new Date(ts).getTime() : NaN
      if (Number.isFinite(done)) {
        end = done
        break
      }
    }
  }
  return Math.max(0, end - start)
}

/**
 * Formats an ISO timestamp as a compact, viewer-local "YYYY-MM-DD HH:mm"
 * string (local tz via Date's local getters). Returns the input unchanged if
 * it is not a parseable date. Used for the "submitted local time" column and
 * the row-detail "Submitted" field.
 */
export function localDateTime(dateStr: string): string {
  const d = new Date(dateStr)
  if (Number.isNaN(d.getTime())) return dateStr
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
