import { afterEach, describe, expect, it, vi } from 'vitest'
import goFixture from './fixtures/runner-http.json'
import { api } from './client'
import { getRunner } from './runners'
import { installFetch, iso } from './runners.fixtures'
import type { WebSocketEvent } from './types'
import {
  decodeExecutionStatus,
  decodeKeyCreate,
  decodeList,
  decodeLiveEvent,
  isPoolEventKind,
  decodePoolEvent,
  decodeRunnerDetail,
  decodeRunnerKey,
  decodeRunnerView,
  decodeSourceHealth,
  decodeTelemetry,
  listOf,
  telemetryAvailable,
} from './runnerDecode'

afterEach(() => vi.unstubAllGlobals())

describe('decodeRunnerView', () => {
  it('decodes the P1 T15 runner DTO field-for-field', () => {
    const wire = {
      id: 'mac-a',
      name: 'mac-a',
      builtIn: false,
      stepLimit: 8,
      labels: { room: 'office' },
      cordoned: true,
      draining: false,
      compat: { state: 'chat_only', reason: 'workspace root /Users/x differs from server /srv/ws' },
      liveness: 'suspect',
      lastContact: iso(-40_000),
      os: 'darwin',
      runnerVersion: '1.2.0',
      harnessVersions: { 'claude-code': '2.1.0' },
      slotsUsed: 3,
      queued: 1,
      homedWorkspaces: 2,
      sources: [
        {
          id: 'mac-a/lunaroute/lr-main',
          kind: 'lunaroute',
          credentialRef: 'lr-main',
          families: ['openai'],
          health: { auth: 'ok', capacity: 'available', congested: true, headroom: 0.4 },
        },
      ],
      telemetry: { sampledAt: iso(-5_000), cpuPercent: 41.5, memUsedBytes: 8e9, memPressure: 'normal', load1Pct: 30, diskFreeBytes: 5e10, thermal: 'nominal', uptimeSeconds: 3_600, cores: 10, ramBytes: 34_359_738_368 },
      drain: null,
    }
    const r = decodeRunnerView(wire)!
    expect(r).toMatchObject({
      id: 'mac-a',
      builtIn: false,
      stepLimit: 8,
      labels: { room: 'office' },
      cordoned: true,
      liveness: 'suspect',
      slotsUsed: 3,
      queued: 1,
      homedWorkspaces: 2,
      runnerVersion: '1.2.0',
      harnessVersions: { 'claude-code': '2.1.0' },
    })
    expect(r.compat).toEqual({ state: 'chat_only', reason: 'workspace root /Users/x differs from server /srv/ws' })
    expect(r.sources).toHaveLength(1)
    expect(r.sources[0]).toMatchObject({ kind: 'lunaroute', credentialRef: 'lr-main', families: ['openai'], backends: [], models: [] })
    expect(r.sources[0].health).toEqual({
      auth: 'ok',
      capacity: 'available',
      resetAt: null,
      headroom: 0.4,
      congested: true,
      congestedUntil: null,
      lastProbeAt: null,
      lastOutcome: '',
    })
    expect(r.telemetry?.cpuPercent).toBe(41.5)
    expect(r.drain).toBeNull()
  })

  it('returns null without an id, so a malformed row never renders as a runner', () => {
    expect(decodeRunnerView({ name: 'ghost' })).toBeNull()
    expect(decodeRunnerView('builtin')).toBeNull()
    expect(decodeRunnerView(null)).toBeNull()
  })

  it('tolerates missing and mistyped fields without inventing health or liveness', () => {
    const r = decodeRunnerView({
      id: 'x',
      stepLimit: 'eight',
      labels: [1],
      sources: [{ id: 's1', kind: 'anthropic_subscription' }, { kind: 'no-id' }, 'junk'],
      telemetry: 'n/a',
      drain: 7,
    })!
    expect(r.name).toBe('x')
    expect(r.stepLimit).toBe(0)
    expect(r.labels).toEqual({})
    expect(r.liveness).toBe('offline')
    expect(r.compat).toEqual({ state: 'compatible', reason: '' })
    expect(r.sources).toHaveLength(1)
    expect(r.sources[0].families).toEqual([])
    expect(r.sources[0].health).toMatchObject({ auth: 'unknown', capacity: 'unknown' })
    expect(r.telemetry).toBeNull()
    expect(r.drain).toBeNull()
  })

  it('treats an unrecognized compatibility state as incompatible and says why', () => {
    const r = decodeRunnerView({ id: 'x', compat: { state: 'maybe' } })!
    expect(r.compat.state).toBe('incompatible')
    expect(r.compat.reason).toBe('unrecognized compatibility state "maybe"')
  })

  it('decodes drain progress with blockers', () => {
    const r = decodeRunnerView({
      id: 'mac-a',
      draining: true,
      drain: { activeSteps: 1, homedWorkspaces: 2, sessions: 0, complete: false, blocked: [{ kind: 'workspace', runId: 'run-9', group: 'anthropic_subscription:anthropic', reason: 'no eligible runner' }, { kind: 'session' }] },
    })!
    expect(r.drain).toEqual({
      activeSteps: 1,
      homedWorkspaces: 2,
      sessions: 0,
      complete: false,
      blocked: [
        { kind: 'workspace', runId: 'run-9', group: 'anthropic_subscription:anthropic', reason: 'no eligible runner' },
        { kind: 'session', runId: null, group: '', reason: '' },
      ],
    })
  })
})

describe('list envelopes', () => {
  it('accepts a bare array or an object wrapping it under the given key', () => {
    expect(listOf([1, 2], 'runners')).toEqual([1, 2])
    expect(listOf({ runners: [3] }, 'runners')).toEqual([3])
    expect(listOf({ items: [3] }, 'runners')).toEqual([])
    expect(listOf(null, 'runners')).toEqual([])
  })

  it('decodeList drops rows that fail to decode', () => {
    expect(decodeList({ runners: [{ id: 'a' }, { nope: 1 }] }, 'runners', decodeRunnerView).map((r) => r.id)).toEqual(['a'])
  })
})

describe('decodeSourceHealth', () => {
  it('fails closed on an absent health object: unknown auth and unknown capacity', () => {
    expect(decodeSourceHealth(undefined)).toMatchObject({ auth: 'unknown', capacity: 'unknown', congested: false, headroom: null })
  })

  it('keeps an absolute reset timestamp and an unknown headroom as null, never 0 or 1', () => {
    const h = decodeSourceHealth({ auth: 'ok', capacity: 'exhausted', resetAt: '2026-11-20T11:00:00Z', lastOutcome: 'exhausted' })
    expect(h).toMatchObject({ capacity: 'exhausted', resetAt: '2026-11-20T11:00:00Z', headroom: null, lastOutcome: 'exhausted' })
  })

  it('maps unknown enum strings to unknown rather than trusting them', () => {
    expect(decodeSourceHealth({ auth: 'logged_in', capacity: 'plenty' })).toMatchObject({ auth: 'unknown', capacity: 'unknown' })
  })
})

describe('telemetry', () => {
  it('reports unavailable when every metric is null (Linux built-in without probes)', () => {
    const t = decodeTelemetry({ sampledAt: iso() })
    expect(t).not.toBeNull()
    expect(telemetryAvailable(t)).toBe(false)
    expect(telemetryAvailable(null)).toBe(false)
    expect(telemetryAvailable(decodeTelemetry({ cpuPercent: 0 }))).toBe(true)
  })
})

describe('decodeRunnerDetail', () => {
  it('adds assignments, recent telemetry, pinned sessions and homes, and never links a hidden run', () => {
    const d = decodeRunnerDetail({
      id: 'mac-a',
      assignments: [
        { jobId: 'j1', runId: 'run-1', nodeId: 'review', state: 'running', sourceId: 'mac-a/anthropic_subscription/-' },
        { jobId: 'j2', runId: 'run-secret', nodeId: 'x', state: 'running', hidden: true },
        { runId: 'no-job' },
      ],
      recentTelemetry: [{ cpuPercent: 10 }, { cpuPercent: null }, 'junk'],
      // P4 T14 / P3 T14 item shapes: sessions {runId,nodeId,hidden,generation}; homes {runId,hidden,generation,dirty}.
      sessions: [{ runId: 'run-2', nodeId: 'chat', hidden: false, generation: 3 }, { runId: 'run-x', nodeId: 'hidden-chat', hidden: true, generation: 1 }],
      homes: [{ runId: 'run-3', hidden: false, generation: 2, dirty: true }, { runId: null, hidden: true, generation: 5, dirty: false }],
    })!
    expect(d.assignments).toEqual([
      { jobId: 'j1', runId: 'run-1', nodeId: 'review', state: 'running', sourceId: 'mac-a/anthropic_subscription/-', hidden: false },
      { jobId: 'j2', runId: null, nodeId: 'x', state: 'running', sourceId: '', hidden: true },
    ])
    expect(d.recentTelemetry.map((t) => t.cpuPercent)).toEqual([10, null])
    expect(d.sessions).toEqual([
      { runId: 'run-2', nodeId: 'chat', hidden: false, generation: 3 },
      { runId: null, nodeId: 'hidden-chat', hidden: true, generation: 1 },
    ])
    expect(d.homes).toEqual([
      { runId: 'run-3', hidden: false, generation: 2, dirty: true },
      { runId: null, hidden: true, generation: 5, dirty: false },
    ])
  })

  it('defaults absent detail lists to empty', () => {
    expect(decodeRunnerDetail({ id: 'builtin' })).toMatchObject({ assignments: [], recentTelemetry: [], sessions: [], homes: [] })
  })
})

describe('decodePoolEvent', () => {
  it('decodes a PoolEventView and its cause', () => {
    expect(
      decodePoolEvent({ id: 'e1', at: iso(), kind: 'runner.cordoned', runnerId: 'mac-a', cause: { kind: 'operator', actor: 'ryan' }, detail: { why: 'maintenance' } }),
    ).toEqual({
      id: 'e1',
      at: iso(),
      kind: 'runner.cordoned',
      runnerId: 'mac-a',
      sourceId: '',
      runId: null,
      cause: { kind: 'operator', actor: 'ryan', outcome: '' },
      detail: { why: 'maintenance' },
    })
  })

  it('rejects events missing id, kind or timestamp', () => {
    expect(decodePoolEvent({ kind: 'runner.lost', at: iso() })).toBeNull()
    expect(decodePoolEvent({ id: 'e', at: iso() })).toBeNull()
    expect(decodePoolEvent({ id: 'e', kind: 'runner.lost' })).toBeNull()
  })
})

describe('runner keys', () => {
  it('decodes the P2 T3 runnerKeyView; omitted claimedAt/lastUsedAt/revokedAt mean not yet', () => {
    // runnerKeyView: id,name,runnerId,prefix,claimed,revoked,createdAt + omitempty claimedAt,lastUsedAt,revokedAt
    expect(decodeRunnerKey({ id: 'k1', name: 'mac-mini', runnerId: 'mac-mini', prefix: 'ark_AbCd', claimed: false, revoked: false, createdAt: iso() })).toEqual({
      id: 'k1',
      name: 'mac-mini',
      runnerId: 'mac-mini',
      prefix: 'ark_AbCd',
      claimed: false,
      revoked: false,
      createdAt: iso(),
      claimedAt: null,
      lastUsedAt: null,
      revokedAt: null,
    })
    expect(decodeRunnerKey({ id: 'k2', name: 'studio', claimed: true, claimedAt: iso(-1_000) })).toMatchObject({ claimed: true, claimedAt: iso(-1_000), revoked: false })
    expect(decodeRunnerKey({ name: 'no-id' })).toBeNull()
  })

  it('reads the one-time plaintext and setup guidance from the P2 T3 create response', () => {
    const meta = { id: 'k1', name: 'mac-mini', runnerId: 'mac-mini', prefix: 'ark_AbCd', claimed: false, revoked: false, createdAt: iso() }
    expect(decodeKeyCreate({ key: meta, plaintext: 'ark_abc', setup: 'Copy this key now; it is shown only once.' })).toMatchObject({
      plaintext: 'ark_abc',
      setup: 'Copy this key now; it is shown only once.',
      key: { id: 'k1', prefix: 'ark_AbCd' },
    })
    expect(decodeKeyCreate({ key: meta, plaintext: 'ark_def' })).toMatchObject({ plaintext: 'ark_def', setup: '' })
  })

  it('returns null when no plaintext or no key metadata came back, so no secret panel can open empty', () => {
    expect(decodeKeyCreate({ key: { id: 'k1', name: 'mac-mini' }, setup: 'x' })).toBeNull()
    expect(decodeKeyCreate({ plaintext: 'ark_x' })).toBeNull()
    expect(decodeKeyCreate({ id: 'k1', name: 'mac-mini', secret: 'ark_guess' })).toBeNull()
  })
})

describe('decodeExecutionStatus', () => {
  it('decodes the contracts §10 json and drops empty optionals', () => {
    expect(
      decodeExecutionStatus({
        runId: 'run-1',
        nodeId: 'review',
        operationId: 'node',
        jobId: 'job-1',
        state: 'waiting_capacity',
        group: 'anthropic_subscription:anthropic',
        model: 'claude-opus-4-1',
        reason: 'all anthropic subscriptions exhausted',
        nextReset: '2026-11-20T11:00:00Z',
        runnerId: '',
      }),
    ).toEqual({
      runId: 'run-1',
      nodeId: 'review',
      operationId: 'node',
      jobId: 'job-1',
      state: 'waiting_capacity',
      group: 'anthropic_subscription:anthropic',
      model: 'claude-opus-4-1',
      reason: 'all anthropic subscriptions exhausted',
      nextReset: '2026-11-20T11:00:00Z',
    })
  })

  it('keeps move progress and the home/pin runners', () => {
    expect(
      decodeExecutionStatus({ runId: 'r', jobId: 'j', state: 'moving_workspace', progress: { bytesDone: 5, bytesTotal: 10 }, homeRunnerId: 'mac-a', sessionPinRunnerId: 'mac-b' }),
    ).toMatchObject({ progress: { bytesDone: 5, bytesTotal: 10 }, homeRunnerId: 'mac-a', sessionPinRunnerId: 'mac-b' })
  })

  it('rejects unknown states and rows without run or job ids', () => {
    expect(decodeExecutionStatus({ runId: 'r', jobId: 'j', state: 'completed' })).toBeNull()
    expect(decodeExecutionStatus({ jobId: 'j', state: 'running' })).toBeNull()
    expect(decodeExecutionStatus({ runId: 'r', state: 'running' })).toBeNull()
  })
})

const frame = (type: string, data: unknown, instanceId = '00000000-0000-0000-0000-000000000000'): WebSocketEvent => ({
  type,
  timestamp: iso(1_000),
  data,
  instanceId,
})

describe('decodeLiveEvent', () => {
  it('decodes runner.*, source.*, job.* and workspace.moved as pool events', () => {
    for (const kind of ['runner.cordoned', 'source.logged_out', 'job.rerouted', 'workspace.moved', 'runner.key_revoked']) {
      const live = decodeLiveEvent(frame(kind, { id: `e-${kind}`, at: iso(), kind, runnerId: 'mac-a' }))
      expect(live).toEqual({ kind: 'pool', event: expect.objectContaining({ id: `e-${kind}`, kind, runnerId: 'mac-a' }) })
    }
  })

  it('takes the envelope type as the kind when the payload disagrees', () => {
    const live = decodeLiveEvent(frame('runner.lost', { id: 'e1', at: iso(), kind: 'runner.updated', runnerId: 'mac-a' }))
    expect(live.kind === 'pool' && live.event.kind).toBe('runner.lost')
  })

  it('decodes execution.status with the envelope timestamp and run id', () => {
    const live = decodeLiveEvent(frame('execution.status', { jobId: 'j1', nodeId: 'n', state: 'queued_slot' }, 'run-7'))
    expect(live).toEqual({
      kind: 'execution',
      at: iso(1_000),
      status: { runId: 'run-7', nodeId: 'n', operationId: '', jobId: 'j1', state: 'queued_slot' },
    })
  })

  it('maps node completion events to node_done for queue pruning', () => {
    expect(decodeLiveEvent(frame('node.completed', { nodeId: 'review' }, 'run-1'))).toEqual({ kind: 'node_done', instanceId: 'run-1', nodeId: 'review' })
    expect(decodeLiveEvent(frame('node.completed', {}, 'run-1'))).toEqual({ kind: 'ignored' })
  })

  it('decodes session.moved, which P4 T8 records as a PoolEventView with the move in detail', () => {
    const view = {
      id: 'e-move',
      at: iso(),
      kind: 'session.moved',
      runnerId: 'mac-b',
      sourceId: 'mac-b/anthropic_subscription/-',
      runId: 'run-3',
      cause: { kind: 'outcome', outcome: 'exhausted' },
      detail: { from: 'mac-a', to: 'mac-b', reason: 'exhausted', lossy: true, nodeId: 'chat' },
    }
    expect(decodeLiveEvent(frame('session.moved', view, 'run-3'))).toEqual({
      kind: 'pool',
      event: {
        id: 'e-move',
        at: iso(),
        kind: 'session.moved',
        runnerId: 'mac-b',
        sourceId: 'mac-b/anthropic_subscription/-',
        runId: 'run-3',
        cause: { kind: 'outcome', actor: '', outcome: 'exhausted' },
        detail: { from: 'mac-a', to: 'mac-b', reason: 'exhausted', lossy: true, nodeId: 'chat' },
      },
    })
    // A payload that is not a PoolEventView is not guessed into one.
    expect(decodeLiveEvent(frame('session.moved', { from: 'mac-a', to: 'mac-b', lossy: true }, 'run-3'))).toEqual({ kind: 'ignored' })
  })

  it('ignores unknown kinds, bare prefixes and malformed payloads without throwing', () => {
    const cases: WebSocketEvent[] = [
      frame('teleport.started', { id: 'x', at: iso(), kind: 'teleport.started' }),
      frame('runner.', { id: 'x', at: iso(), kind: 'runner.' }),
      frame('runner.updated', 'garbage'),
      frame('execution.status', { state: 'exploded', jobId: 'j' }),
      frame('interactive.token', { token: 'hi' }),
      { type: 42 } as unknown as WebSocketEvent,
      null as unknown as WebSocketEvent,
    ]
    for (const c of cases) expect(decodeLiveEvent(c)).toEqual({ kind: 'ignored' })
  })

  it('classifies pool kinds exactly', () => {
    expect(isPoolEventKind('runner.drained')).toBe(true)
    expect(isPoolEventKind('session.moved')).toBe(true)
    expect(isPoolEventKind('runners.drained')).toBe(false)
    expect(isPoolEventKind('instance.started')).toBe(false)
  })
})

describe('Go HTTP contract', () => {
  it('decodes actual Go HTTP fixtures without guessed JSON names', async () => {
    installFetch({ '/api/v1/runners/builtin': goFixture.runner })
    const detail = await getRunner('builtin')
    expect(detail.sources[0].unavailableReason).toBe('harness unavailable')
    expect(detail.sources[0].health.auth).toBe('unknown')
  })

  it('decodes server-emitted session identity and persisted notices through the trace client', async () => {
    const runId = goFixture.trace[0].instanceId
    installFetch({ [`/api/v1/instances/${runId}/trace`]: goFixture.trace })
    const [execution] = await api.instances.getTrace(runId)
    expect(execution.sessionInvocationId).toBe('22222222-2222-4222-8222-222222222222')
    expect(execution.sessionGeneration).toBe(4)
    expect(execution.sessionMoves?.[0]).toMatchObject({
      invocationId: execution.sessionInvocationId, generation: 4, nodeId: 'chat',
      notice: 'tool details and reasoning may be lost', turnIndex: 2,
      requestId: '33333333-3333-4333-8333-333333333333',
    })
  })

  it('ignores malformed session identity on both trace read paths', async () => {
    const malformed = { ...goFixture.trace[0], sessionInvocationId: 'not-a-uuid' }
    const runId = malformed.instanceId
    installFetch({
      [`/api/v1/instances/${runId}/trace`]: [malformed],
      [`/api/v1/instances/${runId}/trace/chat`]: malformed,
    })
    for (const execution of [
      ...(await api.instances.getTrace(runId)), await api.instances.getNodeTrace(runId, 'chat'),
    ]) {
      expect(execution.sessionInvocationId).toBeUndefined()
      expect(execution.sessionPinRunnerId).toBeUndefined()
      expect(execution.sessionGeneration).toBeUndefined()
      expect(execution.sessionMoves).toBeUndefined()
      expect(execution.nodeId).toBe('chat')
    }
  })
})
