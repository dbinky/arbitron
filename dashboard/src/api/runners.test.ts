import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRunnerKey, getRunner, listPoolEvents, listRunnerKeys, listRunners, revokeRunnerKey } from './runners'
import { installFetch, iso, key, poolEvent, reply, runner } from './runners.fixtures'

afterEach(() => vi.unstubAllGlobals())

describe('runners REST client', () => {
  it('listRunners reads GET /api/v1/runners (bare or wrapped) and decodes rows', async () => {
    installFetch({ '/api/v1/runners': [runner(), { name: 'no-id' }] })
    expect((await listRunners()).map((r) => r.id)).toEqual(['builtin'])
    installFetch({ '/api/v1/runners': { runners: [runner({ id: 'mac-a', name: 'mac-a' })] } })
    expect((await listRunners()).map((r) => r.id)).toEqual(['mac-a'])
  })

  it('getRunner escapes the id and returns the decoded detail', async () => {
    const f = installFetch({ '/api/v1/runners/mac%2Fa': { ...runner({ id: 'mac/a' }), assignments: [{ jobId: 'j1', runId: 'r1', nodeId: 'n', state: 'running' }] } })
    const d = await getRunner('mac/a')
    expect(d.assignments).toHaveLength(1)
    expect(f.calls[0]).toMatchObject({ url: '/api/v1/runners/mac%2Fa', method: 'GET' })
  })

  it('getRunner surfaces a 404 with its status', async () => {
    installFetch({ '/api/v1/runners/gone': reply(404, { error: 'runner not found' }) })
    await expect(getRunner('gone')).rejects.toMatchObject({ message: 'API 404: runner not found', status: 404 })
  })

  it('listPoolEvents passes the limit and decodes events', async () => {
    const f = installFetch({ '/api/v1/runners/events?limit=25': { events: [poolEvent({ id: 'e1', at: iso() })] } })
    expect((await listPoolEvents(25)).map((e) => e.id)).toEqual(['e1'])
    expect(f.count('GET', '/api/v1/runners/events?limit=25')).toBe(1)
  })

  it('listRunnerKeys returns metadata only', async () => {
    installFetch({ '/api/v1/runners/keys': [key(), key({ id: 'key-2', name: 'studio', claimedAt: iso(-1_000) })] })
    expect((await listRunnerKeys()).map((k) => [k.name, k.claimedAt])).toEqual([
      ['mac-mini', null],
      ['studio', iso(-1_000)],
    ])
  })

  it('createRunnerKey POSTs the name and returns the one-time plaintext and setup guidance', async () => {
    const setup = 'Copy this key now; it is shown only once.'
    const f = installFetch({ 'POST /api/v1/runners/keys': reply(201, { key: key(), plaintext: 'ark_once', setup }) })
    const res = await createRunnerKey('mac-mini')
    expect(res).toEqual({ key: key(), plaintext: 'ark_once', setup })
    expect(f.calls[0]).toEqual({ url: '/api/v1/runners/keys', method: 'POST', body: JSON.stringify({ name: 'mac-mini' }) })
  })

  it('createRunnerKey rejects a response without a plaintext instead of opening an empty panel', async () => {
    installFetch({ 'POST /api/v1/runners/keys': { key: key() } })
    await expect(createRunnerKey('mac-mini')).rejects.toMatchObject({ status: 502 })
  })

  it('createRunnerKey surfaces a 409 duplicate-name error', async () => {
    installFetch({ 'POST /api/v1/runners/keys': reply(409, { error: 'runner name already exists' }) })
    await expect(createRunnerKey('mac-mini')).rejects.toThrow('API 409: runner name already exists')
  })

  it('revokeRunnerKey DELETEs with the acknowledgment body and accepts the P2 T3 200 {status,id} reply', async () => {
    const f = installFetch({ 'DELETE /api/v1/runners/keys/key-1': reply(200, { status: 'revoked', id: 'key-1' }) })
    await expect(revokeRunnerKey('key-1')).resolves.toBeUndefined()
    expect(f.calls[0]).toEqual({ url: '/api/v1/runners/keys/key-1', method: 'DELETE', body: '{"acknowledge":true}' })
  })

  it('revokeRunnerKey propagates a server refusal', async () => {
    installFetch({ 'DELETE /api/v1/runners/keys/key-1': reply(403, { error: 'admin only' }) })
    await expect(revokeRunnerKey('key-1')).rejects.toMatchObject({ status: 403 })
  })
})
