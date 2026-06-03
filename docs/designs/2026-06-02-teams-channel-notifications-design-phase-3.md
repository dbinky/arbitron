# Teams Channel Notifications — Design Phase 3: Dashboard Settings UI

## Goal

Let an admin view and edit the Teams notification settings from the existing
Settings page, reusing its established card/save patterns. Depends on the
Phase 1 HTTP surface (`GET /system/config`, `PUT /system/config/teams`).

## Decisions (from brainstorm)

- Surfaced in the existing `Settings.tsx`, as a new card alongside Queue
  Configuration.
- The webhook URL is returned **redacted** by the API, so the UI treats it as a
  secret: it shows a "configured / not configured" state and never renders the
  raw value into the input.
- The public base URL is **read-only** in the UI (it is set via server config in
  Phase 1).

## Components

### Types — `dashboard/src/api/types.ts`

Extend `SystemConfig` with:

```ts
teams: { enabled: boolean; webhookUrl: string } // webhookUrl is "" or "**REDACTED**"
server: { /* ...existing... */ publicBaseUrl: string }
```

### API client — `dashboard/src/api/client.ts`

Add `api.system.updateTeamsConfig(body: { webhookUrl?: string; enabled?: boolean })`
→ `PUT /system/config/teams`. Omitting `webhookUrl` leaves the stored secret
unchanged.

### `Settings.tsx`

A new **"Teams Notifications"** card after the Queue Configuration card:

- A text input for the webhook URL. Because the fetched value is redacted, the
  input starts **blank** with a "Configured" / "Not configured" indicator
  derived from whether the fetched `webhookUrl` is non-empty. An empty input on
  Save means "leave the existing URL unchanged" (the PUT omits `webhookUrl`); a
  non-empty value replaces it.
- An **enabled** checkbox/toggle, prefilled from the fetched config.
- A **Save** button reusing the existing `saving` / `saveMsg` / `saveErr` state
  pattern, calling `updateTeamsConfig`.
- A **read-only** display of `server.publicBaseUrl` with a hint that it is set
  via server config (so admins understand where deep-link URLs come from).

## Data Flow

On mount, the existing `api.system.getConfig()` load populates the Teams card
(`enabled`, configured-indicator, base URL). Save → `updateTeamsConfig` →
`PUT /system/config/teams`. No new WebSocket events are required; the card is a
simple load/edit/save form like the queue config.

## Redaction Handling

- The redacted sentinel (`"**REDACTED**"`) is **never** placed into the input
  value; it only drives the configured/not-configured indicator.
- Submitting a blank URL omits `webhookUrl` from the PUT body, preserving the
  stored secret; submitting a new value replaces it.

## Error Handling

- Initial config load failure → the page's existing top-level error path.
- Save failure (e.g. `400` from invalid URL) → surfaced via the existing
  `saveErr` message; the form does not crash and remains editable.

## Testing Strategy — STRICT TDD (all five categories required)

Tests (Vitest + React Testing Library) are written **before** implementation.

- **Happy-path:** the card renders the fetched `enabled` state and a
  "Configured" / "Not configured" indicator from the redacted config.
- **Success:** entering a URL, toggling enabled, and clicking Save calls
  `updateTeamsConfig` with the expected arguments and shows the success message.
- **Failure:** the API returns `400` → an error message renders; no crash; form
  stays usable.
- **Error:** initial load / network failure → the existing error path renders;
  a save-time network error surfaces via `saveErr`.
- **Edge-case:** a blank-URL Save omits `webhookUrl` (secret preserved);
  toggling off with no URL configured is allowed; the redacted sentinel is never
  rendered into the input value.

## YAGNI

No per-user settings, no message preview, no test-send button, no multi-channel
UI — a single global Teams card.
