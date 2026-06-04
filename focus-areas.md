# Focus Areas — Dashboard UX Improvements

Ordered, prioritized work to reach the Definition of Done in `RALPH.md`. Work the
lowest-numbered incomplete item first; one focus area per iteration. Each item
links to the gap(s) it closes in `gaps-identified.md`. Mark `[x]` only when its
checklist passes and the relevant tests are green.

All 10 implementation plan tasks are committed and both suites are green at seed
time, so the list leads with coverage completeness gaps rather than missing
behavior.

---

## ~~1. QueueSection dismissed-row dimming coverage~~  ✅ DONE ⟶ GAP-001 (MEDIUM)

~~`QueueSection.test.tsx` had no test that an instance with `dismissed: true` yields
a dimmed row with a restore control, leaving the `dimmed={inst.dismissed === true}`
passthrough in `QueueSection.tsx` uncovered.~~ **Closed:** added three tests under
`'QueueSection — dismissed row rendering'` covering dismissed→restore, non-dismissed
terminal→dismiss, and active→no control. 205 → 208 tests, all green.

- [x] Added test: dismissed instance renders restore control (not dismiss); clicking
      calls `onRestore(id)`.
- [x] Added test: non-dismissed terminal instance renders dismiss control; clicking
      calls `onDismiss(id)`.
- [x] Added test: active (running) instance renders no dismiss or restore control.
- [x] Ran `npx vitest run` — 208 tests pass; `tsc --noEmit` clean.

## 2. GraphView integration coverage for failed/static-preview centering  ⟶ GAP-002 (MEDIUM)

`GraphView.test.tsx` covers initial centering for a running instance (active node)
and a completed instance (final node). Two spec-required centering cases are not
tested at the integration level:
- **Failed instance** → should center on the failed node.
- **Static workflow preview** (no instance) → should center on the entry node.

`selectAnchorNodeId` unit tests cover both branches; the gap is that broken wiring
in `GraphView` specifically for these branches would go undetected.

- [ ] Add test to `GraphView.test.tsx`: failed instance with `executionStates` marking
      node `b` as `failed`; assert `setCenter` called on node `b`'s position at
      `DEFAULT_ZOOM=0.9`.
- [ ] Add test to `GraphView.test.tsx`: `instance={null}` (static preview with an
      entrypoint node flagged); assert `setCenter` called on the entry node's position.
- [ ] Run `cd dashboard && npx vitest run` — all tests pass.
