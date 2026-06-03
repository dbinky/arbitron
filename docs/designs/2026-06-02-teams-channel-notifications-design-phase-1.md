# Teams Channel Notifications — Design Phase 1: Config Surface (backend)

## Goal

Make the Teams webhook configuration admin-editable at runtime (held in the
in-memory `config.Config`, like queue config), and expose a static, server-level
public base URL for deep links. This phase adds **no delivery behavior** — only
the configuration surface that Phase 2 consumes.

## Decisions (from brainstorm)

- **Storage:** in-memory `config.Config`, not persisted. Mirrors the existing
  queue-config precedent; fits the spec's "single global channel, intended for
  local testing" framing and YAGNI.
- **Public base URL:** a **static, higher-level** config field loaded from the
  config file/env (not UI-editable), conceptually independent of Teams so any
  future notification channel can reuse it.
- **Redaction:** the webhook URL is treated as sensitive and never echoed on
  read, mirroring the existing `APIKey: "**REDACTED**"` pattern in
  `SystemHandler.GetConfig`.

## Components

### Domain — `domain/config/config.go`

- Add a notifications sub-config:
  ```go
  type TeamsConfig struct {
      WebhookURL string
      Enabled    bool
  }
  type NotificationsConfig struct {
      Teams TeamsConfig
  }
  ```
  and a `Notifications NotificationsConfig` field on `Config`.
- Add `PublicBaseURL string` to `ServerConfig` (static, config-file/env only).

### Loader — `adapter/config/loader.go`

- Map koanf keys → the new fields (e.g. `notifications.teams.webhook_url`,
  `notifications.teams.enabled`, `server.public_base_url`).
- Normalize `PublicBaseURL` by trimming any trailing `/` so Phase 2 can
  concatenate `"/instances/<id>"` deterministically.

### HTTP — `adapter/http/server.go` (`SystemHandler`)

- Extend `systemConfigResponse` with a `teams` block
  (`enabled bool`, `webhookUrl string` — **redacted**) and `server.publicBaseUrl`.
  Redaction rule: `"**REDACTED**"` when a URL is set, `""` when unset.
- Add `PUT /system/config/teams` → `UpdateTeamsConfig`, body
  `{ "webhookUrl"?: string, "enabled"?: bool }` using pointer fields so partial
  updates work. Guarded by the existing `h.mu` mutex.
- Validation: when `webhookUrl` is non-empty it must parse as an absolute
  `http`/`https` URL; otherwise `400 BAD_REQUEST` and the config is left
  unchanged. Reuse `http.MaxBytesReader` (1 MiB) as the queue handler does.

### MCP — `adapter/mcp/tools_system.go`

- Surface the Teams config inside the `system_config` tool result **with the
  same redaction as the HTTP read** — report `enabled` plus a redacted webhook
  value (`"**REDACTED**"` when set, `""` when unset), never the raw URL, so the
  "never echoed on read" decision above holds on every read surface, not just
  HTTP.
- Add `system_update_teams_config` mirroring `system_update_queue_config`
  (string `webhookUrl`, string/bool `enabled`), writing the same in-memory
  `srv.deps.Config` so reads stay consistent.

## Data Flow / Integration Seam

The HTTP handler, the MCP handler, and (in Phase 2) the `TeamsNotifier` all read
the **same** `*config.Config` pointer. An admin edit via either surface is
therefore immediately visible to the notifier on its next `Notify` call — no
re-wiring, matching the "toggle takes effect immediately" decision.

## Error Handling

- Malformed JSON body → `400 BAD_REQUEST`.
- Oversized body → rejected by `MaxBytesReader`.
- Non-`http(s)`/unparseable webhook URL → `400`, config unchanged.
- Reads never expose the raw URL.

## Testing Strategy — STRICT TDD (all five categories required)

Tests are written **before** implementation.

- **Happy-path:** loader maps valid `notifications.teams.*` and
  `server.public_base_url` keys onto `Config`.
- **Success:** `PUT /system/config/teams` updates URL + toggle; subsequent
  `GET /system/config` returns redacted `webhookUrl`, correct `enabled`, and the
  `publicBaseUrl`.
- **Failure:** `PUT` with a malformed / non-`http` URL → `400`; config unchanged.
- **Error:** malformed JSON body → `400`; body exceeding the limit → rejected.
- **Edge-case:** unset webhook round-trips as `enabled=false` / `""`; partial
  `PUT` (toggle only) preserves the existing URL; trailing-slash
  `public_base_url` is normalized; MCP `system_update_teams_config` and the HTTP
  handler produce identical in-memory state; the MCP `system_config` read
  redacts the webhook URL identically to the HTTP read.

## YAGNI

No persistence, no per-user/per-workflow/per-type routing, no multi-channel
config — a single global Teams webhook only.
