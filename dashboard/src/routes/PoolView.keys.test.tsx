import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { T0, key, runner } from '../api/runners.fixtures'
import { FakeWebSocket, flush, installPool } from '../hooks/poolSocket.fixtures'
import { localAdmin } from './queueWorld.fixtures'
import PoolView from './PoolView'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const world = { runners: [runner()], events: [], running: [], waiting: [] }

async function renderAs(me: unknown) {
  const f = installPool(world, { '/api/v1/me': me, '/api/v1/runners/keys': [key()], '/api/v1/system/config': { server: { publicBaseUrl: '' }, workspace: { path: '/w' } } })
  render(
    <MemoryRouter>
      <PoolView />
    </MemoryRouter>,
  )
  await flush()
  await flush()
  return f
}

describe('PoolView — runner keys', () => {
  it('an admin sees the runner keys section with the unclaimed key', async () => {
    await renderAs(localAdmin)
    const section = screen.getByRole('region', { name: 'Runner keys' })
    expect(section).toHaveTextContent('mac-mini')
    expect(section).toHaveTextContent('unclaimed')
    expect(screen.getByRole('button', { name: 'Add runner' })).toBeInTheDocument()
  })

  it('a non-admin sees no key controls and the keys endpoint is never called', async () => {
    const f = await renderAs({ ...localAdmin, role: 'user', isAdmin: false })
    expect(screen.queryByRole('region', { name: 'Runner keys' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add runner' })).toBeNull()
    expect(f.count('GET', '/api/v1/runners/keys')).toBe(0)
    expect(document.querySelector('[data-runner-card="builtin"]')).not.toBeNull()
  })

  it('a failed identity read hides key controls but keeps the pool', async () => {
    const f = installPool(world)
    render(
      <MemoryRouter>
        <PoolView />
      </MemoryRouter>,
    )
    await flush()
    expect(screen.queryByRole('region', { name: 'Runner keys' })).toBeNull()
    expect(document.querySelector('[data-runner-card="builtin"]')).not.toBeNull()
    expect(f.count('GET', '/api/v1/me')).toBe(1)
  })
})
