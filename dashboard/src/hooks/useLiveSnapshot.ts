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
