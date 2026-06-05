import type { ThroughputBucket } from '../api/types'

/** Renders a 0..1 rate as a one-decimal percentage string, e.g. 0.8 → "80.0%". */
export function formatPercent(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`
}

/** Renders a duration in seconds as "12.3s" or "1m 35s". Mirrors UsageStats. */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}m ${secs.toFixed(0)}s`
}

/** Total runs across all throughput buckets. */
export function throughputTotal(buckets: ThroughputBucket[]): number {
  return buckets.reduce((sum, b) => sum + b.runs, 0)
}
