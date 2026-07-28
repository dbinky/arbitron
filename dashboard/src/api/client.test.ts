import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'

describe('api.system.updateTeamsConfig', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ enabled: true, webhookUrl: '**REDACTED**' }),
    } as Response)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Success: PUTs both fields to the teams endpoint.
  it('PUTs webhookUrl + enabled to /api/v1/system/config/teams', async () => {
    await api.system.updateTeamsConfig({ webhookUrl: 'https://x.example.com', enabled: true })

    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/system/config/teams',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ webhookUrl: 'https://x.example.com', enabled: true }),
      }),
    )
  })

  // Edge-case: a toggle-only body omits webhookUrl entirely.
  it('omits webhookUrl when not provided', async () => {
    await api.system.updateTeamsConfig({ enabled: false })

    const init = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit
    expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
  })
})

describe('api.instances dismissal', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ status: 'ok' }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('POSTs dismiss to /instances/{id}/dismiss', async () => {
    await api.instances.dismiss('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/dismiss',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs restore to /instances/{id}/restore', async () => {
    await api.instances.restore('abc')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/abc/restore',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs dismissAll with the state query param', async () => {
    await api.instances.dismissAll('failed')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/instances/dismiss-all?state=failed',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('POSTs dismissAll with a since window when provided', async () => {
    await api.instances.dismissAll('completed', '2026-01-01T00:00:00.000Z')
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('state=completed')
    expect(url).toContain('since=')
  })

  it('list includes includeDismissed=true when requested', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed', includeDismissed: true })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain('includeDismissed=true')
    expect(url).toContain('state=failed')
  })

  it('list omits includeDismissed when false/absent', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ state: 'failed' })
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).not.toContain('includeDismissed')
  })

  it('list narrows by parent when requested, and omits it otherwise', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [] } as Response)
    await api.instances.list({ parent: 'p1' })
    await api.instances.list({ state: 'failed' })
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls
    expect(calls[0][0] as string).toContain('parent=p1')
    expect(calls[1][0] as string).not.toContain('parent')
  })

  // Regression guard for the hierarchy fields' wire contract: the server emits
  // these key names (adapter/http/instance_handler.go's enrichedSummaryDTO),
  // and TypeScript's unchecked `res.json() as T` cast cannot itself catch a
  // typo'd field name in types.ts — only a test built from raw string-keyed
  // JSON, not a `row()`-style typed object literal, can.
  it('list surfaces parentInstanceId and childCount from a raw wire-format response verbatim', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => [
        {
          id: 'c1',
          ownerId: 'u1',
          workflowName: 'wf-a',
          state: 'running',
          priority: 'normal',
          createdAt: '2026-01-01T00:00:00.000Z',
          updatedAt: '2026-01-01T00:00:00.000Z',
          runName: 'run #1',
          retryCount: 0,
          parentInstanceId: 'p1',
          childCount: 2,
        },
      ],
    } as Response)
    const out = await api.instances.list({ parent: 'p1' })
    expect(out[0].parentInstanceId).toBe('p1')
    expect(out[0].childCount).toBe(2)
  })
})

describe('api.schedules.list', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ schedules: [{ id: 's1', workflowName: 'wf-a' }] }),
    } as Response)
  })
  afterEach(() => vi.restoreAllMocks())

  it('GETs /schedules?workflowName= and unwraps the schedules array', async () => {
    const out = await api.schedules.list('wf-a')
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toBe('/api/v1/schedules?workflowName=wf-a')
    expect(out).toEqual([{ id: 's1', workflowName: 'wf-a' }])
  })

  it('omits the query when no workflow name is given', async () => {
    await api.schedules.list()
    const url = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toBe('/api/v1/schedules')
  })
})
