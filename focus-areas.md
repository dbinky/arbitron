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

## ~~3. Dismissal HTTP handler error-scenario coverage~~  ✅ DONE ⟶ GAP-003 (MEDIUM)

~~The dismissal HTTP tests were missing three error-category paths: invalid (non-UUID)
instance ID → 400; repo write failure → 500; DismissAll when List fails → 500.~~
**Closed:** added `errDismissalRepo` mirror mock; widened `newDismissalTestServer`
to accept the `DismissalRepository` interface; added five tests for the missing
error paths.

- [x] Added `errDismissalRepo` always-error mock (parallel to `errInstanceRepo`).
- [x] Added `TestDismiss_InvalidID_400` and `TestRestore_InvalidID_400` (non-UUID URL).
- [x] Added `TestDismiss_RepoError_500` and `TestRestore_RepoError_500` (repo write fails).
- [x] Added `TestDismissAll_ListError_500` (instances.List fails).
- [x] `go test ./adapter/http/...` and `make test-all` green.

## ~~2. GraphView integration coverage for failed/static-preview centering~~  ✅ DONE ⟶ GAP-002 (MEDIUM)

~~`GraphView.test.tsx` lacked integration coverage for failed-instance and
static-preview centering — both covered only at the `selectAnchorNodeId` unit
level.~~ **Closed:** added two tests under `'GraphView — default-zoom centering on
mount'`: one for a failed instance (centers on failed node) and one for a static
preview with `instance={null}` (centers on entry node). 208 → 210 tests, all green.

- [x] Added test: failed instance (`state:'failed'`, `failedNodeId:'b'`, trace marking
      'b' failed) → `setCenter` called on node 'b' at `DEFAULT_ZOOM=0.9`.
- [x] Added test: static preview (`instance={null}`, node 'a' has `entrypoint:true`) →
      `setCenter` called on node 'a' at `DEFAULT_ZOOM=0.9`.
- [x] Ran `npx vitest run` — 210 tests pass; `tsc --noEmit` clean.
