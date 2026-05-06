/**
 * Returns a human-readable relative time string (e.g. "3m ago", "2h ago").
 * For dates in the future, returns "just now".
 */
export function relativeTime(dateStr: string): string {
  const now = Date.now()
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
 * (e.g. "1m 23s", "2h 5m"). Useful for showing how long an instance
 * has been running.
 */
export function elapsed(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = Math.max(0, now - then)

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
