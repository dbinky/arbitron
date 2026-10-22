/// <reference types="node" />
import { render } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { describe, it, expect, vi } from 'vitest'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { WorkflowDefinition } from '../types'
import type { Geometry, Point } from './types'
import { RENDERER_CONTRACT, validateGeometry } from './validate'
import { buildArrangedFlow } from './buildArrangedFlow'

vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => createPortal(children, document.body),
  }
})

import RoutedEdge from './RoutedEdge'

/** Actual Go Arrange output for the bundled samples (test-only measurements). */
function goArrangedSamples(): Record<string, { workflow: WorkflowDefinition; geometry: Geometry }> {
  const dir = mkdtempSync(join(tmpdir(), 'arbitron-crossing-seam-'))
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
    geo, err := layout.Arrange(context.Background(), graph, layouttest.Measurements(graph, c), c, layout.DefaultOptions()); check(err)
    out[name] = map[string]any{"workflow": wf, "geometry": geo}
  }
  check(json.NewEncoder(os.Stdout).Encode(out))
}
`,
    )
    return JSON.parse(execFileSync('go', ['run', main], { cwd: resolve('..'), encoding: 'utf8', timeout: 60_000 }))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

type Seg = { a: Point; b: Point; from: number }

/** Subpaths of an M/L path; the dash phase restarts at every M, as in Chrome. */
function subpaths(d: string): Seg[][] {
  const out: Seg[][] = []
  let at: Point | null = null
  let dist = 0
  for (const m of d.matchAll(/([ML]) (-?[\d.]+),(-?[\d.]+)/g)) {
    const p = { x: Number(m[2]), y: Number(m[3]) }
    if (m[1] === 'M') {
      out.push([])
      dist = 0
    } else {
      out[out.length - 1].push({ a: at!, b: p, from: dist })
      dist += Math.hypot(p.x - at!.x, p.y - at!.y)
    }
    at = p
  }
  return out
}

/** Whether the stroke paints p: on a drawn segment and, when dashed, in a dash. */
function inked(p: Point, d: string, dash: number[]): boolean {
  const period = dash.reduce((s, v) => s + v, 0)
  for (const sp of subpaths(d))
    for (const s of sp) {
      const dx = s.b.x - s.a.x, dy = s.b.y - s.a.y, len = Math.hypot(dx, dy)
      const along = ((p.x - s.a.x) * dx + (p.y - s.a.y) * dy) / len
      if (along < 0 || along > len || Math.abs((p.x - s.a.x) * dy - (p.y - s.a.y) * dx) / len > 0.1) continue
      if (!period) return true
      let phase = (s.from + along) % period
      for (let i = 0; i < dash.length; i++) {
        if (phase <= dash[i]) {
          if (i % 2 === 0) return true
          break
        }
        phase -= dash[i]
      }
    }
  return false
}

describe('Go geometry crossings → RoutedEdge DOM (paired:19 seam)', () => {
  // The Go writer puts Over on a horizontal and Under on a vertical segment
  // (domain/layout/route.go:findCrossings); report.ts:collectCrossings probes
  // the same orientation. Every recorded crossing must therefore render with a
  // hole on the under edge, inked banks, and an over edge inked across the span.
  it('renders every crossing of actual Go Arrange output unambiguously, keyed by ordinal and role', () => {
    const samples = goArrangedSamples()
    const half = RENDERER_CONTRACT.crossingGapPx / 2
    const roles = new Set<string>()
    for (const [name, { workflow, geometry: geo }] of Object.entries(samples)) {
      expect(validateGeometry(workflow, geo, RENDERER_CONTRACT), name).toEqual({ ok: true, issues: [] })
      const types = Object.fromEntries(workflow.nodes.map(n => [n.id, n.type]))
      const { edges } = buildArrangedFlow(geo, types, {}, `seam-${name}`)
      const { container } = render(
        <svg>
          {edges.map(e => (
            <RoutedEdge key={e.id} {...({ id: e.id, source: e.source, target: e.target, data: e.data } as unknown as Parameters<typeof RoutedEdge>[0])} />
          ))}
        </svg>,
      )
      const path = (ordinal: number) => {
        const found = container.querySelectorAll<SVGPathElement>(`path[data-edge-path="${ordinal}"]`)
        expect(found.length, `${name} edge ${ordinal}`).toBe(1)
        const dash = found[0].style.strokeDasharray
        return { d: found[0].getAttribute('d') ?? '', dash: dash ? dash.split(/[ ,]+/).map(v => parseFloat(v)) : [] }
      }
      for (const c of geo.crossings) {
        const where = `${name} crossing over ${c.over} under ${c.under} at ${c.point.x},${c.point.y}`
        const under = path(c.under), over = path(c.over)
        for (let v = -half + 0.2; v < half - 0.1; v += 0.5)
          expect(inked({ x: c.point.x, y: c.point.y + v }, under.d, under.dash), `${where}: under ink at ${v}`).toBe(false)
        for (const sign of [-1, 1])
          expect(inked({ x: c.point.x, y: c.point.y + sign * (half + 0.5) }, under.d, under.dash), `${where}: under bank ${sign}`).toBe(true)
        for (let v = -half; v <= half; v += 0.5)
          expect(inked({ x: c.point.x + v, y: c.point.y }, over.d, over.dash), `${where}: over ink at ${v}`).toBe(true)
        roles.add(`${geo.edges[c.over].kind}/${geo.edges[c.under].kind}`)
        if (geo.crossings.some(o => o.over === c.under && o.under === c.over)) roles.add('swapped-pair')
      }
    }
    // The fixtures exercise solid and dashed edges in both roles, and a pair of
    // edges crossing twice with swapped roles, so keying by pair cannot pass.
    expect([...roles].sort()).toEqual(
      expect.arrayContaining(['forward/forward', 'forward/loop_return', 'loop_return/forward', 'swapped-pair']),
    )
  }, 90_000)
})
