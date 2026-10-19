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
- Verified ✅: grep of `\.List(` in `adapter/http` and `adapter/mcp` shows the
  only production instance-list path is `h.service.ListEnriched(...)` at
  `adapter/http/instance_handler.go:384` and
  `srv.deps.Engine.ListEnriched(ctx, filters)` at
  `adapter/mcp/tools_instance.go:115`. The sole `instances.List` call is in
  `DismissAll` (`instance_handler.go:629`), which needs raw IDs, not enriched
  data — correct by design.

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
  - **Fixed**: `TestRouter_InstanceList_RunNameDegradesToShortID_NoVariables` at
    `adapter/http/instance_list_enriched_e2e_test.go` proves nil Variables → short-id
    fallback (no " — " separator). The positive probe
    `TestRouter_InstanceList_EnricherProvenanceProbe` already asserts the derived name.
- **Submitter** — assert the sink shows the resolved `Username`/account identity from
  the real `UserRepository`, and degrades (not crashes) when the user is absent.
  - **Fixed**: `TestRouter_InstanceList_SubmitterDegradesWhenUserAbsent` at
    `adapter/http/instance_list_enriched_e2e_test.go` proves absent user → empty
    submitterName on the wire.
- **Model / retry** — assert the sink carries `AggregateByInstances` output for an
  instance WITH node executions, and is empty/0 for a queued instance with none.
  - **Fixed**: `TestRouter_InstanceList_ModelRetryProvenanceProbe` + negative
    control `TestRouter_InstanceList_ModelRetryAbsentForQueuedInstance` at
    `adapter/http/instance_list_enriched_e2e_test.go`.
- **Friendly step / backend** — assert the label/backend come from the real snapshot
  node, and are blank for terminal rows (see gap on backend in `gaps-identified.md`).
  - **Fixed**: `TestRouter_InstanceList_TerminalRow_FriendlyStepEmpty` at
    `adapter/http/instance_list_enriched_e2e_test.go` proves completed instance →
    empty friendlyStep on the wire. `TestRouter_InstanceList_BackendUnset_EmptyOnWire`
    proves BackendUnset → empty backend on the wire.
- **Density preference** — a test proving the default is `balanced` and a persisted
  value reloads from `localStorage` (corrupt value → `balanced`, no throw).
  - **Fixed**: `dashboard/src/hooks/useDensityPreference.test.ts` covers all five
    categories: default, cycle, persist, reload, corrupt-value, storage-throws-read,
    storage-throws-write. Client-side concern, vitest coverage is appropriate.

`adapter/http/instance_list_enriched_e2e_test.go` already exercises the wired vs.
un-wired enriched path — use it as the template and confirm each field above is
asserted at the wire, not only in a pure unit test.

## 7. Enricher per-row instance load (N+1) vs. design's optimization intent

- Finding: `Enrich` calls `e.instances.Get(ctx, s.ID)` **per row**
  (`domain/engine/enricher.go:105`) and `readWorkflowInputs` marshals→unmarshals the
  snapshot per row (`:206`), with `FindNodeByID` parsing the snapshot again
  (`:174`). Design Phase 1 explicitly **deferred to plans** "distinct-snapshot
  caching across rows that share a workflow" — there is currently NO such caching.
- Decision: accepted, documented cost. The per-row Get is a single-row SQLite lookup
  (primary key, fast). Snapshot re-parse is bounded by page size (default 50). A
  future optimization could add a `GetBatch` method to `InstanceRepository` and a
  snapshot cache keyed by workflow name, but this is not a correctness issue. The
  existing `TestInstanceEnricher_DuplicateOwnerIDs_DedupedToOneLookup` proves the
  user lookup is already batched; the instance Get is the remaining per-row cost.

## 8. Backend column for inheriting nodes (BackendUnset) — escalation, not no-op

- Finding: `resolveNodeFields` sets backend only when
  `node.Config.Backend != workflow.BackendUnset` (`domain/engine/enricher.go:183`);
  the *effective* backend is not persisted on `NodeExecution`. So the model/backend
  column is **permanently blank for any node that inherits its backend** — a value
  the spec lists as a sortable column.
- **Fixed**: `NewInstanceEnricher` now accepts a `defaultAgentBackend` parameter.
  `resolveNodeFields` falls back to `e.defaultAgentBackend` when
  `node.Config.Backend == BackendUnset`. The composition root
  (`cmd/arbitron/main.go:782`) passes `cfg.LLM.DefaultAgentBackend`. Added
  `TestInstanceEnricher_BackendUnset_ResolvesToDefault` proving the resolution.
  The existing `TestInstanceEnricher_BackendUnset_EmptyBackend` still passes
  (no default → empty cell).

## 9. Default ordering policy parity (Phase 2 design ↔ Phase 3 client sort)

- Verified ✅: `QueueSection.test.tsx:78-103` asserts active-by-elapsed-desc
  (`createdAt` asc), queued-by-position-asc, and terminal-by-updatedAt-desc
  defaults. `QueueSection.tsx:27-34` (`defaultSortKey`) implements exactly the
  Phase 2 policy. No gap.

## 10. Polish & spec-spirit sweep — ✅ Complete

- Verified ✅: "you" rendering in local mode vs. display name + account identity
  (row detail) in shared mode, keyed off `authMode` — `QueueTableRow.tsx:42-43`,
  `RowDetailPanel.tsx:42-47`.
- Verified ✅: all missing/optional fields render placeholders (`'—'`), not crashes
  — `queueColumns.ts` accessors use `?? '—'` fallbacks; enricher degrades per-field.
- Verified ✅: last-error lazy-load in `RowDetailPanel` isolates failure to the
  open panel — `RowDetailPanel.test.tsx` covers fetch failure, absent failedNodeId.
- Verified ✅: no spec non-goal crept in — no new queue actions, no execution changes.
