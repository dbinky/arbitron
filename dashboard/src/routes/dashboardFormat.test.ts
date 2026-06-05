import { describe, it, expect } from 'vitest'
import { formatPercent, formatDuration, throughputTotal } from './dashboardFormat'
import type { ThroughputBucket } from '../api/types'

describe('formatPercent', () => {
  it('renders a 0..1 rate as a one-decimal percentage', () => {
    expect(formatPercent(0.8)).toBe('80.0%')
    expect(formatPercent(0)).toBe('0.0%')
    expect(formatPercent(0.125)).toBe('12.5%')
  })
})

describe('formatDuration', () => {
  it('renders sub-minute durations in seconds', () => {
    expect(formatDuration(12.3)).toBe('12.3s')
  })
  it('renders minute+ durations as m s', () => {
    expect(formatDuration(95)).toBe('1m 35s')
  })
  it('renders zero as 0.0s', () => {
    expect(formatDuration(0)).toBe('0.0s')
  })
})

describe('throughputTotal', () => {
  it('sums runs across buckets', () => {
    const buckets: ThroughputBucket[] = [
      { bucket: '2026-06-04T10:00:00Z', runs: 3 },
      { bucket: '2026-06-04T11:00:00Z', runs: 5 },
    ]
    expect(throughputTotal(buckets)).toBe(8)
  })
  it('returns 0 for an empty series', () => {
    expect(throughputTotal([])).toBe(0)
  })
})
