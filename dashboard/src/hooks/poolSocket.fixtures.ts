import { act } from '@testing-library/react'
import { vi } from 'vitest'
import type { PoolEvent, WebSocketEvent } from '../api/types'
import { installFetch, NIL_UUID } from '../api/runners.fixtures'

/** Browser WebSocket stand-in that tests open, feed and drop explicitly. Only transport is faked. */
export class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []
  readonly url: string
  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((msg: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  private closeListeners: Array<() => void> = []

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }
  addEventListener(type: string, fn: () => void) {
    if (type === 'close') this.closeListeners.push(fn)
  }
  send() {}
  open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }
  emit(event: WebSocketEvent) {
    this.onmessage?.({ data: JSON.stringify(event) })
  }
  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
    for (const fn of this.closeListeners) fn()
  }
}

export function systemSockets(): FakeWebSocket[] {
  return FakeWebSocket.instances.filter((w) => w.url.endsWith('/api/v1/ws'))
}
export function openSystemSockets(): void {
  act(() => {
    for (const ws of systemSockets()) if (ws.readyState === FakeWebSocket.CONNECTING) ws.open()
  })
}
export function emitSystem(ev: WebSocketEvent): void {
  act(() => {
    for (const ws of systemSockets()) if (ws.readyState === FakeWebSocket.OPEN) ws.emit(ev)
  })
}
export function dropSystemSockets(): void {
  act(() => {
    for (const ws of systemSockets()) if (ws.readyState !== FakeWebSocket.CLOSED) ws.close()
  })
}

/** A pool event as the system stream delivers it (system scope unless run-scoped). */
export const wsPoolEvent = (ev: PoolEvent): WebSocketEvent => ({ type: ev.kind, timestamp: ev.at, data: ev, instanceId: ev.runId ?? NIL_UUID })

export const POOL_URLS = {
  runners: '/api/v1/runners',
  events: '/api/v1/runners/events?limit=50',
  running: '/api/v1/instances?state=running',
  waiting: '/api/v1/instances?state=waiting',
} as const

/** What the server answers. Mutate a field BEFORE emitting an invalidation to change the next reply. */
export interface PoolWorld {
  runners: unknown
  events: unknown
  running: unknown
  waiting: unknown
}

/** Serves exactly the four pool reads (plus `extra`). No healthy default: an unserved URL is a 404. */
export function installPool(world: PoolWorld, extra: Record<string, unknown> = {}) {
  return installFetch({
    [POOL_URLS.runners]: () => world.runners,
    [POOL_URLS.events]: () => world.events,
    [POOL_URLS.running]: () => world.running,
    [POOL_URLS.waiting]: () => world.waiting,
    ...extra,
  })
}

/** Advances fake time and flushes the promise jobs it released (fetch replies, effects). */
export async function flush(ms = 0): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}
