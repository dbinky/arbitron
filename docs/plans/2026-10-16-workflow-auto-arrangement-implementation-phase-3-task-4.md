# Workflow Auto-Arrangement — Phase 3 Task 4: Deterministic render/measure surface (`render.html`)

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.5) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Ship a second dashboard page, `render.html`, that draws a candidate geometry with the shared `ArrangedGraph` or measures text with the renderer's real CSS. It reports what the browser actually drew as a `RenderReport` or `Measurements` JSON on `window` globals, so the Go browser renderer (P3T5) can review it.

**Architecture:** `render.html` is a second Vite entry next to `index.html`, so the embedded dist ships the same components, CSS modules and `@fontsource/inter` files the workflow and instance views use. `src/render/main.tsx` fetches the one-shot job `/__render/jobs/<token>.json` that the Go loopback server registered. It then runs one of two modes. `RenderSurface` mounts `<ArrangedGraph fixedViewport>` in a container of canvas×zoom page px, collects a DOM report through the P3T2 DOM contract, and probes every runtime state. `MeasureSurface` lays each text item out with the exact renderer class. `report.ts` and `contrast.ts` hold the collectors and the pure maths. `job.ts` holds the page protocol and its validation.

**Tech Stack:** React 19, TypeScript 6, Vite 8 (Rolldown multi-page input), @xyflow/react 12 (`ReactFlowProvider`), Vitest 4 + jsdom + @testing-library/react, DOM APIs `Range.getClientRects`, `document.elementFromPoint`, `SVGPathElement.getPointAtLength`/`isPointInStroke`, `FontFaceSet`.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §4 "Shared renderer and production rendered-review seam". This task resolves the dashboard half of **PLANNED DEPENDENCY RENDER-REVIEW** (Phases 1–3 Data Provenance). The Go half is P3T5/P3T6, and P3T7 wires it in.

---

## Interfaces

**Consumes (exact names; produced by other tasks):**

```ts
// dashboard/src/graph/arranged/types.ts (P3T1)
export interface Point { x: number; y: number }
export interface Rect { x: number; y: number; w: number; h: number }
export interface NodeBox { id: string; box: Rect; text: string; typeCaption: string; textLines: number; rank: number }
export interface EdgeRoute { ordinal: number; source: string; target: string; output?: string; label?: string; kind: 'forward' | 'loop_return' | 'self_loop'; side?: Side; lane?: number; points: Point[]; sourceAttach: Attachment; targetAttach: Attachment; text?: LabelBox }
export interface Crossing { point: Point; over: number; under: number }
export interface RendererContract { version: string; fontFamily: string; nodeLabelFontPx: number; nodeLabelWeight: number; nodeTypeFontPx: number; nodeTypeWeight: number; edgeLabelFontPx: number; edgeLabelWeight: number; lineHeight: number; crossingGapPx: number; practicalZoom: number; minReadableTextPx: number; minContrastRatio: number; /* …all contract.json keys */ }
export interface Geometry { format: string; contract: RendererContract; options: Record<string, number>; nodes: NodeBox[]; edges: EdgeRoute[]; crossings: Crossing[]; canvas: Rect }
export interface LayoutView { savedRevision: string; jobGeneration: number; resultGeneration: number; displayStatus: DisplayStatus; attemptsUsed: number; attemptLimit: number; findings: LayoutFinding[] | null; contract?: RendererContract; geometry?: Geometry; provenance: Provenance; unreviewed: boolean; retryEligible: boolean; available: boolean /* … */ }
// dashboard/src/graph/arranged/validate.ts (P3T1)
export const RENDERER_CONTRACT: RendererContract            // contract.json (P1T2)
export function resolveDisplayGeometry(def: WorkflowDefinition, view: LayoutView | null): { geometry: Geometry | null; issues: string[] }
// dashboard/src/graph/arranged/ArrangedGraph.tsx (P3T3) — must be inside <ReactFlowProvider>
export default function ArrangedGraph(props: ArrangedGraphProps): JSX.Element   // props: workflowDef, view, executionStates?, selectedNodeId, selectedNodeIds?, onNodeClick, anchorNodeId, fixedViewport?
// dashboard/src/graph/arranged/ArrangedNode.tsx / RoutedEdge.tsx (P3T2, contracts §5.6)
export const nodeTextClassNames: { label: string; typeCaption: string }
export const edgeLabelClassName: string
// dashboard/src/graph/arranged/fonts.ts (P3T2) — side-effect import of @fontsource/inter 400/500/600/700
// dashboard/src/graph/arranged/testFixtures.ts (P3T1) — fixtureWorkflow(), fixtureGeometry() (valid against each other)
// dashboard/src/graph/types.ts — WorkflowDefinition, NodeExecutionState
```

DOM contract consumed (P3T2/P3T3, contracts §3 + addenda 5–7):
- `[data-arranged-node="<id>"]` is the node root. It has inline `width/height` and `box-sizing: border-box`, and contains `[data-node-text="label"]` and `[data-node-text="type"]`.
- `path[data-edge-path="<ordinal>"]` is the edge path. Stroke, width and dash are inline style, so they are read with `getComputedStyle`. `marker-end` is `url(#<graphInstanceId>-arranged-arrow-<ordinal>)` and points to a `<marker>` in the same `<g>`. Gaps are `M` subpaths in the same path. Path coordinates are flow (= canvas) coordinates.
- `[data-edge-label="<ordinal>"]` is the edge label. `[data-arranged-canvas="true"]` is the graph root.

**Produces (page protocol; consumed by P3T5 `adapter/renderreview/browser.go`):**

- URL: `/render.html?job=<32 lowercase hex>`. The page fetches `/__render/jobs/<token>.json` with `cache: 'no-store'`.
- Job JSON (contracts §3): `{"mode":"render","revision":"rev-1","attempt":1,"geometry":Geometry,"workflowNodeTypes":{id:type},"states":string[]}` or `{"mode":"measure","request":{"contract":RendererContract,"items":TextItem[]}}`. Nil optional empty edge/crossing slices are normalized; missing geometry, revision, attempt, node types or runtime-state coverage fail explicitly.
- Globals: `window.__ARBITRON_RENDER_STATE__` is `'loading' | 'ready' | 'error'`, plus `__ARBITRON_RENDER_ERROR__: string`, `__ARBITRON_RENDER_REPORT__: RenderReport` and `__ARBITRON_MEASURE_RESULT__: Measurements`. The shapes are byte-compatible with Go `layout.RenderReport` and `layout.Measurements` (contracts §1).
- Render mode coordinates are as follows. The container is `canvas.w*zoom × canvas.h*zoom` CSS px at page origin, where `zoom = geometry.contract.practicalZoom`. The viewport is `{x: -canvas.x*zoom, y: -canvas.y*zoom, zoom}`, which is `(0,0)` for an origin-0 canvas. So a page px equals `(canvas − canvas.origin) × zoom`. Every report `Rect`/`Point` is in canvas coordinates.
- Report semantics:
  - `surfaceVersion` and `contract` come from contract.json via `RENDERER_CONTRACT`.
  - `fontsReady` is `document.fonts.status === 'loaded'` after ensureBundledTextFonts loads the actual used bundled face subsets.
  - `fontFamilyResolved` is true only when ensureBundledTextFonts verifies exact bundled family/range/weight coverage for every actual text item. `document.fonts.check` is not used because it also returns true for a system fallback.
  - `RenderedText.clipped` is set when there are no glyph lines, when the element has scroll overflow, or when the glyph line rects fall outside the container rect by more than ±1px. The container is the node root, or the label itself for edge labels.
  - `occluded` is set when `elementFromPoint` at 25/50/75% of any glyph line is not the element or inside it.
  - `contrast` is the WCAG ratio of the computed colour against the composited ancestor background.
  - Edge `start`/`end` come from `getPointAtLength(0 | total)`. `hasArrow` means the `marker-end` id resolves to a non-empty `<marker>`. `dashed` means the computed `stroke-dasharray` is not `none`/`0`.
  - For crossings, `underGapVisible = !under.isPointInStroke(p)` and `overContinuous = over.isPointInStroke(p)`.
  - `StateProbe.nodeBoxesStable` is false if, in that state, any node box moves more than 1px, any edge endpoint moves, or an arrowhead is lost. `labelsReadable` is false if the state adds readability issues that the baseline did not have. `issues` lists them.
- Measure semantics: `width`/`height` are the actual fractional content box (border box minus computed padding+border); domain ceilPx owns box rounding/slack. `max-width` (= `item.maxWidth`) bounds the content box, and the item has `width: max-content`. `lines = max(1, round(contentHeight / lineHeightPx))`, and an empty text gives `0/0/0`. `contract` is `RENDERER_CONTRACT`. `fontsReady = fontsReady && fontFamilyResolved`.

```ts
// dashboard/src/render/job.ts
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
export interface StateProbe { state: string; nodeBoxesStable: boolean; labelsReadable: boolean; issues?: string[] }
export interface RenderReport { surfaceVersion: string; contract: RendererContract; fontsReady: boolean; fontFamilyResolved: boolean; zoom: number; canvas: Rect; nodes: RenderedNode[]; edges: RenderedEdge[]; crossings: RenderedCrossing[]; states: StateProbe[] }
export interface RenderJob { mode: 'render'; revision: string; attempt: number; geometry: Geometry; workflowNodeTypes: Record<string, string>; states: string[] }
export interface MeasureJob { mode: 'measure'; request: MeasureRequest }
export type SurfaceJob = RenderJob | MeasureJob
export const RUNTIME_STATES: readonly ['pending','running','completed','failed','waiting','skipped','selected']   // mirrors layout.RuntimeStates
export class JobError extends Error {}
export function jobTokenFrom(search: string): string
export function jobUrl(token: string): string
export function parseJob(raw: unknown): SurfaceJob
export function fetchJob(search: string, fetchImpl?: typeof fetch): Promise<SurfaceJob>
// dashboard/src/render/contrast.ts
export interface RGBA { r: number; g: number; b: number; a: number }
export function parseColor(css: string): RGBA | null
export function composite(top: RGBA, bottom: RGBA): RGBA
export function relativeLuminance(c: RGBA): number
export function contrastRatio(fg: RGBA, bg: RGBA): number
export function effectiveBackground(el: Element, win: Window): RGBA
export function textContrast(el: Element, win: Window): number
// dashboard/src/render/report.ts
export function toCanvasRect(r: ClientRectLike, root: { left: number; top: number }, zoom: number, origin: Point): Rect
export function centerPoint(r: Rect): Point
export function unionRects(rects: Rect[]): Rect | null
export function rectInside(inner: Rect, outer: Rect, tol?: number): boolean
export function markerIdFrom(ref: string | null | undefined): string | null
export function fontFamilyResolved(faces: Iterable<FaceLike>, contract: RendererContract): boolean
export function contractFonts(contract: RendererContract): string[]
export function ensureFonts(doc: Document, contract: RendererContract, texts: readonly string[]): Promise<{ fontsReady: boolean; fontFamilyResolved: boolean }>
export function collectText(el: Element | null, container: Element | null, ctx: CollectContext): RenderedText
export function collectNodes(geo: Geometry, ctx: CollectContext): RenderedNode[]
export function collectEdges(geo: Geometry, ctx: CollectContext): RenderedEdge[]
export function collectCrossings(geo: Geometry, ctx: CollectContext): RenderedCrossing[]
export function readabilityIssues(nodes: RenderedNode[], edges: RenderedEdge[], contract: RendererContract, zoom: number): string[]
export function stabilityIssues(base, now, tol?: number): string[]
export function nextFrame(win: Window): Promise<void>
export function waitFor(predicate: () => boolean, win: Window, timeoutMs: number): Promise<boolean>
export function settle(doc: Document, win: Window, maxMs?: number): Promise<void>
// dashboard/src/render/RenderSurface.tsx
export const SURFACE_CONTRACT: RendererContract   // = RENDERER_CONTRACT
export function buildSurfaceModel(job: RenderJob): { workflowDef: WorkflowDefinition; view: LayoutView }
export function probeFor(state: string, nodeIds: string[]): SurfaceProbe
export function RenderSurface(props: { workflowDef; view; probe: SurfaceProbe }): JSX.Element
export function runRenderJob(job: RenderJob, root: Root, doc: Document, win: Window): Promise<RenderReport>
// dashboard/src/render/MeasureSurface.tsx
export function classForStyle(style: TextStyle): string
export function metricsFrom(rect, extras: { padX: number; padY: number }, lineHeightPx: number, text: string): TextMetrics
export function MeasureSurface(props: { items: TextItem[]; fontFamily: string }): JSX.Element
export function runMeasureJob(job: MeasureJob, root: Root, doc: Document, win: Window): Promise<Measurements>
```

None of these TS exports is consumed outside `src/render/`. The only cross-task contract is the page protocol above.

---

### Task 4.1: WCAG contrast (`contrast.ts`)

**Files:**
- Create: `dashboard/src/render/contrast.ts`
- Test:   `dashboard/src/render/contrast.test.ts`

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/contrast.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { composite, contrastRatio, effectiveBackground, parseColor, relativeLuminance, textContrast } from './contrast'

const hex = (s: string) => parseColor(s)!

describe('parseColor', () => {
  it('parses hex forms', () => {
    expect(parseColor('#fff')).toEqual({ r: 255, g: 255, b: 255, a: 1 })
    expect(parseColor('#1e1e38')).toEqual({ r: 0x1e, g: 0x1e, b: 0x38, a: 1 })
    expect(parseColor('#00000080')!.a).toBeCloseTo(128 / 255, 5)
  })
  it('parses computed-style rgb()/rgba() in comma and space syntax', () => {
    expect(parseColor('rgb(224, 224, 224)')).toEqual({ r: 224, g: 224, b: 224, a: 1 })
    expect(parseColor('rgba(30, 30, 56, 0.5)')).toEqual({ r: 30, g: 30, b: 56, a: 0.5 })
    expect(parseColor('rgb(30 30 56 / 50%)')).toEqual({ r: 30, g: 30, b: 56, a: 0.5 })
  })
  it('treats transparent as zero alpha and rejects garbage', () => {
    expect(parseColor('transparent')!.a).toBe(0)
    expect(parseColor('rgba(0, 0, 0, 0)')!.a).toBe(0)
    expect(parseColor('not-a-colour')).toBeNull()
    expect(parseColor('rgb(1, 2)')).toBeNull()
  })
})

describe('contrastRatio', () => {
  it('matches WCAG reference values', () => {
    expect(contrastRatio(hex('#000'), hex('#fff'))).toBeCloseTo(21, 5)
    expect(contrastRatio(hex('#777'), hex('#777'))).toBeCloseTo(1, 5)
    expect(relativeLuminance(hex('#fff'))).toBeCloseTo(1, 5)
  })
  it('passes the arranged renderer label colours on the node surface', () => {
    // label #e0e0e0 and type caption #b4b4cc on --bg-surface #1e1e38 (contracts addendum 7)
    expect(contrastRatio(hex('#e0e0e0'), hex('#1e1e38'))).toBeGreaterThanOrEqual(4.5)
    expect(contrastRatio(hex('#b4b4cc'), hex('#1e1e38'))).toBeGreaterThanOrEqual(4.5)
    // loop-return edge label #9aa6ff on the label background #1a1a2e
    expect(contrastRatio(hex('#9aa6ff'), hex('#1a1a2e'))).toBeGreaterThanOrEqual(4.5)
  })
  it('fails an accent colour used as text', () => {
    expect(contrastRatio(hex('#9b59b6'), hex('#1e1e38'))).toBeLessThan(4.5)
  })
  it('composites a translucent foreground before measuring', () => {
    const half = { r: 224, g: 224, b: 224, a: 0.3 }
    expect(contrastRatio(half, hex('#1e1e38'))).toBeLessThan(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')))
  })
  it('is symmetric', () => {
    expect(contrastRatio(hex('#1e1e38'), hex('#e0e0e0'))).toBeCloseTo(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')), 10)
  })
})

describe('composite', () => {
  it('blends source-over', () => {
    const c = composite({ r: 255, g: 0, b: 0, a: 0.5 }, { r: 0, g: 0, b: 255, a: 1 })
    expect(c).toEqual({ r: 127.5, g: 0, b: 127.5, a: 1 })
  })
})

describe('effectiveBackground / textContrast (DOM)', () => {
  it('uses the first opaque ancestor background and skips transparent layers', () => {
    document.body.innerHTML = '<div id="node" style="background-color: #1e1e38"><span id="wrap"><span id="t" style="color: #e0e0e0">Start</span></span></div>'
    const t = document.getElementById('t')!
    expect(effectiveBackground(t, window)).toEqual({ r: 30, g: 30, b: 56, a: 1 })
    expect(textContrast(t, window)).toBeCloseTo(contrastRatio(hex('#e0e0e0'), hex('#1e1e38')), 5)
  })
  it('composites a translucent overlay over the opaque background below it', () => {
    document.body.innerHTML = '<div style="background-color: rgb(0, 0, 0)"><div id="o" style="background-color: rgba(255, 255, 255, 0.5)"><span id="t" style="color: rgb(0, 0, 0)">x</span></div></div>'
    const bg = effectiveBackground(document.getElementById('t')!, window)
    expect(bg.r).toBeCloseTo(127.5, 5)
    expect(bg.a).toBe(1)
  })
  it('falls back to the white canvas when nothing paints a background', () => {
    document.body.innerHTML = '<span id="t" style="color: #e0e0e0">x</span>'
    expect(effectiveBackground(document.getElementById('t')!, window)).toEqual({ r: 255, g: 255, b: 255, a: 1 })
    expect(textContrast(document.getElementById('t')!, window)).toBeLessThan(4.5)
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import, then 9 behavioral failures against compiling declarations (12 tests).

```bash
cd dashboard && npx vitest run src/render/contrast.test.ts
```
Expected: `FAIL src/render/contrast.test.ts` with `Error: Failed to resolve import "./contrast" from "src/render/contrast.test.ts". Does the file exist?`

- [x] **Step 3: Minimal implementation.** Create `dashboard/src/render/contrast.ts`:

```ts
// WCAG 2.x contrast for the render surface. Pure colour maths plus one DOM
// helper that resolves the colour actually behind a text element.

export interface RGBA {
  r: number // 0..255
  g: number
  b: number
  a: number // 0..1
}

const OPAQUE_WHITE: RGBA = { r: 255, g: 255, b: 255, a: 1 }

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v))
}

function channel(token: string): number {
  const t = token.trim()
  return t.endsWith('%') ? clamp((parseFloat(t) / 100) * 255, 0, 255) : clamp(parseFloat(t), 0, 255)
}

function alpha(token: string | undefined): number {
  if (token === undefined) return 1
  const t = token.trim()
  return t.endsWith('%') ? clamp(parseFloat(t) / 100, 0, 1) : clamp(parseFloat(t), 0, 1)
}

/** Parses #rgb, #rgba, #rrggbb, #rrggbbaa, rgb()/rgba() (comma or space syntax) and 'transparent'. */
export function parseColor(css: string): RGBA | null {
  const s = css.trim().toLowerCase()
  if (s === 'transparent') return { r: 0, g: 0, b: 0, a: 0 }
  const hex = /^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/.exec(s)
  if (hex) {
    let h = hex[1]
    if (h.length <= 4) h = h.split('').map((c) => c + c).join('')
    const n = (i: number) => parseInt(h.slice(i, i + 2), 16)
    return { r: n(0), g: n(2), b: n(4), a: h.length === 8 ? n(6) / 255 : 1 }
  }
  const fn = /^rgba?\((.*)\)$/.exec(s)
  if (!fn) return null
  const [rgbPart, slashAlpha] = fn[1].split('/')
  const parts = rgbPart.includes(',') ? rgbPart.split(',') : rgbPart.trim().split(/\s+/)
  if (parts.length < 3 || parts.length > 4) return null
  const color = { r: channel(parts[0]), g: channel(parts[1]), b: channel(parts[2]), a: alpha(slashAlpha ?? parts[3]) }
  return [color.r, color.g, color.b, color.a].some(Number.isNaN) ? null : color
}

/** Source-over compositing of top onto bottom. */
export function composite(top: RGBA, bottom: RGBA): RGBA {
  const a = top.a + bottom.a * (1 - top.a)
  if (a === 0) return { r: 0, g: 0, b: 0, a: 0 }
  const mix = (t: number, b: number) => (t * top.a + b * bottom.a * (1 - top.a)) / a
  return { r: mix(top.r, bottom.r), g: mix(top.g, bottom.g), b: mix(top.b, bottom.b), a }
}

/** WCAG relative luminance of an opaque colour. */
export function relativeLuminance(c: RGBA): number {
  const lin = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b)
}

/** WCAG contrast ratio (1..21); a translucent foreground is composited over bg first. */
export function contrastRatio(fg: RGBA, bg: RGBA): number {
  const opaqueBg = bg.a < 1 ? composite(bg, OPAQUE_WHITE) : bg
  const opaqueFg = fg.a < 1 ? composite(fg, opaqueBg) : fg
  const l1 = relativeLuminance(opaqueFg)
  const l2 = relativeLuminance(opaqueBg)
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
}

/**
 * The colour painted behind el: walks el and its ancestors, compositing each
 * translucent background-color until an opaque one is reached; the browser's
 * default canvas (white) terminates the walk.
 */
export function effectiveBackground(el: Element, win: Window): RGBA {
  const layers: RGBA[] = []
  for (let node: Element | null = el; node; node = node.parentElement) {
    const bg = parseColor(win.getComputedStyle(node).backgroundColor)
    if (!bg || bg.a === 0) continue
    layers.push(bg)
    if (bg.a >= 1) break
  }
  let out = OPAQUE_WHITE
  for (let i = layers.length - 1; i >= 0; i--) out = composite(layers[i], out)
  return out
}

/** Contrast of el's computed text colour against its effective background. */
export function textContrast(el: Element, win: Window): number {
  const fg = parseColor(win.getComputedStyle(el).color) ?? { r: 0, g: 0, b: 0, a: 1 }
  return contrastRatio(fg, effectiveBackground(el, win))
}
```

- [x] **Step 4: Run it and confirm it passes.**

Observed: 12 tests passed; `tsc --noEmit` exited 0.

```bash
cd dashboard && npx vitest run src/render/contrast.test.ts && npx tsc --noEmit
```
Expected: `Test Files  1 passed (1)`, `Tests  12 passed (12)`. `tsc` prints nothing and exits 0.

- [x] **Step 5: Commit.**

```bash
git add dashboard/src/render/contrast.ts dashboard/src/render/contrast.test.ts && git commit -m "feat(render): WCAG contrast helpers for the render surface" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.2: Page protocol and job validation (`job.ts`)

**Files:**
- Create: `dashboard/src/render/job.ts`
- Test:   `dashboard/src/render/job.test.ts`

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/job.test.ts`. The mocked `fetch` honours the URL: only the page's own token resolves, so the test fails if `fetchJob` ignores the token.

```ts
import { describe, expect, it, vi } from 'vitest'
import contract from '../graph/arranged/contract.json'
import { fixtureGeometry } from '../graph/arranged/testFixtures'
import { fetchJob, jobTokenFrom, jobUrl, parseJob, RUNTIME_STATES } from './job'

const TOKEN = '0123456789abcdef0123456789abcdef'

const geometry = {
  format: 'arbitron.layout/v1',
  contract,
  options: fixtureGeometry().options,
  nodes: [{ id: 'a', box: { x: 40, y: 40, w: 200, h: 60 }, text: 'Start', typeCaption: 'agent call', textLines: 1, rank: 0 }],
  edges: null,
  crossings: null,
  canvas: { x: 0, y: 0, w: 280, h: 140 },
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('jobTokenFrom', () => {
  it('returns a well-formed token', () => {
    expect(jobTokenFrom(`?job=${TOKEN}`)).toBe(TOKEN)
  })
  it('rejects a missing or malformed token', () => {
    expect(() => jobTokenFrom('')).toThrow(/missing/)
    expect(() => jobTokenFrom('?job=../../etc/passwd')).toThrow(/malformed/)
    expect(() => jobTokenFrom('?job=ABCDEF')).toThrow(/malformed/)
  })
})

describe('parseJob', () => {
  it('accepts a render job and normalises Go null slices', () => {
    const job = parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: { a: 'agent_call' }, states: [...RUNTIME_STATES] })
    expect(job.mode).toBe('render')
    if (job.mode !== 'render') return
    expect(job.geometry.edges).toEqual([])
    expect(job.geometry.crossings).toEqual([])
    expect(job.states).toEqual([...RUNTIME_STATES])
    expect(job.workflowNodeTypes).toEqual({ a: 'agent_call' })
  })
  it('rejects absent node types and absent runtime-state coverage', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: null, states: null })).toThrow()
  })
  it('accepts a measure job', () => {
    const job = parseJob({ mode: 'measure', request: { contract, items: [{ key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 }] } })
    expect(job).toMatchObject({ mode: 'measure', request: { items: [{ key: 'node:a:label' }] } })
  })
  it('rejects an unknown mode', () => {
    expect(() => parseJob({ mode: 'paint' })).toThrow(/unknown mode/)
    expect(() => parseJob(null)).toThrow(/object/)
  })
  it('rejects a render job without geometry or with an empty canvas', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: {}, states: [...RUNTIME_STATES] })).toThrow(/geometry is required/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, canvas: { x: 0, y: 0, w: 0, h: 10 } }, states: [] })).toThrow(/canvas/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, nodes: [] }, states: [] })).toThrow(/no nodes/)
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry: { ...geometry, contract: { ...contract, practicalZoom: 0 } }, states: [] })).toThrow(/practicalZoom/)
  })
  it('rejects unknown runtime states', () => {
    expect(() => parseJob({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: { a: 'agent_call' }, geometry, states: ['exploding'] })).toThrow(/unknown runtime state/)
  })
  it('rejects malformed measure items', () => {
    expect(() => parseJob({ mode: 'measure', request: { items: [] } })).toThrow(/contract/)
    expect(() => parseJob({ mode: 'measure', request: { contract, items: [{ key: 'k', text: 'x', style: 'huge', maxWidth: 10 }] } })).toThrow(/invalid text item/)
    expect(() => parseJob({ mode: 'measure', request: { contract, items: [{ key: 'k', text: 'x', style: 'edge_label', maxWidth: 0 }] } })).toThrow(/invalid text item/)
  })
})

describe('fetchJob', () => {
  it('fetches the document named by the URL token', async () => {
    const other = 'ffffffffffffffffffffffffffffffff'
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      // Honour the token: only this page's job exists.
      if (String(input) === jobUrl(TOKEN)) return jsonResponse({ mode: 'measure', request: { contract, items: [] } })
      return jsonResponse({ error: 'not found' }, 404)
    }) as unknown as typeof fetch
    const job = await fetchJob(`?job=${TOKEN}`, fetchImpl)
    expect(job.mode).toBe('measure')
    expect(fetchImpl).toHaveBeenCalledWith('/__render/jobs/' + TOKEN + '.json', { cache: 'no-store' })
    await expect(fetchJob(`?job=${other}`, fetchImpl)).rejects.toThrow(/HTTP 404/)
  })
  it('fails without calling fetch when the token is absent', async () => {
    const fetchImpl = vi.fn() as unknown as typeof fetch
    await expect(fetchJob('', fetchImpl)).rejects.toThrow(/missing/)
    expect(fetchImpl).not.toHaveBeenCalled()
  })
  it('surfaces validation errors from the fetched document', async () => {
    const fetchImpl = vi.fn(async () => jsonResponse({ mode: 'render', revision: 'rev-1', attempt: 1, workflowNodeTypes: {}, states: [...RUNTIME_STATES] })) as unknown as typeof fetch
    await expect(fetchJob(`?job=${TOKEN}`, fetchImpl)).rejects.toThrow(/geometry is required/)
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import, then 29 behavioral failures (35 tests, including the corrective rejection matrix).

```bash
cd dashboard && npx vitest run src/render/job.test.ts
```
Expected: `FAIL src/render/job.test.ts`, `Failed to resolve import "./job"`.

- [x] **Step 3: Minimal implementation.** Create `dashboard/src/render/job.ts`:

```ts
// Page protocol between the Go browser renderer (adapter/renderreview) and the
// render surface: the job document, the result shapes (mirroring domain/layout
// JSON exactly) and the window globals the renderer polls.
import type { Geometry, Point, Rect, RendererContract } from '../graph/arranged/types'

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
}

export interface RenderJob { mode: 'render'; revision: string; attempt: number; geometry: Geometry; workflowNodeTypes: Record<string, string>; states: string[] }
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

// Implement parseGeometry and parseJob with the rejection matrix in Task 4.2:
// validate all shapes before casts, require nonempty safe revision + integral positive
// attempt, full RendererContract equality, every manifest-derived node type, all runtime
// states exactly once, valid geometry/options and no unsupported SVG/JSON shape.
// The page's synthetic definition uses job.revision and exact workflowNodeTypes; run the
// same validateGeometry used by ArrangedGraph before accepting render mode.
// Measure mode requires full bundled contract equality, unique nonempty item keys,
// supported TextStyle and finite positive maxWidth. Reject missing/null items for a
// nonempty measurement request; an explicit [] is valid only for an explicit empty job.
// Add missing revision/type/attempt/state/contract, nil/empty and duplicate-key RED cases
// before implementing these functions. Never make fontsReady true for absent measurements.

/** Fetches and validates the job named by the page's ?job= token. */
export async function fetchJob(search: string, fetchImpl: typeof fetch = fetch): Promise<SurfaceJob> {
  const res = await fetchImpl(jobUrl(jobTokenFrom(search)), { cache: 'no-store' })
  if (!res.ok) throw new JobError(`render job: fetch failed with HTTP ${res.status}`)
  return parseJob(await res.json())
}
```

- [x] **Step 4: Run it and confirm it passes.**

Observed: 35 tests passed; TypeScript passed after using the project's ES2020 own-property idiom.

```bash
cd dashboard && npx vitest run src/render/job.test.ts && npx tsc --noEmit
```
Expected: `Tests  12 passed (12)`; tsc clean.

- [x] **Step 5: Commit.**

```bash
git add dashboard/src/render/job.ts dashboard/src/render/job.test.ts && git commit -m "feat(render): render-surface page protocol and job validation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.3: DOM report collectors and timing helpers (`report.ts`)

**Files:**
- Create: `dashboard/src/render/report.ts`
- Test:   `dashboard/src/render/report.test.ts`

jsdom has no layout, so this file tests the pure helpers and the DOM-contract lookup (missing nodes are omitted; a missing text element is reported as unreadable). Collector *values* (glyph rects, hit-testing, path geometry, stroke hit-testing, fonts) are verified in a real browser by P3T5's `TestBrowserRenderer_RealSurface*` and P3T6's `TestReadabilityReviewer_RealBrowser*`.

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/report.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import contract from '../graph/arranged/contract.json'
import type { RendererContract } from '../graph/arranged/types'
import type { RenderedEdge, RenderedNode, RenderedText } from './job'
import {
  centerPoint, collectNodes, contractFonts, fontFamilyResolved, markerIdFrom, readabilityIssues,
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
  const face = (family: string, weight: string, status = 'loaded') => ({ family, weight, status })
  const complete = c.fontFamily.split(',').flatMap(f => [500, 600, 700].map(w => face(f.trim(), String(w))))
  it('requires every bundled family and contract weight', () => {
    expect(fontFamilyResolved(complete, c)).toBe(true)
    expect(fontFamilyResolved(complete.filter(f => f.family === 'Inter'), c)).toBe(false)
    expect(fontFamilyResolved(complete.map(f => f.family === 'Noto Emoji' ? { ...f, status: 'unloaded' } : f), c)).toBe(false)
    expect(fontFamilyResolved([], c)).toBe(false)
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

describe('waitFor', () => {
  it('resolves true when the predicate becomes true and false on timeout', async () => {
    let n = 0
    await expect(waitFor(() => ++n > 2, window, 1000)).resolves.toBe(true)
    await expect(waitFor(() => false, window, 30)).resolves.toBe(false)
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import; compiling helper declarations produced 11 behavioral failures. Added collector tests then observed 2 edge-collector failures.

```bash
cd dashboard && npx vitest run src/render/report.test.ts
```
Expected: `FAIL src/render/report.test.ts`, `Failed to resolve import "./report"`.

- [x] **Step 3: Minimal implementation.** Create `dashboard/src/render/report.ts`:

Implement report.ts against the interfaces above and the following sink rules. The
pure helpers in the tests define coordinate, rectangle, font, timing and contrast math;
implement those first, then each collector with its own observed behavioral RED test.

1. `collectText` reads actual textContent, each Range.getClientRects line in canvas
   coordinates, computed font size, cumulative ancestor opacity/display/visibility and
   composited contrast. A missing element returns content='', glyphs=[], visible=false,
   clipped/occluded=true. A Range rectangle is a glyph region, not proof of painted ink;
   P3T6 independently checks the PNG. Check clipping ancestors as well as the node box.
2. `collectEdges` parses the actual SVG path d (M/L only) and transforms every start/end
   into canvas coordinates using the actual path matrix relative to the flow viewport.
   Preserve each M gap; unsupported commands, empty segments or non-finite transforms
   fail explicitly. Never fill Segments from candidate Points. Read computed stroke color,
   stroke width, dash pattern and cumulative visibility. Transparent strokes are invisible.
3. Resolve marker-end from the actual path, scoped to that graph's unique namespace.
   Inspect markerUnits, viewBox, refX/refY, orient, its visible child outline and transforms;
   produce ArrowPoints from the actual painted marker at each destination. HasArrow means
   a visible nondegenerate arrow pointing with the actual final segment. Empty defs,
   hidden/transparent marker, unsupported shape or dangling reference is blocking.
4. `collectCrossings` checks the full gap neighborhood on the under path and continuity
   on both sides of the over path using actual transformed segments. A natural dash hole
   exactly at the crossing is not proof of the required gap width or visible over stroke.
5. `stabilityIssues` compares node boxes AND every text box/glyph region/content/visibility,
   complete edge subpaths/styles/marker outlines/labels and crossing separation. Runtime
   state probes include selected+running combinations and extremal animation phases. A
   middle-path change with unchanged endpoints must fail. Do not compare baseline issues
   away when a state makes the same criterion materially worse.
6. Collect after font readiness and settled frames. Poll drawing coverage by exact ID and
   ordinal sets, not only counts. Missing/duplicate/unknown DOM elements remain failures;
   never synthesize elements from geo. Return actual font readiness even on failures.

Add browser assertions to P3T5 surface tests for every field and each DOM mutation above;
jsdom tests cover shapes/lookups/math only. Nil DOM, empty routes and an all-white capture
must block. State probes must not become passing by freezing defective animation at one
harmless frame. The final baseline report is recollected after restoring baseline and
settling, immediately before capture, so it describes the PNG's actual state.

- [x] **Step 4: Run it and confirm it passes.**

Observed: 17 tests passed; TypeScript exited 0. These API-shaped jsdom checks are not real-browser or reviewer acceptance; the additional integration gate below tracks that distinction.

```bash
cd dashboard && npx vitest run src/render/report.test.ts && npx tsc --noEmit
```
Expected: `Tests  15 passed (15)`; tsc clean.

- [x] **Step 5: Commit.**

```bash
git add dashboard/src/render/report.ts dashboard/src/render/report.test.ts && git commit -m "feat(render): collect rendered node/edge/crossing evidence from the DOM contract" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.4: Render mode (`RenderSurface.tsx`)

**Files:**
- Create: `dashboard/src/render/RenderSurface.tsx`
- Test:   `dashboard/src/render/RenderSurface.test.tsx`

`ArrangedGraph` calls `useReactFlow()` at top level, so the surface wraps it in `<ReactFlowProvider>`. The surface refuses (throws, which becomes a render error in Go) to review a payload that `resolveDisplayGeometry` rejects. That payload would otherwise be drawn with the fallback layout and reviewed as if it were the candidate.

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/RenderSurface.test.tsx`. `ArrangedGraph` is mocked so the test can see the props the surface passes. The real component is exercised in a real browser by P3T5.

```tsx
import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import contract from '../graph/arranged/contract.json'
import type { Geometry, RendererContract } from '../graph/arranged/types'
import type { RenderJob } from './job'
import { RUNTIME_STATES } from './job'
import { fixtureGeometry, fixtureWorkflow } from '../graph/arranged/testFixtures'
import { resolveDisplayGeometry } from '../graph/arranged/validate'
import { createRoot } from 'react-dom/client'
import { buildSurfaceModel, probeFor, RenderSurface, runRenderJob, SURFACE_CONTRACT } from './RenderSurface'

vi.mock('../graph/arranged/ArrangedGraph', () => ({
  // Records the props the surface passes to the shared renderer.
  default: (props: { fixedViewport?: { x: number; y: number; zoom: number }; selectedNodeIds?: ReadonlySet<string>; executionStates?: Record<string, string> }) => (
    <div
      data-testid="arranged"
      data-viewport={JSON.stringify(props.fixedViewport)}
      data-selected={[...(props.selectedNodeIds ?? [])].join(',')}
      data-states={JSON.stringify(props.executionStates ?? {})}
    />
  ),
}))

const geometry: Geometry = {
  format: 'arbitron.layout/v1',
  contract: contract as RendererContract,
  options: fixtureGeometry().options,
  nodes: [
    { id: 'a', box: { x: 40, y: 40, w: 200, h: 60 }, text: 'Start', typeCaption: 'agent call', textLines: 1, rank: 0 },
    { id: 'b', box: { x: 40, y: 200, w: 200, h: 60 }, text: 'b', typeCaption: 'transform', textLines: 1, rank: 1 },
  ],
  edges: [{
    ordinal: 0, source: 'a', target: 'b', kind: 'forward', points: [{ x: 140, y: 100 }, { x: 140, y: 200 }],
    sourceAttach: { nodeId: 'a', side: 'bottom', point: { x: 140, y: 100 } }, targetAttach: { nodeId: 'b', side: 'top', point: { x: 140, y: 200 } },
  }],
  crossings: [],
  canvas: { x: 0, y: 0, w: 300, h: 300 },
}
const job: RenderJob = { mode: 'render', revision: 'rev-1', attempt: 1, geometry, workflowNodeTypes: { a: 'agent_call', b: 'transform' }, states: [...RUNTIME_STATES] }

describe('buildSurfaceModel', () => {
  it('builds the workflow from geometry text and the job node types', () => {
    const { workflowDef } = buildSurfaceModel(job)
    expect(workflowDef.nodes.map((n) => [n.id, n.type, n.label])).toEqual([['a', 'agent_call', 'Start'], ['b', 'transform', 'b']])
    expect(workflowDef.edges).toEqual([{ source: 'a', target: 'b' }])
  })
  it('passes the candidate geometry through an arranged view', () => {
    const { view } = buildSurfaceModel(job)
    expect(view.geometry).toBe(geometry)
    expect(view.provenance).toBe('arranged')
    expect(view.displayStatus).toBe('reviewing')
    expect(view.contract).toBe(geometry.contract)
  })
  it('produces a model the shared renderer accepts (no fallback)', () => {
    const geo = fixtureGeometry()
    const types = Object.fromEntries(fixtureWorkflow().nodes.map((n) => [n.id, n.type]))
    const { workflowDef, view } = buildSurfaceModel({ mode: 'render', revision: 'rev-1', attempt: 1, geometry: geo, workflowNodeTypes: types, states: [] })
    expect(resolveDisplayGeometry(workflowDef, view)).toEqual({ geometry: geo, issues: [] })
  })
  it('rejects a missing node type instead of inventing one', () => {
    expect(() => buildSurfaceModel({ ...job, workflowNodeTypes: {} })).toThrow(/node type/)
  })
})

describe('probeFor', () => {
  it('applies an execution state to every node', () => {
    expect(probeFor('failed', ['a', 'b'])).toEqual({ executionStates: { a: 'failed', b: 'failed' } })
  })
  it('selects every node for the selected probe', () => {
    expect([...(probeFor('selected', ['a', 'b']).selectedNodeIds ?? [])]).toEqual(['a', 'b'])
  })
})

describe('RenderSurface', () => {
  it('sizes the container to canvas × zoom and pins the viewport to the canvas origin', () => {
    const { workflowDef, view } = buildSurfaceModel(job)
    const { container, getByTestId } = render(<RenderSurface workflowDef={workflowDef} view={view} probe={probeFor('selected', ['a'])} />)
    const root = container.querySelector<HTMLElement>('[data-render-root]')!
    expect(root.style.width).toBe('270px')
    expect(root.style.height).toBe('270px')
    expect(JSON.parse(getByTestId('arranged').dataset.viewport!)).toEqual({ x: 0, y: 0, zoom: 0.9 })
    expect(getByTestId('arranged').dataset.selected).toBe('a')
  })
  it('implements the contract.json renderer contract', () => {
    expect(SURFACE_CONTRACT.version).toBe('arranged-renderer/1')
  })
})

describe('runRenderJob', () => {
  it('refuses to review a geometry the shared renderer would replace with a fallback', async () => {
    const stale = { ...geometry, contract: { ...geometry.contract, version: 'arranged-renderer/0' } }
    const root = createRoot(document.createElement('div'))
    await expect(runRenderJob({ ...job, geometry: stale }, root, document, window)).rejects.toThrow(/rejects this geometry/)
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import, then 8 behavioral failures against compiling declarations (9 tests).

```bash
cd dashboard && npx vitest run src/render/RenderSurface.test.tsx
```
Expected: `FAIL src/render/RenderSurface.test.tsx`, `Failed to resolve import "./RenderSurface"`.

- [x] **Step 3: Minimal implementation.** Create `dashboard/src/render/RenderSurface.tsx`:

```tsx
// Render mode of the deterministic render surface: draws a candidate geometry
// with the shared ArrangedGraph (the same components, CSS and fonts as the
// workflow and instance views), then reports what was actually drawn.
import { ReactFlowProvider } from '@xyflow/react'
import { flushSync } from 'react-dom'
import type { Root } from 'react-dom/client'
import ArrangedGraph from '../graph/arranged/ArrangedGraph'
import type { Geometry, LayoutView } from '../graph/arranged/types'
import { RENDERER_CONTRACT, resolveDisplayGeometry } from '../graph/arranged/validate'
import type { NodeExecutionState, WorkflowDefinition } from '../graph/types'
import type { RenderJob, RenderReport, StateProbe } from './job'
import {
  collectCrossings, collectEdges, collectNodes, ensureFonts, readabilityIssues, settle, stabilityIssues, waitFor,
  type CollectContext,
} from './report'

/** The renderer contract this build of the surface implements. */
export const SURFACE_CONTRACT = RENDERER_CONTRACT

/** How long the surface waits for every node and edge to appear. */
export const READY_TIMEOUT_MS = 20_000

export interface SurfaceProbe {
  executionStates?: Record<string, NodeExecutionState>
  selectedNodeIds?: ReadonlySet<string>
}

const noop = () => {}

function requireNodeType(job: RenderJob, id: string): string {
  const type = job.workflowNodeTypes[id]
  if (typeof type !== 'string' || type.length === 0) throw new Error(`render job: missing node type for ${id}`)
  return type
}

/** A minimal workflow definition + passed-through layout view for one candidate geometry. */
export function buildSurfaceModel(job: RenderJob): { workflowDef: WorkflowDefinition; view: LayoutView } {
  const geo = job.geometry
  const workflowDef: WorkflowDefinition = {
    name: 'render-surface',
    description: '',
    author: '',
    nodes: geo.nodes.map((n) => ({ id: n.id, type: requireNodeType(job, n.id), label: n.text, description: '', config: {} })),
    presentation: { revision: job.revision, savedAt: '1970-01-01T00:00:00Z', reason: 'render', scope: 'private', name: 'render-surface', retained: { source: 'none' } },
    edges: geo.edges.map((e) => ({ source: e.source, target: e.target, ...(e.output ? { output: e.output } : {}), ...(e.label ? { label: e.label } : {}) })),
  }
  const view: LayoutView = {
    savedRevision: job.revision,
    jobGeneration: 0,
    resultGeneration: 0,
    displayStatus: 'reviewing',
    attemptsUsed: 0,
    attemptLimit: 0,
    findings: [],
    contract: geo.contract,
    geometry: geo,
    provenance: 'arranged',
    unreviewed: true,
    retryEligible: false,
    available: true,
  }
  return { workflowDef, view }
}

/** The runtime styling exercised for one RuntimeStates entry. */
export function probeFor(state: string, nodeIds: string[]): SurfaceProbe {
  if (state === 'selected') return { selectedNodeIds: new Set(nodeIds) }
  return { executionStates: Object.fromEntries(nodeIds.map((id) => [id, state as NodeExecutionState])) }
}

export interface RenderSurfaceProps {
  workflowDef: WorkflowDefinition
  view: LayoutView
  probe: SurfaceProbe
}

/** Canvas-sized container (page px = canvas px × zoom) holding the shared renderer at a fixed viewport. */
export function RenderSurface({ workflowDef, view, probe }: RenderSurfaceProps) {
  const geo = view.geometry as Geometry
  const zoom = geo.contract.practicalZoom
  return (
    <div
      data-render-root="true"
      style={{ position: 'relative', width: geo.canvas.w * zoom, height: geo.canvas.h * zoom, overflow: 'hidden', background: 'var(--bg-primary)' }}
    >
      <ReactFlowProvider>
        <ArrangedGraph
          workflowDef={workflowDef}
          view={view}
          executionStates={probe.executionStates}
          selectedNodeId={null}
          selectedNodeIds={probe.selectedNodeIds}
          onNodeClick={noop}
          anchorNodeId={null}
          fixedViewport={{ x: -geo.canvas.x * zoom, y: -geo.canvas.y * zoom, zoom }}
        />
      </ReactFlowProvider>
    </div>
  )
}

function everythingDrawn(doc: Document, geo: Geometry): boolean {
  const ids = [...doc.querySelectorAll('[data-arranged-node]')].map(el => el.getAttribute('data-arranged-node')).sort()
  const ordinals = [...doc.querySelectorAll('path[data-edge-path]')].map(el => Number(el.getAttribute('data-edge-path'))).sort((a,b) => a-b)
  return JSON.stringify(ids) === JSON.stringify(geo.nodes.map(n => n.id).sort()) &&
    JSON.stringify(ordinals) === JSON.stringify(geo.edges.map(e => e.ordinal).sort((a,b) => a-b))
}

// Implement runRenderJob with this ordered protocol (keep exported signature):
// 1. Build model carrying job.revision; reject resolveDisplayGeometry issues before mount.
// 2. ensureFonts for all actual label/type/branch text; mount baseline and await exact
//    node ID/edge ordinal coverage plus settled layout. Missing fonts/elements fail.
// 3. Collect baseline actual nodes, edges AND crossings. Enumerate actual DOM sets so
//    duplicate/unknown elements cannot disappear by iterating only expected geometry.
// 4. For every required runtime state, plus running+selected, sample every animation's
//    extremal start/mid/end/dash phases using getAnimations(), currentTime and pause.
//    Collect actual boxes/text/full routes/marker shapes/crossings at each phase.
//    StateProbe booleans are conjunction across phase results. Merge running+selected
//    subprobes into the required running/selected records; do not invent an unknown state. Baseline issues remain
//    blocking and must not hide a materially worse same-criterion issue in a state.
// 5. Restore baseline, freeze animation deterministically, ensure fonts and settle again.
//    Recollect the complete report immediately before ready, so PNGs and report agree.
// 6. Return actual SurfaceVersion/Contract/font readiness/Zoom/Canvas and collections.
//    Never fill unknown values from expected geo and never treat [] states as full review.
// Types come from job.ts; all helpers are owned by this task. No synthetic report fallback.

```

- [x] **Step 4: Run it and confirm it passes.**

Observed: 9 tests and TypeScript pass. `ARBITRON_REQUIRE_BROWSER=1 node --test scripts/test-render-surface-browser.mjs` passes real Chrome shared-renderer/collector/runtime mutation sinks. The fixture uses actual browser frames/ResizeObserver, not virtual-time starvation; a genuinely undersized fixture branch label was widened, not suppressed in the collector.

```bash
cd dashboard && npx vitest run src/render/RenderSurface.test.tsx && npx tsc --noEmit
```
Expected: `Tests  8 passed (8)`; tsc clean.

- [x] **Step 5: Commit.**

```bash
git add dashboard/src/render/RenderSurface.tsx dashboard/src/render/RenderSurface.test.tsx && git commit -m "feat(render): render mode draws candidates with the shared ArrangedGraph and probes runtime states" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.5: Measure mode (`MeasureSurface.tsx`)

**Files:**
- Create: `dashboard/src/render/MeasureSurface.tsx`
- Test:   `dashboard/src/render/MeasureSurface.test.tsx`

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/MeasureSurface.test.tsx`:

```tsx
import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { createRoot } from 'react-dom/client'
import contract from '../graph/arranged/contract.json'
import type { RendererContract } from '../graph/arranged/types'
import { classForStyle, MeasureSurface, metricsFrom, runMeasureJob } from './MeasureSurface'

// Keep every real export (the modules are imported for their class names only)
// but pin the class names so the assertions are independent of CSS-module hashing.
vi.mock('../graph/arranged/ArrangedNode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../graph/arranged/ArrangedNode')>()),
  nodeTextClassNames: { label: 'cls-label', typeCaption: 'cls-type' },
}))
vi.mock('../graph/arranged/RoutedEdge', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../graph/arranged/RoutedEdge')>()),
  edgeLabelClassName: 'cls-edge',
}))

describe('classForStyle', () => {
  it('uses the renderer class of each text style', () => {
    expect(classForStyle('node_label')).toBe('cls-label')
    expect(classForStyle('node_type')).toBe('cls-type')
    expect(classForStyle('edge_label')).toBe('cls-edge')
  })
})

describe('metricsFrom', () => {
  it('subtracts padding/border and retains fractional bounds', () => {
    const m = metricsFrom({ width: 43.2, height: 20.85 }, { padX: 12, padY: 6 }, 14.85, 'yes')
    expect(m.width).toBeCloseTo(31.2)
    expect(m.height).toBeCloseTo(14.85)
    expect(m.lines).toBe(1)
  })
  it('counts wrapped lines from the content height', () => {
    expect(metricsFrom({ width: 240, height: 35.1 }, { padX: 0, padY: 0 }, 17.55, 'long text')).toEqual({ width: 240, height: 35.1, lines: 2 })
  })
  it('reports nothing for empty text', () => {
    expect(metricsFrom({ width: 12, height: 6 }, { padX: 12, padY: 6 }, 14.85, '')).toEqual({ width: 0, height: 0, lines: 0 })
  })
})

describe('MeasureSurface', () => {
  it('renders each item with its style class, its max width and a content box', () => {
    const { container } = render(
      <MeasureSurface fontFamily="Inter" items={[
        { key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 },
        { key: 'edge:0', text: 'yes', style: 'edge_label', maxWidth: 160 },
      ]} />,
    )
    const root = container.querySelector<HTMLElement>('[data-measure-root]')!
    expect(root.style.visibility).toBe('hidden')
    expect(root.style.fontFamily).toBe('Inter')
    const label = container.querySelector<HTMLElement>('[data-measure-key="node:a:label"]')!
    expect(label.className).toBe('cls-label')
    expect(label.textContent).toBe('Start')
    expect(label.style.maxWidth).toBe('240px')
    expect(label.style.boxSizing).toBe('content-box')
    expect(container.querySelector<HTMLElement>('[data-measure-key="edge:0"]')!.className).toBe('cls-edge')
  })
})

describe('runMeasureJob', () => {
  it('returns metrics for every requested key and never claims fonts it cannot verify', async () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    const m = await runMeasureJob({ mode: 'measure', request: { contract: contract as RendererContract, items: [
      { key: 'node:a:label', text: 'Start', style: 'node_label', maxWidth: 240 },
      { key: 'edge:0', text: '', style: 'edge_label', maxWidth: 160 },
    ] } }, root, document, window)
    expect(Object.keys(m.items).sort()).toEqual(['edge:0', 'node:a:label'])
    expect(m.items['edge:0']).toEqual({ width: 0, height: 0, lines: 0 })
    expect(m.fontsReady).toBe(false) // jsdom has no FontFaceSet
    expect(m.contract.version).toBe('arranged-renderer/1')
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import, then 5 behavioral failures (6 tests); later duplicate-key boundary test failed before adding validation.

```bash
cd dashboard && npx vitest run src/render/MeasureSurface.test.tsx
```
Expected: `FAIL`, `Failed to resolve import "./MeasureSurface"`.

- [x] **Step 3: Minimal implementation.** Create `dashboard/src/render/MeasureSurface.tsx`:

```tsx
// Measure mode of the render surface: lays every requested text item out with
// the exact CSS class the arranged renderer uses for that style, so Phase 1
// sizes boxes from the real browser, fonts and wrapping.
import { flushSync } from 'react-dom'
import type { Root } from 'react-dom/client'
import { nodeTextClassNames } from '../graph/arranged/ArrangedNode'
import { edgeLabelClassName } from '../graph/arranged/RoutedEdge'
import type { MeasureJob, Measurements, TextItem, TextMetrics, TextStyle } from './job'
import { RENDERER_CONTRACT } from '../graph/arranged/validate'
import { ensureFonts, settle } from './report'

/** The renderer class for each text style (identical to ArrangedNode / RoutedEdge). */
export function classForStyle(style: TextStyle): string {
  switch (style) {
    case 'node_label':
      return nodeTextClassNames.label
    case 'node_type':
      return nodeTextClassNames.typeCaption
    case 'edge_label':
      return edgeLabelClassName
  }
}

export interface BoxExtras { padX: number; padY: number }

/**
 * Content-box metrics from a laid-out element: the border box minus padding
 * and border, retaining actual fractional bounds; domain ceilPx owns box
 * rounding/slack. Lines come from actual content height and line height.
 */
export function metricsFrom(rect: { width: number; height: number }, extras: BoxExtras, lineHeightPx: number, text: string): TextMetrics {
  const w = Math.max(0, rect.width - extras.padX)
  const h = Math.max(0, rect.height - extras.padY)
  if (text === '') return { width: 0, height: 0, lines: 0 }
  return { width: w, height: h, lines: Math.max(1, Math.round(h / lineHeightPx)) }
}

export function MeasureSurface({ items, fontFamily }: { items: TextItem[]; fontFamily: string }) {
  return (
    <div
      data-measure-root="true"
      style={{ position: 'absolute', left: 0, top: 0, width: 0, height: 0, overflow: 'visible', visibility: 'hidden', fontFamily }}
    >
      {items.map((it) => (
        <div
          key={it.key}
          data-measure-key={it.key}
          className={classForStyle(it.style)}
          style={{ position: 'absolute', left: 0, top: 0, margin: 0, width: 'max-content', maxWidth: it.maxWidth, boxSizing: 'content-box', transform: 'none' }}
        >
          {it.text}
        </div>
      ))}
    </div>
  )
}

function px(v: string): number {
  const n = parseFloat(v)
  return Number.isFinite(n) ? n : 0
}

export async function runMeasureJob(job: MeasureJob, root: Root, doc: Document, win: Window): Promise<Measurements> {
  const items = job.request.items
  await ensureFonts(doc, RENDERER_CONTRACT, items.map(it => it.text))
  flushSync(() => root.render(<MeasureSurface items={items} fontFamily={RENDERER_CONTRACT.fontFamily} />))
  await doc.fonts?.ready
  await settle(doc, win)
  const fonts = await ensureFonts(doc, RENDERER_CONTRACT, items.map(it => it.text))

  const out: Record<string, TextMetrics> = {}
  for (const it of items) {
    const el = doc.querySelector(`[data-measure-key="${CSS.escape(it.key)}"]`)
    if (!el) throw new Error(`measure surface: item ${it.key} did not render`)
    const cs = win.getComputedStyle(el)
    const extras = {
      padX: px(cs.paddingLeft) + px(cs.paddingRight) + px(cs.borderLeftWidth) + px(cs.borderRightWidth),
      padY: px(cs.paddingTop) + px(cs.paddingBottom) + px(cs.borderTopWidth) + px(cs.borderBottomWidth),
    }
    const fontPx = px(cs.fontSize)
    const lh = parseFloat(cs.lineHeight)
    const lineHeightPx = Number.isFinite(lh) && lh > 0 ? lh : fontPx * RENDERER_CONTRACT.lineHeight
    out[it.key] = metricsFrom(el.getBoundingClientRect(), extras, lineHeightPx, it.text)
  }
  return { contract: RENDERER_CONTRACT, fontsReady: fonts.fontsReady && fonts.fontFamilyResolved, items: out }
}
```

- [x] **Step 4: Run it and confirm it passes.**

Observed: 7 tests and TypeScript pass. Real Chrome additionally checks computed typography parity, Unicode glyph metrics, changing maxWidth at the metric sink, empty jobs/text and unsupported glyph rejection.

```bash
cd dashboard && npx vitest run src/render/MeasureSurface.test.tsx && npx tsc --noEmit
```
Expected: `Tests  6 passed (6)`; tsc clean.

- [x] **Step 5: Commit.**

```bash
git add dashboard/src/render/MeasureSurface.tsx dashboard/src/render/MeasureSurface.test.tsx && git commit -m "feat(render): measure mode lays out text with the renderer's own classes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.6: `render.html` entry, `main.tsx` boot and Vite multi-page build

**Files:**
- Create: `dashboard/render.html`
- Create: `dashboard/src/render/main.tsx`
- Modify: `dashboard/vite.config.ts:1-14` (whole file; add the second build input)
- Test:   `dashboard/src/render/renderPage.test.ts`

Vite 8 builds with Rolldown. `build.rollupOptions` is a deprecated alias there, so the input goes in `build.rolldownOptions.input`.

- [x] **Step 1: Write the failing test.** Create `dashboard/src/render/renderPage.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import html from '../../render.html?raw'

// render.html is the second Vite entry; the Go browser renderer navigates to
// /render.html?job=<token>. These guards keep captures deterministic.
describe('render.html', () => {
  it('boots the render surface entry into #render-root', () => {
    expect(html).toContain('<div id="render-root"></div>')
    expect(html).toContain('<script type="module" src="/src/render/main.tsx"></script>')
  })
  it('freezes capture after runtime animation phase probes', () => {
    expect(html).toContain('transition: none !important')
    expect(html).toContain('animation-play-state: paused !important')
  })
  it('limits hit-testing to arranged content so occlusion probes are meaningful', () => {
    expect(html).toContain('#render-root * { pointer-events: none !important; }')
    expect(html).toContain('#render-root path[data-edge-path] { pointer-events: auto !important; }')
  })
  it('hides the React Flow attribution so only the graph is painted', () => {
    expect(html).toContain('.react-flow__attribution { display: none !important; }')
  })
})
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing import, then 4 behavioral failures against a minimal valid HTML document. `test -f dist/render.html` exited 1 before implementation.

```bash
cd dashboard && npx vitest run src/render/renderPage.test.ts
```
Expected: `FAIL`, `Failed to resolve import "../../render.html?raw"` (the file does not exist yet).

- [x] **Step 3: Implementation.**

Create `dashboard/render.html`:

```html
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Arbitron render surface</title>
    <style>
      html, body { margin: 0; overflow: hidden; }
      /* Deterministic baseline capture; runRenderJob separately samples animation extrema using the Web Animations API. */
      *, *::before, *::after { transition: none !important; animation-play-state: paused !important; caret-color: transparent !important; }
      /* Occlusion hit-testing: only arranged content (nodes, edge labels, edge strokes) is hit-testable. */
      #render-root * { pointer-events: none !important; }
      #render-root [data-arranged-node], #render-root [data-arranged-node] *,
      #render-root [data-edge-label], #render-root [data-edge-label] *,
      #render-root path[data-edge-path] { pointer-events: auto !important; }
      /* Nothing but the graph is painted. */
      .react-flow__attribution { display: none !important; }
    </style>
  </head>
  <body>
    <div id="render-root"></div>
    <script type="module" src="/src/render/main.tsx"></script>
  </body>
</html>
```

Create `dashboard/src/render/main.tsx`:

```tsx
// Entry of dashboard/render.html — the deterministic render surface driven by
// the Go browser renderer (adapter/renderreview). Not used by the dashboard UI.
import { createRoot } from 'react-dom/client'
import '../styles/theme.css'
import '../graph/arranged/fonts'
import { fetchJob } from './job'
import { runMeasureJob } from './MeasureSurface'
import { runRenderJob } from './RenderSurface'

async function boot(): Promise<void> {
  window.__ARBITRON_RENDER_STATE__ = 'loading'
  try {
    const job = await fetchJob(window.location.search)
    const root = createRoot(document.getElementById('render-root')!)
    if (job.mode === 'measure') {
      window.__ARBITRON_MEASURE_RESULT__ = await runMeasureJob(job, root, document, window)
    } else {
      window.__ARBITRON_RENDER_REPORT__ = await runRenderJob(job, root, document, window)
    }
    window.__ARBITRON_RENDER_STATE__ = 'ready'
  } catch (err) {
    window.__ARBITRON_RENDER_ERROR__ = err instanceof Error ? err.message : String(err)
    window.__ARBITRON_RENDER_STATE__ = 'error'
  }
}

void boot()
```

Replace `dashboard/vite.config.ts` (currently lines 1-14) with:

```ts
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api/v1/ws': { target: 'ws://localhost:8080', ws: true },
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Two pages: the dashboard SPA and the deterministic render surface used
    // by the Go browser renderer (adapter/renderreview).
    rolldownOptions: {
      input: {
        main: fileURLToPath(new URL('./index.html', import.meta.url)),
        render: fileURLToPath(new URL('./render.html', import.meta.url)),
      },
    },
  },
})
```

- [x] **Step 4: Run the tests, the type-check and the production build, and confirm the build has both pages.**

Observed: 6 render suites / 84 tests pass, TypeScript passes, production build contains both pages and bundled fonts; final grep printed `/assets/render-CX129u0s.js`. Real Chrome also navigates the actual render.html/main.tsx entry and asserts nonempty render/measure globals from token-specific fetched jobs, plus missing/malformed/unknown tokens and invalid node types reaching error rather than ready.

```bash
cd dashboard && npx vitest run src/render && npx tsc --noEmit && npm run build && test -f dist/index.html && test -f dist/render.html && grep -o '/assets/render-[A-Za-z0-9_-]*\.js' dist/render.html
```
Expected:
- vitest prints `Test Files  6 passed (6)` and `Tests  58 passed (58)`.
- `vite build` lists `dist/render.html`, `dist/assets/render-<hash>.js`, `dist/assets/render-<hash>.css` and the `inter-*-{400,500,600,700}-normal-*.woff2` font files.
- The final `grep` prints one line `/assets/render-<hash>.js`.

Before Step 3, `test -f dist/render.html` exits 1.

- [x] **Step 5: Run the whole dashboard gate.**

Observed: TypeScript passes; 66 suites / 887 tests pass. The SPA entry is unchanged, so every existing suite must stay green.

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```
Expected: all test files pass. The `src/render/*` files are part of the run.

- [x] **Step 6: Commit.** `dashboard/dist` is git-ignored. The embedded copy is refreshed and committed by P3T5 (`make ui`).

```bash
git add dashboard/render.html dashboard/src/render/main.tsx dashboard/src/render/renderPage.test.ts dashboard/vite.config.ts && git commit -m "feat(render): build render.html as a second Vite entry" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Producer of the page's input (data source):**
  - The job document is written by `adapter/renderreview/browser.go:(*BrowserRenderer).Render` and `.Measure` (P3T5), which register it in `assetServer.register`.
  - The geometry comes from `domain/layout/arrange.go:Arrange` (P1T6). The measure items come from `domain/layout/measure.go:MeasureRequestFor` (P1T4).
  - Both are invoked per job by `domain/layout/coordinator.go:(*Coordinator).Run` (P1T9), which `domain/layout/worker.go:(*Worker)` (P2T8) runs for each saved revision captured by `RevisionStamper` (P2T2/P2T3/P2T4).
  - `workflowNodeTypes` is copied from RenderRequest.WorkflowNodeTypes by P3T5; P1T9 fills it directly from immutable Job.Manifest.Nodes[].Type. Revision/attempt travel with that job. Caption reversal is forbidden. The contract is `dashboard/src/graph/arranged/contract.json` (P1T2), and its Go twin is asserted equal by P1T2's `contract_test.go`.
- **Consumer and wiring:**
  - P3T5 embeds `render.html` (via `make ui` → `adapter/dashboard/dist`, exported by `adapter/dashboard.Assets()`), serves it on its loopback listener and drives it with headless Chrome. P3T5's real-browser tests (`TestBrowserRenderer_RealSurfaceMeasure`, `TestBrowserRenderer_RealSurfaceRender`) are this page's provenance probes: they fail if `render.html` is missing from the embedded build, if the page cannot mount `ArrangedGraph`, or if the report is empty.
  - P3T7 puts that renderer on the live path in `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer`, and its acceptance e2e drives the page from `initDomain` for real samples.
- **Shared renderer:** the page renders `ArrangedGraph` (P3T3) built from `ArrangedNode`/`RoutedEdge` (P3T2). The same components are used by `WorkflowReview` (P3T12) and `ExecutionView` (P3T13), so the reviewed drawing is the displayed drawing.

### Integration TDD: collect actual text, full routes and visible arrow shapes

**Files:** `dashboard/src/render/job.ts`, `report.ts`, `report.test.ts`, `RenderSurface.tsx` and tests.

- [x] **RED:** Extend report JSON shape tests with the §6.5 fields. Real-browser tests (P3T5/P3T6)
  must alter textContent, suppress glyph ink via opacity, change an intermediate SVG
  segment with identical endpoints, and rotate/hide a marker. Each actual report must
  describe the mutation, and reviewer must block. Test state probes detect moving branch
  labels/gaps as well as moving node boxes. Keep unknown state and missing font errors.

> RESOLVED (production revisit, 2026-10-17): The assigned browser/reviewer/root dependencies now exist. `TestLayoutRender_RealSurfaceReportDefectsReachSavedResults` drives real authoring saves through initDomain → SQLite worker/coordinator → embedded render.html → Chrome DOM/PNG → production reviewer → immutable result/row → authorised QueryService. The previously missing production acceptance is now verified, including actual text, ancestor opacity, identical-endpoint/different-middle routes, hidden/rotated markers, running-state branch labels and crossing gaps, and all-white captures. This closes P3T4's surface-provenance gate, not P3T7/P3T14's separately blocked complex-sample release gate.

- [x] Run `cd dashboard && npx vitest run src/render && npx tsc --noEmit` and observe the behavioral failure before implementing the correction.

Observed behavioral REDs include compiling declarations (helpers, protocol, surfaces), collector fields, exact font ranges and malformed CSS colours; real Chrome additionally exposed marker-opacity/CSS-transform and animation-settling defects before their corrections.

- [x] **GREEN:** Mirror RenderedSegment, RenderedText/Edge additions exactly. Collect DOM Content,
  Range Glyphs and cumulative Visible, actual transformed M/L subpaths and marker outline,
  computed stroke/dash/visibility. A marker reference with any child is insufficient:
  it must paint a visible correctly oriented arrow. State comparisons cover all drawn
  geometry, and measurements use the same loaded fonts/CSS. Reject unsupported SVG
  syntax explicitly rather than supplying candidate-derived values.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.

Observed: replacing actual DOM content with empty text fails the collector sink assertion. Removing the requested maxWidth from MeasureSurface fails the real Chrome `maxWidth must feed the metric sink` assertion. Both mutations were restored. Final focused tests (84), whole-dashboard tests (887), TypeScript, real Chrome and production multi-page build pass.

- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

**Self-review (in order):** Spec-compliance review tightened malformed CSS rejection, exact face-range inventory checks, marker CSS-transform/path-opacity handling and animation settling before explicit phase sampling. Anti-green-theater review exercised the actual page entry and metric/report sinks with wrong/missing input, runtime mutations and restored removal probes above; no mock renderer is called production evidence. Code-quality review retained the existing ES2020 own-property idiom, shared typography classes/fonts/validator, scoped marker references and explicit unsupported-shape errors. No extra production flags/options, fake adapter or unrelated route changes were added. Existing untracked docs/visuals work was left untouched.

**Runtime-probe implementation:** Before each state snapshot, use getAnimations() to set
currentTime to start/mid/end phases, including active-edge dash extrema, then pause for
measurement. Test running+selected together as well as isolated states. Remove the HTML's
blanket transition/animation suppression during probes; freeze only for final baseline
capture. StateProbe success is conjunction across phases, not whichever frame was last.

**Font helper implementation (Task 4.3):** `ensureFonts(doc, contract, texts)` delegates
to P3T2 ensureBundledTextFonts. contractFonts/fontFamilyResolved tests enumerate individual
families and actual required ranges/weights; update their old Inter-only fixtures. Never
pass `"Inter, Noto Sans SC, Noto Emoji"` as a single family. MeasureSurface's CSS inherits
the same stack, not a quoted whole-string family; add computed-style parity tests.

## Production revisit evidence (2026-10-17)

- Selection: skipped the already-recorded profile, rename/seed/authorization-lock and P3T1/P3T2 contract/environment blockers. Worked only P3T4's newly available production surface acceptance, not a sibling release gate.
- Added `cmd/arbitron/layout_surface_report_e2e_test.go`. Nine real-root cases use saved Unicode node/branch text and an authored K3,3 graph that produces actual crossing gaps. Only render.html's shell is fault-injected; the observer delegates every measurement/render/review argument to the production factory. Assertions cover the actual DOM report, unchanged manifest bytes, exact captured revision/digest, genuine hashed PNGs, persisted blocking findings and authorised query status. The untouched baseline must pass; missing inputs/wiring cannot satisfy it.
- Behavioral RED → GREEN: with runtime stability reporting removed and embedded assets rebuilt, the branch-label regression failed at the SQLite sink (`needs_attention` expected, `passed` actual; `/tmp/p3t4-red.log`). Restoring the existing implementation and rebuilding made all nine cases pass (64.237s). This is acceptance coverage for previously implemented behavior; no new production feature or policy was necessary. Corrected a test's exact floating-point endpoint comparison to the existing screen-to-canvas tolerance without weakening route/endpoint checks.
- Ordered self-review: **(1) Spec compliance:** kept the exact surface/report slice, including running-state gap and branch-label changes, hidden/rotated arrows, full-route comparison, unknown-state/font-error regressions and real blank captures. No algorithm, threshold, retry policy or extra option changed. **(2) Anti-green-theater:** assert specific BLOCKING criteria at immutable review, row and query sinks, not merely intermediate reports. Require actual gap/label instability issues. Every all-white PNG is decoded and checked; independently disabling the overview pixel guard fails the raster-specific assertion (`/tmp/p3t4-raster-red.log`), even though DOM visibility also blocks. Both production mutations are restored. **(3) Quality:** followed neighbouring root/asset/probe helpers, constructed the crossing fixture directly, kept globals cleanup-scoped and tests serial, used the existing factory/worker/store rather than replacement verdicts, and ran gofmt/diff checks. Existing untracked `docs/visuals/` is untouched.
- Final verification after review: real-root nine-case suite passes under `-race` (122.295s, browser required, no skips); existing real-surface DOM-mutation + real-browser reviewer-defect tests pass (65.293s); all 84 render tests and TypeScript pass; full dashboard rerun passes **70 files / 1,027 tests**. Its first concurrent run reproduced the previously recorded ArrangedGraph anchor timing flake; no assertion or timeout was changed, and the standalone full rerun is green. `make ui` rebuilt both embedded entries after restoring the mutation; `go build ./...`, focused Go vet and `git diff --check` pass. No production/embedded diff or knowingly failing acceptance test remains.
