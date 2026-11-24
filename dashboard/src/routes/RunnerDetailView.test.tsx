import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, detail, health, installFetch, iso, poolEvent, reply, source, telemetry } from '../api/runners.fixtures'
import type { RunnerDetail } from '../api/types'
import { FakeWebSocket, POOL_URLS, dropSystemSockets, emitSystem, flush, openSystemSockets, wsPoolEvent } from '../hooks/poolSocket.fixtures'
import { POOL_EVENT_DEBOUNCE_MS, POOL_POLL_MS } from '../hooks/useLiveSnapshot'
import RunnerDetailView from './RunnerDetailView'

const DETAIL_URL = '/api/v1/runners/mac-a'
const SRC = 'mac-a/anthropic_subscription/-'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function full(over: Partial<RunnerDetail> = {}): RunnerDetail {
  return detail({
    labels: { rack: 'desk' },
    sources: [source({ id: SRC, health: health({ lastProbeAt: iso(-30_000), lastOutcome: 'exhausted', headroom: 0.25 }) })],
    recentTelemetry: [telemetry({ sampledAt: iso(-20_000), cpuPercent: 10 }), telemetry({ sampledAt: iso(-10_000), cpuPercent: 30 }), telemetry({ sampledAt: iso(-5_000), cpuPercent: 42 })],
    assignments: [
      { jobId: 'j1', runId: 'run-1', nodeId: 'review', state: 'running', sourceId: SRC, hidden: false },
      { jobId: 'j2', runId: null, nodeId: 'secret-node', state: 'running', sourceId: SRC, hidden: true },
    ],
    homedWorkspaces: 2,
    homes: [
      { runId: 'run-1', hidden: false, generation: 3, dirty: true },
      { runId: null, hidden: true, generation: 1, dirty: false },
    ],
    sessions: [{ runId: 'run-5', nodeId: 'chat', hidden: false, generation: 2 }],
    ...over,
  })
}

async function renderDetail(routes: Record<string, unknown>) {
  const f = installFetch({ [POOL_URLS.events]: [], ...routes })
  render(
    <MemoryRouter initialEntries={['/pool/runners/mac-a']}>
      <Routes>
        <Route path="/pool/runners/:id" element={<RunnerDetailView />} />
      </Routes>
    </MemoryRouter>,
  )
  await flush()
  return f
}
const section = (name: string) => screen.getByRole('region', { name })
const stat = (name: string) => document.querySelector(`[data-stat="${name}"]`)?.textContent

describe('RunnerDetailView — happy path', () => {
  it('shows telemetry sparklines and current stats', async () => {
    await renderDetail({ [DETAIL_URL]: full() })
    expect(document.querySelector('[data-view="arbitron-runner-detail"]')).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'mac-a' })).toBeInTheDocument()
    const t = section('Telemetry')
    expect(within(t).getByRole('img', { name: 'CPU trend' })).toBeInTheDocument()
    expect(t.querySelector('[data-sparkline="CPU"] polyline')?.getAttribute('points')).toBe('0,28.8 60,22.4 120,18.6')
    expect(stat('cpu')).toBe('42%')
    expect(stat('memory')).toBe('8.0 GiB / 32.0 GiB')
    expect(stat('pressure')).toBe('normal')
    expect(stat('load')).toBe('30%')
    expect(stat('disk')).toBe('100.0 GiB')
    expect(stat('thermal')).toBe('nominal')
    expect(stat('uptime')).toBe('2h 0m')
  })

  it('shows hardware, versions, last contact and a health check per source', async () => {
    await renderDetail({ [DETAIL_URL]: full() })
    expect(stat('os')).toBe('darwin')
    expect(stat('cores')).toBe('10')
    expect(stat('ram')).toBe('32.0 GiB')
    expect(stat('runner-version')).toBe('1.0.0')
    expect(stat('harness-claude-code')).toBe('2.1.0')
    expect(stat('step-limit')).toBe('8')
    expect(stat('labels')).toBe('rack=desk')
    expect(stat('last-contact')).toBe('1s ago')
    const check = document.querySelector<HTMLElement>(`[data-health-check="${SRC}"]`)!
    expect(check.querySelector(`[data-source="${SRC}"]`)).not.toBeNull()
    expect(check).toHaveTextContent('last check 30s ago')
    expect(check).toHaveTextContent('last outcome: subscription exhausted')
    expect(check).toHaveTextContent('headroom 25%')
  })

  it('links assignments, homed workspaces and pinned sessions to their runs; hidden runs get no link', async () => {
    await renderDetail({ [DETAIL_URL]: full() })
    const a = section('Assignments')
    expect(within(a).getByRole('link', { name: 'review' })).toHaveAttribute('href', '/instances/run-1')
    expect(a).toHaveTextContent('hidden run')
    expect(a).not.toHaveTextContent('secret-node')
    expect(stat('homed')).toBe('2')
    const homes = a.querySelectorAll('[data-home]')
    expect(homes).toHaveLength(2)
    expect(within(homes[0] as HTMLElement).getByRole('link')).toHaveAttribute('href', '/instances/run-1')
    expect(homes[0]).toHaveTextContent('generation 3 · unsynced changes')
    expect(within(homes[1] as HTMLElement).queryByRole('link')).toBeNull()
    const pin = a.querySelector<HTMLElement>('[data-session-pin]')!
    expect(within(pin).getByRole('link', { name: 'chat' })).toHaveAttribute('href', '/instances/run-5')
    expect(pin).toHaveTextContent('generation 2')
  })
})

describe('RunnerDetailView — failure', () => {
  it('a draining runner shows progress and the capacity blocking completion', async () => {
    await renderDetail({
      [DETAIL_URL]: full({
        draining: true,
        cordoned: true,
        drain: { activeSteps: 0, homedWorkspaces: 1, sessions: 0, complete: false, blocked: [{ kind: 'workspace', runId: 'run-1', group: 'anthropic_subscription:anthropic', reason: 'no runner with anthropic capacity' }] },
      }),
    })
    const d = section('Drain')
    expect(d).toHaveTextContent('Draining · 0 active steps · 1 homed workspace · 0 sessions')
    expect(d.querySelector('[data-drain-blocker="workspace"]')).toHaveTextContent('no runner with anthropic capacity')
    expect(screen.getByText('cordoned')).toBeInTheDocument()
  })

  it('an incompatible runner shows its explanation', async () => {
    await renderDetail({ [DETAIL_URL]: full({ compat: { state: 'incompatible', reason: 'protocol 1 is older than 2' } }) })
    expect(document.querySelector('[data-compat-reason]')).toHaveTextContent('protocol 1 is older than 2')
  })
})

describe('RunnerDetailView — live and errors', () => {
  it('an invalidation for this runner reloads the header from the changed detail', async () => {
    const f = await renderDetail({ [DETAIL_URL]: full() })
    openSystemSockets()
    await flush() // the connect refresh (P5 T2)
    expect(screen.queryByText('cordoned')).toBeNull()
    const reads = f.count('GET', DETAIL_URL)
    const e1 = poolEvent({ id: 'e1', kind: 'runner.cordoned', runnerId: 'mac-a', at: iso(1_000), cause: { kind: 'operator', actor: 'alice', outcome: '' } })
    f.routes[DETAIL_URL] = full({
      cordoned: true,
      sources: [source({ id: SRC, unavailableReason: 'harness_missing' })],
      assignments: [{ jobId: 'new-job', runId: 'run-new', nodeId: 'new-step', state: 'running', sourceId: SRC, hidden: false }],
    })
    f.routes[POOL_URLS.events] = [e1]
    emitSystem(wsPoolEvent(e1))
    expect(screen.queryByText('cordoned')).toBeNull() // never patched from the event
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', DETAIL_URL)).toBe(reads + 1)
    expect(screen.getByText('cordoned')).toBeInTheDocument()
    expect(section('Recent events')).toHaveTextContent('runner cordoned · by alice')
    expect(section('Machine')).toHaveTextContent('harness_missing')
    expect(within(section('Assignments')).getByRole('link', { name: 'new-step' })).toHaveAttribute('href', '/instances/run-new')
    expect(within(section('Assignments')).queryByRole('link', { name: 'review' })).toBeNull()
  })

  it('a WebSocket drop shows the stale banner and polling resumes', async () => {
    const f = await renderDetail({ [DETAIL_URL]: full() })
    openSystemSockets()
    dropSystemSockets()
    expect(screen.getByTestId('runner-stale')).toBeInTheDocument()
    const before = f.count('GET', DETAIL_URL)
    await flush(POOL_POLL_MS)
    expect(f.count('GET', DETAIL_URL)).toBe(before + 1)
  })

  it('a missing runner shows the error with retry', async () => {
    const routes: Record<string, unknown> = { [DETAIL_URL]: reply(404, { error: 'runner not found' }) }
    await renderDetail(routes)
    expect(screen.getByRole('alert')).toHaveTextContent('API 404: runner not found')
    expect(screen.getByRole('link', { name: 'Back to pool' })).toHaveAttribute('href', '/pool')
    installFetch({ [POOL_URLS.events]: [], [DETAIL_URL]: full() })
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await flush()
    expect(screen.getByRole('heading', { name: 'mac-a' })).toBeInTheDocument()
  })
})

describe('RunnerDetailView — authoritative health', () => {
  it('renders unavailableReason rather than a healthy source', async () => {
    await renderDetail({ [DETAIL_URL]: full({ sources: [source({ id: SRC, unavailableReason: 'harness_unsupported' })] }) })
    const chip = document.querySelector(`[data-source="${SRC}"]`)!
    expect(chip).toHaveTextContent('harness_unsupported')
    expect(chip).toHaveAttribute('data-status', 'unavailable')
    expect(chip).not.toHaveTextContent('logged out')
  })

  it('marks a REST refresh failure stale even while the socket is connected', async () => {
    const f = await renderDetail({ [DETAIL_URL]: full() })
    openSystemSockets()
    await flush()
    f.routes[DETAIL_URL] = reply(503, { error: 'snapshot unavailable' })
    emitSystem(wsPoolEvent(poolEvent({ kind: 'runner.cordoned', runnerId: 'mac-a' })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(screen.getByRole('alert')).toHaveTextContent('snapshot unavailable')
    expect(screen.getByTestId('runner-stale')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'mac-a' })).toBeInTheDocument()
  })

  it('does not turn exhausted capacity green when the countdown expires', async () => {
    await renderDetail({ [DETAIL_URL]: full({ sources: [source({ id: SRC, health: health({ capacity: 'exhausted', resetAt: iso(1_000) }) })] }) })
    openSystemSockets()
    await flush(2_000)
    expect(document.querySelector(`[data-source="${SRC}"]`)).toHaveAttribute('data-status', 'exhausted')
  })
})

describe('RunnerDetailView — measurement freshness', () => {
  for (const sampledAt of [null, '0001-01-01T00:00:00Z', 'invalid', iso(60_000), iso(-60_000)]) {
    it(`does not present unavailable sample ${sampledAt} as current or as a trend`, async () => {
      const sample = telemetry({ sampledAt, cpuPercent: 99 })
      await renderDetail({ [DETAIL_URL]: full({ telemetry: sample, recentTelemetry: [sample, sample] }) })
      openSystemSockets()
      await flush()
      expect(section('Telemetry')).toHaveTextContent('Telemetry unavailable')
      expect(section('Telemetry').querySelectorAll('polyline')).toHaveLength(0)
      expect(stat('cores')).toBe('—')
      expect(stat('ram')).toBe('—')
    })
  }
})

describe('RunnerDetailView — edge cases', () => {
  it('telemetry "unavailable" renders gracefully', async () => {
    await renderDetail({ [DETAIL_URL]: full({ telemetry: null, recentTelemetry: [] }) })
    const t = section('Telemetry')
    expect(t).toHaveTextContent('Telemetry unavailable')
    expect(within(t).getAllByText('Not enough samples').length).toBeGreaterThan(0)
    expect(stat('cores')).toBe('—')
    expect(stat('runner-version')).toBe('1.0.0')
  })

  it('a partial sample renders unknown metrics as a dash and gaps as split lines', async () => {
    await renderDetail({
      [DETAIL_URL]: full({
        telemetry: telemetry({ thermal: null, memPressure: null }),
        recentTelemetry: [telemetry({ cpuPercent: 10 }), telemetry({ cpuPercent: 20 }), telemetry({ cpuPercent: null }), telemetry({ cpuPercent: 30 }), telemetry({ cpuPercent: 40 })],
      }),
    })
    expect(stat('thermal')).toBe('—')
    expect(stat('pressure')).toBe('—')
    expect(document.querySelectorAll('[data-sparkline="CPU"] polyline')).toHaveLength(2)
  })

  it('a source never probed says so', async () => {
    await renderDetail({ [DETAIL_URL]: full({ sources: [source({ id: SRC, health: health({ lastProbeAt: null, lastOutcome: '', headroom: null }) })] }) })
    const check = document.querySelector<HTMLElement>(`[data-health-check="${SRC}"]`)!
    expect(check).toHaveTextContent('never checked')
    expect(check).not.toHaveTextContent('headroom')
  })
})
