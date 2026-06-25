# Focus Areas — Queue List Visibility (ordered by priority)

One area per iteration, lowest number first. Each is a concrete, named check with
the grep/file evidence gathered at seed time. "Verified ✅" notes what already holds
so the loop confirms rather than re-discovers; the work is the open sub-points.

Priority order: (1) new test failures → (2) whole-pipeline wiring/provenance gaps
→ (3) gaps vs. plans/designs → (4) coverage depth → (5) polish.

---

## 1. New-failure gate — keep the suite at baseline

No work unless a run regresses. Re-run all three commands each iteration and
compare to the baseline in `RALPH.md`.

- Verified ✅ at seed: `go test ./...` = 1 failure, exactly the baseline
  `adapter/config.TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`; vitest
  428/0; tsc clean.
- A new failure (Go failure set not a subset of the baseline, or any vitest/tsc
  break) **preempts all other focus areas** — fix it first.

## 2. Provenance: enrichment step is wired-and-fed at the composition root

- Verified ✅: `engine.NewInstanceEnricher(instanceRepo, nodeExecRepo, userRepo, nil)`
  at `cmd/arbitron/main.go:782` (real repos, not mocks), then
  `eng.SetInstanceEnricher(instanceEnricher)` at `:783`, and
  `instanceEnricher.SetQueuePositionProvider(queueMgr)` at `:792` (real queue
  manager). `Engine.ListEnriched` calls `enricher.Enrich` at `domain/engine/engine.go:528`.
- Open: confirm there is **no second `ListEnriched`-less code path** still serving
  instances un-enriched in production (grep `\.List(` consumers in `adapter/http`
  and `adapter/mcp`). The lean `summaryDTO` must not be what production returns.

## 3. Provenance: HTTP and MCP both consume the enriched path (no nil/empty sink)

- Verified ✅: HTTP `h.service.ListEnriched(...)` + `toEnrichedSummaryDTO`
  (`adapter/http/instance_handler.go:384`, `:250`); MCP
  `srv.deps.Engine.ListEnriched(ctx, filters)` (`adapter/mcp/tools_instance.go:115`).
- Verified ✅: MCP parity test `TestInstanceList_ReturnsEnrichedFields` now asserts
  `submitter` and `retryCount` on the wire alongside the previously-asserted `runName`,
  `friendlyStep`, `model`, and `backend` (`adapter/mcp/tools_instance_test.go:981-986`).

## 4. Provenance: queue position is fed by the real manager, asserted at the SINK

- Verified ✅ (exemplary): `adapter/http/instance_list_queue_position_e2e_test.go`
  asserts `queuePosition` on the **HTTP JSON wire** AND has a load-bearing negative
  control (`TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired`) proving
  the value is absent when the provider is unwired. This is the gold standard.
- Open: confirm `queue.Manager.QueuePositions` (`domain/queue/positions.go`) is the
  ONLY producer of the position and that the enricher never re-derives ordering
  (design Phase 2 "single authority"). Grep `QueuePositions` shows no dashboard- or
  enricher-side re-computation — confirm it stays that way.

## 5. Provenance: dashboard components are mounted in the live tree AND embedded

- Verified ✅: `QueueView.tsx` wires `useDensityPreference(me?.userId ?? null)`,
  `<DensityToggle>`, `authMode`, and passes `density` into every `<QueueSection>`;
  `QueueSection` reads `queueColumns` and renders `QueueTableRow` + `RowDetailPanel`.
  The embedded bundle `adapter/dashboard/dist/assets/*.js` contains the markers
  `balanced`, `density`, `expanded`, `queuePosition`, `runName`, and is timestamped
  after the latest source — i.e. `make ui` was run (commit `112c0dc`).
- Open: any further `dashboard/src` change in this loop MUST be followed by
  `make ui`; re-grep the bundle for the new symbol to prove the served binary
  changed (a green vitest run alone does NOT update the embedded `dist`).

## 6. "Fail-if-nil-fed" coverage per component (anti-over-mocking)

The done-condition requires **at least one test per component that would fail if its
production input were nil/empty**. Queue position has this (area 4). Audit and, where
missing, add the equivalent for:

- **Run name** — `DeriveRunName` (`domain/instance/runname.go`): a test proving the
  HTTP/MCP sink carries the *derived* name from real `Variables`/inputs, and that an
  empty instance still falls back to the short id (ladder's last rung).
- **Submitter** — assert the sink shows the resolved `Username`/account identity from
  the real `UserRepository`, and degrades (not crashes) when the user is absent.
- **Model / retry** — assert the sink carries `AggregateByInstances` output for an
  instance WITH node executions, and is empty/0 for a queued instance with none.
- **Friendly step / backend** — assert the label/backend come from the real snapshot
  node, and are blank for terminal rows (see gap on backend in `gaps-identified.md`).
- **Density preference** — a test proving the default is `balanced` and a persisted
  value reloads from `localStorage` (corrupt value → `balanced`, no throw).

`adapter/http/instance_list_enriched_e2e_test.go` already exercises the wired vs.
un-wired enriched path — use it as the template and confirm each field above is
asserted at the wire, not only in a pure unit test.

## 7. Enricher per-row instance load (N+1) vs. design's optimization intent

- Finding: `Enrich` calls `e.instances.Get(ctx, s.ID)` **per row**
  (`domain/engine/enricher.go:105`) and `readWorkflowInputs` marshals→unmarshals the
  snapshot per row (`:206`), with `FindNodeByID` parsing the snapshot again
  (`:174`). Design Phase 1 explicitly **deferred to plans** "distinct-snapshot
  caching across rows that share a workflow" — there is currently NO such caching.
- Open: confirm whether a plan task implemented this optimization. If not, decide:
  add distinct-snapshot/instance caching for a page (rows sharing a workflow reuse
  one parsed snapshot), or record it as an accepted, documented cost with a test
  bounding it. Correctness is fine; this is a design-spirit / efficiency gap.

## 8. Backend column for inheriting nodes (BackendUnset) — escalation, not no-op

- Finding: `resolveNodeFields` sets backend only when
  `node.Config.Backend != workflow.BackendUnset` (`domain/engine/enricher.go:183`);
  the *effective* backend is not persisted on `NodeExecution`. So the model/backend
  column is **permanently blank for any node that inherits its backend** — a value
  the spec lists as a sortable column.
- Open: this is NOT a tolerated no-op. Either (a) persist/derive the effective
  backend so inheriting nodes show a real value, or (b) escalate it in
  `gaps-identified.md` as a known spec limitation with rationale. Pick one; do not
  leave it silently empty.

## 9. Default ordering policy parity (Phase 2 design ↔ Phase 3 client sort)

- Open: verify the client-side default order matches the Phase 2 policy: Active by
  elapsed desc (`CreatedAt` asc), Pending by queue position asc, Terminal by most
  recent first. Confirm `QueueSection`/`queueColumns` implement exactly this and a
  vitest asserts the queued-by-position-asc and active-by-elapsed-desc defaults.

## 10. Polish & spec-spirit sweep

- "you" rendering in local mode vs. display name + account identity (row detail) in
  shared mode, keyed off `domain/identity.Mode` / `authMode`.
- Long submitted titles, non-string `Variables`, terminal rows with no current step,
  rows missing optional fields — all render placeholders, not crashes.
- Last-error lazy-load in `RowDetailPanel` isolates failure to the open panel.
- Confirm no spec non-goal crept in (no new queue actions, no execution changes).
