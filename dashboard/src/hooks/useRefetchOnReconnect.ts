import { useEffect, useRef } from 'react'

/**
 * Invokes `refetch` when `connected` transitions back to true after a prior
 * disconnect (a reconnect). It does NOT fire on the initial connection —
 * callers load their initial data separately.
 *
 * This closes a staleness gap: the system WebSocket auto-reconnects after a
 * drop, but events emitted while it was down are not replayed. Without a
 * resync, a view that only refetches on incoming events stays stale until the
 * next event arrives. Refetching on reconnect restores consistency.
 *
 * The `refetch` callback may change identity between renders without
 * retriggering — only a connection transition fires it, and the latest
 * callback is always the one invoked.
 */
export function useRefetchOnReconnect(connected: boolean, refetch: () => void) {
  const hasConnected = useRef(false)
  const refetchRef = useRef(refetch)
  refetchRef.current = refetch

  useEffect(() => {
    if (!connected) return
    if (hasConnected.current) {
      refetchRef.current()
    } else {
      hasConnected.current = true
    }
  }, [connected])
}
