import { useCallback, useEffect, useRef, useState } from 'react'
import type { GraphState, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { useInstanceWebSocket } from './useInstanceWebSocket'
import { useRefetchOnReconnect } from './useRefetchOnReconnect'

/** Bounded periodic refetch interval while any visible context is active. */
export const GRAPH_STATE_POLL_MS = 5000
/** Inactive runs can be retried/resumed elsewhere without emitting a frame here. */
const INACTIVE_POLL_MS = 30_000
/** Bound both the transport and response body, releasing a stalled poll. */
const REQUEST_TIMEOUT_MS = 15_000
/** Debounce window coalescing a burst of subtree events into one refetch. */
export const GRAPH_STATE_EVENT_DEBOUNCE_MS = 300

const REFRESH_PREFIXES = ['node.', 'instance.', 'group.']

/** Lifecycle events refresh graph state; streaming chat traffic does not. */
export function isGraphStateRefreshEvent(type: string): boolean {
  if (type === 'interactive.ended' || type === 'subtree.inactive') return true
  return REFRESH_PREFIXES.some((p) => type.startsWith(p) && type.length > p.length)
}

export interface UseGraphStateOptions {
  /** When false, nothing is fetched or subscribed. Defaults to true. */
  enabled?: boolean
}

export interface GraphStateHandle {
  /** Latest applied aggregate; null until the first successful load. */
  state: GraphState | null
  /** True until the newest request settles. */
  loading: boolean
  error: string | null
  connected: boolean
  refetch: () => Promise<void>
}

/**
 * Keeps the authorized aggregate fresh through an initial fetch, debounced
 * subtree events, polling and reconnect resync. Inactive runs poll less often
 * to discover revived work. Transient failures retry; missing/forbidden runs
 * and unsupported endpoints stop automatic requests and socket reconnects.
 * Requests have a deadline and older responses cannot overwrite newer state.
 */
export function useGraphState(
  instanceId: string | undefined,
  options: UseGraphStateOptions = {},
): GraphStateHandle {
  const enabled = (options.enabled ?? true) && Boolean(instanceId)
  const [state, setState] = useState<GraphState | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<string | null>(null)
  const [stopped, setStopped] = useState(false)
  const issuedRef = useRef(0)
  const appliedRef = useRef(0)
  const inFlightRef = useRef(new Map<number, AbortController>())

  const refetch = useCallback(async () => {
    if (!enabled || !instanceId) return
    const seq = ++issuedRef.current
    const controller = new AbortController()
    inFlightRef.current.set(seq, controller)
    const aborted = new Promise<never>((_, reject) => {
      controller.signal.addEventListener('abort', () => reject(controller.signal.reason), { once: true })
    })
    const deadline = setTimeout(() => controller.abort(new Error('Graph state request timed out')), REQUEST_TIMEOUT_MS)
    setLoading(true)
    try {
      // Race explicitly: even a transport/body that ignores abort cannot keep
      // the in-flight guard locked or apply a response after its deadline.
      const next = await Promise.race([api.instances.graphState(instanceId, controller.signal), aborted])
      if (seq <= appliedRef.current) return
      appliedRef.current = seq
      setState((prev) =>
        prev && prev.version === next.version && prev.rootInstanceId === next.rootInstanceId ? prev : next,
      )
      setError(null)
      setStopped(false)
    } catch (err) {
      if (seq <= appliedRef.current) return
      appliedRef.current = seq
      setError(err instanceof Error ? err.message : 'Failed to load graph state')
      const status = err && typeof err === 'object' && 'status' in err ? err.status : undefined
      if (status === 401 || status === 403 || status === 404 || status === 501) {
        setStopped(true)
        setState(null)
        // Permission loss invalidates every response already in flight.
        appliedRef.current = issuedRef.current
        for (const [otherSeq, other] of inFlightRef.current) {
          if (otherSeq !== seq) other.abort()
        }
      }
    } finally {
      clearTimeout(deadline)
      inFlightRef.current.delete(seq)
      if (seq === issuedRef.current) setLoading(false)
    }
  }, [enabled, instanceId])

  const refetchRef = useRef(refetch)
  refetchRef.current = refetch

  // Invalidate requests for the previous subject before fetching the new one.
  useEffect(() => {
    appliedRef.current = issuedRef.current
    inFlightRef.current.clear()
    setState(null)
    setError(null)
    setStopped(false)
    setLoading(enabled)
    if (enabled) void refetch()
    return () => {
      appliedRef.current = issuedRef.current
      for (const controller of inFlightRef.current.values()) controller.abort()
      inFlightRef.current.clear()
    }
  }, [enabled, refetch])

  const pollMs = state?.active === false ? INACTIVE_POLL_MS : GRAPH_STATE_POLL_MS
  useEffect(() => {
    if (!enabled || stopped) return
    const timer = setInterval(() => {
      if (inFlightRef.current.size > 0) return
      void refetchRef.current()
    }, pollMs)
    return () => clearInterval(timer)
  }, [enabled, stopped, pollMs])

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const onEvent = useCallback((event: WebSocketEvent) => {
    if (!isGraphStateRefreshEvent(event.type)) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void refetchRef.current()
    }, GRAPH_STATE_EVENT_DEBOUNCE_MS)
  }, [])
  useEffect(
    () => () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = null
    },
    [enabled, instanceId, stopped],
  )

  // Undefined id closes the socket without scheduling another reconnect.
  // Before the first successful read, a failed read suspends the socket: the
  // server's handshake performs the same root read, so it would 500 every
  // reconnect. Polling keeps retrying the read and re-subscribes on success.
  const subscribe = enabled && !stopped && (state == null ? error == null : state.active)
  const { connected } = useInstanceWebSocket(subscribe ? instanceId : undefined, onEvent, { subtree: true })
  useRefetchOnReconnect(connected, refetch)

  return { state, loading, error, connected, refetch }
}
