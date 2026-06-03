# Phase 3 — Task 1: Dashboard Types + API Client Method

**Goal:** Extend the `SystemConfig` type with `teams` and `server.publicBaseUrl`, and add `api.system.updateTeamsConfig(body)` calling `PUT /system/config/teams`.

**Architecture:** Frontend API layer — `dashboard/src/api/types.ts` mirrors the backend JSON shape; `dashboard/src/api/client.ts` is the typed REST client built on a `fetch` wrapper (`put` helper sets method, JSON content-type, and body). The new method forwards a partial body so omitting `webhookUrl` preserves the stored secret.

**Tech Stack:** TypeScript, React/Vite app. Tests with Vitest (`vitest run`), `global.fetch` stubbed via `vi.fn()`.

---

## Context

- Edit: `dashboard/src/api/types.ts` (`SystemConfig` at lines 90-105)
- Edit: `dashboard/src/api/client.ts` (`api.system` block at lines 157-167; `put` helper at line 48; `request` uses global `fetch`)
- New test: `dashboard/src/api/client.test.ts`
- Backend contract (Phase 1 Task 3): `PUT /system/config/teams` body `{ webhookUrl?: string; enabled?: boolean }`; `GET /system/config` returns `teams: { enabled, webhookUrl }` (webhookUrl redacted) and `server.publicBaseUrl`.

## Steps

### 1. Write the failing client test

- [x] Create `dashboard/src/api/client.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api } from './client'

describe('api.system.updateTeamsConfig', () => {
  beforeEach(() => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ enabled: true, webhookUrl: '**REDACTED**' }),
    } as Response)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Success: PUTs both fields to the teams endpoint.
  it('PUTs webhookUrl + enabled to /api/v1/system/config/teams', async () => {
    await api.system.updateTeamsConfig({ webhookUrl: 'https://x.example.com', enabled: true })

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/v1/system/config/teams',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ webhookUrl: 'https://x.example.com', enabled: true }),
      }),
    )
  })

  // Edge-case: a toggle-only body omits webhookUrl entirely.
  it('omits webhookUrl when not provided', async () => {
    await api.system.updateTeamsConfig({ enabled: false })

    const init = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1] as RequestInit
    expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
  })
})
```

### 2. Run — confirm it FAILS

- [x] Run:

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```

- [x] Expected: failure — `api.system.updateTeamsConfig is not a function` (TypeScript/runtime error), suite red.

### 3. Extend the `SystemConfig` type

- [x] In `dashboard/src/api/types.ts`, replace the `SystemConfig` interface (lines 90-105):

```ts
/** System configuration returned by /system/config. */
export interface SystemConfig {
  server: { host: string; port: number; publicBaseUrl: string }
  database: { connected: boolean }
  workspace: { path: string }
  llm: { provider: string; apiKey: string }
  logging: { level: string; file: string }
  tracing: { enabled: boolean; exportDir: string }
  queue: {
    globalCap: number
    perUserCap: number
    runningCount: number
    queuedCount: number
    waitingCount: number
  }
  /** webhookUrl is redacted by the API: "" (unset) or "**REDACTED**" (set). */
  teams: { enabled: boolean; webhookUrl: string }
}
```

- [x] In `dashboard/src/api/types.ts`, add a response type after `QueueConfigUpdate` (after line 111):

```ts
/** Teams configuration update response (webhookUrl redacted). */
export interface TeamsConfigUpdate {
  enabled: boolean
  webhookUrl: string
}
```

### 4. Add the client method

- [x] In `dashboard/src/api/client.ts`, add `TeamsConfigUpdate` to the type import block (after `SystemConfig,`):

```ts
  SystemConfig,
  TeamsConfigUpdate,
```

- [x] In `dashboard/src/api/client.ts`, in the `system:` block, add after `updateQueueConfig` (after line 166):

```ts
    /**
     * Update Teams notification config. Omitting `webhookUrl` leaves the stored
     * (redacted) secret unchanged; passing a value replaces it.
     */
    updateTeamsConfig(body: { webhookUrl?: string; enabled?: boolean }): Promise<TeamsConfigUpdate> {
      return put('/api/v1/system/config/teams', body)
    },
```

### 5. Run — confirm it PASSES

- [x] Run:

```bash
cd dashboard && npx vitest run src/api/client.test.ts
```

- [x] Expected:

```
 ✓ src/api/client.test.ts (2 tests)
 Test Files  1 passed (1)
```

### 6. Typecheck the dashboard

- [x] Run:

```bash
cd dashboard && npx tsc --noEmit
```

- [x] Expected: no output, exit code 0.

### 7. Commit

- [x] Run:

```bash
git add dashboard/src/api/types.ts dashboard/src/api/client.ts dashboard/src/api/client.test.ts
git commit -m "feat(dashboard): SystemConfig teams/publicBaseUrl types + updateTeamsConfig client"
```
