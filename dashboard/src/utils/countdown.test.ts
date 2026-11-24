import { describe, expect, it } from 'vitest'
import { formatCountdown } from './countdown'

const NOW = Date.parse('2026-11-20T10:00:00Z')

describe('formatCountdown', () => {
  it('counts down to the server-normalized absolute reset time', () => {
    expect(formatCountdown('2026-11-20T10:01:05Z', NOW)).toBe('resets in 1m 5s')
    expect(formatCountdown('2026-11-20T12:00:00Z', NOW)).toBe('resets in 2h 0m')
    expect(formatCountdown('2026-11-20T10:00:09Z', NOW)).toBe('resets in 9s')
  })

  it('never changes capacity when reset countdown expires: it clamps and awaits confirmation', () => {
    expect(formatCountdown('2026-11-20T09:59:59Z', NOW)).toBe('reset due · awaiting confirmation')
    expect(formatCountdown('2026-11-20T10:00:00Z', NOW)).toBe('reset due · awaiting confirmation')
  })

  it('keeps unknown, empty and malformed timestamps unknown', () => {
    expect(formatCountdown(null, NOW)).toBe('reset time unknown')
    expect(formatCountdown(undefined, NOW)).toBe('reset time unknown')
    expect(formatCountdown('', NOW)).toBe('reset time unknown')
    expect(formatCountdown('soon', NOW)).toBe('reset time unknown')
    expect(formatCountdown('2026-11-20T10:01:05Z', Number.NaN)).toBe('reset time unknown')
  })
})
