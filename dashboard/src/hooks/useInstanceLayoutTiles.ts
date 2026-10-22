import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { ExecutionContext } from '../api/types'
import { ACTIVE_STATUSES, type LayoutView } from '../graph/arranged/types'
import { MAX_BACKOFF_MS, POLL_INTERVAL_MS } from './useLayoutStatus'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import type { WorkflowDefinition } from '../graph/types'

const MAX_CHILD_GETS = 4
const CHILD_GET_TIMEOUT_MS = 15_000
const message = (error: unknown) => error instanceof Error ? error.message : String(error)
type TileState = {
  views: ReadonlyMap<string, LayoutView>; errors: ReadonlyMap<string, string>
  retrying: ReadonlySet<string>; retryErrors: ReadonlyMap<string, string>
}
type Entry = {
  id: string; revision: string; definition: WorkflowDefinition; view?: LayoutView; error?: string; retryError?: string
  retrying: boolean; failures: number; minimum: number; sequence: number
  timer?: ReturnType<typeof setTimeout>; loading: boolean; queued: boolean
  controller?: AbortController; release?: () => void
}
type Controls = { refresh: (id: string) => void; retry: (id: string) => Promise<void>; reconcile: (contexts: readonly ExecutionContext[]) => void }
const emptyState = (): TileState => ({ views: new Map(), errors: new Map(), retrying: new Set(), retryErrors: new Map() })

/** A single bounded scheduler, with an independent lifetime for each snapshot. */
export function useInstanceLayoutTiles(contexts: readonly ExecutionContext[]): TileState & {
  refresh: (instanceId: string) => void; retry: (instanceId: string) => Promise<void>
} {
  const keys = JSON.stringify(contexts.map(c => [c.instanceId, (c.graph?.presentation as { revision?: string } | undefined)?.revision ?? '']))
  const snapshots = useRef(contexts)
  snapshots.current = contexts
  const controls = useRef<Controls | null>(null)
  const [state, setState] = useState(emptyState)
  const refresh = useCallback((id: string) => controls.current?.refresh(id), [])
  const retry = useCallback(async (id: string) => { await controls.current?.retry(id) }, [])

  useEffect(() => {
    const entries = new Map<string, Entry>()
    const queue: Entry[] = []
    let running = 0, disposed = false
    const live = (e: Entry) => !disposed && entries.get(e.id) === e
    function publish() {
      if (disposed) return
      const views = new Map<string, LayoutView>(), errors = new Map<string, string>()
      const retryErrors = new Map<string, string>(), retrying = new Set<string>()
      for (const e of entries.values()) {
        if (e.view) views.set(e.id, e.view)
        if (e.error) errors.set(e.id, e.error)
        if (e.retryError) retryErrors.set(e.id, e.retryError)
        if (e.retrying) retrying.add(e.id)
      }
      setState({ views, errors, retryErrors, retrying })
    }
    function schedule(e: Entry, delay: number) {
      clearTimeout(e.timer)
      if (live(e)) e.timer = setTimeout(() => enqueue(e), delay)
    }
    function fail(e: Entry, error: unknown) {
      e.error = message(error)
      if (e.view) e.view = { ...e.view, available: false, unreviewed: true, evidence: undefined,
        provenance: e.view.geometry ? 'retained_geometry' : e.view.provenance }
      e.failures++
      publish()
      schedule(e, Math.min(MAX_BACKOFF_MS, POLL_INTERVAL_MS * 2 ** (e.failures - 1)))
    }
    function enqueue(e: Entry) {
      clearTimeout(e.timer)
      if (!live(e) || e.loading || e.queued) return
      e.queued = true
      queue.push(e)
      pump()
    }
    function pump() {
      while (!disposed && running < MAX_CHILD_GETS && queue.length) {
        const e = queue.shift()!
        e.queued = false
        if (!live(e)) continue
        e.loading = true; running++
        const controller = new AbortController()
        e.controller = controller
        let released = false
        const release = () => {
          if (released) return
          released = true; e.loading = false; running--; pump()
        }
        e.release = release
        void load(e, controller).finally(release)
      }
    }
    async function load(e: Entry, controller: AbortController) {
      const { signal } = controller
      const sequence = ++e.sequence
      let timedOut = false
      let rejectAbort!: (reason: unknown) => void
      const aborted = new Promise<never>((_, reject) => { rejectAbort = reject })
      const onAbort = () => rejectAbort(signal.reason)
      signal.addEventListener('abort', onAbort, { once: true })
      const deadline = setTimeout(() => {
        timedOut = true
        controller.abort(new Error('Child layout request timed out'))
      }, CHILD_GET_TIMEOUT_MS)
      try {
        // Bound both fetch and body decoding, even if the transport ignores abort.
        const next = await Promise.race([api.instances.getLayout(e.id, signal), aborted])
        if (!live(e) || signal.aborted || sequence !== e.sequence) return
        if (next.savedRevision !== e.revision) throw new Error('Layout revision does not match the child snapshot revision')
        const previous = e.view
        if (!next.available) {
          const compatible = resolveDisplayGeometry(e.definition, next).geometry ? next
            : previous && resolveDisplayGeometry(e.definition, previous).geometry ? previous : null
          const geometry = compatible?.geometry
          e.view = { ...next, geometry, contract: compatible?.contract,
            groups: compatible?.groups, retained: next.retained ?? previous?.retained,
            jobGeneration: Math.max(next.jobGeneration, previous?.jobGeneration ?? 0),
            resultGeneration: Math.max(next.resultGeneration, previous?.resultGeneration ?? 0),
            provenance: geometry ? 'retained_geometry' : next.provenance, unreviewed: true, evidence: undefined }
          fail(e, next.availabilityError || 'Arrangement status unavailable')
          return
        }
        if (next.jobGeneration < e.minimum || (previous && (next.jobGeneration < previous.jobGeneration || next.resultGeneration < previous.resultGeneration))) {
          schedule(e, POLL_INTERVAL_MS)
          return
        }
        e.view = next; e.failures = 0; e.error = undefined
        publish()
        if (ACTIVE_STATUSES.has(next.displayStatus)) schedule(e, POLL_INTERVAL_MS)
      } catch (error) {
        if (live(e) && (!signal.aborted || timedOut) && sequence === e.sequence) fail(e, error)
      } finally {
        clearTimeout(deadline)
        signal.removeEventListener('abort', onAbort)
      }
    }
    async function retryCurrent(id: string) {
      const e = entries.get(id)
      if (!e || e.retrying) return
      if (!e.view) { enqueue(e); return }
      const displayed = e.view
      e.retrying = true; e.retryError = undefined; publish()
      try {
        const ack = await api.instances.retryLayout(id, displayed.savedRevision)
        if (!live(e)) return
        if (ack.savedRevision !== e.revision || ack.jobGeneration <= displayed.jobGeneration) throw new Error('Invalid arrangement retry acknowledgement')
        e.minimum = ack.jobGeneration
        e.sequence++
        e.failures = 0
        if (e.view!.jobGeneration <= ack.jobGeneration) e.view = { ...e.view!, jobGeneration: ack.jobGeneration, state: ack.state, displayStatus: 'pending', stage: 'pending', attemptsUsed: 0, findings: [], failureStage: undefined, failureReason: undefined,
          available: true, availabilityError: undefined, retryEligible: false, unreviewed: true, evidence: undefined,
          provenance: e.view!.geometry ? 'retained_geometry' : e.view!.provenance }
        e.error = undefined; publish()
        e.controller?.abort()
        e.release?.()
        enqueue(e)
      } catch (error) {
        if (live(e)) { e.retryError = message(error); publish() }
      } finally {
        if (live(e)) { e.retrying = false; publish() }
      }
    }
    controls.current = { refresh: id => { const e = entries.get(id); if (e) enqueue(e) }, retry: retryCurrent,
      reconcile: contexts => {
        const keys = contexts.map(c => [c.instanceId, (c.graph?.presentation as { revision?: string } | undefined)?.revision ?? ''] as const)
        const wanted = new Map(keys)
        for (const [id, e] of entries) if (wanted.get(id) !== e.revision) {
          clearTimeout(e.timer); entries.delete(id); e.controller?.abort(); e.release?.()
        }
        for (const [id, revision] of keys) if (!entries.has(id)) {
          const e: Entry = { id, revision, definition: contexts.find(c => c.instanceId === id)!.graph as unknown as WorkflowDefinition, retrying: false, failures: 0, minimum: 0, sequence: 0, loading: false, queued: false }
          entries.set(id, e); enqueue(e)
        }
        publish()
      } }
    const focus = () => { for (const e of entries.values()) enqueue(e) }
    const visible = () => { if (document.visibilityState === 'visible') focus() }
    window.addEventListener('focus', focus)
    document.addEventListener('visibilitychange', visible)
    return () => {
      disposed = true
      for (const e of entries.values()) { clearTimeout(e.timer); e.controller?.abort(); e.release?.() }
      controls.current = null
      window.removeEventListener('focus', focus)
      document.removeEventListener('visibilitychange', visible)
    }
  }, [])
  useEffect(() => { controls.current?.reconcile(snapshots.current) }, [keys])
  return { ...state, refresh, retry }
}
