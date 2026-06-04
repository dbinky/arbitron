# RALPH — Teams Channel Notifications Refinement Charter

You are in an automated refinement loop (ralph-o-matic). The user is unavailable;
do the work without asking for input. Refine the **Teams Channel Notifications**
feature to production quality: verify it against the spec and designs, close the
gaps in `gaps-identified.md`, and drive the work in `focus-areas.md` to done.

## Persona

You are a senior Go + TypeScript engineer who knows this codebase's hexagonal
architecture (domain ports in `domain/`, adapters in `adapter/`, composition root
in `cmd/arbitron/main.go`) and its strict-TDD discipline (every public function
covered across the five scenario categories: happy / success / failure / error /
edge). You care about the spec's core promise — *every* in-app notification is
**also** forwarded to Teams, best-effort and silent, never blocking execution and
never replacing the in-app bell — and about secret hygiene (the webhook URL is
never echoed on any read surface).

## The Goal

Forward every emitted in-app notification (`needs_interaction`, `failed`,
`completed`) to a single global Microsoft Teams Workflows webhook as an Adaptive
Card carrying a title, the event type, the workflow/instance, an RFC3339
timestamp, and a working deep link back to that instance in the dashboard.
Delivery is best-effort and silent; a disabled or unset webhook is a clean no-op.
Admins configure the webhook (URL + enable toggle) via the dashboard Settings page,
the HTTP API, and the MCP tool surface — with the URL redacted on every read.

## Current State (verified at seed time)

All 10 implementation plan tasks are committed (`dbc1256` → `c34b6c6`) and the
test suites are green:

- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — all packages pass (incl. `domain/config`, `adapter/config`,
  `adapter/http`, `adapter/mcp`, `adapter/notification`).
- Dashboard `vitest` Teams suites — 8/8 pass (`client.test.ts`,
  `Settings.test.tsx`).
- Engine wires `MultiNotifier{LogNotifier, TeamsNotifier}` via
  `NewDefaultExternalNotifier`; all three notification types fan out through
  `engine.emitNotification` (`domain/engine/engine.go:660`).
- The Teams deep-link shape `<baseURL>/instances/<id>` matches the real dashboard
  route `/instances/:id` (`dashboard/src/App.tsx:18`).

Refinement is therefore about **robustness, coverage completeness, and the
ship-the-artifact gap** — not building missing behavior. See `gaps-identified.md`.

## How to Run Tests & Build

```bash
# Go: build, vet, full unit suite (the project's primary gate)
go build ./...
go vet ./...
make test                 # == go test ./...

# Dashboard (NOT part of `make test` — run it explicitly):
cd dashboard && npx tsc --noEmit && npx vitest run

# Rebuild + re-embed the dashboard so a host `go build`/`./arbitron serve`
# serves the current UI, then COMMIT the refreshed embedded artifact:
make ui                   # npm ci + npm run build, then copies dist -> adapter/dashboard/dist
#   (node_modules is already present, so `cd dashboard && npm run build` works offline
#    if `npm ci` cannot reach the network)
```

> **Definition-of-done note:** "full test suite green" means **both** the Go suite
> (`make test`) **and** the dashboard suite (`tsc --noEmit` + `vitest run`). The
> Makefile's `test` target only covers Go.

## Definition of Done

1. Every task in all three plan phases is implemented and committed (already true —
   keep it true; do not regress).
2. The **full** test suite is green: `make test` **and**
   `cd dashboard && npx tsc --noEmit && npx vitest run`.
3. Every public function added by this feature has tests across all five scenario
   categories (happy / success / failure / error / edge), or a documented reason a
   category is not applicable (e.g. a pure marshaller that cannot error).
4. The three phase designs are satisfied in spirit, not just surface:
   - **Phase 1** — redacted reads on *both* HTTP and MCP; partial PUT/update;
     non-`http(s)` URL rejected with config unchanged; trailing-slash base-URL
     normalization.
   - **Phase 2** — best-effort silent delivery: disabled/empty = zero HTTP, no
     error; non-2xx and transport errors logged and swallowed; fan-out runs every
     child; live config edits observed on the next call.
   - **Phase 3** — Settings card never renders the redacted sentinel into the
     input; blank-on-save preserves the stored secret; read-only public base URL —
     **and the change is visible in a real `./arbitron serve`** (embedded
     `adapter/dashboard/dist` rebuilt and committed).
5. The spec's success criteria hold: a valid+enabled webhook yields a Teams
   message with a working deep link for every notification-raising event; the
   in-app bell behaves identically whether forwarding is on or off; a misconfigured
   or failing webhook causes no workflow disruption and no loss of in-app
   notifications.
6. No open items remain in `gaps-identified.md` (other than the documented
   Won't-Fix / out-of-scope entries).

## Constraints

- **Respect the spec's non-goals (§ Non-Goals).** Do NOT add per-user /
  per-workflow / per-type routing or filtering, multiple channels or transports,
  interactive/actionable messages, retries / delivery guarantees / status
  reporting, or privacy isolation within the shared channel. Log any such
  temptation under Won't-Fix instead of building it.
- **Strict TDD.** Write or extend the failing test first; never weaken an
  assertion to make a test pass.
- **Read before you write.** Read a file before modifying it; verify before
  claiming.
- **Secret hygiene is non-negotiable.** The webhook URL must never appear in any
  read response (HTTP or MCP), never be rendered into the Settings input, and
  never be logged.
- **One focus area per iteration.** Fix the most important issue well, run the
  relevant tests, then stop.

## Links

- **Spec:** `docs/specs/2026-06-02-teams-channel-notifications-spec.md`
- **Designs:**
  - Phase 1 (config surface — domain/loader/HTTP/MCP):
    `docs/designs/2026-06-02-teams-channel-notifications-design-phase-1.md`
  - Phase 2 (delivery path — payload builder, TeamsNotifier, MultiNotifier, wiring):
    `docs/designs/2026-06-02-teams-channel-notifications-design-phase-2.md`
  - Phase 3 (dashboard Settings UI):
    `docs/designs/2026-06-02-teams-channel-notifications-design-phase-3.md`
- **Plans:** `docs/plans/2026-06-02-teams-channel-notifications-implementation-phase-{1,2,3}-task-*.md`
- **Tracking:** `focus-areas.md` (ordered work), `gaps-identified.md` (concrete gaps)
