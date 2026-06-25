import { describe, it, expect } from 'vitest'
import { localDateTime } from './time'

describe('localDateTime', () => {
  it('formats an ISO timestamp as YYYY-MM-DD HH:mm', () => {
    expect(localDateTime('2024-06-25T12:00:00Z')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
  })
  it('returns the input unchanged for an unparseable value', () => {
    expect(localDateTime('not-a-date')).toBe('not-a-date')
  })
})