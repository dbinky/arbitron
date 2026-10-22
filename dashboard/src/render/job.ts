// Page protocol between the Go browser renderer (adapter/renderreview) and the
// render surface: the job document, the result shapes (mirroring domain/layout
// JSON exactly) and the window globals the renderer polls.
import type { GroupGraph } from '../api/types'
import type { Geometry, Point, Rect, RendererContract } from '../graph/arranged/types'
import type { WorkflowDefinition } from '../graph/types'
import { RENDERER_CONTRACT, validateGeometry } from '../graph/arranged/validate'

export type TextStyle = 'node_label' | 'node_type' | 'edge_label'
export interface TextItem { key: string; text: string; style: TextStyle; maxWidth: number }
export interface MeasureRequest { contract: RendererContract; items: TextItem[] }
export interface TextMetrics { width: number; height: number; lines: number }
export interface Measurements { contract: RendererContract; fontsReady: boolean; items: Record<string, TextMetrics> }

export interface RenderedText { content: string; glyphs: Rect[]; visible: boolean; box: Rect; clipped: boolean; fontPx: number; contrast: number; occluded: boolean }
export interface RenderedNode { id: string; box: Rect; label: RenderedText; type: RenderedText }
export interface RenderedSegment { start: Point; end: Point }
export interface RenderedEdge { segments: RenderedSegment[]; strokeColor: string; dashPattern: string; visible: boolean; arrowPoints: Point[]; ordinal: number; start: Point; end: Point; hasArrow: boolean; dashed: boolean; strokePx: number; label?: RenderedText }
export interface RenderedCrossing { point: Point; over: number; under: number; underGapVisible: boolean; overContinuous: boolean }
export interface RenderedGroupText { key: string; text: RenderedText }
export interface RenderedGroupElement { id: string; box: Rect; texts: RenderedGroupText[]; visible: boolean; borderColor: string; borderPx: number; backgroundColor: string }
export interface RenderedGroupConnector { id: string; segments: RenderedSegment[]; strokeColor: string; dashPattern: string; visible: boolean; arrowPoints: Point[]; start: Point; end: Point; hasArrow: boolean; strokePx: number }
export interface RenderedGroupCrossing { point: Point; over: string; under: string; underGapVisible: boolean; overContinuous: boolean }
export interface StateProbe { state: string; nodeBoxesStable: boolean; labelsReadable: boolean; issues?: string[] }
export interface RenderReport {
  surfaceVersion: string
  contract: RendererContract
  fontsReady: boolean
  fontFamilyResolved: boolean
  zoom: number
  canvas: Rect
  nodes: RenderedNode[]
  edges: RenderedEdge[]
  crossings: RenderedCrossing[]
  states: StateProbe[]
  groupElements?: RenderedGroupElement[]
  groupConnectors?: RenderedGroupConnector[]
  groupCrossings?: RenderedGroupCrossing[]
}

export interface RenderJob { mode: 'render'; revision: string; attempt: number; geometry: Geometry; workflowNodeTypes: Record<string, string>; workflowGroups?: GroupGraph; states: string[] }
export interface MeasureJob { mode: 'measure'; request: MeasureRequest }
export type SurfaceJob = RenderJob | MeasureJob

export type SurfaceState = 'loading' | 'ready' | 'error'

declare global {
  interface Window {
    __ARBITRON_RENDER_STATE__?: SurfaceState
    __ARBITRON_RENDER_ERROR__?: string
    __ARBITRON_RENDER_REPORT__?: RenderReport
    __ARBITRON_MEASURE_RESULT__?: Measurements
  }
}

/** Mirrors domain/layout.RuntimeStates. */
export const RUNTIME_STATES = ['pending', 'running', 'completed', 'failed', 'waiting', 'skipped', 'selected'] as const
const TEXT_STYLES: readonly TextStyle[] = ['node_label', 'node_type', 'edge_label']
const TOKEN_RE = /^[0-9a-f]{32}$/

export class JobError extends Error {}

/** Reads and validates ?job=<token> (32 lowercase hex chars, as issued by the Go asset server). */
export function jobTokenFrom(search: string): string {
  const token = new URLSearchParams(search).get('job')
  if (!token) throw new JobError('render surface: missing ?job= token')
  if (!TOKEN_RE.test(token)) throw new JobError('render surface: malformed job token')
  return token
}

export function jobUrl(token: string): string {
  return `/__render/jobs/${token}.json`
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function finite(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

// Go marshals nil slices/maps as null; normalise them.
function arrayOrEmpty(v: unknown, what: string): unknown[] {
  if (v === null || v === undefined) return []
  if (!Array.isArray(v)) throw new JobError(`render job: ${what} must be an array`)
  return v
}

function requireContract(raw: unknown): asserts raw is RendererContract {
  if (!isObject(raw) || Object.keys(raw).length !== Object.keys(RENDERER_CONTRACT).length ||
      Object.entries(RENDERER_CONTRACT).some(([k, v]) => raw[k] !== v)) {
    throw new JobError('render job: contract does not match bundled renderer (including practicalZoom)')
  }
}

/** Validate the captured ownership's identities, not a second reachability analysis. */
function groupsForJob(job: RenderJob): GroupGraph | undefined {
  const ids = job.geometry.nodes.map(n => n.id)
  const groupIds = ids.filter(id => job.workflowNodeTypes[id] === 'parallel_group')
  const raw: unknown = job.workflowGroups
  const fail = (): never => { throw new JobError('render job: missing or mismatched group ownership') }
  if (!groupIds.length) {
    if (raw != null) fail()
    return undefined
  }
  if (!isObject(raw) || !isObject(raw.groups) || !isObject(raw.owner) || !Array.isArray(raw.order) ||
      JSON.stringify(raw.order) !== JSON.stringify(groupIds) || Object.keys(raw.groups).length !== groupIds.length) return fail()
  const groups = raw as unknown as GroupGraph
  const expectedOwner = new Map<string, GroupGraph['owner'][string]>()
  for (const id of groupIds) {
    const scope = groups.groups[id]
    if (!isObject(scope) || scope.nodeId !== id || !isObject(scope.config) || !isObject(scope.bodies) || !Array.isArray(scope.nested)) return fail()
    const cfg = scope.config
    if (!['branches', 'workflows'].includes(cfg.form) || !['all_succeed', 'first_success'].includes(cfg.completionRule) ||
        !['wait_for_remaining', 'cancel_unfinished', 'finish_independently'].includes(cfg.unfinishedBehavior) ||
        !['results', 'results_and_files'].includes(cfg.gatherMode) || !Array.isArray(cfg.tasks) || !cfg.tasks.length) return fail()
    const keys = new Set<string>()
    for (const task of cfg.tasks) {
      if (!isObject(task) || typeof task.key !== 'string' || !task.key.trim() || keys.has(task.key)) return fail()
      keys.add(task.key)
      if (cfg.form === 'workflows') {
        if (typeof task.workflow !== 'string' || !task.workflow.trim() || task.entry) return fail()
      } else {
        if (typeof task.entry !== 'string' || !ids.includes(task.entry) || task.workflow) return fail()
        const body = scope.bodies[task.key]
        if (!Array.isArray(body) || !body.includes(task.entry) || body.includes(id) ||
            JSON.stringify(ids.filter(n => body.includes(n))) !== JSON.stringify(body)) return fail()
        for (const node of body) {
          if (expectedOwner.has(node)) return fail()
          expectedOwner.set(node, { groupNodeId: id, taskKey: task.key })
        }
      }
    }
    if (cfg.form === 'workflows' ? Object.keys(scope.bodies).length !== 0 :
        Object.keys(scope.bodies).length !== keys.size || Object.keys(scope.bodies).some(k => !keys.has(k))) return fail()
    for (const [port, target] of [['success', scope.successTarget], ['failure', scope.failureTarget]]) {
      const edges = job.geometry.edges.filter(e => e.source === id && e.output === port)
      if (edges.length !== 1 || edges[0].target !== target) return fail()
    }
    const taskEdges = job.geometry.edges.filter(e => e.source === id && e.output === 'task')
    if (cfg.form === 'workflows' ? taskEdges.length !== 0 : taskEdges.length !== cfg.tasks.length ||
        cfg.tasks.some(t => taskEdges.filter(e => e.target === t.entry).length !== 1)) return fail()
  }
  const ownerKeys = Object.keys(groups.owner)
  if (ownerKeys.length !== expectedOwner.size || ownerKeys.some(id => {
    const owner = groups.owner[id], expected = expectedOwner.get(id)
    return !isObject(owner) || !expected || owner.groupNodeId !== expected.groupNodeId || owner.taskKey !== expected.taskKey
  })) return fail()
  for (const id of groupIds) {
    const scope = groups.groups[id], parent = expectedOwner.get(id)
    if (parent ? !isObject(scope.parent) || scope.parent.groupNodeId !== parent.groupNodeId || scope.parent.taskKey !== parent.taskKey : scope.parent != null) return fail()
    if (JSON.stringify(scope.nested) !== JSON.stringify(groupIds.filter(n => expectedOwner.get(n)?.groupNodeId === id))) return fail()
    const seen = new Set<string>([id])
    let ancestor = parent?.groupNodeId
    while (ancestor) {
      if (seen.has(ancestor)) return fail()
      seen.add(ancestor)
      ancestor = expectedOwner.get(ancestor)?.groupNodeId
    }
  }
  return groups
}

/** The manifest types/configs come from the immutable Go job, never captions. */
export function workflowForJob(job: RenderJob): WorkflowDefinition {
  const groups = groupsForJob(job)
  return {
    name: 'render-surface', description: '', author: '',
    presentation: { revision: job.revision, savedAt: '1970-01-01T00:00:00Z', reason: 'render', scope: 'private', name: 'render-surface', retained: { source: 'none' } },
    nodes: job.geometry.nodes.map(n => {
      const type = job.workflowNodeTypes[n.id]
      if (!Object.prototype.hasOwnProperty.call(job.workflowNodeTypes, n.id) || typeof type !== 'string' || !type.trim()) throw new JobError(`render job: missing node type for ${n.id}`)
      return { id: n.id, type, label: n.text, description: '', config: type === 'parallel_group' ? { group: structuredClone(groups!.groups[n.id].config) } : {} }
    }),
    edges: job.geometry.edges.map(e => ({ source: e.source, target: e.target, ...(e.output ? { output: e.output } : {}), ...(e.label ? { label: e.label } : {}) })),
  }
}

export function parseJob(raw: unknown): SurfaceJob {
  if (!isObject(raw)) throw new JobError('render job must be an object')
  if (raw.mode === 'measure') {
    const req = raw.request
    if (!isObject(req)) throw new JobError('measure request is required')
    requireContract(req.contract)
    if (!Array.isArray(req.items)) throw new JobError('measure items must be an explicit array')
    const keys = new Set<string>()
    for (const it of req.items) {
      if (!isObject(it) || typeof it.key !== 'string' || !it.key.trim() || keys.has(it.key) ||
          typeof it.text !== 'string' || !TEXT_STYLES.includes(it.style as TextStyle) || !finite(it.maxWidth) || it.maxWidth <= 0) {
        throw new JobError('measure request: invalid text item or duplicate key')
      }
      keys.add(it.key)
    }
    return { mode: 'measure', request: { contract: req.contract, items: req.items as TextItem[] } }
  }
  if (raw.mode !== 'render') throw new JobError('render job: unknown mode')
  if (typeof raw.revision !== 'string' || !/^[a-zA-Z0-9_-]+$/.test(raw.revision)) throw new JobError('render job: invalid revision')
  if (!finite(raw.attempt) || !Number.isSafeInteger(raw.attempt) || raw.attempt <= 0) throw new JobError('render job: invalid attempt')
  if (!isObject(raw.geometry)) throw new JobError('render job: geometry is required')
  const geo = { ...raw.geometry, edges: arrayOrEmpty(raw.geometry.edges, 'edges'), crossings: arrayOrEmpty(raw.geometry.crossings, 'crossings') }
  const shape = geo as Record<string, unknown>
  requireContract(shape.contract)
  if (!isObject(shape.canvas) || !finite(shape.canvas.w) || !finite(shape.canvas.h) || shape.canvas.w <= 0 || shape.canvas.h <= 0) throw new JobError('render job: invalid canvas')
  if (!Array.isArray(shape.nodes) || !shape.nodes.length) throw new JobError('render job: no nodes')
  if (!shape.nodes.every(n => isObject(n) && typeof n.id === 'string' && typeof n.text === 'string') ||
      !geo.edges.every(e => isObject(e) && typeof e.source === 'string' && typeof e.target === 'string')) throw new JobError('render job: malformed geometry')
  if (!isObject(raw.workflowNodeTypes) || Object.keys(raw.workflowNodeTypes).length !== shape.nodes.length ||
      !Object.values(raw.workflowNodeTypes).every(t => typeof t === 'string' && t.trim())) throw new JobError('render job: invalid node types')
  const job = { ...raw, geometry: geo } as unknown as RenderJob
  const definition = workflowForJob(job)
  if (job.workflowGroups && job.geometry.format === 'arbitron.layout/v1') throw new JobError('render job: reviewed group geometry is unavailable in v1')
  const checked = validateGeometry(definition, job.geometry, RENDERER_CONTRACT,job.workflowGroups)
  if (!checked.ok) throw new JobError(`render job: ${checked.issues.join('; ')}`)
  if (!Array.isArray(raw.states)) throw new JobError('render job: runtime states required')
  if (raw.states.some(s => !RUNTIME_STATES.includes(s))) throw new JobError('render job: unknown runtime state')
  if (raw.states.length !== RUNTIME_STATES.length || new Set(raw.states).size !== RUNTIME_STATES.length) throw new JobError('render job: runtime state coverage must be complete and unique')
  return job
}

/** Fetches and validates the job named by the page's ?job= token. */
export async function fetchJob(search: string, fetchImpl: typeof fetch = fetch): Promise<SurfaceJob> {
  const res = await fetchImpl(jobUrl(jobTokenFrom(search)), { cache: 'no-store' })
  if (!res.ok) throw new JobError(`render job: fetch failed with HTTP ${res.status}`)
  return parseJob(await res.json())
}
