import type { Geometry, Point, Rect, RendererContract } from '../graph/arranged/types'
import { ensureBundledTextFonts } from '../graph/arranged/fonts'
import fontCoverage from '../graph/arranged/fontCoverage.json'
import type { RenderedCrossing, RenderedEdge, RenderedNode, RenderedSegment, RenderedText, RenderedGroupElement, RenderedGroupConnector, RenderedGroupCrossing } from './job'
import { parseColor, textContrast } from './contrast'

export interface CollectContext { doc: Document; win: Window; root: Element; zoom: number; origin: Point }
interface ClientRectLike { left: number; top: number; width: number; height: number }
interface FaceLike { family: string; weight: string; status: string; style?: string; unicodeRange?: string }
interface Snapshot { nodes: RenderedNode[]; edges: RenderedEdge[]; crossings?: RenderedCrossing[] }
const px = (v: string) => parseFloat(v) || 0
const near = (a: number, b: number, tol: number) => Math.abs(a - b) <= tol
const samePoint = (a: Point, b: Point, tol: number) => near(a.x, b.x, tol) && near(a.y, b.y, tol)
const sameRect = (a: Rect, b: Rect, tol: number) => samePoint(a, b, tol) && near(a.w, b.w, tol) && near(a.h, b.h, tol)

export function toCanvasRect(r: ClientRectLike, root: { left: number; top: number }, zoom: number, origin: Point): Rect {
  return { x: (r.left - root.left) / zoom + origin.x, y: (r.top - root.top) / zoom + origin.y, w: r.width / zoom, h: r.height / zoom }
}
export function centerPoint(r: Rect): Point { return { x: r.x + r.w / 2, y: r.y + r.h / 2 } }
export function unionRects(rects: Rect[]): Rect | null {
  if (!rects.length) return null
  const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y))
  return { x, y, w: Math.max(...rects.map(r => r.x + r.w)) - x, h: Math.max(...rects.map(r => r.y + r.h)) - y }
}
export function rectInside(i: Rect, o: Rect, tol = 1): boolean {
  return i.x >= o.x - tol && i.y >= o.y - tol && i.x + i.w <= o.x + o.w + tol && i.y + i.h <= o.y + o.h + tol
}
export function markerIdFrom(ref: string | null | undefined): string | null {
  return ref?.match(/^url\(["']?(?:[^#]*#)([^"')]+)["']?\)$/)?.[1] ?? null
}
const families = (c: RendererContract) => c.fontFamily.split(',').map(f => f.trim().replace(/["']/g, ''))
const fontStyles = (c: RendererContract) => [[c.nodeLabelWeight, c.nodeLabelFontPx], [c.nodeTypeWeight, c.nodeTypeFontPx], [c.edgeLabelWeight, c.edgeLabelFontPx]]
function normalizedRange(range: string): string {
  return range.split(',').map(part => {
    const [a, b = a] = part.trim().replace(/^U\+/i, '').split('-').map(n => parseInt(n, 16))
    return `${a}-${b}`
  }).join(',')
}
/** Complete-face inventory check. Per-job readiness uses the producer's text-specific subset/hash checks below. */
export function fontFamilyResolved(faces: Iterable<FaceLike>, c: RendererContract): boolean {
  const signature = (family: string, weight: number, style: string | undefined, range: string) =>
    JSON.stringify([family.replace(/["']/g, ''), weight, style, normalizedRange(range)])
  const loaded = new Set<string>()
  for (const face of faces) {
    const range = face.unicodeRange
    if (face.status === 'loaded' && range) loaded.add(signature(face.family, Number(face.weight), face.style, range))
  }
  const required = fontCoverage.faces.filter(f => families(c).includes(f.family) && fontStyles(c).some(([weight]) => weight === f.weight))
  return required.length > 0 && required.every(want => loaded.has(signature(want.family, want.weight, want.style, want.unicodeRange)))
}
export function contractFonts(c: RendererContract): string[] {
  return families(c).flatMap(f => fontStyles(c).map(([w, size]) => `${w} ${size}px "${f}"`))
}
export async function ensureFonts(doc: Document, contract: RendererContract, texts: readonly string[]) {
  if (!doc.fonts) return { fontsReady: false, fontFamilyResolved: false }
  const result = await ensureBundledTextFonts(doc, texts, contract)
  return { ...result, fontsReady: result.fontsReady && doc.fonts.status === 'loaded' }
}

export function textReadabilityIssues(t: RenderedText, name: string, c: RendererContract, zoom: number): string[] {
  const issues: string[] = []
  if (!t.visible) issues.push(`${name} invisible`)
  if (t.clipped) issues.push(`${name} clipped`)
  if (t.occluded) issues.push(`${name} occluded`)
  const size = t.fontPx * zoom
  if (size + 0.001 < c.minReadableTextPx) issues.push(`${name} ${Number(size.toFixed(2))}px < ${c.minReadableTextPx}px`)
  if (t.contrast < c.minContrastRatio) issues.push(`${name} contrast ${t.contrast.toFixed(2)} < ${c.minContrastRatio}`)
  return issues
}
export function readabilityIssues(nodes: RenderedNode[], edges: RenderedEdge[], c: RendererContract, zoom: number): string[] {
  return [
    ...nodes.flatMap(n => [...textReadabilityIssues(n.label, `node ${n.id} label`, c, zoom), ...textReadabilityIssues(n.type, `node ${n.id} type`, c, zoom)]),
    ...edges.flatMap(e => e.label ? textReadabilityIssues(e.label, `edge ${e.ordinal} label`, c, zoom) : []),
  ]
}
export function stabilityIssues(base: Snapshot, now: Snapshot, tol = 1): string[] {
  const issues: string[] = []
  const textChanged = (a: RenderedText, b: RenderedText) => a.content !== b.content || a.visible !== b.visible ||
    !sameRect(a.box, b.box, tol) || a.glyphs.length !== b.glyphs.length || a.glyphs.some((r, i) => !sameRect(r, b.glyphs[i], tol)) ||
    (!a.clipped && b.clipped) || (!a.occluded && b.occluded) || b.contrast < a.contrast - 0.01 || b.fontPx < a.fontPx - 0.01
  for (const a of base.nodes) {
    const b = now.nodes.find(n => n.id === a.id)
    if (!b) issues.push(`node ${a.id} disappeared`)
    else {
      if (!sameRect(a.box, b.box, tol)) issues.push(`node ${a.id} box changed`)
      if (textChanged(a.label, b.label) || textChanged(a.type, b.type)) issues.push(`node ${a.id} text changed`)
    }
  }
  for (const a of base.edges) {
    const b = now.edges.find(e => e.ordinal === a.ordinal)
    if (!b) { issues.push(`edge ${a.ordinal} disappeared`); continue }
    if (!samePoint(a.start, b.start, tol) || !samePoint(a.end, b.end, tol)) issues.push(`edge ${a.ordinal} endpoints moved`)
    else if (a.segments.length !== b.segments.length || a.segments.some((s, i) => !samePoint(s.start, b.segments[i].start, tol) || !samePoint(s.end, b.segments[i].end, tol))) issues.push(`edge ${a.ordinal} route changed`)
    if (a.hasArrow && !b.hasArrow) issues.push(`edge ${a.ordinal} lost its arrowhead`)
    else if (a.arrowPoints.length !== b.arrowPoints.length || a.arrowPoints.some((p, i) => !samePoint(p, b.arrowPoints[i], tol))) {
      // Endpoint movement already accounts for a translated arrow.
      if (samePoint(a.end, b.end, tol)) issues.push(`edge ${a.ordinal} arrow changed`)
    }
    if (a.visible !== b.visible || a.strokeColor !== b.strokeColor || a.dashPattern !== b.dashPattern || !near(a.strokePx, b.strokePx, 0.01)) issues.push(`edge ${a.ordinal} style changed`)
    if (!!a.label !== !!b.label || (a.label && b.label && textChanged(a.label, b.label))) issues.push(`edge ${a.ordinal} label changed`)
  }
  if (JSON.stringify(base.crossings ?? []) !== JSON.stringify(now.crossings ?? [])) issues.push('crossing separation changed')
  return issues
}

function canvasRect(el: Element, ctx: CollectContext): Rect {
  return toCanvasRect(el.getBoundingClientRect(), ctx.root.getBoundingClientRect(), ctx.zoom, ctx.origin)
}
/** Include ancestor opacity: a Range still has bounds when no ink is painted. */
function opacity(el: Element, win: Window): number {
  let out = 1
  for (let p: Element | null = el; p; p = p.parentElement) {
    const cs = win.getComputedStyle(p)
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.visibility === 'collapse') return 0
    out *= cs.opacity === '' ? 1 : Number(cs.opacity)
  }
  return out
}
// Chrome reports a wrapped line's hanging space as its own narrow rect. It
// belongs to the line it ends, and read alone it is a blank "line" with no ink.
// One rect per line: the union of that line's ink fragments.
function foldLineRects(rects: ClientRectLike[]): ClientRectLike[] {
  const out: ClientRectLike[] = []
  for (const r of rects) {
    const i = out.findIndex(p => Math.abs(p.top - r.top) < 1 && Math.abs(p.height - r.height) < 1)
    if (i < 0) { out.push(r); continue }
    const p = out[i], left = Math.min(p.left, r.left)
    out[i] = { left, top: p.top, width: Math.max(p.left + p.width, r.left + r.width) - left, height: p.height }
  }
  return out
}
// Collapsible whitespace carries no ink, and a space at a soft wrap may hang
// past the line box, so only the runs between it are glyph bounds.
function inkLineRects(el: Element, ctx: CollectContext): ClientRectLike[] {
  const rects: ClientRectLike[] = []
  const walker = ctx.doc.createTreeWalker(el, 4 /* NodeFilter.SHOW_TEXT */)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    for (const m of (n.nodeValue ?? '').matchAll(/[^ \t\n\r\f]+/g)) {
      const range = ctx.doc.createRange()
      range.setStart(n, m.index)
      range.setEnd(n, m.index + m[0].length)
      if (typeof range.getClientRects === 'function') rects.push(...[...range.getClientRects()].filter(r => r.width > 0 && r.height > 0))
    }
  }
  return foldLineRects(rects)
}
export function collectText(el: Element | null, container: Element | null, ctx: CollectContext): RenderedText {
  if (!el || !container) return { content: '', glyphs: [], visible: false, box: { x: 0, y: 0, w: 0, h: 0 }, clipped: true, fontPx: 0, contrast: 0, occluded: true }
  const cs = ctx.win.getComputedStyle(el), box = canvasRect(el, ctx)
  const lines = inkLineRects(el, ctx)
  const root = ctx.root.getBoundingClientRect()
  const glyphs = lines.map(r => toCanvasRect(r, root, ctx.zoom, ctx.origin))
  let clipped = !glyphs.length || el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1
  const tolerance = 1 / ctx.zoom
  if (glyphs.some(r => !rectInside(r, canvasRect(container, ctx), tolerance))) clipped = true
  for (let p: Element | null = el; p; p = p.parentElement) {
    const style = ctx.win.getComputedStyle(p)
    const clipsX = /hidden|clip|auto|scroll/.test(style.overflowX || style.overflow)
    const clipsY = /hidden|clip|auto|scroll/.test(style.overflowY || style.overflow)
    const b = canvasRect(p, ctx)
    if (glyphs.some(r => (clipsX && (r.x < b.x - tolerance || r.x + r.w > b.x + b.w + tolerance)) ||
      (clipsY && (r.y < b.y - tolerance || r.y + r.h > b.y + b.h + tolerance)))) clipped = true
  }
  const occluded = !lines.length || lines.some(r => [0.25, 0.5, 0.75].some(f => {
    const hit = ctx.doc.elementFromPoint?.(r.left + r.width * f, r.top + r.height / 2)
    return !hit || (hit !== el && !el.contains(hit))
  }))
  const color = parseColor(cs.color)
  return { content: el.textContent ?? '', glyphs, box, visible: opacity(el, ctx.win) > 0 && !!color && color.a > 0,
    clipped, occluded, fontPx: px(cs.fontSize), contrast: textContrast(el, ctx.win) }
}
export function collectNodes(geo: Geometry, ctx: CollectContext): RenderedNode[] {
  const result: RenderedNode[] = []
  for (const n of geo.nodes) {
    const el = [...ctx.root.querySelectorAll('[data-arranged-node]')].find(el => el.getAttribute('data-arranged-node') === n.id)
    if (el) result.push({ id: n.id, box: canvasRect(el, ctx), label: collectText(el.querySelector('[data-node-text="label"]'), el, ctx), type: collectText(el.querySelector('[data-node-text="type"]'), el, ctx) })
  }
  return result
}
interface Matrix { a: number; b: number; c: number; d: number; e: number; f: number }
const IDENTITY: Matrix = { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 }
function transform(p: Point, m: Matrix): Point {
  const out = { x: m.a * p.x + m.c * p.y + m.e, y: m.b * p.x + m.d * p.y + m.f }
  if (!Number.isFinite(out.x) || !Number.isFinite(out.y)) throw new Error('render surface: nonfinite SVG transform')
  return out
}
function screenPoint(p: Point, path: SVGGraphicsElement, ctx: CollectContext): Point {
  const m = path.getScreenCTM()
  if (!m) throw new Error('render surface: missing SVG transform')
  const s = transform(p, m), root = ctx.root.getBoundingClientRect()
  return { x: (s.x - root.left) / ctx.zoom + ctx.origin.x, y: (s.y - root.top) / ctx.zoom + ctx.origin.y }
}
/** Only the renderer's explicit M/L route, its quarter-arc corners and closed polygon marker grammar are supported. */
/**
 * `subpathStarts[i]` marks a segment that begins a subpath, where SVG restarts the dash pattern.
 * A rounded corner (`A r,r 0 0 sweep x,y`, a quarter circle) is reported as its chord;
 * `lengths[i]` is the drawn length, the arc's for a corner, which advances the dash phase.
 */
function pathPoints(d: string, closed = false): { points: Point[]; segments: RenderedSegment[]; subpathStarts: boolean[]; lengths: number[]; corners: boolean[] } {
  const tokens = d.match(/[MLAZ]|[-+]?(?:\d*\.\d+|\d+\.?\d*)(?:e[-+]?\d+)?/gi) ?? []
  if (!tokens.length || d.replace(/[MLAZ]|[-+]?(?:\d*\.\d+|\d+\.?\d*)(?:e[-+]?\d+)?|[\s,]/gi, '')) throw new Error('render surface: unsupported SVG path')
  const points: Point[] = [], segments: RenderedSegment[] = [], subpathStarts: boolean[] = [], lengths: number[] = [], corners: boolean[] = []
  let current: Point | null = null, start: Point | null = null, moved = false
  // Unit direction the stroke is travelling at `current`; unknown after a move.
  let heading: Point | null = null
  for (let i = 0; i < tokens.length;) {
    const cmd = tokens[i++]
    if (cmd === 'Z' && closed && current && start) { current = start; continue }
    if (cmd !== 'M' && cmd !== 'L' && cmd !== 'A') throw new Error('render surface: unsupported SVG command')
    const arc = cmd === 'A' ? tokens.slice(i, i += 5).map(Number) : []
    const x = Number(tokens[i++]), y = Number(tokens[i++])
    if (!Number.isFinite(x) || !Number.isFinite(y)) throw new Error('render surface: invalid SVG point')
    const p = { x, y }
    if (cmd === 'A') {
      const [rx, ry, rotation, large, sweep] = arc
      // Rounded to 2 decimals, a quarter circle's chord spans its radius on both axes.
      const quarter = (v: number) => Math.abs(Math.abs(v) - rx) <= 0.02
      if (arc.length !== 5 || !(rx > 0) || rx !== ry || rotation !== 0 || large !== 0 || (sweep !== 0 && sweep !== 1) || !current || closed || !quarter(p.x - current.x) || !quarter(p.y - current.y))
        throw new Error('render surface: unsupported SVG arc')
      // The corner continues the stroke's heading into its bend, then turns a
      // quarter towards the end point, clockwise exactly when sweep is 1. A
      // corner straight after a move (a gap ending at it) has no known heading.
      if (heading) {
        const turn: Point = { x: p.x - (current.x + heading.x * rx), y: p.y - (current.y + heading.y * rx) }
        const cross = heading.x * turn.y - heading.y * turn.x
        if (Math.abs(heading.x * turn.x + heading.y * turn.y) > 0.02 || !quarter(Math.hypot(turn.x, turn.y)) || (cross > 0 ? 1 : 0) !== sweep)
          throw new Error('render surface: SVG arc does not round its bend')
        heading = { x: turn.x / rx, y: turn.y / rx }
      }
      segments.push({ start: current, end: p, corner: true })
      subpathStarts.push(moved)
      lengths.push((Math.PI / 2) * rx)
      corners.push(true)
      moved = false
    } else if (cmd === 'L') {
      if (!current || samePoint(current, p, 0)) throw new Error('render surface: empty SVG segment')
      segments.push({ start: current, end: p })
      subpathStarts.push(moved)
      lengths.push(Math.hypot(p.x - current.x, p.y - current.y))
      heading = { x: (p.x - current.x) / lengths[lengths.length - 1], y: (p.y - current.y) / lengths[lengths.length - 1] }
      corners.push(false)
      moved = false
    } else {
      start = p
      moved = true
      heading = null
    }
    current = p
    points.push(p)
  }
  if (!segments.length) throw new Error('render surface: empty SVG path')
  if (closed && tokens[tokens.length - 1] !== 'Z') throw new Error('render surface: open marker outline')
  return { points, segments, subpathStarts, lengths, corners }
}
function routePath(ordinal: number, ctx: CollectContext): SVGPathElement {
  const found = [...ctx.root.querySelectorAll<SVGPathElement>('path[data-edge-path]')].filter(p => p.getAttribute('data-edge-path') === String(ordinal))
  if (found.length !== 1) throw new Error(`render surface: edge ${ordinal} coverage missing or duplicate`)
  return found[0]
}
function painted(el: Element, property: 'stroke' | 'fill', ctx: CollectContext): boolean {
  const cs = ctx.win.getComputedStyle(el), color = parseColor(cs[property])
  return opacity(el, ctx.win) > 0 && !!color && color.a > 0 && (cs.getPropertyValue(`${property}-opacity`) === '' || Number(cs.getPropertyValue(`${property}-opacity`)) > 0)
}
function markerOutline(path: SVGPathElement, end: Point, previous: Point, stroke: number, ctx: CollectContext): { hasArrow: boolean; arrowPoints: Point[] } {
  const ref = ctx.win.getComputedStyle(path).markerEnd || path.getAttribute('marker-end')
  const id = markerIdFrom(ref)
  if (!id) return { hasArrow: false, arrowPoints: [] }
  const matches = [...ctx.root.querySelectorAll<SVGMarkerElement>('marker')].filter(m => m.id === id)
  const marker = matches[0]
  if (matches.length !== 1 || !path.parentElement?.contains(marker) || !/(?:-arranged-arrow-\d+|-arranged-connector-arrow)$/.test(id)) throw new Error('render surface: dangling or unscoped marker')
  if (marker.children.length !== 1 || marker.children[0].tagName.toLowerCase() !== 'path' || marker.hasAttribute('transform')) throw new Error('render surface: unsupported marker shape')
  const child = marker.children[0] as SVGPathElement
  if (opacity(path, ctx.win) <= 0 || !painted(child, 'fill', ctx)) return { hasArrow: false, arrowPoints: [] }
  const points = pathPoints(child.getAttribute('d') ?? '', true).points
  if (points.length !== 3) throw new Error('render surface: unsupported arrow outline')
  const numbers = (v: string | null) => v?.trim().split(/[\s,]+/).map(Number) ?? []
  const vb = numbers(marker.getAttribute('viewBox'))
  const width = Number(marker.getAttribute('markerWidth')), height = Number(marker.getAttribute('markerHeight'))
  const refX = Number(marker.getAttribute('refX')), refY = Number(marker.getAttribute('refY'))
  if (vb.length !== 4 || ![...vb, width, height, refX, refY].every(Number.isFinite) || vb[2] <= 0 || vb[3] <= 0 || width <= 0 || height <= 0) throw new Error('render surface: invalid marker viewport')
  const aspect = marker.getAttribute('preserveAspectRatio') ?? 'xMidYMid meet'
  if (aspect !== 'xMidYMid meet' && aspect !== 'none') throw new Error('render surface: unsupported marker aspect ratio')
  const unit = marker.getAttribute('markerUnits') ?? 'strokeWidth'
  if (unit !== 'userSpaceOnUse' && unit !== 'strokeWidth') throw new Error('render surface: unsupported marker units')
  const scaleX = aspect === 'none' ? width / vb[2] : Math.min(width / vb[2], height / vb[3])
  const scaleY = aspect === 'none' ? height / vb[3] : scaleX
  const orient = marker.getAttribute('orient') ?? '0'
  const direction = Math.atan2(end.y - previous.y, end.x - previous.x)
  const angle = orient === 'auto' || orient === 'auto-start-reverse' ? direction : Number(orient.replace(/deg$/, '')) * Math.PI / 180
  if (!Number.isFinite(angle)) throw new Error('render surface: unsupported marker orientation')
  const cssTransform = ctx.win.getComputedStyle(child).transform
  const childMatrix = cssTransform && cssTransform !== 'none' ? new DOMMatrixReadOnly(cssTransform)
    : child.hasAttribute('transform') ? child.transform.baseVal.consolidate()?.matrix : IDENTITY
  if (!childMatrix) throw new Error('render surface: missing marker transform')
  const factor = unit === 'strokeWidth' ? stroke : 1
  const arrowPoints = points.map(p => {
    const q = transform(p, childMatrix)
    if (q.x < vb[0] || q.y < vb[1] || q.x > vb[0] + vb[2] || q.y > vb[1] + vb[3]) throw new Error('render surface: clipped marker outline')
    const x = (q.x - refX) * scaleX * factor, y = (q.y - refY) * scaleY * factor
    return screenPoint({ x: end.x + x * Math.cos(angle) - y * Math.sin(angle), y: end.y + x * Math.sin(angle) + y * Math.cos(angle) }, path, ctx)
  })
  const tip = screenPoint(end, path, ctx), back = screenPoint(previous, path, ctx)
  const length = Math.hypot(tip.x - back.x, tip.y - back.y)
  const ux = (tip.x - back.x) / length, uy = (tip.y - back.y) / length
  const atTip = arrowPoints.filter(p => samePoint(p, tip, 0.1)).length === 1
  const projections = arrowPoints.map(p => (p.x - tip.x) * ux + (p.y - tip.y) * uy)
  const area = Math.abs((arrowPoints[1].x - arrowPoints[0].x) * (arrowPoints[2].y - arrowPoints[0].y) - (arrowPoints[1].y - arrowPoints[0].y) * (arrowPoints[2].x - arrowPoints[0].x))
  return { hasArrow: atTip && area > 0.1 && projections.every(p => p <= 0.1) && projections.filter(p => p < -0.1).length === 2, arrowPoints }
}
function collectRoute(path: SVGPathElement, ctx: CollectContext): Omit<RenderedGroupConnector, 'id'> {
    const cs = ctx.win.getComputedStyle(path)
    const raw = pathPoints(path.getAttribute('d') ?? '')
    const segments: RenderedSegment[] = raw.segments.map(s => ({ start: screenPoint(s.start, path, ctx), end: screenPoint(s.end, path, ctx), ...(s.corner ? { corner: true as const } : {}) }))
    const total = path.getTotalLength()
    if (!Number.isFinite(total) || total <= 0) throw new Error('render surface: empty edge length')
    const first = path.getPointAtLength(0), last = path.getPointAtLength(total)
    const start = screenPoint(first, path, ctx), end = screenPoint(last, path, ctx)
    const stroke = px(cs.strokeWidth)
    const m = path.getScreenCTM()!
    const scaleX = Math.hypot(m.a, m.b) / ctx.zoom, scaleY = Math.hypot(m.c, m.d) / ctx.zoom
    if (!near(scaleX, scaleY, 0.001)) throw new Error('render surface: unsupported nonuniform edge scale')
    const dashPattern = cs.strokeDasharray || 'none'
    return { segments, start, end, strokeColor: cs.stroke, dashPattern, visible: painted(path, 'stroke', ctx) && stroke > 0,
      strokePx: stroke * scaleX, ...markerOutline(path, last, raw.segments[raw.segments.length - 1].start, stroke, ctx) }
}
export function collectEdges(geo: Geometry, ctx: CollectContext): RenderedEdge[] {
  return geo.edges.map(e => {
    const path = routePath(e.ordinal, ctx), actual = collectRoute(path, ctx)
    for (const overlay of ctx.root.querySelectorAll('[data-edge-overlay]')) {
      if (overlay.getAttribute('data-edge-overlay') === String(e.ordinal) && overlay.getAttribute('d') !== path.getAttribute('d')) throw new Error('render surface: active overlay route differs')
    }
    const label = [...ctx.root.querySelectorAll('[data-edge-label]')].find(l => l.getAttribute('data-edge-label') === String(e.ordinal))
    return { ordinal: e.ordinal, ...actual, dashed: actual.dashPattern !== 'none' && actual.dashPattern.split(/[ ,]+/).some(v => px(v) > 0),
      ...(label || e.text ? { label: collectText(label ?? null, label ?? null, ctx) } : {}) }
  })
}
function unique(root: Element, attribute: string, id: string, name: string): Element {
  const found = root.querySelectorAll(`[${attribute}="${CSS.escape(id)}"]`)
  if (found.length !== 1) throw new Error(`render surface: ${name} ${id} missing or duplicate`)
  return found[0]
}
export function collectGroupElements(geo: Geometry, ctx: CollectContext): RenderedGroupElement[] {
  return (geo.groupElements ?? []).map(e => {
    const el = unique(ctx.root, 'data-arranged-group-element', e.id, 'group element'), cs = ctx.win.getComputedStyle(el)
    for (const t of e.texts) unique(el, 'data-group-text', t.key, 'group text')
    const texts = [...el.querySelectorAll('[data-group-text]')].map(t => {
      const key = t.getAttribute('data-group-text')!
      unique(el, 'data-group-text', key, 'group text')
      return { key, text: collectText(t, el, ctx) }
    })
    return { id: e.id, box: canvasRect(el, ctx), texts, visible: opacity(el, ctx.win) > 0,
      borderColor: cs.borderTopColor, borderPx: px(cs.borderTopWidth), backgroundColor: cs.backgroundColor }
  })
}
function connectorPath(id: string, ctx: CollectContext): SVGPathElement {
  return unique(ctx.root, 'data-group-connector', id, 'group connector') as SVGPathElement
}
export function collectGroupConnectors(geo: Geometry, ctx: CollectContext): RenderedGroupConnector[] {
  return (geo.groupConnectors ?? []).map(c => ({ id: c.id, ...collectRoute(connectorPath(c.id, ctx), ctx) }))
}
/** Geometry and dash phase must both paint; isPointInStroke alone ignores dash holes in SVG.
 * The phase restarts at every subpath, including a same-point `M`, as Chrome paints it. */
function strokeAt(p: Point, path: SVGPathElement, ctx: CollectContext): boolean {
  if (!painted(path, 'stroke', ctx)) return false
  const cs = ctx.win.getComputedStyle(path), raw = pathPoints(path.getAttribute('d') ?? '')
  const dash = (cs.strokeDasharray || 'none') === 'none' ? [] : cs.strokeDasharray.split(/[ ,]+/).map(v => /^\d+(?:\.\d+)?(?:px)?$/.test(v) ? px(v) : NaN)
  if (dash.some(n => !Number.isFinite(n) || n < 0)) throw new Error('render surface: unsupported dash pattern')
  if (dash.length % 2) dash.push(...dash)
  const period = dash.reduce((a, b) => a + b, 0)
  const matrix = path.getScreenCTM()
  if (!matrix) throw new Error('render surface: missing stroke transform')
  const determinant = matrix.a * matrix.d - matrix.b * matrix.c
  if (!Number.isFinite(determinant) || determinant === 0) throw new Error('render surface: singular stroke transform')
  const root = ctx.root.getBoundingClientRect()
  const screen = { x: (p.x - ctx.origin.x) * ctx.zoom + root.left - matrix.e, y: (p.y - ctx.origin.y) * ctx.zoom + root.top - matrix.f }
  const local = { x: (matrix.d * screen.x - matrix.c * screen.y) / determinant, y: (-matrix.b * screen.x + matrix.a * screen.y) / determinant }
  if (!path.isPointInStroke(local as DOMPoint)) return false
  let distance = 0
  for (const [i, s] of raw.segments.entries()) {
    if (raw.subpathStarts[i]) distance = 0
    // A corner's ink curves off its chord; it only advances the dash phase.
    if (raw.corners[i]) { distance += raw.lengths[i]; continue }
    const a = screenPoint(s.start, path, ctx), b = screenPoint(s.end, path, ctx)
    const dx = b.x - a.x, dy = b.y - a.y, len = Math.hypot(dx, dy)
    const along = ((p.x - a.x) * dx + (p.y - a.y) * dy) / len
    const normal = Math.abs((p.x - a.x) * dy - (p.y - a.y) * dx) / len
    if (along >= 0 && along <= len && normal <= 0.1) {
      const rawLength = Math.hypot(s.end.x - s.start.x, s.end.y - s.start.y)
      let phase = period ? ((distance + along / len * rawLength + px(cs.strokeDashoffset)) % period + period) % period : 0
      let on = true
      if (period) for (let i = 0; i < dash.length; i++) { if (phase <= dash[i]) { on = i % 2 === 0; break } phase -= dash[i] }
      return on
    }
    distance += raw.lengths[i]
  }
  return false
}
function crossingPaint(p: Point, under: SVGPathElement, over: SVGPathElement, geo: Geometry, ctx: CollectContext) {
    const half = geo.contract.crossingGapPx / 2
    const offsets = Array.from({ length: Math.ceil(half * 2) + 1 }, (_, i) => -half + i * (half * 2 / Math.ceil(half * 2)))
    // Under is vertical, over horizontal (validated geometry contract). Probe the
    // entire required hole, not a lucky dash pixel at its centre, plus both banks.
    const underGapVisible = offsets.filter(v => Math.abs(v) < half - 0.1).every(v => !strokeAt({ x: p.x, y: p.y + v }, under, ctx)) &&
      [-1, 1].every(sign => strokeAt({ x: p.x, y: p.y + sign * (half + 0.5) }, under, ctx))
    const overContinuous = offsets.every(v => strokeAt({ x: p.x + v, y: p.y }, over, ctx))
    return { underGapVisible, overContinuous }
}
export function collectCrossings(geo: Geometry, ctx: CollectContext): RenderedCrossing[] {
  return geo.crossings.map(c => ({ ...c, ...crossingPaint(c.point, routePath(c.under, ctx), routePath(c.over, ctx), geo, ctx) }))
}
export function collectGroupCrossings(geo: Geometry, ctx: CollectContext): RenderedGroupCrossing[] {
  const path = (key: string) => key.startsWith('edge:') ? routePath(Number(key.slice(5)), ctx) : connectorPath(key, ctx)
  return (geo.groupCrossings ?? []).map(c => ({ ...c, ...crossingPaint(c.point, path(c.under), path(c.over), geo, ctx) }))
}

export function nextFrame(win: Window): Promise<void> { return new Promise(resolve => win.requestAnimationFrame(() => resolve())) }
export async function waitFor(predicate: () => boolean, win: Window, timeoutMs: number): Promise<boolean> {
  const start = win.performance.now()
  do {
    if (predicate()) return true
    await new Promise(resolve => win.setTimeout(resolve, 10))
  } while (win.performance.now() - start < timeoutMs)
  return false
}
export async function settle(doc: Document, win: Window, maxMs = 1000): Promise<void> {
  await doc.fonts?.ready
  const start = win.performance.now()
  let previous = '', stable = 0
  do {
    await nextFrame(win)
    const snapshot = JSON.stringify([...doc.querySelectorAll('[data-arranged-node], [data-node-text], [data-edge-label], [data-edge-path], [data-arranged-group-element], [data-group-text], [data-group-connector], [data-measure-key]')].map(el => {
      const r = el.getBoundingClientRect()
      return [r.left, r.top, r.width, r.height, el.getAttribute('d')]
    }))
    stable = snapshot === previous ? stable + 1 : 0
    previous = snapshot
    if (stable >= 2) return
  } while (win.performance.now() - start < maxMs)
  throw new Error('render surface: layout did not settle')
}
