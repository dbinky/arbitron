/// <reference types="node" />
import { describe, it, expect } from 'vitest'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  validateGeometry,
  resolveDisplayGeometry,
  RENDERER_CONTRACT,
  GEOMETRY_FORMAT,
} from './validate'
import {
  fixtureGeometry,
  fixtureGeometryWithBoxes,
  fixtureView,
  fixtureWorkflow,
} from './testFixtures'
import type { Geometry, Rect, RendererContract } from './types'
import type { WorkflowDefinition } from '../types'

const def = fixtureWorkflow()

describe('validateGeometry', () => {
  it('uses the bundled renderer contract and accepts complete manifest-ordered geometry', () => {
    expect(RENDERER_CONTRACT.version).toBe('arranged-renderer/2')
    expect(RENDERER_CONTRACT.fontFamily).toBe('Inter, Noto Sans SC, Noto Emoji')
    expect(RENDERER_CONTRACT.nodeTextMaxWidth).toBe(240)
    expect(RENDERER_CONTRACT.loopReturnDash).toBe('6 4')
    expect(GEOMETRY_FORMAT).toBe('arbitron.layout/v2')
    expect(validateGeometry(def, fixtureGeometry(), RENDERER_CONTRACT)).toEqual({
      ok: true,
      issues: [],
    })
  })

  const cases: Array<[string, (g: Geometry) => void, string?]> = [
    [
      'version',
      g => {
        g.contract.version = 'arranged-renderer/1'
      },
      'renderer contract version "arranged-renderer/1" does not match "arranged-renderer/2"',
    ],
    [
      'format',
      g => {
        g.format = 'arbitron.layout/v0'
      },
      'format "arbitron.layout/v0" is not arbitron.layout/v2',
    ],
    [
      'missing node',
      g => {
        g.nodes.pop()
      },
      'geometry has 2 nodes, workflow has 3',
    ],
    [
      'node order',
      g => {
        ;[g.nodes[0], g.nodes[1]] = [g.nodes[1], g.nodes[0]]
      },
      'node 0 is "b", expected "a"',
    ],
    [
      'empty',
      g => {
        g.nodes = []
        g.edges = []
      },
      'geometry has 0 nodes, workflow has 3',
    ],
    [
      'extra node',
      g => {
        g.nodes.push(g.nodes[0])
      },
    ],
    [
      'extra edge',
      g => {
        g.edges.push(g.edges[0])
      },
    ],
    [
      'missing edge',
      g => {
        g.edges.pop()
      },
    ],
    [
      'edge endpoints',
      g => {
        g.edges[1].source = 'a'
      },
      'edge 1 routes a->c, expected b->c',
    ],
    [
      'ordinal',
      g => {
        g.edges[2].ordinal = 7
      },
      'edge 2 has ordinal 7',
    ],
    [
      'node NaN',
      g => {
        g.nodes[0].box.x = NaN
      },
      'node "a" has an invalid box',
    ],
    [
      'zero node width',
      g => {
        g.nodes[2].box.w = 0
      },
      'node "c" has an invalid box',
    ],
    [
      'negative node height',
      g => {
        g.nodes[0].box.h = -1
      },
    ],
    [
      'overflow rectangle',
      g => {
        g.nodes[0].box.x = Number.MAX_VALUE
        g.nodes[0].box.w = Number.MAX_VALUE
      },
    ],
    [
      'node outside canvas',
      g => {
        g.nodes[0].box.x = 500
      },
    ],
    [
      'node full text',
      g => {
        g.nodes[1].text = 'Draft…'
      },
    ],
    [
      'node type',
      g => {
        g.nodes[1].typeCaption = 'llm_call'
      },
    ],
    [
      'zero text lines',
      g => {
        g.nodes[0].textLines = 0
      },
    ],
    [
      'fractional text lines',
      g => {
        g.nodes[0].textLines = 1.5
      },
    ],
    [
      'negative rank',
      g => {
        g.nodes[0].rank = -1
      },
    ],
    [
      'fractional rank',
      g => {
        g.nodes[0].rank = 0.5
      },
    ],
    [
      'same-version font',
      g => {
        g.contract.fontFamily = 'serif'
      },
    ],
    [
      'same-version padding',
      g => {
        g.contract.nodePaddingX++
      },
    ],
    [
      'missing contract field',
      g => {
        delete (g.contract as Partial<RendererContract>).fontPackage
      },
    ],
    [
      'extra contract field',
      g => {
        Object.assign(g.contract, { other: 1 })
      },
    ],
    [
      'nonfinite contract',
      g => {
        g.contract.lineHeight = Infinity
      },
    ],
    [
      'changed output',
      g => {
        g.edges[1].output = 'no'
      },
    ],
    [
      'changed label',
      g => {
        g.edges[2].label = 'changed'
      },
    ],
    [
      'changed branch text',
      g => {
        g.edges[1].text!.text = 'no'
      },
    ],
    [
      'missing branch text',
      g => {
        delete g.edges[1].text
      },
    ],
    [
      'unexpected branch text',
      g => {
        g.edges[0].text = g.edges[1].text
      },
    ],
    [
      'zero label lines',
      g => {
        g.edges[1].text!.lines = 0
      },
    ],
    [
      'fractional label lines',
      g => {
        g.edges[1].text!.lines = 1.5
      },
    ],
    [
      'nonfinite label',
      g => {
        g.edges[1].text!.box.h = Infinity
      },
    ],
    [
      'label outside canvas',
      g => {
        g.edges[1].text!.box.y = 500
      },
    ],
    [
      'short route',
      g => {
        g.edges[0].points = [{ x: 1, y: 1 }]
      },
      'edge 0 has an invalid route',
    ],
    [
      'nonfinite route',
      g => {
        g.edges[1].points[1].x = Infinity
      },
      'edge 1 has an invalid route',
    ],
    [
      'diagonal route',
      g => {
        g.edges[0].points[1].x += 0.1
      },
    ],
    [
      'negative half-pixel quantization matches Go rounding',
      g => {
        g.nodes[0].box.x = -0.01
        g.nodes[0].box.w = 0.02
        g.edges[0].points[0].x = -0.005
        g.edges[0].sourceAttach.point.x = -0.005
        g.edges[0].points[1].x = -0.004
      },
    ],
    [
      'zero segment',
      g => {
        g.edges[0].points.splice(1, 0, g.edges[0].points[0])
      },
    ],
    [
      'route outside canvas',
      g => {
        g.edges[0].points[1].x = -1
      },
    ],
    [
      'wrong attachment node',
      g => {
        g.edges[0].sourceAttach.nodeId = 'b'
      },
    ],
    [
      'wrong attachment side',
      g => {
        g.edges[0].sourceAttach.side = 'top'
      },
    ],
    [
      'attachment endpoint mismatch',
      g => {
        g.edges[0].targetAttach.point.x++
      },
    ],
    [
      'off boundary',
      g => {
        g.edges[0].points[0].y--
        g.edges[0].sourceAttach.point.y--
      },
    ],
    [
      'corner',
      g => {
        g.edges[0].points[0].x = 40
        g.edges[0].sourceAttach.point.x = 40
      },
    ],
    [
      'unknown kind',
      g => {
        g.edges[0].kind = 'other' as Geometry['edges'][number]['kind']
      },
    ],
    [
      'self-loop mismatch',
      g => {
        g.edges[0].kind = 'self_loop'
        g.edges[0].side = 'right'
      },
    ],
    [
      'forward side',
      g => {
        g.edges[0].side = 'right'
      },
    ],
    [
      'forward lane',
      g => {
        g.edges[0].lane = 1
      },
    ],
    [
      'null lane',
      g => {
        Object.assign(g.edges[0], { lane: null })
      },
    ],
    [
      'return side',
      g => {
        g.edges[2].side = 'top'
      },
    ],
    [
      'return lane',
      g => {
        g.edges[2].lane = 0
      },
    ],
    [
      'fractional lane',
      g => {
        g.edges[2].lane = 1.5
      },
    ],
    [
      'nonfinite options',
      g => {
        g.options.rankSpacing = Infinity
      },
    ],
    [
      'missing options',
      g => {
        delete g.options.margin
      },
    ],
    [
      'negative slack',
      g => {
        g.options.nodeTextSlack = -1
      },
    ],
    [
      'fractional sweeps',
      g => {
        g.options.crossingSweeps = 0.5
      },
    ],
    [
      'zero canvas',
      g => {
        g.canvas.w = 0
      },
      'canvas is invalid',
    ],
    [
      'nonfinite canvas',
      g => {
        g.canvas.w = NaN
      },
      'canvas is invalid',
    ],
    [
      'nonzero origin',
      g => {
        g.canvas.x = 0.001
      },
    ],
    [
      'unknown crossing',
      g => {
        g.crossings = [{ point: { x: 1, y: 1 }, over: 0, under: 9 }]
      },
      'crossing 0 references an unknown edge',
    ],
    [
      'same crossing edge',
      g => {
        g.crossings = [{ point: { x: 130, y: 116 }, over: 0, under: 0 }]
      },
    ],
    [
      'crossing off routes',
      g => {
        g.crossings = [{ point: { x: 140, y: 230 }, over: 1, under: 0 }]
      },
    ],
    [
      'crossing at bend',
      g => {
        g.crossings = [{ point: { x: 140, y: 116 }, over: 2, under: 0 }]
      },
    ],
  ]
  it.each(cases)('rejects %s at the display sink', (_name, mutate, issue) => {
    const geo = fixtureGeometry()
    mutate(geo)
    const result = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(result.ok).toBe(false)
    expect(result.issues.length).toBeGreaterThan(0)
    if (issue) expect(result.issues).toContain(issue)
    expect(resolveDisplayGeometry(def, fixtureView({ geometry: geo }))).toEqual({
      geometry: null,
      issues: result.issues,
    })
  })

  it.each([
    (wf: WorkflowDefinition) => {
      wf.nodes[0].label = 'changed'
    },
    (wf: WorkflowDefinition) => {
      wf.nodes[1].type = 'tool_call'
    },
    (wf: WorkflowDefinition) => {
      wf.edges[0].source = 'b'
    },
    (wf: WorkflowDefinition) => {
      wf.edges[0].target = 'c'
    },
    (wf: WorkflowDefinition) => {
      wf.edges[1].output = 'changed'
    },
    (wf: WorkflowDefinition) => {
      wf.edges[2].label = 'changed'
    },
  ])('depends on the actual manifest text and each edge occurrence (%#)', mutate => {
    const wf = fixtureWorkflow()
    mutate(wf)
    const r = resolveDisplayGeometry(wf, fixtureView())
    expect(r.geometry).toBeNull()
    expect(r.issues.length).toBeGreaterThan(0)
  })

  it('does not mutate any supplied workflow, geometry, contract or fixture boxes', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry(),
      contract = { ...RENDERER_CONTRACT }
    const before = JSON.stringify({ wf, geo, contract })
    expect(validateGeometry(wf, geo, contract).ok).toBe(true)
    expect(JSON.stringify({ wf, geo, contract })).toBe(before)
    const boxes = { b: { x: 400, y: 180, w: 300, h: 200 } }
    const originalBoxes = JSON.stringify(boxes)
    const moved = fixtureGeometryWithBoxes(boxes)
    moved.nodes[1].box.x++
    expect(JSON.stringify(boxes)).toBe(originalBoxes)
    geo.nodes[0].text = 'corrupt'
    const corruptBefore = JSON.stringify({ wf, geo, contract })
    expect(validateGeometry(wf, geo, contract).ok).toBe(false)
    expect(JSON.stringify({ wf, geo, contract })).toBe(corruptBefore)
  })

  it('normalizes Go omitempty strings and preserves identical parallel occurrences', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry()
    geo.edges[0].output = ''
    geo.edges[0].label = ''
    wf.edges.push({ ...wf.edges[0] })
    geo.edges.push({ ...geo.edges[0], ordinal: 3 })
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT).ok).toBe(true)
    geo.edges.pop()
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT).ok).toBe(false)
  })

  it('uses label before output and label/id fallback without dropping Unicode/newlines', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry()
    wf.nodes[0].label = ''
    geo.nodes[0].text = 'a'
    wf.nodes[1].label = '中文 e\u0301\n↻'
    geo.nodes[1].text = wf.nodes[1].label
    wf.edges[1].label = '优先'
    geo.edges[1].label = '优先'
    geo.edges[1].text!.text = '优先'
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT).ok).toBe(true)
  })

  it('does not accept a custom expected contract even when it matches the payload', () => {
    const geo = fixtureGeometry()
    geo.contract.nodePaddingX++
    expect(validateGeometry(def, geo, geo.contract).ok).toBe(false)
  })

  it.each([null, {}, [], 'bad'])(
    'rejects malformed workflow/expected contract %j without throwing',
    value => {
      expect(
        validateGeometry(value as WorkflowDefinition, fixtureGeometry(), RENDERER_CONTRACT).ok,
      ).toBe(false)
      expect(validateGeometry(def, fixtureGeometry(), value as RendererContract).ok).toBe(false)
    },
  )

  it('rejects duplicate/empty workflow IDs, dangling edges and malformed workflow text', () => {
    for (const mutate of [
      (wf: WorkflowDefinition) => {
        wf.nodes[1].id = 'a'
      },
      (wf: WorkflowDefinition) => {
        wf.nodes[0].id = ''
      },
      (wf: WorkflowDefinition) => {
        wf.edges[0].target = 'absent'
      },
      (wf: WorkflowDefinition) => {
        Object.assign(wf.nodes[0], { label: 5 })
      },
    ]) {
      const wf = fixtureWorkflow()
      mutate(wf)
      expect(validateGeometry(wf, fixtureGeometry(), RENDERER_CONTRACT).ok).toBe(false)
    }
  })

  it('reports null geometry and missing arrays explicitly', () => {
    expect(validateGeometry(def, null as unknown as Geometry, RENDERER_CONTRACT)).toEqual({
      ok: false,
      issues: ['geometry is missing'],
    })
    const result = validateGeometry(
      def,
      { format: GEOMETRY_FORMAT, contract: RENDERER_CONTRACT } as Geometry,
      RENDERER_CONTRACT,
    )
    expect(result.issues).toEqual(
      expect.arrayContaining([
        'geometry has no node list',
        'geometry has no edge list',
        'geometry has no crossing list',
      ]),
    )
  })

  it.each([
    ['nodes', [null]],
    ['edges', [null]],
    ['crossings', [null]],
    ['nodes', 'bad'],
    ['options', null],
    ['canvas', null],
    ['contract', null],
  ])('rejects malformed %s without throwing', (key, value) => {
    const geo = { ...fixtureGeometry(), [key as string]: value }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).ok).toBe(false)
  })

  it.each(['points', 'sourceAttach', 'targetAttach', 'text'])(
    'rejects malformed edge %s without throwing',
    key => {
      const geo = fixtureGeometry()
      Object.assign(geo.edges[1], { [key]: null })
      expect(validateGeometry(def, geo, RENDERER_CONTRACT).ok).toBe(false)
    },
  )
})

describe('malformed JSON scalar fields', () => {
  const malformed = JSON.parse('{"toString":null,"valueOf":null}') as unknown
  it.each([
    (g: Geometry) => Object.assign(g, { format: malformed }),
    (g: Geometry) => Object.assign(g.contract, { version: malformed }),
    (g: Geometry) => Object.assign(g.nodes[0], { id: malformed }),
    (g: Geometry) => Object.assign(g.edges[0], { ordinal: malformed }),
    (g: Geometry) => Object.assign(g.edges[0], { source: malformed }),
    (g: Geometry) => Object.assign(g.edges[2], { side: malformed }),
  ])('rejects non-coercible JSON objects without throwing (%#)', mutate => {
    const geo = fixtureGeometry()
    mutate(geo)
    expect(resolveDisplayGeometry(def, fixtureView({ geometry: geo })).geometry).toBeNull()
  })

  it('reports a malformed saved revision without coercing untrusted objects', () => {
    const view = fixtureView()
    Object.assign(view, { savedRevision: malformed })
    expect(resolveDisplayGeometry(def, view).geometry).toBeNull()
  })
})

describe('resolveDisplayGeometry', () => {
  it.each([0, false, ''])(
    'reports malformed present geometry instead of silently treating %j as absent',
    value => {
      const view = fixtureView({ displayStatus: 'pending' })
      Object.assign(view, { geometry: value })
      const r = resolveDisplayGeometry(def, view)
      expect(r.geometry).toBeNull()
      expect(r.issues).toEqual(['geometry is missing'])
    },
  )
  it('returns the exact geometry, not a substitute layout', () => {
    const view = fixtureView()
    expect(resolveDisplayGeometry(def, view).geometry).toBe(view.geometry)
    expect(resolveDisplayGeometry(def, view).issues).toEqual([])
  })
  it('accepts same-token absent geometry only without a complete claim', () => {
    expect(resolveDisplayGeometry(def, null)).toEqual({
      geometry: null,
      issues: [],
    })
    expect(
      resolveDisplayGeometry(def, fixtureView({ geometry: undefined, displayStatus: 'pending' })),
    ).toEqual({ geometry: null, issues: [] })
    const r = resolveDisplayGeometry(def, fixtureView({ geometry: undefined }))
    expect(r.geometry).toBeNull()
    expect(r.issues.join(' ')).toMatch(/missing.*reviewed geometry/i)
  })
  it.each([true, false])(
    'rejects foreign revision even with identical node IDs or positions only (%s)',
    withGeometry => {
      const view = fixtureView({
        savedRevision: 'rev-other',
        geometry: withGeometry ? fixtureGeometry() : undefined,
        retained: { source: 'manifest', positions: { a: { x: 1, y: 2 } } },
      })
      const r = resolveDisplayGeometry(def, view)
      expect(r.geometry).toBeNull()
      expect(r.issues.join(' ')).toMatch(/revision.*rev-other.*rev-1/)
    },
  )
  it('supports legacy tokens but rejects a saved token joined to a legacy manifest', () => {
    const wf = fixtureWorkflow()
    delete wf.presentation
    expect(resolveDisplayGeometry(wf, fixtureView({ savedRevision: '' })).geometry).not.toBeNull()
    expect(resolveDisplayGeometry(wf, fixtureView()).geometry).toBeNull()
  })
})

describe('server geometry parity and fixture transforms', () => {
  it.each(Object.keys(RENDERER_CONTRACT))('rejects any same-version change to contract.%s', key => {
    const geo = fixtureGeometry()
    const fields = geo.contract as unknown as Record<string, unknown>
    fields[key] =
      typeof fields[key] === 'number'
        ? Number(fields[key]) + 0.01
        : `${String(fields[key])}/changed`
    expect(resolveDisplayGeometry(def, fixtureView({ geometry: geo })).geometry).toBeNull()
  })

  it.each(Object.keys(fixtureGeometry().options))(
    'rejects negative/nonfinite/missing options.%s',
    key => {
      for (const value of [-1, NaN, Infinity, undefined]) {
        const geo = fixtureGeometry()
        if (value === undefined) delete geo.options[key]
        else geo.options[key] = value
        expect(validateGeometry(def, geo, RENDERER_CONTRACT).ok).toBe(false)
      }
    },
  )

  it.each(Object.keys(fixtureGeometry().options))('allows zero only for policy options.%s', key => {
    const geo = fixtureGeometry()
    Object.assign(geo.options, { margin: 1, nodeTextSlack: 1, crossingSweeps: 1 })
    geo.options[key] = 0
    const allowed = ['margin', 'nodeTextSlack', 'crossingSweeps'].includes(key)
    const view = fixtureView({ geometry: geo })
    const displayed = resolveDisplayGeometry(def, view)
    expect(displayed.geometry).toBe(allowed ? geo : null)
    expect(displayed.issues).toEqual(allowed ? [] : ['geometry options are invalid'])
  })

  it('mirrors the server 1/100px precision', () => {
    const geo = fixtureGeometry()
    geo.edges[0].points[1].x += 0.004
    expect(validateGeometry(def, geo, RENDERER_CONTRACT)).toEqual({
      ok: true,
      issues: [],
    })
  })

  it('accepts an isolated node with an empty type caption, without requiring an edge', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry()
    wf.nodes = [wf.nodes[0]]
    wf.nodes[0].type = ''
    wf.edges = []
    geo.nodes = [geo.nodes[0]]
    geo.nodes[0].typeCaption = ''
    geo.edges = []
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT)).toEqual({
      ok: true,
      issues: [],
    })
  })

  it('validates right-side self loops and rejects endpoint/kind/side/lane inconsistencies', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry()
    wf.edges = [{ source: 'a', target: 'a' }]
    geo.edges = [
      {
        ordinal: 0,
        source: 'a',
        target: 'a',
        kind: 'self_loop',
        side: 'right',
        points: [
          { x: 220, y: 55 },
          { x: 250, y: 55 },
          { x: 250, y: 75 },
          { x: 220, y: 75 },
        ],
        sourceAttach: { nodeId: 'a', elementId:JSON.stringify(['node','a']), side: 'right', point: { x: 220, y: 55 } },
        targetAttach: { nodeId: 'a', elementId:JSON.stringify(['node','a']), side: 'right', point: { x: 220, y: 75 } },
      },
    ]
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT).ok).toBe(true)
    for (const change of [
      { kind: 'forward' },
      { kind: 'loop_return', lane: 1 },
      { side: 'left' },
      { lane: -1 },
    ]) {
      const changed = structuredClone(geo)
      Object.assign(changed.edges[0], change)
      expect(validateGeometry(wf, changed, RENDERER_CONTRACT).ok).toBe(false)
    }
  })

  it('requires proper crossing orientation/interiors/uniqueness but leaves missing crossings to preflight', () => {
    const wf = fixtureWorkflow(),
      geo = fixtureGeometry()
    wf.nodes = ['a', 'b', 'c', 'd'].map(id => ({
      id,
      label: id,
      type: 'script',
      config: {},
      description: '',
    }))
    wf.edges = [
      { source: 'a', target: 'b' },
      { source: 'c', target: 'd' },
    ]
    geo.nodes = [
      {
        id: 'a',
        box: { x: 40, y: 40, w: 180, h: 60 },
        text: 'a',
        typeCaption: 'script',
        textLines: 1,
        rank: 0,
      },
      {
        id: 'b',
        box: { x: 400, y: 200, w: 180, h: 60 },
        text: 'b',
        typeCaption: 'script',
        textLines: 1,
        rank: 1,
      },
      {
        id: 'c',
        box: { x: 240, y: 40, w: 180, h: 60 },
        text: 'c',
        typeCaption: 'script',
        textLines: 1,
        rank: 0,
      },
      {
        id: 'd',
        box: { x: 240, y: 200, w: 180, h: 60 },
        text: 'd',
        typeCaption: 'script',
        textLines: 1,
        rank: 1,
      },
    ]
    geo.edges = [
      {
        ordinal: 0,
        source: 'a',
        target: 'b',
        kind: 'forward',
        points: [
          { x: 130, y: 100 },
          { x: 130, y: 150 },
          { x: 490, y: 150 },
          { x: 490, y: 200 },
        ],
        sourceAttach: {
          nodeId: 'a',elementId:JSON.stringify(['node','a']),
          side: 'bottom',
          point: { x: 130, y: 100 },
        },
        targetAttach: { nodeId: 'b', elementId:JSON.stringify(['node','b']), side: 'top', point: { x: 490, y: 200 } },
      },
      {
        ordinal: 1,
        source: 'c',
        target: 'd',
        kind: 'forward',
        points: [
          { x: 330, y: 100 },
          { x: 330, y: 200 },
        ],
        sourceAttach: {
          nodeId: 'c',elementId:JSON.stringify(['node','c']),
          side: 'bottom',
          point: { x: 330, y: 100 },
        },
        targetAttach: { nodeId: 'd', elementId:JSON.stringify(['node','d']), side: 'top', point: { x: 330, y: 200 } },
      },
    ]
    geo.canvas = { x: 0, y: 0, w: 620, h: 300 }
    geo.crossings = [{ point: { x: 330, y: 150 }, over: 0, under: 1 }]
    expect(validateGeometry(wf, geo, RENDERER_CONTRACT)).toEqual({
      ok: true,
      issues: [],
    })
    for (const crossings of [
      [geo.crossings[0], geo.crossings[0]],
      [{ point: { x: 330, y: 150 }, over: 1, under: 0 }],
      [{ point: { x: 330, y: 100 }, over: 0, under: 1 }],
      [{ point: { x: NaN, y: 150 }, over: 0, under: 1 }],
    ]) {
      expect(validateGeometry(wf, { ...geo, crossings }, RENDERER_CONTRACT).ok).toBe(false)
    }
    expect(validateGeometry(wf, { ...geo, crossings: [] }, RENDERER_CONTRACT).ok).toBe(true)
  })

  it.each<Record<string, Rect>>([
    { b: { x: 400, y: 180, w: 300, h: 200 } },
    {
      a: { x: 100, y: 50, w: 210, h: 60 },
      c: { x: 600, y: 600, w: 220, h: 90 },
    },
    {
      a: { x: 60, y: 30, w: 200, h: 52 },
      b: { x: 300, y: 160, w: 240, h: 100 },
      c: { x: 40, y: 450, w: 180, h: 52 },
    },
  ])('moves boxes together with their actual endpoints, routes, labels and canvas (%j)', boxes => {
    const geo = fixtureGeometryWithBoxes(boxes)
    for (const [id, box] of Object.entries(boxes))
      expect(geo.nodes.find(n => n.id === id)?.box).toEqual(box)
    expect(validateGeometry(def, geo, RENDERER_CONTRACT)).toEqual({
      ok: true,
      issues: [],
    })
    expect(geo.canvas.w).toBeGreaterThanOrEqual(Math.max(...geo.nodes.map(n => n.box.x + n.box.w)))
    expect(geo.canvas.h).toBeGreaterThanOrEqual(Math.max(...geo.nodes.map(n => n.box.y + n.box.h)))
  })

  // Test-only measurements establish wire/geometry parity, not rendered acceptance.
  it.each(['default', 'zero policy values'])(
    'validates all three Go Arrange samples with %s',
    policy => {
      const dir = mkdtempSync(join(tmpdir(), 'arbitron-layout-parity-'))
      try {
        const main = join(dir, 'main.go')
        writeFileSync(
          main,
          `package main
import (
  "context"
  "encoding/json"
  "os"
  "path/filepath"
  "github.com/arbitron/arbitron/domain/layout"
  "github.com/arbitron/arbitron/domain/layout/layouttest"
  "github.com/arbitron/arbitron/domain/workflow"
)
func check(err error) { if err != nil { panic(err) } }
func main() {
  out := map[string]map[string]any{}
  for _, name := range []string{"pr-review", "feature-pipeline", "ralph-loop"} {
    raw, err := os.ReadFile(filepath.Join("samples", name, "workflow.json")); check(err)
    var wf workflow.Workflow; check(json.Unmarshal(raw, &wf))
    graph, err := layout.BuildGraph(&wf); check(err)
    c := layout.DefaultContract()
    options := layout.DefaultOptions()
    ${policy === 'zero policy values' ? 'options.Margin, options.NodeTextSlack, options.CrossingSweeps = 0, 0, 0' : ''}
    geo, err := layout.Arrange(context.Background(), graph, layouttest.Measurements(graph, c), c, options); check(err)
    if !layout.Compatible(geo, graph) { panic("Go rejected its own candidate") }
    out[name] = map[string]any{"workflow": wf, "geometry": geo}
  }
  check(json.NewEncoder(os.Stdout).Encode(out))
}
`,
        )
        const raw = execFileSync('go', ['run', main], {
          cwd: resolve('..'),
          encoding: 'utf8',
          timeout: 60_000,
        })
        const samples = JSON.parse(raw) as Record<
          string,
          { workflow: WorkflowDefinition; geometry: Geometry }
        >
        expect(Object.keys(samples).sort()).toEqual(['feature-pipeline', 'pr-review', 'ralph-loop'])
        for (const [name, sample] of Object.entries(samples)) {
          const manifest = JSON.parse(
            readFileSync(resolve('..', 'samples', name, 'workflow.json'), 'utf8'),
          ) as WorkflowDefinition
          const identity = (wf: WorkflowDefinition) => ({
            nodes: wf.nodes.map(n => [n.id, n.type, n.label || '']),
            edges: wf.edges.map(e => [e.source, e.target, e.output || '', e.label || '']),
          })
          expect(identity(sample.workflow), name).toEqual(identity(manifest))
          expect(
            validateGeometry(manifest, { ...sample.geometry, nodes: [] }, RENDERER_CONTRACT).ok,
            name,
          ).toBe(false)
          if (policy === 'zero policy values') {
            expect(sample.geometry.options, name).toMatchObject({
              margin: 0,
              nodeTextSlack: 0,
              crossingSweeps: 0,
            })
          }
          expect(validateGeometry(manifest, sample.geometry, RENDERER_CONTRACT), name).toEqual({
            ok: true,
            issues: [],
          })
          const view = fixtureView({
            savedRevision: manifest.presentation?.revision ?? '',
            geometry: sample.geometry,
          })
          const displayed = resolveDisplayGeometry(manifest, view)
          expect(displayed.geometry, name).toBe(sample.geometry)
          expect(displayed.issues, name).toEqual([])
          for (const key of ['margin', 'nodeTextSlack', 'crossingSweeps']) {
            const invalidValues = [-1, NaN, -Infinity, Infinity]
            if (key === 'crossingSweeps') invalidValues.push(0.5)
            for (const invalid of invalidValues) {
              const geometry = structuredClone(sample.geometry)
              geometry.options[key] = invalid
              expect(
                resolveDisplayGeometry(manifest, { ...view, geometry }),
                `${name}: ${key}=${invalid}`,
              ).toEqual({
                geometry: null,
                issues: ['geometry options are invalid'],
              })
            }
          }
          const empty = { ...sample.geometry, nodes: [], edges: [] }
          expect(
            resolveDisplayGeometry(manifest, { ...view, geometry: empty }).geometry,
            name,
          ).toBeNull()
          expect(
            resolveDisplayGeometry(manifest, { ...view, geometry: undefined }).issues,
            name,
          ).toEqual(['missing reviewed geometry for complete layout'])
        }
      } finally {
        rmSync(dir, { recursive: true, force: true })
      }
    },
    90_000,
  )
})
