import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'

const setCenter = vi.fn()
const getZoom = vi.fn(() => 1.5)

vi.mock('@xyflow/react', () => ({
  useReactFlow: () => ({ setCenter, getZoom }),
}))

import { useFollowActiveNode } from './useFollowActiveNode'

beforeEach(() => {
  setCenter.mockClear()
  getZoom.mockClear()
  // Default: no reduced-motion preference, matchMedia present.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(window as any).matchMedia = vi.fn().mockImplementation((q: string) => ({
    matches: false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
})

describe('useFollowActiveNode — initial state', () => {
  it('starts with following=true', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    expect(result.current.following).toBe(true)
  })

  it('does NOT call setCenter on the first render (mount-centering owns that)', () => {
    renderHook(() => useFollowActiveNode({ x: 10, y: 20 }, true))
    expect(setCenter).not.toHaveBeenCalled()
  })
})

describe('useFollowActiveNode — follow on advance', () => {
  it('animates to the new anchor when it changes while following', () => {
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    // First render recorded the anchor; no setCenter yet.
    expect(setCenter).not.toHaveBeenCalled()

    rerender({ a: { x: 100, y: 200 } })
    expect(setCenter).toHaveBeenCalledTimes(1)
    expect(setCenter).toHaveBeenCalledWith(100, 200, {
      zoom: 1.5, // from mocked getZoom()
      duration: 400,
    })
  })

  it('does not animate when the anchor value is unchanged', () => {
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    rerender({ a: { x: 10, y: 20 } }) // same value, new object identity
    expect(setCenter).not.toHaveBeenCalled()
  })
})

describe('useFollowActiveNode — pause on user interaction', () => {
  it('stops following when onMoveStart receives a real (non-null) event', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    act(() => {
      result.current.onMoveStart({ type: 'pointerdown' }) // real user gesture
    })
    expect(result.current.following).toBe(false)
  })

  it('does NOT stop following when onMoveStart receives a null event (programmatic)', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    act(() => {
      result.current.onMoveStart(null) // our own setCenter
    })
    expect(result.current.following).toBe(true)
  })

  it('does not animate to a new anchor after the user paused following', () => {
    const { result, rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    act(() => {
      result.current.onMoveStart({ type: 'wheel' }) // user zooms => pause
    })
    setCenter.mockClear()
    rerender({ a: { x: 100, y: 200 } }) // execution advances
    expect(setCenter).not.toHaveBeenCalled()
  })
})

describe('useFollowActiveNode — recenter', () => {
  it('re-engages following and centers on the current anchor at DEFAULT_ZOOM', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    // Pause first.
    act(() => result.current.onMoveStart({ type: 'pointerdown' }))
    expect(result.current.following).toBe(false)
    setCenter.mockClear()

    act(() => result.current.recenter())

    expect(result.current.following).toBe(true)
    expect(setCenter).toHaveBeenCalledTimes(1)
    expect(setCenter).toHaveBeenCalledWith(10, 20, { zoom: 0.9 })
  })
})

describe('useFollowActiveNode — recenter after advance-while-paused', () => {
  it('recenter jumps to the latest anchor even if execution advanced while paused', () => {
    // Real risk: user pauses, workflow advances several nodes. Clicking Recenter must
    // jump to where the workflow IS NOW, not where it was at pause-time.
    // The implementation always updates prevAnchorRef even when following=false,
    // so this test verifies that invariant is preserved.
    const { result, rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )

    // Advance once while following: animates to (100, 200).
    rerender({ a: { x: 100, y: 200 } })
    expect(setCenter).toHaveBeenCalledWith(100, 200, expect.objectContaining({ zoom: 1.5 }))

    // User pans — pauses follow.
    act(() => result.current.onMoveStart({ type: 'pointerdown' }))
    setCenter.mockClear()

    // Execution advances to (300, 400) while paused — no animation, but ref updates.
    rerender({ a: { x: 300, y: 400 } })
    expect(setCenter).not.toHaveBeenCalled()

    // Recenter must use the current anchor (300, 400), not the stale (100, 200).
    act(() => result.current.recenter())
    expect(setCenter).toHaveBeenCalledWith(300, 400, { zoom: 0.9 })
  })
})

describe('useFollowActiveNode — edge case: null anchor (empty graph)', () => {
  it('does not call setCenter and does not throw when anchor is null', () => {
    // The design doc: "Empty graph / no anchor: skip centering (no-op); nothing throws."
    renderHook(() => useFollowActiveNode(null, false))
    expect(setCenter).not.toHaveBeenCalled()
  })

  it('recenter re-engages following but does not call setCenter when no anchor has been recorded', () => {
    // prevAnchorRef is never set when anchor stays null, so recenter must guard
    // against calling setCenter with undefined coordinates.
    const { result } = renderHook(() => useFollowActiveNode(null, false))
    act(() => {
      result.current.onMoveStart({ type: 'pointerdown' })
    })
    expect(result.current.following).toBe(false)

    act(() => result.current.recenter())

    expect(setCenter).not.toHaveBeenCalled()
    // following is re-engaged so it will animate when an anchor eventually arrives.
    expect(result.current.following).toBe(true)
  })
})

describe('useFollowActiveNode — isRunning guard', () => {
  // Regression: a completed (not-running) instance must NOT pan when a late
  // anchor change arrives (e.g. a trailing trace/layout update after the run
  // finished). The animate branch is gated on isRunning, so a stopped instance
  // never auto-pans even though following is still true.
  it('does NOT pan on a late anchor change when the instance is not running', () => {
    const { rerender } = renderHook(
      ({ a, running }: { a: { x: number; y: number }; running: boolean }) =>
        useFollowActiveNode(a, running),
      { initialProps: { a: { x: 10, y: 20 }, running: false } },
    )
    // First render records the anchor; no setCenter regardless.
    expect(setCenter).not.toHaveBeenCalled()

    // A late anchor change while not running must not animate.
    rerender({ a: { x: 100, y: 200 }, running: false })
    expect(setCenter).not.toHaveBeenCalled()
  })

  it('pans on an anchor change once the instance is running', () => {
    const { rerender } = renderHook(
      ({ a, running }: { a: { x: number; y: number }; running: boolean }) =>
        useFollowActiveNode(a, running),
      { initialProps: { a: { x: 10, y: 20 }, running: true } },
    )
    rerender({ a: { x: 100, y: 200 }, running: true })
    expect(setCenter).toHaveBeenCalledWith(100, 200, { zoom: 1.5, duration: 400 })
  })
})

describe('useFollowActiveNode — reduced motion', () => {
  it('uses duration 0 when prefers-reduced-motion is set', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).matchMedia = vi.fn().mockImplementation((q: string) => ({
      matches: q.includes('reduce'),
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    rerender({ a: { x: 100, y: 200 } })
    expect(setCenter).toHaveBeenCalledWith(100, 200, {
      zoom: 1.5,
      duration: 0,
    })
  })
})
