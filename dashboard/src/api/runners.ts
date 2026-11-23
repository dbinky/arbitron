import { request } from './client'
import { decodeKeyCreate, decodeList, decodePoolEvent, decodeRunnerDetail, decodeRunnerKey, decodeRunnerView } from './runnerDecode'
import type { PoolEvent, RunnerDetail, RunnerKey, RunnerKeyCreateResponse, RunnerView } from './types'

const BASE = '/api/v1/runners'

function malformed(what: string): Error {
  return Object.assign(new Error(`API 502: malformed ${what} response`), { status: 502 })
}

/** GET /api/v1/runners (P1 T15): every runner with sources, health, slots and telemetry. */
export async function listRunners(): Promise<RunnerView[]> {
  return decodeList(await request<unknown>(BASE), 'runners', decodeRunnerView)
}

/** GET /api/v1/runners/{id} (P1 T15; P2 telemetry window, P3 drain, P4 sessions). */
export async function getRunner(id: string): Promise<RunnerDetail> {
  const detail = decodeRunnerDetail(await request<unknown>(`${BASE}/${encodeURIComponent(id)}`))
  if (!detail) throw malformed('runner')
  return detail
}

/** GET /api/v1/runners/events?limit=N (P1 T15): newest first; hidden runs already dropped by the server. */
export async function listPoolEvents(limit: number): Promise<PoolEvent[]> {
  const q = new URLSearchParams({ limit: String(limit) })
  return decodeList(await request<unknown>(`${BASE}/events?${q.toString()}`), 'events', decodePoolEvent)
}

/** GET /api/v1/runners/keys (P2 T3, admin): metadata only — the server never returns a secret here. */
export async function listRunnerKeys(): Promise<RunnerKey[]> {
  return decodeList(await request<unknown>(`${BASE}/keys`), 'keys', decodeRunnerKey)
}

/**
 * POST /api/v1/runners/keys (P2 T3, admin). The one-time secret is handed to
 * the caller only: nothing here caches, stores or logs it.
 */
export async function createRunnerKey(name: string): Promise<RunnerKeyCreateResponse> {
  const created = decodeKeyCreate(
    await request<unknown>(`${BASE}/keys`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    }),
  )
  if (!created) throw malformed('runner key')
  return created
}

/** DELETE /api/v1/runners/keys/{id} with the required acknowledgment (P2 T3). */
export async function revokeRunnerKey(id: string): Promise<void> {
  await request<unknown>(`${BASE}/keys/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ acknowledge: true }),
  })
}
