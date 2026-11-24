import { describe, expect, it } from 'vitest'
import type { LiveEvent } from '../api/runnerDecode'
import type { PoolEvent } from '../api/types'
import { execStatus, health, iso, poolEvent, remote, runner, source } from '../api/runners.fixtures'
import { applyLiveEvent, POOL_EVENT_LIMIT, pruneExecutions, snapshotState, upsertExecution } from './poolState'

const pool = (event: PoolEvent): LiveEvent => ({ kind: 'pool', event })
const SRC = 'mac-a/anthropic_subscription/-'
const exhausted = () => remote('mac-a', { sources: [source({ id: SRC, health: health({ capacity: 'exhausted', resetAt: iso(600_000) }) })] })
const base = (events: PoolEvent[] = []) => snapshotState([runner(), exhausted()], events, [execStatus({ jobId: 'j1' })])
const ev = (over: Partial<PoolEvent>) => poolEvent({ runnerId: 'mac-a', at: iso(5_000), ...over })

describe('snapshotState', () => {
  it('keeps the authoritative REST rows and at most POOL_EVENT_LIMIT log entries, deduplicated by id', () => {
    const many = Array.from({ length: POOL_EVENT_LIMIT + 5 }, (_, i) => ev({ id: `e${i}` }))
    const s = snapshotState([runner()], [ev({ id: 'e0' }), ...many], [execStatus()])
    expect(s.events).toHaveLength(POOL_EVENT_LIMIT)
    expect(new Set(s.events.map((e) => e.id)).size).toBe(POOL_EVENT_LIMIT)
    expect(s.runners.map((r) => r.id)).toEqual(['builtin'])
    expect(s.executions).toHaveLength(1)
    expect(Object.keys(s).sort()).toEqual(['events', 'executions', 'runners'])
  })
})

describe('applyLiveEvent — events invalidate, never patch', () => {
  it.each([
    'runner.cordoned',
    'runner.connected',
    'runner.lost',
    'runner.updated',
    'source.recovered',
    'source.health_changed',
    'job.requeued',
    'pool.changed',
    'workspace.moved',
    'session.moved',
  ])('%s appends to the display log and asks for an authoritative reload without touching rows', (kind) => {
    const s = base()
    const out = applyLiveEvent(s, pool(ev({ id: 'x', kind, sourceId: SRC, detail: { capacity: 'available', auth: 'ok' } })))
    expect(out.refetch).toBe(true)
    expect(out.state.runners).toBe(s.runners)
    expect(out.state.executions).toBe(s.executions)
    expect(out.state.events.map((e) => e.id)).toEqual(['x'])
  })

  it('keeps an exhausted source exhausted after an out-of-order recovered event', () => {
    const out = applyLiveEvent(base(), pool(ev({ id: 'late', kind: 'source.recovered', sourceId: SRC, at: iso(9_000_000) })))
    expect(out.state.runners[1].sources[0].health).toMatchObject({ capacity: 'exhausted', resetAt: iso(600_000) })
    expect(out.refetch).toBe(true)
  })

  it('never resurrects or patches an execution from execution.status or node completion', () => {
    const s = base()
    const a = applyLiveEvent(s, { kind: 'execution', at: iso(1), status: execStatus({ jobId: 'old', state: 'running' }) })
    expect(a).toEqual({ state: s, refetch: true })
    expect(a.state).toBe(s)
    const b = applyLiveEvent(s, { kind: 'node_done', instanceId: 'run-1', nodeId: 'review' })
    expect(b.state).toBe(s)
    expect(b.refetch).toBe(true)
  })

  it('deduplicates event display without assuming independent snapshots are atomic', () => {
    const s = base([ev({ id: 'e1', kind: 'runner.cordoned' })])
    const out = applyLiveEvent(s, pool(ev({ id: 'e1', kind: 'runner.cordoned' })))
    expect(out.state).toBe(s)
    expect(out.refetch).toBe(true)
  })

  it('handles same-time and reversed events without treating At as a cursor', () => {
    const s = base([ev({ id: 'e5', at: iso(10_000) })])
    const older = applyLiveEvent(s, pool(ev({ id: 'e4', at: iso(5_000) })))
    expect(older.refetch).toBe(true)
    expect(older.state.events.map((e) => e.id)).toEqual(['e4', 'e5'])
    const same = applyLiveEvent(older.state, pool(ev({ id: 'e6', at: iso(10_000) })))
    expect(same.refetch).toBe(true)
    expect(same.state.events.map((e) => e.id)).toEqual(['e6', 'e4', 'e5'])
  })

  it('caps the display log at POOL_EVENT_LIMIT', () => {
    let s = base()
    for (let i = 0; i < POOL_EVENT_LIMIT + 3; i++) s = applyLiveEvent(s, pool(ev({ id: `n${i}` }))).state
    expect(s.events).toHaveLength(POOL_EVENT_LIMIT)
    expect(s.events[0].id).toBe(`n${POOL_EVENT_LIMIT + 2}`)
  })

  it('ignores unknown events: same state, no reload', () => {
    const s = base()
    expect(applyLiveEvent(s, { kind: 'ignored' })).toEqual({ state: s, refetch: false })
    expect(applyLiveEvent(s, { kind: 'ignored' }).state).toBe(s)
  })
})

describe('upsertExecution / pruneExecutions (pure helpers for authoritative snapshots only)', () => {
  it('upserts by durable job id and prunes one run/node, keeping identity when nothing changes', () => {
    const list = [execStatus({ jobId: 'j1', runId: 'r1', nodeId: 'a' }), execStatus({ jobId: 'j2', runId: 'r1', nodeId: 'b' })]
    expect(upsertExecution(list, execStatus({ jobId: 'j1', runId: 'r1', nodeId: 'a', state: 'recovering' })).map((x) => [x.jobId, x.state])).toEqual([
      ['j1', 'recovering'],
      ['j2', 'running'],
    ])
    expect(upsertExecution(list, execStatus({ jobId: 'j3' }))).toHaveLength(3)
    expect(pruneExecutions(list, 'r1', 'a').map((x) => x.jobId)).toEqual(['j2'])
    expect(pruneExecutions(list, 'nope', 'nope')).toBe(list)
    expect(list).toHaveLength(2)
  })
})
