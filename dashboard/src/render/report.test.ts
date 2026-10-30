import { describe, expect, it, vi } from 'vitest'
import contract from '../graph/arranged/contract.json'
import coverage from '../graph/arranged/fontCoverage.json'
import type { RendererContract } from '../graph/arranged/types'
import type { RenderedEdge, RenderedNode, RenderedText } from './job'
import {
  centerPoint, collectNodes, collectText, collectEdges, collectCrossings, contractFonts, fontFamilyResolved, markerIdFrom, readabilityIssues,
  rectInside, stabilityIssues, toCanvasRect, unionRects, waitFor,
} from './report'

const c = contract as RendererContract
const okText: RenderedText = { content: 'Start', glyphs: [{ x: 0, y: 0, w: 10, h: 10 }], visible: true, box: { x: 0, y: 0, w: 10, h: 10 }, clipped: false, fontPx: 13, contrast: 12, occluded: false }

describe('geometry helpers', () => {
  it('toCanvasRect divides by zoom and adds the canvas origin', () => {
    const r = toCanvasRect({ left: 109, top: 54, width: 180, height: 54 }, { left: 10, top: 0 }, 0.9, { x: 0, y: 0 })
    expect(r.x).toBeCloseTo(110)
    expect(r.y).toBeCloseTo(60)
    expect(r.w).toBeCloseTo(200)
    expect(r.h).toBeCloseTo(60)
    const shifted = toCanvasRect({ left: 0, top: 0, width: 9, height: 9 }, { left: 0, top: 0 }, 0.9, { x: -20, y: 5 })
    expect(shifted).toEqual({ x: -20, y: 5, w: 10, h: 10 })
  })
  it('centerPoint and unionRects', () => {
    expect(centerPoint({ x: 10, y: 20, w: 100, h: 40 })).toEqual({ x: 60, y: 40 })
    expect(unionRects([])).toBeNull()
    expect(unionRects([{ x: 0, y: 0, w: 10, h: 10 }, { x: 20, y: -5, w: 5, h: 5 }])).toEqual({ x: 0, y: -5, w: 25, h: 15 })
  })
  it('rectInside allows the tolerance and no more', () => {
    const outer = { x: 0, y: 0, w: 100, h: 50 }
    expect(rectInside({ x: -1, y: 0, w: 101, h: 50 }, outer, 1)).toBe(true)
    expect(rectInside({ x: 0, y: 0, w: 100, h: 52 }, outer, 1)).toBe(false)
  })
})

describe('markerIdFrom', () => {
  it('extracts ids from attribute and computed-style forms', () => {
    expect(markerIdFrom('url(#test-graph-arranged-arrow-3)')).toBe('test-graph-arranged-arrow-3')
    expect(markerIdFrom('url("#test-graph-arranged-arrow-12")')).toBe('test-graph-arranged-arrow-12')
    expect(markerIdFrom("url('#m')")).toBe('m')
    expect(markerIdFrom('none')).toBeNull()
    expect(markerIdFrom(null)).toBeNull()
  })
})

describe('fontFamilyResolved', () => {
  const complete = coverage.faces.filter(f => [c.nodeLabelWeight, c.nodeTypeWeight, c.edgeLabelWeight].includes(f.weight)).map(f => ({ family: f.family, weight: String(f.weight), style: f.style, unicodeRange: f.unicodeRange, status: 'loaded' }))
  it('requires every bundled family and contract weight', () => {
    expect(fontFamilyResolved(complete, c)).toBe(true)
    expect(fontFamilyResolved(complete.filter(f => f.family === 'Inter'), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(f => f.family === 'Noto Emoji' ? { ...f, status: 'unloaded' } : f), c)).toBe(false)
    expect(fontFamilyResolved([], c)).toBe(false)
    expect(fontFamilyResolved(complete.map(f => ({ ...f, unicodeRange: 'U+0000-0001' })), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(({ family, weight, status }) => ({ family, weight, status })), c)).toBe(false)
  })
  it('indexes the complete inventory once, including every subset', () => {
    let reads = 0
    const faces = complete.map(face => ({
      ...face,
      get family() { reads++; return face.family },
    }))
    expect(fontFamilyResolved(faces, c)).toBe(true)
    expect(reads).toBe(faces.length)
    expect(fontFamilyResolved(complete.slice(0, -1), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(face => ({ ...face, style: 'italic' })), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(face => ({ ...face, weight: '900' })), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(face => ({ ...face, family: `"${face.family}"`, unicodeRange: face.unicodeRange.toLowerCase() })), c)).toBe(true)
  })
  it('contractFonts enumerates separate CSS family names and styles', () => {
    expect(contractFonts(c)).toContain('600 13px "Inter"')
    expect(contractFonts(c)).toContain('600 13px "Noto Sans SC"')
    expect(contractFonts(c)).toContain('500 11px "Noto Emoji"')
    expect(contractFonts(c)).not.toContain('600 13px "Inter, Noto Sans SC, Noto Emoji"')
  })
})

describe('readabilityIssues', () => {
  const node = (label: Partial<RenderedText>): RenderedNode => ({ id: 'a', box: okText.box, label: { ...okText, ...label }, type: okText })
  it('is empty for readable text', () => {
    expect(readabilityIssues([node({})], [], c, 0.9)).toEqual([])
  })
  it('flags clipping, occlusion, size and contrast', () => {
    expect(readabilityIssues([node({ clipped: true })], [], c, 0.9)).toEqual(['node a label clipped'])
    expect(readabilityIssues([node({ occluded: true })], [], c, 0.9)).toEqual(['node a label occluded'])
    expect(readabilityIssues([node({ fontPx: 9 })], [], c, 0.9)).toEqual(['node a label 8.1px < 9px'])
    expect(readabilityIssues([node({ contrast: 3 })], [], c, 0.9)).toEqual(['node a label contrast 3.00 < 4.5'])
  })
  it('checks edge labels when present', () => {
    const e: RenderedEdge = { ordinal: 2, segments: [{ start: { x: 0, y: 0 }, end: { x: 0, y: 1 } }], strokeColor: '#8a8ab0', dashPattern: '', visible: true, arrowPoints: [{ x: 0, y: 1 }, { x: -1, y: 0 }, { x: 1, y: 0 }], start: { x: 0, y: 0 }, end: { x: 0, y: 1 }, hasArrow: true, dashed: false, strokePx: 2, label: { ...okText, contrast: 2 } }
    expect(readabilityIssues([], [e], c, 0.9)).toEqual(['edge 2 label contrast 2.00 < 4.5'])
  })
})

describe('stabilityIssues', () => {
  const n = (y: number): RenderedNode => ({ id: 'a', box: { x: 0, y, w: 10, h: 10 }, label: okText, type: okText })
  const e = (x: number, arrow = true): RenderedEdge => ({ ordinal: 0, segments: [{ start: { x, y: 0 }, end: { x, y: 10 } }], strokeColor: '#8a8ab0', dashPattern: '', visible: true, arrowPoints: [{ x, y: 10 }, { x: x - 1, y: 8 }, { x: x + 1, y: 8 }], start: { x, y: 0 }, end: { x, y: 10 }, hasArrow: arrow, dashed: false, strokePx: 2 })
  it('accepts sub-pixel jitter', () => {
    expect(stabilityIssues({ nodes: [n(0)], edges: [e(0)] }, { nodes: [n(0.5)], edges: [e(0.5)] })).toEqual([])
  })
  it('reports moved boxes, moved endpoints, lost arrows and missing elements', () => {
    expect(stabilityIssues({ nodes: [n(0)], edges: [] }, { nodes: [n(3)], edges: [] })[0]).toMatch(/node a box changed/)
    expect(stabilityIssues({ nodes: [], edges: [e(0)] }, { nodes: [], edges: [e(4)] })).toEqual(['edge 0 endpoints moved'])
    expect(stabilityIssues({ nodes: [], edges: [e(0)] }, { nodes: [], edges: [e(0, false)] })).toEqual(['edge 0 lost its arrowhead'])
    expect(stabilityIssues({ nodes: [n(0)], edges: [e(0)] }, { nodes: [], edges: [] })).toEqual(['node a disappeared', 'edge 0 disappeared'])
  })
})

describe('collectNodes (DOM contract)', () => {
  it('reports only nodes present in the DOM and marks a missing text element unreadable', () => {
    document.body.innerHTML = '<div id="root"><div data-arranged-node="a"><span data-node-text="type">agent call</span></div></div>'
    const root = document.getElementById('root')!
    const geo = { nodes: [{ id: 'a' }, { id: 'b' }], edges: [], crossings: [] } as unknown as Parameters<typeof collectNodes>[0]
    const nodes = collectNodes(geo, { doc: document, win: window, root, zoom: 0.9, origin: { x: 0, y: 0 } })
    expect(nodes.map((x) => x.id)).toEqual(['a'])
    expect(nodes[0].label).toMatchObject({ clipped: true, occluded: true, fontPx: 0 })
  })
})

describe('actual DOM evidence (jsdom API-shaped values, not browser acceptance)', () => {
  const ctx = () => ({ doc: document, win: window, root: document.body, zoom: 1, origin: { x: 0, y: 0 } })
  const geo = { nodes: [], edges: [{ ordinal: 0 }], crossings: [] } as unknown as Parameters<typeof collectEdges>[0]
  function pathDOM(d = 'M 0,0 L 0,10 L 10,10') {
    document.body.innerHTML = `<svg><g><defs><marker id="graph-arranged-arrow-0" markerWidth="10" markerHeight="10" viewBox="0 0 10 10" refX="10" refY="5" orient="auto" markerUnits="userSpaceOnUse"><path d="M 0,0 L 10,5 L 0,10 Z" style="fill: rgb(138,138,176)" /></marker></defs><path data-edge-path="0" d="${d}" marker-end="url(#graph-arranged-arrow-0)" style="stroke: rgb(138,138,176);stroke-width:2;fill:none;marker-end:url(#graph-arranged-arrow-0)" /></g></svg>`
    const path = document.querySelector<SVGPathElement>('[data-edge-path]')!
    // Deliberately translate the painted route. Candidate points are not provided.
    const matrix = { a: 1, b: 0, c: 0, d: 1, e: 20, f: 30 }
    Object.assign(path, { getScreenCTM: () => matrix, getTotalLength: () => 20, getPointAtLength: (v: number) => v === 0 ? { x: 0, y: 0 } : { x: 10, y: 10 } })
    return path
  }
  it('reads textContent, glyph bounds, opacity and ancestor clipping, not candidate text', () => {
    document.body.innerHTML = '<div style="overflow:hidden"><span style="color:#e0e0e0;font-size:13px">Actual 中</span></div>'
    const el = document.querySelector('span')!, parent = el.parentElement!
    const rect = (x: number, w: number) => ({ left: x, top: 0, width: w, height: 10, right: x+w, bottom: 10, x, y: 0, toJSON() {} })
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(rect(0, 10))
    vi.spyOn(parent, 'getBoundingClientRect').mockReturnValue(rect(0, 10))
    const range = document.createRange()
    Object.assign(range, { getClientRects: () => [rect(0, 20)] })
    vi.spyOn(document, 'createRange').mockReturnValue(range)
    Object.defineProperty(document, 'elementFromPoint', { configurable: true, value: () => el })
    expect(collectText(el, parent, ctx())).toMatchObject({ content: 'Actual 中', glyphs: [{ x: 0, y: 0, w: 20, h: 10 }], visible: true, clipped: true, occluded: false, fontPx: 13 })
    parent.style.opacity = '0'
    expect(collectText(el, parent, ctx()).visible).toBe(false)
    expect(collectText(null, null, ctx())).toMatchObject({ content: '', glyphs: [], visible: false, clipped: true })
    vi.restoreAllMocks()
  })
  it('excludes the hanging space at a wrap from glyph ink, so a full line stays inside its box (G1)', () => {
    document.body.innerHTML = '<div><span style="color:#e0e0e0;font-size:13px">plans exist wrapped</span></div>'
    const el = document.querySelector('span')!, parent = el.parentElement!, node = el.firstChild!
    const rect = (x: number, y: number, w: number) => ({ left: x, top: y, width: w, height: 14, right: x + w, bottom: y + 14, x, y, toJSON() {} })
    const box = { ...rect(0, 0, 115), height: 29, bottom: 29 }
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(box)
    vi.spyOn(parent, 'getBoundingClientRect').mockReturnValue(box)
    // 10px monospace laid out by the browser as "plans exist " (its space hangs past 115px) + "wrapped".
    const at = (i: number) => i < 12 ? { x: i * 10, y: 0 } : { x: (i - 12) * 10, y: 15 }
    vi.spyOn(document, 'createRange').mockImplementation(() => {
      let from = 0, to = 0
      const range = {
        selectNodeContents: () => { from = 0; to = node.textContent!.length },
        setStart: (n: Node, o: number) => { expect(n).toBe(node); from = o },
        setEnd: (n: Node, o: number) => { expect(n).toBe(node); to = o },
        getClientRects: () => [0, 15].flatMap(y => {
          const xs = Array.from({ length: to - from }, (_, k) => at(from + k)).filter(p => p.y === y).map(p => p.x)
          return xs.length ? [rect(Math.min(...xs), y, Math.max(...xs) + 10 - Math.min(...xs))] : []
        }),
      }
      return range as unknown as Range
    })
    Object.defineProperty(document, 'elementFromPoint', { configurable: true, value: () => el })
    const text = collectText(el, parent, ctx())
    expect(text.glyphs).toEqual([{ x: 0, y: 0, w: 110, h: 14 }, { x: 0, y: 15, w: 70, h: 14 }])
    expect(text.clipped).toBe(false)
    vi.restoreAllMocks()
  })
  it('reads full transformed path subsegments and marker ink, including middle-path mutations', () => {
    const path = pathDOM()
    const first = collectEdges(geo, ctx())[0]
    expect(first).toMatchObject({ start: { x: 20, y: 30 }, end: { x: 30, y: 40 }, segments: [{ start: { x: 20, y: 30 }, end: { x: 20, y: 40 } }, { start: { x: 20, y: 40 }, end: { x: 30, y: 40 } }], visible: true, hasArrow: true, strokePx: 2, strokeColor: 'rgb(138, 138, 176)' })
    expect(first.arrowPoints).toEqual([{ x: 20, y: 35 }, { x: 30, y: 40 }, { x: 20, y: 45 }])
    path.setAttribute('d', 'M 0,0 L -5,0 L -5,10 L 10,10')
    expect(stabilityIssues({ nodes: [], edges: [first] }, { nodes: [], edges: collectEdges(geo, ctx()) })).toContain('edge 0 route changed')
    document.querySelector('marker')!.style.opacity = '0'
    expect(collectEdges(geo, ctx())[0]).toMatchObject({ hasArrow: false, arrowPoints: [] })
    expect(() => collectEdges(geo, { ...ctx(), root: document.createElement('div') })).toThrow(/coverage|missing/)
  })
  it('preserves M gaps and fails unsupported, empty, nonfinite and dangling SVG', () => {
    const path = pathDOM('M 0,0 L 0,4 M 0,6 L 0,10 L 10,10')
    expect(collectEdges(geo, ctx())[0].segments).toHaveLength(3)
    for (const d of ['', 'M 0,0 Q 3,4 10,10', 'M 0,0 L NaN,10']) {
      path.setAttribute('d', d)
      expect(() => collectEdges(geo, ctx())).toThrow()
    }
    path.setAttribute('d', 'M 0,0 L 0,10 L 10,10')
    document.querySelector('marker')!.remove()
    expect(() => collectEdges(geo, ctx())).toThrow(/marker/)
  })
  it('reads a rounded corner as its chord and rejects arcs the renderer never draws', () => {
    const path = pathDOM('M 0,0 L 0,4 A 6,6 0 0 0 6,10 L 10,10')
    // pathDOM translates the painted route by (20, 30). The corner is flagged so
    // a diagonal L chord can never pass for it.
    expect(collectEdges(geo, ctx())[0].segments).toEqual([
      { start: { x: 20, y: 30 }, end: { x: 20, y: 34 } },
      { start: { x: 20, y: 34 }, end: { x: 26, y: 40 }, corner: true },
      { start: { x: 26, y: 40 }, end: { x: 30, y: 40 } },
    ])
    for (const d of [
      'M 0,0 L 0,4 A 6,5 0 0 0 6,10',
      'M 0,0 L 0,4 A 6,6 15 0 0 6,10',
      'M 0,0 L 0,4 A 6,6 0 1 0 6,10',
      'M 0,0 L 0,4 A 6,6 0 0 2 6,10',
      // Down then right turns anticlockwise on screen: sweep 1 bulges the wrong way.
      'M 0,0 L 0,4 A 6,6 0 0 1 6,10',
      // The arc must leave tangent to the segment it continues.
      'M 0,0 L 0,4 A 6,6 0 0 0 6,-2',
      'M 0,0 L 0,4 A 6,6 0 0 0 8,10',
      'M 0,0 L 0,4 A 6,6 0 0 0',
      'M 0,0 A 0,0 0 0 0 0,0',
    ]) {
      path.setAttribute('d', d)
      expect(() => collectEdges(geo, ctx()), d).toThrow()
    }
  })
  it('cannot claim a crossing from absent paths', () => {
    document.body.innerHTML = ''
    expect(() => collectCrossings({ ...geo, crossings: [{ point: { x: 10, y: 10 }, over: 0, under: 1 }] }, ctx())).toThrow()
  })
})

describe('waitFor', () => {
  it('resolves true when the predicate becomes true and false on timeout', async () => {
    let n = 0
    await expect(waitFor(() => ++n > 2, window, 1000)).resolves.toBe(true)
    await expect(waitFor(() => false, window, 30)).resolves.toBe(false)
  })
})
