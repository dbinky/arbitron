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
