import { ReactFlowProvider } from '@xyflow/react'
import { flushSync } from 'react-dom'
import type { Root } from 'react-dom/client'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import type { Geometry, LayoutView, RendererContract } from '../graph/arranged/types'
import type { NodeExecutionState, WorkflowDefinition } from '../graph/types'
import { RENDERER_CONTRACT, resolveDisplayGeometry } from '../graph/arranged/validate'
import { parseJob, workflowForJob, RUNTIME_STATES } from './job'
import type { RenderJob, RenderReport, StateProbe } from './job'
import { collectCrossings, collectEdges, collectNodes, ensureFonts, nextFrame, readabilityIssues, settle, stabilityIssues, waitFor } from './report'
import type { CollectContext } from './report'
import { collectGroupElements, collectGroupConnectors, collectGroupCrossings } from './report'
import { geometryTexts } from '../graph/arranged/groupVocabulary'
import type { GroupLiveSummary, NodeContextInfo } from '../graph/groupLayout'
import { buildGroupProbeCases } from './groupProbes'
import { collectGroupRows, groupProbeIssues, groupRowIssues } from './groupReport'

export const SURFACE_CONTRACT: RendererContract = RENDERER_CONTRACT
export const READY_TIMEOUT_MS = 20_000
export interface SurfaceProbe { executionStates?: Record<string, NodeExecutionState>; selectedNodeIds?: ReadonlySet<string>; groupLive?: Record<string, GroupLiveSummary>; nodeContexts?: Record<string, NodeContextInfo> }
export interface RenderSurfaceProps { workflowDef: WorkflowDefinition; view: LayoutView; probe: SurfaceProbe }
const noop = () => {}

export function buildSurfaceModel(job: RenderJob): { workflowDef: WorkflowDefinition; view: LayoutView } {
  return { workflowDef: workflowForJob(job), view: {
    savedRevision: job.revision, jobGeneration: 0, resultGeneration: 0, displayStatus: 'reviewing',
    attemptsUsed: job.attempt, attemptLimit: job.attempt, findings: [], contract: job.geometry.contract, geometry: job.geometry,
    provenance: 'arranged', unreviewed: true, retryEligible: false, available: true,
    ...(job.workflowGroups ? { groups: structuredClone(job.workflowGroups) } : {}),
  } }
}
export function probeFor(state: string, nodeIds: string[]): SurfaceProbe {
  if (state === 'selected') return { selectedNodeIds: new Set(nodeIds) }
  if (!RUNTIME_STATES.includes(state as typeof RUNTIME_STATES[number])) throw new Error(`render surface: unknown runtime state ${state}`)
  return { executionStates: Object.fromEntries(nodeIds.map(id => [id, state as NodeExecutionState])) }
}
export function RenderSurface({ workflowDef, view, probe }: RenderSurfaceProps) {
  const geo = view.geometry as Geometry, zoom = geo.contract.practicalZoom
  return <div data-render-root="true" style={{ position: 'relative', width: geo.canvas.w * zoom, height: geo.canvas.h * zoom, overflow: 'hidden', background: 'var(--bg-primary)' }}>
    <ReactFlowProvider><ArrangedGraph workflowDef={workflowDef} view={view} executionStates={probe.executionStates}
      groupLive={probe.groupLive} nodeContexts={probe.nodeContexts}
      selectedNodeId={null} selectedNodeIds={probe.selectedNodeIds} onNodeClick={noop} anchorNodeId={null}
      fixedViewport={{ x: -geo.canvas.x * zoom, y: -geo.canvas.y * zoom, zoom }} /></ReactFlowProvider>
  </div>
}
function exact(values: (string | null)[], wanted: string[]): boolean {
  return JSON.stringify(values.sort()) === JSON.stringify([...wanted].sort())
}
function everythingDrawn(doc: Document, geo: Geometry): boolean {
  return exact([...doc.querySelectorAll('[data-arranged-group-element]')].map(el => el.getAttribute('data-arranged-group-element')), (geo.groupElements ?? []).map(e => e.id)) &&
    exact([...doc.querySelectorAll('path[data-group-connector]')].map(el => el.getAttribute('data-group-connector')), (geo.groupConnectors ?? []).map(c => c.id)) &&
    (geo.groupElements ?? []).every(e => e.texts.every(t => doc.querySelectorAll(`[data-group-text="${CSS.escape(t.key)}"]`).length === 1)) &&
    doc.querySelector('[data-layout-mode="arranged"]') != null &&
    exact([...doc.querySelectorAll('[data-arranged-node]')].map(el => el.getAttribute('data-arranged-node')), geo.nodes.map(n => n.id)) &&
    exact([...doc.querySelectorAll('path[data-edge-path]')].map(el => el.getAttribute('data-edge-path')), geo.edges.map(e => String(e.ordinal))) &&
    exact([...doc.querySelectorAll('[data-edge-label]')].map(el => el.getAttribute('data-edge-label')), geo.edges.filter(e => e.text).map(e => String(e.ordinal))) &&
    geo.nodes.every(n => {
      const node = [...doc.querySelectorAll('[data-arranged-node]')].find(el => el.getAttribute('data-arranged-node') === n.id)!
      return node.querySelectorAll('[data-node-text="label"]').length === 1 && node.querySelectorAll('[data-node-text="type"]').length === 1
    })
}

function freezeAnimations(doc: Document): void {
  if (typeof doc.getAnimations !== 'function') throw new Error('render surface: Web Animations API unavailable')
  for (const animation of doc.getAnimations()) { animation.pause(); animation.currentTime = 0 }
}

/** Sample all declared keyframe extrema plus intermediate dash phases, independently of wall time. */
async function animationPhases(doc: Document, win: Window, collect: () => void): Promise<void> {
  if (typeof doc.getAnimations !== 'function') throw new Error('render surface: Web Animations API unavailable')
  const animations = doc.getAnimations()
  const fractions = new Set([0, 0.25, 0.5, 0.75, 1])
  for (const a of animations) {
    for (const frame of (a.effect as KeyframeEffect | null)?.getKeyframes() ?? []) if (frame.computedOffset != null) fractions.add(frame.computedOffset)
    a.pause()
  }
  for (const fraction of [...fractions].sort()) {
    for (const animation of animations) {
      const timing = animation.effect?.getComputedTiming(), duration = timing?.duration
      if (typeof duration !== 'number' || !Number.isFinite(duration) || duration <= 0) throw new Error('render surface: unsupported animation timing')
      animation.currentTime = Number(timing?.delay ?? 0) + duration * fraction
    }
    await nextFrame(win)
    collect()
  }
}
export async function runRenderJob(job: RenderJob, root: Root, doc: Document, win: Window): Promise<RenderReport> {
  const { workflowDef, view } = buildSurfaceModel(job)
  const display = resolveDisplayGeometry(workflowDef, view)
  if (!display.geometry || display.issues.length) throw new Error(`shared renderer rejects this geometry: ${display.issues.join('; ')}`)
  parseJob(job)
  const geo = job.geometry, zoom = geo.contract.practicalZoom, texts = geometryTexts(geo, job.workflowGroups)
  const fonts = await ensureFonts(doc, RENDERER_CONTRACT, texts)
  if (!fonts.fontsReady || !fonts.fontFamilyResolved) throw new Error('render surface: bundled fonts unavailable')
  const mount = (probe: SurfaceProbe) => flushSync(() => root.render(<RenderSurface workflowDef={workflowDef} view={view} probe={probe} />))
  mount({})
  if (!await waitFor(() => everythingDrawn(doc, geo), win, READY_TIMEOUT_MS)) throw new Error('render surface: exact DOM coverage did not become ready')
  await settle(doc, win)
  const el = doc.querySelector('[data-render-root]')!
  const ctx: CollectContext = { doc, win, root: el, zoom, origin: geo.canvas }
  const collect = () => {
    if (!everythingDrawn(doc, geo)) throw new Error('render surface: DOM coverage changed')
    return { nodes: collectNodes(geo, ctx), edges: collectEdges(geo, ctx), crossings: collectCrossings(geo, ctx),
      ...(geo.groupElements?.length ? { groupElements: collectGroupElements(geo, ctx), groupConnectors: collectGroupConnectors(geo, ctx), groupCrossings: collectGroupCrossings(geo, ctx) } : {}) }
  }
  const baseline = collect(), baselineRows = collectGroupRows(ctx), ids = geo.nodes.map(n => n.id)
  const states: StateProbe[] = []
  doc.documentElement.dataset.renderProbing = 'true'
  try {
    for (const state of job.states) {
      mount(probeFor(state, ids))
      freezeAnimations(doc)
      await settle(doc, win)
      const stability = new Set<string>(), readability = new Set<string>()
      await animationPhases(doc, win, () => {
        const now = collect()
        stabilityIssues(baseline, now).forEach(i => stability.add(i))
        readabilityIssues(now.nodes, now.edges, geo.contract, zoom).forEach(i => readability.add(i))
        if (job.workflowGroups) {
          const group = groupProbeIssues(geo, job.workflowGroups, baseline, now)
          group.stability.forEach(i => stability.add(i))
          group.readability.forEach(i => readability.add(i))
        }
      })
      states.push({ state, nodeBoxesStable: !stability.size, labelsReadable: !readability.size, issues: [...stability, ...readability] })
    }
    mount({ ...probeFor('running', ids), ...probeFor('selected', ids) })
    freezeAnimations(doc)
    await settle(doc, win)
    await animationPhases(doc, win, () => {
      const now = collect(), changed = stabilityIssues(baseline, now), unreadable = readabilityIssues(now.nodes, now.edges, geo.contract, zoom)
      for (const state of states.filter(s => s.state === 'running' || s.state === 'selected')) {
        state.nodeBoxesStable &&= !changed.length
        state.labelsReadable &&= !unreadable.length
        state.issues = [...new Set([...(state.issues ?? []), ...changed, ...unreadable])]
      }
    })
    for (const probe of job.workflowGroups ? buildGroupProbeCases(job.workflowGroups) : []) {
      mount({ ...probe, ...(probe.name === 'group:selected-running' ? probeFor('running', ids) : {}) })
      freezeAnimations(doc)
      await settle(doc, win)
      const stability = new Set<string>(), readability = new Set<string>()
      await animationPhases(doc, win, () => {
        const now = collect(), group = groupProbeIssues(geo, job.workflowGroups!, baseline, now, probe)
        stabilityIssues(baseline, now).forEach(i => stability.add(i))
        readabilityIssues(now.nodes, now.edges, geo.contract, zoom).forEach(i => readability.add(i))
        group.stability.forEach(i => stability.add(i))
        group.readability.forEach(i => readability.add(i))
        groupRowIssues(baselineRows, collectGroupRows(ctx), now.groupElements ?? []).forEach(i => readability.add(i))
      })
      states.push({ state: probe.name, nodeBoxesStable: !stability.size, labelsReadable: !readability.size, issues: [...stability, ...readability] })
    }
  } finally {
    delete doc.documentElement.dataset.renderProbing
    mount({})
  }
  freezeAnimations(doc)
  const finalFonts = await ensureFonts(doc, RENDERER_CONTRACT, texts)
  await settle(doc, win)
  // This is the restored, settled baseline the browser will capture, not an earlier probe.
  const final = collect()
  return { surfaceVersion: RENDERER_CONTRACT.version, contract: RENDERER_CONTRACT, ...finalFonts, zoom, canvas: geo.canvas, ...final, states }
}
