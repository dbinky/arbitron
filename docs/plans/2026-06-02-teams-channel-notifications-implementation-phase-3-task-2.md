# Phase 3 — Task 2: Settings.tsx Teams Notifications Card

**Goal:** Add a "Teams Notifications" card to the Settings page that shows configured/enabled state from the redacted config, lets an admin edit the webhook + toggle, displays the read-only public base URL, and saves via `updateTeamsConfig`.

**Architecture:** Frontend — `dashboard/src/routes/Settings.tsx` is a load/edit/save form. On mount, `api.system.getConfig()` populates the card. The redacted webhook never enters the input (it only drives a "Configured"/"Not configured" indicator); a blank input on Save omits `webhookUrl` to preserve the secret. Dedicated `teamsSave*` state mirrors the existing queue-save pattern so the two cards' messages don't collide.

**Tech Stack:** TypeScript, React. Tests with Vitest + React Testing Library (`@testing-library/react`, `user-event`), `api`/WebSocket hook mocked via `vi.mock`.

---

## Context

- Edit: `dashboard/src/routes/Settings.tsx` (existing queue card at lines 127-163; save pattern `saving`/`saveMsg`/`saveErr` at 86-98; CSS classes from `./Settings.module.css`: `card`, `cardHeader`, `cardBody`, `fieldRow`, `fieldLabel`, `fieldValue`, `numberInput`, `saveRow`, `saveMessage`, `saveError`, `saveButton`)
- New test: `dashboard/src/routes/Settings.test.tsx`
- Depends on Task 1 (`SystemConfig.teams`, `server.publicBaseUrl`, `api.system.updateTeamsConfig`).
- DOM uniqueness for tests: queue caps render as `type="number"` (role `spinbutton`); the new webhook input is the **only** `type="text"` (role `textbox`) and the toggle is the **only** `checkbox`. The teams Save button is the **second** button named "Save" (queue card's is first).

## Steps

### 1. Write the failing component test

- [ ] Create `dashboard/src/routes/Settings.test.tsx`:

```tsx
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach } from 'vitest'
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
```

### 2. Run — confirm it FAILS

- [ ] Run:

```bash
cd dashboard && npx vitest run src/routes/Settings.test.tsx
```

- [ ] Expected: failures — `Unable to find an element with the text: Teams Notifications` / no `checkbox` role, suite red.

### 3. Add Teams state to the component

- [ ] In `dashboard/src/routes/Settings.tsx`, add after the live-status state (after line 22, the `waitingCount` declaration):

```tsx
  // Editable Teams notification config.
  const [teamsEnabled, setTeamsEnabled] = useState(false)
  const [teamsConfigured, setTeamsConfigured] = useState(false)
  const [teamsWebhook, setTeamsWebhook] = useState('')
  const [teamsSaving, setTeamsSaving] = useState(false)
  const [teamsSaveMsg, setTeamsSaveMsg] = useState<string | null>(null)
  const [teamsSaveErr, setTeamsSaveErr] = useState<string | null>(null)
```

### 4. Populate Teams state on load

- [ ] In `dashboard/src/routes/Settings.tsx`, inside `load()`, in the `if (!cancelled) { ... }` block (after `setWaitingCount(data.queue.waitingCount)` at line 35), add:

```tsx
          setTeamsEnabled(data.teams.enabled)
          setTeamsConfigured(data.teams.webhookUrl !== '')
```

### 5. Add the Teams save handler

- [ ] In `dashboard/src/routes/Settings.tsx`, add after `handleSave` (after line 98):

```tsx
  const handleSaveTeams = useCallback(async () => {
    setTeamsSaving(true)
    setTeamsSaveMsg(null)
    setTeamsSaveErr(null)
    try {
      const body: { webhookUrl?: string; enabled?: boolean } = { enabled: teamsEnabled }
      const trimmed = teamsWebhook.trim()
      if (trimmed !== '') body.webhookUrl = trimmed
      await api.system.updateTeamsConfig(body)
      if (trimmed !== '') setTeamsConfigured(true)
      setTeamsWebhook('')
      setTeamsSaveMsg('Saved successfully.')
    } catch (err) {
      setTeamsSaveErr(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setTeamsSaving(false)
    }
  }, [teamsEnabled, teamsWebhook])
```

### 6. Render the Teams card

- [ ] In `dashboard/src/routes/Settings.tsx`, insert this block immediately after the closing `</div>` of the Queue Configuration card (after line 163, before the `{/* Queue Status (live) */}` comment):

```tsx
      {/* Teams Notifications (editable) */}
      <div className={styles.card}>
        <div className={styles.cardHeader}>Teams Notifications</div>
        <div className={styles.cardBody}>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Status</span>
            <span className={styles.fieldValue}>
              {teamsConfigured ? 'Configured' : 'Not configured'}
            </span>
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Webhook URL</span>
            <input
              className={styles.numberInput}
              type="text"
              placeholder={teamsConfigured ? 'Leave blank to keep current' : 'https://...'}
              value={teamsWebhook}
              onChange={(e) => setTeamsWebhook(e.target.value)}
            />
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Enabled</span>
            <input
              type="checkbox"
              checked={teamsEnabled}
              onChange={(e) => setTeamsEnabled(e.target.checked)}
            />
          </div>
          <div className={styles.fieldRow}>
            <span className={styles.fieldLabel}>Public Base URL</span>
            <span className={styles.fieldValue}>
              {config.server.publicBaseUrl || '(not set — deep links disabled)'}
            </span>
          </div>
          <div className={styles.saveRow}>
            {teamsSaveMsg && <span className={styles.saveMessage}>{teamsSaveMsg}</span>}
            {teamsSaveErr && <span className={styles.saveError}>{teamsSaveErr}</span>}
            <button
              className={styles.saveButton}
              onClick={handleSaveTeams}
              disabled={teamsSaving}
            >
              {teamsSaving ? 'Saving...' : 'Save'}
            </button>
          </div>
        </div>
      </div>
```

### 7. Run — confirm the component tests PASS

- [ ] Run:

```bash
cd dashboard && npx vitest run src/routes/Settings.test.tsx
```

- [ ] Expected:

```
 ✓ src/routes/Settings.test.tsx (6 tests)
 Test Files  1 passed (1)
```

### 8. Typecheck + full dashboard test sweep (no regressions)

- [ ] Run:

```bash
cd dashboard && npx tsc --noEmit && npx vitest run
```

- [ ] Expected: typecheck clean (exit 0); all test files pass, including the existing component/hook tests.

### 9. Commit

- [ ] Run:

```bash
git add dashboard/src/routes/Settings.tsx dashboard/src/routes/Settings.test.tsx
git commit -m "feat(dashboard): Teams Notifications settings card"
```
