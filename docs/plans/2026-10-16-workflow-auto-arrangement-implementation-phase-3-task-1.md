# Workflow Auto-Arrangement — Phase 3 Task 1: Dashboard layout types, payload validation, API client

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

**Alignment requirements:** Read contracts §6 (6.6) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Give the dashboard a typed mirror of the server's layout payloads, a client-side geometry validator, and API-client methods for the workflow and instance layout endpoints.

**Architecture:** `dashboard/src/graph/arranged/types.ts` mirrors the Go JSON of `domain/layout` (Geometry, RendererContract, View, RetryAck) and `domain/workflow` (Presentation) field for field. `validate.ts` imports the shared `contract.json` (written by P1T2) as `RENDERER_CONTRACT` and decides whether a stored geometry can be drawn for a given `WorkflowDefinition` (format, full contract equality, graph text/identity, node/edge coverage and order, finite geometry and topology). `api/client.ts` gains `getLayout`/`retryLayout` for workflows and instances, which call the P3T9 HTTP endpoints. `WorkflowDefinition.presentation` and `Instance.layout` carry the saved revision used to join the manifest and the layout.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters, relative imports only), React 19, @xyflow/react 12, Vitest 4 + jsdom + Testing Library, CSS modules, dagre (fallback only).

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · Design: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` (§4 shared renderer, §5 navigation/status/refresh, §6 remove manual positioning) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS + §5 Addenda 5–7

**Prerequisite:** `dashboard/src/graph/arranged/contract.json` exists (P1T2). It holds exactly the `RendererContract` JSON keys/values in contracts §1. `tsconfig.json` uses `moduleResolution: "bundler"`, so TypeScript 6 enables `resolveJsonModule` by default. No tsconfig change is needed.

---

### Task 1.1: Layout payload types + `validateGeometry` + shared test fixtures

**Files:**
- Create: `dashboard/src/graph/arranged/types.ts`
- Create: `dashboard/src/graph/arranged/validate.ts`
- Create: `dashboard/src/graph/arranged/testFixtures.ts` (test-only fixtures; P3T2/P3T3/P3T11–13 tests import it; production code never does)
- Modify: `dashboard/src/graph/types.ts:1` (add the type import) and `:20-28` (`WorkflowDefinition` gains `presentation?`)
- Test: `dashboard/src/graph/arranged/validate.test.ts`

**Interfaces**

Consumes:
- `dashboard/src/graph/arranged/contract.json` (P1T2): keys exactly as `RendererContract` below, `"version": "arranged-renderer/1"`.
- `WorkflowDefinition`, `WorkflowNode`, `WorkflowEdge` from `dashboard/src/graph/types.ts` (existing).

Produces (contracts §3, verbatim; these additions are not in contracts and are marked ★):
```ts
// types.ts — Point, Rect, EdgeKind, Side, Attachment, LabelBox, NodeBox, EdgeRoute, Crossing,
// RendererContract, Geometry, DisplayStatus, Provenance, LayoutFinding, RetainedLayout,
// LayoutEvidence, LayoutView, RetryAck, Presentation (full definitions in Step 1)
export const ACTIVE_STATUSES: ReadonlySet<DisplayStatus> // pending, arranging, rendering, reviewing
// validate.ts
export const RENDERER_CONTRACT: RendererContract            // ★ contract.json as imported
export const GEOMETRY_FORMAT = 'arbitron.layout/v1'           // ★
export function validateGeometry(def: WorkflowDefinition, geo: Geometry, contract: RendererContract): { ok: boolean; issues: string[] }
export function resolveDisplayGeometry(def: WorkflowDefinition, view: LayoutView | null): { geometry: Geometry | null; issues: string[] } // ★
// testFixtures.ts ★ (test-only)
export function fixtureWorkflow(): WorkflowDefinition   // a→b→c + loop return c→b, presentation.revision "rev-1"
export function fixtureGeometry(): Geometry             // valid for fixtureWorkflow(), distinctive non-dagre boxes
export function fixtureView(overrides?: Partial<LayoutView>): LayoutView // complete/passed view carrying fixtureGeometry()
// graph/types.ts
interface WorkflowDefinition { /* existing */ presentation?: Presentation }
```

- [x] **Step 1: Create the payload type mirror** (declarations only, no behaviour to test). Create `dashboard/src/graph/arranged/types.ts`:

```ts
// Mirrors the Go JSON of domain/layout (geometry.go, contract.go, reader.go,
// query.go) and domain/workflow/presentation.go exactly. Field names are the
// Go `json:"..."` tags; optional fields are the Go `omitempty` ones.

export interface Point { x: number; y: number }
export interface Rect { x: number; y: number; w: number; h: number }
export type EdgeKind = 'forward' | 'loop_return' | 'self_loop'
export type Side = 'top' | 'bottom' | 'left' | 'right'
export interface Attachment { nodeId: string; side: Side; point: Point }
export interface LabelBox { text: string; box: Rect; lines: number }
export interface NodeBox { id: string; box: Rect; text: string; typeCaption: string; textLines: number; rank: number }
export interface EdgeRoute {
  ordinal: number
  source: string
  target: string
  output?: string
  label?: string
  kind: EdgeKind
  side?: Side
  lane?: number
  points: Point[]
  sourceAttach: Attachment
  targetAttach: Attachment
  text?: LabelBox
}
export interface Crossing { point: Point; over: number; under: number }
export interface RendererContract {
  version: string
  fontFamily: string
  fontPackage: string
  nodeLabelFontPx: number
  nodeLabelWeight: number
  nodeTypeFontPx: number
  nodeTypeWeight: number
  edgeLabelFontPx: number
  edgeLabelWeight: number
  lineHeight: number
  nodePaddingX: number
  nodePaddingY: number
  nodeBorderPx: number
  nodeTypeBarPx: number
  nodeTextGapPx: number
  nodeMinWidth: number
  nodeTextMaxWidth: number
  edgeLabelPaddingX: number
  edgeLabelPaddingY: number
  edgeLabelMaxWidth: number
  strokePx: number
  arrowPx: number
  crossingTreatment: string
  crossingGapPx: number
  forwardColor: string
  loopReturnColor: string
  loopReturnDash: string
  practicalZoom: number
  minReadableTextPx: number
  minContrastRatio: number
  minClearancePx: number
}
export interface Geometry {
  format: string
  contract: RendererContract
  options: Record<string, number>
  nodes: NodeBox[]
  edges: EdgeRoute[]
  crossings: Crossing[]
  canvas: Rect
}
export type DisplayStatus =
  | 'pending'
  | 'arranging'
  | 'rendering'
  | 'reviewing'
  | 'complete'
  | 'needs_attention'
  | 'failed'
  | 'legacy'
  | 'unavailable'
export type Provenance = 'arranged' | 'retained_geometry' | 'retained_positions' | 'legacy'
export interface LayoutFinding {
  criterion: string
  severity: 'blocking' | 'advisory'
  source: 'preflight' | 'review'
  nodeIds?: string[]
  edgeOrdinals?: number[]
  evidence: string
}
export interface RetainedLayout {
  source: 'layout' | 'manifest' | 'none'
  fromRevision?: string
  fromGeneration?: number
  positions?: Record<string, { x: number; y: number }>
}
export interface LayoutEvidence {
  geometryDigest: string
  rendererVersion: string
  contract: RendererContract
  fontsReady: boolean
  zoom: number
  tiles: number
  artifacts: Array<{ kind: string; path: string; sha256: string; region: Rect; scale: number }>
  reviewedAt: string
}
export interface LayoutView {
  savedRevision: string
  jobGeneration: number
  resultGeneration: number
  state?: string
  displayStatus: DisplayStatus
  stage?: string
  attemptsUsed: number
  attemptLimit: number
  findings: LayoutFinding[] | null
  failureStage?: string
  failureReason?: string
  evidence?: LayoutEvidence
  contract?: RendererContract
  geometry?: Geometry
  retained?: RetainedLayout
  provenance: Provenance
  unreviewed: boolean
  retryEligible: boolean
  available: boolean
  availabilityError?: string
}
export interface RetryAck { savedRevision: string; jobGeneration: number; state: string; displayStatus: DisplayStatus }
export interface Presentation {
  revision: string
  savedAt: string
  reason: string
  scope: string
  ownerId?: string
  name: string
  retained: RetainedLayout
}

/** Display statuses during which the server is still working on the revision (poll while in this set). */
export const ACTIVE_STATUSES: ReadonlySet<DisplayStatus> = new Set<DisplayStatus>([
  'pending',
  'arranging',
  'rendering',
  'reviewing',
])
```

- [x] **Step 2: Add `presentation` to `WorkflowDefinition`.** In `dashboard/src/graph/types.ts`, insert at line 1:

```ts
import type { Presentation } from './arranged/types'
```

and replace lines 26-28:

```ts
  inputs?: Array<{ name: string; type: string; description: string; default?: unknown; required: boolean }>
  defaultErrorBehavior?: Record<string, unknown>
}
```

with:

```ts
  inputs?: Array<{ name: string; type: string; description: string; default?: unknown; required: boolean }>
  defaultErrorBehavior?: Record<string, unknown>
  /** Saved-revision metadata stamped by the server on every authoring save (absent for pre-feature saves). */
  presentation?: Presentation
}
```

- [x] **Step 3: Write the shared test fixtures.** Create `dashboard/src/graph/arranged/testFixtures.ts`. Later tasks' tests reuse it. The coordinates are deliberately distinctive so a test can tell geometry-driven positions from dagre ones.

```ts
// Shared, deterministic fixtures for the arranged-renderer tests. Not imported
// by production code.
import type { WorkflowDefinition } from '../types'
import type { Geometry, LayoutView } from './types'
import { RENDERER_CONTRACT } from './validate'

/** a → b → c, plus the loop return c → b. Saved as revision "rev-1". */
export function fixtureWorkflow(): WorkflowDefinition {
  return {
    name: 'wf',
    description: '',
    author: '',
    nodes: [
      { id: 'a', type: 'script', label: 'Start', description: '', config: {}, entrypoint: true },
      { id: 'b', type: 'llm_call', label: 'Draft a very long label that must wrap', description: '', config: {} },
      { id: 'c', type: 'conditional', label: 'Check', description: '', config: {} },
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

/** A valid arranged geometry for fixtureWorkflow() with distinctive, non-dagre coordinates. */
export function fixtureGeometry(): Geometry {
  return {
    format: 'arbitron.layout/v1',
    contract: { ...RENDERER_CONTRACT },
    options: { rankSpacing: 48, nodeSpacing: 56, laneSeparation: 14, gutterWidth: 48, labelGap: 10, portSpacing: 24, nodeTextSlack: 0, dummyWidth: 24, crossingSweeps: 8, margin: 40 },
    nodes: [
      { id: 'a', box: { x: 40, y: 40, w: 180, h: 52 }, text: 'Start', typeCaption: 'script', textLines: 1, rank: 0 },
      { id: 'b', box: { x: 40, y: 140, w: 200, h: 70 }, text: 'Draft a very long label that must wrap', typeCaption: 'llm call', textLines: 2, rank: 1 },
      { id: 'c', box: { x: 40, y: 260, w: 180, h: 52 }, text: 'Check', typeCaption: 'conditional', textLines: 1, rank: 2 },
    ],
    edges: [
      {
        ordinal: 0, source: 'a', target: 'b', kind: 'forward',
        points: [{ x: 130, y: 92 }, { x: 130, y: 116 }, { x: 140, y: 116 }, { x: 140, y: 140 }],
        sourceAttach: { nodeId: 'a', side: 'bottom', point: { x: 130, y: 92 } },
        targetAttach: { nodeId: 'b', side: 'top', point: { x: 140, y: 140 } },
      },
      {
        ordinal: 1, source: 'b', target: 'c', output: 'yes', kind: 'forward',
        points: [{ x: 140, y: 210 }, { x: 140, y: 235 }, { x: 130, y: 235 }, { x: 130, y: 260 }],
        sourceAttach: { nodeId: 'b', side: 'bottom', point: { x: 140, y: 210 } },
        targetAttach: { nodeId: 'c', side: 'top', point: { x: 130, y: 260 } },
        text: { text: 'yes', box: { x: 146, y: 222, w: 30, h: 21 }, lines: 1 },
      },
      {
        ordinal: 2, source: 'c', target: 'b', label: 'again', kind: 'loop_return', side: 'right', lane: 1,
        points: [{ x: 220, y: 286 }, { x: 268, y: 286 }, { x: 268, y: 175 }, { x: 240, y: 175 }],
        sourceAttach: { nodeId: 'c', side: 'right', point: { x: 220, y: 286 } },
        targetAttach: { nodeId: 'b', side: 'right', point: { x: 240, y: 175 } },
        text: { text: 'again', box: { x: 274, y: 220, w: 44, h: 21 }, lines: 1 },
      },
    ],
    crossings: [],
    canvas: { x: 0, y: 0, w: 360, h: 352 },
  }
}

/** A complete (passed) view for revision "rev-1" carrying fixtureGeometry(). */
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
```

- [x] **Step 4: Write the failing test.** Create `dashboard/src/graph/arranged/validate.test.ts`:

```ts
import { describe, it, expect } from 'vitest'
import { validateGeometry, resolveDisplayGeometry, RENDERER_CONTRACT, GEOMETRY_FORMAT } from './validate'
import { fixtureGeometry, fixtureView, fixtureWorkflow } from './testFixtures'
import type { Geometry } from './types'

const def = fixtureWorkflow()

describe('RENDERER_CONTRACT', () => {
  it('is the shared contract.json (version arranged-renderer/1, Inter font)', () => {
    expect(RENDERER_CONTRACT.version).toBe('arranged-renderer/1')
    expect(RENDERER_CONTRACT.fontFamily).toBe('Inter, Noto Sans SC, Noto Emoji')
    expect(RENDERER_CONTRACT.nodeTextMaxWidth).toBe(240)
    expect(RENDERER_CONTRACT.loopReturnDash).toBe('6 4')
    expect(GEOMETRY_FORMAT).toBe('arbitron.layout/v1')
  })
})

describe('validateGeometry — happy path', () => {
  it('accepts a geometry covering every node and edge in manifest order', () => {
    expect(validateGeometry(def, fixtureGeometry(), RENDERER_CONTRACT)).toEqual({ ok: true, issues: [] })
  })

  it('rejects a crossing point that is not on both referenced routes', () => {
    const geo = fixtureGeometry()
    geo.crossings = [{ point: { x: 140, y: 230 }, over: 1, under: 0 }]
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).ok).toBe(false)
  })
})

describe('validateGeometry — version and format', () => {
  it('rejects a renderer contract version mismatch', () => {
    const geo = fixtureGeometry()
    geo.contract = { ...geo.contract, version: 'arranged-renderer/2' }
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.ok).toBe(false)
    expect(r.issues).toContain('renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"')
  })

  it('rejects an unknown payload format', () => {
    const geo = { ...fixtureGeometry(), format: 'arbitron.layout/v0' }
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.ok).toBe(false)
    expect(r.issues).toContain('format "arbitron.layout/v0" is not arbitron.layout/v1')
  })
})

describe('validateGeometry — coverage and order', () => {
  it('rejects a geometry that is missing a node', () => {
    const geo = fixtureGeometry()
    geo.nodes = geo.nodes.slice(0, 2)
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.ok).toBe(false)
    expect(r.issues).toContain('geometry has 2 nodes, workflow has 3')
  })

  it('rejects nodes that are out of manifest order', () => {
    const geo = fixtureGeometry()
    geo.nodes = [geo.nodes[1], geo.nodes[0], geo.nodes[2]]
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.issues).toContain('node 0 is "b", expected "a"')
  })

  it('rejects an empty geometry for a non-empty workflow', () => {
    const geo: Geometry = { ...fixtureGeometry(), nodes: [], edges: [] }
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.ok).toBe(false)
    expect(r.issues).toContain('geometry has 0 nodes, workflow has 3')
    expect(r.issues).toContain('geometry has 0 edges, workflow has 3')
  })

  it('rejects an edge whose endpoints differ from the manifest edge at that ordinal', () => {
    const geo = fixtureGeometry()
    geo.edges[1] = { ...geo.edges[1], source: 'a' }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain(
      'edge 1 routes a->c, expected b->c',
    )
  })

  it('rejects an edge whose ordinal is not its index', () => {
    const geo = fixtureGeometry()
    geo.edges[2] = { ...geo.edges[2], ordinal: 7 }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain('edge 2 has ordinal 7')
  })

  it('rejects a crossing that references an edge ordinal that does not exist', () => {
    const geo = fixtureGeometry()
    geo.crossings = [{ point: { x: 1, y: 1 }, over: 0, under: 9 }]
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain(
      'crossing 0 references an unknown edge',
    )
  })
})

describe('validateGeometry — numbers', () => {
  it('rejects a node box with a non-finite coordinate', () => {
    const geo = fixtureGeometry()
    geo.nodes[0] = { ...geo.nodes[0], box: { x: Number.NaN, y: 0, w: 180, h: 52 } }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain('node "a" has an invalid box')
  })

  it('rejects a node box with zero width', () => {
    const geo = fixtureGeometry()
    geo.nodes[2] = { ...geo.nodes[2], box: { x: 0, y: 0, w: 0, h: 52 } }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain('node "c" has an invalid box')
  })

  it('rejects a route with fewer than two points or an infinite point', () => {
    const geo = fixtureGeometry()
    geo.edges[0] = { ...geo.edges[0], points: [{ x: 1, y: 1 }] }
    geo.edges[1] = { ...geo.edges[1], points: [{ x: 1, y: 1 }, { x: Number.POSITIVE_INFINITY, y: 2 }] }
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.issues).toContain('edge 0 has an invalid route')
    expect(r.issues).toContain('edge 1 has an invalid route')
  })

  it('rejects an invalid canvas', () => {
    const geo = { ...fixtureGeometry(), canvas: { x: 0, y: 0, w: Number.NaN, h: 1 } }
    expect(validateGeometry(def, geo, RENDERER_CONTRACT).issues).toContain('canvas is invalid')
  })
})

describe('validateGeometry — malformed payloads never throw', () => {
  it('reports a null geometry as missing', () => {
    const r = validateGeometry(def, null as unknown as Geometry, RENDERER_CONTRACT)
    expect(r).toEqual({ ok: false, issues: ['geometry is missing'] })
  })

  it('reports missing node/edge/crossing arrays', () => {
    const geo = { format: 'arbitron.layout/v1', contract: RENDERER_CONTRACT } as unknown as Geometry
    const r = validateGeometry(def, geo, RENDERER_CONTRACT)
    expect(r.ok).toBe(false)
    expect(r.issues).toEqual(
      expect.arrayContaining(['geometry has no node list', 'geometry has no edge list', 'geometry has no crossing list']),
    )
  })
})

describe('resolveDisplayGeometry', () => {
  it('returns the view geometry when it validates', () => {
    const view = fixtureView()
    expect(resolveDisplayGeometry(def, view)).toEqual({ geometry: view.geometry, issues: [] })
  })

  it('returns null with no issues when the view has no geometry (pending, legacy, retained positions)', () => {
    expect(resolveDisplayGeometry(def, fixtureView({ geometry: undefined }))).toEqual({ geometry: null, issues: [] })
    expect(resolveDisplayGeometry(def, null)).toEqual({ geometry: null, issues: [] })
  })

  it('returns null with the validation issues when the geometry cannot be drawn', () => {
    const geo = fixtureGeometry()
    geo.contract = { ...geo.contract, version: 'arranged-renderer/9' }
    const r = resolveDisplayGeometry(def, fixtureView({ geometry: geo }))
    expect(r.geometry).toBeNull()
    expect(r.issues).toEqual(['renderer contract version "arranged-renderer/9" does not match "arranged-renderer/1"'])
  })
})
```

- [x] **Step 5: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/validate.test.ts
```
Expected:
```text
FAIL  src/graph/arranged/validate.test.ts [ src/graph/arranged/validate.test.ts ]
Error: Failed to resolve import "./validate" from "src/graph/arranged/validate.test.ts". Does the file exist?
```

- [x] **Step 6: Implement.** Create `dashboard/src/graph/arranged/validate.ts`:

> Contract correction (§6.18): finite zero Margin and NodeTextSlack and integral zero CrossingSweeps are valid, matching domain validation and the Go-emitted zero-policy fixture. Other spacing values stay strictly positive. The corrected implementation rule and negative matrix below resolve the contradictory plan; re-run Go→TS parity before checking Step 6.

Implement the pure validator before its consumer. Import contract.json as
`RENDERER_CONTRACT`; export `GEOMETRY_FORMAT = 'arbitron.layout/v1'`. Do not ship the old
version-only comparison. The complete rejection matrix belongs in this step's RED tests:

- Guard unknown shapes before accessing properties, including null array entries, null
  workflow/contract, missing arrays, missing options/attachments and malformed text.
- Compare every expected contract field/value with the bundled contract, reject extra or
  missing keys and non-finite numeric values. A matching version alone does not establish
  compatibility. Keep DOM/font measurement out of this pure validator.
- Require exactly one node per immutable manifest occurrence in manifest order, unique IDs,
  complete displayed text (`label || id`), exact `type.replaceAll('_', ' ')`, positive boxes,
  nonnegative integral ranks and positive integral text line counts.
- Require every edge occurrence exactly once with ordinal=index, endpoints, normalized
  output/label (Go omitempty means empty and omitted both mean absent), kind and branch
  text (`label ?? output` after omitempty normalization). Require label geometry iff text
  exists. Identical parallel occurrences remain separate.
- Require finite axis-aligned nonzero segments, endpoints equal to attachments on their
  correct node's declared boundary, valid sides/kinds/lane rules and no endpoint/side
  mismatch. Check the same structural rules as P1T2 Geometry.Validate.
- Require the complete finite options object with positive spacings, nonnegative
  margin/slack and nonnegative integral crossing sweeps (zero is valid, matching Go). Require canvas origin (0,0), positive size,
  and containment of every box, label, point and attachment. A zero-area canvas is invalid.
- Check crossing ordinals are distinct/in range, finite points lie on both actual routes,
  and crossing records are unique and meet P1T2's allowed intersection rules. An arbitrary
  point associated with two existing ordinals is not a valid crossing.

`resolveDisplayGeometry` first compares `(def.presentation?.revision ?? '')` with
`view.savedRevision`; a foreign token returns null geometry and an explicit revision issue,
including when the foreign value has only retained positions. For same-token absent geometry,
return null; if status is complete, also return a missing-reviewed-geometry issue. Otherwise
validate and return the exact supplied geometry or null plus issues. It never substitutes a
second layout or clears an issue because the response says complete.

Notes:
- Malformed JSON never throws; callers retain captured fallback and show the issue.
- Drawing is permitted only with the full contract implemented by this dashboard build.
- Tests must include same-version modified padding/font, changed full text/type, changed
  branch text, wrong attachment, diagonal route, non-finite options, zero canvas and a
  mismatch where node IDs happen to be identical in the two revisions.

- [x] **Step 7: Run it and confirm it passes**

```bash
cd dashboard && npx vitest run src/graph/arranged/validate.test.ts && npx tsc --noEmit
```
Expected:
```text
 Test Files  1 passed (1)
      Tests  20 passed (20)
(tsc prints nothing and exits 0)
```

- [x] **Step 8: Commit** (from the repo root)

```bash
git add dashboard/src/graph/arranged/types.ts dashboard/src/graph/arranged/validate.ts dashboard/src/graph/arranged/validate.test.ts dashboard/src/graph/arranged/testFixtures.ts dashboard/src/graph/types.ts && git commit -m "feat(dashboard): add arranged layout payload types and geometry validation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 1.2: API client layout endpoints + `Instance.layout`

**Files:**
- Modify: `dashboard/src/api/types.ts:1` (import) and `:100-102` (`Instance` gains `layout?`)
- Modify: `dashboard/src/api/client.ts:17` (import), `:167-171` (after `instances.getVariables`), `:196` (before `workflows.saveLayout`. P3T12 deletes `saveLayout`. Do not touch it here.)
- Test: `dashboard/src/api/client.test.ts` (append a `describe` after line 163)

**Interfaces**

Consumes (P3T9 HTTP surfaces, contracts §3):
- `GET /api/v1/workflows/{name}/layout` → `LayoutView` JSON (`layout.View`)
- `POST /api/v1/workflows/{name}/layout/retry` body `{"expectedRevision": "..."}` → 202 `RetryAck`
- `GET /api/v1/instances/{id}/layout` → `LayoutView`; `POST /api/v1/instances/{id}/layout/retry` → 202 `RetryAck`
- `GET /api/v1/instances/{id}` response gains `layout` (`instanceResponse.Layout *layout.View`); `GET /api/v1/workflows/{name}` includes `presentation`.
- Errors use the real `adapter/http/errors.go:ErrorResponse` shape `{"error": ..., "code": ...}`. Update `request()` to prefer a nonempty string `body.error`, then legacy `body.message`, then statusText. All 5xx text is sanitized to "internal server error" by WriteError; do not assert private server reasons in HTTP error text.

Produces:
```ts
api.workflows.getLayout(name: string): Promise<LayoutView>
api.workflows.retryLayout(name: string, expectedRevision: string): Promise<RetryAck>
api.instances.getLayout(id: string): Promise<LayoutView>
api.instances.retryLayout(id: string, expectedRevision: string): Promise<RetryAck>
interface Instance { /* existing */ layout?: LayoutView }
```

- [x] **Step 1: Write the failing tests.** Append to `dashboard/src/api/client.test.ts`, after the last `describe` (line 163). The file already imports `describe, it, expect, vi, beforeEach, afterEach` and `api`.

```ts

describe('api layout endpoints', () => {
  afterEach(() => vi.restoreAllMocks())

  const okJson = (body: unknown) =>
    vi.fn().mockResolvedValue({ ok: true, json: async () => body } as Response)

  it('GETs /workflows/{name}/layout and returns the wire LayoutView verbatim', async () => {
    // Raw string-keyed JSON (not a typed literal) so a renamed field in
    // graph/arranged/types.ts cannot hide behind the unchecked `as T` cast.
    globalThis.fetch = okJson({
      savedRevision: 'rev-9',
      jobGeneration: 3,
      resultGeneration: 2,
      displayStatus: 'needs_attention',
      attemptsUsed: 5,
      attemptLimit: 5,
      findings: [{ criterion: 'text_clipped', severity: 'blocking', source: 'review', evidence: 'label clipped' }],
      provenance: 'arranged',
      unreviewed: false,
      retryEligible: true,
      available: true,
    })
    const view = await api.workflows.getLayout('pr-review')
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toBe('/api/v1/workflows/pr-review/layout')
    expect(view.savedRevision).toBe('rev-9')
    expect(view.jobGeneration).toBe(3)
    expect(view.displayStatus).toBe('needs_attention')
    expect(view.retryEligible).toBe(true)
    expect(view.findings?.[0].criterion).toBe('text_clipped')
  })

  it('POSTs the expected revision to /workflows/{name}/layout/retry', async () => {
    globalThis.fetch = okJson({ savedRevision: 'rev-9', jobGeneration: 4, state: 'pending', displayStatus: 'pending' })
    const ack = await api.workflows.retryLayout('pr-review', 'rev-9')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/workflows/pr-review/layout/retry',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ expectedRevision: 'rev-9' }) }),
    )
    expect(ack).toEqual({ savedRevision: 'rev-9', jobGeneration: 4, state: 'pending', displayStatus: 'pending' })
  })

  it('GETs /instances/{id}/layout', async () => {
    globalThis.fetch = okJson({ savedRevision: 'rev-1', displayStatus: 'pending' })
    const view = await api.instances.getLayout('inst-1')
    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('/api/v1/instances/inst-1/layout')
    expect(view.displayStatus).toBe('pending')
  })

  it('POSTs the snapshot revision to /instances/{id}/layout/retry', async () => {
    globalThis.fetch = okJson({ savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' })
    await api.instances.retryLayout('inst-1', 'rev-1')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/inst-1/layout/retry',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ expectedRevision: 'rev-1' }) }),
    )
  })

  it('rejects with the server message on a stale-revision conflict (409)', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: async () => ({ error: 'layout: expected revision is not current', code: 'CONFLICT' }),
    } as Response)
    await expect(api.workflows.retryLayout('pr-review', 'rev-old')).rejects.toThrow(
      'API 409: layout: expected revision is not current',
    )
  })

  it('rejects when the layout status store is unavailable (503)', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      statusText: 'Service Unavailable',
      json: async () => ({ error: 'internal server error', code: 'UNAVAILABLE' }),
    } as Response)
    await expect(api.instances.getLayout('inst-1')).rejects.toThrow('API 503: internal server error')
  })

  it('surfaces Instance.layout and WorkflowDefinition.presentation from raw wire JSON', async () => {
    globalThis.fetch = okJson({ id: 'inst-1', layout: { savedRevision: 'rev-1', displayStatus: 'complete' } })
    const inst = await api.instances.get('inst-1')
    expect(inst.layout?.savedRevision).toBe('rev-1')

    globalThis.fetch = okJson({ name: 'wf', nodes: [], edges: [], presentation: { revision: 'rev-1', reason: 'update' } })
    const def = await api.workflows.get('wf')
    expect(def.presentation?.revision).toBe('rev-1')
  })
})
```

- [x] **Step 2: Run them and confirm they fail**

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```
Expected:
```text
TypeError: api.workflows.getLayout is not a function
TypeError: api.workflows.retryLayout is not a function
TypeError: api.instances.getLayout is not a function
TypeError: api.instances.retryLayout is not a function
      Tests  6 failed | 14 passed (20)
```

(The raw-wire test for `Instance.layout`/`presentation` passes at runtime but fails `npx tsc --noEmit` until Step 3, with `Property 'layout' does not exist on type 'Instance'`.)

- [x] **Step 3: Implement.** First fix `request()` in client.ts to consume the real `error`/`code` response as above; add tests for error, legacy message, malformed/empty body and statusText fallback. Then in `dashboard/src/api/types.ts`, insert at line 1:

```ts
import type { LayoutView } from '../graph/arranged/types'
```

and in `interface Instance`, replace lines 100-102:

```ts
  /** The node ID of the interactive node waiting for input, or null. */
  interactiveNodeId: string | null
}
```

with:

```ts
  /** The node ID of the interactive node waiting for input, or null. */
  interactiveNodeId: string | null
  /** Arrangement status + best/fallback geometry for the snapshot's saved revision (omitted when unavailable). */
  layout?: LayoutView
}
```

In `dashboard/src/api/client.ts`, apply this diff (it adds the type import, the two instance methods after `getVariables`, and the two workflow methods before `saveLayout`):

```diff
--- a/dashboard/src/api/client.ts
+++ b/dashboard/src/api/client.ts
@@ -15,6 +15,7 @@
   WorkflowSummary,
 } from './types'
 import type { WorkflowDefinition } from '../graph/types'
+import type { LayoutView, RetryAck } from '../graph/arranged/types'
 
 /** Parameters for listing instances. */
 export interface ListInstancesParams {
@@ -169,6 +170,20 @@
       return request(`/api/v1/instances/${id}/variables`)
     },
 
+    /** Get arrangement status and best/fallback geometry for the instance's snapshot revision. */
+    getLayout(id: string): Promise<LayoutView> {
+      return request(`/api/v1/instances/${id}/layout`)
+    },
+
+    /**
+     * Re-run arrangement for the instance's snapshot revision. `expectedRevision`
+     * must be the snapshot's saved revision; the server answers 202 with an
+     * acknowledgement and never waits for the review to finish.
+     */
+    retryLayout(id: string, expectedRevision: string): Promise<RetryAck> {
+      return post(`/api/v1/instances/${id}/layout/retry`, { expectedRevision })
+    },
+
   },
 
   workflows: {
@@ -194,6 +209,19 @@
       })
     },
 
+    /** Get arrangement status and best/fallback geometry for the workflow's current saved revision. */
+    getLayout(name: string): Promise<LayoutView> {
+      return request(`/api/v1/workflows/${name}/layout`)
+    },
+
+    /**
+     * Re-run arrangement for the workflow's current saved revision. A stale
+     * `expectedRevision` is rejected with 409; the server never waits for review.
+     */
+    retryLayout(name: string, expectedRevision: string): Promise<RetryAck> {
+      return post(`/api/v1/workflows/${name}/layout/retry`, { expectedRevision })
+    },
+
     /** Save node positions for a workflow layout. */
     saveLayout(
       name: string,
```

- [x] **Step 4: Run them and confirm they pass**

```bash
cd dashboard && npx vitest run src/api/client.test.ts && npx tsc --noEmit
```
Expected:
```text
 Test Files  1 passed (1)
      Tests  20 passed (20)
(tsc prints nothing and exits 0)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/api/client.ts dashboard/src/api/client.test.ts dashboard/src/api/types.ts && git commit -m "feat(dashboard): add workflow and instance layout API client methods" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Data source (who fills these values in production):**
  - `GET/POST …/layout[/retry]` are served by `adapter/http/layout_handler.go:LayoutHandler` (P3T9). It calls `layout.QueryService.WorkflowLayout/InstanceLayout/RetryWorkflow/RetryInstance` (P3T8).
  - `QueryService` reads `layout.Reader.View` (P2T5). That reads the `layout.Store` row written by the `layout.Worker` (P2T8) and persisted by `adaptsqlite.LayoutRepo` (P2T6).
  - `Geometry` comes from `layout.Arrange` (P1T6) through `Coordinator.Run` (P1T9).
  - `WorkflowDefinition.presentation` is stamped by `workflow.RevisionStamper.Stamp` (P2T2) on every save path (P2T3/P2T4). The manifest GET returns it. Instance snapshots carry it via engine snapshot propagation (P2T2).
  - `Instance.layout` is set by `InstanceHandler.SetLayouts` (P3T9).
  - `contract.json` is P1T2's, and a Go test there keeps it equal to `DefaultContract()`.
- **Wiring (who calls these in production):**
  - `api.workflows.getLayout/retryLayout` are called from `dashboard/src/routes/WorkflowReview.tsx` through `useLayoutStatus` (P3T11, wired in P3T12).
  - `api.instances.getLayout/retryLayout` are called from `dashboard/src/routes/ExecutionView.tsx` (P3T13).
  - `validateGeometry`/`resolveDisplayGeometry`/`RENDERER_CONTRACT` are used by `ArrangedGraph` (P3T3), by WorkflowReview/ExecutionView for `displayIssues` (P3T12/P3T13), and by the render surface (P3T4).
- **Provenance probes in this task:**
  - `validate.test.ts` "rejects an empty geometry for a non-empty workflow" and "version mismatch" fail if validation ignored its input.
  - The raw-wire client tests fail if a field name drifts from the Go JSON tags.

### Alignment TDD: mirror server validation instead of trusting the version

**Files:** `dashboard/src/graph/arranged/validate.ts`, `validate.test.ts`, `testFixtures.ts`, `types.ts`.

- [x] **RED:** Port the Go §6.6 rejection matrix as table tests: same-version changed font/padding,
  text/type, output/label, diagonal points, attachment on the wrong node/side, missing
  branch text, invalid crossings and non-finite dimensions/options. Confirm all three
  real sample payloads and identical parallel occurrences remain valid. A rejected
  payload yields display issues and fallback, never a complete badge.
- [x] Run `cd dashboard && npx vitest run src/graph/arranged/validate.test.ts && npx tsc --noEmit` and observe the behavioral failure before implementing the correction.
- [x] **GREEN:** Validate full contract and graph identity/geometry invariants, mirroring P1T2.
  Extend shared TS fixture helpers to move route endpoints/attachments/canvas along with
  their node boxes: older tests that only resize/move nodes create invalid geometry and
  must be repaired, not used to weaken validation. Export one fixture transformer here
  for P3T3/P3T12/P3T13; do not copy diverging helpers into each route test.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.

> Verified on revisit: The assigned producers and consumers now exist. `TestLayoutSurfaces_ClientValidationAndAPIReachDOM` drives actual initDomain → HTTP authoring → saved manifest/Engine snapshot → browser measurement/render/review → SQLite → authorised HTTP → embedded WorkflowReview/ExecutionView. Both real API GETs deliver every persisted node box/text and edge route/marker/label to the DOM. HTTP-boundary negative controls corrupt only actual generated JSON (same-version padding, full text, attachment, empty/missing geometry); both routes retain exact saved positions/text, report validation issues and never claim reviewed. Actual result deletion exposes Retry, and both client POST methods reach a new terminal SQLite generation and reviewed DOM for the unchanged revision. Version-only validation fails the specific validator-issue DOM assertion; emptied retry tokens fail both request-body tests. All mutations are restored. This closes P3T1's installed client seam, not P3T7/P3T14's still-blocked complex-sample release gate or Step 6's zero-policy conflict.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

**Shared fixture producer:** In testFixtures.ts also export
`fixtureGeometryWithBoxes(boxes: Record<string, Rect>): Geometry`. Start from fixtureGeometry,
replace the named node boxes, recompute each affected attachment on the declared side,
rebuild orthogonal waypoints including loop gutters, and expand canvas bounds. Tests assert
validateGeometry(fixtureWorkflow(), result, RENDERER_CONTRACT).ok. P3T3/P3T12/P3T13 import
this helper for moved/tall fixture nodes; their old direct-box-only edits must be replaced.
This helper is test support, not another production layout algorithm.

## Pass execution evidence and review

- Selected P3T1 after inspecting the ordered task inventory and skipping previously documented, still-blocked Phase 1/2 production/browser/protocol gates. No other task was implemented. The unrelated pre-existing `docs/visuals/` directory remains untouched/uncommitted.
- Task 1.1 RED: first observed the missing import, then ran compiling no-validation/no-revision-join stubs: 86 behavioral failures and 3 passes. Implemented the payload mirror, complete contract/manifest/geometry checks and revision-scoped resolver; 89 focused tests and TypeScript passed. Source commit: `54dde35`.
- Task 1.2 RED: missing methods produced 16 failures/14 passes; compiling wrong-URL stubs produced 7 behavioral failures/23 passes. Implemented the exact four endpoints, expected-revision bodies, Instance.layout and canonical error/legacy-message/statusText fallback. All 30 API tests and TypeScript passed. Source commit: `61195cf`.
- Alignment RED: 3 fixture-transform failures/135 passes against the compiling unchanged-geometry helper. Additional behavioral RED cases exposed null-lane acceptance, Go-vs-JavaScript negative half rounding, malformed JSON conversion exceptions, and false/zero/empty-string geometry silently treated as absent. Corrected each, reran GREEN and retained the regressions. The shared transformer updates boxes, attachments, orthogonal routes, return gutter, labels and canvas together without mutating caller boxes.
- Actual sample schema parity runs Go BuildGraph → Arrange → Compatible → JSON over the real pr-review, feature-pipeline and ralph-loop files on every focused run. The TS sink validates every emitted geometry; independently read manifest identities and empty-node negative controls prevent fixture substitution from hiding a missing input. Measurements in this parity test are explicitly test-only; this is not browser/review/root acceptance. Added only the Node development types needed to typecheck that cross-language test; no tsconfig, runtime dependency, flag or alternate renderer was added.
- **Self-review, spec first:** checked the shared Go JSON fields and exact task ownership. Corrected the example fixture's invalid right-side return ports to the server's bottom/top policy. Kept saveLayout and the existing manual-positioning surfaces untouched for their assigned tasks. ES2020-compatible replace(/_/g, ' ') implements the required caption normalization without changing tsconfig. The contradictory zero-policy requirement and missing production chain are recorded above, not waived.
- **Self-review, anti-green-theater second:** temporarily reducing full-contract comparison to version-only caused 5 regression failures; dropping the retry token caused the request-body sink to fail; removing the revision guard and fixture attachment updates caused 6 display/geometry sink failures. Restored every mutation. Tests assert exact geometry or null plus issues, not constructor counts. The production graph/status sink cannot yet be driven and remains blocked, rather than replaced by mocks.
- **Self-review, quality third:** inspected neighboring graph/client patterns, formatted the new TS sources, used the named Go coordinate tolerance and half-away-from-zero quantization, and avoided coercing untrusted JSON objects while reporting errors. No stub, mutation, dead symbol or production import of test fixtures remains. Rebuilt the embedded SPA with make ui as required by repository guidance.
- Final GREEN: `npx vitest run src/graph/arranged/validate.test.ts src/api/client.test.ts` (157 validator + 30 API tests), `npx tsc --noEmit`, full `npx vitest run` (51 files, 696 tests), `go test ./domain/layout/ ./adapter/dashboard/`, `go build ./...`, `make ui` and `git diff --check`. The focused Go zero-policy regression also passes, confirming the options blocker is a real contract conflict. Build emitted the existing large-bundle warning; no unrelated dependency upgrades or build-limit changes were made.
- Production completion was not claimed by the initial pass. The installed client-seam gate is closed on revisit below; the separate zero-policy conflict remains unchecked.

## Revisit — installed HTTP, reviewer and route producers (2026-10-17)

- Selection: skipped the earlier tasks' already-recorded sample/profile, rename, seed-retention and retry-lock protocol blockers without re-failing them. P3T1 is the first task with newly unblocked client acceptance. Worked only this task; its Step 6 still requires contradictory positive margin/sweeps and existing server zero-value parity. No policy or unrelated task was changed.
- Added `cmd/arbitron/layout_client_acceptance_test.go`. The root constructs every collaborator and consumes actual saved transform nodes/edges/positions; no passing renderer, fake endpoint or test factory replaces production. The Engine queue gate persists a real snapshot without executing nodes. Positive HTTP responses are unmodified. Deliberate negative controls operate only after the authorised handler has produced its real JSON, including Instance.layout, so a good embedded value cannot mask a bad poll. These are client fault-injection probes, not substitute complex-sample acceptance.
- Strict TDD for existing behavior follows contracts §6.1: wrote the sink assertions, temporarily reduced the full-contract guard to version-only, rebuilt embedded assets and observed behavioral RED at the specific `renderer contract.nodePaddingX` DOM issue. An initial broad issue assertion was insufficient because the independent font guard also rejects the fault; strengthened it before recording RED. Restored the existing correct guard and rebuilt to GREEN. Independently emptied both API retry tokens: both request-body tests failed, then passed after restoration. No production source diff or stale mutated bundle remains.
- Self-review IN ORDER: (1) exact P3T1 slice: full-contract/manifest validation, wire types and four API methods through installed consumers; no options, policy, algorithm or release-gate changes. (2) anti-green-theater: positive checks compare all actual HTTP geometry against independent DOM bounds/paths/markers/labels; negative checks assert exact retained node positions and saved text, explicit issues and absence of a reviewed badge. Result deletion and real UI Retry must produce a strictly newer terminal SQLite generation and matching DOM for the same token. Missing/empty input or absent client wiring cannot satisfy these sinks. (3) quality: read neighboring surface/root helpers, reused real adapters and browser utilities, kept fault selection atomic and isolated browser lifetimes per route, and ran gofmt. The initial one-minute producer wait expired under concurrent host load; the new test now uses the neighboring three-minute integration bound without altering production deadlines. No verification assertion was relaxed.
- Verification: focused validator/API tests (189), TypeScript, all 1,027 dashboard tests across 70 files (`--maxWorkers=2`), `make ui`, `go test ./domain/layout ./adapter/dashboard`, `go vet ./cmd/arbitron`, `go build ./...`, gofmt and diff checks pass. The complete new real-browser test passes under `-race` twice after corrections (51.217s and final post-review 85.580s), no skips/races. One earlier combined build/test command hit its outer deadline, and the initial race attempt exhausted the one-minute producer wait; neither was counted as green. Unrelated untracked `docs/visuals/` remains excluded.

## Revisit — close Step 6 under the reconciled zero policy

- Selected P3T1, the first task with an unchecked step in numeric phase/task order. Contracts §§6.6/6.18 now explicitly resolve the historical zero-policy conflict. The existing production validator already implements that rule; no production behavior or unrelated task was changed.
- Expanded the zero-value matrix through `resolveDisplayGeometry`: only Margin, NodeTextSlack and CrossingSweeps may be zero. Each case starts with independently positive policy values so one zero cannot mask another guard. Parameterized the real Go BuildGraph → Arrange → Compatible → JSON parity test over default and all-zero policy values for all three unchanged bundled samples. Assert the emitted zero values, independent manifest identity and exact geometry returned to the display consumer. Negative/nonfinite policy values, fractional sweeps, empty geometry and missing reviewed geometry must yield null geometry/explicit issues.
- Strict TDD for existing behavior (§6.1): after writing the regressions, temporarily changed the three policy guards to reject zero. Observed 5 behavioral failures (three independently isolated zero policies and both Go parity cases), 7 passes. Restored the existing correct implementation; 211 focused validator/API tests and TypeScript passed.
- Self-review, in order: **(1) spec compliance:** Step 6's corrected policy and complete validation matrix only; no v2/group migration, new option, API, fallback algorithm or release-gate claim. **(2) anti-green-theater:** removing the options rejection produced 9 behavioral failures, including both real Go-output display-consumer cases. Restored the guard. Empty/missing data cannot satisfy the positive exact-geometry assertions. The installed composition-root browser test also re-verifies actual authoring → worker/review → SQLite → authorised HTTP → both route DOMs and Retry, rather than fixture-only wiring. **(3) code quality:** retained neighboring fixture/parity conventions, formatted the test, kept subprocess files temporary/cleaned and preserved the existing coordinate-precision regression. All mutation changes were restored; no production source or embedded-asset diff remains.
- Final GREEN after review: `npx vitest run src/graph/arranged/validate.test.ts src/api/client.test.ts` (211 tests), `npx tsc --noEmit`, full `npx vitest run --maxWorkers=2` (90 files / 1,380 tests), `make ui`, `go test ./domain/layout ./adapter/dashboard`, `go build ./...`, and `git diff --check`. `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run '^TestLayoutSurfaces_ClientValidationAndAPIReachDOM$' -count=1 -timeout=5m` passed in 31.944s without skips. Existing npm audit findings (9 vulnerabilities) and Vite's large-chunk warning were observed; no unrelated dependency/build-policy changes were made.
- Step 6 is now checked: Go→TS zero-policy parity and the installed client seam are verified. This closes P3T1, not the separate complex-sample release gates or remaining tasks.
