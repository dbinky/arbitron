import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, execStatus, health, iso, poolEvent, remote, reply, row, runner, source, telemetry } from '../api/runners.fixtures'
import { RECOVERY_DISCLOSURE } from '../components/poolModel'
import {
  FakeWebSocket,
  POOL_URLS,
  dropSystemSockets,
  emitSystem,
  flush,
  installPool,
  openSystemSockets,
  wsPoolEvent,
  type PoolWorld,
} from '../hooks/poolSocket.fixtures'
import { POOL_EVENT_DEBOUNCE_MS, POOL_POLL_MS } from '../hooks/useLiveSnapshot'
import PoolView from './PoolView'

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

const world = (over: Partial<PoolWorld> = {}): PoolWorld => ({ runners: [runner()], events: [], running: [], waiting: [], ...over })
async function renderPool(w: PoolWorld) {
  const f = installPool(w)
  render(
    <MemoryRouter>
      <PoolView />
    </MemoryRouter>,
  )
  await flush()
  return f
}
const card = (id: string) => document.querySelector<HTMLElement>(`[data-runner-card="${id}"]`)!
const group = (key: string) => document.querySelector<HTMLElement>(`[data-group="${key}"]`)!
const fact = (el: HTMLElement, name: string) => el.querySelector(`[data-fact="${name}"]`)?.textContent
const alerts = (kind?: string) => [...document.querySelectorAll<HTMLElement>(kind ? `[data-alert="${kind}"]` : '[data-alert]')]
const ANTHROPIC = 'anthropic_subscription:anthropic'

describe('PoolView — happy path', () => {
  it('renders a one-runner local pool with healthy sources', async () => {
    await renderPool(world())
    openSystemSockets()
    await flush() // every (re)connect triggers an authoritative refresh (P5 T2); stale clears when it succeeds
    expect(document.querySelector('[data-view="arbitron-pool-view"]')).not.toBeNull()
    expect(screen.getByTestId('local-mode')).toHaveTextContent('Local mode')
    const b = card('builtin')
    expect(within(b).getByRole('link', { name: 'builtin' })).toHaveAttribute('href', '/pool/runners/builtin')
    expect(b.getAttribute('data-liveness')).toBe('online')
    expect(fact(b, 'version')).toBe('runner 1.0.0 · claude-code 2.1.0')
    expect(fact(b, 'heartbeat')).toBe('1s ago')
    expect(fact(b, 'slots')).toBe('0/8')
    expect(fact(b, 'telemetry')).toBe('CPU 42% · Mem 8.0 GiB / 32.0 GiB')
    expect(fact(b, 'homed')).toBe('0')
    expect(b.querySelector('[data-source="builtin/anthropic_subscription/-"]')?.getAttribute('data-status')).toBe('healthy')
    const g = group(ANTHROPIC)
    expect(fact(g, 'healthy')).toBe('1/1')
    expect(fact(g, 'next-reset')).toBe('—')
    expect(alerts()).toHaveLength(0)
    expect(screen.getByText('No alerts')).toBeInTheDocument()
    expect(screen.queryByTestId('pool-stale')).toBeNull()
  })
})

describe('PoolView — success', () => {
  it('invalidation reload updates runner cards from the changed server reply', async () => {
    const w = world({ runners: [runner(), remote('mac-a')] })
    const f = await renderPool(w)
    openSystemSockets()
    await flush()
    expect(screen.queryByTestId('local-mode')).toBeNull()
    expect(card('mac-a').querySelector('[data-badge="cordoned"]')).toBeNull()
    const reads = f.count('GET', POOL_URLS.runners)
    w.runners = [runner(), remote('mac-a', { cordoned: true })]
    w.events = [poolEvent({ id: 'e1', kind: 'runner.cordoned', runnerId: 'mac-a', at: iso(1_000) })]
    emitSystem(wsPoolEvent(poolEvent({ id: 'e1', kind: 'runner.cordoned', runnerId: 'mac-a', at: iso(1_000) })))
    // The event only invalidates: no client patch of the card before the authoritative reload.
    expect(card('mac-a').querySelector('[data-badge="cordoned"]')).toBeNull()
    expect(document.querySelector('[data-event="e1"]')).toHaveTextContent('runner cordoned')
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(reads + 1)
    expect(card('mac-a').querySelector('[data-badge="cordoned"]')).not.toBeNull()
  })

  it('countdowns tick from the absolute reset time (local mode still shows capacity status)', async () => {
    await renderPool(world({ runners: [runner({ sources: [source({ health: health({ capacity: 'exhausted', resetAt: iso(65_000) }) })] })] }))
    const chip = () => card('builtin').querySelector('[data-source]')!
    expect(chip().getAttribute('data-status')).toBe('exhausted')
    expect(chip()).toHaveTextContent('resets in 1m 5s')
    expect(fact(group(ANTHROPIC), 'next-reset')).toBe('resets in 1m 5s')
    await flush(1_000)
    expect(chip()).toHaveTextContent('resets in 1m 4s')
    expect(fact(group(ANTHROPIC), 'next-reset')).toBe('resets in 1m 4s')
  })

  it('model-group cards count healthy sources, in-flight and queued work, and spread it per runner', async () => {
    await renderPool(
      world({
        runners: [runner(), remote('mac-a'), remote('mac-b', { sources: [source({ id: 'mac-b/anthropic_subscription/-', health: health({ capacity: 'exhausted', resetAt: iso(3_600_000) }) })] })],
        running: [
          row('run-1', [
            execStatus({ jobId: 'j1', nodeId: 'n1', runnerId: 'mac-a' }),
            execStatus({ jobId: 'j2', nodeId: 'n2', runnerId: 'mac-a' }),
            execStatus({ jobId: 'j3', nodeId: 'n3', runnerId: 'builtin' }),
            execStatus({ jobId: 'j4', nodeId: 'n4', state: 'queued_slot', runnerId: undefined }),
          ]),
        ],
      }),
    )
    const g = group(ANTHROPIC)
    expect(fact(g, 'healthy')).toBe('2/3')
    expect(fact(g, 'inflight')).toBe('3')
    expect(fact(g, 'queued')).toBe('1')
    expect(fact(g, 'next-reset')).toBe('resets in 1h 0m')
    const spread = [...g.querySelectorAll('[data-spread-runner]')].map((s) => [s.getAttribute('data-spread-runner'), s.getAttribute('data-count')])
    expect(spread).toEqual([
      ['builtin', '1'],
      ['mac-a', '2'],
      ['mac-b', '0'],
    ])
  })
})

describe('PoolView — failure', () => {
  it('a logged-out source raises an alert', async () => {
    await renderPool(world({ runners: [runner(), remote('mac-a', { sources: [source({ id: 'mac-a/anthropic_subscription/-', health: health({ auth: 'logged_out' }) })] })] }))
    const [a] = alerts('logged_out')
    expect(a).toHaveTextContent('mac-a: Anthropic subscription is logged out')
    expect(a).toHaveTextContent('returns to service automatically once login is restored')
    expect(card('mac-a').querySelector('[data-source]')?.getAttribute('data-status')).toBe('logged_out')
  })

  it('an incompatible runner shows its explanation and is not counted healthy', async () => {
    const reason = 'claude-code 1.0.3 is below the supported 2.0.0'
    await renderPool(world({ runners: [runner(), remote('mac-a', { compat: { state: 'incompatible', reason } })] }))
    expect(alerts('incompatible')[0]).toHaveTextContent('mac-a is incompatible')
    expect(alerts('incompatible')[0]).toHaveTextContent(reason)
    expect(card('mac-a').querySelector('[data-compat-reason]')).toHaveTextContent(reason)
    expect(fact(group(ANTHROPIC), 'healthy')).toBe('1/2')
  })

  it('a draining runner shows progress and the capacity blocking completion', async () => {
    await renderPool(
      world({
        runners: [
          runner(),
          remote('mac-a', {
            draining: true,
            cordoned: true,
            drain: { activeSteps: 1, homedWorkspaces: 2, sessions: 1, complete: false, blocked: [{ kind: 'workspace', runId: 'run-7', group: ANTHROPIC, reason: 'no eligible runner with anthropic capacity' }] },
          }),
        ],
      }),
    )
    const d = card('mac-a').querySelector<HTMLElement>('[data-drain]')!
    expect(d).toHaveTextContent('1 active step · 2 homed workspaces · 1 session')
    expect(d.querySelector('[data-drain-blocker]')).toHaveTextContent(`workspace blocked: no eligible runner with anthropic capacity (${ANTHROPIC})`)
    const a = alerts('drain_blocked')[0]
    expect(a).toHaveTextContent('Drain of mac-a is blocked')
    expect(within(a).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/run-7')
  })

  it.each([
    ['suspect', 'No contact for about 30 seconds'],
    ['lost', 'its work is being recovered on other runners'],
    ['offline', 'Disconnected.'],
  ] as const)('a %s runner raises an alert', async (liveness, text) => {
    await renderPool(world({ runners: [runner(), remote('mac-a', { liveness })] }))
    const a = alerts('runner_down')[0]
    expect(a).toHaveTextContent(`mac-a is ${liveness}`)
    expect(a).toHaveTextContent(text)
    expect(card('mac-a').getAttribute('data-liveness')).toBe(liveness)
  })

  it('runs waiting · capacity show their reason, exact model and countdown, linked to the run', async () => {
    await renderPool(
      world({
        running: [row('run-1', [execStatus({ state: 'waiting_capacity', runnerId: undefined, reason: 'all anthropic subscriptions exhausted', nextReset: iso(120_000) })])],
      }),
    )
    const a = alerts('waiting_capacity')[0]
    expect(a).toHaveTextContent('Run waiting · capacity (review)')
    expect(a).toHaveTextContent('all anthropic subscriptions exhausted · model claude-opus-4-1')
    expect(a).toHaveTextContent('resets in 2m 0s')
    expect(within(a).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/run-1')
  })

  it('recovering work carries the durable recovery disclosure (idle interactive waits included)', async () => {
    await renderPool(world({ waiting: [row('run-2', [execStatus({ runId: 'run-2', state: 'recovering' })], { state: 'waiting' })] }))
    const a = alerts('recovering')[0]
    expect(a).toHaveTextContent(RECOVERY_DISCLOSURE)
    expect(within(a).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/instances/run-2')
  })
})

describe('PoolView — error', () => {
  it('a WebSocket drop shows the stale banner and polling resumes', async () => {
    const f = await renderPool(world())
    openSystemSockets()
    await flush()
    expect(screen.queryByTestId('pool-stale')).toBeNull()
    dropSystemSockets()
    expect(screen.getByTestId('pool-stale')).toHaveTextContent('Live updates disconnected')
    const before = f.count('GET', POOL_URLS.runners)
    await flush(POOL_POLL_MS)
    expect(f.count('GET', POOL_URLS.runners)).toBe(before + 1)
    openSystemSockets()
    await flush()
    expect(screen.queryByTestId('pool-stale')).toBeNull()
  })

  it('a failed load shows an inline error with retry', async () => {
    const w = world({ runners: reply(503, { error: 'pool unavailable' }) })
    await renderPool(w)
    expect(screen.getByRole('alert')).toHaveTextContent('API 503: pool unavailable')
    w.runners = [runner()]
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await flush()
    expect(card('builtin')).not.toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('PoolView — edge cases', () => {
  it('renders telemetry "unavailable" gracefully', async () => {
    await renderPool(world({ runners: [runner({ telemetry: null }), remote('mac-a', { telemetry: telemetry({ cpuPercent: null, memUsedBytes: null }) })] }))
    expect(fact(card('builtin'), 'telemetry')).toBe('Telemetry unavailable')
    expect(fact(card('builtin'), 'slots')).toBe('0/8')
    expect(fact(card('mac-a'), 'telemetry')).toBe('CPU — · Mem — / 32.0 GiB')
  })

  it('LunaRoute busy renders as pressure, never exhaustion', async () => {
    const lr = source({ id: 'mac-a/lunaroute/lr-main', kind: 'lunaroute', credentialRef: 'lr-main', families: ['openai'], backends: ['codex'], health: health({ congested: true, congestedUntil: iso(30_000) }) })
    await renderPool(world({ runners: [runner(), remote('mac-a', { sources: [lr] })] }))
    const chip = card('mac-a').querySelector(`[data-source="${lr.id}"]`)!
    expect(chip.getAttribute('data-status')).toBe('pressure')
    expect(chip).toHaveTextContent('busy · pressure')
    expect(chip).not.toHaveTextContent('exhausted')
    const g = group('lunaroute:lr-main:openai')
    expect(g.getAttribute('data-pressure')).toBe('true')
    expect(fact(g, 'healthy')).toBe('1/1')
    expect(fact(g, 'next-reset')).toBe('—')
    expect(alerts()).toHaveLength(0)
  })

  it('a logged-out and exhausted source is not shown healthy after login returns', async () => {
    const src = source({ id: 'mac-a/anthropic_subscription/-', health: health({ auth: 'logged_out', capacity: 'exhausted', resetAt: iso(600_000) }) })
    const w = world({ runners: [runner(), remote('mac-a', { sources: [src] })] })
    await renderPool(w)
    openSystemSockets()
    await flush()
    expect(alerts('logged_out')).toHaveLength(1)
    // Server now reports login restored but capacity still exhausted; the event alone proves nothing.
    w.runners = [runner(), remote('mac-a', { sources: [{ ...src, health: { ...src.health, auth: 'ok' } }] })]
    emitSystem(wsPoolEvent(poolEvent({ id: 'e9', kind: 'source.recovered', runnerId: 'mac-a', sourceId: src.id, at: iso(1_000), cause: { kind: 'outcome', actor: '', outcome: '' } })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    const chip = card('mac-a').querySelector(`[data-source="${src.id}"]`)!
    expect(chip.getAttribute('data-status')).toBe('exhausted')
    expect(chip).toHaveTextContent('resets in 10m 0s') // useNow ticks per second; the 300 ms debounce has not crossed a tick
    expect(alerts('logged_out')).toHaveLength(0)
    expect(fact(group(ANTHROPIC), 'healthy')).toBe('1/2')
  })

  it('recent events show their cause and never link or reveal hidden runs', async () => {
    await renderPool(
      world({
        events: [
          poolEvent({ id: 'e1', kind: 'runner.cordoned', runnerId: 'builtin', cause: { kind: 'operator', actor: 'alice', outcome: '' } }),
          poolEvent({ id: 'e2', kind: 'job.requeued', runnerId: 'builtin', runId: 'run-3', at: iso(-1_000), cause: { kind: 'outcome', actor: '', outcome: 'exhausted' } }),
          poolEvent({ id: 'e3', kind: 'job.requeued', runnerId: 'builtin', runId: null, at: iso(-2_000), cause: { kind: 'outcome', actor: '', outcome: 'runner_lost' }, detail: { prompt: 'SECRET PROMPT' } }),
        ],
      }),
    )
    const e1 = document.querySelector<HTMLElement>('[data-event="e1"]')!
    expect(e1).toHaveTextContent('runner cordoned')
    expect(e1).toHaveTextContent('by alice')
    const e2 = document.querySelector<HTMLElement>('[data-event="e2"]')!
    expect(e2).toHaveTextContent('outcome: subscription exhausted')
    expect(within(e2).getByRole('link', { name: 'run' })).toHaveAttribute('href', '/instances/run-3')
    const e3 = document.querySelector<HTMLElement>('[data-event="e3"]')!
    expect(e3).toHaveTextContent('outcome: runner lost')
    expect(within(e3).queryByRole('link')).toBeNull()
    expect(document.body).not.toHaveTextContent('SECRET PROMPT')
  })
})

describe('PoolView — authoritative health and freshness', () => {
  it('renders unavailableReason and excludes it from healthy count', async () => {
    await renderPool(world({ runners: [runner({ sources: [source({ unavailableReason: 'harness_not_found' })] })] }))
    const chip = card('builtin').querySelector('[data-source]')!
    expect(chip).toHaveAttribute('data-status', 'unavailable')
    expect(chip).toHaveTextContent('harness_not_found')
    expect(fact(group(ANTHROPIC), 'healthy')).toBe('0/1')
    expect(alerts('logged_out')).toHaveLength(0)
  })

  it('marks REST refresh failure stale while socket is connected', async () => {
    const w = world()
    await renderPool(w)
    openSystemSockets()
    await flush()
    w.runners = reply(503, { error: 'refresh failed' })
    emitSystem(wsPoolEvent(poolEvent({ id: 'refresh-error' })))
    await flush(POOL_EVENT_DEBOUNCE_MS)
    expect(screen.getByRole('alert')).toHaveTextContent('refresh failed')
    expect(screen.getByTestId('pool-stale')).toHaveTextContent('Pool refresh failed')
    expect(screen.getByTestId('pool-stale')).not.toHaveTextContent('disconnected')
    expect(card('builtin')).not.toBeNull()
  })

  it.each(['1970-01-01T00:00:00Z', 'invalid', iso(60_000), iso(-31_000)])(
    'renders zero, invalid, future and expired telemetry as unavailable with a connected socket (%s)',
    async (sampledAt) => {
      await renderPool(world({ runners: [runner({ telemetry: telemetry({ sampledAt }) })] }))
      openSystemSockets()
      await flush()
      expect(screen.queryByTestId('pool-stale')).toBeNull()
      expect(fact(card('builtin'), 'telemetry')).toBe('Telemetry unavailable')
    },
  )

  it('never turns exhausted capacity green when countdown reaches zero', async () => {
    await renderPool(world({ runners: [runner({ sources: [source({ health: health({ capacity: 'exhausted', resetAt: iso(1_000) }) })] })] }))
    openSystemSockets()
    await flush()
    await flush(2_000)
    expect(card('builtin').querySelector('[data-source]')).toHaveAttribute('data-status', 'exhausted')
    expect(fact(group(ANTHROPIC), 'healthy')).toBe('0/1')
    expect(fact(group(ANTHROPIC), 'next-reset')).toContain('awaiting confirmation')
  })
})
