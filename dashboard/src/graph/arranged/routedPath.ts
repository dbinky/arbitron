import type { Point } from './types'

/** Distance (px) within which a gap point counts as lying on a segment. */
const ON_SEGMENT_EPSILON = 0.5

function fmt(n: number): string {
  const r = Math.round(n * 100) / 100
  return String(Object.is(r, -0) ? 0 : r)
}

function xy(p: Point): string {
  return `${fmt(p.x)},${fmt(p.y)}`
}

/** Parameter (0..1) of `g` along a→b when `g` lies on the segment, else null. */
function paramOnSegment(a: Point, b: Point, g: Point): number | null {
  const dx = b.x - a.x
  const dy = b.y - a.y
  const len2 = dx * dx + dy * dy
  if (len2 === 0) return null
  const t = ((g.x - a.x) * dx + (g.y - a.y) * dy) / len2
  const len = Math.sqrt(len2)
  if (t < -ON_SEGMENT_EPSILON / len || t > 1 + ON_SEGMENT_EPSILON / len) return null
  const tc = Math.min(1, Math.max(0, t))
  const px = a.x + tc * dx
  const py = a.y + tc * dy
  if (Math.hypot(g.x - px, g.y - py) > ON_SEGMENT_EPSILON) return null
  return tc
}

/** Ink beyond each side of an over crossing's gap span (px). */
const OVER_BANK_PX = 1

/**
 * Dashed strokes restart their pattern at every subpath, even a same-point `M`.
 * `onPx` is the pattern's first dash; `overs` are crossings this route passes over.
 */
export interface DashRestarts {
  onPx: number
  overs: Point[]
}

/**
 * Build the SVG path `d` for a persisted orthogonal route. Each gap point that
 * lies on a segment opens a gap of `gapPx` centred on it (clamped to the
 * segment): the current subpath ends before the gap and a new `M` subpath
 * starts after it, all in the SAME path string. A gap point is consumed by the
 * first segment it lies on; gap points on no segment are ignored. Coordinates
 * are rounded to 2 decimals. Returns '' for fewer than two points.
 *
 * With `dash`, the pattern is restarted (a same-point `M`) one dash before each
 * gap, so both banks are inked, and in dash steps across each over crossing, so
 * the over stroke is continuous across the whole gap span. A dash hole there
 * would make a crossing read as a junction. The reviewer mirrors this policy in
 * `adapter/renderreview/reviewer_checks.go:expectedSegments`.
 */
export function buildRoutedPath(points: Point[], gaps: Point[], gapPx: number, dash?: DashRestarts): string {
  if (points.length < 2) return ''
  const half = Math.max(0, gapPx) / 2
  const on = dash && dash.onPx > 0 ? dash.onPx : 0
  const remaining = [...gaps]
  const overs = on ? [...dash!.overs] : []
  const cmds: string[] = [`M ${xy(points[0])}`]
  let fresh = true

  for (let i = 0; i < points.length - 1; i++) {
    const a = points[i]
    const b = points[i + 1]
    const len = Math.hypot(b.x - a.x, b.y - a.y)
    const onThis: number[] = []
    for (let k = remaining.length - 1; k >= 0; k--) {
      const t = paramOnSegment(a, b, remaining[k])
      if (t != null) {
        onThis.push(t * len)
        remaining.splice(k, 1)
      }
    }
    onThis.sort((p, q) => p - q)
    const restarts: number[] = []
    if (on) {
      for (const s of onThis) restarts.push(Math.max(0, s - half - on))
      for (let k = overs.length - 1; k >= 0; k--) {
        const t = paramOnSegment(a, b, overs[k])
        if (t == null) continue
        overs.splice(k, 1)
        const s = t * len
        let r = s - half - OVER_BANK_PX
        do { restarts.push(Math.max(0, r)); r += on } while (r < s + half + OVER_BANK_PX)
      }
    }
    // Order by where each event starts drawing, gaps first on ties, so a
    // restart inside a gap span is skipped instead of inking the gap.
    type Event = { at: number; gap: boolean }
    const from = (e: Event) => (e.gap ? e.at - half : e.at)
    const events: Event[] = [...restarts.filter(r => r < len).map(at => ({ at, gap: false })), ...onThis.map(at => ({ at, gap: true }))]
    events.sort((p, q) => from(p) - from(q) || Number(q.gap) - Number(p.gap))

    const at = (s: number): Point => ({
      x: a.x + ((b.x - a.x) * s) / len,
      y: a.y + ((b.y - a.y) * s) / len,
    })
    let cursor = 0
    for (const ev of events) {
      if (!ev.gap) {
        if (ev.at < cursor || (ev.at === cursor && fresh)) continue
        if (ev.at > cursor) cmds.push(`L ${xy(at(ev.at))}`)
        cmds.push(`M ${xy(at(ev.at))}`)
        cursor = ev.at
        fresh = true
        continue
      }
      const start = Math.max(cursor, ev.at - half)
      const end = Math.min(len, ev.at + half)
      if (end <= cursor) continue
      if (start > cursor) cmds.push(`L ${xy(at(start))}`)
      cmds.push(`M ${xy(at(end))}`)
      cursor = end
      fresh = true
    }
    if (cursor < len) {
      cmds.push(`L ${xy(b)}`)
      fresh = false
    }
  }
  return cmds.join(' ')
}
