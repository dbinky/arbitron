import { describe, it, expect } from 'vitest'
import { elapsed, formatDuration, localDateTime, relativeTime, runDuration } from './time'

describe('localDateTime', () => {
  it('formats an ISO timestamp as YYYY-MM-DD HH:mm', () => {
    expect(localDateTime('2024-06-25T12:00:00Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
  it('returns the input unchanged for an unparseable value', () => {
    expect(localDateTime('not-a-date')).toBe('not-a-date')
  })
})
describe('injected now', () => {
  const now = Date.parse('2026-10-10T01:02:00Z')
  it('elapsed uses the injected now', () => {
    expect(elapsed('2026-10-10T00:00:00Z', now)).toBe('1h 2m')
  })
  it('relativeTime uses the injected now', () => {
    expect(relativeTime('2026-10-10T00:57:00Z', now)).toBe('5m ago')
  })
  it('formatDuration formats milliseconds like elapsed', () => {
    expect(formatDuration(83_000)).toBe('1m 23s')
    expect(formatDuration(0)).toBe('0s')
  })
})

describe('runDuration', () => {
  const created = '2026-10-10T00:00:00Z'
  const done = '2026-10-10T00:30:00Z'
  const now = Date.parse('2026-10-10T02:00:00Z')

  it('active row counts to now', () => {
    expect(runDuration({ state: 'running', createdAt: created }, now)).toBe(2 * 3600_000)
  })
  it.each(['completed', 'failed'])('%s row freezes at completedAt', (state) => {
    expect(runDuration({ state, createdAt: created, completedAt: done, updatedAt: created }, now)).toBe(30 * 60_000)
  })
  it('cancelled row without completedAt freezes at updatedAt as now advances', () => {
    // Cancellation persists only state/updated_at — this is the real row shape.
    const cancelled = { state: 'cancelled', createdAt: created, updatedAt: done }
    expect(runDuration(cancelled, now)).toBe(30 * 60_000)
    expect(runDuration(cancelled, now + 3600_000)).toBe(30 * 60_000)
  })
  it('terminal row with no parseable end timestamp falls back to now', () => {
    expect(runDuration({ state: 'completed', createdAt: created }, now)).toBe(2 * 3600_000)
    expect(runDuration({ state: 'completed', createdAt: created, completedAt: 'garbage', updatedAt: 'x' }, now)).toBe(2 * 3600_000)
  })
  it('terminal row with unparseable completedAt uses updatedAt', () => {
    expect(runDuration({ state: 'failed', createdAt: created, completedAt: 'garbage', updatedAt: done }, now)).toBe(30 * 60_000)
  })
  it('active row ignores a stray completedAt and updatedAt', () => {
    expect(runDuration({ state: 'running', createdAt: created, completedAt: done, updatedAt: done }, now)).toBe(2 * 3600_000)
  })
  it('runDuration clamps negative to 0', () => {
    expect(runDuration({ state: 'completed', createdAt: done, completedAt: created }, now)).toBe(0)
    expect(runDuration({ state: 'running', createdAt: 'not a date' }, now)).toBe(0)
    expect(runDuration({ state: 'completed', createdAt: created, completedAt: 'garbage' }, now)).toBe(2 * 3600_000)
  })
})
