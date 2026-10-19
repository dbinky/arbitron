# Gaps Identified — Queue List Visibility

Concrete gaps between current code and the spec/designs/plans, found by reading the
code and grepping for provenance at seed time. Move items to `## Fixed` as the loop
resolves them; record genuinely-out-of-scope ideas under `## Won't Fix` with a
reason. **No entry may record an unfed/no-op production path as merely "acceptable"
— such a path is a gap to fix or an explicit escalation.**

The feature arrived green and genuinely wired (see `RALPH.md` arrival state), so
these are depth/hardening gaps, not "missing feature" gaps.

## Open Issues

_(none)_

## Accepted Costs

### G2 — Enricher does a per-row instance Get + per-row snapshot re-parse (N+1)
- Where: `domain/engine/enricher.go:105` calls `e.instances.Get(ctx, s.ID)` once per
  row; `readWorkflowInputs` (`:206`) JSON-marshals then unmarshals the snapshot per
  row; `FindNodeByID` (`:174`) parses the snapshot again.
- Impact: a page of N rows = N instance loads + 2×N snapshot parses. Design Phase 1
  explicitly **deferred "distinct-snapshot caching across rows that share a
  workflow"** to plans, and it does not appear implemented.
- Decision: accepted, documented cost. The per-row Get is a single-row SQLite primary
  key lookup (fast). Snapshot re-parse is bounded by page size (default 50). A future
  optimization could add `GetBatch` to `InstanceRepository` and a snapshot cache keyed
  by workflow name. The user lookup is already batched
  (`TestInstanceEnricher_DuplicateOwnerIDs_DedupedToOneLookup`).

## Fixed

### G1 — Backend column is permanently blank for nodes that inherit their backend
- Where: `domain/engine/enricher.go:183` (`resolveNodeFields`) sets `backend` only
  when `node.Config.Backend != workflow.BackendUnset`. The *effective* backend is
  never persisted on `NodeExecution`, so an inheriting node (`BackendUnset == ""`)
  yields an empty cell.
- Impact: the spec lists "model/backend" as a sortable column; for any inheriting
  node it is always empty — an under-fed production path, not a true "no value."
- **Fixed**: `NewInstanceEnricher` now accepts a `defaultAgentBackend` parameter.
  `resolveNodeFields` falls back to `e.defaultAgentBackend` when
  `node.Config.Backend == BackendUnset`. The composition root
  (`cmd/arbitron/main.go:782`) passes `cfg.LLM.DefaultAgentBackend`. Added
  `TestInstanceEnricher_BackendUnset_ResolvesToDefault` proving the resolution.
  The existing `TestInstanceEnricher_BackendUnset_EmptyBackend` still passes
  (no default → empty cell).

### G3 — Confirm "fail-if-nil-fed" coverage exists for every enriched component
- The done-condition requires at least one test per component that fails if its
  production input were nil/empty. Verified present for **queue position**
  (`adapter/http/instance_list_queue_position_e2e_test.go` + its negative-control
  `TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired`).
- **Fixed**: added `TestRouter_InstanceList_ModelRetryProvenanceProbe` (asserts
  model, backend, retryCount on the wire with a seeded node execution) and
  `TestRouter_InstanceList_ModelRetryAbsentForQueuedInstance` (negative control:
  queued instance with no executions → empty model, 0 retryCount) at
  `adapter/http/instance_list_enriched_e2e_test.go`.
- **Fixed**: added `TestRouter_InstanceList_SubmitterDegradesWhenUserAbsent`
  (negative control: absent user → empty submitterName on the wire) at
  `adapter/http/instance_list_enriched_e2e_test.go`.
- **Fixed**: added `TestRouter_InstanceList_TerminalRow_FriendlyStepEmpty`
  (negative control: completed instance → empty friendlyStep on the wire) at
  `adapter/http/instance_list_enriched_e2e_test.go`.
- **Fixed**: added `TestRouter_InstanceList_RunNameDegradesToShortID_NoVariables`
  (negative control: nil Variables → short-id fallback) at
  `adapter/http/instance_list_enriched_e2e_test.go`.
- **Fixed**: added `TestRouter_InstanceList_BackendUnset_EmptyOnWire`
  (negative control: BackendUnset → empty backend on the wire) at
  `adapter/http/instance_list_enriched_e2e_test.go`.
- **Fixed**: density default coverage exists in
  `dashboard/src/hooks/useDensityPreference.test.ts` (default, cycle, persist,
  reload, corrupt-value, storage-throws-read, storage-throws-write). Client-side
  concern; vitest coverage is appropriate.

### G4 — Verify MCP enriched output has field parity with the HTTP/dashboard contract
- Where: `adapter/mcp/tools_instance.go:115` returns `MarshalResult(enriched)`. A
  parity test is referenced at `adapter/mcp/tools_instance_test.go:958`.
- Gap: confirm that test asserts the actual enriched field names (run name,
  submitter, model/backend, retry, queue position) on the MCP wire — not merely that
  a non-empty list is returned. If the MCP struct serializes different JSON keys than
  `enrichedSummaryDTO`, that is a contract gap.
- **Fixed**: added `submitter` and `retryCount` assertions to
  `TestInstanceList_ReturnsEnrichedFields` at `adapter/mcp/tools_instance_test.go:985-986`.

### G5 — Confirm no un-enriched production list path remains
- The lean `summaryDTO` / `InstanceRepository.List` must not be what any production
  endpoint returns now that `ListEnriched` exists. Grep consumers of `.List(` in
  `adapter/http` and `adapter/mcp` and confirm every production list response flows
  through `ListEnriched`/`toEnrichedSummaryDTO`. Record any leftover lean path here.
- **Fixed**: grep of `.List(` in `adapter/http` and `adapter/mcp` shows the only
  production instance-list path is `h.service.ListEnriched(...)` at
  `adapter/http/instance_handler.go:384` and
  `srv.deps.Engine.ListEnriched(ctx, filters)` at
  `adapter/mcp/tools_instance.go:115`. The sole `instances.List` call is in
  `DismissAll` (`instance_handler.go:629`), which needs raw IDs, not enriched
  data — correct by design.

### G6 — Default-ordering parity (Phase 2 policy ↔ Phase 3 client sort) needs a test
- Design Phase 2 defines: Active by elapsed desc (`CreatedAt` asc), Pending by queue
  position asc, Terminal by most-recent first. Confirm `QueueSection`/`queueColumns`
  implement exactly this default and that a vitest pins the queued-by-position-asc
  and active-by-elapsed-desc defaults (so a regression in default order is caught).
- **Fixed**: `QueueSection.test.tsx:78-103` asserts active-by-elapsed-desc
  (`createdAt` asc), queued-by-position-asc, and terminal-by-updatedAt-desc
  defaults. `QueueSection.tsx:27-34` (`defaultSortKey`) implements exactly the
  Phase 2 policy.

## Won't Fix

_(none yet — record out-of-scope ideas here with a one-line reason; spec non-goals
such as new queue actions, execution/scheduling changes, or last-error content in
default columns belong here if proposed.)_
