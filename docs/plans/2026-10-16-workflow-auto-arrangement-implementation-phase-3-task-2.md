# Workflow Auto-Arrangement — Phase 3 Task 2: ArrangedNode + RoutedEdge (+ Inter font)

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

**Goal:** Build the node and edge components that draw a reviewed geometry payload exactly: payload box sizes, wrapped full labels, persisted orthogonal routes with crossing gaps and arrowheads, and label boxes. Fonts are bundled locally.

**Architecture:**
- `ArrangedNode` (xyflow node type `arrangedNode`) sizes its root to `nodeBox.box` with `box-sizing: border-box` and the constant contract border. It renders the payload `text`/`typeCaption` in `[data-node-text]` spans that wrap without truncation, and shows runtime state only through border colour and box-shadow.
- `RoutedEdge` (edge type `routedEdge`) ignores xyflow's handle coordinates. It draws `route.points` through `buildRoutedPath` as ONE `<path data-edge-path data-edge-kind>` with its own `<marker id="<graphInstanceId>-arranged-arrow-<ordinal>">` and gap subpaths. It draws the label box through `EdgeLabelRenderer`.
- `fonts.ts` bundles Inter, Noto Sans SC and Noto Emoji so every surface (dashboard, P3T4 render surface, P3T5 headless Chrome) draws the same glyphs.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters, relative imports only), React 19, @xyflow/react 12, Vitest 4 + jsdom + Testing Library, CSS modules, dagre (fallback only).

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · Design: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` (§4 shared renderer, §5 navigation/status/refresh, §6 remove manual positioning) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS + §5 Addenda 5–7

**Depends on:** P3T1 (`graph/arranged/types.ts`, `validate.ts` `RENDERER_CONTRACT`, `testFixtures.ts`).

**Interfaces (consumed, from P3T1 / contracts §3):**
```ts
export interface Point { x: number; y: number }
export interface Rect { x: number; y: number; w: number; h: number }
export type EdgeKind = 'forward' | 'loop_return' | 'self_loop'
export interface NodeBox { id: string; box: Rect; text: string; typeCaption: string; textLines: number; rank: number }
export interface LabelBox { text: string; box: Rect; lines: number }
export interface EdgeRoute { ordinal: number; source: string; target: string; output?: string; label?: string; kind: EdgeKind; side?: Side; lane?: number; points: Point[]; sourceAttach: Attachment; targetAttach: Attachment; text?: LabelBox }
export interface RendererContract { /* … */ nodePaddingX: number; nodePaddingY: number; nodeBorderPx: number; nodeTypeBarPx: number; nodeTextGapPx: number; nodeTextMaxWidth: number; lineHeight: number; edgeLabelPaddingX: number; edgeLabelPaddingY: number; strokePx: number; arrowPx: number; crossingGapPx: number; forwardColor: string; loopReturnColor: string; loopReturnDash: string /* … */ }
export const RENDERER_CONTRACT: RendererContract                 // validate.ts
export function fixtureGeometry(): Geometry                        // testFixtures.ts (b: 200x70 at (40,140); edge 1 b→c label "yes"; edge 2 loop_return c→b label "again")
export type NodeExecutionState = 'pending' | 'running' | 'completed' | 'failed' | 'waiting' | 'skipped' // graph/types.ts
```

**Interfaces (produced; contracts §3 + Addenda 5–7; ★ = not in contracts):**
```ts
// graph/nodes/typeColors.ts ★ (extracted from ArbitronNode.tsx; shared)
export const typeColors: Record<string, string>
export const DEFAULT_TYPE_COLOR = '#888888'
export function typeAccent(nodeType: string): string
// graph/arranged/routedPath.ts
export function buildRoutedPath(points: Point[], gaps: Point[], gapPx: number): string
// graph/arranged/ArrangedNode.tsx
export interface ArrangedNodeData { nodeBox: NodeBox; nodeType: string; executionState: NodeExecutionState; contract: RendererContract; [key: string]: unknown }
export const nodeTextClassNames: { label: string; typeCaption: string }
export default memo(ArrangedNode)   // xyflow node type key 'arrangedNode'
// graph/arranged/RoutedEdge.tsx
export interface RoutedEdgeData { route: EdgeRoute; gaps: Point[]; contract: RendererContract; active: boolean; graphInstanceId: string; [key: string]: unknown }
export const edgeLabelClassName: string
export default memo(RoutedEdge)     // xyflow edge type key 'routedEdge'
// graph/arranged/fonts.ts — side-effect imports of @fontsource/inter 400/500/600/700
```

**DOM contract implemented here (consumed by P3T4's render report):**
- **Node root:** `[data-arranged-node="<id>"]`, inline `width`/`height` = `box.w`/`box.h`, inline `border-width` = `nodeBorderPx`, CSS `box-sizing: border-box`, `data-state`.
- **Node text:** `[data-node-text="type"]` holds the type caption; `[data-node-text="label"]` holds the full payload text.
- **Edge path:** `<path data-edge-path="<ordinal>" data-edge-kind="<kind>" d="…" marker-end="url(#<graphInstanceId>-arranged-arrow-<ordinal>)">`. Stroke, stroke-width and stroke-dasharray are set as **inline style**, so read them with `getComputedStyle`. Crossing gaps are extra `M` subpaths in the same `d`.
- **Arrow marker:** the `<marker id="<graphInstanceId>-arranged-arrow-<ordinal>">` sits in this edge's own `<defs>`, with `markerUnits="userSpaceOnUse"` and size `arrowPx`.
- **Active overlay:** a separate `<path data-edge-overlay="<ordinal>" class=activeOverlay>` with no marker.
- **Edge label:** `<div data-edge-label="<ordinal>">`, absolutely positioned via `transform: translate(box.x px, box.y px)`, with width/height = label box.
- **Class names:** `nodeTextClassNames.label`, `nodeTextClassNames.typeCaption` and `edgeLabelClassName` carry every font and wrapping property themselves (family, size, weight, line-height, white-space, overflow-wrap). The P3T4 measure surface can therefore measure with those classes alone.

---

### Task 2.1: Extract the node-type accent map (refactor)

**Files:**
- Create: `dashboard/src/graph/nodes/typeColors.ts`
- Modify: `dashboard/src/graph/nodes/ArbitronNode.tsx:5-17` (map moves out) and `:47` (lookup)
- Test (existing safety net): `dashboard/src/graph/nodes/ArbitronNode.test.tsx`

- [x] **Step 1: Confirm the safety net is green before the refactor**

```bash
cd dashboard && npx vitest run src/graph/nodes/ArbitronNode.test.tsx
```
Expected:
```text
      Tests  3 passed (3)
```

- [x] **Step 2: Create `dashboard/src/graph/nodes/typeColors.ts`**

```ts
/** Accent colours keyed by workflow node type (shared by ArbitronNode and ArrangedNode). */
export const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
}

/** Accent colour used when a node type has no entry in typeColors. */
export const DEFAULT_TYPE_COLOR = '#888888'

export function typeAccent(nodeType: string): string {
  return typeColors[nodeType] ?? DEFAULT_TYPE_COLOR
}
```

- [x] **Step 3: Use it from ArbitronNode.** Apply to `dashboard/src/graph/nodes/ArbitronNode.tsx`:

```diff
--- a/dashboard/src/graph/nodes/ArbitronNode.tsx
+++ b/dashboard/src/graph/nodes/ArbitronNode.tsx
@@ -2,19 +2,8 @@
 import { Handle, Position } from '@xyflow/react'
 import type { NodeProps, Node } from '@xyflow/react'
 import type { NodeExecutionState } from '../types'
+import { typeAccent } from './typeColors'
 import styles from './ArbitronNode.module.css'
-
-/** Accent colors keyed by workflow node type. */
-const typeColors: Record<string, string> = {
-  llm_call: '#7c8cf5',
-  script: '#e67e22',
-  tool_call: '#1abc9c',
-  conditional: '#f39c12',
-  transform: '#9b59b6',
-  timer_delay: '#00bcd4',
-  webhook_wait: '#e91e63',
-  spawn_workflow: '#2ecc71',
-}
 
 const stateNodeClass: Record<NodeExecutionState, string> = {
   completed: styles.stateCompleted,
@@ -44,7 +33,7 @@
 type ArbitronNodeType = Node<ArbitronNodeData, 'arbitronNode'>
 
 function ArbitronNode({ data, selected }: NodeProps<ArbitronNodeType>) {
-  const accentColor = typeColors[data.nodeType] ?? '#888888'
+  const accentColor = typeAccent(data.nodeType)
   const execState = data.executionState ?? 'pending'
   const nodeClasses = [
     styles.node,
```

- [x] **Step 4: Run the safety net again** (it covers the spawn_workflow accent and the `#888888` fallback)

```bash
cd dashboard && npx vitest run src/graph/nodes/ArbitronNode.test.tsx && npx tsc --noEmit
```
Expected:
```text
      Tests  3 passed (3)
(tsc exits 0)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/graph/nodes/typeColors.ts dashboard/src/graph/nodes/ArbitronNode.tsx && git commit -m "refactor(dashboard): extract node type accent colours" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2.2: `buildRoutedPath` — persisted orthogonal route with crossing gaps

**Files:**
- Create: `dashboard/src/graph/arranged/routedPath.ts`
- Test: `dashboard/src/graph/arranged/routedPath.test.ts`

- [x] **Step 1: Write the failing test**

```ts
import { describe, it, expect } from 'vitest'
import { buildRoutedPath } from './routedPath'

describe('buildRoutedPath', () => {
  it('draws an orthogonal polyline through every point when there are no gaps', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 20 }, { x: 10, y: 80 }, { x: 50, y: 80 }], [], 10),
    ).toBe('M 10,20 L 10,80 L 50,80')
  })

  it('splits a vertical segment around one gap with a new M subpath', () => {
    expect(buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [{ x: 10, y: 50 }], 10)).toBe(
      'M 10,0 L 10,45 M 10,55 L 10,100',
    )
  })

  it('splits a horizontal segment around a gap', () => {
    expect(buildRoutedPath([{ x: 0, y: 0 }, { x: 100, y: 0 }], [{ x: 40, y: 0 }], 10)).toBe(
      'M 0,0 L 35,0 M 45,0 L 100,0',
    )
  })

  it('opens two gaps on one segment in order along the segment regardless of input order', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 0 }, { x: 10, y: 100 }], [{ x: 10, y: 70 }, { x: 10, y: 30 }], 10),
    ).toBe('M 10,0 L 10,25 M 10,35 L 10,65 M 10,75 L 10,100')
  })

  it('clamps a gap near the end of a segment to the segment end', () => {
    expect(
      buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 50, y: 100 }], [{ x: 0, y: 97 }], 10),
    ).toBe('M 0,0 L 0,92 M 0,100 L 50,100')
  })

  it('consumes a gap at a shared corner only once', () => {
    expect(
      buildRoutedPath([{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }], [{ x: 0, y: 100 }], 10),
    ).toBe('M 0,0 L 0,95 M 0,100 L 100,100')
  })

  it('ignores a gap point that is not on any segment', () => {
    expect(
      buildRoutedPath([{ x: 10, y: 20 }, { x: 10, y: 80 }, { x: 50, y: 80 }], [{ x: 30, y: 50 }], 10),
    ).toBe('M 10,20 L 10,80 L 50,80')
  })

  it('rounds coordinates to two decimals', () => {
    expect(buildRoutedPath([{ x: 0.123, y: 0.456 }, { x: 0.123, y: 10.006 }], [], 10)).toBe(
      'M 0.12,0.46 L 0.12,10.01',
    )
  })

  it('returns an empty string for a route with fewer than two points', () => {
    expect(buildRoutedPath([{ x: 1, y: 1 }], [], 10)).toBe('')
    expect(buildRoutedPath([], [], 10)).toBe('')
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/routedPath.test.ts
```
Expected:
```text
Error: Failed to resolve import "./routedPath" from "src/graph/arranged/routedPath.test.ts". Does the file exist?
```

- [x] **Step 3: Implement**

```ts
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

/**
 * Build the SVG path `d` for a persisted orthogonal route. Each gap point that
 * lies on a segment opens a gap of `gapPx` centred on it (clamped to the
 * segment): the current subpath ends before the gap and a new `M` subpath
 * starts after it, all in the SAME path string. A gap point is consumed by the
 * first segment it lies on; gap points on no segment are ignored. Coordinates
 * are rounded to 2 decimals. Returns '' for fewer than two points.
 */
export function buildRoutedPath(points: Point[], gaps: Point[], gapPx: number): string {
  if (points.length < 2) return ''
  const half = Math.max(0, gapPx) / 2
  const remaining = [...gaps]
  const cmds: string[] = [`M ${xy(points[0])}`]

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

    const at = (s: number): Point => ({
      x: a.x + ((b.x - a.x) * s) / len,
      y: a.y + ((b.y - a.y) * s) / len,
    })
    let cursor = 0
    for (const s of onThis) {
      const start = Math.max(cursor, s - half)
      const end = Math.min(len, s + half)
      if (end <= cursor) continue
      if (start > cursor) cmds.push(`L ${xy(at(start))}`)
      cmds.push(`M ${xy(at(end))}`)
      cursor = end
    }
    if (cursor < len) cmds.push(`L ${xy(b)}`)
  }
  return cmds.join(' ')
}
```

- [x] **Step 4: Run it and confirm it passes**

```bash
cd dashboard && npx vitest run src/graph/arranged/routedPath.test.ts
```
Expected:
```text
      Tests  9 passed (9)
```

- [x] **Step 5: Commit**

```bash
git add dashboard/src/graph/arranged/routedPath.ts dashboard/src/graph/arranged/routedPath.test.ts && git commit -m "feat(dashboard): build routed edge paths with crossing gaps" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2.3: Bundle contract fonts and deterministic Unicode coverage

**Files:**
- Modify: `dashboard/package.json:18-26` (`dependencies` gains `@fontsource/inter`), `dashboard/package-lock.json` (npm writes it)
- Create: `dashboard/src/graph/arranged/fonts.ts`

- [x] **Step 1: Install the font package** (contract `fontPackage: "@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2"`)

```bash
cd dashboard && npm install @fontsource/inter@^5.3.0 @fontsource/noto-sans-sc@5.3.1 @fontsource/noto-emoji@5.3.2 && npm install --save-dev fontkit@2.0.4
```
Expected:
```text
added 1 package …
    "@fontsource/inter": "^5.3.0",
```

`dependencies` in `dashboard/package.json` now begins with `"@fontsource/inter": "^5.3.0",` (alphabetically before `@xyflow/react`).

- [x] **Step 2: Create `dashboard/src/graph/arranged/fonts.ts`.** It is imported by `ArrangedGraph.tsx` (P3T3) and by `src/render/main.tsx` (P3T4).

```ts
// The renderer contract's font (contract.json names the complete Inter/Noto package set below).
// Bundled locally — never fetched from a CDN — so the dashboard, the render
// surface and the Go browser renderer all draw identical glyphs.
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/noto-sans-sc/400.css'
import '@fontsource/noto-sans-sc/500.css'
import '@fontsource/noto-sans-sc/600.css'
import '@fontsource/noto-sans-sc/700.css'
import '@fontsource/noto-emoji/400.css'
import '@fontsource/noto-emoji/500.css'
import '@fontsource/noto-emoji/600.css'
import '@fontsource/noto-emoji/700.css'
```

- [x] **Step 3: Verify it type-checks and bundles**

```bash
cd dashboard && npx tsc --noEmit && npx vite build
```
Expected:
```text
(tsc exits 0)
✓ built in …  (dist/assets contains inter-latin-*.woff2 files once fonts.ts is imported by ArrangedGraph in P3T3)
```

- [x] **Step 4: Commit**

```bash
git add dashboard/package.json dashboard/package-lock.json dashboard/src/graph/arranged/fonts.ts && git commit -m "build(dashboard): bundle the Inter renderer-contract font" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2.4: `ArrangedNode`

**Files:**
- Create: `dashboard/src/graph/arranged/ArrangedNode.tsx`
- Create: `dashboard/src/graph/arranged/ArrangedNode.module.css`
- Test: `dashboard/src/graph/arranged/ArrangedNode.test.tsx`

Design rules this component enforces (Addendum 7):
- The box comes **only** from the payload.
- Border width is constant in every state. State shows through border colour and box-shadow only, never opacity, so contrast is unaffected.
- The type caption is `#b4b4cc` (the accent colour appears only on the type bar). The label is `#e0e0e0` and wraps (`white-space: normal; overflow-wrap: anywhere; width: max-content; max-width: var(--node-text-max)`) with no ellipsis.
- The state dot is absolutely positioned inside the right padding, below the type bar, so it never overlaps the text spans.
- Hidden top/bottom Handles exist only because xyflow will not mount edges without them.

- [x] **Step 1: Write the failing test**

```tsx
import { render } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { NodeExecutionState } from '../types'
import { fixtureGeometry } from './testFixtures'
import { RENDERER_CONTRACT } from './validate'

// Handle needs the React Flow store; stub it to a marker so the handles a node
// exposes can be asserted.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import ArrangedNode, { nodeTextClassNames } from './ArrangedNode'

type Props = Parameters<typeof ArrangedNode>[0]

function props(overrides: { state?: NodeExecutionState; selected?: boolean; nodeType?: string } = {}): Props {
  const nodeBox = fixtureGeometry().nodes[1] // b: 200x70, long label, typeCaption "llm call"
  return {
    id: nodeBox.id,
    data: {
      nodeBox,
      nodeType: overrides.nodeType ?? 'llm_call',
      executionState: overrides.state ?? 'pending',
      contract: RENDERER_CONTRACT,
    },
    selected: overrides.selected ?? false,
  } as unknown as Props
}

function root(container: HTMLElement): HTMLElement {
  return container.querySelector('[data-arranged-node]') as HTMLElement
}

describe('ArrangedNode — DOM contract', () => {
  it('marks the root with data-arranged-node and sizes it to the payload box', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const el = root(container)
    expect(el.getAttribute('data-arranged-node')).toBe('b')
    expect(el.style.width).toBe('200px')
    expect(el.style.height).toBe('70px')
    expect(el.className).toContain('node')
  })

  it('renders the payload text (not the manifest label) in the label element, untruncated', () => {
    const p = props()
    p.data.nodeBox = { ...p.data.nodeBox, text: 'Payload text with a deliberately long run of words to wrap' }
    const { container } = render(<ArrangedNode {...p} />)
    const label = container.querySelector('[data-node-text="label"]') as HTMLElement
    expect(label.textContent).toBe('Payload text with a deliberately long run of words to wrap')
    expect(label.className).toBe(nodeTextClassNames.label)
    expect(label.style.marginTop).toBe('2px')
  })

  it('renders the payload type caption in the type element', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const type = container.querySelector('[data-node-text="type"]') as HTMLElement
    expect(type.textContent).toBe('llm call')
    expect(type.className).toBe(nodeTextClassNames.typeCaption)
  })

  it('applies the contract padding, type-bar height and text max width', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const content = container.querySelector('[class*="content"]') as HTMLElement
    expect(content.style.padding).toBe('8px 12px')
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    expect(bar.style.height).toBe('4px')
    expect(root(container).style.getPropertyValue('--node-text-max')).toBe('240px')
  })

  it('colours only the type bar with the node-type accent', () => {
    const { container } = render(<ArrangedNode {...props({ nodeType: 'spawn_workflow' })} />)
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(46, 204, 113)')
    const type = container.querySelector('[data-node-text="type"]') as HTMLElement
    expect(type.style.color).toBe('')
  })

  it('exposes exactly the hidden top target and bottom source handles', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const handles = [...container.querySelectorAll('[data-testid="handle"]')]
    expect(handles.map((h) => [h.getAttribute('data-handle-type'), h.getAttribute('data-position')])).toEqual([
      ['target', 'top'],
      ['source', 'bottom'],
    ])
  })
})

describe('ArrangedNode — runtime states never resize or fade the node', () => {
  const states: NodeExecutionState[] = ['pending', 'running', 'completed', 'failed', 'waiting', 'skipped']

  it.each(states)('state %s keeps the contract border width, box size and no opacity', (state) => {
    const { container } = render(<ArrangedNode {...props({ state })} />)
    const el = root(container)
    expect(el.style.borderWidth).toBe('2px')
    expect(el.style.width).toBe('200px')
    expect(el.style.height).toBe('70px')
    expect(el.style.opacity).toBe('')
    expect(el.getAttribute('data-state')).toBe(state)
    expect(el.className).toContain(`state${state[0].toUpperCase()}${state.slice(1)}`)
  })

  it('adds the selected class without changing the box', () => {
    const { container } = render(<ArrangedNode {...props({ selected: true })} />)
    const el = root(container)
    expect(el.className).toContain('selected')
    expect(el.style.width).toBe('200px')
  })

  it('positions the state dot absolutely below the type bar (outside the text flow)', () => {
    const { container } = render(<ArrangedNode {...props()} />)
    const dot = container.querySelector('[class*="stateDot"]') as HTMLElement
    expect(dot.getAttribute('aria-hidden')).toBe('true')
    expect(dot.style.top).toBe('8px')
    // The dot is a sibling of the content block, never inside the label.
    expect(container.querySelector('[data-node-text="label"] [class*="stateDot"]')).toBeNull()
  })

  it('defaults a missing execution state to pending', () => {
    const p = props()
    ;(p.data as { executionState?: NodeExecutionState }).executionState = undefined
    const { container } = render(<ArrangedNode {...p} />)
    expect(root(container).getAttribute('data-state')).toBe('pending')
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/ArrangedNode.test.tsx
```
Expected:
```text
FAIL  src/graph/arranged/ArrangedNode.test.tsx
Error: Failed to resolve import "./ArrangedNode" from "src/graph/arranged/ArrangedNode.test.tsx". Does the file exist?
```

- [x] **Step 3: Implement the stylesheet** `dashboard/src/graph/arranged/ArrangedNode.module.css`:

```css
/* Arranged (reviewed-geometry) node. Size comes ONLY from the payload box
   (inline width/height); nothing here may change the box size or move text in
   any runtime state. State is shown by border colour and box-shadow only:
   border width is constant and opacity is never used (it breaks contrast). */
.node {
  position: relative;
  box-sizing: border-box;
  border-style: solid;
  border-color: var(--border-color);
  border-radius: var(--radius-md);
  background: var(--bg-surface);
  overflow: visible;
  cursor: pointer;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}

/* Hidden anchor handles: xyflow needs them to mount edges; routed edges ignore
   their coordinates. */
.node :global(.react-flow__handle) {
  opacity: 0;
  pointer-events: none;
  border: none;
  min-width: 0;
  min-height: 0;
  width: 1px;
  height: 1px;
}

.typeBar {
  width: 100%;
  border-top-left-radius: calc(var(--radius-md) - 2px);
  border-top-right-radius: calc(var(--radius-md) - 2px);
}

.content {
  box-sizing: border-box;
}

.typeCaption {
  display: block;
  font-family: 'Inter', 'Noto Sans SC', 'Noto Emoji';
  font-size: 10px;
  font-weight: 700;
  line-height: 1.35;
  letter-spacing: 1px;
  text-transform: uppercase;
  color: #b4b4cc;
  white-space: nowrap;
}

.label {
  display: block;
  width: max-content;
  max-width: var(--node-text-max, 240px);
  font-family: 'Inter', 'Noto Sans SC', 'Noto Emoji';
  font-size: 13px;
  font-weight: 600;
  line-height: 1.35;
  color: #e0e0e0;
  white-space: normal;
  overflow-wrap: anywhere;
}

/* Absolutely positioned inside the right padding so it never affects wrapping. */
.stateDot {
  position: absolute;
  right: 4px;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-dim);
  pointer-events: none;
}

.statePending {
  border-color: var(--border-color);
}

.stateRunning {
  border-color: var(--state-running);
  box-shadow: 0 0 10px rgba(52, 152, 219, 0.45);
  animation: arrangedPulse 2s ease-in-out infinite;
}

.stateRunning .stateDot {
  background: var(--state-running);
}

.stateCompleted {
  border-color: var(--state-completed);
}

.stateCompleted .stateDot {
  background: var(--state-completed);
}

.stateFailed {
  border-color: var(--state-failed);
}

.stateFailed .stateDot {
  background: var(--state-failed);
}

.stateWaiting {
  border-color: var(--state-waiting);
}

.stateWaiting .stateDot {
  background: var(--state-waiting);
}

.stateSkipped {
  border-color: #3a3a5a;
}

.selected {
  border-color: var(--accent) !important;
  box-shadow: 0 0 0 2px var(--accent);
}

@keyframes arrangedPulse {
  0%, 100% { box-shadow: 0 0 6px rgba(52, 152, 219, 0.35); }
  50% { box-shadow: 0 0 14px rgba(52, 152, 219, 0.6); }
}
```

- [x] **Step 4: Implement the component** `dashboard/src/graph/arranged/ArrangedNode.tsx`. Never use the inline `border` shorthand: it resets `border-color` and would erase the state colour.

```tsx
import { memo } from 'react'
import type { CSSProperties } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { Node, NodeProps } from '@xyflow/react'
import type { NodeExecutionState } from '../types'
import type { NodeBox, RendererContract } from './types'
import { typeAccent } from '../nodes/typeColors'
import styles from './ArrangedNode.module.css'

export interface ArrangedNodeData {
  nodeBox: NodeBox
  nodeType: string
  executionState: NodeExecutionState
  contract: RendererContract
  [key: string]: unknown
}

/** Class names the measure surface (P3T4) applies to measure text exactly as nodes render it. */
export const nodeTextClassNames = { label: styles.label, typeCaption: styles.typeCaption }

const stateClass: Record<NodeExecutionState, string> = {
  pending: styles.statePending,
  running: styles.stateRunning,
  completed: styles.stateCompleted,
  failed: styles.stateFailed,
  waiting: styles.stateWaiting,
  skipped: styles.stateSkipped,
}

type ArrangedNodeType = Node<ArrangedNodeData, 'arrangedNode'>

function ArrangedNode({ data, selected }: NodeProps<ArrangedNodeType>) {
  const { nodeBox, contract } = data
  const state: NodeExecutionState = data.executionState ?? 'pending'
  const className = [styles.node, stateClass[state], selected ? styles.selected : '']
    .filter(Boolean)
    .join(' ')

  // Border width is set here (constant in every state); colour comes from the
  // state class. Never use the `border` shorthand: it would reset the colour.
  const rootStyle = {
    width: nodeBox.box.w,
    height: nodeBox.box.h,
    borderWidth: contract.nodeBorderPx,
    '--node-text-max': `${contract.nodeTextMaxWidth}px`,
  } as CSSProperties

  return (
    <div className={className} style={rootStyle} data-arranged-node={nodeBox.id} data-state={state}>
      <Handle type="target" position={Position.Top} isConnectable={false} />
      <div
        className={styles.typeBar}
        style={{ height: contract.nodeTypeBarPx, background: typeAccent(data.nodeType) }}
      />
      <div
        className={styles.content}
        style={{ padding: `${contract.nodePaddingY}px ${contract.nodePaddingX}px` }}
      >
        <span
          data-node-text="type"
          className={styles.typeCaption}
          style={{ lineHeight: contract.lineHeight }}
        >
          {nodeBox.typeCaption}
        </span>
        <span
          data-node-text="label"
          className={styles.label}
          style={{ lineHeight: contract.lineHeight, marginTop: contract.nodeTextGapPx }}
        >
          {nodeBox.text}
        </span>
      </div>
      <span
        className={styles.stateDot}
        style={{ top: contract.nodeTypeBarPx + 4 }}
        aria-hidden="true"
      />
      <Handle type="source" position={Position.Bottom} isConnectable={false} />
    </div>
  )
}

export default memo(ArrangedNode)
```

- [x] **Step 5: Run it and confirm it passes**

```bash
cd dashboard && npx vitest run src/graph/arranged/ArrangedNode.test.tsx && npx tsc --noEmit
```
Expected:
```text
      Tests  15 passed (15)
(tsc exits 0)
```

- [x] **Step 6: Commit**

```bash
git add dashboard/src/graph/arranged/ArrangedNode.tsx dashboard/src/graph/arranged/ArrangedNode.module.css dashboard/src/graph/arranged/ArrangedNode.test.tsx && git commit -m "feat(dashboard): add ArrangedNode drawn from reviewed geometry" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2.5: `RoutedEdge`

**Files:**
- Create: `dashboard/src/graph/arranged/RoutedEdge.tsx`
- Create: `dashboard/src/graph/arranged/RoutedEdge.module.css`
- Test: `dashboard/src/graph/arranged/RoutedEdge.test.tsx`

Design rules this component enforces:
- **Stroke styling:** forward edges are solid `contract.forwardColor`. `loop_return` **and** `self_loop` edges are dashed `contract.loopReturnDash` in `contract.loopReturnColor`. Stroke is set as inline style, never as attributes, because xyflow's stylesheet would override presentation attributes.
- **Arrowhead:** every edge defines its own `marker#<graphInstanceId>-arranged-arrow-<ordinal>` (Addendum 5) in `userSpaceOnUse` units, so `arrowPx` is real pixels and does not scale with stroke width.
- **Active animation:** running sources get a separate overlay path with the same `d`. The edge's own arrow, dash and gaps never change.

- [x] **Step 1: Write the failing test** (provenance probe: xyflow's `sourceX/targetX` are set to 999/888 and must not appear in `d`)

```tsx
import { render } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { describe, it, expect, vi } from 'vitest'
import type { EdgeRoute, Point } from './types'
import { fixtureGeometry } from './testFixtures'
import { RENDERER_CONTRACT } from './validate'
import { buildRoutedPath } from './routedPath'

// EdgeLabelRenderer portals into React Flow's label layer; in isolation portal
// into document.body so labels render as HTML outside the <svg>.
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    EdgeLabelRenderer: ({ children }: { children: React.ReactNode }) => createPortal(children, document.body),
  }
})

import RoutedEdge, { edgeLabelClassName } from './RoutedEdge'

type Props = Parameters<typeof RoutedEdge>[0]

function edgeProps(route: EdgeRoute, opts: { gaps?: Point[]; active?: boolean } = {}): Props {
  return {
    id: `routed-${route.ordinal}`,
    source: route.source,
    target: route.target,
    // Deliberately far from the route: the component must ignore xyflow's handle coordinates.
    sourceX: 999,
    sourceY: 999,
    targetX: 888,
    targetY: 888,
    sourcePosition: 'bottom',
    targetPosition: 'top',
    data: { route, gaps: opts.gaps ?? [], contract: RENDERER_CONTRACT, active: opts.active ?? false, graphInstanceId: 'test-graph' },
  } as unknown as Props
}

function renderEdge(p: Props) {
  return render(
    <svg>
      <RoutedEdge {...p} />
    </svg>,
  )
}

const geo = fixtureGeometry()
const forward = geo.edges[1] // b -> c, label "yes"
const loopReturn = geo.edges[2] // c -> b, label "again"

describe('RoutedEdge — draws the persisted route', () => {
  it('builds d from route.points and ignores xyflow sourceX/targetX', () => {
    const { container } = renderEdge(edgeProps(forward))
    const path = container.querySelector(`[data-edge-path="${forward.ordinal}"]`) as SVGPathElement
    expect(path.getAttribute('d')).toBe('M 140,210 L 140,235 L 130,235 L 130,260')
    expect(path.getAttribute('d')).not.toContain('999')
    expect(path.getAttribute('d')).not.toContain('888')
  })

  it('tags the path with its ordinal and kind', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('data-edge-path')).toBe('2')
    expect(path.getAttribute('data-edge-kind')).toBe('loop_return')
  })

  it('renders crossing gaps as extra M subpaths inside the SAME path', () => {
    const gap = { x: 140, y: 222 }
    const { container } = renderEdge(edgeProps(forward, { gaps: [gap] }))
    const paths = container.querySelectorAll(`[data-edge-path="${forward.ordinal}"]`)
    expect(paths).toHaveLength(1)
    const d = paths[0].getAttribute('d')!
    expect(d).toBe(buildRoutedPath(forward.points, [gap], RENDERER_CONTRACT.crossingGapPx))
    expect(d).toBe('M 140,210 L 140,217 M 140,227 L 140,235 L 130,235 L 130,260')
  })
})

describe('RoutedEdge — stroke, dash and arrowhead', () => {
  it('draws forward edges solid in the contract forward colour', () => {
    const { container } = renderEdge(edgeProps(forward))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.style.stroke).toBe('rgb(138, 138, 176)') // #8a8ab0
    expect(path.style.strokeWidth).toBe('2')
    expect(path.style.strokeDasharray).toBe('')
  })

  it('draws loop returns dashed in the contract loop colour', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.style.stroke).toBe('rgb(124, 140, 245)') // #7c8cf5
    expect(path.style.strokeDasharray).toBe('6 4')
  })

  it('draws self loops dashed like loop returns', () => {
    const self: EdgeRoute = { ...loopReturn, ordinal: 5, kind: 'self_loop', source: 'b', target: 'b' }
    const { container } = renderEdge(edgeProps(self))
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('data-edge-kind')).toBe('self_loop')
    expect(path.style.strokeDasharray).toBe('6 4')
  })

  it('defines its own arrow marker <graphInstanceId>-arranged-arrow-<ordinal> and references it from marker-end', () => {
    const { container } = renderEdge(edgeProps(forward))
    const marker = container.querySelector('defs > marker#test-graph-arranged-arrow-1') as SVGMarkerElement
    expect(marker).not.toBeNull()
    expect(marker.getAttribute('markerWidth')).toBe('10')
    expect(marker.getAttribute('markerHeight')).toBe('10')
    expect(marker.getAttribute('orient')).toBe('auto')
    expect(marker.getAttribute('markerUnits')).toBe('userSpaceOnUse')
    expect(marker.querySelector('path')!.getAttribute('fill')).toBe('#8a8ab0')
    const path = container.querySelector('[data-edge-path]') as SVGPathElement
    expect(path.getAttribute('marker-end')).toBe('url(#test-graph-arranged-arrow-1)')
  })

  it('colours a loop-return arrowhead with the loop colour', () => {
    const { container } = renderEdge(edgeProps(loopReturn))
    expect(container.querySelector('marker#test-graph-arranged-arrow-2 path')!.getAttribute('fill')).toBe('#7c8cf5')
  })
})

describe('RoutedEdge — active source animation', () => {
  it('adds a separate overlay path with the same d and no arrowhead when active', () => {
    const gap = { x: 140, y: 222 }
    const { container } = renderEdge(edgeProps(forward, { gaps: [gap], active: true }))
    const main = container.querySelector('[data-edge-path]') as SVGPathElement
    const overlay = container.querySelector('[data-edge-overlay]') as SVGPathElement
    expect(overlay).not.toBeNull()
    expect(overlay.getAttribute('class')).toContain('activeOverlay')
    expect(overlay.getAttribute('d')).toBe(main.getAttribute('d'))
    expect(overlay.getAttribute('marker-end')).toBeNull()
    // The edge itself keeps its arrow, solid stroke and gaps.
    expect(main.getAttribute('marker-end')).toBe('url(#test-graph-arranged-arrow-1)')
    expect(main.style.strokeDasharray).toBe('')
  })

  it('renders no overlay when the source is not running', () => {
    const { container } = renderEdge(edgeProps(forward))
    expect(container.querySelector('[data-edge-overlay]')).toBeNull()
  })
})

describe('RoutedEdge — label box', () => {
  it('places the label at its reviewed box with contract padding', () => {
    renderEdge(edgeProps(forward))
    const label = document.body.querySelector('[data-edge-label="1"]') as HTMLElement
    expect(label.textContent).toBe('yes')
    expect(label.className).toBe(edgeLabelClassName)
    expect(label.style.transform).toBe('translate(146px, 222px)')
    expect(label.style.width).toBe('30px')
    expect(label.style.height).toBe('21px')
    expect(label.style.padding).toBe('3px 6px')
    expect(label.style.color).toBe('rgb(224, 224, 224)') // #e0e0e0
  })

  it('uses the loop label colour for loop-return labels', () => {
    renderEdge(edgeProps(loopReturn))
    const label = document.body.querySelector('[data-edge-label="2"]') as HTMLElement
    expect(label.textContent).toBe('again')
    expect(label.style.color).toBe('rgb(154, 166, 255)') // #9aa6ff
  })

  it('renders no label element when the route has no text', () => {
    renderEdge(edgeProps(geo.edges[0]))
    expect(document.body.querySelector('[data-edge-label]')).toBeNull()
  })
})

describe('RoutedEdge — defensive', () => {
  it('renders nothing when edge data is missing', () => {
    const p = { ...edgeProps(forward), data: undefined } as unknown as Props
    const { container } = renderEdge(p)
    expect(container.querySelector('path')).toBeNull()
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

```bash
cd dashboard && npx vitest run src/graph/arranged/RoutedEdge.test.tsx
```
Expected:
```text
Error: Failed to resolve import "./RoutedEdge" from "src/graph/arranged/RoutedEdge.test.tsx". Does the file exist?
```

- [x] **Step 3: Implement the stylesheet** `dashboard/src/graph/arranged/RoutedEdge.module.css`:

```css
/* Edge label box at its reviewed position (flow coordinates). Inline styles
   carry the box geometry, padding and colour; this class carries typography so
   the measure surface (P3T4) can measure labels exactly as they render. */
.edgeLabel {
  position: absolute;
  left: 0;
  top: 0;
  box-sizing: border-box;
  font-family: 'Inter', 'Noto Sans SC', 'Noto Emoji';
  font-size: 11px;
  font-weight: 500;
  line-height: 1.35;
  white-space: normal;
  overflow-wrap: anywhere;
  background: #1a1a2e;
  color: #e0e0e0;
  border-radius: 3px;
  pointer-events: none;
}

/* Running-source animation drawn as a SEPARATE path on top of the edge, so the
   edge's own stroke, crossing gaps and arrowhead never change. */
.activeOverlay {
  fill: none;
  stroke: #ffffff;
  stroke-opacity: 0.55;
  stroke-dasharray: 4 10;
  stroke-linecap: round;
  pointer-events: none;
  animation: arrangedEdgeFlow 0.9s linear infinite;
}

@keyframes arrangedEdgeFlow {
  from { stroke-dashoffset: 14; }
  to { stroke-dashoffset: 0; }
}
```

- [x] **Step 4: Implement the component** `dashboard/src/graph/arranged/RoutedEdge.tsx`:

```tsx
import { memo } from 'react'
import { EdgeLabelRenderer } from '@xyflow/react'
import type { Edge, EdgeProps } from '@xyflow/react'
import type { EdgeRoute, Point, RendererContract } from './types'
import { buildRoutedPath } from './routedPath'
import styles from './RoutedEdge.module.css'

export interface RoutedEdgeData {
  route: EdgeRoute
  gaps: Point[]
  contract: RendererContract
  active: boolean
  graphInstanceId: string
  [key: string]: unknown
}

/** Class name the measure surface (P3T4) applies to measure edge labels exactly as they render. */
export const edgeLabelClassName = styles.edgeLabel

/** Text colours for edge labels (contrast-checked against the #1a1a2e label background). */
const FORWARD_LABEL_COLOR = '#e0e0e0'
const LOOP_LABEL_COLOR = '#9aa6ff'

type RoutedEdgeType = Edge<RoutedEdgeData, 'routedEdge'>

/**
 * Draws a persisted route exactly. xyflow's sourceX/sourceY/targetX/targetY are
 * deliberately ignored: the reviewed geometry is the only source of the path.
 */
function RoutedEdge({ data }: EdgeProps<RoutedEdgeType>) {
  if (!data) return null
  const { route, gaps, contract, active, graphInstanceId } = data
  if (!graphInstanceId) throw new Error('graphInstanceId is required')
  const isLoop = route.kind !== 'forward'
  const color = isLoop ? contract.loopReturnColor : contract.forwardColor
  const d = buildRoutedPath(route.points, gaps, contract.crossingGapPx)
  const markerId = `${graphInstanceId}-arranged-arrow-${route.ordinal}`
  const a = contract.arrowPx

  return (
    <>
      <defs>
        <marker
          id={markerId}
          markerWidth={a}
          markerHeight={a}
          viewBox={`0 0 ${a} ${a}`}
          refX={a}
          refY={a / 2}
          orient="auto"
          markerUnits="userSpaceOnUse"
        >
          <path d={`M 0,0 L ${a},${a / 2} L 0,${a} Z`} fill={color} />
        </marker>
      </defs>
      <path
        data-edge-path={route.ordinal}
        data-edge-kind={route.kind}
        d={d}
        markerEnd={`url(#${markerId})`}
        style={{
          fill: 'none',
          stroke: color,
          strokeWidth: contract.strokePx,
          strokeDasharray: isLoop ? contract.loopReturnDash : undefined,
        }}
      />
      {active && (
        <path
          className={styles.activeOverlay}
          data-edge-overlay={route.ordinal}
          d={d}
          style={{ strokeWidth: Math.max(1, contract.strokePx / 2) }}
        />
      )}
      {route.text && (
        <EdgeLabelRenderer>
          <div
            data-edge-label={route.ordinal}
            className={styles.edgeLabel}
            style={{
              transform: `translate(${route.text.box.x}px, ${route.text.box.y}px)`,
              width: route.text.box.w,
              height: route.text.box.h,
              padding: `${contract.edgeLabelPaddingY}px ${contract.edgeLabelPaddingX}px`,
              lineHeight: contract.lineHeight,
              color: isLoop ? LOOP_LABEL_COLOR : FORWARD_LABEL_COLOR,
            }}
          >
            {route.text.text}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export default memo(RoutedEdge)
```

- [x] **Step 5: Run it, the whole graph suite, and the type check**

```bash
cd dashboard && npx vitest run src/graph && npx tsc --noEmit
```
Expected:
```text
      Tests  … passed  (RoutedEdge.test.tsx: 14 passed; no failures in src/graph)
(tsc exits 0)
```

- [x] **Step 6: Commit**

```bash
git add dashboard/src/graph/arranged/RoutedEdge.tsx dashboard/src/graph/arranged/RoutedEdge.module.css dashboard/src/graph/arranged/RoutedEdge.test.tsx && git commit -m "feat(dashboard): add RoutedEdge drawing persisted routes, gaps and arrows" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Wiring:**
  - `ArrangedNode`/`RoutedEdge` are registered as `arrangedNodeTypes`/`arrangedEdgeTypes` by `buildArrangedFlow.ts` (P3T3) and rendered by `ArrangedGraph` (P3T3).
  - In production, `ArrangedGraph` is mounted by `WorkflowReview.tsx` (P3T12), `ExecutionView.tsx` `GraphView` (P3T13) and the render surface `src/render/RenderSurface.tsx` (P3T4).
  - The Go renderer `renderreview.BrowserRenderer` (P3T5) loads that surface, so reviews measure exactly these components.
  - `nodeTextClassNames`/`edgeLabelClassName` are consumed by `src/render/MeasureSurface.tsx` (P3T4).
  - `fonts.ts` is imported by `ArrangedGraph.tsx` (P3T3) and `src/render/main.tsx` (P3T4).
  - The real-React-Flow DOM test in P3T3 (`ArrangedGraph.dom.test.tsx`) proves these components mount through the composition and emit the DOM contract.
- **Data source:**
  - `ArrangedNodeData.nodeBox/contract` and `RoutedEdgeData.route/gaps/contract` come from `Geometry` (`view.geometry`), served by `GET /api/v1/{workflows|instances}/…/layout` (P3T9). That endpoint is fed by `layout.QueryService` (P3T8) ← `layout.Reader` (P2T5) ← the worker-stored result (P2T8) ← `layout.Arrange` (P1T6).
  - `nodeType` comes from the manifest (`WorkflowDefinition.nodes[].type`).
  - `executionState`/`active` come from `ExecutionView.tsx:buildExecutionStates` (existing; passed through in P3T13).
- **Provenance probes:**
  - `RoutedEdge.test.tsx` "builds d from route.points and ignores xyflow sourceX/targetX".
  - `ArrangedNode.test.tsx` "renders the payload text (not the manifest label)" and "sizes it to the payload box".

### Alignment TDD: visibility and marker behavior have browser evidence

**Files:** `ArrangedNode.tsx`, `RoutedEdge.tsx` and their tests under dashboard/src/graph/arranged; DOM producers consumed by P3T4.

- [x] **RED:** Mutate actual computed ancestor opacity/visibility, route middle path and marker
  orientation in a real browser fixture; assert P3T4 reports invisible/wrong actual
  geometry and P3T6 blocks. Confirm running/selected styles keep readable text, route
  and label positions in both interactive views; frozen render-page animation alone
  is not proof of all animation frames.

> Completed on revisit: P3T4/P3T6/P3T12/P3T13 now exist. Actual embedded-browser ancestor opacity/visibility, same-endpoint/wrong-middle paths and reversed/hidden markers reach blocking reviewer findings. New root acceptance authors Unicode labels over the real router, waits for the production worker/reviewer/SQLite result, runs a real script to produce running state and checks both interactive routes at animation phases 0/25/50/75/100%. Removing ExecutionView's execution-state input fails the running-node/overlay DOM assertion; restoring it passes. No report, geometry, identity or runtime-state fake is installed.

- [x] Run `cd dashboard && npx vitest run src/graph/arranged/ArrangedNode.test.tsx src/graph/arranged/RoutedEdge.test.tsx && npx tsc --noEmit` and observe the behavioral failure before implementing the correction.
- [x] **GREEN:** Preserve exact persisted components and attributes; make actual d, marker shape,
  labels and computed styles available to the §6.5 collector. Active animation must be
  a non-occluding overlay preserving base path/arrow contrast; test extremal animation
  states rather than disabling the defect in the review-only surface.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.

> Resolved owning-package gate (2026-10-18): on the now-low-load host, TypeScript and the entire owning graph package pass (30 files / 540 tests), as do the actual-route component/font tests under -race (40.638s). Removing the reload button and, independently, replacing the real display-issue callback input with an empty list fails BOTH actual routes; all mutations and embedded assets are restored. The earlier load>350 timeouts are historical, not new failures. The broader dashboard run reached completion (1379/1380 passed) but exposed an unrelated GraphView centering assertion (zero setCenter calls); it is not claimed green and was not altered.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

### Graph identity and typography acceptance

- [x] Before implementing marker namespacing, mount two real ArrangedGraph instances
  with ordinal 0, different loop/forward colours, then remove the first. Assert unique
  marker IDs and that each remaining path resolves to its own graph's visible marker.
  A missing/empty graphInstanceId is rejected, never a global `arranged-arrow-0` fallback.

> BLOCKED (revisit): P3T3 now supplies real useId namespaces; its two-ArrangedGraph DOM test passes, including different forward/loop colours and first-graph removal. Removing the namespace fails that exact sink assertion. An exploratory real-Chrome two-graph extension also failed under the global-ID mutation, but the restored-browser run hit Chrome's teardown watchdog/60s process timeout during severe host contention. The unverified extension was removed rather than committed as passing acceptance. Existing leaf/component and real-Flow DOM tests remain; the complete visible two-graph Chrome gate remains unproven.
> BLOCKED (2026-10-18 follow-up): the low-load host now passes the unchanged Chrome leaf test. A two-real-ArrangedGraph extension of its --dump-dom/virtual-time harness mounted both complete node sets but produced no edge paths, even with restored production namespaces and visible fixed-position canvases. It therefore cannot reach the marker sink; this failure is NOT evidence against marker correctness or a successful mutation probe. Logs: /tmp/p3t2-markers-red.log and /tmp/p3t2-markers-green.log (despite the filename, the restored run failed). The unverified extension was removed, existing source/harness restored, and the unchanged browser test rerun. The real-Flow DOM gate remains green; this browser gate needs a compositor-driven/CDP harness, not a weakened path count.

> Resolved (2026-10-19): `dashboard/scripts/test-arranged-markers-browser.mjs` uses real-time CDP/compositor frames with two complete production ArrangedGraphs, actual bundled fonts and measured React Flow handles. All six IDs are unique; each persisted SVG path resolves its own marker. Ordinal 0 is forward in one graph and loop-return in the other. Actual PNG off-shaft arrow-interior pixels have the corresponding distinct colours before removal; the surviving graph retains its exact namespace, paths and visible arrow pixels afterward. Existing missing/empty-namespace unit tests remain green. Global-ID and hidden-marker mutations fail independently; both are restored. The former virtual-time harness blocker is closed, not bypassed.

- [x] The 200×70 hand-authored box is a structural/component fixture, not proof that its
  long label fits. P3T4/P3T5 real-measure fixtures use actual metrics to size candidate
  boxes. Never reclassify a clipping fixture as readable by accepting its promised lines.
- [x] Add a browser CSS measurement parity test for max-width, borders, caption letter
  spacing/case, wrapping, padding and line height. Font CSS and measurement must agree;
  use the exact contract values, and fail same-version drift before rendering.

### Task 2.6: Bundled-font coverage and ready-font producer

**Files:** Create dashboard/scripts/build-font-coverage.mjs and
dashboard/src/graph/arranged/fontCoverage.json; extend fonts.ts/font tests and package.json
build script; commit the generated coverage, package-lock and redistributed font licenses.
fontkit@2.0.4 is a build-only dev dependency; no Python service/runtime dependency is added.

**Exact contract:** FontFamily = `Inter, Noto Sans SC, Noto Emoji`; FontPackage =
`@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2`.
ContractVersion stays arranged-renderer/1 because it has not shipped yet. P1T2 updates the
Go default/contract.json; all components and measurement surfaces use the same CSS stack.

**Produces in fonts.ts:**
```ts
export const RENDERER_FONT_FAMILIES: readonly string[]
export function ensureBundledTextFonts(doc: Document, texts: readonly string[], contract: RendererContract): Promise<{ fontsReady: boolean; fontFamilyResolved: boolean }>
```

- [x] RED: Use Latin+accent/CJK+emoji fixtures, a scalar absent from the bundled cmap,
  an unsupported variation/ZWJ sequence, missing WOFF2 subset and changed asset bytes.
  Measured/rendered supported labels use identical bundled faces and widths in two
  browsers with different system fonts; absent coverage fails with explicit reason.
  A loaded Inter face alone must not establish CJK/emoji font readiness.

> Historical blocker (revisit; resolved below): P3T3–P3T7 now exist and saved Unicode labels reach actual production measurement, review, SQLite and both embedded routes. Coverage/shaping/hash/subset/font-readiness tests and font-generator tests pass. This environment still provides only one Chrome installation, not two browsers with independently controlled system-font inventories; that portability assertion remains unproven. Do not re-fail the same unavailable two-browser gate.

> Resolved (2026-10-19 portability revisit): Docker now provides two isolated Chromium processes with independently verified system-font inventories (76 faces versus zero). Both consume current source/bundled assets through the real MeasureSurface → measured payload → RenderSurface/ArrangedGraph components, with the production render.html styles. Actual fractional metrics, full rendered reports/glyph bounds and Blink's actual supplying custom-font identities/counts match exactly; compositor PNGs contain every text line. Fontconfig retains identical rendering rules and changes only available faces. Unsupported scalar/variation/ZWJ and missing Inter/CJK/emoji subsets or changed bytes fail explicitly in BOTH measure and render stages of BOTH browsers. This is two independently controlled browser environments, not a claim of cross-engine parity. Run `docker build -t arbitron:layout-render .` then `cd dashboard && npm run test:font-portability`; the gate never skips missing Docker/image/browser.
- [x] GREEN: build-font-coverage reads the exact CSS-imported WOFF2 files via fontkit,
  records a top-level fingerprint (SHA256 of sorted package/face metadata), resolved package versions, family/weight/unicode-range coverage and SHA256
  for every shipped face. Validate generated coverage against bytes before each build.
  Runtime rejects any text scalar unsupported by bundled faces before measuring/rendering;
  validate supported variation/ZWJ sequences against bundled shaping output, not scalar
  presence alone. Explicitly allow whitespace/shaping controls only in valid supported
  sequences. Unsupported text fails visibly/retryably; never silently system-fallback.
- [x] ensureBundledTextFonts selects required face subsets/weights using actual text,
  explicitly loads every used family/range, waits for FontFaceSet ready and checks exact
  loaded faces and coverage. P3T4 passes all label/type/branch text to it for both modes.
  CSS font-family values are separate family names, never one quoted combined name.

> Completed on revisit: Actual saved Café/CJK/combining/emoji node and branch text reaches P3T4 measurement/rendering and P3T3 in both real routes. Removing only the used Inter Latin 600 WOFF2 subset makes actual measurement and rendering fail; removing that subset from the user-view HTTP asset response produces retained fallback, the explicit font error and no reviewed badge. Restoring the exact asset and reloading draws every saved box/text/path/marker/label from the same reviewed revision. Generator tests verify the shipped bytes, not a readiness flag.
- [x] Run font generator/tests, TypeScript/Vitest and make ui. Remove one used subset
  temporarily and require failed measure/render plus retained fallback, then restore.
  P3T5 and P3T7 verify fresh browser capture of the Unicode acceptance labels.

> Current plan correction (§6.18): client font failure uses ArrangedGraph's existing `Reload reviewed fonts` action, which reloads client assets and preserves a valid passed server generation. Server font/measure/render failure still uses arrangement Retry. The real-route gate below must prove failure→reload→restored geometry with no arrangement POST; do not add a button that the passed-row retry policy will reject.

## Execution evidence

- Selection: skipped the earlier recorded production/protocol gates, which still depend on later renderer/query/view work or unresolved rename/seed contracts. This pass implements only P3T2's unblocked slice; P3T3 is the next independent task.
- Task 2.1: existing three-test accent safety net passed before and after extracting the shared map. Tasks 2.2/2.4/2.5: observed missing-import errors, then behavioral failures against compiling empty-path/null-component stubs (8/15/11 failures respectively), followed by GREEN implementations. Extra payload-change, missing namespace, two same-ordinal component/removal, empty caption, Unicode/newline and reversed/overlapping gap cases pass. No stub remains.
- Task 2.6: observed behavioral RED with an empty coverage builder and unconditional font-readiness stub. Generated 476 CSS-selected WOFF2 face records from actual cmap, cmap variation records and GSUB/fontkit shaping, including every shipped WOFF fallback hash, exact package versions, CSS hashes and a deterministic fingerprint. Pinned Noto packages/fontkit, committed redistributed font licenses, and made production build verify (not silently regenerate) coverage. Runtime selects actual-text family/range/contract-weight loads, hashes used assets and awaits exact loaded subsets plus FontFaceSet.ready. Unsupported scalars, malformed controls/ZWJ/variation sequences, load errors, missing subsets and changed bytes reject explicitly.
- Fresh Chrome leaf fixture (`npm run test:arranged-browser`) mounts the real components through React Flow, loads bundled Latin/accent/combining/CJK/emoji/variation/ZWJ text, compares standalone measurement classes with rendered node typography and verifies box/path stability at animation extrema with running+selected styling. It first failed on Chrome-canonical unicode ranges and collapsed/nowrap text. Numeric range normalization, pre-wrap text and wrapped max-width captions corrected these defects. Empty captions reserve no block/gap. The fixture's hand-sized boxes are structural, not reviewed/readable candidate evidence.
- Self-review, in order: (1) matched this task's exact DOM/types/route/box/style interfaces and named consumers; no graph semantics, layout options, production fake or substitute root was added. Explicit newlines require shared pre-wrap classes rather than the earlier normal/nowrap snippets. (2) Payload-width, empty-route, global-marker-ID, unconditional-font-ready and nowrap-caption mutations all failed their sink assertions; restored each. Missing installed Inter WOFF2 also made the actual `npm run build` fail before compilation; restored the bytes. Unit font fakes honor family, weight, range and text; no fake is installed in production. (3) checked neighboring styles, preserved constant borders/colour-only runtime state and class exports, excluded unimported monolithic font files from Vite's URL map, hashed WOFF fallbacks too, and kept test-only browser construction separate from production.
- Final local gates: full dashboard Vitest **55 files / 756 tests passed**; arranged focused suite **217 passed**; TypeScript passes; generator tests **3 passed**; real Chrome component test **1 passed**, including with `ARBITRON_REQUIRE_BROWSER=1`; `make ui`, `go test ./adapter/dashboard` and `git diff --check` pass. Node/Vite emit their existing localstorage/chunk-size warnings; npm reports 9 audit findings (dependency remediation is outside this task).
- The mission's permitted single per-task commit consolidates the local subtask commit steps. **Production completion is not claimed:** the nine unchecked gates above require assigned P3T3–P3T7/P3T11–P3T13 producers/consumers and portable two-browser evidence. In particular, the current embedded SPA has no assigned shared-renderer import yet and does not ship the new fonts through a fabricated input.

## Production revisit evidence (2026-10-17)

- Selected this task after skipping the earlier still-blocked profile regression, rename/seed/lock-set protocols and P3T1 zero-option parity contradiction. Only P3T2's newly available production component/font gates were worked; no sibling task was started.
- Strict characterization RED: removed the real ExecutionView execution-state feed, rebuilt embedded assets and ran the new runtime test. It failed at the missing running-node/active-overlay sink (`/tmp/p3t2-runtime-red.log`). Restored the feed and rebuilt; the test passes. Global marker-ID mutations fail P3T3's real-Flow two-graph assertion (`/tmp/p3t2-marker-red.log`); the exploratory Chrome assertion also failed before its restored-run environment blocker. All mutations are restored; no production source changes are needed for those existing components.
- Added `cmd/arbitron/layout_component_surfaces_test.go`: actual initDomain/authoring/worker/reviewer/SQLite/authorised-router/embedded-SPA provenance, real script-produced runtime state, click-produced selection, complete persisted geometry at animation extrema/intermediate phases, visible unclipped glyph ranges and non-occluding same-route overlays. Missing one used font subset asserts explicit failure/retained real label/no reviewed badge; restoring its actual bytes restores every reviewed DOM sink without changing the saved revision/generation.
- Strengthened actual embedded-browser reviewer defects to require BLOCKING findings, including ancestor visibility. Missing-font coverage now removes one load-bearing subset rather than all fonts; both actual Measure and Render fail explicitly.
- Ordered self-review: **(1) Spec:** kept only this component/font acceptance slice; removed the speculative Retry feature and unverified two-graph Chrome extension. No server retry policy, extra flag, layout algorithm or test threshold was changed. **(2) Anti-green-theater:** absent execution feed/global namespace fail actual sinks; font removal affects actual saved text/DOM, not a fake readiness flag. The script executes through the real engine; renderer/reviewer/HTTP responses and graph components are real. The combined Retry and portable-browser gates remain unchecked, not inferred from local greens. **(3) Quality:** followed neighbouring root/surface tests and helpers, used an atomic asset-outage switch, bounded the real script, released/joined execution before root shutdown, isolated supporting files in the temp workflow and ran gofmt/diff checks. Unrelated pre-existing `docs/visuals/` is untouched.
- After that review, final focused GREEN: `GOMAXPROCS=2 ARBITRON_REQUIRE_BROWSER=1 go test -p 1 ./cmd/arbitron -run '^TestLayoutSurfaces_(ComponentRuntimeStylesPreserveReviewedGeometry|MissingBundledSubsetRetainsFallback)$' -count=1 -timeout=2m` (**47.442s**); the focused renderer/reviewer missing-font/DOM-defect command (**55.825s**); ArrangedNode/RoutedEdge/ArrangedGraph real-Flow Vitest (**40 passed**, 16.72s) and TypeScript. Font-generator tests (**3 passed**) and restored `make ui` also pass. The bounded Go build parallelism changes no test semantics.
- Wider graph/dashboard and additional -race runs hit the environment failures recorded under the unchecked package gate. They are not reported as green. Browser teardown timeout and the one-browser portability constraint remain separately recorded; no knowingly failing acceptance test is committed.

Selection sources: [Fontsource Noto Sans SC](https://fontsource.org/fonts/noto-sans-sc/use),
[Fontsource Noto Emoji](https://fontsource.org/fonts/noto-emoji/use), and
[fontkit package](https://www.npmjs.com/package/fontkit). Package versions were checked
against the primary npm registry during this review (noto-sans-sc 5.3.1, noto-emoji 5.3.2,
fontkit 2.0.4). These sources supply self-hosted assets; actual cmap/hash/sequence evidence
is generated from the installed pinned files, not inferred from a family name.

## Client font recovery acceptance (current contract)

Client-only asset failure has an existing producer: ArrangedGraph renders
`Reload reviewed fonts` on failure and invokes window.location.reload. P3T11's panel
suppresses a reviewed badge while onDisplayIssues is nonempty; valid server passed rows
continue rejecting arrangement Retry. Server font/render failure still has the ordinary
eligible arrangement Retry path.

- [x] Browser-required actual-route RED→GREEN/mutation: save a Unicode revision through
  initDomain; block one required client font asset only after real server review passed.
  Both views retain a readable unreviewed fallback, report the font error, hide the
  reviewed badge and expose Reload reviewed fonts. Restore the asset and click it;
  after real page/font reload every node/route/digest matches the same persisted
  revision/job/result generation. Assert zero arrangement retry POSTs and unchanged
  stored verdict. Removing the button or font-ready callback fails the DOM sink.
- [x] Missing server font subset still fails actual measurement/capture and produces
  eligible retry with saved content retained. Keep these two recovery paths separate
  in the existing P3T14 route acceptance suite, with real client and server data.

## Client/server font recovery revisit (2026-10-18)

- This pass stays in P3T2. Earlier unproven two-graph Chrome and independently controlled two-browser font portability gates remain unchecked. The unavailable second font inventory was not retried; a bounded low-load two-graph follow-up exposed the distinct harness limitation recorded under its step and was stopped without shipping an unverified test.
- Extended `cmd/arbitron/layout_component_surfaces_test.go` to use BOTH embedded WorkflowReview and ExecutionView routes over a genuinely saved Unicode revision and Engine.Trigger snapshot. The actual worker/browser/reviewer passes before client asset removal. Each fresh browser loses one required Inter subset, retains the real label with an explicit font error/no reviewed badge, then clicks the real reload action. Complete DOM geometry, exact revision/job/result generation and candidate digest return; stored row/result/evidence remain byte-for-value identical and HTTP observes ZERO arrangement retry POSTs.
- Added `cmd/arbitron/layout_font_recovery_test.go`, selected by the existing `TestLayoutSurfaces` make test-render prefix. Only production job assets lose the subset, either during measurement or at capture after real measurement. Both routes report failed/unreviewed status with eligible arrangement Retry and retained saved labels, not client font reload. Workflow retry (measure case) and snapshot retry (capture case) each advance exactly one generation, reach passed through the actual worker/reviewer, and restore every reviewed DOM sink without rewriting manifest bytes. Existing Reader behavior truthfully retains an unreviewed candidate after capture failure; the test does not wrongly require missing geometry in that case.
- RED: removing the reload button failed both routes at the real click sink (`/tmp/p3t2-font-recovery-red.log`); independently emptying onDisplayIssues failed both error/status sinks (`/tmp/p3t2-font-callback-red.log`). The server test with its asset interceptor disabled failed both cases because actual production review passed rather than failed (`/tmp/p3t2-server-font-red.log`); enabling only the one-subset outage made them green. No report, measurements, geometry, successful verdict or route is fabricated.
- Ordered self-review: (1) **Spec:** characterized the existing §6.18 feature rather than adding a redundant option/retry implementation; kept client reload and server Retry separate and left portable/multi-graph acceptance unclaimed. (2) **Anti-green-theater:** authoring, snapshot, worker, SQLite, authorized HTTP and embedded DOM are real; reload/callback/outage negative controls fail consumer assertions; recovered geometry and unchanged persisted evidence are asserted, not merely request counts. (3) **Quality:** followed adjacent surface-test helpers; used atomics at actual asset boundaries, forwarded all renderer arguments unchanged, isolated browser caches per route and restored global factories via cleanup. Added an explicit nonnil geometry assertion before digest inspection.
- After review: restored `make ui`; browser-required focused client/server/runtime tests under **-race PASS (40.638s)**; missing-measure/render-subset production adapter tests **PASS (4.569s)**; `go test ./adapter/dashboard` and `git diff --check` pass. Font coverage --check and all **3 generator tests** pass; TypeScript passes; full owning graph Vitest **30 files / 540 tests pass**. Full dashboard **89 files passed / 1 failed; 1379 tests passed / 1 failed**, solely the pre-existing GraphView tall-node centering assertion (`/tmp/p3t2-final-dashboard.log`); no unrelated test/time limit was changed. Final post-restoration reruns: focused node/edge/real-Flow DOM Vitest **43 passed**, TypeScript passes, browser-required leaf CSS/wrap/runtime **1 passed (1.7s)**, root client/server/runtime **PASS (38.980s)**. The leaf test is not the unproven two-graph gate. An initial generator run collided with make ui's npm ci; rerunning sequentially after the build passed, with no dependency/source change.

## Multi-graph compositor acceptance revisit (2026-10-19)

- Worked only P3T2's newly unblocked marker gate. The independently controlled two-browser/system-font inventory gate remains BLOCKED as recorded under Task 2.6; it was not retried or claimed by opening two tabs in the same browser.
- Added `dashboard/tests/arranged-markers-fixture.tsx` and its real-time CDP driver, included in the existing `npm run test:arranged-browser` command. Both complete ArrangedGraphs consume nonempty geometry/types via production buildArrangedFlow/RoutedEdge and actual bundled-font readiness, without supplied handle measurements, React Flow doubles or report fakes. The existing real initDomain/authoring/worker/SQLite/HTTP/two-route regressions also run under -race; no production wiring or feature addition was needed.
- RED before restoration: global marker IDs failed the new browser sink (3 unique IDs instead of 6; `/tmp/p3t2-cdp-markers-red.log`). Independently making marker paint transparent retained valid ID/colour declarations but failed at actual PNG ink (0 of 6 interior pixels; `/tmp/p3t2-cdp-markers-ink-red.log`). Restoring production namespacing/paint passes. During test development, corrected pixel sampling to use whole triangle-interior pixels rather than treating PNG pixel corners as centres; no visibility tolerance or required sample count was relaxed.
- The wider root regression exposed an existing baseline race: StatePassed is persisted before cache publication, so the client font test sometimes compared CacheGeneration 0 against the later 1. Observed RED in `/tmp/p3t2-cdp-root-race.log`. The test now waits for the real worker's matching published generation before taking its pre-outage baseline. The complete-record/result/evidence equality assertions are unchanged; restored -race run passes (`/tmp/p3t2-cdp-root-race-green.log`).
- Ordered self-review: **(1) Spec:** limited this pass to the two-real-graph visible-marker acceptance and its own regression synchronization; no renderer feature, option, alternate font policy or sibling task. Only the completed marker box is checked. **(2) Anti-green-theater:** exact persisted SVG paths and own-marker resolution are checked alongside actual compositor PNGs before/after first-graph unmount. Both missing namespaces and invisible ink fail independently; tests cannot pass on empty geometry or missing wiring. The saved-input root probes retain every production argument and immutable-result assertion. **(3) Quality:** followed the neighbouring CDP fixture pattern, used isolated profiles and real handle initialization, named the geometric pixel samples, added bounded CDP calls/Chrome teardown, and kept both graph positions stable across unmount. All production mutations are restored; make ui produces no embedded-asset diff.
- Final GREEN after review: TypeScript; complete owning graph Vitest **30 files / 540 tests**; font generator **3 tests**; browser-required existing typography/runtime plus new multi-graph checks **2 tests, no skips**; `make ui`; `go test ./adapter/dashboard`; browser-required actual-route component/font regressions **-race PASS (19.125s)**; `git diff --check`. No full-module/dashboard/release gate is newly claimed.

## Independently controlled font inventories revisit (2026-10-19)

- Selected only P3T2's final unchecked portability step. Its former environment dependency is now available through the existing Chromium runtime image; no sibling task or product behavior is changed. Added `dashboard/scripts/test-font-portability-browser.mjs`, its adjacent real-surface fixture and the discoverable `test:font-portability` npm command. No production correction was needed.
- Behavioral RED: removing Noto from the actual production label CSS fails on Blink's actual system-font glyph usage (CJK HK/Symbols/Color Emoji), not a family declaration or readiness flag (`/tmp/p3t2-portability-fallback-red.log`). Independently removing the production asset-hash guard makes the changed-byte acceptance fail (`/tmp/p3t2-portability-hash-red.log`). Both mutations are restored. The inventory-zero assertion and complete node/text/route/marker/metric/ink sinks forbid empty inputs or absent drawing from passing.
- Each isolated browser measures real Latin/accent/combining/CJK/emoji/variation/ZWJ labels, sizes its structural fixture from those actual metrics, then mounts the production renderer. The compared reports contain actual Range/SVG/style evidence; supplying font identities come from CDP, and text ink from actual compositor PNGs. Missing used Inter/CJK/emoji subsets and changed bytes are intercepted only at the real asset fetch, never replaced by fabricated metrics/reports. Invalid text and asset failures are checked independently at measurement and capture, with no successful result published. The existing saved-input root tests below continue proving real composition, producer, storage and both route consumers; this structural fixture is not substituted for readability acceptance.
- Ordered self-review: **(1) Spec:** limited to the required two-inventory font proof, preserving existing renderer/contracts/dependencies; no feature flag, alternate renderer or relaxed tolerance. **(2) Anti-green-theater:** actual supplying faces are custom in both environments, missing fallback families/hash guards fail consumer assertions, complete persisted-route DOM and visible PNG text are required, and the true inventory difference is asserted. **(3) Quality:** reused the production HTML/styles and measure/render APIs, selected used subsets from committed coverage, used the production raster antialias tolerance and private bounded CDP pipe commands, and closed targets/containers/server/temp files. Rendering rules stay constant while system faces are filtered; changing font hinting rules is not a portability control.
- Final GREEN after review: font coverage `--check`; **3 generator tests**; TypeScript; entire dashboard **90 files / 1380 tests**; `make ui` (no embedded-asset diff); browser-required typography/runtime and multi-graph **2 tests, no skips**; new two-browser gate **1 test, no skips (21.204s)** with verified **76 vs 0** system faces; actual saved-input component/client/server font-recovery root regressions **-race PASS (39.123s)**; `go test ./adapter/dashboard`; `git diff --check`. Logs: `/tmp/p3t2-portability-final.log`, `/tmp/p3t2-portability-vitest.log`, `/tmp/p3t2-portability-ui.log`, `/tmp/p3t2-portability-root-race.log`. Existing Vite chunk-size/Node localstorage warnings remain; no full-module/release gate is newly claimed. All P3T2 boxes are now checked; P3T7's release race gate and P3T15 remain separate work.
