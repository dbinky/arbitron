import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, detail, runner } from './api/runners.fixtures'
import { FakeWebSocket, flush, installPool, openSystemSockets, systemSockets } from './hooks/poolSocket.fixtures'
import { localAdmin } from './routes/queueWorld.fixtures'
import App from './App'

const world = { runners: [runner()], events: [], running: [], waiting: [] }
const shell = {
  '/api/v1/me': localAdmin,
  '/api/v1/runners/keys': [],
  '/api/v1/system/config': { server: { publicBaseUrl: 'https://arb.example' }, workspace: { path: '/srv/arbitron/workspaces' } },
  '/api/v1/notifications/unread-count': { count: 0 },
  '/api/v1/notifications?dismissed=false&limit=50': { items: [], hasMore: false },
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})
afterEach(() => {
  window.history.pushState({}, '', '/')
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function renderAt(path: string, extra: Record<string, unknown> = {}) {
  window.history.pushState({}, '', path)
  const f = installPool(world, { ...shell, ...extra })
  render(<App />)
  await flush()
  await flush()
  return f
}

describe('App — Pool wiring', () => {
  it('renders the Pool view at /pool with the built-in runner card, fed by the runner REST and system stream', async () => {
    const f = await renderAt('/pool')
    expect(document.querySelector('[data-view="arbitron-pool-view"]')).not.toBeNull()
    expect(document.querySelector('[data-runner-card="builtin"]')).not.toBeNull()
    expect(screen.getByTestId('local-mode')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Pool' })).toHaveAttribute('aria-current', 'page')
    expect(f.count('GET', '/api/v1/runners')).toBe(1)
    expect(systemSockets().length).toBeGreaterThan(0)
    openSystemSockets()
    await flush()
    expect(f.count('GET', '/api/v1/runners')).toBe(2)
    expect(screen.queryByTestId('pool-stale')).toBeNull()
    expect(screen.getByRole('region', { name: 'Runner keys' })).toBeInTheDocument()
  })

  it('renders the runner detail at /pool/runners/:id', async () => {
    const f = await renderAt('/pool/runners/builtin', {
      '/api/v1/runners/builtin': detail({
        ...runner(), id: 'builtin', name: 'builtin',
        assignments: [{ jobId: 'job-review', runId: 'run-review', nodeId: 'review', state: 'running', sourceId: 'builtin/anthropic_subscription/-', hidden: false }],
      }),
    })
    expect(document.querySelector('[data-view="arbitron-runner-detail"]')).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'builtin' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to pool' })).toHaveAttribute('href', '/pool')
    expect(f.count('GET', '/api/v1/runners/builtin')).toBe(1)
    expect(screen.getByRole('link', { name: 'review' })).toHaveAttribute('href', '/instances/run-review')
  })
})
