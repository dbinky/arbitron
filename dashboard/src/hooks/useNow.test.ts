import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNow } from './useNow'

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('useNow', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-10T00:00:00Z'))
    setVisibility('visible')
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('ticks every second by default', () => {
    const { result } = renderHook(() => useNow())
    const start = result.current
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(start + 1000)
    act(() => {
      vi.advanceTimersByTime(3000)
    })
    expect(result.current).toBe(start + 4000)
  })

  it('does not tick while the tab is hidden', () => {
    const { result } = renderHook(() => useNow())
    act(() => setVisibility('hidden'))
    const frozen = result.current
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(frozen)
  })

  it('resyncs immediately when the tab becomes visible', () => {
    const { result } = renderHook(() => useNow())
    act(() => setVisibility('hidden'))
    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    act(() => setVisibility('visible'))
    expect(result.current).toBe(Date.now())
  })

  it('clears its interval on unmount', () => {
    const { unmount } = renderHook(() => useNow())
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
