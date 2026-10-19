import { useCallback, useEffect, useRef, useState } from 'react'
import { useReactFlow } from '@xyflow/react'
import { DEFAULT_ZOOM, FOLLOW_BOUNDS_PADDING, FOLLOW_DURATION_MS } from './constants'

interface AnchorPosition {
  x: number
  y: number
}

/** Flow-space rectangle enclosing every concurrently active node. */
export interface FollowBounds {
  x: number
  y: number
  width: number
  height: number
}

interface UseFollowActiveNodeResult {
  following: boolean
  onMoveStart: (event: unknown) => void
  recenter: () => void
}

function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return false
  }
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function sameBounds(a: FollowBounds | null, b: FollowBounds | null): boolean {
  if (a === b) return true
  if (!a || !b) return false
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
}

/**
 * Keeps the viewport on the active work while the run is live.
 *
 * With ONE active node the viewport follows that node (setCenter). With
 * several concurrently active nodes (`bounds` set — parallel branches, workflow
 * tasks, independent work) it frames ALL of them (fitBounds): following a
 * single node would pan the other concurrent paths off screen, hiding exactly
 * the parallelism the view exists to show.
 */
export function useFollowActiveNode(
  anchor: AnchorPosition | null,
  isRunning: boolean,
  bounds: FollowBounds | null = null,
): UseFollowActiveNodeResult {
  const { setCenter, getZoom, fitBounds } = useReactFlow()
  const [following, setFollowing] = useState(true)

  // Remember the anchor/bounds we last moved to so the first render does not
  // animate (mount-centering in GraphView handles the initial view) and so we
  // only animate when the target actually changes.
  const prevAnchorRef = useRef<AnchorPosition | null>(null)
  const prevBoundsRef = useRef<FollowBounds | null>(null)

  useEffect(() => {
    // Only auto-pan for a running instance with an anchor. A completed/stopped
    // instance must not pan on a late anchor change (trailing trace/layout
    // update); `following` stays user-controllable for the running case.
    if (!anchor || !isRunning) return
    const prev = prevAnchorRef.current
    // First time we see any anchor: record it, do not animate.
    if (prev === null) {
      prevAnchorRef.current = anchor
      prevBoundsRef.current = bounds
      return
    }
    // Unchanged target: nothing to do.
    if (prev.x === anchor.x && prev.y === anchor.y && sameBounds(prevBoundsRef.current, bounds)) return

    prevAnchorRef.current = anchor
    prevBoundsRef.current = bounds
    if (!following) return

    const duration = prefersReducedMotion() ? 0 : FOLLOW_DURATION_MS
    if (bounds) {
      void fitBounds(bounds, { padding: FOLLOW_BOUNDS_PADDING, duration })
      return
    }
    setCenter(anchor.x, anchor.y, { zoom: getZoom(), duration })
  }, [anchor, bounds, isRunning, following, setCenter, getZoom, fitBounds])

  const onMoveStart = useCallback((event: unknown) => {
    if (event != null) setFollowing(false)
  }, [])

  const recenter = useCallback(() => {
    setFollowing(true)
    if (prevBoundsRef.current) {
      void fitBounds(prevBoundsRef.current, { padding: FOLLOW_BOUNDS_PADDING })
      return
    }
    if (prevAnchorRef.current) {
      const a = prevAnchorRef.current
      setCenter(a.x, a.y, { zoom: DEFAULT_ZOOM })
    }
  }, [setCenter, fitBounds])

  return { following, onMoveStart, recenter }
}
