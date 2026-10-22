import { useCallback, useEffect, useRef, useState } from 'react'
import { ACTIVE_STATUSES, type LayoutView, type RetryAck } from '../graph/arranged/types'

export interface UseLayoutStatus {
  view: LayoutView | null
  error: string | null
  loading: boolean
  refresh: () => void
  retry: () => Promise<void>
  retrying: boolean
  retryError: string | null
}
export const POLL_INTERVAL_MS = 2000
export const MAX_BACKOFF_MS = 30000
/** Deadline for one status GET including its body, matching the child-tile hook. */
export const STATUS_GET_TIMEOUT_MS = 15_000

type StatusState = Omit<UseLayoutStatus, 'refresh' | 'retry'>
const initialState = (): StatusState => ({
  view: null, error: null, loading: true, retrying: false, retryError: null,
})
const message = (error: unknown) => error instanceof Error ? error.message : String(error)

/**
 * Each effect owns one key epoch, including its requests, retry and sole timer.
 * At most one GET is in flight; a refresh/focus/retry while it runs coalesces
 * into exactly one follow-up GET once it settles. Every GET is bounded by
 * STATUS_GET_TIMEOUT_MS and aborted when the epoch ends.
 */
export function useLayoutStatus(
  fetcher: (signal: AbortSignal) => Promise<LayoutView>,
  retrier: (expectedRevision: string) => Promise<RetryAck>,
  key: string,
  initialView: LayoutView | null = null,
): UseLayoutStatus {
  // A snapshot seed participates in the same generation guards as every GET.
  // The caller mounts this key's status owner once the snapshot is available.
  const callbacks = useRef({ fetcher, retrier, initialView })
  callbacks.current = { fetcher, retrier, initialView }
  const controls = useRef<{ refresh: () => void; retry: () => Promise<void> } | null>(null)
  const [state, setState] = useState<StatusState>(() => ({ ...initialState(), view: initialView }))
  const refresh = useCallback(() => controls.current?.refresh(), [])
  const retry = useCallback(async () => { await controls.current?.retry() }, [])

  useEffect(() => {
    let disposed = false
    let seq = 0
    let applied = 0
    let failures = 0
    let current: LayoutView | null = callbacks.current.initialView
    let retrying = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let inFlight: Promise<void> | null = null
    let refreshPending = false
    let followUp: { promise: Promise<void>; resolve: () => void } | null = null
    const epoch = new AbortController()
    const minimums = new Map<string, number>()
    setState({ ...initialState(), view: current })

    function clearTimer() {
      clearTimeout(timer)
      timer = undefined
    }
    function schedule(delay: number) {
      clearTimer()
      if (!disposed) timer = setTimeout(() => { void load() }, delay)
    }
    function failed(error: unknown) {
      failures++
      setState(s => ({ ...s, error: message(error), loading: false }))
      schedule(Math.min(MAX_BACKOFF_MS, POLL_INTERVAL_MS * 2 ** (failures - 1)))
    }
    // Bound the whole GET (transport and body) locally: the fetcher receives an
    // abort signal, and the race also rejects a fetcher that ignores it.
    async function boundedGet(): Promise<LayoutView> {
      const request = new AbortController()
      const { signal } = request
      const onEpochAbort = () => request.abort(epoch.signal.reason)
      epoch.signal.addEventListener('abort', onEpochAbort, { once: true })
      let rejectAbort!: (reason: unknown) => void
      const aborted = new Promise<never>((_, reject) => { rejectAbort = reject })
      signal.addEventListener('abort', () => rejectAbort(signal.reason), { once: true })
      const deadline = setTimeout(() => request.abort(new Error('Arrangement status request timed out')), STATUS_GET_TIMEOUT_MS)
      try {
        return await Promise.race([callbacks.current.fetcher(signal), aborted])
      } finally {
        clearTimeout(deadline)
        epoch.signal.removeEventListener('abort', onEpochAbort)
      }
    }
    /** Single-flight GET; while one runs, callers share one coalesced follow-up. */
    function load(): Promise<void> {
      if (disposed) return Promise.resolve()
      if (inFlight) {
        refreshPending = true
        if (!followUp) {
          let resolve!: () => void
          const promise = new Promise<void>(r => { resolve = r })
          followUp = { promise, resolve }
        }
        return followUp.promise
      }
      const run = fetchOnce()
      inFlight = run
      void run.finally(() => {
        inFlight = null
        const waiting = followUp
        followUp = null
        if (refreshPending && !disposed) {
          refreshPending = false
          void load().then(waiting?.resolve)
        } else {
          refreshPending = false
          waiting?.resolve()
        }
      })
      return run
    }
    async function fetchOnce() {
      clearTimer()
      const request = ++seq
      try {
        const next = await boundedGet()
        if (disposed || request < applied) return
        applied = request
        const cur = current
        if (!next.available) {
          // An unavailable store may return zero generations, not a stale replica.
          const previous = cur?.savedRevision === next.savedRevision ? cur : null
          const geometry = next.geometry ?? previous?.geometry
          current = {
            ...next,
            jobGeneration: Math.max(next.jobGeneration, previous?.jobGeneration ?? 0),
            resultGeneration: Math.max(next.resultGeneration, previous?.resultGeneration ?? 0),
            geometry,
            contract: next.contract ?? previous?.contract,
            retained: next.retained ?? previous?.retained,
            provenance: geometry ? 'retained_geometry' : next.provenance,
            unreviewed: true,
            evidence: undefined,
          }
          setState(s => ({ ...s, view: current }))
          failed(next.availabilityError || 'Arrangement status unavailable')
          return
        }
        if (next.jobGeneration < (minimums.get(next.savedRevision) ?? 0) ||
          (cur?.savedRevision === next.savedRevision &&
            (next.jobGeneration < cur.jobGeneration || next.resultGeneration < cur.resultGeneration))) {
          schedule(POLL_INTERVAL_MS)
          return
        }
        current = next
        failures = 0
        setState(s => ({ ...s, view: next, error: null, loading: false }))
        if (ACTIVE_STATUSES.has(next.displayStatus)) schedule(POLL_INTERVAL_MS)
        else clearTimer()
      } catch (error) {
        if (disposed || request < applied) return
        applied = request
        failed(error)
      }
    }
    async function retryCurrent() {
      if (retrying || disposed) return
      if (!current) { await load(); return }
      const displayed = current
      retrying = true
      setState(s => ({ ...s, retrying: true, retryError: null }))
      try {
        const ack = await callbacks.current.retrier(displayed.savedRevision)
        if (disposed) return
        if (current?.savedRevision !== displayed.savedRevision ||
          ack.savedRevision !== displayed.savedRevision || ack.jobGeneration <= displayed.jobGeneration) {
          throw new Error('Invalid arrangement retry acknowledgement')
        }
        minimums.set(ack.savedRevision, ack.jobGeneration)
        failures = 0
        applied = ++seq // GETs issued before the acknowledgement cannot undo it.
        if (current.jobGeneration <= ack.jobGeneration) {
          current = {
            ...current,
            jobGeneration: ack.jobGeneration,
            state: ack.state,
            displayStatus: 'pending',
            stage: 'pending',
            attemptsUsed: 0,
            findings: [],
            failureStage: undefined,
            failureReason: undefined,
            available: true,
            availabilityError: undefined,
            retryEligible: false,
            unreviewed: true,
            provenance: current.geometry ? 'retained_geometry' : current.provenance,
            evidence: undefined,
          }
        }
        setState(s => ({ ...s, view: current, error: null }))
        await load()
      } catch (error) {
        if (!disposed) setState(s => ({ ...s, retryError: message(error) }))
      } finally {
        retrying = false
        if (!disposed) setState(s => ({ ...s, retrying: false }))
      }
    }
    controls.current = { refresh: () => { void load() }, retry: retryCurrent }
    const focus = () => { void load() }
    const visible = () => { if (document.visibilityState === 'visible') void load() }
    window.addEventListener('focus', focus)
    document.addEventListener('visibilitychange', visible)
    void load()
    return () => {
      disposed = true
      epoch.abort(new Error('Arrangement status request cancelled'))
      clearTimer()
      controls.current = null
      window.removeEventListener('focus', focus)
      document.removeEventListener('visibilitychange', visible)
    }
  }, [key])

  return { ...state, refresh, retry }
}
