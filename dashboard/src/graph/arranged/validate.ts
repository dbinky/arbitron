import contractJSON from './contract.json'
import previousContractsJSON from './contract.previous.json'
import type { GroupGraph } from '../../api/types'
import { groupElementID, groupGeometryIssues } from './groupValidate'
import type { WorkflowDefinition } from '../types'
import type { EdgeRoute, Geometry, LayoutView, Point, Rect, RendererContract } from './types'

export const RENDERER_CONTRACT: RendererContract = contractJSON
/**
 * Earlier contracts whose stored layouts are shown, drawn with their own
 * contract, only as retained geometry while they are re-arranged. Shared with
 * domain/layout/contract.go:PreviousContracts.
 */
export const PREVIOUS_RENDERER_CONTRACTS: RendererContract[] = previousContractsJSON
export const GEOMETRY_FORMAT = 'arbitron.layout/v2'

// Keep coordinate tolerance/quantization aligned with domain/layout/geometry.go.
const EPS = 0.01
const object = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)
const finite = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v)
const integer = (v: unknown, min: number): v is number =>
  finite(v) && Number.isInteger(v) && v >= min
const point = (v: unknown): v is Point => object(v) && finite(v.x) && finite(v.y)
const rect = (v: unknown): v is Rect =>
  object(v) &&
  finite(v.x) &&
  finite(v.y) &&
  finite(v.w) &&
  finite(v.h) &&
  v.w > 0 &&
  v.h > 0 &&
  finite(v.x + v.w) &&
  finite(v.y + v.h)
const nearly = (a: number, b: number) => Math.abs(a - b) <= EPS
const samePoint = (a: Point, b: Point) => nearly(a.x, b.x) && nearly(a.y, b.y)
const containsPoint = (r: Rect, p: Point) =>
  p.x >= r.x - EPS && p.y >= r.y - EPS && p.x <= r.x + r.w + EPS && p.y <= r.y + r.h + EPS
const containsRect = (r: Rect, b: Rect) =>
  containsPoint(r, b) && containsPoint(r, { x: b.x + b.w, y: b.y + b.h })
const optionalString = (v: unknown) => v === undefined || typeof v === 'string'
const normalized = (v: string | undefined) => v || ''
// Error reporting must not invoke conversion methods from malformed JSON objects.
const describeValue = (v: unknown): string =>
  v !== null && (typeof v === 'object' || typeof v === 'function') ? '[invalid value]' : String(v)

function contractIssues(value: unknown, prefix: string, bundled: RendererContract = RENDERER_CONTRACT): string[] {
  if (!object(value)) return [`${prefix} is missing or malformed`]
  const issues: string[] = []
  for (const [key, expected] of Object.entries(bundled)) {
    if (value[key] !== expected) {
      issues.push(
        key === 'version'
          ? `${prefix} version "${describeValue(value[key])}" does not match "${expected}"`
          : `${prefix}.${key} does not match the bundled renderer`,
      )
    }
  }
  for (const key of Object.keys(value)) {
    if (!Object.prototype.hasOwnProperty.call(bundled, key))
      issues.push(`${prefix} has unknown field ${key}`)
  }
  return issues
}

function validWorkflow(value: unknown): value is WorkflowDefinition {
  if (
    !object(value) ||
    !Array.isArray(value.nodes) ||
    value.nodes.length === 0 ||
    !Array.isArray(value.edges)
  )
    return false
  const ids = new Set<string>()
  for (const n of value.nodes) {
    if (
      !object(n) ||
      typeof n.id !== 'string' ||
      !n.id ||
      ids.has(n.id) ||
      typeof n.type !== 'string' ||
      !optionalString(n.label)
    )
      return false
    ids.add(n.id)
  }
  return value.edges.every(
    e =>
      object(e) &&
      typeof e.source === 'string' &&
      typeof e.target === 'string' &&
      ids.has(e.source) &&
      ids.has(e.target) &&
      optionalString(e.output) &&
      optionalString(e.label),
  )
}

function onSide(p: Point, side: unknown, r: Rect): boolean {
  switch (side) {
    case 'top':
      return nearly(p.y, r.y) && p.x > r.x && p.x < r.x + r.w
    case 'bottom':
      return nearly(p.y, r.y + r.h) && p.x > r.x && p.x < r.x + r.w
    case 'left':
      return nearly(p.x, r.x) && p.y > r.y && p.y < r.y + r.h
    case 'right':
      return nearly(p.x, r.x + r.w) && p.y > r.y && p.y < r.y + r.h
    default:
      return false
  }
}

function quantize(v: number): number {
  // Go math.Round uses half-away-from-zero, unlike JavaScript for negatives.
  return Math.abs(v) > Number.MAX_VALUE * EPS
    ? v
    : Math.sign(v) * Math.round(Math.abs(v) / EPS) * EPS
}

function validRoute(value: unknown, boxes: Map<string, Rect>, canvas: Rect, groups?:GroupGraph|null): value is EdgeRoute {
  if (!object(value) || typeof value.source !== 'string' || typeof value.target !== 'string')
    return false
  if ((value.source === value.target) !== (value.kind === 'self_loop')) return false
  const lane = value.lane === undefined ? 0 : value.lane
  if (!integer(lane, 0)) return false
  switch (value.kind) {
    case 'forward':
      if ((value.side !== undefined && value.side !== '') || lane !== 0) return false
      break
    case 'loop_return':
      if ((value.side !== 'left' && value.side !== 'right') || lane < 1) return false
      break
    case 'self_loop':
      if (value.side !== 'right') return false
      break
    default:
      return false
  }
  if (!Array.isArray(value.points) || value.points.length < 2 || !value.points.every(point))
    return false
  const points = value.points as Point[]
  for (let i = 0; i < points.length; i++) {
    const p = points[i]
    if (!containsPoint(canvas, p)) return false
    if (i > 0) {
      const a = points[i - 1]
      if ((quantize(a.x) === quantize(p.x)) === (quantize(a.y) === quantize(p.y))) return false
    }
  }
  const ends = [
    {
      attach: value.sourceAttach,
      id: value.source,
      side: value.kind === 'self_loop' ? 'right' : 'bottom',
      p: points[0],
    },
    {
      attach: value.targetAttach,
      id: value.target,
      side: value.kind === 'self_loop' ? 'right' : 'top',
      p: points[points.length - 1],
    },
  ]
  return ends.every(({ attach, id, side, p }) => {
    const elementId=groupElementID(id, side==='bottom'&&groups&&Object.prototype.hasOwnProperty.call(groups.groups,id)&&value.output!=='task'?'footer':'header')
    const box = boxes.get(elementId)
    return (
      box !== undefined &&
      object(attach) &&
      attach.nodeId === id &&
      attach.elementId === elementId &&
      attach.side === side &&
      point(attach.point) &&
      samePoint(attach.point, p) &&
      onSide(attach.point, side, box)
    )
  })
}

function interiorOnRoute(p: Point, points: Point[], horizontal: boolean): boolean {
  if (points.some(v => samePoint(v, p))) return false
  for (let i = 1; i < points.length; i++) {
    const a = { x: quantize(points[i - 1].x), y: quantize(points[i - 1].y) }
    const b = { x: quantize(points[i].x), y: quantize(points[i].y) }
    if (
      horizontal &&
      a.y === b.y &&
      nearly(p.y, a.y) &&
      p.x > Math.min(a.x, b.x) &&
      p.x < Math.max(a.x, b.x)
    )
      return true
    if (
      !horizontal &&
      a.x === b.x &&
      nearly(p.x, a.x) &&
      p.y > Math.min(a.y, b.y) &&
      p.y < Math.max(a.y, b.y)
    )
      return true
  }
  return false
}

/** Pure stored-payload boundary: no DOM or font measurements, and no layout substitution. */
export function validateGeometry(
  def: WorkflowDefinition,
  geo: Geometry,
  contract: RendererContract,
  groups?: GroupGraph|null,
): { ok: boolean; issues: string[] } {
  const issues: string[] = []
  const result = () => ({ ok: issues.length === 0, issues })
  if (!object(geo)) return { ok: false, issues: ['geometry is missing'] }
  if (!validWorkflow(def)) return { ok: false, issues: ['workflow is missing or malformed'] }
  // The expected contract is the bundled one or, for retained display only, a
  // previous one; the geometry must carry exactly the expected contract.
  const expected = PREVIOUS_RENDERER_CONTRACTS.find(c => contractIssues(contract, '', c).length === 0) ?? RENDERER_CONTRACT
  issues.push(
    ...contractIssues(contract, 'expected renderer contract', expected),
    ...contractIssues(geo.contract, 'renderer contract', expected),
  )
  if (geo.format !== GEOMETRY_FORMAT)
    issues.push(`format "${describeValue(geo.format)}" is not ${GEOMETRY_FORMAT}`)
  if (!rect(geo.canvas) || geo.canvas.x !== 0 || geo.canvas.y !== 0)
    issues.push('canvas is invalid')
  const options = geo.options
  // The Go boundary permits zero margin and zero search sweeps (an initial candidate).
  const positiveOptions = [
    'rankSpacing',
    'nodeSpacing',
    'laneSeparation',
    'gutterWidth',
    'labelGap',
    'portSpacing',
    'dummyWidth',
  ]
  if (
    !object(options) ||
    positiveOptions.some(k => !finite(options[k]) || options[k] <= 0) ||
    !finite(options.nodeTextSlack) ||
    options.nodeTextSlack < 0 ||
    !finite(options.margin) ||
    options.margin < 0 ||
    !integer(options.crossingSweeps, 0) ||
    Object.values(options).some(v => !finite(v))
  ) {
    issues.push('geometry options are invalid')
  }
  if (!Array.isArray(geo.nodes)) issues.push('geometry has no node list')
  if (!Array.isArray(geo.edges)) issues.push('geometry has no edge list')
  if (!Array.isArray(geo.crossings)) issues.push('geometry has no crossing list')
  if (!Array.isArray(geo.nodes) || !Array.isArray(geo.edges) || !Array.isArray(geo.crossings))
    return result()
  const groupIssues=groupGeometryIssues(def,geo,groups,rect,containsRect)
  issues.push(...groupIssues)
  if(groupIssues.length)return result()
  if (geo.nodes.length !== def.nodes.length)
    issues.push(`geometry has ${geo.nodes.length} nodes, workflow has ${def.nodes.length}`)
  if (geo.edges.length !== def.edges.length)
    issues.push(`geometry has ${geo.edges.length} edges, workflow has ${def.edges.length}`)
  const boxes = new Map<string, Rect>()
  geo.nodes.forEach((n, i) => {
    if (!object(n)) {
      issues.push(`node ${i} is malformed`)
      return
    }
    const want = def.nodes[i]
    if (!want || n.id !== want.id)
      issues.push(`node ${i} is "${describeValue(n.id)}", expected "${want?.id}"`)
    if (
      want &&
      (n.text !== (want.label || want.id) || n.typeCaption !== want.type.replace(/_/g, ' '))
    )
      issues.push(`node ${i} text/type does not match workflow`)
    if (typeof n.id !== 'string' || boxes.has(groupElementID(n.id,'header')))
      issues.push(`node ${i} has a duplicate or invalid id`)
    if (!rect(n.box) || !rect(geo.canvas) || !containsRect(geo.canvas, n.box))
      issues.push(`node "${describeValue(n.id)}" has an invalid box`)
    else if (typeof n.id === 'string') boxes.set(groupElementID(n.id,'header'), n.box)
    if (!integer(n.textLines, 1) || !integer(n.rank, 0))
      issues.push(`node ${i} has invalid lines/rank`)
  })
  for(const e of geo.groupElements??[]) if(object(e)&&typeof e.id==='string'&&rect(e.box)) boxes.set(e.id,e.box)
  const routes = new Map<number, EdgeRoute>()
  geo.edges.forEach((e, i) => {
    if (!object(e)) {
      issues.push(`edge ${i} is malformed`)
      return
    }
    const want = def.edges[i]
    if (e.ordinal !== i) issues.push(`edge ${i} has ordinal ${describeValue(e.ordinal)}`)
    if (!want || e.source !== want.source || e.target !== want.target)
      issues.push(
        `edge ${i} routes ${describeValue(e.source)}->${describeValue(e.target)}, expected ${want?.source}->${want?.target}`,
      )
    if (
      !optionalString(e.output) ||
      !optionalString(e.label) ||
      (want &&
        (normalized(e.output) !== normalized(want.output) ||
          normalized(e.label) !== normalized(want.label)))
    )
      issues.push(`edge ${i} output/label does not match workflow`)
    if (!rect(geo.canvas) || !validRoute(e, boxes, geo.canvas,groups))
      issues.push(`edge ${i} has an invalid route`)
    else routes.set(i, e)
    const text = want && (normalized(want.label) || normalized(want.output))
    if (!text) {
      if (e.text !== undefined && e.text !== null)
        issues.push(`edge ${i} has unexpected label geometry`)
    } else if (
      !object(e.text) ||
      e.text.text !== text ||
      !integer(e.text.lines, 1) ||
      !rect(e.text.box) ||
      !rect(geo.canvas) ||
      !containsRect(geo.canvas, e.text.box)
    )
      issues.push(`edge ${i} has invalid label text/box/lines`)
  })
  geo.crossings.forEach((c, i) => {
    if (!object(c)) {
      issues.push(`crossing ${i} is malformed`)
      return
    }
    if (
      !integer(c.over, 0) ||
      !integer(c.under, 0) ||
      c.over >= geo.edges.length ||
      c.under >= geo.edges.length ||
      c.over === c.under
    ) {
      issues.push(`crossing ${i} references an unknown edge`)
      return
    }
    const over = routes.get(c.over),
      under = routes.get(c.under)
    if (
      !point(c.point) ||
      !rect(geo.canvas) ||
      !containsPoint(geo.canvas, c.point) ||
      !over ||
      !under ||
      !interiorOnRoute(c.point, over.points, true) ||
      !interiorOnRoute(c.point, under.points, false)
    ) {
      issues.push(`crossing ${i} is not a proper horizontal/vertical intersection`)
      return
    }
    const p = c.point
    if (
      geo.crossings
        .slice(0, i)
        .some(
          prev =>
            object(prev) &&
            prev.over === c.over &&
            prev.under === c.under &&
            point(prev.point) &&
            samePoint(prev.point, p),
        )
    )
      issues.push(`crossing ${i} is duplicated`)
  })
  return result()
}

export function resolveDisplayGeometry(
  def: WorkflowDefinition,
  view: LayoutView | null,
): { geometry: Geometry | null; issues: string[] } {
  if (!view) return { geometry: null, issues: [] }
  const revision = def?.presentation?.revision ?? ''
  if (view.savedRevision !== revision)
    return {
      geometry: null,
      issues: [
        `layout revision "${describeValue(view.savedRevision)}" does not match workflow revision "${describeValue(revision)}"`,
      ],
    }
  if (view.geometry === undefined || view.geometry === null)
    return {
      geometry: null,
      issues:
        view.displayStatus === 'complete' ? ['missing reviewed geometry for complete layout'] : [],
    }
  const version = object(view.geometry) && object(view.geometry.contract) ? view.geometry.contract.version : undefined
  const previous = view.provenance === 'retained_geometry' ? PREVIOUS_RENDERER_CONTRACTS.find(c => c.version === version) : undefined
  const validation = validateGeometry(def, view.geometry, previous ?? RENDERER_CONTRACT, view.groups)
  return {
    geometry: validation.ok ? view.geometry : null,
    issues: validation.issues,
  }
}
