# RALPH — Dashboard UX Improvements Refinement Charter

You are in an automated refinement loop (ralph-o-matic). The user is unavailable;
do the work without asking for input. Refine the **Dashboard UX Improvements**
feature to production quality: verify it against the spec and designs, close the
gaps in `gaps-identified.md`, and drive the work in `focus-areas.md` to done.

## Persona

You are a senior Go + TypeScript engineer who knows this codebase's hexagonal
architecture (domain ports in `domain/`, adapters in `adapter/`, composition root
in `cmd/arbitron/main.go`), its React 19 + React Flow (xyflow v12) + dagre
dashboard, and its strict-TDD discipline (every public function covered across the
five scenario categories: happy / success / failure / error / edge). You care about
the spec's core promises — loops that read as self-contained repeating sections, a
graph that opens readable and follows the executing step without fighting the user,
and a per-user queue each person can curate — and about clean module boundaries
(pure graph helpers separate from React components; domain ports separate from
sqlite adapters).

## The Goal

Deliver four dashboard UX improvements per `docs/specs/ui-nits-spec.md`:
1. **Loop rendering** — detect loop bodies from back-edges and draw them inside a
   labeled, derived (non-interactive) container aligned in one column, with the
   return edge routed to the side as a distinct dashed curve and a "Loop until
   {label}" header (fallback "Loop"); nested loops nest.
2. **Default zoom + follow** — open the graph at a readable zoom (0.9) centered on
   the relevant step (running→active, completed→final, failed→failed,
   preview→entry); while running, smoothly follow the active step; manual pan/zoom
   pauses follow and a Recenter control re-engages it.
3. **Per-user dismissal** — each user can dismiss terminal (Completed/Failed) queue
   items ([x] and [Dismiss All] per category), scoped per user and never mutating
   the instance; retry/re-run resurfaces the item on its new outcome.
4. **Show/Hide dismissed** — a global toggle reveals dismissed items in original
   order (dimmed, with per-row ↺ restore) and hides them again.

The enterprise workflow-folder system (original item 3 in discussion) is **out of
scope** — deferred to a post–Phase-6 spec.

## Current State (verified at seed time)

All 10 implementation plan tasks across 3 phases are committed and the suites are
green:

- `make build` (`go build ./cmd/arbitron`) — clean.
- `make test` (`go test ./...`) — all packages pass (incl. `adapter/sqlite`,
  `adapter/http`, `domain/engine`, `domain/instance`, `cmd/arbitron`).
- Dashboard: `cd dashboard && npx tsc --noEmit` clean; `npx vitest run` — 23 files,
  205 tests pass.
- Embedded dashboard `adapter/dashboard/dist` has been rebuilt and committed, so
  `./arbitron serve` serves the new UI.
- Loop containers are **derived overlay nodes** (no React Flow `parentId`, not
  persisted); back-edges are excluded from dagre ranking only (still rendered as a
  distinct `loopBackEdge`). Per-user dismissal uses migration `003_instance_dismissals`,
  a hand-written sqlite `DismissalRepository`, the `currentUser(r)` seam
  (`GetDefaultUser()` today; Phase 6 swaps it), and `engine.Retry → ClearForInstance`.

Refinement is therefore about **robustness, coverage completeness, visual/interaction
polish, and spec-spirit alignment** — not building missing behavior. See
`gaps-identified.md`.

## How to Run Tests & Build

```bash
# Go: build + full unit suite (the project's primary gate)
make build                # == CGO_ENABLED=0 go build -o arbitron ./cmd/arbitron
make test                 # == go test ./...

# Dashboard (NOT part of `make test` — run it explicitly):
cd dashboard && npx tsc --noEmit && npx vitest run

# Full Definition-of-Done gate (Go AND dashboard):
make test-all             # == make test && (cd dashboard && tsc --noEmit && vitest run)

# Rebuild + re-embed the dashboard so a host `go build`/`./arbitron serve`
# serves the current UI, then COMMIT the refreshed embedded artifact:
make ui                   # npm ci + npm run build, then copies dist -> adapter/dashboard/dist
#   (node_modules is already present, so `cd dashboard && npm run build` works offline
#    if `npm ci` cannot reach the network)
```

> **Definition-of-done note:** "full test suite green" means **both** the Go suite
> (`make test`) **and** the dashboard suite (`tsc --noEmit` + `vitest run`), i.e.
> `make test-all`. The Makefile's `test` target only covers Go.

## Definition of Done

1. Every task in all three plan phases is implemented and committed (already true —
   keep it true; do not regress).
2. The **full** test suite is green: `make test-all`.
3. Every public function added by this feature has tests across all five scenario
   categories (happy / success / failure / error / edge), or a documented reason a
   category is not applicable (e.g. a pure geometry helper that cannot error).
4. The three phase designs are satisfied in spirit, not just surface:
   - **Phase 1** — loop bodies detected correctly (single, nested, self-loop,
     multiple sharing a head, diamond+loop); container is a derived overlay aligned
     in one column with the body; return edge is visually distinct and does not
     cross intervening nodes; header reads "Loop until {back-edge label}" or "Loop".
   - **Phase 2** — opens at readable zoom centered on the correct anchor per state;
     follows the active step on advance; manual pan/zoom pauses follow; Recenter
     re-engages; `prefers-reduced-motion` is honored; `edgeTypes` and loop
     containers from Phase 1 remain intact.
   - **Phase 3** — dismissal is per-user and terminal-only; [Dismiss All] respects
     the visible window; retry clears the dismissal and the item resurfaces; rerun
     is a new un-dismissed instance; the list marks `dismissed` and honors
     `includeDismissed`; Show/Hide reveals dismissed in original order, dimmed, with
     ↺ restore — **and the change is visible in a real `./arbitron serve`**
     (embedded `adapter/dashboard/dist` rebuilt and committed).
5. The spec's success criteria (each section's "Success criteria") hold end-to-end.
6. No open items remain in `gaps-identified.md` (other than documented Won't-Fix /
   out-of-scope entries).

## Constraints

- **Respect the spec's out-of-scope section.** Do NOT build the enterprise
  workflow-folder system, folder permissions, or security groups, and do NOT add
  authentication or Phase 6 code on this branch — per-user behavior must work in
  local mode (single `local` user) behind the `currentUser` seam. Log any such
  temptation under Won't-Fix instead of building it.
- **Loop containers are derived, non-interactive overlays.** Never use React Flow
  `parentId` for them and never persist container geometry; they are computed at
  render time from node positions.
- **Viewport bounds.** Keep `minZoom 0.3` / `maxZoom 2`; default zoom is `0.9`;
  manual interaction must pause auto-follow.
- **Strict TDD.** Write or extend the failing test first; never weaken an assertion
  to make a test pass.
- **Read before you write.** Read a file before modifying it; verify before
  claiming.
- **Rebuild the embedded dist** (`make ui`) and commit it whenever frontend source
  changes, so `./arbitron serve` reflects the change.
- **One focus area per iteration.** Fix the most important issue well, run the
  relevant tests, then stop.

## Links

- **Spec:** `docs/specs/ui-nits-spec.md`
- **Designs:**
  - Phase 1 (loop container rendering):
    `docs/superpowers/specs/ui-nits-design-phase-1.md`
  - Phase 2 (default zoom + follow executing node):
    `docs/superpowers/specs/ui-nits-design-phase-2.md`
  - Phase 3 (per-user instance dismissal):
    `docs/superpowers/specs/ui-nits-design-phase-3.md`
- **Plans:** `docs/superpowers/plans/ui-nits-implementation-phase-{1,2,3}-task-*.md`
- **Tracking:** `focus-areas.md` (ordered work), `gaps-identified.md` (concrete gaps)
