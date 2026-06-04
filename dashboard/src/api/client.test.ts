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
})
