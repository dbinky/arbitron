import { afterEach, expect, it, vi } from 'vitest'
import { api } from './client'

afterEach(() => vi.unstubAllGlobals())
it.each([
  ['instance', (signal: AbortSignal) => api.instances.getLayout('child', signal)],
  ['workflow', (signal: AbortSignal) => api.workflows.getLayout('pr-review', signal)],
] as const)('forwards the %s layout signal into the pending real request helper fetch', async (_kind, get) => {
  let signal: AbortSignal | undefined
  vi.stubGlobal('fetch', (_url: string, init?: RequestInit) => new Promise((_resolve, reject) => {
    signal = init?.signal as AbortSignal | undefined
    signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
  }))
  const controller = new AbortController()
  const pending = get(controller.signal)
  expect(signal).toBe(controller.signal)
  const rejected = expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  controller.abort()
  await rejected
})
