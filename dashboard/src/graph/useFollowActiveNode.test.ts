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
