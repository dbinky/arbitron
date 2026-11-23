import { describe, expect, it } from 'vitest'
import { execStatus, health, iso, remote, runner, source } from '../api/runners.fixtures'
import {
  buildAlerts,
  buildModelGroups,
  causeLabel,
  eventLabel,
  formatBytes,
  formatPercent,
  groupKey,
  plural,
  RECOVERY_DISCLOSURE,
  runnerAcceptsWork,
  sourceStatus,
} from './poolModel'

describe('groupKey', () => {
  it('matches the server ModelGroup string form', () => {
    expect(groupKey('anthropic_subscription', '', 'anthropic')).toBe('anthropic_subscription:anthropic')
    expect(groupKey('server_connection', 'conn-1', 'openai')).toBe('server_connection:conn-1:openai')
    expect(groupKey('lunaroute', 'lr', 'openai')).toBe('lunaroute:lr:openai')
    expect(groupKey('none', '', '')).toBe('none')
  })
})

describe('sourceStatus precedence', () => {
  it.each([
    [{ auth: 'logged_out', capacity: 'exhausted' }, 'logged_out'],
    [{ auth: 'ok', capacity: 'exhausted', congested: true }, 'exhausted'],
    [{ auth: 'ok', capacity: 'probation' }, 'probation'],
    [{ auth: 'ok', capacity: 'unknown' }, 'unknown'],
    [{ auth: 'unknown', capacity: 'available' }, 'unknown'],
    [{ auth: 'ok', capacity: 'available', congested: true }, 'pressure'],
    [{ auth: 'ok', capacity: 'available' }, 'healthy'],
  ] as const)('%o → %s', (h, expected) => {
    expect(sourceStatus(source({ health: health(h) }))).toBe(expected)
  })
})

describe('runnerAcceptsWork', () => {
  it('excludes cordoned, draining, incompatible and non-online runners; chat-only still accepts chat', () => {
    expect(runnerAcceptsWork(runner())).toBe(true)
    expect(runnerAcceptsWork(runner({ cordoned: true }))).toBe(false)
    expect(runnerAcceptsWork(runner({ draining: true }))).toBe(false)
    expect(runnerAcceptsWork(runner({ liveness: 'suspect' }))).toBe(false)
    expect(runnerAcceptsWork(runner({ compat: { state: 'incompatible', reason: 'x' } }))).toBe(false)
    expect(runnerAcceptsWork(runner({ compat: { state: 'chat_only', reason: 'root mismatch' } }))).toBe(true)
  })
})

describe('buildModelGroups', () => {
  it('excludes unavailable capability and unknown health from healthy counts without inferring logout', () => {
    const unavailable = source({ unavailableReason: 'harness_unsupported' })
    expect(sourceStatus(unavailable)).toBe('unavailable')
    const cards = buildModelGroups([
      runner({ sources: [unavailable] }),
      remote('unknown-auth', { sources: [source({ health: health({ auth: 'unknown' }) })] }),
      remote('unknown-capacity', { sources: [source({ health: health({ capacity: 'unknown' }) })] }),
    ], [])
    expect(cards[0]).toMatchObject({ healthy: 0, total: 3 })
    expect(buildAlerts([runner({ sources: [unavailable] })], [])).toEqual([])
  })

  it('keys cards by credential kind AND family and keeps LunaRoute families apart', () => {
    const lr = source({ id: 'mac-a/lunaroute/lr', kind: 'lunaroute', credentialRef: 'lr', families: ['anthropic', 'openai'] })
    const cards = buildModelGroups([runner(), remote('mac-a', { sources: [lr] })], [])
    expect(cards.map((c) => [c.key, c.label])).toEqual([
      ['anthropic_subscription:anthropic', 'Anthropic subscription · anthropic'],
      ['lunaroute:lr:anthropic', 'LunaRoute (lr) · anthropic'],
      ['lunaroute:lr:openai', 'LunaRoute (lr) · openai'],
    ])
  })

  it('counts only serviceable sources on runners accepting work as healthy; pressure stays serviceable', () => {
    const cards = buildModelGroups(
      [
        runner(),
        remote('mac-a', { cordoned: true }),
        remote('mac-b', { sources: [source({ id: 'mac-b/anthropic_subscription/-', health: health({ congested: true }) })] }),
        remote('mac-c', { sources: [source({ id: 'mac-c/anthropic_subscription/-', health: health({ capacity: 'exhausted', resetAt: iso(90_000) }) })] }),
        remote('mac-d', { sources: [source({ id: 'mac-d/anthropic_subscription/-', health: health({ capacity: 'exhausted', resetAt: iso(30_000) }) })] }),
      ],
      [],
    )
    expect(cards).toHaveLength(1)
    expect(cards[0]).toMatchObject({ healthy: 2, total: 5, pressure: true, nextReset: iso(30_000) })
    expect(cards[0].members.map((m) => [m.runnerId, m.status, m.eligible])).toEqual([
      ['builtin', 'healthy', true],
      ['mac-a', 'healthy', false],
      ['mac-b', 'pressure', true],
      ['mac-c', 'exhausted', true],
      ['mac-d', 'exhausted', true],
    ])
  })

  it('counts in-flight and queued executions of the group and spreads in-flight work per runner', () => {
    const cards = buildModelGroups(
      [runner(), remote('mac-a')],
      [
        execStatus({ jobId: 'j1', runnerId: 'mac-a' }),
        execStatus({ jobId: 'j2', runnerId: 'mac-a', state: 'moving_workspace' }),
        execStatus({ jobId: 'j3', runnerId: 'mac-z' }),
        execStatus({ jobId: 'j4', state: 'waiting_capacity', runnerId: undefined }),
        execStatus({ jobId: 'j5', state: 'queued_slot', runnerId: 'builtin' }),
        execStatus({ jobId: 'j6', group: 'openai_subscription:openai' }),
      ],
    )
    expect(cards[0]).toMatchObject({ inFlight: 3, queued: 2 })
    expect(cards[0].spread).toEqual([
      { runnerId: 'builtin', runnerName: 'builtin', inFlight: 0 },
      { runnerId: 'mac-a', runnerName: 'mac-a', inFlight: 2 },
      { runnerId: 'mac-z', runnerName: 'mac-z', inFlight: 1 },
    ])
  })

  it('groups slot-only process sources under "none"', () => {
    const cards = buildModelGroups([runner({ sources: [source({ id: 'builtin/none/-', kind: 'none', families: [] })] })], [])
    expect(cards.map((c) => [c.key, c.label])).toEqual([['none', 'Process steps (no credential)']])
  })
})

describe('buildAlerts', () => {
  it('a healthy one-runner pool has no alerts; a slot wait is not an alert', () => {
    expect(buildAlerts([runner()], [execStatus({ state: 'queued_slot' })])).toEqual([])
  })

  it('raises logged-out, incompatible, chat-only, down-runner and blocked-drain alerts', () => {
    const alerts = buildAlerts(
      [
        runner(),
        remote('mac-a', { sources: [source({ id: 'mac-a/anthropic_subscription/-', health: health({ auth: 'logged_out' }) })] }),
        remote('mac-b', { compat: { state: 'incompatible', reason: 'protocol 1 is older than 2' } }),
        remote('mac-c', { compat: { state: 'chat_only', reason: 'workspace root /w differs from /srv/w' } }),
        remote('mac-d', { liveness: 'lost' }),
        remote('mac-e', { draining: true, drain: { activeSteps: 0, homedWorkspaces: 1, sessions: 0, complete: false, blocked: [{ kind: 'workspace', runId: null, group: 'g', reason: 'no capacity' }] } }),
      ],
      [],
    )
    expect(alerts.map((a) => [a.kind, a.severity, a.title, a.runId])).toEqual([
      ['logged_out', 'error', 'mac-a: Anthropic subscription is logged out', null],
      ['incompatible', 'error', 'mac-b is incompatible', null],
      ['chat_only', 'warning', 'mac-c runs chat steps only', null],
      ['runner_down', 'error', 'mac-d is lost', null],
      ['drain_blocked', 'error', 'Drain of mac-e is blocked', null],
    ])
    expect(alerts[1].detail).toContain('protocol 1 is older than 2')
    expect(alerts[2].detail).toContain('workspace root /w differs from /srv/w')
  })

  it('raises waiting-capacity and recovery alerts from execution statuses', () => {
    const alerts = buildAlerts(
      [runner()],
      [
        execStatus({ jobId: 'j1', state: 'waiting_capacity', reason: 'all anthropic subscriptions exhausted', nextReset: iso(60_000) }),
        execStatus({ jobId: 'j2', runId: 'run-2', state: 'recovering' }),
      ],
    )
    expect(alerts).toEqual([
      expect.objectContaining({ kind: 'waiting_capacity', runId: 'run-1', resetAt: iso(60_000), detail: 'all anthropic subscriptions exhausted · model claude-opus-4-1' }),
      expect.objectContaining({ kind: 'recovering', runId: 'run-2', detail: RECOVERY_DISCLOSURE }),
    ])
  })
})

describe('labels', () => {
  it('explains event causes', () => {
    expect(causeLabel({ kind: 'operator', actor: 'alice', outcome: '' })).toBe('by alice')
    expect(causeLabel({ kind: 'outcome', actor: '', outcome: 'exhausted' })).toBe('outcome: subscription exhausted')
    expect(causeLabel({ kind: 'outcome', actor: '', outcome: 'fenced' })).toBe('outcome: rolled back to the last committed checkpoint')
    expect(causeLabel({ kind: 'liveness', actor: '', outcome: '' })).toBe('liveness check')
    expect(causeLabel({ kind: 'mystery', actor: '', outcome: '' })).toBe('mystery')
    expect(eventLabel('source.logged_out')).toBe('source logged out')
  })

  it('formats bytes, percentages and plurals; unknown metrics render as a dash', () => {
    expect(formatBytes(8 * 1024 ** 3)).toBe('8.0 GiB')
    expect(formatBytes(null)).toBe('—')
    expect(formatPercent(41.6)).toBe('42%')
    expect(formatPercent(null)).toBe('—')
    expect(plural(1, 'session')).toBe('1 session')
    expect(plural(2, 'session')).toBe('2 sessions')
  })
})
