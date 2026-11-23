import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installFetch, key, reply } from '../api/runners.fixtures'
import RunnerKeyAddModal, { RUNNER_KEY_NAME, runnerEnvSnippet, workspaceRootGuidance } from './RunnerKeyAddModal'
import RunnerKeyRevokeModal, { REVOKE_WARNINGS } from './RunnerKeyRevokeModal'

const SECRET = 'rk_live_7f3c9a-SECRET-0042'
// P2 T3 create response also carries runner.KeySetupGuidance under `setup`.
const SETUP_GUIDANCE = 'Copy this key now; it is shown only once.'
const KEYS = '/api/v1/runners/keys'
const writeText = vi.fn<(text: string) => Promise<void>>()

beforeEach(() => {
  writeText.mockReset()
  writeText.mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function renderAdd(over: Partial<Parameters<typeof RunnerKeyAddModal>[0]> = {}) {
  const onCreated = vi.fn()
  const onClose = vi.fn()
  render(<RunnerKeyAddModal serverUrl="https://arb.example" workspaceRoot="/srv/arbitron/workspaces" onCreated={onCreated} onClose={onClose} {...over} />)
  return { onCreated, onClose }
}
const nameInput = () => screen.getByLabelText('Runner name')
async function create(name = 'mac-mini') {
  fireEvent.change(nameInput(), { target: { value: name } })
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))
  })
}

describe('RunnerKeyAddModal — success', () => {
  it('creates the key and shows the secret once, obscured, with copy, warning and setup guidance', async () => {
    const f = installFetch({ [`POST ${KEYS}`]: { key: key({ name: 'mac-mini' }), plaintext: SECRET, setup: SETUP_GUIDANCE } })
    const { onCreated } = renderAdd()
    await create()
    expect(f.calls).toEqual([{ url: KEYS, method: 'POST', body: JSON.stringify({ name: 'mac-mini' }) }])
    const field = screen.getByLabelText('Runner key') as HTMLInputElement
    expect(field.type).toBe('password')
    expect(field.value).toBe(SECRET)
    expect(screen.getByRole('alert')).toHaveTextContent("This key won't be shown again")
    const snippet = screen.getByTestId('runner-env')
    expect(snippet).toHaveTextContent('ARBITRON_SERVER_URL=https://arb.example')
    expect(snippet).toHaveTextContent('ARBITRON_RUNNER_KEY=<paste the key you copied>')
    expect(snippet).not.toHaveTextContent(SECRET)
    expect(screen.getByText(SETUP_GUIDANCE)).toBeInTheDocument()
    expect(screen.getByTestId('workspace-root')).toHaveTextContent('/srv/arbitron/workspaces')
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    })
    expect(writeText).toHaveBeenCalledWith(SECRET)
    expect(screen.getByRole('status')).toHaveTextContent('Copied')
    fireEvent.click(screen.getByRole('button', { name: 'Show' }))
    expect(field.type).toBe('text')
    expect(onCreated).toHaveBeenCalledTimes(1)
    expect(onCreated.mock.calls[0][0]).toMatchObject({ id: 'key-1', name: 'mac-mini' })
    expect(JSON.stringify(onCreated.mock.calls)).not.toContain(SECRET)
  })

  it('closing after create clears the secret from the dialog', async () => {
    installFetch({ [`POST ${KEYS}`]: { key: key(), plaintext: SECRET, setup: SETUP_GUIDANCE } })
    const { onClose } = renderAdd()
    await create()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByLabelText('Runner key')).toBeNull()
    expect(document.body.innerHTML).not.toContain(SECRET)
  })
})

describe('RunnerKeyAddModal — errors', () => {
  it('a failed create shows the error and no secret panel', async () => {
    installFetch({ [`POST ${KEYS}`]: reply(409, { error: 'a key named mac-mini already exists' }) })
    const { onCreated } = renderAdd()
    await create()
    expect(screen.getByRole('alert')).toHaveTextContent('Create failed: API 409: a key named mac-mini already exists')
    expect(screen.queryByLabelText('Runner key')).toBeNull()
    expect(screen.queryByTestId('runner-env')).toBeNull()
    expect(nameInput()).toHaveValue('mac-mini')
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('a copy failure says so and keeps the secret visible', async () => {
    installFetch({ [`POST ${KEYS}`]: { key: key(), plaintext: SECRET, setup: SETUP_GUIDANCE } })
    writeText.mockRejectedValue(new Error('denied'))
    renderAdd()
    await create()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    })
    expect(screen.getByRole('status')).toHaveTextContent('Copy failed')
    const field = screen.getByLabelText('Runner key') as HTMLInputElement
    expect(field.value).toBe(SECRET)
    expect(field.type).toBe('text')
  })

  it('a clipboard-less browser is treated as a copy failure', async () => {
    installFetch({ [`POST ${KEYS}`]: { key: key(), plaintext: SECRET, setup: SETUP_GUIDANCE } })
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    renderAdd()
    await create()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    })
    expect(screen.getByRole('status')).toHaveTextContent('Copy failed')
    expect(screen.getByLabelText('Runner key')).toHaveValue(SECRET)
  })
})

describe('RunnerKeyAddModal — edge cases', () => {
  it('closes during an in-flight create without leaking its late secret', async () => {
    let finish!: (response: Response) => void
    const fetch = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve }))
    vi.stubGlobal('fetch', fetch)
    const { onCreated, onClose } = renderAdd()
    await create()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await act(async () => {
      finish(new Response(JSON.stringify({ key: key(), plaintext: SECRET, setup: SETUP_GUIDANCE })))
    })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onCreated).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.body.innerHTML).not.toContain(SECRET)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not call the metadata callback after unmount during create', async () => {
    let finish!: (response: Response) => void
    vi.stubGlobal('fetch', () => new Promise<Response>((resolve) => { finish = resolve }))
    const onCreated = vi.fn()
    const view = render(<RunnerKeyAddModal serverUrl="https://arb.example" workspaceRoot={null} onCreated={onCreated} onClose={vi.fn()} />)
    await create()
    view.unmount()
    await act(async () => {
      finish(new Response(JSON.stringify({ key: key(), plaintext: SECRET, setup: SETUP_GUIDANCE })))
    })
    expect(onCreated).not.toHaveBeenCalled()
  })
  it('cancelling before create makes no API call', () => {
    const f = installFetch({})
    const { onClose } = renderAdd()
    fireEvent.change(nameInput(), { target: { value: 'mac-mini' } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(f.calls).toHaveLength(0)
  })

  it('an invalid name cannot be submitted', () => {
    const f = installFetch({})
    renderAdd()
    for (const bad of ['', 'Mac Mini', '-mac', 'a']) {
      fireEvent.change(nameInput(), { target: { value: bad } })
      expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
    }
    expect(f.calls).toHaveLength(0)
    expect(RUNNER_KEY_NAME.test('mac-mini-2')).toBe(true)
  })

  it('guidance falls back when the server root is unknown; the snippet never carries a secret', () => {
    expect(workspaceRootGuidance(null)).toContain("the server's workspace root")
    expect(runnerEnvSnippet('https://arb.example')).toBe('ARBITRON_SERVER_URL=https://arb.example\nARBITRON_RUNNER_KEY=<paste the key you copied>')
  })
})

describe('RunnerKeyRevokeModal', () => {
  const k = key({ id: 'key-1', name: 'mac-mini', claimedAt: '2026-11-19T10:00:00Z' })

  it('states the consequences and sends the acknowledgment', async () => {
    const f = installFetch({ [`DELETE ${KEYS}/key-1`]: reply(200, { status: 'revoked', id: 'key-1' }) })
    const onRevoked = vi.fn()
    render(<RunnerKeyRevokeModal runnerKey={k} onRevoked={onRevoked} onClose={vi.fn()} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Revoke runner key "mac-mini"?')
    for (const w of REVOKE_WARNINGS) expect(dialog).toHaveTextContent(w)
    expect(dialog).toHaveTextContent('disconnects immediately')
    expect(dialog).toHaveTextContent('may repeat interrupted steps')
    expect(dialog).toHaveTextContent('drain the runner first')
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Revoke key' }))
    })
    expect(f.calls).toEqual([{ url: `${KEYS}/key-1`, method: 'DELETE', body: '{"acknowledge":true}' }])
    expect(onRevoked).toHaveBeenCalledTimes(1)
  })

  it('a failed revoke shows the error and stays open', async () => {
    installFetch({ [`DELETE ${KEYS}/key-1`]: reply(500, { error: 'hub unavailable' }) })
    const onRevoked = vi.fn()
    render(<RunnerKeyRevokeModal runnerKey={k} onRevoked={onRevoked} onClose={vi.fn()} />)
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Revoke key' }))
    })
    expect(screen.getByRole('alert')).toHaveTextContent('Revoke failed: API 500: hub unavailable')
    expect(onRevoked).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('ignores revoke completion after closing', async () => {
    let finish!: (response: Response) => void
    vi.stubGlobal('fetch', () => new Promise<Response>((resolve) => { finish = resolve }))
    const onRevoked = vi.fn()
    const onClose = vi.fn()
    render(<RunnerKeyRevokeModal runnerKey={k} onRevoked={onRevoked} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: 'Revoke key' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await act(async () => { finish(new Response(JSON.stringify({ status: 'revoked', id: k.id }))) })
    expect(onRevoked).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('cancel sends nothing', () => {
    const f = installFetch({})
    const onClose = vi.fn()
    render(<RunnerKeyRevokeModal runnerKey={k} onRevoked={vi.fn()} onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(f.calls).toHaveLength(0)
  })
})
