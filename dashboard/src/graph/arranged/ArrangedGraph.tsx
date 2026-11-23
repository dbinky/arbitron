import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Background, Controls, ReactFlow, useReactFlow } from '@xyflow/react'
import type { Node } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ensureBundledTextFonts } from './fonts'
import fontCoverage from './fontCoverage.json'
import { geometryTexts } from './groupVocabulary'
import type { NodeExecutionState, WorkflowDefinition, WorkflowEdge, WorkflowNode } from '../types'
import type { GroupGraph } from '../../api/types'
import { definitionNodeIdOf, groupHeaderId } from '../groupLayout'
import type { GroupLiveSummary, NodeContextInfo } from '../groupLayout'
import type { LayoutView } from './types'
import { resolveDisplayGeometry } from './validate'
import { arrangedEdgeTypes, arrangedNodeTypes, buildArrangedFlow } from './buildArrangedFlow'
import { fallbackPositions } from './fallbackLayout'
import { composeArrangedTiles, type LayoutTile } from './composeArrangedTiles'
import { NODE_HEIGHT, NODE_WIDTH, useGraphLayout } from '../useGraphLayout'
import { nodeTypes as legacyNodeTypes } from '../nodes/nodeTypes'
import { edgeTypes as legacyEdgeTypes } from '../edges/edgeTypes'
import { DEFAULT_ZOOM } from '../constants'
import styles from './ArrangedGraph.module.css'
import { applyRunnerMarksByOwner, type RunnerMarksByOwner } from '../runnerMarks'

export interface ArrangedGraphProps {
  runnerMarksByOwner?: RunnerMarksByOwner
  rootInstanceId?: string
  workflowDef: WorkflowDefinition
  view: LayoutView | null
  executionStates?: Record<string, NodeExecutionState>
  selectedNodeId: string | null
  selectedNodeIds?: ReadonlySet<string>
  onNodeClick: (nodeId: string, node?: Node) => void
  onDisplayNodes?: (nodes: Node[]) => void
  onTileDisplayIssues?: (issues: ReadonlyMap<string, readonly string[]>) => void
  tiles?: readonly LayoutTile[]
  anchorNodeId: string | null
  onMoveStart?: (event: unknown) => void
  onDisplayIssues?: (issues: readonly string[]) => void
  onAnchorPosition?: (center: { x: number; y: number } | null) => void
  /** Render surface: pinned viewport, no navigation chrome or automatic centring. */
  fixedViewport?: { x: number; y: number; zoom: number }
  /** Frozen ownership for the fallback; reviewed geometry always uses view.groups. */
  groups?: GroupGraph | null
  groupLive?: Record<string, GroupLiveSummary>
  nodeContexts?: Record<string, NodeContextInfo>
  children?: ReactNode
}

const OVERLAY_ISSUE = 'Client group overlay exceeds reviewed reservation'
const EMPTY_STATES: Record<string, NodeExecutionState> = {}
const NO_NODES: WorkflowNode[] = []
const NO_EDGES: WorkflowEdge[] = []
const sceneNodeTypes = { ...legacyNodeTypes, ...arrangedNodeTypes }
const sceneEdgeTypes = { ...legacyEdgeTypes, ...arrangedEdgeTypes }

/** Centre of the displayed payload node, or the actual fallback node and its size. */
export function nodeCenter(
  view: LayoutView | null,
  rfNodes: Node[],
  nodeId: string,
): { x: number; y: number } | null {
  const box = view?.geometry?.nodes.find(n => n.id === nodeId)?.box
  if (box) return { x: box.x + box.w / 2, y: box.y + box.h / 2 }
  const n = rfNodes.find(n => n.id === nodeId)
  return n ? {
    x: n.position.x + (n.width ?? NODE_WIDTH) / 2,
    y: n.position.y + (n.height ?? NODE_HEIGHT) / 2,
  } : null
}

/** Shared renderer. Its parent supplies ReactFlowProvider so follow controls share the viewport. */
export default function ArrangedGraph({
  workflowDef, view, executionStates = EMPTY_STATES, selectedNodeId, selectedNodeIds,
  onNodeClick, anchorNodeId, onMoveStart, onDisplayIssues, onAnchorPosition, fixedViewport, children,
  groups = null, groupLive, nodeContexts, tiles, onDisplayNodes, onTileDisplayIssues, runnerMarksByOwner, rootInstanceId = '',
}: ArrangedGraphProps) {
  const reactFlow = useReactFlow()
  const graphInstanceId = `graph-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
  const groupFallback = (groups?.order.length ?? 0) > 0 && workflowDef.nodes.some(n => n.type === 'parallel_group')
  const display = useMemo(() => {
    const result = resolveDisplayGeometry(workflowDef, view)
    if (result.geometry && workflowDef.nodes.some(n => !n.type)) {
      return { geometry: null, issues: [...result.issues, 'Manifest node type is missing'] }
    }
    return result
  }, [workflowDef, view])
  const { geometry } = display

  // Revision, exact geometry and bundled-asset epochs fence every async font result.
  const fontKey = JSON.stringify([view?.savedRevision, geometry, fontCoverage.fingerprint])
  const overlayEpochs = useRef<ReadonlyMap<string, string>>(new Map())
  const [overlayFailures, setOverlayFailures] = useState<ReadonlyMap<string, string>>(new Map())
  const failOverlay = useCallback((id: string, key: string) => {
    if (overlayEpochs.current.get(id) !== key) return
    setOverlayFailures(previous => {
      if (previous.get(id) === key) return previous
      const next = new Map(previous); next.set(id, key); return next
    })
  }, [])
  const rootOverlayOverflow = useCallback(() => failOverlay('', fontKey), [failOverlay, fontKey])
  const rootOverlayFailed = overlayFailures.get('') === fontKey
  const [fontState, setFontState] = useState<{ key: string; ready: boolean; issues: string[] } | null>(null)
  useEffect(() => {
    if (!geometry) return
    let cancelled = false
    const texts = geometryTexts(geometry, view?.groups)
    ensureBundledTextFonts(document, texts, geometry.contract).then(result => {
      if (cancelled) return
      const ready = result.fontsReady && result.fontFamilyResolved
      setFontState({ key: fontKey, ready, issues: ready ? [] : ['Reviewed fonts could not be loaded'] })
    }).catch(error => {
      if (!cancelled) setFontState({ key: fontKey, ready: false, issues: [String(error)] })
    })
    return () => { cancelled = true }
  }, [geometry, fontKey, view?.groups])
  const awaitingFonts = geometry != null && fontState?.key !== fontKey
  const arranged = geometry != null && fontState?.key === fontKey && fontState.ready && !rootOverlayFailed
  const issues = useMemo(() => {
    const fontIssues = awaitingFonts
      ? ['Loading reviewed fonts']
      : geometry && !arranged ? fontState?.issues ?? [] : []
    return [...display.issues, ...fontIssues, ...(rootOverlayFailed ? [OVERLAY_ISSUE] : [])]
  }, [display.issues, awaitingFonts, geometry, arranged, fontState, rootOverlayFailed])
  useEffect(() => { onDisplayIssues?.(issues) }, [issues, onDisplayIssues])

  const nodeTypesById = useMemo(
    () => Object.fromEntries(workflowDef.nodes.map(n => [n.id, n.type])),
    [workflowDef.nodes],
  )
  const activeSources = useMemo(
    () => new Set(Object.keys(executionStates).filter(id => executionStates[id] === 'running')),
    [executionStates],
  )
  const childInputs = useMemo(() => (tiles ?? []).slice(1).map(t => {
    const display = resolveDisplayGeometry(t.definition, t.view)
    return { tile: t, ...display, key: JSON.stringify([t.instanceId, t.view?.savedRevision, display.geometry, t.view?.groups, fontCoverage.fingerprint]) }
  }), [tiles])
  overlayEpochs.current = new Map([['', fontKey], ...childInputs.map(input => [input.tile.instanceId, input.key] as const)])
  const childOverlayOverflow = useMemo(() => new Map(childInputs.map(input => [input.tile.instanceId,
    () => failOverlay(input.tile.instanceId, input.key)])), [childInputs, failOverlay])
  type ChildFonts = { key: string; ready: boolean; issues: readonly string[] }
  const [childFonts, setChildFonts] = useState<ReadonlyMap<string, ChildFonts>>(new Map())
  const fontLoads = useRef(new Map<string, Promise<Omit<ChildFonts, 'key'>>>())
  useEffect(() => {
    let cancelled = false
    const wanted = new Set(childInputs.map(t => t.key))
    for (const key of fontLoads.current.keys()) if (!wanted.has(key)) fontLoads.current.delete(key)
    for (const { tile, geometry: geo, key } of childInputs) {
      if (!geo) continue
      let load = fontLoads.current.get(key)
      if (!load) {
        load = ensureBundledTextFonts(document, geometryTexts(geo, tile.view?.groups), geo.contract)
          .then(result => {
            const ready = result.fontsReady && result.fontFamilyResolved
            return { ready, issues: ready ? [] : ['Reviewed fonts could not be loaded'] }
          }).catch(() => ({ ready: false, issues: ['Reviewed fonts could not be loaded'] }))
        fontLoads.current.set(key, load)
      }
      void load.then(result => {
        if (!cancelled) setChildFonts(previous => {
          if (previous.get(tile.instanceId)?.key === key) return previous
          const next = new Map(previous); next.set(tile.instanceId, { key, ...result }); return next
        })
      })
    }
    return () => { cancelled = true }
  }, [childInputs])
  const tileIssues = useMemo(() => new Map(childInputs.map(input => {
    const fonts = childFonts.get(input.tile.instanceId)
    const fontIssues = !input.geometry ? [] : fonts?.key === input.key ? fonts.issues : ['Loading reviewed fonts']
    return [input.tile.instanceId, [...input.issues, ...fontIssues, ...(overlayFailures.get(input.tile.instanceId) === input.key ? [OVERLAY_ISSUE] : [])]] as const
  })), [childInputs, childFonts, overlayFailures])
  useEffect(() => { onTileDisplayIssues?.(tileIssues) }, [tileIssues, onTileDisplayIssues])
  const scene = useMemo(() => tiles ? composeArrangedTiles(tiles.map((t, i) => ({ ...t,
    onOverlayOverflow: i === 0 ? rootOverlayOverflow : childOverlayOverflow.get(t.instanceId),
    view: (i === 0 ? arranged : childFonts.get(t.instanceId)?.key === childInputs[i - 1].key && childFonts.get(t.instanceId)?.ready && overlayFailures.get(t.instanceId) !== childInputs[i - 1].key) ? t.view : null,
  })), graphInstanceId) : null, [tiles, arranged, childFonts, childInputs, graphInstanceId, rootOverlayOverflow, childOverlayOverflow, overlayFailures])
  const arrangedFlow = useMemo(
    () => geometry && arranged && !tiles
      ? buildArrangedFlow(geometry, nodeTypesById, executionStates, graphInstanceId, activeSources, { groups: view?.groups ?? null, groupLive, nodeContexts, onOverlayOverflow: rootOverlayOverflow })
      : null,
    [geometry, arranged, nodeTypesById, executionStates, graphInstanceId, activeSources, view?.groups, groupLive, nodeContexts, tiles, rootOverlayOverflow],
  )

  const sameRevision = view?.savedRevision === (workflowDef.presentation?.revision ?? '')
  const retainedPositions = sameRevision ? view?.retained?.positions : undefined
  const positions = useMemo(
    () => arranged ? null : fallbackPositions(workflowDef.nodes, retainedPositions),
    [arranged, workflowDef.nodes, retainedPositions],
  )
  // Hooks stay unconditional across fallback/loading/arranged transitions.
  const legacy = useGraphLayout({
    nodes: arranged || tiles ? NO_NODES : workflowDef.nodes,
    edges: arranged || tiles ? NO_EDGES : workflowDef.edges,
    executionStates,
    positions: positions ?? undefined,
    groups: groupFallback ? groups : null,
    groupLive, nodeContexts,
  })
  const base = useMemo(() => {
    const nodes = scene?.nodes ?? arrangedFlow?.nodes ?? legacy.rfNodes
    const edges = scene?.edges ?? arrangedFlow?.edges ?? legacy.rfEdges
    return runnerMarksByOwner ? applyRunnerMarksByOwner(nodes, edges, runnerMarksByOwner, rootInstanceId) : { nodes, edges }
  }, [scene, arrangedFlow, legacy.rfNodes, legacy.rfEdges, runnerMarksByOwner, rootInstanceId])
  const baseNodes: Node[] = base.nodes
  const edges = base.edges
  useEffect(() => { onDisplayNodes?.(baseNodes) }, [baseNodes, onDisplayNodes])
  // Group parts (header, footer, task cards) select their definition node;
  // the container box is never highlighted.
  const nodes = useMemo(() => {
    const picked = baseNodes.find(p => p.id === selectedNodeId)
    return baseNodes.map(n => {
      const defId = n.type === 'groupContainer' ? null : definitionNodeIdOf(n)
      const selected = n.id === selectedNodeId || (defId != null && (tiles
        ? picked != null && picked.data.layoutInstanceId === n.data.layoutInstanceId && definitionNodeIdOf(picked) === defId
        : defId === selectedNodeId || (selectedNodeIds?.has(defId) ?? false)))
      return n.selected === selected ? n : { ...n, selected }
    })
  }, [baseNodes, selectedNodeId, selectedNodeIds, tiles])
  // A group node never renders itself in the fallback layout: its header stands in.
  const anchorRfId = anchorNodeId && !arranged && groupFallback && groups?.groups[anchorNodeId]
    ? groupHeaderId(anchorNodeId)
    : anchorNodeId
  const sceneAnchor = tiles && anchorRfId ? nodes.find(n => n.id === anchorRfId || (n.data.layoutInstanceId === tiles[0]?.instanceId && definitionNodeIdOf(n) === anchorRfId && n.type !== 'groupContainer')) : null
  const absolutePosition = (n: Node): { x: number; y: number } => {
    const parent = n.parentId ? nodes.find(p => p.id === n.parentId) : null
    const p = parent ? absolutePosition(parent) : { x: 0, y: 0 }
    return { x: p.x + n.position.x, y: p.y + n.position.y }
  }
  const anchor = !awaitingFonts && sceneAnchor
    ? { x: absolutePosition(sceneAnchor).x + (sceneAnchor.width ?? NODE_WIDTH) / 2, y: absolutePosition(sceneAnchor).y + (sceneAnchor.height ?? NODE_HEIGHT) / 2 }
    : !awaitingFonts && anchorRfId ? nodeCenter(arranged ? view : null, nodes, anchorRfId) : null
  const anchorX = anchor?.x, anchorY = anchor?.y
  useEffect(() => {
    onAnchorPosition?.(anchorX != null && anchorY != null ? { x: anchorX, y: anchorY } : null)
  }, [anchorX, anchorY, onAnchorPosition])

  const layoutKey = arranged && view ? `arranged:${view.savedRevision}:${view.resultGeneration}`
    : `fallback:${workflowDef.presentation?.revision ?? ''}:${workflowDef.name}`
  const centredFor = useRef<string | null>(null)
  useEffect(() => {
    if (fixedViewport || anchorX == null || anchorY == null || centredFor.current === layoutKey) return
    centredFor.current = layoutKey
    reactFlow.setCenter(anchorX, anchorY, { zoom: DEFAULT_ZOOM })
  }, [anchorX, anchorY, layoutKey, fixedViewport, reactFlow])

  if (awaitingFonts) {
    return (
      <div data-arranged-canvas="true" data-layout-mode="loading" className={styles.canvas}>
        Loading reviewed fonts…
      </div>
    )
  }
  const fixed = fixedViewport != null
  // Failed CSS font faces stay failed for this document, so recovery is a fresh
  // page/font epoch. A passed row stays non-retryable on the server (G8).
  const fontsFailed = !fixed && ((geometry != null && !arranged) || childInputs.some(t => childFonts.get(t.tile.instanceId)?.key === t.key && childFonts.get(t.tile.instanceId)?.ready === false))
  return (
    <div data-arranged-canvas="true" data-layout-mode={arranged ? 'arranged' : 'fallback'} className={styles.canvas}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={tiles ? sceneNodeTypes : arranged ? arrangedNodeTypes : legacyNodeTypes}
        edgeTypes={tiles ? sceneEdgeTypes : arranged ? arrangedEdgeTypes : legacyEdgeTypes}
        onNodeClick={(_event, node) => onNodeClick(definitionNodeIdOf(node), node)}
        onMoveStart={onMoveStart ? event => onMoveStart(event) : undefined}
        zIndexMode={arranged || tiles ? 'manual' : 'basic'}
        nodesDraggable={false}
        nodesConnectable={false}
        edgesFocusable={false}
        minZoom={0.2}
        maxZoom={2}
        defaultViewport={fixedViewport}
        viewport={fixedViewport}
        panOnDrag={!fixed}
        zoomOnScroll={!fixed}
        zoomOnPinch={!fixed}
        zoomOnDoubleClick={!fixed}
        proOptions={{ hideAttribution: true }}
      >
        {!fixed && <Background color="#2a2a4a" gap={20} size={1} />}
        {!fixed && <Controls showInteractive={false} />}
        {!fixed && (
          <button
            type="button"
            className={styles.fitButton}
            aria-label="Fit whole workflow"
            title="Fit whole workflow"
            onClick={() => reactFlow.fitView({ padding: 0.1 })}
          >
            Fit all
          </button>
        )}
        {fontsFailed && (
          <button
            type="button"
            className={styles.fontReloadButton}
            onClick={() => window.location.reload()}
          >
            Reload reviewed fonts
          </button>
        )}
        {children}
      </ReactFlow>
    </div>
  )
}
