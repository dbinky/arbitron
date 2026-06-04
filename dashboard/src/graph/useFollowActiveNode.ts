import { useCallback, useEffect, useRef, useState } from 'react'
import { useReactFlow } from '@xyflow/react'
import { DEFAULT_ZOOM, FOLLOW_DURATION_MS } from './constants'

interface AnchorPosition {
  x: number
  y: number
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

export function useFollowActiveNode(
  anchor: AnchorPosition | null,
  _isRunning: boolean,
): UseFollowActiveNodeResult {
  const { setCenter, getZoom } = useReactFlow()
  const [following, setFollowing] = useState(true)

  // Remember the anchor we last centered on so the first render does not animate
  // (mount-centering in GraphView handles the initial center) and so we only
  // animate when the anchor actually changes.
  const prevAnchorRef = useRef<AnchorPosition | null>(null)

  useEffect(() => {
    if (!anchor) return
    const prev = prevAnchorRef.current
    // First time we see any anchor: record it, do not animate.
    if (prev === null) {
      prevAnchorRef.current = anchor
      return
    }
    // Unchanged anchor: nothing to do.
    if (prev.x === anchor.x && prev.y === anchor.y) return

    prevAnchorRef.current = anchor
    if (!following) return

    const duration = prefersReducedMotion() ? 0 : FOLLOW_DURATION_MS
    setCenter(anchor.x, anchor.y, { zoom: getZoom(), duration })
  }, [anchor, following, setCenter, getZoom])

  const onMoveStart = useCallback((event: unknown) => {
    if (event != null) setFollowing(false)
  }, [])

  const recenter = useCallback(() => {
    setFollowing(true)
    if (prevAnchorRef.current) {
      const a = prevAnchorRef.current
      setCenter(a.x, a.y, { zoom: DEFAULT_ZOOM })
    }
  }, [setCenter])

  return { following, onMoveStart, recenter }
}
