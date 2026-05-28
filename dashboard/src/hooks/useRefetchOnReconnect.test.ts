import { renderHook } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { useRefetchOnReconnect } from './useRefetchOnReconnect'

describe('useRefetchOnReconnect', () => {
  it('does not fire on the initial connect', () => {
    const refetch = vi.fn()
    const { rerender } = renderHook(
      ({ c }: { c: boolean }) => useRefetchOnReconnect(c, refetch),
      { initialProps: { c: false } },
    )
    rerender({ c: true }) // first time the socket connects
    expect(refetch).not.toHaveBeenCalled()
  })

  it('does not fire when starting already connected', () => {
    const refetch = vi.fn()
    renderHook(({ c }: { c: boolean }) => useRefetchOnReconnect(c, refetch), {
      initialProps: { c: true },
    })
    expect(refetch).not.toHaveBeenCalled()
  })

  it('fires once on reconnect (connected after a drop)', () => {
    const refetch = vi.fn()
    const { rerender } = renderHook(
      ({ c }: { c: boolean }) => useRefetchOnReconnect(c, refetch),
      { initialProps: { c: false } },
    )
    rerender({ c: true }) // initial connect — no fire
    rerender({ c: false }) // dropped
    rerender({ c: true }) // reconnect — fire
    expect(refetch).toHaveBeenCalledTimes(1)
  })

  it('fires again on each subsequent reconnect', () => {
    const refetch = vi.fn()
    const { rerender } = renderHook(
      ({ c }: { c: boolean }) => useRefetchOnReconnect(c, refetch),
      { initialProps: { c: true } }, // initial connect
    )
    rerender({ c: false })
    rerender({ c: true }) // reconnect 1
    rerender({ c: false })
    rerender({ c: true }) // reconnect 2
    expect(refetch).toHaveBeenCalledTimes(2)
  })

  it('does not fire merely because the refetch callback identity changes', () => {
    const refetchA = vi.fn()
    const refetchB = vi.fn()
    const { rerender } = renderHook(
      ({ c, fn }: { c: boolean; fn: () => void }) => useRefetchOnReconnect(c, fn),
      { initialProps: { c: true, fn: refetchA } },
    )
    // Still connected, only the callback identity changed — must not refetch.
    rerender({ c: true, fn: refetchB })
    expect(refetchA).not.toHaveBeenCalled()
    expect(refetchB).not.toHaveBeenCalled()
  })

  it('calls the latest refetch callback on reconnect', () => {
    const stale = vi.fn()
    const fresh = vi.fn()
    const { rerender } = renderHook(
      ({ c, fn }: { c: boolean; fn: () => void }) => useRefetchOnReconnect(c, fn),
      { initialProps: { c: true, fn: stale } },
    )
    rerender({ c: false, fn: fresh }) // drop, callback swapped
    rerender({ c: true, fn: fresh }) // reconnect
    expect(stale).not.toHaveBeenCalled()
    expect(fresh).toHaveBeenCalledTimes(1)
  })
})
