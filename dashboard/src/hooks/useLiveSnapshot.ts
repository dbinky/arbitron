import { useCallback, useEffect, useRef, useState } from 'react'
import { decodeLiveEvent, type LiveEvent } from '../api/runnerDecode'
import type { WebSocketEvent } from '../api/types'
import { useSystemWebSocket } from './useSystemWebSocket'

/** Poll interval while the system socket is down (precedent: GRAPH_STATE_POLL_MS). */
export const POOL_POLL_MS = 5000
/**
 * Refresh interval while connected. Heartbeat telemetry is deliberately not a
 * bus event, so live CPU, memory and heartbeat age are refreshed from REST.
 */
export const POOL_REFRESH_MS = 15_000
/** Coalesces bursts of invalidations (and the dirty follow-up) into one REST read. */
export const POOL_EVENT_DEBOUNCE_MS = 300

/** Result of applying one live event: the next (display-only) state and whether an authoritative reload is needed. */
export interface Applied<S> {
  state: S
  refetch: boolean
}

export interface LiveSnapshotOptions<S> {
  /** False: nothing is fetched and no state is kept. */
  enabled: boolean
  /** Changing the subject starts a new epoch: state resets and reloads. Fold identity into it when it can change. */
  subject: string
  load: () => Promise<S>
  apply: (state: S, live: LiveEvent) => Applied<S>
}

export interface LiveSnapshot<S> {
  /** Last successfully loaded snapshot (plus display-only log entries); null until the first success. */
  state: S | null
  loading: boolean
  error: string | null
  connected: boolean
  /** True while disconnected, before the first success, after a failed load, or until a (re)connect refresh succeeds. */
  stale: boolean
  refetch: () => Promise<void>
}

/**
 * Authoritative REST snapshot with event invalidation.
 *
 * - One active load per subject epoch plus one dirty flag: an invalidation
 *   during a load schedules exactly one fresh load after it settles. Events
 *   are never replayed as data patches on top of a result.
 * - A subject/enabled change or unmount bumps the epoch: older successes and
 *   errors are ignored and scheduled work is cancelled.
 * - A failed load keeps the last good state, sets `error` and `stale`.
 * - Disconnected: stale immediately, poll every POOL_POLL_MS. Every
 *   (re)connect triggers an authoritative refresh; stale clears only after a
 *   load succeeds while connected. Connected: refresh every POOL_REFRESH_MS.
 */
export function useLiveSnapshot<S>({ enabled, subject, load, apply }: LiveSnapshotOptions<S>): LiveSnapshot<S> {
  const [state, setState] = useState<S | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)
  const [fresh, setFresh] = useState(false)
  const stateRef = useRef<S | null>(null)
  const loadRef = useRef(load)
  loadRef.current = load
  const applyRef = useRef(apply)
  applyRef.current = apply
  const enabledRef = useRef(enabled)
  enabledRef.current = enabled
  const epoch = useRef(0)
  const active = useRef<Promise<void> | null>(null)
  const dirty = useRef(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const commit = useCallback((next: S | null) => {
    stateRef.current = next
    setState(next)
  }, [])

  const startLoadRef = useRef<() => Promise<void>>(() => Promise.resolve())

  const schedule = useCallback(() => {
    if (timer.current) return // one pending reload already covers this invalidation
    timer.current = setTimeout(() => {
      timer.current = null
      void startLoadRef.current()
    }, POOL_EVENT_DEBOUNCE_MS)
  }, [])

  const startLoad = useCallback((): Promise<void> => {
    if (!enabledRef.current) return Promise.resolve()
    if (active.current) {
      dirty.current = true
      return active.current
    }
    const mine = epoch.current
    dirty.current = false
    const run = (async () => {
      try {
        const next = await loadRef.current()
        if (mine !== epoch.current) return
        commit(next)
        setError(null)
        setFresh(true)
      } catch (err) {
        if (mine !== epoch.current) return
        setError(err instanceof Error ? err.message : 'Failed to load')
        setFresh(false)
      } finally {
        if (mine === epoch.current) {
          active.current = null
          setLoading(false)
          if (dirty.current) {
            dirty.current = false
            schedule()
          }
        }
      }
    })()
    active.current = run
    return run
  }, [commit, schedule])
  startLoadRef.current = startLoad

  useEffect(() => {
    epoch.current += 1
    active.current = null
    dirty.current = false
    commit(null)
    setError(null)
    setFresh(false)
    setLoading(enabled)
    if (enabled) void startLoadRef.current()
    return () => {
      epoch.current += 1
      active.current = null
      dirty.current = false
      if (timer.current) clearTimeout(timer.current)
      timer.current = null
    }
  }, [subject, enabled, commit])

  const onEvent = useCallback(
    (event: WebSocketEvent) => {
      if (!enabledRef.current) return
      const live = decodeLiveEvent(event)
      if (live.kind === 'ignored') return
      const current = stateRef.current
      let wanted = true
      if (current !== null) {
        const out = applyRef.current(current, live)
        if (out.state !== current) commit(out.state)
        wanted = out.refetch
      }
      if (!wanted) return
      if (active.current) dirty.current = true
      else schedule()
    },
    [commit, schedule],
  )

  const { connected } = useSystemWebSocket(onEvent)

  const wasConnected = useRef(false)
  useEffect(() => {
    if (connected === wasConnected.current) return
    wasConnected.current = connected
    setFresh(false)
    // Events published while the socket was closed (or before it first opened) are never replayed.
    if (connected) void startLoadRef.current()
  }, [connected])

  const interval = connected ? POOL_REFRESH_MS : POOL_POLL_MS
  useEffect(() => {
    if (!enabled) return
    const id = setInterval(() => {
      void startLoadRef.current()
    }, interval)
    return () => clearInterval(id)
  }, [enabled, interval, subject])

  return { state, loading, error, connected, stale: enabled && (!connected || !fresh || error !== null), refetch: startLoad }
}
