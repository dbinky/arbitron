# Gaps Identified — Teams Channel Notifications

Concrete, verified gaps between the current code and the spec/designs/plans.
Each maps to a focus area in `focus-areas.md`. Check an item off only when it is
closed with a test (or a recorded, deliberate decision). Only the user may move
items into Won't Fix.

Verified at seed time: all 10 plan tasks committed (`dbc1256` → `c34b6c6`);
`go build ./...`, `go vet ./...`, `go test ./...` clean; dashboard Teams suites
8/8. These gaps are about robustness, coverage completeness, and shipping the
built artifact — **not** missing behavior.

## Open Issues

### GAP-001: Embedded dashboard `dist` is stale — Teams card invisible on a real server run  (HIGH)
**Area:** `adapter/dashboard/dist` (embedded via `//go:embed`), `dashboard/src/routes/Settings.tsx`
**Focus area:** 1
**What's wrong:** `dashboard/src` gained the "Teams Notifications" card in commit
`c34b6c6`, but `adapter/dashboard/dist` was last rebuilt in merge `9829cf9`
(pre-Teams). `grep -rl "Teams Notifications" adapter/dashboard/dist` returns
nothing. Because a host `go build` / `./arbitron serve` serves the embedded
`adapter/dashboard/dist`, the Phase 3 card never appears in a real run — so the
spec's "admins configure … in the existing System Configuration area" criterion
and the Phase 3 design outcome are unmet outside `vitest`.
**Fix:** `make ui` (or `cd dashboard && npm run build` then copy to
`adapter/dashboard/dist`); verify the card string is present; commit the refreshed
artifact. The Makefile explicitly requires committing the refreshed dist so it
does not drift.

### GAP-002: `make test` excludes the dashboard suite  (MEDIUM)
**Area:** `Makefile`, CI/verification expectations
**Focus area:** 2
**What's wrong:** `make test` is only `go test ./...`. The entire Phase 3 surface
(`client.test.ts`, `Settings.test.tsx`, `tsc --noEmit`) is outside it, so "full
suite green" can silently skip the dashboard. RALPH.md's DoD now requires both;
this gap tracks making that operationally true (run both each iteration; optional
`test-all` target).

### GAP-003: No end-to-end engine → Teams coverage  (MEDIUM)
**Area:** `domain/engine/engine.go` (`emitNotification`), `adapter/notification/*`
**Focus area:** 3
**What's wrong:** Units are covered in isolation, but no test exercises the real
seam `emitNotification` → `MultiNotifier` → `TeamsNotifier` → webhook for each of
the three notification types. The spec's headline outcome ("every
notification-raising event produces a corresponding Teams message") is therefore
asserted only by construction, not by an integration test. `default_notifier_test.go`
starts at the factory, bypassing the engine.
**Fix:** Add an engine-level test with an `httptest` Teams stub asserting a card
per type, plus an assertion that the in-app notification path fires regardless of
Teams delivery outcome.

### GAP-004: Missing TDD scenario categories on three public functions  (MEDIUM)
**Area:** `adapter/http/server.go`, `adapter/notification/teams_payload.go`, `adapter/mcp/tools_system.go`
**Focus area:** 4
**What's wrong:** The designs mandate all five categories (happy/success/failure/
error/edge) per public function. Gaps found:
- HTTP `UpdateTeamsConfig`: the Phase 1 *error* category lists an over-limit body
  (`MaxBytesReader`) being rejected, but only the malformed-JSON error case is
  tested.
- `buildTeamsPayload`: has happy + edge; failure/error are absent. For a pure
  `json.Marshal` of a fixed struct this is arguably N/A — but that should be a
  recorded decision, not an unexplained omission.
- MCP `system_update_teams_config`: the non-string `webhookUrl` / non-bool
  `enabled` type-error branches exist in the handler; confirm they are exercised
  and add whichever is missing.
**Fix:** Add the missing tests (TDD: red first), or document N/A explicitly.

### GAP-005: Notifier path robustness — nil logger & timeout  (LOW)
**Area:** `adapter/notification/teams_notifier.go`, `multi_notifier.go`
**Focus area:** 5
**What's wrong:** `TeamsNotifier`/`MultiNotifier` call `logger.Warn(...)` on the
failure/error paths; a `nil` logger would panic — there is no nil guard or
documented non-nil contract with a test. Separately, the `defaultTeamsTimeout`
bound is not proven by a slow-endpoint/context-deadline test.
**Fix:** Default to a discard logger (or require non-nil with a test), and add a
timeout test.

### GAP-006: Config-example & deep-link documentation polish  (LOW)
**Area:** `adapter/config/loader.go` (`defaultYAMLContent`), README / config docs
**Focus area:** 6
**What's wrong:** Need to confirm the default YAML documents the new keys and their
env-var overrides (`ARBITRON_SERVER_PUBLICBASEURL`,
`ARBITRON_NOTIFICATIONS_TEAMS_WEBHOOKURL` / `_ENABLED`), and that the
deep-link-requires-`public_base_url` behavior (empty base URL ⇒ OpenUrl action
omitted, by design) is documented for operators.

## Won't Fix (Out of Scope — spec Non-Goals)

_Only the user may add to or move items in this section. Pre-seeded from the spec's
documented non-goals so the loop does not spend iterations "fixing" them._

### WF-001: Per-user / per-workflow / per-event-type routing or filtering
Spec Non-Goals. Every notification goes to the single global channel; do not add
routing or filters.

### WF-002: Multiple channels or additional transports (email, Slack, …)
Spec Non-Goals. `MultiNotifier` is the extension point for a *future* channel, but
adding one now is out of scope.

### WF-003: Interactive / actionable Teams messages
Spec Non-Goals. The card carries a read-only deep link only; no acting on a
notification from inside Teams.

### WF-004: Retries, delivery guarantees, delivery-status reporting
Spec Non-Goals. Delivery is best-effort and silent by design; failures are logged
and ignored.

### WF-005: Privacy isolation within the shared channel
Spec Non-Goals. All users' notification events are visible to every channel
member — an accepted consequence of a single global channel for this version.

### WF-006: Webhook URL persistence
Per Phase 1 design, the webhook config is in-memory only (mirrors QueueConfig),
matching the "single global channel, intended for local testing" framing. Do not
add persistence.
