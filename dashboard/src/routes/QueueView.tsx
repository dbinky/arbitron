import { useEffect, useState, useCallback, useRef } from 'react'
import type { EnrichedInstanceSummary, Me, NodeEventData, WebSocketEvent } from '../api/types'
import { api } from '../api/client'
import { decodeLiveEvent } from '../api/runnerDecode'
import { useSystemWebSocket } from '../hooks/useSystemWebSocket'
import { useRefetchOnReconnect } from '../hooks/useRefetchOnReconnect'
import { useDensityPreference } from '../hooks/useDensityPreference'
import { useNow } from '../hooks/useNow'
import QueueSection from '../components/QueueSection'
import DensityToggle from '../components/DensityToggle'
import { QUEUE_POLL_MS, isQueueRefreshEvent } from './queueRefresh'

/** Returns an ISO date string for N days ago. */
function daysAgo(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return d.toISOString()
}

const HISTORY_DAYS = 5
const DEBOUNCE_MS = 300
const NO_IDS: ReadonlySet<string> = new Set()

/** Node events that should patch a single row in place (no refetch). */
const NODE_PATCH_EVENTS = new Set(['node.started', 'node.failed'])

type RowPatcher = (id: string, fields: Partial<EnrichedInstanceSummary>) => void

/**
 * The row fields a node event patches. A field ABSENT from the payload (older
 * server) is left out so the row keeps its prior value; an explicit "" (a
 * script node has no model) is kept so it overwrites. node.started carries the
 * same binding + git ref a refetch would compute (server-side
 * resolveNodeBinding/deriveGitRef), so the whole row patches together.
 * node.failed patches the step only: its legacy `backend` ignores the profile,
 * and instance.failed refetches anyway.
 */
function nodeEventPatch(type: string, d: NodeEventData): Partial<EnrichedInstanceSummary> {
  const src: Partial<EnrichedInstanceSummary> = { currentNodeId: d.nodeId, friendlyStep: d.nodeLabel }
  if (type === 'node.started') {
    Object.assign(src, {
      nodeType: d.nodeType,
      model: d.model,
      effort: d.effort,
      backend: d.backend,
      provider: d.provider,
      gitRef: d.gitRef,
    })
  }
  const fields: Partial<EnrichedInstanceSummary> = {}
  for (const [k, v] of Object.entries(src)) {
    if (v != null) (fields as Record<string, unknown>)[k] = v
  }
  return fields
}

/** Ancestor rows follow the active rows they own; a row already listed keeps its place. */
function withAncestors(active: EnrichedInstanceSummary[], ancestors: EnrichedInstanceSummary[]): EnrichedInstanceSummary[] {
  const listed = new Set(active.map((r) => r.id))
  return [...active, ...ancestors.filter((a) => !listed.has(a.id))]
}

interface AncestorResult {
  rows: EnrichedInstanceSummary[]
  error: string | null
}

// Explicitly match the REST maximum. A full page is a continuation, never a
// complete discovery result. Keep successful pages if a later request fails.
const ANCESTOR_PAGE_LIMIT = 500
async function discoverAncestors(): Promise<AncestorResult> {
  const rows = new Map<string, EnrichedInstanceSummary>()
  try {
    for (let offset = 0; ; offset += ANCESTOR_PAGE_LIMIT) {
      const page = await api.instances.list({
        activeDescendantAncestors: true, includeDismissed: true,
        limit: ANCESTOR_PAGE_LIMIT, offset,
      })
      for (const row of page) rows.set(row.id, row)
      if (page.length < ANCESTOR_PAGE_LIMIT) return { rows: [...rows.values()], error: null }
    }
  } catch (err) {
    return { rows: [...rows.values()], error: err instanceof Error ? err.message : 'Unknown error' }
  }
}

export default function QueueView() {
  const [active, setActive] = useState<EnrichedInstanceSummary[]>([])
  const [pending, setPending] = useState<EnrichedInstanceSummary[]>([])
  const [failed, setFailed] = useState<EnrichedInstanceSummary[]>([])
  const [completed, setCompleted] = useState<EnrichedInstanceSummary[]>([])
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showDismissed, setShowDismissed] = useState(false)
  // Terminal runs listed in Active only because they still own active work.
  const [ownsActiveWork, setOwnsActiveWork] = useState<ReadonlySet<string>>(NO_IDS)
  const [ancestorError, setAncestorError] = useState<string | null>(null)
  // Bumped on every successful top-level refetch so each QueueSection
  // refreshes its (independent) run-hierarchy cache — an expanded row's
  // children are otherwise never refreshed on their own, so a completed/failed
  // child kept rendering its stale state under an already-open parent.
  const [refreshedAt, setRefreshedAt] = useState(0)
  const inFlight = useRef(false)
  // Unversioned events invalidate; only authoritative reads supply rows.
  const dirty = useRef(false)
  const epoch = useRef(0)
  const [loadedOnce, setLoadedOnce] = useState(false)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fetchAllRef = useRef<() => Promise<void>>(() => Promise.resolve())
  const scheduleRefetch = useCallback(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void fetchAllRef.current()
    }, DEBOUNCE_MS)
  }, [])

  const { density, cycle: cycleDensity } = useDensityPreference(me?.userId ?? null)
  // One page-level clock so Elapsed / "N ago" cells advance between events.
  const now = useNow()
  const authMode = me?.authMode ?? 'local'

  const fetchAll = useCallback(async () => {
    if (inFlight.current) {
      dirty.current = true
      return
    }
    inFlight.current = true
    const mine = epoch.current
    try {
      const since = daysAgo(HISTORY_DAYS)

      // Ancestor discovery: TERMINAL runs that are ancestors of still-active
      // work the caller may see. Without it a completed fire-and-forget or
      // finish_independently parent falls out of the queue (outside the
      // Completed window, or dismissed) while its child keeps running.
      // includeDismissed: true — this is structural context for live work,
      // not the personal Completed inbox. A failure is reported but never takes
      // the queue down: the active child still shows (topLevelRows keeps a row
      // whose parent is not listed at top level), only its parent row is missing.
      const ancestorsReq = discoverAncestors()

      // Seam: `showDismissed` drives the FETCH (includeDismissed), per-row
      // `inst.dismissed` from the server drives the RENDER (Restore vs Dismiss,
      // dimming). The server reconciles the two — do NOT re-thread showDismissed
      // down into the rows or it reintroduces the toggle/refetch flicker.
      const [running, waiting, queued, paused, failedRes, completedRes, meResp, ancestors] =
        await Promise.all([
          api.instances.list({ state: 'running' }),
          api.instances.list({ state: 'waiting' }),
          api.instances.list({ state: 'queued' }),
          api.instances.list({ state: 'paused' }),
          api.instances.list({ state: 'failed', since, includeDismissed: showDismissed }),
          api.instances.list({ state: 'completed', since, includeDismissed: showDismissed }),
          api.me(),
          ancestorsReq,
        ])
      if (mine !== epoch.current) return

      // Ancestors go into the ACTIVE section: topLevelRows nests a row under
      // its parent only within one section, so this is what makes an active
      // branch a non-root row reached from its completed parent.
      const ancestorIds = new Set(ancestors.rows.map((r) => r.id))
      setActive(withAncestors([...running, ...waiting, ...paused], ancestors.rows))
      setOwnsActiveWork(ancestorIds)
      setAncestorError(ancestors.error)
      setPending(queued)
      // A terminal ancestor belongs to Active while it owns live work, even
      // when it is also inside the history window. Only subtract rows actually
      // discovered: on discovery failure the ordinary history stays available.
      setFailed(failedRes.filter((r) => !ancestorIds.has(r.id)))
      setCompleted(completedRes.filter((r) => !ancestorIds.has(r.id)))
      setMe(meResp)
      setError(null)
      setLoadedOnce(true)
      setRefreshedAt((n) => n + 1)
    } catch (err) {
      if (mine !== epoch.current) return
      const msg = err instanceof Error ? err.message : 'Unknown error'
      setError(msg)
    } finally {
      inFlight.current = false
      setLoading(false)
      if (dirty.current || mine !== epoch.current) {
        dirty.current = false
        scheduleRefetch()
      }
    }
  }, [showDismissed, scheduleRefetch])
  fetchAllRef.current = fetchAll

  const handleDismiss = useCallback(async (id: string) => {
    try {
      await api.instances.dismiss(id)
      await fetchAll()
    } catch (err) {
      // Route failures through the shared error surface so the unhappy path
      // isn't silent (a swallowed catch reads as "broken" to the user).
      setError(err instanceof Error ? err.message : 'Failed to dismiss instance')
    }
  }, [fetchAll])

  const handleRestore = useCallback(async (id: string) => {
    try {
      await api.instances.restore(id)
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restore instance')
    }
  }, [fetchAll])

  const handleDismissAll = useCallback(async (state: 'failed' | 'completed') => {
    try {
      // Scope to the same window the Failed/Completed sections show (daysAgo(5)),
      // so [Dismiss All] dismisses exactly "every item currently listed".
      await api.instances.dismissAll(state, daysAgo(HISTORY_DAYS))
      await fetchAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to dismiss instances')
    }
  }, [fetchAll])

  useEffect(() => {
    epoch.current += 1
    void fetchAll()
  }, [fetchAll])

  // Each QueueSection owns a run hierarchy whose expanded children are cached
  // rows separate from `active` (a child may be past the list window, or sit
  // under a completed parent), so node events are fed to every section's
  // hierarchy as well as to `active`.
  const rowPatchers = useRef(new Set<RowPatcher>())
  const registerRowPatcher = useCallback((patch: RowPatcher) => {
    rowPatchers.current.add(patch)
    return () => {
      rowPatchers.current.delete(patch)
    }
  }, [])

  const { connected } = useSystemWebSocket(
    useCallback(
      (event: WebSocketEvent) => {
        if (NODE_PATCH_EVENTS.has(event.type)) {
          const instId = event.instanceId
          if (!instId) return
          // `nodeLabel`/`nodeId` are untrusted: they originate from
          // workflow-author-controlled definitions and cross the Go→JSON→TS
          // boundary unvalidated. They are safe ONLY because `friendlyStep`
          // renders through React's escaped text sink (JSX text child). Never
          // route these into a raw-HTML (dangerouslySetInnerHTML) or attribute
          // sink (e.g. `title`) without sanitizing first.
          const fields = nodeEventPatch(event.type, (event.data ?? {}) as NodeEventData)
          setActive((prev) => {
            const idx = prev.findIndex((r) => r.id === instId)
            if (idx === -1) return prev // not listed yet; instance.started refetch will surface it
            const next = [...prev]
            next[idx] = { ...next[idx], ...fields }
            return next
          })
          for (const patch of rowPatchers.current) patch(instId, fields)
          return
        }
        // Lifecycle AND group events refetch (and so refresh open hierarchy
        // levels): a group decision or task update changes child rows and
        // their markers with no instance event on the expanded parent.
        if (isQueueRefreshEvent(event.type)) {
          scheduleRefetch()
          return
        }
        const live = decodeLiveEvent(event)
        if (live.kind === 'execution' || live.kind === 'node_done' || (live.kind === 'pool' && live.event.runId !== null)) {
          scheduleRefetch()
        }
      },
      [scheduleRefetch],
    ),
  )

  // The system WebSocket auto-reconnects after a drop, but events emitted
  // while it was down are not replayed. Resync on reconnect so the queue
  // doesn't sit stale until the next event arrives.
  useRefetchOnReconnect(connected, fetchAll)

  // Bounded periodic refetch while anything is active or pending: the bus
  // drops events when buffers fill and some transitions have no emitter, so a
  // lost final event must not leave a row or an open level stale forever. A
  // tick is skipped while a refetch is still in flight; nothing polls once the
  // queue holds only terminal runs.
  const hasLiveWork = active.length > 0 || pending.length > 0
  useEffect(() => {
    if (!hasLiveWork) return
    const timer = setInterval(() => {
      if (inFlight.current) return
      void fetchAll()
    }, QUEUE_POLL_MS)
    return () => clearInterval(timer)
  }, [hasLiveWork, fetchAll])

  // Clean up debounce timer on unmount.
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [])

  if (loading) {
    return (
      <div style={{ padding: 32, color: 'var(--text-muted)', textAlign: 'center' }}>
        Loading instances...
      </div>
    )
  }

  if (error && !loadedOnce) {
    return (
      <div style={{ padding: 32, textAlign: 'center' }}>
        <div style={{ color: 'var(--state-failed)', marginBottom: 12 }}>
          Failed to load instances: {error}
        </div>
        <button
          onClick={() => { setLoading(true); void fetchAll() }}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div style={{ padding: '16px 0' }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '0 16px 8px' }}>
        <DensityToggle density={density} onCycle={cycleDensity} />
      </div>
      {error && (
        <div role="alert" data-testid="queue-stale" style={{ margin: '0 16px 8px', padding: '6px 12px', fontSize: 12, color: 'var(--state-failed)', border: '1px solid var(--border-color)', borderRadius: 'var(--radius-md)' }}>
          {`Failed to refresh instances: ${error} — showing the last loaded data.`}{' '}
          <button type="button" onClick={() => { void fetchAll() }}>Retry refresh</button>
        </div>
      )}
      {ancestorError && (
        <div
          role="status"
          data-testid="ancestor-warning"
          style={{
            margin: '0 16px 8px',
            padding: '6px 12px',
            fontSize: 12,
            color: 'var(--state-waiting)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
          }}
        >
          {`Couldn't load completed runs that still own active work: ${ancestorError}`}
          {' '}
          <button type="button" onClick={() => { void fetchAll() }}>
            Retry ancestor discovery
          </button>
        </div>
      )}
      <QueueSection
        title="Active"
        instances={active}
        titleColor="var(--state-running)"
        countBg="rgba(52, 152, 219, 0.2)"
        countColor="var(--state-running)"
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        ownsActiveWork={ownsActiveWork}
      />
      <QueueSection
        title="Pending"
        instances={pending}
        titleColor="var(--state-queued)"
        countBg="rgba(241, 196, 15, 0.2)"
        countColor="var(--state-queued)"
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
      />
      <QueueSection
        title="Failed"
        instances={failed}
        titleColor="var(--state-failed)"
        countBg="rgba(231, 76, 60, 0.2)"
        countColor="var(--state-failed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        onDismissAll={() => handleDismissAll('failed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />
      <QueueSection
        title="Completed"
        instances={completed}
        titleColor="var(--state-completed)"
        countBg="rgba(46, 204, 113, 0.2)"
        countColor="var(--state-completed)"
        subtitle={`Last ${HISTORY_DAYS} days`}
        timeMode="elapsed"
        now={now}
        density={density}
        authMode={authMode}
        refreshSignal={refreshedAt}
        registerRowPatcher={registerRowPatcher}
        defaultCollapsed
        onDismissAll={() => handleDismissAll('completed')}
        onDismiss={handleDismiss}
        onRestore={handleRestore}
      />

      <div style={{ textAlign: 'center', padding: '16px 0' }}>
        <button
          type="button"
          onClick={() => setShowDismissed((s) => !s)}
          style={{
            padding: '6px 16px',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-color)',
            borderRadius: 'var(--radius-md)',
            color: 'var(--text-primary)',
            cursor: 'pointer',
          }}
        >
          {showDismissed ? 'Hide Dismissed' : 'Show Dismissed'}
        </button>
      </div>
    </div>
  )
}
