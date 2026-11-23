import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFetch, iso, key, reply } from '../api/runners.fixtures'
import { localAdmin } from '../routes/queueWorld.fixtures'
import RunnerKeysSection from './RunnerKeysSection'

const KEYS = '/api/v1/runners/keys'
const CONFIG = '/api/v1/system/config'
const SECRET = 'rk_live_SECRET-never-persist'
// P2 T3 create response also carries runner.KeySetupGuidance under `setup`.
const SETUP_GUIDANCE = 'Copy this key now; it is shown only once.'
const config = { server: { host: '0.0.0.0', port: 8080, publicBaseUrl: 'https://arb.example' }, workspace: { path: '/srv/arbitron/workspaces' } }

beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn(async () => {}) }, configurable: true })
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  window.localStorage.clear()
})

function renderSection(me = localAdmin) {
  return render(
    <MemoryRouter>
      <RunnerKeysSection me={me} />
    </MemoryRouter>,
  )
}
const rowOf = (name: string) => document.querySelector<HTMLElement>(`tr[data-key-name="${name}"]`)!
const findRow = (name: string) =>
  waitFor(() => {
    const r = document.querySelector<HTMLElement>(`tr[data-key-name="${name}"]`)
    if (!r) throw new Error(`no row for ${name}`)
    return r
  })

describe('RunnerKeysSection — access', () => {
  it('a non-admin user sees no key controls and nothing is requested', () => {
    const f = installFetch({ [KEYS]: [key()] })
    const { container } = renderSection({ ...localAdmin, role: 'user', isAdmin: false })
    expect(container).toBeEmptyDOMElement()
    expect(f.calls).toHaveLength(0)
  })

  it('an unknown identity renders nothing', () => {
    const f = installFetch({})
    const { container } = render(
      <MemoryRouter>
        <RunnerKeysSection me={null} />
      </MemoryRouter>,
    )
    expect(container).toBeEmptyDOMElement()
    expect(f.calls).toHaveLength(0)
  })
})

describe('RunnerKeysSection — list', () => {
  it('lists name, runner or "unclaimed", created and last use', async () => {
    installFetch({
      [KEYS]: {
        keys: [
          key({ id: 'k1', name: 'mac-mini', runnerId: 'mac-mini' }),
          key({ id: 'k2', name: 'studio', runnerId: 'studio', claimedAt: iso(-3_600_000), lastUsedAt: iso(-60_000) }),
          key({ id: 'k3', name: 'old-air', runnerId: 'old-air', claimedAt: iso(-7_200_000), revokedAt: iso(-600_000) }),
        ],
      },
      [CONFIG]: config,
    })
    renderSection()
    await findRow('mac-mini')
    expect(within(rowOf('mac-mini')).getByText('unclaimed')).toBeInTheDocument()
    expect(within(rowOf('mac-mini')).getByText('never')).toBeInTheDocument()
    expect(within(rowOf('studio')).getByRole('link', { name: 'studio' })).toHaveAttribute('href', '/pool/runners/studio')
    expect(within(rowOf('studio')).queryByText('never')).toBeNull()
    expect(within(rowOf('old-air')).getByText('revoked')).toBeInTheDocument()
    expect(within(rowOf('old-air')).queryByRole('button', { name: /revoke/i })).toBeNull()
    expect(within(rowOf('mac-mini')).getByRole('button', { name: 'Revoke mac-mini' })).toBeInTheDocument()
  })

  it('a failed list load shows the error with retry', async () => {
    const routes: Record<string, unknown> = { [KEYS]: reply(500, { error: 'keys unavailable' }), [CONFIG]: config }
    installFetch(routes)
    renderSection()
    expect(await screen.findByRole('alert')).toHaveTextContent('API 500: keys unavailable')
    routes[KEYS] = [key()]
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await findRow('mac-mini')
  })
})

describe('RunnerKeysSection — add and revoke', () => {
  it('adding lists the new unclaimed key; the secret never reaches storage, URL, logs or later requests', async () => {
    const logs = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m))
    const setItem = vi.spyOn(window.localStorage, 'setItem')
    const f = installFetch({
      [KEYS]: [],
      [CONFIG]: config,
      [`POST ${KEYS}`]: { key: key({ id: 'k9', name: 'mac-new', runnerId: 'mac-new' }), plaintext: SECRET, setup: SETUP_GUIDANCE },
    })
    renderSection()
    await screen.findByText('No runner keys yet')
    fireEvent.click(screen.getByRole('button', { name: 'Add runner' }))
    fireEvent.change(screen.getByLabelText('Runner name'), { target: { value: 'mac-new' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    })
    expect(screen.getByLabelText('Runner key')).toHaveValue(SECRET)
    expect(screen.getByTestId('runner-env')).toHaveTextContent('ARBITRON_SERVER_URL=https://arb.example')
    expect(screen.getByTestId('workspace-root')).toHaveTextContent('/srv/arbitron/workspaces')
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(within(rowOf('mac-new')).getByText('unclaimed')).toBeInTheDocument()
    expect(document.body.innerHTML).not.toContain(SECRET)
    fireEvent.click(screen.getByRole('button', { name: 'Add runner' }))
    expect(screen.queryByLabelText('Runner key')).toBeNull()
    expect(screen.getByLabelText('Runner name')).toHaveValue('')
    expect(setItem.mock.calls.flat().join(' ')).not.toContain(SECRET)
    for (let i = 0; i < window.localStorage.length; i++) {
      expect(window.localStorage.getItem(window.localStorage.key(i)!) ?? '').not.toContain(SECRET)
    }
    expect(window.location.href).not.toContain(SECRET)
    expect(JSON.stringify(window.history.state ?? null)).not.toContain(SECRET)
    for (const spy of logs) expect(JSON.stringify(spy.mock.calls)).not.toContain(SECRET)
    for (const c of f.calls) expect(`${c.url} ${c.body ?? ''}`).not.toContain(SECRET)
  })

  it('revoking reloads the list; a failed revoke leaves the key listed with the error', async () => {
    let revoked = false
    const routes: Record<string, unknown> = {
      [KEYS]: () => [key({ id: 'k1', name: 'mac-mini', claimedAt: iso(-60_000), revokedAt: revoked ? iso() : null })],
      [CONFIG]: config,
      [`DELETE ${KEYS}/k1`]: reply(500, { error: 'hub unavailable' }),
    }
    const f = installFetch(routes)
    renderSection()
    await findRow('mac-mini')
    fireEvent.click(screen.getByRole('button', { name: 'Revoke mac-mini' }))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Revoke key' }))
    })
    expect(screen.getByRole('alert')).toHaveTextContent('Revoke failed: API 500: hub unavailable')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(within(rowOf('mac-mini')).getByRole('button', { name: 'Revoke mac-mini' })).toBeInTheDocument()

    routes[`DELETE ${KEYS}/k1`] = () => {
      revoked = true
      return reply(200, { status: 'revoked', id: 'k1' })
    }
    fireEvent.click(screen.getByRole('button', { name: 'Revoke mac-mini' }))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Revoke key' }))
    })
    await waitFor(() => expect(within(rowOf('mac-mini')).getByText('revoked')).toBeInTheDocument())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(f.calls.filter((c) => c.method === 'DELETE').map((c) => c.body)).toEqual(['{"acknowledge":true}', '{"acknowledge":true}'])
  })

  it('works without system config: guidance falls back and the page origin is the server URL', async () => {
    installFetch({ [KEYS]: [], [CONFIG]: reply(403, { error: 'forbidden' }), [`POST ${KEYS}`]: { key: key({ name: 'mac-new' }), plaintext: SECRET, setup: SETUP_GUIDANCE } })
    renderSection()
    await screen.findByText('No runner keys yet')
    fireEvent.click(screen.getByRole('button', { name: 'Add runner' }))
    fireEvent.change(screen.getByLabelText('Runner name'), { target: { value: 'mac-new' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    })
    expect(screen.getByTestId('runner-env')).toHaveTextContent(`ARBITRON_SERVER_URL=${window.location.origin}`)
    expect(screen.getByTestId('workspace-root')).toHaveTextContent("the server's workspace root")
  })
})
