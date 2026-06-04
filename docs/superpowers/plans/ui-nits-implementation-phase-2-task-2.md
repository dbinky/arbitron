# UI Nits Phase 2 — Task 2: Follow Active Node + Recenter Control

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This file continues `ui-nits-implementation-phase-2-task-1.md` — complete that file first.

**Goal:** While an instance runs, smoothly animate the viewport to keep the executing step centered as execution advances; pause following on manual pan/zoom; and provide a Recenter / Follow control that re-engages following and re-centers on the current anchor.

**Architecture:** A new hook `useFollowActiveNode` owns `following` state (default `true`), animates to the anchor's node-center via `useReactFlow().setCenter` on anchor change while following, and pauses following when React Flow's `onMoveStart` fires with a **non-null** event (a real user gesture; programmatic `setCenter` passes a `null` event). A `prefers-reduced-motion` check makes the animation a jump (`duration: 0`). `GraphView` wires the hook, renders a Recenter / Follow button, and forwards `onMoveStart` to the hook. This composes with task-1's one-shot mount centering and preserves Phase 1's `edgeTypes`/`loopContainer` nodes.

**Tech Stack:** React 19, @xyflow/react v12 (useReactFlow/setCenter/onMoveStart), TypeScript, Vitest 4 (`npm test` → `vitest run`) + @testing-library/react.

---

## Conventions for this plan

- From `dashboard/`, run `npm test` (= `vitest run`). Scope with `npm test -- <path>`.
- STRICT TDD: failing test → confirm fail → minimal impl → confirm pass → commit.
- The mount-centering effect from task-1 stays. This task adds *advance-following*; the two effects are distinct: mount-center fires once (guarded by a ref), follow-on-advance fires whenever the anchor changes while `following` is true.
- Reduced-motion: read `window.matchMedia('(prefers-reduced-motion: reduce)').matches`. In jsdom `matchMedia` is undefined by default, so the hook must guard for its absence and tests must stub it where relevant.

---

### Task 1: `useFollowActiveNode` hook

A hook that, given the anchor node-center position and whether the instance is running, animates to the anchor on change while following, exposes `following` + setters, and pauses on real user moves.

**Files:**

- Create: `src/graph/useFollowActiveNode.ts`
- Create: `src/graph/useFollowActiveNode.test.ts`

**Public shape (final):**

```ts
interface AnchorPosition {
  x: number
  y: number
}

interface UseFollowActiveNodeResult {
  following: boolean
  /** Pass React Flow's onMoveStart event here; non-null event => user move => pause. */
  onMoveStart: (event: unknown) => void
  /** Re-engage following and immediately re-center on the current anchor. */
  recenter: () => void
}

export function useFollowActiveNode(
  anchor: AnchorPosition | null,
  isRunning: boolean,
): UseFollowActiveNodeResult
```

Behavior:

- `following` starts `true`.
- When `anchor` changes (by value x/y) and `following` is true, call `setCenter(x, y, { zoom: <current zoom>, duration })`. `duration` is `400` normally, `0` under reduced motion. The current zoom comes from `useReactFlow().getZoom()` so the animation preserves the user's zoom (only mount-centering forces `DEFAULT_ZOOM`).
- The very first render must NOT trigger a follow animation (that is task-1's mount center's job); follow only fires on *subsequent* anchor changes. Track the previous anchor with a ref.
- `onMoveStart(event)`: if `event` is non-null, set `following = false`. Programmatic `setCenter` passes `null`, so it must not pause.
- `recenter()`: set `following = true` and immediately `setCenter` on the current anchor at `DEFAULT_ZOOM` (re-engage at the readable zoom). Re-export `DEFAULT_ZOOM` from a shared module so the value matches task-1.

> First, extract `DEFAULT_ZOOM` to a shared constant so the hook and `GraphView` agree. In task-1 it lives inline in `ExecutionView.tsx`. Move it to `src/graph/useGraphLayout.ts` (or a small `src/graph/constants.ts`) and import it in both places. The step below assumes `src/graph/constants.ts`.

#### Step 1.0 — Extract DEFAULT_ZOOM to a shared constant (refactor)

- [ ] Create `src/graph/constants.ts`:

```ts
/** Readable default zoom level for the execution graph (within minZoom/maxZoom). */
export const DEFAULT_ZOOM = 0.9
/** Animation duration (ms) for follow-the-active-node viewport moves. */
export const FOLLOW_DURATION_MS = 400
```

- [ ] In `src/routes/ExecutionView.tsx`, remove the inline `const DEFAULT_ZOOM = 0.9` added in task-1 and import it instead: `import { DEFAULT_ZOOM } from '../graph/constants'`.
- [ ] Run: `npm test`
- [ ] **Expected PASS:** unchanged — task-1 tests still green.
- [ ] Commit: `refactor(ui-nits): extract DEFAULT_ZOOM/FOLLOW_DURATION_MS to graph/constants`

#### Step 1.1 — Failing test: starts following and does NOT animate on first render

- [ ] Create `src/graph/useFollowActiveNode.test.ts`:

```ts
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'

const setCenter = vi.fn()
const getZoom = vi.fn(() => 1.5)

vi.mock('@xyflow/react', () => ({
  useReactFlow: () => ({ setCenter, getZoom }),
}))

import { useFollowActiveNode } from './useFollowActiveNode'

beforeEach(() => {
  setCenter.mockClear()
  getZoom.mockClear()
  // Default: no reduced-motion preference, matchMedia present.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(window as any).matchMedia = vi.fn().mockImplementation((q: string) => ({
    matches: false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
})

describe('useFollowActiveNode — initial state', () => {
  it('starts with following=true', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    expect(result.current.following).toBe(true)
  })

  it('does NOT call setCenter on the first render (mount-centering owns that)', () => {
    renderHook(() => useFollowActiveNode({ x: 10, y: 20 }, true))
    expect(setCenter).not.toHaveBeenCalled()
  })
})
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected FAIL:** module not found (file does not exist).

#### Step 1.2 — Minimal impl: state + no-first-render-animation

- [ ] Create `src/graph/useFollowActiveNode.ts`:

```ts
import { useCallback, useEffect, useRef, useState } from 'react'
import { useReactFlow } from '@xyflow/react'
import { DEFAULT_ZOOM, FOLLOW_DURATION_MS } from './constants'

interface AnchorPosition {
  x: number
  y: number
}

interface UseFollowActiveNodeResult {
  following: boolean
  onMoveStart: (event: unknown) => void
  recenter: () => void
}

function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return false
  }
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

export function useFollowActiveNode(
  anchor: AnchorPosition | null,
  _isRunning: boolean,
): UseFollowActiveNodeResult {
  const { setCenter, getZoom } = useReactFlow()
  const [following, setFollowing] = useState(true)

  // Remember the anchor we last centered on so the first render does not animate
  // (mount-centering in GraphView handles the initial center) and so we only
  // animate when the anchor actually changes.
  const prevAnchorRef = useRef<AnchorPosition | null>(null)

  useEffect(() => {
    if (!anchor) return
    const prev = prevAnchorRef.current
    // First time we see any anchor: record it, do not animate.
    if (prev === null) {
      prevAnchorRef.current = anchor
      return
    }
    // Unchanged anchor: nothing to do.
    if (prev.x === anchor.x && prev.y === anchor.y) return

    prevAnchorRef.current = anchor
    if (!following) return

    const duration = prefersReducedMotion() ? 0 : FOLLOW_DURATION_MS
    setCenter(anchor.x, anchor.y, { zoom: getZoom(), duration })
  }, [anchor, following, setCenter, getZoom])

  const onMoveStart = useCallback((event: unknown) => {
    if (event != null) setFollowing(false)
  }, [])

  const recenter = useCallback(() => {
    setFollowing(true)
    if (prevAnchorRef.current) {
      const a = prevAnchorRef.current
      setCenter(a.x, a.y, { zoom: DEFAULT_ZOOM })
    }
  }, [setCenter])

  return { following, onMoveStart, recenter }
}
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected PASS:** both initial-state tests pass.
- [ ] Commit: `feat(ui-nits): useFollowActiveNode hook scaffold (state, no first-render animate)`

#### Step 1.3 — Failing test: animates on anchor change while following

- [ ] Append:

```ts
describe('useFollowActiveNode — follow on advance', () => {
  it('animates to the new anchor when it changes while following', () => {
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    // First render recorded the anchor; no setCenter yet.
    expect(setCenter).not.toHaveBeenCalled()

    rerender({ a: { x: 100, y: 200 } })
    expect(setCenter).toHaveBeenCalledTimes(1)
    expect(setCenter).toHaveBeenCalledWith(100, 200, {
      zoom: 1.5, // from mocked getZoom()
      duration: 400,
    })
  })

  it('does not animate when the anchor value is unchanged', () => {
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    rerender({ a: { x: 10, y: 20 } }) // same value, new object identity
    expect(setCenter).not.toHaveBeenCalled()
  })
})
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected:** PASS (the impl already covers this). If the "unchanged value" test FAILs, confirm the ref comparison uses `prev.x === anchor.x && prev.y === anchor.y`, not object identity.
- [ ] Commit: `test(ui-nits): useFollowActiveNode animates on anchor advance, skips no-op`

#### Step 1.4 — Failing test: pauses on real user move, ignores programmatic null move

- [ ] Append:

```ts
describe('useFollowActiveNode — pause on user interaction', () => {
  it('stops following when onMoveStart receives a real (non-null) event', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    act(() => {
      result.current.onMoveStart({ type: 'pointerdown' }) // real user gesture
    })
    expect(result.current.following).toBe(false)
  })

  it('does NOT stop following when onMoveStart receives a null event (programmatic)', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    act(() => {
      result.current.onMoveStart(null) // our own setCenter
    })
    expect(result.current.following).toBe(true)
  })

  it('does not animate to a new anchor after the user paused following', () => {
    const { result, rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    act(() => {
      result.current.onMoveStart({ type: 'wheel' }) // user zooms => pause
    })
    setCenter.mockClear()
    rerender({ a: { x: 100, y: 200 } }) // execution advances
    expect(setCenter).not.toHaveBeenCalled()
  })
})
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected:** PASS (impl covers it). If the "does not animate after pause" test FAILs, confirm the effect early-returns when `!following`.
- [ ] Commit: `test(ui-nits): useFollowActiveNode pauses on real move, ignores null move`

#### Step 1.5 — Failing test: recenter re-engages and centers at DEFAULT_ZOOM

- [ ] Append:

```ts
describe('useFollowActiveNode — recenter', () => {
  it('re-engages following and centers on the current anchor at DEFAULT_ZOOM', () => {
    const { result } = renderHook(() =>
      useFollowActiveNode({ x: 10, y: 20 }, true),
    )
    // Pause first.
    act(() => result.current.onMoveStart({ type: 'pointerdown' }))
    expect(result.current.following).toBe(false)
    setCenter.mockClear()

    act(() => result.current.recenter())

    expect(result.current.following).toBe(true)
    expect(setCenter).toHaveBeenCalledTimes(1)
    expect(setCenter).toHaveBeenCalledWith(10, 20, { zoom: 0.9 })
  })
})
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected:** PASS. If FAIL, confirm `recenter` reads `prevAnchorRef.current` and uses `DEFAULT_ZOOM`.
- [ ] Commit: `test(ui-nits): useFollowActiveNode recenter re-engages at DEFAULT_ZOOM`

#### Step 1.6 — Failing test: reduced-motion uses duration 0

- [ ] Append:

```ts
describe('useFollowActiveNode — reduced motion', () => {
  it('uses duration 0 when prefers-reduced-motion is set', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).matchMedia = vi.fn().mockImplementation((q: string) => ({
      matches: q.includes('reduce'),
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
    const { rerender } = renderHook(
      ({ a }: { a: { x: number; y: number } }) => useFollowActiveNode(a, true),
      { initialProps: { a: { x: 10, y: 20 } } },
    )
    rerender({ a: { x: 100, y: 200 } })
    expect(setCenter).toHaveBeenCalledWith(100, 200, {
      zoom: 1.5,
      duration: 0,
    })
  })
})
```

- [ ] Run: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected:** PASS (impl reads `prefersReducedMotion()` per follow). If FAIL, confirm `prefersReducedMotion` is called inside the effect (not memoized at mount) so the per-test `matchMedia` stub takes effect.
- [ ] Commit: `test(ui-nits): useFollowActiveNode honors prefers-reduced-motion (duration 0)`

#### Step 1.7 — Hook review

- [ ] Run the whole hook file: `npm test -- src/graph/useFollowActiveNode.test.ts`
- [ ] **Expected PASS:** all hook tests green (initial state, follow-on-advance, pause, recenter, reduced-motion).

---

### Task 2: Recenter / Follow control + wire hook into GraphView

Render a Recenter / Follow button in the graph toolbar area, forward `onMoveStart` from `<ReactFlow>` to the hook, and pass the anchor position into the hook. The button label reflects state: "Following" (active/highlighted) when `following`, "Recenter" / "Follow" when paused. Clicking it calls `recenter()`.

**Files:**

- Modify: `src/routes/ExecutionView.tsx` — `GraphView` (the version produced by task-1): add the `useFollowActiveNode` call, render the control, and add `onMoveStart` to `<ReactFlow>`. Keep the task-1 mount-center effect and Phase 1 props.
- Modify: `src/routes/ExecutionView.module.css` — add styles for the control (`.recenterButton`, `.recenterButtonActive`).
- Modify: `src/routes/GraphView.test.tsx` — extend the mock to forward `onMoveStart`, add control-behavior tests.

#### Step 2.1 — Failing test: control renders and reflects following state

- [ ] In `src/routes/GraphView.test.tsx`, extend the `@xyflow/react` `ReactFlow` mock so it can fire `onMoveStart` (so tests can simulate a user move). Update the `ReactFlow` mock to render a hidden button that calls the prop, and to keep recording nodes:

```ts
vi.mock('@xyflow/react', () => ({
  ReactFlow: (props: {
    nodes: Array<{ id: string; position?: { x: number; y: number } }>
    children?: React.ReactNode
    onMoveStart?: (event: unknown) => void
  }) => {
    renderCount.value += 1
    lastReactFlowProps = props
    return (
      <div>
        <button
          data-testid="rf-fire-user-move"
          onClick={() => props.onMoveStart?.({ type: 'pointerdown' })}
        />
        {props.nodes.map((n) => (
          <div key={n.id} data-testid={`node-${n.id}`} />
        ))}
        {props.children}
      </div>
    )
  },
  ReactFlowProvider: ({ children }: { children: React.ReactNode }) => children,
  Background: () => null,
  Controls: () => null,
  MarkerType: { ArrowClosed: 'arrowclosed' },
  useNodes: () => [],
  useReactFlow: () => ({
    flowToScreenPosition: (p: unknown) => p,
    setCenter: setCenterSpy,
    getZoom: () => 1,
  }),
}))
```

> Note: `getZoom` is added so `useFollowActiveNode` works under the mock. `props.children` is now rendered so the Recenter control (a child of `<ReactFlow>`, alongside `<Controls>`) appears in the DOM.

- [ ] Add a describe block (uses the module-level `instance` fixture which is running on node 'a'):

```ts
import { fireEvent } from '@testing-library/react'

describe('GraphView — Recenter / Follow control', () => {
  it('renders the control showing the following state by default', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn).toBeTruthy()
    // Default state is "following" — surfaced via aria-pressed.
    expect(btn.getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to paused state after a user move and back after Recenter', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    // Simulate a user pan/zoom via the mock's forwarded onMoveStart.
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    const btn = screen.getByRole('button', { name: /following|recenter|follow/i })
    expect(btn.getAttribute('aria-pressed')).toBe('false')

    // Re-engage via the control.
    fireEvent.click(btn)
    expect(
      screen
        .getByRole('button', { name: /following|recenter|follow/i })
        .getAttribute('aria-pressed'),
    ).toBe('true')
  })
})
```

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected FAIL:** no button matching the name (control not rendered yet).

#### Step 2.2 — Minimal impl: wire hook + render control

- [ ] In `src/routes/ExecutionView.tsx`, add the import: `import { useFollowActiveNode } from '../graph/useFollowActiveNode'`.
- [ ] In `GraphView`, after the `anchorPos` memo and the mount-center effect from task-1, add the hook call. Pass whether the instance is running:

```ts
  const isRunning = instance?.state === 'running' || instance?.state === 'waiting'
  const { following, onMoveStart, recenter } = useFollowActiveNode(
    anchorPos,
    isRunning,
  )
```

- [ ] Add `onMoveStart` to the `<ReactFlow>` element and render the control as a child (alongside `<Background>` / `<Controls>`):

```tsx
      <ReactFlow
        nodes={nodesWithSelection}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodeClick={(_event, node) => onNodeClick(node.id)}
        onMoveStart={(event) => onMoveStart(event)}
        minZoom={0.3}
        maxZoom={2}
        proOptions={{ hideAttribution: true }}
      >
        <Background color="#2a2a4a" gap={20} size={1} />
        <Controls showInteractive={false} />
        <button
          type="button"
          className={`${styles.recenterButton} ${following ? styles.recenterButtonActive : ''}`}
          aria-pressed={following}
          title={following ? 'Following the active step' : 'Recenter and follow the active step'}
          onClick={recenter}
        >
          {following ? 'Following' : 'Recenter'}
        </button>
      </ReactFlow>
```

> Preserve Phase 1's `edgeTypes={edgeTypes}` prop and its import — the block above includes it. Add `onMoveStart` alongside it; do not replace or drop `edgeTypes`. (If Phase 1 has not landed, omit the `edgeTypes` line — but the Phase 4 ordering requires Phase 1 first, so it should be present.)

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected PASS:** the control renders, toggles to paused on the simulated user move, and returns to following on Recenter; existing centering + no-remount tests still pass.
- [ ] Commit: `feat(ui-nits): Recenter/Follow control + follow-on-advance wired into GraphView`

#### Step 2.3 — Failing test: Recenter calls setCenter at DEFAULT_ZOOM on the current anchor

- [ ] Append to the `Recenter / Follow control` describe block:

```ts
  it('re-centers on the active node at DEFAULT_ZOOM when Recenter is clicked', () => {
    render(
      <GraphView
        instance={instance}
        workflowDef={workflowDef}
        trace={trace}
        onNodeClick={() => {}}
        selectedNodeId={null}
      />,
    )
    // Pause via a user move, then clear the mount-center call.
    fireEvent.click(screen.getByTestId('rf-fire-user-move'))
    setCenterSpy.mockClear()

    fireEvent.click(screen.getByRole('button', { name: /recenter/i }))

    const aNode = lastReactFlowProps!.nodes.find((n) => n.id === 'a')!
    expect(setCenterSpy).toHaveBeenCalledWith(
      aNode.position!.x + 100, // NODE_WIDTH / 2
      aNode.position!.y + 40, // NODE_HEIGHT / 2
      { zoom: 0.9 },
    )
  })
```

- [ ] Run: `npm test -- src/routes/GraphView.test.tsx`
- [ ] **Expected:** PASS. If FAIL, confirm `recenter` in the hook reads the recorded anchor (which equals `anchorPos`) and uses `DEFAULT_ZOOM`.
- [ ] Commit: `test(ui-nits): Recenter re-centers active node at DEFAULT_ZOOM`

#### Step 2.4 — Style the control

- [ ] Open `src/routes/ExecutionView.module.css` and add a positioned button style. Place it over the graph (the `.graphContainer` is the React Flow wrapper). Adjust the existing color palette to match (dark theme used elsewhere: borders `#2a2a4a`/`#444466`, muted text `#888`):

```css
.recenterButton {
  position: absolute;
  bottom: 12px;
  right: 12px;
  z-index: 5;
  padding: 6px 12px;
  font-size: 12px;
  color: #ccccdd;
  background: #1a1a2e;
  border: 1px solid #444466;
  border-radius: 4px;
  cursor: pointer;
}

.recenterButton:hover {
  border-color: #6666aa;
}

.recenterButtonActive {
  color: #ffffff;
  border-color: #6666aa;
  background: #2a2a4a;
}
```

- [ ] Confirm `.graphContainer` (already present in this module, used at line 101 of `ExecutionView.tsx`) is `position: relative` so the absolutely-positioned button anchors to it. If it is not, add `position: relative;` to `.graphContainer`. (Read the rule first; only edit if needed.)
- [ ] Run: `npm test`
- [ ] **Expected PASS:** all dashboard tests green (CSS modules are class-name stubs in tests, so styling changes do not affect assertions).
- [ ] Commit: `style(ui-nits): position Recenter/Follow control over the graph`

#### Step 2.5 — Full suite + build

- [ ] Run: `npm test`
- [ ] **Expected PASS:** entire dashboard suite green.
- [ ] Run: `npm run build` (`tsc && vite build`).
- [ ] **Expected:** build succeeds with no TypeScript errors. Resolve any type errors (e.g. the `onMoveStart` event type — React Flow's signature is `(event, viewport) => void`; the inline arrow `(event) => onMoveStart(event)` is assignment-compatible, but if `tsc` complains, type the param as `unknown` or import React Flow's `OnMove` type).

---

## Manual verification (record results; do not skip)

Per design "Testing → Manual check", after `npm run build` and serving the dashboard against a live server:

- [ ] Open a **running** instance: the view opens centered on the active step at a readable zoom; as execution advances, the viewport smoothly animates to the new active step.
- [ ] **Pan/zoom** manually: following stops (the control shows "Recenter", `aria-pressed=false`); the view does not jump when the next step starts.
- [ ] Click **Recenter**: following resumes and the view re-centers on the active step at the default zoom.
- [ ] Open a **completed** instance: centered on the final step; a **failed** instance: centered on the failed step; a **static preview** (no instance): centered on the entry step.
- [ ] With OS "reduce motion" enabled, advancing steps **jump** (no animation) rather than gliding.

---

## Done criteria for this plan file

- `useFollowActiveNode` exists with passing tests covering: starts following; no animation on first render; animates on anchor change while following; no-op on unchanged anchor; pauses on real (non-null) `onMoveStart` and ignores null (programmatic) moves; does not animate after pause; `recenter` re-engages and centers at `DEFAULT_ZOOM`; reduced-motion uses `duration 0`.
- `GraphView` renders a Recenter / Follow control reflecting `following` (via `aria-pressed`), forwards `onMoveStart` to the hook, and feeds the hook the anchor position. Task-1's mount-centering and Phase 1's `edgeTypes`/`loopContainer` nodes are preserved.
- `npm test` and `npm run build` both pass; manual verification recorded.
