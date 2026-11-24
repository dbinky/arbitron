import { act, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { execStatus, row } from '../api/runners.fixtures'
import type { EnrichedInstanceSummary, WebSocketEvent } from '../api/types'

let capturedHandler: ((e: WebSocketEvent) => void) | null = null

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: (cb: (e: WebSocketEvent) => void) => {
    capturedHandler = cb
    return { connected: true }
  },
}))
vi.mock('../hooks/useRefetchOnReconnect', () => ({ useRefetchOnReconnect: () => {} }))
vi.mock('../hooks/useDensityPreference', () => ({
  useDensityPreference: () => ({ density: 'balanced', cycle: vi.fn() }),
}))

/** What the server answers for state=running; tests change it BEFORE emitting the invalidation. */
let running: () => EnrichedInstanceSummary[] | Promise<EnrichedInstanceSummary[]>
const listMock = vi.fn(async ({ state }: { state: string }) => (state === 'running' ? running() : []))

vi.mock('../api/client', () => ({
  api: {
    instances: { list: (p: { state: string }) => listMock(p), getNodeTrace: vi.fn() },
    me: vi.fn(async () => ({ userId: 'u1', authMode: 'local' })),
  },
}))

import QueueView from './QueueView'

const send = (e: WebSocketEvent) => act(() => capturedHandler?.(e))
const runningReads = () => listMock.mock.calls.filter(([p]) => p.state === 'running').length
const statusCell = (c: HTMLElement) => c.querySelector<HTMLElement>('tr[data-row-id="inst-1"] td[data-col="status"]')
const runnerCell = (c: HTMLElement) => c.querySelector<HTMLElement>('tr[data-row-id="inst-1"] td[data-col="runner"]')
const exec = (over = {}) => execStatus({ runId: 'inst-1', jobId: 'j1', nodeId: 'review', ...over })
const rowWith = (...e: ReturnType<typeof execStatus>[]) => [row('inst-1', e, { friendlyStep: 'Review' })]
const statusEvent = (over = {}, timestamp = '2026-11-20T10:00:05Z'): WebSocketEvent => ({ type: 'execution.status', timestamp, instanceId: 'inst-1', data: exec(over) })

function renderView() {
  return render(
    <MemoryRouter>
      <QueueView />
    </MemoryRouter>,
  )
}

describe('QueueView — execution status invalidation', () => {
  beforeEach(() => {
    capturedHandler = null
    listMock.mockClear()
    running = () => rowWith(exec({ state: 'queued_slot', runnerId: undefined }))
  })

  it('renders the authoritative capacity badge and location from REST', async () => {
    running = () => rowWith(exec({ state: 'waiting_capacity', runnerId: undefined, reason: 'all anthropic subscriptions exhausted', nextReset: new Date(Date.now() + 120_000).toISOString() }))
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)?.querySelector('[data-exec-badge="waiting_capacity"]')).toHaveTextContent('all anthropic subscriptions exhausted · model claude-opus-4-1 · resets in'))
  })

  it('invalidates and renders authoritative changed branch assignments', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)?.querySelector('[data-exec-badge="queued_slot"]')).not.toBeNull())
    const reads = runningReads()
    running = () => rowWith(exec({ state: 'running', runnerId: 'mac-a' }), exec({ jobId: 'j2', nodeId: 'lint', state: 'running', runnerId: 'mac-b' }))
    // The event payload is NOT applied: it even claims a different state.
    send(statusEvent({ state: 'recovering' }))
    expect(statusCell(container)!.querySelector('[data-exec-badge="recovering"]')).toBeNull()
    await waitFor(() => expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-b"]')).toHaveTextContent('mac-b (lint)'))
    expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-a"]')).not.toBeNull()
    expect(statusCell(container)!.querySelector('[data-exec-badge]')).toBeNull()
    expect(runningReads()).toBe(reads + 1)
  })

  it('never resurrects completed jobs from delayed execution.status', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)?.querySelector('[data-exec-badge="queued_slot"]')).not.toBeNull())
    running = () => rowWith()
    send({ type: 'node.completed', timestamp: '2026-11-20T10:00:07Z', instanceId: 'inst-1', data: { nodeId: 'review' } })
    send(statusEvent({ state: 'queued_slot', runnerId: undefined }, '2026-11-20T10:00:01Z'))
    await waitFor(() => expect(statusCell(container)!.querySelector('[data-exec-badge]')).toBeNull())
    await new Promise((r) => setTimeout(r, 400))
    expect(statusCell(container)!.querySelector('[data-exec-badge]')).toBeNull()
  })

  it('keeps last rows stale on refetch error and recovers on retry', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)?.querySelector('[data-exec-badge="queued_slot"]')).not.toBeNull())
    running = () => Promise.reject(new Error('API 503: unavailable'))
    send(statusEvent())
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('API 503: unavailable'))
    expect(statusCell(container)?.querySelector('[data-exec-badge="queued_slot"]')).not.toBeNull()
    running = () => rowWith(exec({ state: 'running', runnerId: 'mac-a' }))
    act(() => screen.getByRole('button', { name: 'Retry refresh' }).click())
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
    expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-a"]')).not.toBeNull()
  })

  it('keeps same-node loop visits and simultaneous branches distinct across same-timestamp events', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)).not.toBeNull())
    running = () => rowWith(exec({ jobId: 'visit-2', state: 'running', runnerId: 'mac-a' }), exec({ jobId: 'j3', nodeId: 'lint', state: 'running', runnerId: 'mac-b' }))
    send(statusEvent({ jobId: 'visit-1', state: 'running', runnerId: 'mac-z' }))
    send(statusEvent({ jobId: 'visit-2', state: 'running', runnerId: 'mac-a' }))
    send(statusEvent({ jobId: 'j3', nodeId: 'lint', state: 'running', runnerId: 'mac-b' }))
    await waitFor(() => expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-b"]')).not.toBeNull())
    expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-a"]')).not.toBeNull()
    expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-z"]')).toBeNull()
  })

  it('serializes overlapping reads: an invalidation during a read causes exactly one follow-up', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)).not.toBeNull())
    let release: () => void = () => {}
    running = () => new Promise((resolve) => (release = () => resolve(rowWith(exec({ state: 'running', runnerId: 'mac-a' })))))
    send(statusEvent())
    await waitFor(() => expect(runningReads()).toBe(2))
    running = () => rowWith(exec({ state: 'running', runnerId: 'mac-b' }))
    send(statusEvent({ runnerId: 'mac-b' }))
    send(statusEvent({ runnerId: 'mac-b' }))
    await new Promise((r) => setTimeout(r, 400))
    expect(runningReads()).toBe(2) // still one active read
    await act(async () => release())
    await waitFor(() => expect(runnerCell(container)!.querySelector('[data-exec-runner="mac-b"]')).not.toBeNull())
    expect(runningReads()).toBe(3)
  })

  it('does not add unlisted runs from events and ignores malformed payloads', async () => {
    const { container } = renderView()
    await waitFor(() => expect(statusCell(container)).not.toBeNull())
    const reads = runningReads()
    send({ type: 'execution.status', timestamp: '2026-11-20T10:00:05Z', instanceId: 'inst-1', data: { state: 'exploded' } })
    await new Promise((r) => setTimeout(r, 400))
    expect(runningReads()).toBe(reads)
    send({ type: 'execution.status', timestamp: '2026-11-20T10:00:05Z', instanceId: 'other', data: execStatus({ runId: 'other', jobId: 'jx', state: 'recovering' }) })
    await waitFor(() => expect(runningReads()).toBe(reads + 1))
    expect(container.querySelector('tr[data-row-id="other"]')).toBeNull()
  })
})
