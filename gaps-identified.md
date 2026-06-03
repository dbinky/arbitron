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

### ~~GAP-001: Embedded dashboard `dist` is stale — Teams card invisible on a real server run~~  ✅ CLOSED (HIGH)
**Area:** `adapter/dashboard/dist` (embedded via `//go:embed`), `dashboard/src/routes/Settings.tsx`
**Focus area:** 1
**What was wrong:** `dashboard/src` gained the "Teams Notifications" card in commit
`c34b6c6`, but `adapter/dashboard/dist` was last rebuilt pre-Teams, so the card
never appeared in a real `./arbitron serve` run.
**Closed:** rebuilt the dashboard offline (`npm run build`) and copied
`dashboard/dist` → `adapter/dashboard/dist`. `grep -rl "Teams Notifications"
adapter/dashboard/dist` now matches (`assets/index-BRHMsNHu.js`). `go build ./...`,
`make test`, and `cd dashboard && npx tsc --noEmit && npx vitest run` (121 tests)
all green with the refreshed artifact embedded. Committed.

### ~~GAP-002: `make test` excludes the dashboard suite~~  ✅ CLOSED (MEDIUM)
**Area:** `Makefile`, CI/verification expectations
**Focus area:** 2
**What was wrong:** `make test` was only `go test ./...`. The entire Phase 3 surface
(`client.test.ts`, `Settings.test.tsx`, `tsc --noEmit`) was outside it, so "full
suite green" could silently skip the dashboard.
**Closed:** added `test-ui` (`cd dashboard && npx tsc --noEmit && npx vitest run`)
and `test-all` (`test test-ui`) Make targets. The full DoD gate is now one command,
`make test-all`; confirmed green (Go suite + 121 dashboard tests, tsc clean).

### ~~GAP-003: No end-to-end engine → Teams coverage~~  ✅ CLOSED (MEDIUM)
**Area:** `domain/engine/engine.go` (`emitNotification`), `adapter/notification/*`
**Focus area:** 3
**What was wrong:** Units were covered in isolation, but no test exercised the real
seam `emitNotification` → `MultiNotifier` → `TeamsNotifier` → webhook for each of
the three notification types. The spec's headline outcome was asserted only by
construction. `default_notifier_test.go` starts at the factory, bypassing the engine.
**Closed:** added `domain/engine/engine_teams_integration_test.go`. Four tests drive
a real `Engine` wired with `NewDefaultExternalNotifier(cfg, …)` against an `httptest`
Teams webhook stub that parses each received Adaptive Card:
- `_CompletedForwardsCard` (happy) — asserts Event/Workflow/Instance facts and the
  `<baseURL>/instances/<id>` deep-link action.
- `_FailedForwardsCard` (failure) — failing node ⇒ `failed` card.
- `_NeedsInteractionForwardsCard` (edge) — interactive node returning `IsWait` ⇒
  `needs_interaction` card (fixtures now register `NodeTypeInteractive`).
- `_InAppFiresWhenTeamsFails` (error/best-effort) — stub returns 500; the instance
  still completes, the in-app notification is persisted, and the `notification.created`
  bell event still fires. Proves Teams is *in addition to*, never replaces, the bell.
All four green; full `go test ./...` + `go vet ./...` clean.

### ~~GAP-004: Missing TDD scenario categories on three public functions~~  ✅ CLOSED (MEDIUM)
**Area:** `adapter/http/server.go`, `adapter/notification/teams_payload.go`, `adapter/mcp/tools_system.go`
**Focus area:** 4
**What was wrong:** The designs mandate all five categories (happy/success/failure/
error/edge) per public function. Three were missing.
**Closed:**
- HTTP `UpdateTeamsConfig`: added `TestRouter_UpdateTeamsConfig_OversizeBody` —
  a body past the 1 MiB `MaxBytesReader` limit (otherwise-valid `webhookUrl`) is
  rejected 400, config unchanged. Genuinely exercises the limiter, not just the
  decode path.
- `buildTeamsPayload`: added `TestBuildTeamsPayload_NeverErrors_RecordedNA`, which
  records the deliberate failure/error = N/A decision and pins it: the only
  fallible op is `json.Marshal` of fixed string-only structs (no
  channels/funcs/cyclic pointers/custom Marshalers ⇒ unreachable error). Asserts
  no error + valid JSON across every escaping path; will fail loudly if a fallible
  field is ever added.
- MCP `system_update_teams_config`: added
  `TestSystemUpdateTeamsConfig_WebhookURLNotString` and
  `TestSystemUpdateTeamsConfig_EnabledNotBool` — both type-error branches now
  assert the error message and that config is left unchanged.
All new tests green; `go build ./...`, `go vet ./adapter/...`, `go test ./...` clean.

### ~~GAP-005: Notifier path robustness — nil logger & timeout~~  ✅ CLOSED (LOW)
**Area:** `adapter/notification/teams_notifier.go`, `multi_notifier.go`
**Focus area:** 5
**What was wrong:** `TeamsNotifier`/`MultiNotifier` call `logger.Warn(...)` on the
failure/error paths; a `nil` logger would panic — there was no nil guard or
documented non-nil contract with a test. Separately, the `defaultTeamsTimeout`
bound was not proven by a slow-endpoint test.
**Closed:** both constructors now default a nil logger to
`slog.New(slog.DiscardHandler)` with a documented contract. TDD tests
`TestTeamsNotifier_Notify_NilLoggerNoPanic` and
`TestMultiNotifier_Notify_NilLoggerNoPanic` panicked before the guard and pass
after. Added `TestTeamsNotifier_Notify_TimeoutBoundsHungEndpoint` — a blocking
handler + short-timeout client; Notify returns nil within the bound and logs the
swallowed transport error, proving a hung endpoint cannot stall the path.
`go build ./...`, `go vet ./adapter/notification/`, `make test` all green.

### ~~GAP-006: Config-example & deep-link documentation polish~~  ✅ CLOSED (LOW)
**Area:** `adapter/config/loader.go` (`defaultYAMLContent`), README / config docs
**Focus area:** 6
**What was wrong:** Needed to confirm the default YAML documents the new keys/env
overrides, and that the deep-link-requires-`public_base_url` behavior is documented
for operators. Inspection also surfaced that `README.md` still described Teams as a
*future/deferred* notification transport, contradicting the now-shipped feature.
**Closed:**
- The default YAML (`defaultYAMLContent`) already documents `server.publicbaseurl`
  (lines 68–72), `notifications.teams.webhookurl`/`enabled` (lines 124–131), and
  every env override (`ARBITRON_SERVER_PUBLICBASEURL`,
  `ARBITRON_NOTIFICATIONS_TEAMS_WEBHOOKURL`/`_ENABLED`). No change needed;
  `loader_test.go` already asserts these keys load from YAML.
- `README.md` corrected: the **Notifications** bullet now documents optional Teams
  forwarding (best-effort/silent) and the deep-link → `server.public_base_url`
  requirement (empty ⇒ OpenUrl action omitted by design); the **Scope Boundaries**
  "Deferred" list no longer counts Teams among unshipped transports.
`go build ./...`, `go vet ./...`, `go test ./adapter/config/` all green.

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
