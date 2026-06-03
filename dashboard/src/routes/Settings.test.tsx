import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { it, expect, vi, beforeEach } from 'vitest'
import Settings from './Settings'
import { api } from '../api/client'

vi.mock('../hooks/useSystemWebSocket', () => ({
  useSystemWebSocket: () => ({ connected: true }),
}))

vi.mock('../api/client', () => ({
  api: {
    system: {
      getConfig: vi.fn(),
      updateQueueConfig: vi.fn(),
      updateTeamsConfig: vi.fn(),
    },
  },
}))

const baseConfig = {
  server: { host: 'localhost', port: 8080, publicBaseUrl: 'http://localhost:8080' },
  database: { connected: true },
  workspace: { path: './ws' },
  llm: { provider: 'anthropic', apiKey: '**REDACTED**' },
  logging: { level: 'info', file: 'a.log' },
  tracing: { enabled: false, exportDir: './t' },
  queue: { globalCap: 20, perUserCap: 5, runningCount: 0, queuedCount: 0, waitingCount: 0 },
  teams: { enabled: false, webhookUrl: '' },
}

beforeEach(() => {
  vi.clearAllMocks()
})

// Happy-path: configured + enabled state renders from the redacted config.
it('renders Configured and a checked toggle when teams is set + enabled', async () => {
  vi.mocked(api.system.getConfig).mockResolvedValue({
    ...baseConfig,
    teams: { enabled: true, webhookUrl: '**REDACTED**' },
  } as never)

  render(<Settings />)

  expect(await screen.findByText('Configured')).toBeInTheDocument()
  expect(screen.getByRole('checkbox')).toBeChecked()
  // The redacted sentinel must never be placed into the input value.
  expect(screen.getByRole('textbox')).toHaveValue('')
})

// Edge-case: empty webhook reads back as "Not configured".
it('renders Not configured when webhook is empty', async () => {
  vi.mocked(api.system.getConfig).mockResolvedValue(baseConfig as never)
  render(<Settings />)
  expect(await screen.findByText('Not configured')).toBeInTheDocument()
})

// Success: entering a URL + toggling + Save calls updateTeamsConfig and shows success.
it('saves webhookUrl + enabled and shows the success message', async () => {
  vi.mocked(api.system.getConfig).mockResolvedValue(baseConfig as never)
  vi.mocked(api.system.updateTeamsConfig).mockResolvedValue({
    enabled: true,
    webhookUrl: '**REDACTED**',
  } as never)

  render(<Settings />)
  await screen.findByText('Teams Notifications')

  await userEvent.type(screen.getByRole('textbox'), 'https://new.example.com/hook')
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(screen.getAllByRole('button', { name: /save/i })[1])

  await waitFor(() =>
    expect(api.system.updateTeamsConfig).toHaveBeenCalledWith({
      enabled: true,
      webhookUrl: 'https://new.example.com/hook',
    }),
  )
  expect(await screen.findByText('Saved successfully.')).toBeInTheDocument()
})

// Failure: a rejected save surfaces an error and the form stays usable.
it('surfaces a save error without crashing', async () => {
  vi.mocked(api.system.getConfig).mockResolvedValue(baseConfig as never)
  vi.mocked(api.system.updateTeamsConfig).mockRejectedValue(new Error('API 400: bad url'))

  render(<Settings />)
  await screen.findByText('Teams Notifications')

  await userEvent.type(screen.getByRole('textbox'), 'bad')
  await userEvent.click(screen.getAllByRole('button', { name: /save/i })[1])

  expect(await screen.findByText(/API 400/)).toBeInTheDocument()
  expect(screen.getByRole('textbox')).toBeInTheDocument() // still rendered
})

// Edge-case: a blank-URL Save omits webhookUrl so the stored secret is kept.
it('omits webhookUrl on Save when the input is blank', async () => {
  vi.mocked(api.system.getConfig).mockResolvedValue({
    ...baseConfig,
    teams: { enabled: true, webhookUrl: '**REDACTED**' },
  } as never)
  vi.mocked(api.system.updateTeamsConfig).mockResolvedValue({
    enabled: false,
    webhookUrl: '**REDACTED**',
  } as never)

  render(<Settings />)
  await screen.findByText('Teams Notifications')

  await userEvent.click(screen.getByRole('checkbox')) // toggle enabled -> false
  await userEvent.click(screen.getAllByRole('button', { name: /save/i })[1])

  await waitFor(() =>
    expect(api.system.updateTeamsConfig).toHaveBeenCalledWith({ enabled: false }),
  )
})

// Error: a failed initial load renders the existing top-level error path.
it('renders the error path when initial config load fails', async () => {
  vi.mocked(api.system.getConfig).mockRejectedValue(new Error('boom'))
  render(<Settings />)
  expect(await screen.findByText('boom')).toBeInTheDocument()
})
