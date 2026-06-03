# Teams Channel Notifications — Design Phase 2: Delivery Path

## Goal

Forward **every** emitted notification to the configured Teams channel —
best-effort and silent — using a fan-out composite notifier. This phase realizes
the spec's core behavior. It depends only on the config types from Phase 1.

## Decisions (from brainstorm)

- **Composition:** a fan-out `MultiNotifier` implementing the existing
  `notification.ExternalNotifier` port; the engine keeps its single-port wiring,
  and future channels just join the slice.
- **Gating** lives inside the `TeamsNotifier`, reading the **live** in-memory
  config each call. Disabled or empty URL ⇒ clean no-op, never an error.
- **Payload:** an Adaptive Card (the format the modern Teams *Workflows* webhook
  accepts), produced by a pure builder function.

## Existing seam (already present)

`domain/notification/external.go` already defines
`ExternalNotifier { Notify(ctx, *Notification) error }`, and the engine already
calls `externalNotifier.Notify(...)` in `emitNotification` (`engine.go:680`),
logging and swallowing any error so delivery never blocks execution. This phase
supplies new implementations of that port; the engine is **unchanged**.

## Components

### Pure payload builder — `adapter/notification/teams_payload.go`

```go
func buildTeamsPayload(n *notification.Notification, baseURL string) ([]byte, error)
```

Produces the Adaptive Card envelope:

```json
{ "type": "message",
  "attachments": [{
    "contentType": "application/vnd.microsoft.card.adaptive",
    "content": { /* Adaptive Card */ }
  }] }
```

The card contains: a **title** (e.g. `"Workflow <name>: <type>"`), a **FactSet**
(event type, workflow name, instance ID, RFC3339 timestamp), and an
`Action.OpenUrl` **deep link** `"<baseURL>/instances/<instanceID>"`. Pure — no
I/O, no clock, no network — so it is trivially unit-testable. When `baseURL` is
empty the OpenUrl action is omitted (the card is still valid).

### `TeamsNotifier` — `adapter/notification/teams_notifier.go`

Implements `ExternalNotifier`. Fields: `*config.Config` (live read),
`publicBaseURL string`, an `*http.Client` with a bounded timeout, and a
`*slog.Logger`. `Notify`:

1. Read live Teams config. If `!Enabled || WebhookURL == ""` → return `nil`
   (no-op, no HTTP).
2. `buildTeamsPayload(n, publicBaseURL)`; `POST` JSON to the webhook with a
   request-scoped context/timeout.
3. Transport error **or** non-2xx response → log at warn/error and return `nil`.
   Failures are never propagated (best-effort, silent).

### `MultiNotifier` — `adapter/notification/multi_notifier.go`

Implements `ExternalNotifier`; holds `[]ExternalNotifier`. `Notify` calls each
child in order, logging and swallowing individual errors, and always returns
`nil`. Adding a channel later is an append.

### Wiring — `cmd/arbitron/main.go`

Replace the lone `LogNotifier` (12b, ~line 416) with
`MultiNotifier{ LogNotifier, TeamsNotifier }`, passing `config` and
`config.Server.PublicBaseURL` into the `TeamsNotifier`.

## Data Flow

`engine.emitNotification` → `MultiNotifier.Notify` → fan-out to `LogNotifier`
(unchanged dev log) and `TeamsNotifier` (gate → build → POST). The in-app bell
path is untouched, satisfying "Teams delivery is in addition to, never replaces,
the in-app notification."

## Error Handling

- Disabled/unset → no-op, zero HTTP calls, no error.
- Webhook 4xx/5xx or unreachable host / transport error → logged, `Notify`
  returns `nil`; workflow execution proceeds.
- One child notifier failing never prevents the others from running.
- HTTP client timeout bounds a hung webhook endpoint.

## Testing Strategy — STRICT TDD (all five categories required)

Tests are written **before** implementation; HTTP is exercised against an
`httptest` stub server.

- **Happy-path:** `buildTeamsPayload` emits a well-formed Adaptive Card with the
  correct title, facts, and deep-link URL.
- **Success:** an enabled `TeamsNotifier` POSTs the expected JSON body to the
  stub; `MultiNotifier` invokes every child.
- **Failure:** webhook responds `500` → error logged, `Notify` returns `nil`,
  execution not blocked.
- **Error:** unreachable host / transport error → logged, `nil` returned; with
  `MultiNotifier`, one child erroring still runs the others.
- **Edge-case:** disabled or empty URL → zero HTTP calls; empty `publicBaseURL`
  → OpenUrl action omitted but card still valid; live config edit between two
  `Notify` calls is observed on the second.

## YAGNI

No retries, no delivery guarantees, no status reporting, no per-type routing —
single global best-effort channel.
