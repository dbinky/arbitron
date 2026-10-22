// Shared deterministic test support. Never imported by production code.
import type { WorkflowDefinition } from '../types'
import type { Geometry, LayoutView, Rect } from './types'
import { RENDERER_CONTRACT } from './validate'

/** a → b → c, plus c → b, saved as rev-1. */
export function fixtureWorkflow(): WorkflowDefinition {
  return {
    name: 'wf',
    description: '',
    author: '',
    nodes: [
      {
        id: 'a',
        type: 'script',
        label: 'Start',
        description: '',
        config: {},
        entrypoint: true,
      },
      {
        id: 'b',
        type: 'llm_call',
        label: 'Draft a very long label that must wrap',
        description: '',
        config: {},
      },
      {
        id: 'c',
        type: 'conditional',
        label: 'Check',
        description: '',
        config: {},
      },
    ],
    edges: [
      { source: 'a', target: 'b' },
      { source: 'b', target: 'c', output: 'yes' },
      { source: 'c', target: 'b', label: 'again' },
    ],
    presentation: {
      revision: 'rev-1',
      savedAt: '2026-10-16T00:00:00Z',
      reason: 'update',
      scope: 'private',
      name: 'wf',
      retained: { source: 'none' },
    },
  }
}

/** Distinctive non-dagre coordinates, with the server's bottom/top return ports. */
export function fixtureGeometry(): Geometry {
  const geo:Geometry = {
    format: 'arbitron.layout/v2',
    contract: { ...RENDERER_CONTRACT },
    options: {
      rankSpacing: 48,
      nodeSpacing: 56,
      laneSeparation: 14,
      gutterWidth: 48,
      labelGap: 10,
      portSpacing: 24,
      nodeTextSlack: 0,
      dummyWidth: 24,
      crossingSweeps: 8,
      margin: 40,
    },
    nodes: [
      {
        id: 'a',
        box: { x: 40, y: 40, w: 180, h: 52 },
        text: 'Start',
        typeCaption: 'script',
        textLines: 1,
        rank: 0,
      },
      {
        id: 'b',
        box: { x: 40, y: 140, w: 200, h: 70 },
        text: 'Draft a very long label that must wrap',
        typeCaption: 'llm call',
        textLines: 2,
        rank: 1,
      },
      {
        id: 'c',
        box: { x: 40, y: 260, w: 180, h: 52 },
        text: 'Check',
        typeCaption: 'conditional',
        textLines: 1,
        rank: 2,
      },
    ],
    edges: [
      {
        ordinal: 0,
        source: 'a',
        target: 'b',
        kind: 'forward',
        points: [
          { x: 130, y: 92 },
          { x: 130, y: 116 },
          { x: 140, y: 116 },
          { x: 140, y: 140 },
        ],
        sourceAttach: { nodeId: 'a', side: 'bottom', point: { x: 130, y: 92 } },
        targetAttach: { nodeId: 'b', side: 'top', point: { x: 140, y: 140 } },
      },
      {
        ordinal: 1,
        source: 'b',
        target: 'c',
        output: 'yes',
        kind: 'forward',
        points: [
          { x: 140, y: 210 },
          { x: 140, y: 235 },
          { x: 130, y: 235 },
          { x: 130, y: 260 },
        ],
        sourceAttach: {
          nodeId: 'b',
          side: 'bottom',
          point: { x: 140, y: 210 },
        },
        targetAttach: { nodeId: 'c', side: 'top', point: { x: 130, y: 260 } },
        text: { text: 'yes', box: { x: 146, y: 222, w: 30, h: 21 }, lines: 1 },
      },
      {
        ordinal: 2,
        source: 'c',
        target: 'b',
        label: 'again',
        kind: 'loop_return',
        side: 'right',
        lane: 1,
        points: [
          { x: 130, y: 312 },
          { x: 130, y: 330 },
          { x: 268, y: 330 },
          { x: 268, y: 116 },
          { x: 140, y: 116 },
          { x: 140, y: 140 },
        ],
        sourceAttach: {
          nodeId: 'c',
          side: 'bottom',
          point: { x: 130, y: 312 },
        },
        targetAttach: { nodeId: 'b', side: 'top', point: { x: 140, y: 140 } },
        text: {
          text: 'again',
          box: { x: 274, y: 220, w: 44, h: 21 },
          lines: 1,
        },
      },
    ],
    crossings: [],
    canvas: { x: 0, y: 0, w: 360, h: 352 },
  }
  for(const e of geo.edges){e.sourceAttach.elementId=JSON.stringify(['node',e.source]);e.targetAttach.elementId=JSON.stringify(['node',e.target])}
  return geo
}

/** Move/resize fixture nodes without creating stale ports, paths or canvas bounds. */
export function fixtureGeometryWithBoxes(boxes: Record<string, Rect>): Geometry {
  const geo = fixtureGeometry()
  if (Object.keys(boxes).length === 0) return geo
  for (const n of geo.nodes) {
    if (boxes[n.id]) n.box = { ...boxes[n.id] }
  }
  const byId = new Map(geo.nodes.map(n => [n.id, n.box]))
  const right = Math.max(...geo.nodes.map(n => n.box.x + n.box.w))
  for (const e of geo.edges) {
    const source = byId.get(e.source)!,
      target = byId.get(e.target)!
    const start = { x: source.x + source.w / 2, y: source.y + source.h }
    const end = { x: target.x + target.w / 2, y: target.y }
    e.sourceAttach.point = { ...start }
    e.targetAttach.point = { ...end }
    const midY = (start.y + end.y) / 2
    if (e.kind === 'loop_return') {
      const gutter = right + geo.options.gutterWidth + (e.lane ?? 1) * geo.options.laneSeparation
      const stub = geo.options.portSpacing / 2
      e.points = [
        start,
        { x: start.x, y: start.y + stub },
        { x: gutter, y: start.y + stub },
        { x: gutter, y: end.y - stub },
        { x: end.x, y: end.y - stub },
        end,
      ]
      if (e.text) {
        e.text.box.x = gutter + geo.options.labelGap
        e.text.box.y = Math.max(0, midY - e.text.box.h / 2)
      }
    } else {
      e.points = [start, { x: start.x, y: midY }, { x: end.x, y: midY }, end]
      if (e.text) {
        e.text.box.x = Math.max(start.x, end.x) + geo.options.labelGap
        e.text.box.y = Math.max(0, midY - e.text.box.h / 2)
      }
    }
    e.points = e.points.filter(
      (p, i, points) => i === 0 || p.x !== points[i - 1].x || p.y !== points[i - 1].y,
    )
  }
  const extents = geo.nodes.map(n => ({
    x: n.box.x + n.box.w,
    y: n.box.y + n.box.h,
  }))
  for (const e of geo.edges) {
    extents.push(...e.points)
    if (e.text)
      extents.push({
        x: e.text.box.x + e.text.box.w,
        y: e.text.box.y + e.text.box.h,
      })
  }
  geo.canvas.w = Math.max(geo.canvas.w, ...extents.map(p => p.x + geo.options.margin))
  geo.canvas.h = Math.max(geo.canvas.h, ...extents.map(p => p.y + geo.options.margin))
  return geo
}

export function fixtureView(overrides: Partial<LayoutView> = {}): LayoutView {
  return {
    savedRevision: 'rev-1',
    jobGeneration: 1,
    resultGeneration: 1,
    state: 'passed',
    displayStatus: 'complete',
    stage: 'done',
    attemptsUsed: 1,
    attemptLimit: 5,
    findings: [],
    contract: { ...RENDERER_CONTRACT },
    geometry: fixtureGeometry(),
    provenance: 'arranged',
    unreviewed: false,
    retryEligible: false,
    available: true,
    ...overrides,
  }
}
