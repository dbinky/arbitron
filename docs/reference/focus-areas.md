# Focus Areas Review Tracking

This document tracks the systematic review of the **Dashboard UX Improvements** (`docs/specs/ui-nits-spec.md`) for correctness, robustness, and spec-spirit alignment.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Loop detection (`dashboard/src/graph/loops.ts` — back-edge classification, body/head/tail/depth derivation) | [x] | [x] | Done |
| 2 | Loop geometry + container overlays (`dashboard/src/graph/loopLayout.ts`, `useGraphLayout.ts`, `nodes/LoopContainerNode.tsx` — bounding box, label rule, dagre grouping/exclusion) | [x] | [x] | Done |
| 3 | Loop back-edge rendering (`dashboard/src/graph/edges/LoopBackEdge.tsx`, edgeTypes, GraphView wiring — side routing, distinct style) | [x] | [x] | Done |
| 4 | Anchor selection + default zoom (`dashboard/src/graph/selectAnchorNodeId.ts`, `constants.ts`, `routes/ExecutionView.tsx` — anchor per state, mount centering at 0.9) | [x] | [x] | Done |
| 5 | Follow-active-node + Recenter control (`dashboard/src/graph/useFollowActiveNode.ts` — follow on advance, pause on manual move, reduced-motion, re-engage) | [x] | [x] | Done |
| 6 | Dismissal persistence (`adapter/sqlite/dismissal_repo.go`, `migrations/003_instance_dismissals.*`, `domain/instance` DismissalRepository — idempotency, scoping, cascade) | [x] | [x] | Done |
| 7 | Dismissal HTTP handlers + routes + acting-user seam (`adapter/http/*`, `currentUser(r)` — dismiss/restore/dismiss-all, guards) | [x] | [x] | Done |
| 8 | Instance list dismissed-marking (`adapter/http/instance_handler.go` — `dismissed` flag, `includeDismissed`, default omit, order preserved) | [x] | [x] | Done |
| 9 | Engine retry resurface + wiring (`domain/engine/engine.go`, `cmd/arbitron/main.go` — Retry→ClearForInstance, SetDismissals, rerun new-instance) | [x] | [x] | Done |
| 10 | Queue dismissal UI (`dashboard/src/routes/QueueView.tsx`, `components/QueueSection.tsx`, `components/InstanceRow.tsx`, `api/client.ts`, `api/types.ts` — [x], [Dismiss All], Show/Hide toggle, dimming, ↺ restore) | [x] | [x] | Done |
| 11 | Domain: loop semantics end-to-end (workflow def back-edge + label → detection → layout/container → on-screen) | [x] | [x] | Done |
| 12 | Domain: viewport behavior (default zoom + follow + manual override) as a cross-cutting graph concern | [ ] | [ ] | |
| 13 | Domain: per-user dismissal state model (per-user scoping, terminal-only, resurface-on-retry) across backend + frontend | [ ] | [ ] | |
| 14 | Vertical slice: dismiss action ([x] → POST /instances/{id}/dismiss → repo → list omits → UI updates) | [ ] | [ ] | |
| 15 | Vertical slice: show/hide dismissed (toggle → list `includeDismissed=true` → marked → dimmed render + ↺ restore) | [ ] | [ ] | |
| 16 | Vertical slice: loop render (workflow definition → detection → layout/container → graph) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 17 | Loop detection + Loop geometry/overlays (loops.ts output feeds loopLayout/useGraphLayout correctly; bodies map to boxes) | [ ] | |
| 18 | Loop geometry + back-edge rendering (container box handed to the back-edge; routing avoids body nodes) | [ ] | |
| 19 | Anchor selection + Follow hook (shared anchor identity; container overlays excluded from candidates; GraphView wiring) | [ ] | |
| 20 | Dismissal repo + HTTP handlers (port contract: method names/signatures, error mapping, currentUser scoping) | [ ] | |
| 21 | List marking + Queue UI (`dismissed`/`includeDismissed` contract match; order preservation drives chronological reveal) | [ ] | |
| 22 | Dismiss slice + engine retry resurface (dismiss then retry clears; item reappears active→terminal) | [ ] | |
| 23 | Cross-cutting: currentUser seam + all dismissal endpoints (per-user scoping consistent everywhere; local-mode correct) | [ ] | |
| 24 | Cross-cutting: GraphView integration (Phase 1 edgeTypes + loop containers coexist with Phase 2 zoom/follow; no fitView regression) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy / success / failure / error / edge).
- The code does what the design doc says it should.
- Error handling is thorough — no silent failures; HTTP guards return correct codes.
- Module boundaries are clean (pure graph helpers free of React; domain ports free of sqlite types).

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance.
- The component is ready to be extended (e.g. Phase 6 swapping the currentUser seam).
- Test assertions test real risks (loop topology edge cases, follow pause/resume, dismissal idempotency), not happy-path ceremony.
- Edge cases handled (no-loop graphs, self-loops, nested loops; empty/degenerate graphs; non-terminal dismiss; restore of non-dismissed).

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- Data flows cleanly across the boundary (loop topology → geometry → edge; repo → handler → list → UI).
- Error propagation works end-to-end.
- No assumptions in one component that the other doesn't satisfy (param/field name contracts, ordering).
- Integration tests cover the seams between the pair, including the shared GraphView `<ReactFlow>` element across Phases 1 and 2.
