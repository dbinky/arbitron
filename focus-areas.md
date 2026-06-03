# Focus Areas — Teams Channel Notifications

Ordered, prioritized work to reach the Definition of Done in `RALPH.md`. Work the
lowest-numbered incomplete item first; one focus area per iteration. Each item
links to the gap(s) it closes in `gaps-identified.md`. Mark `[x]` only when its
checklist passes and the relevant tests are green.

The implementation landed green at seed time (`go build`/`go vet`/`go test ./...`
clean; dashboard Teams suites 8/8). There are **no failing tests** today, so the
list leads with the one gap that breaks the feature's *observable* behavior, then
coverage and end-to-end-confidence gaps, then polish.

---

## ~~1. Ship the dashboard UI — rebuild & commit the embedded `dist`~~  ✅ DONE ⟶ GAP-001 (HIGH)

~~The Phase 3 "Teams Notifications" card exists in `dashboard/src` but the embedded
`adapter/dashboard/dist` artifact predates it.~~ **Closed:** rebuilt `dashboard/dist`
offline (`npm run build`) and copied it to `adapter/dashboard/dist`. A host
`go build` now embeds the card via `//go:embed`.

- [x] Run `make ui` (or `cd dashboard && npm run build` offline, then copy
      `dashboard/dist` → `adapter/dashboard/dist`).
- [x] Confirm the rebuilt artifact contains the card: `grep -rl "Teams
      Notifications" adapter/dashboard/dist` returns a match
      (`adapter/dashboard/dist/assets/index-BRHMsNHu.js`).
- [x] Commit the refreshed `adapter/dashboard/dist` so the checked-in artifact no
      longer drifts from `dashboard/src`.

## ~~2. Make the full test suite the gate — include the dashboard suite~~  ✅ DONE ⟶ GAP-002 (MEDIUM)

~~`make test` runs only `go test ./...`; the Vitest + `tsc` dashboard tests are not
part of it, so "full suite green" can silently exclude the entire Phase 3 surface.~~
**Closed:** added `test-ui` (`tsc --noEmit` + `vitest run`) and `test-all`
(`test test-ui`) targets to the `Makefile`, so the full Definition-of-Done gate is
a single runnable command instead of only prose in `RALPH.md`.

- [x] Verified `cd dashboard && npx tsc --noEmit && npx vitest run` is green
      (121 tests pass; tsc clean).
- [x] Made the two-suite expectation explicit and runnable: `make test-all` runs
      the Go suite then the dashboard suite. Confirmed green end-to-end.

## ~~3. End-to-end engine → Teams seam coverage~~  ✅ DONE ⟶ GAP-003 (MEDIUM)

~~Each piece is unit-tested, but nothing proves the spec's headline outcome —
"every notification-raising event produces a corresponding Teams message" — across
the real seam `engine.emitNotification` → `MultiNotifier` → `TeamsNotifier` → HTTP.~~
**Closed:** added `domain/engine/engine_teams_integration_test.go`, which drives a
real `Engine` wired with `SetExternalNotifier(NewDefaultExternalNotifier(cfg, …))`
against an `httptest` Teams webhook stub and asserts a parsed Adaptive Card
(Event/Workflow/Instance facts + deep-link action) for **each** of the three types.

- [x] Added a test that drives `Engine` (with `SetExternalNotifier(NewDefaultExternalNotifier(cfg, …))`
      and a stub notification repo) and asserts an `httptest` Teams stub receives a
      card for **each** of the three types: `TestEngine_TeamsSeam_CompletedForwardsCard`,
      `_FailedForwardsCard`, `_NeedsInteractionForwardsCard` (the last via an
      interactive node returning `IsWait`; fixtures now register
      `NodeTypeInteractive`). Deep link asserted as `<baseURL>/instances/<id>`.
- [x] Asserted the in-app path (notification repo `Create` + `notification.created`
      event) still fires when Teams delivery fails (stub returns 500):
      `TestEngine_TeamsSeam_InAppFiresWhenTeamsFails` — instance still completes,
      the in-app notification is persisted, and the bell event is published.

## 4. Fill the missing TDD scenario categories  ⟶ GAP-004 (MEDIUM)

The designs require all five categories per public function. A few are missing:

- [ ] **HTTP** `UpdateTeamsConfig` — add the *error* case the Phase 1 design calls
      out: an over-limit body is rejected by `MaxBytesReader` (the malformed-JSON
      error case exists; the oversize case does not).
- [ ] **Payload builder** `buildTeamsPayload` — it has happy + edge; document (or
      assert) why *failure/error* are N/A for a pure `json.Marshal` of a
      fixed-shape struct, so the omission is a deliberate, recorded decision rather
      than a gap.
- [ ] **MCP** `system_update_teams_config` — confirm the non-string `webhookUrl`
      and non-bool `enabled` type-error branches are exercised (handler returns an
      error result); add the missing one(s).

## 5. Robustness polish on the notifier path  ⟶ GAP-005 (LOW)

- [ ] `MultiNotifier`/`TeamsNotifier` guard against a `nil` logger (today a nil
      logger would panic on the failure/error paths). Either require non-nil at
      construction with a documented contract + test, or default to a discard
      logger.
- [ ] Confirm the `TeamsNotifier` HTTP timeout (`defaultTeamsTimeout`) actually
      bounds a hung endpoint via a context-deadline / slow-server test.

## 6. Documentation & config-example polish  ⟶ GAP-006 (LOW)

- [ ] Verify the default YAML comments (`adapter/config/loader.go`
      `defaultYAMLContent()`) document `server.publicbaseurl`,
      `notifications.teams.webhookurl`/`enabled`, and the env-var overrides
      (`ARBITRON_SERVER_PUBLICBASEURL`,
      `ARBITRON_NOTIFICATIONS_TEAMS_WEBHOOKURL`/`_ENABLED`).
- [ ] Note in the README / configuration docs that deep links require
      `server.public_base_url` to be set (empty base URL → the card omits the
      OpenUrl action by design).
