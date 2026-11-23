import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ReactFlowProvider, useReactFlow, type Node } from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { GraphState, Instance, Me, NodeExecution, ResolutionSessionRef } from '../api/types'
import type { WorkflowDefinition, NodeExecutionState } from '../graph/types'
import { api } from '../api/client'
import type { LayoutView } from '../graph/arranged/types'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import LayoutStatusPanel from '../graph/arranged/LayoutStatusPanel'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import { useLayoutStatus } from '../hooks/useLayoutStatus'
import { NODE_WIDTH, NODE_HEIGHT } from '../graph/useGraphLayout'
import { useInstanceLayoutTiles } from '../hooks/useInstanceLayoutTiles'
import type { LayoutTile } from '../graph/arranged/composeArrangedTiles'
import type { ArrangedGroupElementData } from '../graph/arranged/buildArrangedFlow'
import { selectAnchorNodeId } from '../graph/selectAnchorNodeId'
import { FOLLOW_BOUNDS_PADDING } from '../graph/constants'
import { useFollowActiveNode, type FollowBounds } from '../graph/useFollowActiveNode'
import { groupHeaderId, TASK_CARD_HEIGHT } from '../graph/groupLayout'
import type { GroupTaskCardData } from '../graph/groupLayout'
import {
  buildAggregateView,
  activeNodeLabels,
  findGroupPanel,
  nodeLabelIn,
  selectionFromNode,
  type GraphSelection,
} from '../graph/aggregateState'
import { nestedContexts } from '../graph/nestedGraphs'
import { GRAPH_STATE_POLL_MS, useGraphState } from '../hooks/useGraphState'
import { useInteractiveSession } from '../hooks/useInteractiveSession'
import CalloutConnector from '../graph/CalloutConnector'
import StateBadge from '../components/StateBadge'
import InstanceControls from '../components/InstanceControls'
import ResizablePanel from '../components/ResizablePanel'
import SidePanel from '../components/SidePanel'
import InteractivePanel from '../components/InteractivePanel'
import GroupPanel from '../components/GroupPanel'
import PlacementPanel from '../components/PlacementPanel'
import { useRunnerMarksByOwner } from '../hooks/useRunnerMarks'
import styles from './ExecutionView.module.css'

/**
 * Map trace data + instance state onto per-node execution states for the graph.
 * Legacy single-instance path: used until (or when) the aggregate graph state
 * is unavailable. With an aggregate, GraphView uses buildAggregateView instead,
 * keyed `${instanceId}:${nodeId}`.
 */
function buildExecutionStates(
  instance: Instance | null,
  trace: NodeExecution[],
  workflowDef: WorkflowDefinition | null,
): Record<string, NodeExecutionState> {
  const states: Record<string, NodeExecutionState> = {}

  if (!workflowDef) return states

  // Initialize all nodes as pending.
  for (const node of workflowDef.nodes) {
    states[node.id] = 'pending'
  }

  // Apply trace results.
  for (const exec of trace) {
    switch (exec.status) {
      case 'succeeded':
        states[exec.nodeId] = 'completed'
        break
      case 'failed':
        states[exec.nodeId] = 'failed'
        break
      case 'skipped':
        states[exec.nodeId] = 'skipped'
        break
    }
  }

  // Mark the current node as running/waiting if the instance is active.
  if (instance?.currentNodeId) {
    if (instance.state === 'running') {
      states[instance.currentNodeId] = 'running'
    } else if (instance.state === 'waiting') {
      states[instance.currentNodeId] = 'waiting'
    }
  }

  return states
}

const ACTIVE_STATES = new Set<string>(['queued', 'running', 'waiting', 'paused'])
const EMPTY_WORKFLOW: WorkflowDefinition = { name: '', description: '', author: '', nodes: [], edges: [] }
const NO_CONTEXTS: GraphState['contexts'] = []

export function GraphView({
  instance,
  workflowDef,
  trace,
  onNodeClick,
  selectedNodeId,
  graphState = null,
  onSelect,
  layoutView = null,
  onDisplayIssues,
  canRetry = false,
}: {
  instance: Instance | null
  workflowDef: WorkflowDefinition | null
  trace: NodeExecution[]
  /** Legacy click callback (definition node id). Ignored when onSelect is given. */
  onNodeClick?: (nodeId: string) => void
  /** Rendered (React Flow) id of the selected node, header, footer or card. */
  selectedNodeId: string | null
  /** Authorized aggregate of this run and its descendants (P4-T6 useGraphState). */
  graphState?: GraphState | null
  /** Instance-qualified selection: a step node with its owning instance, or a group. */
  onSelect?: (selection: GraphSelection) => void
  /** Server-arranged geometry for the snapshot revision (null: fallback layout). */
  layoutView?: LayoutView | null
  onDisplayIssues?: (issues: readonly string[]) => void
  canRetry?: boolean
}) {
  const runnerMarks = useRunnerMarksByOwner(instance, workflowDef, trace, graphState)
  // Aggregate view (P4-T8): node states keyed `${instanceId}:${nodeId}`,
  // projected onto the rendered graph through Phase 1 ownership. Memoized on
  // the GraphState object, which useGraphState only replaces when its version
  // changes, so the layout does not recompute on unrelated renders.
  const aggregate = useMemo(
    () => (graphState && workflowDef ? buildAggregateView(graphState, workflowDef) : null),
    [graphState, workflowDef],
  )
  // Memoize so identity only changes when the inputs actually change. Without
  // this, every re-render (e.g. each live WebSocket event) produces a fresh
  // executionStates object, defeating useGraphLayout's memo — dagre re-runs and
  // ReactFlow re-renders every node, which reads as a per-event "flicker".
  const legacyStates = useMemo(
    () => buildExecutionStates(instance, trace, workflowDef),
    [instance, trace, workflowDef],
  )
  const executionStates = aggregate ? aggregate.executionStates : legacyStates
  const groups = aggregate ? graphState?.layout ?? null : null
  const childContexts = useMemo(() => graphState ? nestedContexts(graphState) : NO_CONTEXTS, [graphState])
  const childLayouts = useInstanceLayoutTiles(childContexts)
  const tiles = useMemo<LayoutTile[] | undefined>(() => {
    if (!workflowDef || !graphState || !aggregate) return undefined
    const root: LayoutTile = { instanceId: graphState.rootInstanceId, definition: workflowDef, view: layoutView, groups,
      executionStates: aggregate.executionStates, groupLive: aggregate.groupLive, nodeContexts: aggregate.nodeContexts }
    if (!childContexts.length && !resolveDisplayGeometry(workflowDef, layoutView).geometry) return undefined
    return [root, ...childContexts.map(c => {
      const definition = c.graph as unknown as WorkflowDefinition
      const local = buildAggregateView({ ...graphState, rootInstanceId: c.instanceId, layout: c.layout }, definition)
      return { instanceId: c.instanceId, definition, view: childLayouts.views.get(c.instanceId) ?? null, groups: c.layout ?? null,
        executionStates: local.executionStates, groupLive: local.groupLive, nodeContexts: local.nodeContexts }
    })]
  }, [workflowDef, graphState, aggregate, layoutView, groups, childContexts, childLayouts.views])
  const [rfNodes, setRfNodes] = useState<Node[]>([])
  const [tileIssues, setTileIssues] = useState<ReadonlyMap<string, readonly string[]>>(new Map())
  const absolute = useCallback((node: Node): { x: number; y: number } => {
    let x = node.position.x, y = node.position.y, parent = node.parentId
    while (parent) {
      const p = rfNodes.find(n => n.id === parent)
      if (!p) break
      x += p.position.x; y += p.position.y; parent = p.parentId
    }
    return { x, y }
  }, [rfNodes])

  const reactFlow = useReactFlow()

  // Follow every active branch step and workflow task card, including
  // independent descendant work, rather than just the root cursor.
  const activeIds = useMemo(
    () =>
      rfNodes
        .filter((n) => {
          if (n.type === 'arbitronNode' || n.type === 'arrangedNode') return ACTIVE_STATES.has(n.data.executionState as NodeExecutionState)
          if (n.type !== 'groupTaskCard') return false
          const live = (n.data.reviewed as ArrangedGroupElementData | undefined)?.taskLive ?? (n.data as GroupTaskCardData).live
          return Boolean(live && !live.redacted && (live.activeNodes.length > 0 || ACTIVE_STATES.has(live.status)))
        })
        .map((n) => n.id),
    [rfNodes],
  )

  // With several active nodes the viewport frames all of them (see
  // useFollowActiveNode); following one node would hide the other paths.
  const activeBounds = useMemo<FollowBounds | null>(() => {
    if (activeIds.length < 2) return null
    const rects = rfNodes.filter((n) => activeIds.includes(n.id))
    const minX = Math.min(...rects.map((n) => absolute(n).x))
    const minY = Math.min(...rects.map((n) => absolute(n).y))
    const maxX = Math.max(...rects.map((n) => absolute(n).x + (n.width ?? NODE_WIDTH)))
    const maxY = Math.max(...rects.map((n) => absolute(n).y + (n.height ?? (n.type === 'groupTaskCard' ? TASK_CARD_HEIGHT : NODE_HEIGHT))))
    return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
  }, [activeIds, rfNodes, absolute])

  // The real step node the viewport should center on for this instance state.
  // A group node never renders itself: its header stands in for it.
  const anchorId = useMemo(() => {
    if (aggregate && activeIds.length === 1) return activeIds[0]
    const id = selectAnchorNodeId(instance, workflowDef, executionStates, trace)
    if (id && groups?.groups[id]) return groupHeaderId(id)
    return id
  }, [aggregate, activeIds, instance, workflowDef, executionStates, trace, groups])

  // Resolve the anchor's node-center coordinates from the laid-out rfNodes.
  // Loop-container overlay nodes are excluded automatically: anchorId only
  // ever references a real workflowDef node id (or a group header).
  const anchorPos = useMemo(() => {
    if (activeBounds) {
      return { x: activeBounds.x + activeBounds.width / 2, y: activeBounds.y + activeBounds.height / 2 }
    }
    if (!anchorId) return null
    const node = rfNodes.find((n) => n.id === anchorId)
    if (!node) return null
    return {
      x: absolute(node).x + (node.width ?? NODE_WIDTH) / 2,
      y: absolute(node).y + (node.height ?? (node.type === 'groupTaskCard' ? TASK_CARD_HEIGHT : NODE_HEIGHT)) / 2,
    }
  }, [activeBounds, anchorId, rfNodes, absolute])

  // The shared renderer centres a single anchor; frame simultaneous work once.
  // Subsequent changes respect the user's follow/pan choice.
  const didInitialCenter = useRef(false)
  useEffect(() => {
    if (!activeBounds || didInitialCenter.current) return
    void reactFlow.fitBounds(activeBounds, { padding: FOLLOW_BOUNDS_PADDING })
    didInitialCenter.current = true
  }, [activeBounds, reactFlow])

  // ArrangedGraph reports the displayed payload centre of the anchor node.
  const [arrangedAnchor, setArrangedAnchor] = useState<{ x: number; y: number } | null>(null)
  const arrangedAnchorId = useMemo(
    () => selectAnchorNodeId(instance, workflowDef, executionStates, trace),
    [instance, workflowDef, executionStates, trace],
  )

  const isRunning = aggregate
    ? graphState?.active === true
    : instance?.state === 'running' || instance?.state === 'waiting'
  const { following, onMoveStart, recenter } = useFollowActiveNode(
    activeIds.length ? anchorPos : arrangedAnchor,
    isRunning,
    activeBounds,
  )

  const fallbackInstanceId = instance?.id ?? graphState?.rootInstanceId ?? null

  const recenterButton = (
    <button
      type="button"
      className={`${styles.recenterButton} ${following ? styles.recenterButtonActive : ''}`}
      aria-pressed={following}
      title={following ? 'Following the active steps' : 'Recenter and follow the active steps'}
      onClick={recenter}
    >
      {following ? 'Following' : 'Recenter'}
    </button>
  )

  return (
    <div className={styles.tileScene}>
      {Object.entries(runnerMarks.errors).map(([owner, error]) => <div role="status" key={owner}>Runner placement unavailable for {owner}: {error}</div>)}
      {childContexts.length > 0 && <nav className={styles.tileRail} aria-label="Child arrangements">
        {childContexts.map(c => {
          const definition = c.graph as unknown as WorkflowDefinition
          const view = childLayouts.views.get(c.instanceId) ?? null
          return <section key={c.instanceId} data-layout-status-instance={c.instanceId}>
            <Link to={`/instances/${c.instanceId}`}>{definition.name || c.workflowName || 'Child workflow'}</Link>
            <LayoutStatusPanel view={view} error={childLayouts.errors.get(c.instanceId) ?? null}
              onRetry={() => { if (view) void childLayouts.retry(c.instanceId); else childLayouts.refresh(c.instanceId) }}
              retrying={childLayouts.retrying.has(c.instanceId)} retryError={childLayouts.retryErrors.get(c.instanceId) ?? null}
              canRetry={canRetry} displayIssues={tileIssues.get(c.instanceId) ?? resolveDisplayGeometry(definition, view).issues} />
          </section>
        })}
      </nav>}
      <div className={styles.graphContainer}>
        <ArrangedGraph
          workflowDef={workflowDef ?? EMPTY_WORKFLOW} view={layoutView} tiles={tiles}
          runnerMarksByOwner={runnerMarks.marks} rootInstanceId={instance?.id ?? graphState?.rootInstanceId ?? ''}
          executionStates={executionStates} groups={groups} groupLive={aggregate?.groupLive} nodeContexts={aggregate?.nodeContexts}
          selectedNodeId={selectedNodeId}
          onNodeClick={(nodeId, node) => {
            if (onSelect && node) onSelect(selectionFromNode(node, fallbackInstanceId))
            else onNodeClick?.(nodeId)
          }}
          anchorNodeId={activeIds.length === 1 ? activeIds[0] : arrangedAnchorId}
          onMoveStart={onMoveStart} onAnchorPosition={setArrangedAnchor} onDisplayNodes={setRfNodes} onTileDisplayIssues={setTileIssues}
          onDisplayIssues={onDisplayIssues}
        >{recenterButton}</ArrangedGraph>
      </div>
    </div>
  )
}

/** Mounted only once the immutable snapshot is known; keyed by instance/revision. */
function InstanceArrangement({ instance, workflowDef, trace, graphState, onSelect, selectedNodeId }: {
  instance: Instance
  workflowDef: WorkflowDefinition | null
  trace: NodeExecution[]
  graphState: GraphState | null
  onSelect: (selection: GraphSelection) => void
  selectedNodeId: string | null
}) {
  const [me, setMe] = useState<Me | null>(null)
  useEffect(() => {
    let cancelled = false
    api.me().then(identity => {
      if (!cancelled) setMe(identity)
    }).catch(() => {
      // Unknown identity keeps the intervention control hidden.
    })
    return () => { cancelled = true }
  }, [])
  // Mirror engine.canIntervene for the already-authorized instance read.
  // The retry endpoint rechecks permissions in case they changed since loading.
  const canRetry = me != null && (me.isAdmin || me.userId === instance.ownerId ||
    (instance.department != null && me.ledDepartments.includes(instance.department)))
  const revision = workflowDef?.presentation?.revision ?? ''
  const seed = instance.layout?.savedRevision === revision ? instance.layout : null
  const layout = useLayoutStatus(
    signal => api.instances.getLayout(instance.id, signal),
    expectedRevision => api.instances.retryLayout(instance.id, expectedRevision),
    instance.id,
    seed,
  )
  const matches = layout.view?.savedRevision === revision
  // An outage preserves the same displayed boxes without claiming fresh review.
  const joinedView = useMemo(() => {
    if (!matches || !layout.view) return null
    return layout.error ? { ...layout.view, available: false, unreviewed: true } : layout.view
  }, [matches, layout.view, layout.error])
  const mismatch = layout.view && !matches ? 'Layout revision does not match the instance snapshot revision' : null
  const [renderIssues, setRenderIssues] = useState<readonly string[]>([])
  const displayIssues = useMemo(() => [
    ...(workflowDef ? resolveDisplayGeometry(workflowDef, joinedView).issues : []),
    ...renderIssues,
  ], [workflowDef, joinedView, renderIssues])
  const handleRetry = () => {
    if (joinedView) void layout.retry()
    else layout.refresh()
  }

  return (
    <>
      <LayoutStatusPanel
        view={joinedView}
        error={mismatch ?? layout.error}
        onRetry={handleRetry}
        retrying={layout.retrying}
        retryError={layout.retryError}
        canRetry={canRetry}
        displayIssues={displayIssues}
      />
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        graphState={graphState}
        onSelect={onSelect}
        selectedNodeId={selectedNodeId}
        layoutView={joinedView}
        onDisplayIssues={setRenderIssues}
        canRetry={canRetry}
      />
    </>
  )
}

const priorityClass: Record<string, string> = {
  high: styles.priorityHigh,
  normal: styles.priorityNormal,
  low: styles.priorityLow,
}

/** A keyed lifetime prevents one owner's conversation from surviving a session switch. */
function LinkedResolutionSession({ target, nodeName, onEnded }: {
  target: ResolutionSessionRef
  nodeName: string
  onEnded: () => void
}) {
  const session = useInteractiveSession(target.instanceId)
  const { seedFromInstance, isActive } = session
  const wasActive = useRef(false)
  useEffect(() => { seedFromInstance(target.nodeId, 'group_resolution') }, [seedFromInstance, target.nodeId])
  useEffect(() => {
    if (isActive) wasActive.current = true
    else if (wasActive.current) onEnded()
  }, [isActive, onEnded])
  return <InteractivePanel session={session} nodeName={nodeName} />
}

export default function ExecutionView() {
  const { id } = useParams<{ id: string }>()
  // An instance switch fences all REST, status, font, selection and WS state.
  return <ExecutionPage key={id} id={id} />
}

function ExecutionPage({ id }: { id: string | undefined }) {
  const [instance, setInstance] = useState<Instance | null>(null)
  const [workflowDef, setWorkflowDef] = useState<WorkflowDefinition | null>(null)
  const [trace, setTrace] = useState<NodeExecution[]>([])
  const [variables, setVariables] = useState<Record<string, unknown> | null>(null)
  const [selection, setSelection] = useState<GraphSelection | null>(null)
  const [selectedExec, setSelectedExec] = useState<NodeExecution | null>(null)
  const [ownerVariables, setOwnerVariables] = useState<Record<string, unknown> | null>(null)
  const [sessionTarget, setSessionTarget] = useState<ResolutionSessionRef | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Aggregate state of this run and every visible descendant: subtree events,
  // bounded polling while active, reconnect resync (P4-T6).
  const graph = useGraphState(id)

  // Interactive session state.
  const interactiveSession = useInteractiveSession(id)

  const closeLinkedSession = useCallback(() => setSessionTarget(null), [])

  // Pre-warm session state from the REST instance data so the interactive panel
  // renders immediately on page load, before interactive.history arrives over WebSocket.
  useEffect(() => {
    if (instance?.interactiveActive && instance.interactiveNodeId) {
      interactiveSession.seedFromInstance(instance.interactiveNodeId)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seedFromInstance is
    // a stable useCallback with no deps; including interactiveSession would cause
    // an infinite re-render loop since every state update creates a new object.
  }, [instance?.interactiveActive, instance?.interactiveNodeId])

  // Opening a resolution session from a group panel seeds the owning session.
  // Keyed by the full session identity: after an attempt ends, the next
  // attempt at the same node must seed again.
  useEffect(() => {
    if (sessionTarget && sessionTarget.instanceId === id) interactiveSession.seedFromInstance(sessionTarget.nodeId, 'group_resolution')
    // eslint-disable-next-line react-hooks/exhaustive-deps -- seedFromInstance is stable (see above).
  }, [sessionTarget, id])

  const labelOf = useCallback(
    (nodeId: string) => workflowDef?.nodes.find((n) => n.id === nodeId)?.label || nodeId,
    [workflowDef],
  )

  // Resolve the interactive session's nodeId to a human-readable label.
  const interactiveNodeName = useMemo(() => {
    if (!interactiveSession.nodeId || !workflowDef) return 'Unknown Node'
    return labelOf(interactiveSession.nodeId)
  }, [interactiveSession.nodeId, workflowDef, labelOf])

  // Fetch the route instance's own reads (header, controls, variables).
  const fetchData = useCallback(async () => {
    if (!id) return
    try {
      const [inst, traceData, vars] = await Promise.all([
        api.instances.get(id),
        api.instances.getTrace(id),
        api.instances.getVariables(id),
      ])
      setInstance(inst)
      setTrace(traceData)
      setVariables(vars)

      if (inst.workflowSnapshot) {
        setWorkflowDef(inst.workflowSnapshot as unknown as WorkflowDefinition)
      }

      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load instance')
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  // A server without graph-state support has no aggregate version changes to
  // drive these reads. Keep its single-run graph/header live through polling.
  const legacyRefresh = Boolean(graph.error && !graph.state)
  useEffect(() => {
    if (!legacyRefresh) return
    const timer = setInterval(() => { void fetchData() }, GRAPH_STATE_POLL_MS)
    return () => clearInterval(timer)
  }, [legacyRefresh, fetchData])

  // The route instance's reads follow the aggregate: whenever its version
  // changes (a subtree event, a poll, or a reconnect resync inside
  // useGraphState) refresh them. This replaces the former per-event handler,
  // which also listened for three event types the engine never emits
  // (a skipped-node, a variables-updated and a state-changed event).
  const version = graph.state?.version
  const seenVersion = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (!version) return
    if (seenVersion.current === undefined) {
      seenVersion.current = version // the first aggregate lands alongside the initial fetch
      return
    }
    if (seenVersion.current === version) return
    seenVersion.current = version
    void fetchData()
  }, [version, fetchData])

  const refreshAll = useCallback(() => {
    void fetchData()
    void graph.refetch()
  }, [fetchData, graph])

  // Instance-qualified selection: a branch-body node opens ITS branch
  // instance's trace (and variables); header/footer/task card open the group.
  const selectSeq = useRef(0)
  const handleSelect = useCallback(
    async (sel: GraphSelection) => {
      const seq = ++selectSeq.current
      setSelection(sel)
      setSessionTarget(null)
      setSelectedExec(null)
      setOwnerVariables(null)
      if (sel.kind !== 'node' || !sel.instanceId) return
      const owner = sel.instanceId
      const [exec, vars] = await Promise.all([
        api.instances.getNodeTrace(owner, sel.nodeId).catch(() => null),
        owner === id ? Promise.resolve(null) : api.instances.getVariables(owner).catch(() => null),
      ])
      if (seq !== selectSeq.current) return // a newer selection won
      setSelectedExec(exec)
      setOwnerVariables(vars)
    },
    [id],
  )

  if (loading) {
    return <div className={styles.loading}>Loading instance...</div>
  }

  if (error) {
    return <div className={styles.error}>{error}</div>
  }

  if (!instance) {
    return <div className={styles.error}>Instance not found</div>
  }

  const shortId = instance.id.slice(0, 8)
  const connected = graph.connected
  const selectedOwner = selection?.kind === 'node' ? selection.instanceId : null
  const ownerContext = selectedOwner && selectedOwner !== id
    ? graph.state?.contexts.find((c) => c.instanceId === selectedOwner) ?? null
    : null
  // Branches share their nearest workflow ancestor's immutable definition.
  let selectedDefinition = workflowDef
  let definitionOwner = ownerContext
  const visitedOwners = new Set<string>()
  while (definitionOwner && !visitedOwners.has(definitionOwner.instanceId)) {
    visitedOwners.add(definitionOwner.instanceId)
    if (definitionOwner.graph) {
      selectedDefinition = definitionOwner.graph as unknown as WorkflowDefinition
      break
    }
    definitionOwner = graph.state?.contexts.find(c => c.instanceId === definitionOwner?.parentInstanceId) ?? null
  }
  const groupPanel = selection?.kind === 'group'
    ? findGroupPanel(graph.state, selection.instanceId, selection.nodeId)
    : null
  const linkedActive = Boolean(sessionTarget && sessionTarget.instanceId !== id)
  const routeSessionVisible = interactiveSession.isActive && !linkedActive &&
    (selection?.kind !== 'group' || sessionTarget?.instanceId === id)
  const calloutNodeId = linkedActive ? sessionTarget?.nodeId : interactiveSession.nodeId
  const calloutInstanceId = linkedActive ? sessionTarget?.instanceId : instance.id

  let right
  if (linkedActive && sessionTarget) {
    right = (
      <LinkedResolutionSession
        key={`${sessionTarget.instanceId}:${sessionTarget.nodeId}:${sessionTarget.groupRunId}:${sessionTarget.mergeIndex}:${sessionTarget.attempt}`}
        target={sessionTarget}
        nodeName={`Conflict resolution · ${labelOf(sessionTarget.nodeId)}`}
        onEnded={closeLinkedSession}
      />
    )
  } else if (sessionTarget?.instanceId === id && interactiveSession.isActive) {
    right = <InteractivePanel session={interactiveSession} nodeName={interactiveNodeName} />
  } else if (selection?.kind === 'group') {
    right = <GroupPanel panel={groupPanel} label={nodeLabelIn(graph.state, workflowDef, selection.instanceId, selection.nodeId)} onOpenSession={setSessionTarget}
      activeNodes={Object.fromEntries((groupPanel?.tasks ?? []).filter(t => !t.redacted && t.childInstanceId).map(t => [t.key,
        graph.state ? activeNodeLabels(graph.state, t.childInstanceId!, workflowDef ?? EMPTY_WORKFLOW) : [],
      ]))} />
  } else if (interactiveSession.isActive) {
    right = <InteractivePanel session={interactiveSession} nodeName={interactiveNodeName} />
  } else {
    right = (
      <div className={styles.sideStack}>
        {ownerContext && (
          <div className={styles.ownerBanner} data-testid="trace-owner">
            {ownerContext.redacted
              ? 'Trace hidden (hidden dependency)'
              : `Trace from ${ownerContext.kind}${ownerContext.taskKey ? ` task ${ownerContext.taskKey}` : ''} · ${ownerContext.instanceId.slice(0, 8)}`}
          </div>
        )}
        {selection?.kind === 'node' && !selection.instanceId && (
          <div className={styles.ownerBanner} data-testid="trace-owner">Not started in this run yet</div>
        )}
        {selection?.kind === 'node' && selection.instanceId && !ownerContext?.redacted && (
          <PlacementPanel
            execution={selectedExec}
            instance={instance}
            nodeId={selection.nodeId}
            nodeType={selectedDefinition?.nodes.find(n => n.id === selection.nodeId)?.type ?? null}
            ownerIsRoute={selection.instanceId === id}
          />
        )}
        <SidePanel
          execution={selectedExec}
          variables={ownerContext ? ownerVariables : variables}
          selectedNodeId={selection?.kind === 'node' ? selection.nodeId : null}
          workflowDef={workflowDef}
        />
      </div>
    )
  }

  return (
    <ReactFlowProvider>
      <div className={styles.page} data-execution-view="true">
        {/* Breadcrumb bar */}
        <div className={styles.breadcrumb}>
          <Link to="/" className={styles.breadcrumbLink}>
            Queue
          </Link>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.breadcrumbCurrent}>
            {instance.workflowName}
          </span>
          <span className={styles.breadcrumbSep}>/</span>
          <span className={styles.instanceId}>{shortId}</span>
          <StateBadge state={instance.state} />
          <span
            className={`${styles.priorityBadge} ${priorityClass[instance.priority] ?? ''}`}
          >
            {instance.priority}
          </span>
          <div
            className={`${styles.connIndicator} ${connected ? styles.connConnected : styles.connDisconnected}`}
            title={connected ? 'WebSocket connected' : 'WebSocket disconnected'}
          />
          <div className={styles.spacer} />
          <InstanceControls
            instanceId={instance.id}
            state={instance.state}
            onAction={refreshAll}
          />
        </div>

        {/* Main content: graph + side panel */}
        <ResizablePanel
          left={
            <InstanceArrangement
              key={`${instance.id}:${workflowDef?.presentation?.revision ?? ''}`}
              instance={instance}
              workflowDef={workflowDef}
              trace={trace}
              graphState={graph.state}
              onSelect={handleSelect}
              selectedNodeId={selection?.rfId ?? null}
            />
          }
          right={right}
          rightWidthFraction={routeSessionVisible || linkedActive ? 0.6 : undefined}
        />

        {/* Connector line from interactive node to panel border */}
        {calloutNodeId && (
          <CalloutConnector
            nodeId={calloutNodeId}
            visible={routeSessionVisible || linkedActive}
            instanceId={calloutInstanceId}
          />
        )}
      </div>
    </ReactFlowProvider>
  )
}
