# Workflow Auto-Arrangement — Phase 3 Task 11: `useLayoutStatus` polling hook + `LayoutStatusPanel`

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

**Goal:** Give both graph views a revision-scoped arrangement status. It polls while work is active (independent of execution and the WebSocket), backs off on errors without losing the last geometry, ignores out-of-order responses, and refetches on focus. A panel shows a truthful verdict, findings and Retry.

**Architecture:**
- `useLayoutStatus(fetcher, retrier, key)` is a pure-React hook. It keeps the last good `LayoutView`, schedules one timer at a time, and guards responses by request sequence, by key epoch, and by `(savedRevision, jobGeneration)`.
- `retry()` posts the displayed `savedRevision` and then refetches, which resumes polling because the new job is `pending`.
- `LayoutStatusPanel` maps `displayStatus` to titles. It never renders "reviewed" wording for `needs_attention`, retained/unreviewed provenance, or client-side display problems.

**Tech Stack:** TypeScript 6 (strict, noUnusedLocals/Parameters, relative imports only), React 19, @xyflow/react 12, Vitest 4 + jsdom + Testing Library, CSS modules, dagre (fallback only).

Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` · Design: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` (§4 shared renderer, §5 navigation/status/refresh, §6 remove manual positioning) · Contracts: `docs/plans/2026-10-16-workflow-auto-arrangement-implementation-contracts.md` §3 Dashboard TS + §5 Addenda 5–7

**Depends on:** P3T1 (`LayoutView`, `RetryAck`, `DisplayStatus`, `LayoutFinding`, `ACTIVE_STATUSES` in `dashboard/src/graph/arranged/types.ts`).

**Interfaces (consumed, P3T1):**
```ts
export type DisplayStatus = 'pending' | 'arranging' | 'rendering' | 'reviewing' | 'complete' | 'needs_attention' | 'failed' | 'legacy' | 'unavailable'
export interface LayoutFinding { criterion: string; severity: 'blocking' | 'advisory'; source: 'preflight' | 'review'; nodeIds?: string[]; edgeOrdinals?: number[]; evidence: string }
export interface LayoutView { savedRevision: string; jobGeneration: number; resultGeneration: number; state?: string; displayStatus: DisplayStatus; stage?: string; attemptsUsed: number; attemptLimit: number; findings: LayoutFinding[] | null; failureStage?: string; failureReason?: string; geometry?: Geometry; retained?: RetainedLayout; provenance: Provenance; unreviewed: boolean; retryEligible: boolean; available: boolean; availabilityError?: string; /* evidence?, contract? */ }
export interface RetryAck { savedRevision: string; jobGeneration: number; state: string; displayStatus: DisplayStatus }
export const ACTIVE_STATUSES: ReadonlySet<DisplayStatus> // pending, arranging, rendering, reviewing
```

**Interfaces (produced; contracts §3; ★ = addition):**
```ts
// dashboard/src/hooks/useLayoutStatus.ts
export interface UseLayoutStatus { view: LayoutView | null; error: string | null; loading: boolean; refresh: () => void; retry: () => Promise<void>; retrying: boolean; retryError: string | null }
export function useLayoutStatus(fetcher: () => Promise<LayoutView>, retrier: (expectedRevision: string) => Promise<RetryAck>, key: string): UseLayoutStatus
export const POLL_INTERVAL_MS = 2000
export const MAX_BACKOFF_MS = 30000
// dashboard/src/graph/arranged/LayoutStatusPanel.tsx
export interface LayoutStatusPanelProps { view: LayoutView | null; error: string | null; onRetry: () => void; retrying: boolean; retryError: string | null; canRetry: boolean; displayIssues?: readonly string[] /* ★ resolveDisplayGeometry issues */ }
export default function LayoutStatusPanel(props: LayoutStatusPanelProps): JSX.Element | null
```
`refresh` is a stable callback (same identity across renders for a given key). P3T12 depends on that to refetch on a revision mismatch without looping.

---

### Task 11.1: `useLayoutStatus`

**Files:**
- Create: `dashboard/src/hooks/useLayoutStatus.ts`
- Test: `dashboard/src/hooks/useLayoutStatus.test.ts`

Rules:
- **Fetch triggers:** on mount and on `key` change (state resets, and in-flight responses for the old key are discarded via an epoch counter), on window `focus`, and on `visibilitychange` when it becomes `visible`.
- **Polling:** while `view.displayStatus ∈ ACTIVE_STATUSES`, the next fetch runs `POLL_INTERVAL_MS` after each response. Polling stops at terminal statuses.
- **Errors:** keep the last view and set `error`. The next attempt is after `min(MAX_BACKOFF_MS, 2000·2^(n-1))` for the n-th consecutive failure (2 s, 4 s, 8 s, 16 s, 30 s, 30 s …). Retrying never gives up; a success clears `error`.
- **Out-of-order guard:** each request gets an increasing sequence number. A response older than the last applied one is dropped. A view with the same savedRevision but a lower jobGeneration or lower resultGeneration is dropped, but polling continues. A RetryAck installs its minimum accepted generation before any refetch.
- **`retry()`:** sets `retrying`, calls retrier(view.savedRevision), validates same revision plus a higher generation, installs its acknowledged minimum generation, invalidates earlier GETs, displays pending with retained same-revision geometry, then refetches. Stale terminal reads below that generation cannot stop polling. On failure it sets `retryError` to the error message and keeps the view. With no view yet, it just reloads.
- **Unmount:** clears the timer and listeners and invalidates in-flight work.

The tests use `vi.useFakeTimers()`. Fetchers are scripted per call number, and deferred steps let the test control resolution order. Each fake honours the call sequence, so out-of-order and back-off behaviour are actually exercised.

- [x] **Step 1: Write the failing test**

```ts
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useLayoutStatus, POLL_INTERVAL_MS, MAX_BACKOFF_MS } from './useLayoutStatus'
import type { DisplayStatus, LayoutView, RetryAck } from '../graph/arranged/types'

function view(displayStatus: DisplayStatus, over: Partial<LayoutView> = {}): LayoutView {
  return {
    savedRevision: 'rev-1',
    jobGeneration: 1,
    resultGeneration: 0,
    displayStatus,
    attemptsUsed: 1,
    attemptLimit: 5,
    findings: [],
    provenance: 'retained_positions',
    unreviewed: true,
    retryEligible: displayStatus === 'needs_attention' || displayStatus === 'failed',
    available: true,
    ...over,
  }
}

interface Deferred {
  promise: Promise<LayoutView>
  resolve: (v: LayoutView) => void
}
function deferred(): Deferred {
  let resolve!: (v: LayoutView) => void
  const promise = new Promise<LayoutView>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

type Step = LayoutView | Error | Deferred

/** A fetcher that answers call N with steps[N] (last step repeats), honouring call order. */
function scripted(steps: Step[]) {
  return vi.fn(async (): Promise<LayoutView> => {
    const i = scriptedCalls.get(steps) ?? 0
    scriptedCalls.set(steps, i + 1)
    const step = steps[Math.min(i, steps.length - 1)]
    if (step instanceof Error) throw step
    if ('promise' in step) return step.promise
    return step
  })
}
const scriptedCalls = new Map<Step[], number>()

const okRetrier = () =>
  vi.fn(async (rev: string): Promise<RetryAck> => ({
    savedRevision: rev,
    jobGeneration: 2,
    state: 'pending',
    displayStatus: 'pending',
  }))

/** Advance fake time and flush the promise chains it releases. */
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  scriptedCalls.clear()
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useLayoutStatus — polling', () => {
  it('fetches on mount and stops polling once the layout is complete', async () => {
    const fetcher = scripted([view('pending'), view('complete')])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(result.current.view?.displayStatus).toBe('pending')
    expect(result.current.loading).toBe(false)

    await advance(POLL_INTERVAL_MS)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(result.current.view?.displayStatus).toBe('complete')

    await advance(POLL_INTERVAL_MS * 10)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('keeps polling through arranging → reviewing → complete', async () => {
    const fetcher = scripted([
      view('arranging', { attemptsUsed: 1 }),
      view('reviewing', { attemptsUsed: 2 }),
      view('complete', { attemptsUsed: 2 }),
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(result.current.view?.displayStatus).toBe('arranging')
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.displayStatus).toBe('reviewing')
    expect(result.current.view?.attemptsUsed).toBe(2)
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.displayStatus).toBe('complete')
    await advance(POLL_INTERVAL_MS * 5)
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('does not poll a terminal needs_attention view', async () => {
    const fetcher = scripted([view('needs_attention')])
    renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(POLL_INTERVAL_MS * 5)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('useLayoutStatus — errors and back-off', () => {
  it('keeps the last view on a network error and backs off 2s, 4s, 8s', async () => {
    const fetcher = scripted([
      view('arranging'),
      new Error('API 503: layout: status store unavailable'),
      new Error('network down'),
      new Error('network down'),
      view('complete'),
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    await advance(POLL_INTERVAL_MS) // call 2 fails
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(result.current.error).toBe('API 503: layout: status store unavailable')
    expect(result.current.view?.displayStatus).toBe('arranging')

    await advance(2000 - 1)
    expect(fetcher).toHaveBeenCalledTimes(2)
    await advance(1) // call 3 at +2s fails
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(result.current.error).toBe('network down')

    await advance(4000 - 1)
    expect(fetcher).toHaveBeenCalledTimes(3)
    await advance(1) // call 4 at +4s fails
    expect(fetcher).toHaveBeenCalledTimes(4)

    await advance(8000 - 1)
    expect(fetcher).toHaveBeenCalledTimes(4)
    await advance(1) // call 5 at +8s succeeds
    expect(fetcher).toHaveBeenCalledTimes(5)
    expect(result.current.error).toBeNull()
    expect(result.current.view?.displayStatus).toBe('complete')
  })

  it('caps the back-off at MAX_BACKOFF_MS', async () => {
    const fetcher = scripted([new Error('down')])
    renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0) // 1 (fail) → next in 2s
    await advance(2000) // 2 → 4s
    await advance(4000) // 3 → 8s
    await advance(8000) // 4 → 16s
    await advance(16000) // 5 → 32s capped to 30s
    expect(fetcher).toHaveBeenCalledTimes(5)
    await advance(MAX_BACKOFF_MS - 1)
    expect(fetcher).toHaveBeenCalledTimes(5)
    await advance(1)
    expect(fetcher).toHaveBeenCalledTimes(6)
  })

  it('reports an error with no view when the first read fails', async () => {
    const fetcher = scripted([new Error('API 403: forbidden')])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(result.current.view).toBeNull()
    expect(result.current.error).toBe('API 403: forbidden')
    expect(result.current.loading).toBe(false)
  })
})

describe('useLayoutStatus — out-of-order responses', () => {
  it('ignores a response that arrives after a newer one was applied', async () => {
    const slow = deferred()
    const fetcher = scripted([slow, view('complete', { jobGeneration: 1 })])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    // A focus refetch is issued while the first request is still in flight.
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    await advance(0)
    expect(result.current.view?.displayStatus).toBe('complete')
    // The older request finally answers with stale in-progress data.
    await act(async () => {
      slow.resolve(view('reviewing', { jobGeneration: 1 }))
    })
    await advance(0)
    expect(result.current.view?.displayStatus).toBe('complete')
  })

  it('ignores a view for the same revision with a lower job generation', async () => {
    const fetcher = scripted([
      view('arranging', { jobGeneration: 2 }),
      view('needs_attention', { jobGeneration: 1 }), // stale replica of the previous job
      view('complete', { jobGeneration: 2 }),
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.displayStatus).toBe('arranging')
    expect(result.current.view?.jobGeneration).toBe(2)
    // Still active, so polling continues and picks up the real result.
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.displayStatus).toBe('complete')
  })

  it('accepts a newer revision even if its generation is lower', async () => {
    const fetcher = scripted([view('complete', { jobGeneration: 3 })])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    fetcher.mockImplementationOnce(async () => view('pending', { savedRevision: 'rev-2', jobGeneration: 1 }))
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-2')
  })
})

describe('useLayoutStatus — retry', () => {
  it('posts the current savedRevision, refetches and resumes polling with the new generation', async () => {
    const fetcher = scripted([
      view('needs_attention', { jobGeneration: 1 }),
      view('pending', { jobGeneration: 2 }),
      view('complete', { jobGeneration: 2 }),
    ])
    const retrier = okRetrier()
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(POLL_INTERVAL_MS * 3)
    expect(fetcher).toHaveBeenCalledTimes(1)

    await act(async () => {
      await result.current.retry()
    })
    expect(retrier).toHaveBeenCalledWith('rev-1')
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(result.current.view?.displayStatus).toBe('pending')
    expect(result.current.view?.jobGeneration).toBe(2)
    expect(result.current.retrying).toBe(false)

    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.displayStatus).toBe('complete')
  })

  it('keeps the view and reports retryError when the retry is rejected', async () => {
    const fetcher = scripted([view('failed', { failureReason: 'renderer unavailable' })])
    const retrier = vi.fn(async (): Promise<RetryAck> => {
      throw new Error('API 409: layout: expected revision is not current')
    })
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    await act(async () => {
      await result.current.retry()
    })
    expect(result.current.retryError).toBe('API 409: layout: expected revision is not current')
    expect(result.current.view?.displayStatus).toBe('failed')
    expect(result.current.retrying).toBe(false)
  })

  it('just reloads when there is no view to retry against', async () => {
    const fetcher = scripted([new Error('down'), view('pending')])
    const retrier = okRetrier()
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    await act(async () => {
      await result.current.retry()
    })
    expect(retrier).not.toHaveBeenCalled()
    expect(result.current.view?.displayStatus).toBe('pending')
  })
})

describe('useLayoutStatus — lifecycle', () => {
  it('clears its timers on unmount', async () => {
    const fetcher = scripted([view('arranging')])
    const { unmount } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    await advance(POLL_INTERVAL_MS * 5)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('refetches on window focus and when the document becomes visible', async () => {
    const fetcher = scripted([view('complete')])
    renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(1)

    await act(async () => {
      window.dispatchEvent(new Event('focus'))
    })
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(2)

    let visibility: DocumentVisibilityState = 'hidden'
    const spy = vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(2)
    visibility = 'visible'
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(3)
    spy.mockRestore()
  })

  it('resets and refetches when the key changes, ignoring the old key in flight', async () => {
    const old = deferred()
    const fetcherA = scripted([old])
    const fetcherB = scripted([view('complete', { savedRevision: 'rev-b' })])
    const { result, rerender } = renderHook(
      ({ f, k }: { f: () => Promise<LayoutView>; k: string }) => useLayoutStatus(f, okRetrier(), k),
      { initialProps: { f: fetcherA as () => Promise<LayoutView>, k: 'a' } },
    )
    await advance(0)
    rerender({ f: fetcherB, k: 'b' })
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-b')
    await act(async () => {
      old.resolve(view('arranging', { savedRevision: 'rev-a' }))
    })
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-b')
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

Observed missing import, then all 15 behavioral assertions failing against a compiling empty hook.

```bash
cd dashboard && npx vitest run src/hooks/useLayoutStatus.test.ts
```
Expected:
```text
Error: Failed to resolve import "./useLayoutStatus" from "src/hooks/useLayoutStatus.test.ts". Does the file exist?
```

- [x] **Step 3: Implement**

Implement `useLayoutStatus(fetcher, retrier, key): UseLayoutStatus` in
`dashboard/src/hooks/useLayoutStatus.ts`, keeping the exported interface above. Use one
request sequence, a key/unmount epoch, and a minimum acknowledged job generation per
revision. Apply the ordering, in-band outage and RetryAck algorithm in contracts §6.6.
Preserve callback refs and timer cleanup. Pending/active GETs poll at 2s; transport and
in-band availability failures back off at 2/4/8/.../30s; only a healthy terminal response
at or above the acknowledged generation stops polling. A stale GET must not stop the timer.
Reset retrying and all guards on key change. No view from another key is retained.


- [x] **Step 4: Run it and confirm it passes**

Observed: initially 25 hook tests pass; final review expands this to 28, all passing; TypeScript exits 0. Includes outage, retry-fence and epoch regressions added before their correction.

```bash
cd dashboard && npx vitest run src/hooks/useLayoutStatus.test.ts && npx tsc --noEmit
```
Expected:
```text
      Tests  15 passed (15)
(tsc exits 0)
```

- [x] **Step 5: Mutation check (do not commit the mutations).** Observed independent behavioral failures after removing sequence ordering, job-generation ordering and cleanup; restored from `/tmp/p3t11-hook-backup.ts`. Additional outage/result-generation/ack-invalidation/epoch mutations also fail their targeted assertions. Each guard has a test that fails without it. Run these one at a time and restore after each with a temporary-file backup of `dashboard/src/hooks/useLayoutStatus.ts` taken before the mutation:
  - Delete ` || seq < appliedRef.current` from the success branch → "ignores a response that arrives after a newer one was applied" fails.
  - Replace `next.jobGeneration < cur.jobGeneration` with `false` → "ignores a view for the same revision with a lower job generation" fails.
  - Delete `clearTimer()` from the effect cleanup → "clears its timers on unmount" fails.

- [x] **Step 6: Commit**

```bash
git add dashboard/src/hooks/useLayoutStatus.ts dashboard/src/hooks/useLayoutStatus.test.ts && git commit -m "feat(dashboard): add revision-scoped layout status polling hook" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11.2: `LayoutStatusPanel`

**Files:**
- Create: `dashboard/src/graph/arranged/LayoutStatusPanel.tsx`
- Create: `dashboard/src/graph/arranged/LayoutStatusPanel.module.css`
- Test: `dashboard/src/graph/arranged/LayoutStatusPanel.test.tsx`

**Titles:**

| displayStatus | title |
|---|---|
| pending | Arrangement pending |
| arranging | Arranging layout (with "attempt N of M") |
| rendering | Rendering for review (with "attempt N of M") |
| reviewing | Reviewing readability (with "attempt N of M") |
| complete | Layout reviewed |
| needs_attention | Layout needs attention |
| failed | Layout arrangement failed |
| unavailable | Arrangement status unavailable |
| legacy | renders nothing (unless there is an error) |

No view plus an error also shows "Arrangement status unavailable". `complete` with displayIssues, unreviewed=true, missing geometry, unavailable status or retained provenance is downgraded to "Layout needs attention". Only a healthy reviewed arranged geometry may say "Layout reviewed".

**Body:**
- Findings, blocking first, as `criterion (severity): evidence`.
- `Reason (stage): failureReason`.
- `availabilityError`.
- `Could not refresh arrangement status: <error>`.
- `Retry failed: <retryError>`.
- One line per display issue.
- The note "Showing the previous layout — not yet reviewed for this revision" when provenance is `retained_geometry`/`retained_positions`, or `unreviewed`, or there are display issues.

**Retry button:** `type="button"`, shown when `canRetry && (view.retryEligible || error != null || view.available === false)`, disabled while `retrying`.

- [x] **Step 1: Write the failing test**

```tsx
import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import LayoutStatusPanel, { type LayoutStatusPanelProps } from './LayoutStatusPanel'
import type { DisplayStatus, LayoutView } from './types'
import { fixtureView } from './testFixtures'

function view(displayStatus: DisplayStatus, over: Partial<LayoutView> = {}): LayoutView {
  return fixtureView({ displayStatus, attemptsUsed: 2, ...over })
}

function renderPanel(over: Partial<LayoutStatusPanelProps> = {}) {
  const props: LayoutStatusPanelProps = {
    view: null,
    error: null,
    onRetry: vi.fn(),
    retrying: false,
    retryError: null,
    canRetry: true,
    ...over,
  }
  return { ...render(<LayoutStatusPanel {...props} />), props }
}

describe('LayoutStatusPanel — titles', () => {
  it.each([
    ['pending', 'Arrangement pending'],
    ['arranging', 'Arranging layout'],
    ['rendering', 'Rendering for review'],
    ['reviewing', 'Reviewing readability'],
    ['complete', 'Layout reviewed'],
    ['needs_attention', 'Layout needs attention'],
    ['failed', 'Layout arrangement failed'],
    ['unavailable', 'Arrangement status unavailable'],
  ] as Array<[DisplayStatus, string]>)('%s → %s', (status, title) => {
    renderPanel({ view: view(status) })
    expect(screen.getByRole('status')).toHaveTextContent(title)
  })

  it('shows attempt N of M while arranging, rendering or reviewing', () => {
    renderPanel({ view: view('rendering', { attemptsUsed: 3, attemptLimit: 5 }) })
    expect(screen.getByText('attempt 3 of 5')).toBeInTheDocument()
  })

  it('does not show an attempt counter for pending or terminal states', () => {
    renderPanel({ view: view('pending', { attemptsUsed: 0 }) })
    expect(screen.queryByText(/attempt \d+ of/)).toBeNull()
  })

  it('renders nothing for a legacy (pre-feature) revision', () => {
    const { container } = renderPanel({ view: view('legacy', { provenance: 'legacy' }) })
    expect(container).toBeEmptyDOMElement()
  })

  it('shows a checking state before the first status arrives', () => {
    renderPanel()
    expect(screen.getByRole('status')).toHaveTextContent('Checking arrangement status')
  })
})

describe('LayoutStatusPanel — truthful verdicts', () => {
  it('never shows reviewed/complete wording for needs_attention (including unfinished superseded work)', () => {
    renderPanel({ view: view('needs_attention', { state: 'superseded', provenance: 'retained_geometry' }) })
    const status = screen.getByRole('status')
    expect(status).not.toHaveTextContent('Layout reviewed')
    expect(status).not.toHaveTextContent(/complete/i)
    expect(status).toHaveTextContent('Layout needs attention')
  })

  it('downgrades complete to needs attention when the geometry could not be displayed', () => {
    renderPanel({
      view: view('complete'),
      displayIssues: ['renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"'],
    })
    const status = screen.getByRole('status')
    expect(status).not.toHaveTextContent('Layout reviewed')
    expect(status).toHaveTextContent('Layout needs attention')
    expect(status).toHaveTextContent(
      'This layout could not be displayed: renderer contract version "arranged-renderer/2" does not match "arranged-renderer/1"',
    )
    expect(status).toHaveTextContent('Showing the previous layout — not yet reviewed for this revision')
  })

  it.each(['retained_geometry', 'retained_positions'] as const)(
    'notes that a %s layout is not yet reviewed for this revision',
    (provenance) => {
      renderPanel({ view: view('arranging', { provenance }) })
      expect(screen.getByText('Showing the previous layout — not yet reviewed for this revision')).toBeInTheDocument()
    },
  )

  it('notes an unreviewed best layout', () => {
    renderPanel({ view: view('needs_attention', { unreviewed: true }) })
    expect(screen.getByText('Showing the previous layout — not yet reviewed for this revision')).toBeInTheDocument()
  })

  it('does not add the note for a reviewed arranged layout', () => {
    renderPanel({ view: view('complete') })
    expect(screen.queryByText(/not yet reviewed/)).toBeNull()
  })
})

describe('LayoutStatusPanel — findings and problems', () => {
  it('lists blocking findings before advisory ones with criterion and evidence', () => {
    renderPanel({
      view: view('needs_attention', {
        findings: [
          { criterion: 'crossings', severity: 'advisory', source: 'preflight', evidence: '3 crossings' },
          { criterion: 'text_clipped', severity: 'blocking', source: 'review', evidence: 'label of "b" clipped' },
        ],
      }),
    })
    const items = screen.getAllByRole('listitem')
    expect(items.map((li) => li.getAttribute('data-severity'))).toEqual(['blocking', 'advisory'])
    expect(items[0]).toHaveTextContent('text_clipped (blocking): label of "b" clipped')
    expect(items[1]).toHaveTextContent('crossings (advisory): 3 crossings')
  })

  it('treats null findings as none', () => {
    renderPanel({ view: view('failed', { findings: null }) })
    expect(screen.queryAllByRole('listitem')).toHaveLength(0)
  })

  it('shows the failure stage and reason', () => {
    renderPanel({ view: view('failed', { failureStage: 'rendering', failureReason: 'no Chrome/Chromium browser available' }) })
    expect(screen.getByText('Reason (rendering): no Chrome/Chromium browser available')).toBeInTheDocument()
  })

  it('shows the availability error', () => {
    renderPanel({ view: view('pending', { availabilityError: 'awaiting recovery' }) })
    expect(screen.getByText('awaiting recovery')).toBeInTheDocument()
  })

  it('shows a network error while keeping the last status', () => {
    renderPanel({ view: view('reviewing'), error: 'network down' })
    expect(screen.getByRole('status')).toHaveTextContent('Reviewing readability')
    expect(screen.getByText('Could not refresh arrangement status: network down')).toBeInTheDocument()
  })

  it('shows status unavailable when the first read failed', () => {
    renderPanel({ error: 'API 503: layout: status store unavailable' })
    expect(screen.getByRole('status')).toHaveTextContent('Arrangement status unavailable')
  })

  it('shows the retry error', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), retryError: 'API 409: conflict' })
    expect(screen.getByText('Retry failed: API 409: conflict')).toBeInTheDocument()
  })
})

describe('LayoutStatusPanel — Retry', () => {
  it('offers Retry for a retry-eligible revision and calls onRetry', () => {
    const { props } = renderPanel({ view: view('needs_attention', { retryEligible: true }) })
    const btn = screen.getByRole('button', { name: 'Retry arrangement' })
    expect(btn).toHaveAttribute('type', 'button')
    fireEvent.click(btn)
    expect(props.onRetry).toHaveBeenCalledTimes(1)
  })

  it('offers Retry after a status read error even when the view is not eligible', () => {
    renderPanel({ view: view('reviewing'), error: 'network down' })
    expect(screen.getByRole('button', { name: 'Retry arrangement' })).toBeInTheDocument()
  })

  it('hides Retry when not eligible and there is no error', () => {
    renderPanel({ view: view('complete') })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('hides Retry when the caller cannot retry', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), canRetry: false })
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('disables Retry while a retry is in flight', () => {
    renderPanel({ view: view('failed', { retryEligible: true }), retrying: true })
    expect(screen.getByRole('button', { name: 'Retrying…' })).toBeDisabled()
  })
})
```

- [x] **Step 2: Run it and confirm it fails**

Observed missing import, then 24 behavioral failures against the compiling null component (6 absence assertions passed). The expanded pre-implementation panel suite had 34 behavioral failures.

```bash
cd dashboard && npx vitest run src/graph/arranged/LayoutStatusPanel.test.tsx
```
Expected:
```text
Error: Failed to resolve import "./LayoutStatusPanel" from "src/graph/arranged/LayoutStatusPanel.test.tsx". Does the file exist?
```

- [x] **Step 3: Implement the stylesheet** `dashboard/src/graph/arranged/LayoutStatusPanel.module.css`:

```css
.panel {
  flex-shrink: 0;
  padding: 8px 16px;
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-secondary);
  font-size: 12px;
  color: var(--text-secondary);
}

.header {
  display: flex;
  align-items: center;
  gap: 10px;
}

.title {
  font-weight: 600;
  color: var(--text-primary);
}

.toneActive .title {
  color: var(--state-running);
}

.toneOk .title {
  color: var(--state-completed);
}

.toneAttention .title {
  color: var(--state-waiting);
}

.toneFailed .title {
  color: var(--state-failed);
}

.attempt {
  color: var(--text-muted);
}

.spacer {
  flex: 1;
}

.retryButton {
  padding: 4px 12px;
  font-size: 12px;
  font-weight: 600;
  color: #ffffff;
  background: var(--accent);
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.retryButton:disabled {
  cursor: default;
  background: var(--border-color);
}

.note {
  margin-top: 4px;
  color: var(--text-muted);
}

.problem {
  margin-top: 4px;
  color: var(--state-failed);
}

.findings {
  margin: 6px 0 0 16px;
  padding: 0;
}

.finding {
  margin: 2px 0;
}

.criterion {
  font-family: var(--font-mono);
  font-size: 11px;
}

.blocking .criterion {
  color: var(--state-failed);
}

.advisory .criterion {
  color: var(--text-muted);
}
```

- [x] **Step 4: Implement the component** `dashboard/src/graph/arranged/LayoutStatusPanel.tsx`:

```tsx
import type { DisplayStatus, LayoutFinding, LayoutView } from './types'
import styles from './LayoutStatusPanel.module.css'

export interface LayoutStatusPanelProps {
  view: LayoutView | null
  error: string | null
  onRetry: () => void
  retrying: boolean
  retryError: string | null
  canRetry: boolean
  /** Client-side problems drawing view.geometry (resolveDisplayGeometry issues); never shown as reviewed. */
  displayIssues?: readonly string[]
}

type Tone = 'active' | 'ok' | 'attention' | 'failed'

const TITLES: Record<Exclude<DisplayStatus, 'legacy'>, { title: string; tone: Tone }> = {
  pending: { title: 'Arrangement pending', tone: 'active' },
  arranging: { title: 'Arranging layout', tone: 'active' },
  rendering: { title: 'Rendering for review', tone: 'active' },
  reviewing: { title: 'Reviewing readability', tone: 'active' },
  complete: { title: 'Layout reviewed', tone: 'ok' },
  needs_attention: { title: 'Layout needs attention', tone: 'attention' },
  failed: { title: 'Layout arrangement failed', tone: 'failed' },
  unavailable: { title: 'Arrangement status unavailable', tone: 'failed' },
}

const TONE_CLASS: Record<Tone, string> = {
  active: styles.toneActive,
  ok: styles.toneOk,
  attention: styles.toneAttention,
  failed: styles.toneFailed,
}

const IN_PROGRESS: ReadonlySet<DisplayStatus> = new Set(['arranging', 'rendering', 'reviewing'])

function sortedFindings(findings: LayoutFinding[] | null): LayoutFinding[] {
  const list = findings ?? []
  return [...list.filter((f) => f.severity === 'blocking'), ...list.filter((f) => f.severity !== 'blocking')]
}

/**
 * Truthful arrangement/review status for the displayed revision. "Layout
 * reviewed" appears only for displayStatus complete with the reviewed geometry
 * actually drawn; needs_attention (including unfinished superseded work) and
 * any client-side display problem never read as reviewed.
 */
export default function LayoutStatusPanel({
  view,
  error,
  onRetry,
  retrying,
  retryError,
  canRetry,
  displayIssues = [],
}: LayoutStatusPanelProps) {
  if (view?.displayStatus === 'legacy' && !error && displayIssues.length === 0) return null

  let heading: { title: string; tone: Tone }
  if (!view) {
    heading = error
      ? { title: 'Arrangement status unavailable', tone: 'failed' }
      : { title: 'Checking arrangement status', tone: 'active' }
  } else if (view.displayStatus === 'legacy') {
    // Only reached with an error or display issue: a legacy revision has no arrangement to report.
    heading = TITLES.unavailable
  } else {
    heading = TITLES[view.displayStatus]
    if (view.displayStatus === 'complete' && (displayIssues.length > 0 || view.unreviewed || !view.geometry || view.provenance !== 'arranged' || !view.available)) {
      heading = TITLES.needs_attention
    }
  }

  const showAttempt = view != null && IN_PROGRESS.has(view.displayStatus) && view.attemptLimit > 0
  const notReviewed =
    view != null &&
    (view.provenance === 'retained_geometry' ||
      view.provenance === 'retained_positions' ||
      view.unreviewed ||
      displayIssues.length > 0)
  const findings = sortedFindings(view?.findings ?? null)
  const showRetry = canRetry && ((view?.retryEligible ?? false) || error != null || view?.available === false)

  return (
    <div role="status" aria-live="polite" className={`${styles.panel} ${TONE_CLASS[heading.tone]}`}>
      <div className={styles.header}>
        <span className={styles.title}>{heading.title}</span>
        {showAttempt && (
          <span className={styles.attempt}>
            attempt {Math.max(1, view!.attemptsUsed)} of {view!.attemptLimit}
          </span>
        )}
        <span className={styles.spacer} />
        {showRetry && (
          <button type="button" className={styles.retryButton} onClick={onRetry} disabled={retrying}>
            {retrying ? 'Retrying…' : 'Retry arrangement'}
          </button>
        )}
      </div>
      {notReviewed && (
        <div className={styles.note}>Showing the previous layout — not yet reviewed for this revision</div>
      )}
      {view?.failureReason && (
        <div className={styles.problem}>
          Reason{view.failureStage ? ` (${view.failureStage})` : ''}: {view.failureReason}
        </div>
      )}
      {view?.availabilityError && <div className={styles.problem}>{view.availabilityError}</div>}
      {displayIssues.map((issue) => (
        <div key={issue} className={styles.problem}>
          This layout could not be displayed: {issue}
        </div>
      ))}
      {error && <div className={styles.problem}>Could not refresh arrangement status: {error}</div>}
      {retryError && <div className={styles.problem}>Retry failed: {retryError}</div>}
      {findings.length > 0 && (
        <ul className={styles.findings}>
          {findings.map((f, i) => (
            <li
              key={`${f.criterion}-${i}`}
              className={`${styles.finding} ${f.severity === 'blocking' ? styles.blocking : styles.advisory}`}
              data-severity={f.severity}
            >
              <span className={styles.criterion}>{f.criterion}</span> ({f.severity}): {f.evidence}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
```

- [x] **Step 5: Run it and confirm it passes**

Observed: all 40 panel tests pass; TypeScript exits 0.

```bash
cd dashboard && npx vitest run src/graph/arranged/LayoutStatusPanel.test.tsx && npx tsc --noEmit
```
Expected:
```text
      Tests  30 passed (30)
(tsc exits 0)
```

- [x] **Step 6: Commit**

```bash
git add dashboard/src/graph/arranged/LayoutStatusPanel.tsx dashboard/src/graph/arranged/LayoutStatusPanel.module.css dashboard/src/graph/arranged/LayoutStatusPanel.test.tsx && git commit -m "feat(dashboard): add truthful layout status panel with retry" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Wiring:**
  - `useLayoutStatus` + `LayoutStatusPanel` are constructed in `dashboard/src/routes/WorkflowReview.tsx` (P3T12) with `() => api.workflows.getLayout(name)` / `rev => api.workflows.retryLayout(name, rev)`, key `name`.
  - They are also constructed in the `ExecutionView.tsx` default export (P3T13) with `() => api.instances.getLayout(id)` / `rev => api.instances.retryLayout(id, rev)`, key `id`.
  - Both routes pass the hook's (revision-joined) `view` to `ArrangedGraph` (P3T3) and to the panel, plus `displayIssues` from `resolveDisplayGeometry` (P3T1).
  - The route-level tests (`WorkflowReview.test.tsx`, `ExecutionView.layout.test.tsx`) drive the real hook and panel through those roots and assert the rendered status, the polled geometry and the Retry call arguments.
- **Data source:**
  - The fetchers/retriers are the P3T1 client methods. They hit P3T9's `LayoutHandler`, which calls P3T8's `layout.QueryService`.
  - `View` fields come from `layout.Reader.View` (P2T5) over the `layout.Store` row (P2T6) that `layout.Worker` (P2T8) advances: stage/attempts by `Progress`; verdict, findings and failure by `Complete`.
  - Retry generations come from `layout.Requester.Retry` → `Store.Retry` (P2T5/P2T6).
- **Provenance probes:**
  - Hook tests: every poll asserts the value from the scripted fetcher's N-th call, so a hook that stopped calling the fetcher, or returned a cached value, fails.
  - "posts the current savedRevision" asserts the retrier argument.

### Alignment TDD: in-band outages and stale reads after retry

**Files:** `dashboard/src/hooks/useLayoutStatus.ts`, `useLayoutStatus.test.ts`, `LayoutStatusPanel.test.tsx`.

- [x] **RED:** Add fake-timer tests: complete geometry → available=false response without geometry
  → recovered healthy view retains last same-revision geometry while showing outage and
  backing off; initial unavailable also continues polling. RetryAck generation 2 followed
  by GET terminal generation 1 must show pending, keep geometry and poll until generation 2.
  A GET issued before retry cannot undo that acknowledgment. Test same-generation lower
  resultGeneration, key change during retry (retrying resets), and unmount cleanup.
  Panel complete+Unreviewed or missing reviewed geometry shows attention, never “Layout reviewed”.
- [x] Run `cd dashboard && npx vitest run src/hooks/useLayoutStatus.test.ts src/graph/arranged/LayoutStatusPanel.test.tsx && npx tsc --noEmit` and observe the behavioral failure before implementing the correction.

Observed two in-band availability failures before fixing the hook, and the panel failures above before implementation. Review added two further RED→GREEN cases for per-revision retry floors and a valid acknowledgment concurrent with a GET at its generation.
- [x] **GREEN:** Implement the §6.6 algorithm in Task 11.1. Use the actual RetryAck, not only
  `await retrier` as a signal. Preserve same-revision geometry on availability failure,
  but never blend a foreign token's geometry/status. Make all newly added tests part
  of the owning task's ordinary suite; update documented test counts after running.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.

> RESOLVED (production-completion revisit, 2026-10-17): The original local gate passed 68 focused tests, TypeScript, 955 dashboard tests and `make ui`, but correctly remained blocked until P3T12/P3T13 mounted the hook/panel. Both production consumers now exist. This revisit passes 122 hook/panel/route tests, TypeScript, all 1,027 dashboard tests, `make ui`, and six real-root/browser tests under `-race` (168.261s, no skips). Removing same-revision in-band geometry retention fails the actual embedded instance DOM; disconnecting the panel Retry callback fails the persisted SQLite generation sink. Both mutations are restored and the rebuilt embedded assets are unchanged. Full evidence and ordered review appear below.

- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

### Panel truth assertions added before implementation

Table-test complete with unreviewed=true, missing geometry, retained provenance, invalid
contract/displayIssues and available=false. Every row must lack “Layout reviewed”; healthy
fixtureView remains a positive control. For initial unavailable and a failed retry, show
reload/Retry even when no geometry exists. Retry eligibility remains enforced server-side.
Record the actual count after adding the alignment cases; the earlier 15/30 examples do
not waive these tests.

### Pass verification and ordered self-review

- **Spec first:** implemented only Task 11's hook/panel interfaces, polling/backoff/retry lifecycle, truthful headings, findings and errors. Fixed two review-discovered retry races using failing regressions before implementation. Route rewrites remain with their assigned tasks.
- **Anti-green-theater second:** scripted fetches deliver distinguishable geometry/status; Retry asserts the displayed revision. Missing guard/geometry/availability/provenance and disconnected Retry mutations fail at the hook's returned state or panel DOM. This is unit evidence, not the missing production route acceptance above.
- **Quality third:** reviewed neighboring hooks, effect-owned epochs/cleanup, stable callback refs, immutable findings ordering, scoped CSS and TypeScript. No unrelated user files staged.
- Final local gate: 68 focused tests and TypeScript pass after restoring all mutations. Full dashboard: 955 tests pass serially with two workers; the first full run overlapped `make ui`'s dependency reinstall and had missing-module worker errors plus a font-test timeout. The clean serial rerun resolves those failures. `make ui` succeeds; embedded tracked assets are unchanged because route imports are deliberately the next tasks' responsibility.

## Production-completion revisit — 2026-10-17

### Selection and exact scope

Earlier unchecked tasks retain their recorded protocol, policy, baseline-suite or environment blockers; they were not re-failed. P3T11's only remaining step was the verification/dependency gate above, and its P3T12/P3T13 dependencies are now installed. No new production behavior, option, fixture or redundant test was needed to complete that step.

### Ordered self-review

1. **Spec compliance first:** compared the hook/panel and both consuming routes with contracts §6.6 and this task's polling, retry, epoch, revision/generation and truthful-status rules. WorkflowReview supplies the real workflow API fetcher/retrier and joins the saved manifest token; ExecutionView supplies the instance API fetcher/retrier and joins the immutable snapshot token. Both pass the coherent view and client display issues into the actual graph/panel. No sibling protocol or release-gate requirement was changed.
2. **Anti-green-theater second:** used existing behavioral regressions and rebuilt the embedded SPA for each temporary production mutation:
   - Removed `?? previous?.geometry` from the hook's in-band outage path. The hook geometry assertion and ExecutionView's actual node-position assertion failed (2 failures / 48 tests). The real Chrome `TestLayoutSurfaces_HTTPRecoveryRetainsGraphAndBacksOff/instance` then failed at `layout_http_backoff_test.go:122`: expected `arranged`, got `fallback`. Its actual SQLite table outage produces `available=false` with no server geometry, so the sink cannot pass by receiving a convenient fallback from the server/cache. Restored the retention guard.
   - Disconnected `LayoutStatusPanel`'s button callback. Panel and both route suites failed (11 failures / 94 tests). The real Chrome `TestLayoutSurfaces_RenderFailureRetainsFallbackAndRetry` then failed at `layout_surfaces_e2e_test.go:495`: clicking Retry never advanced the actual SQLite row's job generation. Restored the callback.
   - The restored browser suite additionally proves authorised HTTP → real route → every node box/text, route/marker/label and status; actual reviewer findings never read as reviewed; retry after result/cache loss and restart preserves the historical snapshot while leaving the newer workflow generation alone; completed-instance polling refreshes without execution WebSocket events and stops after SPA unmount. These use actual saved manifests/snapshots, SQLite, BrowserRenderer/ReadabilityReviewer and embedded routes, not mocked hook inputs or graph sinks.
3. **Code quality third:** inspected the neighboring catalog hook, effect-owned cleanup/stable callback patterns, both route joins and existing sink helpers. All temporary source edits were restored exactly. `make ui` restores byte-identical tracked embedded assets; no dead code, unrelated visuals or new API is included in this verification-only completion.

### Final verification after restoration

- `cd dashboard && npx vitest run src/hooks/useLayoutStatus.test.ts src/graph/arranged/LayoutStatusPanel.test.tsx src/routes/WorkflowReview.test.tsx src/routes/ExecutionView.layout.test.tsx --maxWorkers=2`: **122 passed**.
- `cd dashboard && npx tsc --noEmit`: **passed**.
- `cd dashboard && npx vitest run --maxWorkers=2`: **1,027 passed / 70 files** (68.81s).
- `make ui`: **passed**, embedded tracked assets unchanged after restoration.
- `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^TestLayoutSurfaces_(HTTPRecoveryRetainsGraphAndBacksOff|RenderFailureRetainsFallbackAndRetry|CompletedInstanceRefreshesWithoutWebSocket|HistoricalRetryAfterSaveCacheLossAndRestart|NeedsAttentionShowsFindingsNotReviewed|PRReviewWorkflowAndInstanceUseReviewedGeometry)$' -count=1 -timeout 6m -v`: **six tests passed** (168.261s), no skips or race warnings.
- Logs: `/tmp/layout11-retention-{unit,browser}-red.log`, `/tmp/layout11-retry-{unit,browser}-red.log`, `/tmp/layout11-full-dashboard.log`, `/tmp/layout11-root-green.log`.

P3T11 is complete. This does not claim the separate complex-sample release, rename, raw-file-read or stdio-identity blockers in sibling tasks are resolved.
