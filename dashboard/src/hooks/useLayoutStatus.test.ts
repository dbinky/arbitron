import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useLayoutStatus, POLL_INTERVAL_MS, MAX_BACKOFF_MS, STATUS_GET_TIMEOUT_MS } from './useLayoutStatus'
import type { DisplayStatus, LayoutView, RetryAck } from '../graph/arranged/types'
import { fixtureView } from '../graph/arranged/testFixtures'

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

interface Deferred<T = LayoutView> {
  promise: Promise<T>
  resolve: (v: T) => void
}
function deferred<T = LayoutView>(): Deferred<T> {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => {
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
  vi.restoreAllMocks()
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
  it('coalesces focus/visibility refetches during an in-flight GET into exactly one follow-up GET', async () => {
    const slow = deferred()
    const fetcher = scripted([slow, view('complete', { jobGeneration: 1 })])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    const visible = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    await act(async () => {
      window.dispatchEvent(new Event('focus'))
      window.dispatchEvent(new Event('focus'))
      document.dispatchEvent(new Event('visibilitychange'))
      result.current.refresh()
    })
    visible.mockRestore()
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(1) // never stacked behind a pending GET
    await act(async () => {
      slow.resolve(view('reviewing', { jobGeneration: 1 }))
    })
    await advance(0)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(result.current.view?.displayStatus).toBe('complete')
    await advance(MAX_BACKOFF_MS)
    expect(fetcher).toHaveBeenCalledTimes(2)
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

describe('useLayoutStatus — bounded single-flight GET', () => {
  /** Never settles and ignores its abort signal, like a hung transport/body. */
  function hanging(signals: AbortSignal[]) {
    return (signal: AbortSignal) => { signals.push(signal); return new Promise<LayoutView>(() => {}) }
  }

  it('times out a never-settling, signal-ignoring GET, keeps the retained graph and recovers by polling', async () => {
    const good = fixtureView({ displayStatus: 'arranging' })
    const recovered = fixtureView({ attemptsUsed: 4 })
    const signals: AbortSignal[] = []
    let calls = 0
    const fetcher = vi.fn((signal: AbortSignal) => {
      calls++
      if (calls === 1) return Promise.resolve(good)
      if (calls === 2) return hanging(signals)(signal)
      return Promise.resolve(recovered)
    })
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(result.current.view).toEqual(good)
    await advance(POLL_INTERVAL_MS) // second GET hangs
    expect(fetcher).toHaveBeenCalledTimes(2)
    await advance(STATUS_GET_TIMEOUT_MS - 1)
    expect(result.current.error).toBeNull()
    expect(signals[0].aborted).toBe(false)
    await advance(1)
    expect(signals[0].aborted).toBe(true) // the real transport is cancelled too
    expect(result.current.error).toMatch(/timed out/)
    expect(result.current.loading).toBe(false)
    expect(result.current.view?.geometry).toEqual(good.geometry) // retained graph still shown
    await advance(POLL_INTERVAL_MS) // first failure backs off one poll interval
    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(result.current.view).toEqual(recovered)
    expect(result.current.error).toBeNull()
  })

  it('stops loading on a hung first GET so the route can show its seed or fallback', async () => {
    const seed = fixtureView({ displayStatus: 'reviewing' })
    const signals: AbortSignal[] = []
    const fetcher = vi.fn(hanging(signals))
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'inst', seed))
    await advance(STATUS_GET_TIMEOUT_MS)
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toMatch(/timed out/)
    expect(result.current.view).toEqual(seed)
    const unseeded = renderHook(() => useLayoutStatus(vi.fn(hanging(signals)), okRetrier(), 'wf'))
    await advance(STATUS_GET_TIMEOUT_MS)
    expect(unseeded.result.current.loading).toBe(false)
    expect(unseeded.result.current.view).toBeNull()
    expect(unseeded.result.current.error).toMatch(/timed out/)
    await advance(POLL_INTERVAL_MS)
    expect(fetcher).toHaveBeenCalledTimes(2) // polling keeps trying after the timeout
  })

  it('a retry acknowledged during an in-flight GET triggers exactly one fresh GET after it completes', async () => {
    const older = deferred()
    const afterAck = deferred()
    const latest = fixtureView({ jobGeneration: 2, resultGeneration: 2 })
    const fetcher = scripted([fixtureView({ displayStatus: 'failed', retryEligible: true }), older, afterAck])
    const retrier = okRetrier()
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    act(() => result.current.refresh()) // the older GET is now in flight
    let retry!: Promise<void>
    await act(async () => { retry = result.current.retry() })
    expect(retrier).toHaveBeenCalledTimes(1)
    expect(result.current.view?.displayStatus).toBe('pending')
    await act(async () => { window.dispatchEvent(new Event('focus')) })
    expect(fetcher).toHaveBeenCalledTimes(2) // the ack did not stack a concurrent GET
    expect(result.current.retrying).toBe(true) // retry waits for its fresh GET
    await act(async () => { older.resolve(fixtureView({ displayStatus: 'failed', jobGeneration: 3, resultGeneration: 3 })) })
    await advance(0)
    expect(result.current.view?.displayStatus).toBe('pending') // pre-ack GET cannot undo the ack
    expect(fetcher).toHaveBeenCalledTimes(3) // exactly one follow-up
    await act(async () => { afterAck.resolve(latest); await retry })
    expect(result.current.view).toEqual(latest)
    expect(result.current.retrying).toBe(false)
    await advance(MAX_BACKOFF_MS)
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('aborts the in-flight GET signal when the epoch ends', async () => {
    const signals: AbortSignal[] = []
    const { unmount } = renderHook(() => useLayoutStatus(vi.fn(hanging(signals)), okRetrier(), 'wf'))
    await advance(0)
    expect(signals[0].aborted).toBe(false)
    unmount()
    expect(signals[0].aborted).toBe(true)
    await act(async () => {}) // let the aborted race settle and release its deadline
    expect(vi.getTimerCount()).toBe(0)
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

describe('useLayoutStatus — availability, retry fences and epochs', () => {
  it('retains same-revision geometry on in-band outages and backs off until healthy', async () => {
    const good = fixtureView()
    const outage = view('unavailable', { available: false, availabilityError: 'store offline', jobGeneration: 0 })
    const recovered = fixtureView({ attemptsUsed: 4 })
    const fetcher = scripted([good, outage, outage, recovered])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    act(() => result.current.refresh())
    await advance(0)
    expect(result.current.view?.geometry).toEqual(good.geometry)
    expect(result.current.view?.available).toBe(false)
    expect(result.current.view?.availabilityError).toBe('store offline')
    expect(result.current.view?.unreviewed).toBe(true)
    expect(result.current.view?.provenance).toBe('retained_geometry')
    expect(result.current.error).toBe('store offline')
    await advance(1999)
    expect(fetcher).toHaveBeenCalledTimes(2)
    await advance(1)
    expect(fetcher).toHaveBeenCalledTimes(3)
    await advance(3999)
    expect(fetcher).toHaveBeenCalledTimes(3)
    await advance(1)
    expect(result.current.view).toEqual(recovered)
    expect(result.current.error).toBeNull()
    await advance(MAX_BACKOFF_MS)
    expect(fetcher).toHaveBeenCalledTimes(4)
  })

  it('polls initial unavailable and never borrows geometry from another revision', async () => {
    const fetcher = scripted([
      view('unavailable', { available: false }), fixtureView(),
      view('unavailable', { savedRevision: 'rev-2', available: false }),
      fixtureView({ savedRevision: 'rev-2' }),
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    expect(result.current.view?.available).toBe(false)
    expect(result.current.view?.geometry).toBeUndefined()
    expect(result.current.error).toBeTruthy()
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.geometry).toEqual(fixtureView().geometry)
    act(() => result.current.refresh())
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-2')
    expect(result.current.view?.geometry).toBeUndefined()
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view).toEqual(fixtureView({ savedRevision: 'rev-2' }))
  })

  it('installs RetryAck before refetch; stale terminal reads cannot drop geometry or stop polling', async () => {
    const before = fixtureView({ displayStatus: 'needs_attention', retryEligible: true })
    const after = fixtureView({ jobGeneration: 2, resultGeneration: 2, attemptsUsed: 3 })
    after.geometry!.nodes[0].box.x = 76
    const fetcher = scripted([before, before, after])
    const retrier = okRetrier()
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    await act(async () => { await result.current.retry() })
    expect(retrier).toHaveBeenCalledWith(before.savedRevision)
    expect(result.current.view?.displayStatus).toBe('pending')
    expect(result.current.view?.jobGeneration).toBe(2)
    expect(result.current.view?.geometry).toEqual(before.geometry)
    expect(result.current.view?.unreviewed).toBe(true)
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view).toEqual(after)
    expect(result.current.error).toBeNull()
    await advance(POLL_INTERVAL_MS * 3)
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('invalidates GETs issued before retry acknowledgment, even with higher generations', async () => {
    const slow = deferred()
    const latest = fixtureView({ jobGeneration: 2, resultGeneration: 2 })
    const afterAck = deferred()
    const fetcher = scripted([fixtureView({ displayStatus: 'failed' }), slow, afterAck])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    act(() => result.current.refresh())
    let retry!: Promise<void>
    await act(async () => { retry = result.current.retry() })
    expect(result.current.view?.displayStatus).toBe('pending')
    await act(async () => { slow.resolve(fixtureView({ displayStatus: 'failed', jobGeneration: 3, resultGeneration: 3 })) })
    expect(result.current.view?.displayStatus).toBe('pending')
    expect(result.current.view?.jobGeneration).toBe(2)
    await act(async () => { afterAck.resolve(latest); await retry })
    expect(result.current.view).toEqual(latest)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('drops same-generation lower resultGeneration and keeps polling', async () => {
    const before = fixtureView({ displayStatus: 'reviewing', jobGeneration: 3, resultGeneration: 2 })
    const fetcher = scripted([before, fixtureView({ jobGeneration: 3, resultGeneration: 1 }), fixtureView({ jobGeneration: 3, resultGeneration: 3 })])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view).toEqual(before)
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.resultGeneration).toBe(3)
  })

  it.each([
    { savedRevision: 'foreign', jobGeneration: 2 },
    { savedRevision: 'rev-1', jobGeneration: 1 },
  ])('rejects invalid RetryAck %j without changing the view', async (ack) => {
    const before = fixtureView({ displayStatus: 'failed' })
    const fetcher = scripted([before])
    const retrier = vi.fn(async (revision: string): Promise<RetryAck> => {
      expect(revision).toBe(before.savedRevision)
      return { ...ack, state: 'pending', displayStatus: 'pending' }
    })
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    await act(async () => { await result.current.retry() })
    expect(result.current.retryError).toMatch(/acknowledgement/)
    expect(result.current.view).toEqual(before)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('resets retrying and all guards on key change during retry (A → B → A)', async () => {
    const ack = deferred<RetryAck>()
    const retrier = vi.fn((revision: string) => {
      expect(revision).toBe('rev-1')
      return ack.promise
    })
    const fetcher = scripted([fixtureView({ jobGeneration: 4 }), fixtureView({ savedRevision: 'rev-b' }), fixtureView()])
    const { result, rerender } = renderHook(({ key }) => useLayoutStatus(fetcher, retrier, key), { initialProps: { key: 'a' } })
    await advance(0)
    let retry!: Promise<void>
    act(() => { retry = result.current.retry() })
    expect(result.current.retrying).toBe(true)
    rerender({ key: 'b' })
    expect(result.current.view).toBeNull()
    expect(result.current.retrying).toBe(false)
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-b')
    rerender({ key: 'a' })
    await advance(0)
    expect(result.current.view).toEqual(fixtureView())
    await act(async () => {
      ack.resolve({ savedRevision: 'rev-1', jobGeneration: 5, state: 'pending', displayStatus: 'pending' })
      await retry
    })
    expect(result.current.view).toEqual(fixtureView())
    expect(result.current.retryError).toBeNull()
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('keeps refresh stable while consuming the newest fetcher without effect loops', async () => {
    const fetcher = scripted([fixtureView()])
    const next = scripted([fixtureView({ savedRevision: 'rev-new' })])
    const { result, rerender } = renderHook(({ f }) => useLayoutStatus(f, okRetrier(), 'wf'), { initialProps: { f: fetcher } })
    const refresh = result.current.refresh
    await advance(0)
    rerender({ f: next })
    expect(result.current.refresh).toBe(refresh)
    expect(next).not.toHaveBeenCalled()
    act(() => refresh())
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-new')
  })

  it('keeps an acknowledged minimum for each revision after other revisions are retried', async () => {
    const a = fixtureView({ displayStatus: 'failed' })
    const b = fixtureView({ savedRevision: 'rev-b', displayStatus: 'failed' })
    const fetcher = scripted([
      a, fixtureView({ jobGeneration: 2 }), b,
      fixtureView({ savedRevision: 'rev-b', jobGeneration: 2 }), a,
      fixtureView({ jobGeneration: 2 }),
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    await advance(0)
    await act(async () => { await result.current.retry() })
    act(() => result.current.refresh())
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-b')
    await act(async () => { await result.current.retry() })
    act(() => result.current.refresh())
    await advance(0)
    expect(result.current.view?.savedRevision).toBe('rev-b')
    await advance(POLL_INTERVAL_MS)
    expect(result.current.view?.savedRevision).toBe('rev-1')
    expect(result.current.view?.jobGeneration).toBe(2)
  })

  it.each([2, 3])('accepts a valid RetryAck without regressing concurrent GET generation %i', async (generation) => {
    const ack = deferred<RetryAck>()
    const retrier = vi.fn((revision: string) => {
      expect(revision).toBe('rev-1')
      return ack.promise
    })
    const afterAck = deferred()
    const fetcher = scripted([
      fixtureView({ displayStatus: 'failed' }),
      fixtureView({ displayStatus: 'pending', jobGeneration: generation }),
      afterAck,
    ])
    const { result } = renderHook(() => useLayoutStatus(fetcher, retrier, 'wf'))
    await advance(0)
    let retry!: Promise<void>
    act(() => { retry = result.current.retry() })
    act(() => result.current.refresh())
    await advance(0)
    await act(async () => {
      ack.resolve({ savedRevision: 'rev-1', jobGeneration: 2, state: 'pending', displayStatus: 'pending' })
    })
    expect(result.current.view?.jobGeneration).toBe(generation)
    await act(async () => {
      afterAck.resolve(fixtureView({ jobGeneration: generation, resultGeneration: generation }))
      await retry
    })
    expect(result.current.retryError).toBeNull()
    expect(result.current.view).toEqual(fixtureView({ jobGeneration: generation, resultGeneration: generation }))
  })

  it('ignores in-flight resolutions and removes focus/visibility listeners after unmount', async () => {
    const slow = deferred()
    const fetcher = scripted([slow])
    const { unmount } = renderHook(() => useLayoutStatus(fetcher, okRetrier(), 'wf'))
    unmount()
    await act(async () => { slow.resolve(view('arranging')) })
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
    await advance(MAX_BACKOFF_MS)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })
})
