# Phase 2, Task 4: Validation Extensions (Connection Existence + Azure `api-version`)

## Goal

Extend validation so a profile's non-empty `ConnectionRef` must name an existing connection (checked at `workflow_validate` via an injected checker, the same way the existing file `checker` is threaded), and so Azure providers require `api-version` in their connection `Extras` (checked at connection **create/update**, not at workflow validate). The Phase 1 kind-matching (`agent_call`↔agent, `llm_call`↔chat) is unchanged. The domain stays adapter-free: validation receives an injected interface, not a `ConnectionStore` directly.

This task realizes the validation half of acceptance **#7** (a profile referencing a missing connection fails before any run; Azure misconfiguration is rejected at connection write).

**Dependency ordering:** Builds on **Phase 1 Task 5** (`validateNodeConfigs` kind-matching + injected-checker plumbing in `domain/workflow/validation.go`). Builds on **Task 2** (`ConnectionStore`) and **Task 3** (widened `Profile.ConnectionRef`). Task 5 wires the concrete checker. Sources of truth: design Phase 2 §4; design Phase 1 §4 (kind-matching, injected `checker` pattern); existing `domain/workflow/validation.go` (`Validate(wf, checker)`, `validateFileReferences`, `validateNodeConfigs`).

## Files to Create/Modify

### Modify: `domain/workflow/validation.go`

- Add a minimal injected checker interface for connection existence, mirroring the existing `FileChecker` pattern (a small subset the domain depends on — **not** the full `ConnectionStore`, so the domain imports no adapter and no `provider` persistence concern leaks in):
  ```go
  // ConnectionChecker is the minimal subset used to validate that a profile's
  // ConnectionRef names an existing connection. The concrete implementation is
  // injected at wiring time (Task 5); the domain depends only on this interface.
  type ConnectionChecker interface {
      // ProfileConnectionRef returns the ConnectionRef for a profile alias, and
      // whether the alias is known.
      ProfileConnectionRef(alias string) (connectionRef string, aliasKnown bool)
      // ConnectionExists reports whether a connection with the given name exists.
      ConnectionExists(name string) bool
  }
  ```
  (Keeping both lookups behind one checker lets validation map alias → ConnectionRef → existence without importing `domain/provider`.)
- Extend the top-level `Validate` signature to accept the new checker alongside the existing `FileChecker`, following the current injected-checker convention. Prefer an additive optional parameter so existing call sites stay valid; a nil `ConnectionChecker` skips the connection check (same pattern as a nil `FileChecker` skipping file checks):
  ```go
  func Validate(wf *Workflow, fileChecker FileChecker, connChecker ConnectionChecker) []ValidationError
  ```
  Update internal callers accordingly. (If Phase 1 already widened this signature for the profile registry, extend that signature rather than adding a competing one.)
- Add `validateConnectionReferences(wf, connChecker)`:
  - For every `llm_call`/`agent_call` node, read its `model_profile` alias (already validated for kind in Phase 1).
  - `connChecker.ProfileConnectionRef(alias)`; if the alias is unknown, skip here (Phase 1's kind/alias validation already reports the unknown-alias error — do not double-report).
  - If `connectionRef == ""`, it is the claude-cli no-key subscription path (Task 3) — **valid**, no check.
  - If `connectionRef != ""` and `!connChecker.ConnectionExists(connectionRef)`, append a clear `ValidationError`: e.g. `profile "<alias>" references unknown connection "<connectionRef>"`. This fails `workflow_validate` before any run.
  - Guard on `connChecker != nil` so a nil checker skips the whole check (mirrors `validateFileReferences`).
- Wire `validateConnectionReferences` into `Validate` next to `validateFileReferences`. **Kind-matching from Phase 1 is unchanged** — do not touch `validateNodeConfigs`'s kind logic.

### Modify: `domain/provider/connection.go` (Azure extras validation, write-time)

- Add a pure validation helper invoked by the `ConnectionStore` adapter at **create/update** (not at workflow validate). Keep it in the domain so the rule lives with the `Connection` type:
  ```go
  // ValidateConnection enforces provider-specific requirements at create/update
  // time. Azure providers ("azure-openai", "azure-foundry") require a non-empty
  // "api-version" entry in Extras. Returns a clear error otherwise.
  func ValidateConnection(c Connection) error
  ```
  - For Azure providers (treat `azure-openai` and `azure-foundry` as the Azure set; centralize the set so Phase 3 can extend it), require `c.Extras["api-version"]` present and non-empty; otherwise return e.g. `var ErrAzureAPIVersionRequired = errors.New("azure connections require an api-version in extras")`.
  - Non-Azure providers pass with no extras requirement.

### Modify: `adapter/sqlite/connection_repo.go` (from Task 2)

- In `Create` and `Update`, call `provider.ValidateConnection(c)` **before** sealing/persisting; return its error unchanged. This enforces the Azure `api-version` rule at connection write time (design §4: "checked when the connection is created/updated, not at workflow validate time"). (This is a small follow-on edit to Task 2's repo; note it here so a fresh subagent doing Task 4 knows to add it.)

## Testing (strict TDD — all five scenario categories)

Test `validateConnectionReferences` with a **fake `ConnectionChecker`** and `ValidateConnection` directly. Cover design §7's `Validation` bullet:

- **Happy path** — a workflow whose `llm_call`/`agent_call` profiles reference existing connections validates with no connection errors; an Azure connection **with** `api-version` passes `ValidateConnection`.
- **Success** — a profile with an empty `ConnectionRef` (claude-cli subscription) validates with no connection error (no existence check performed); a non-Azure connection with no `api-version` passes `ValidateConnection`.
- **Failure** — a profile referencing a missing connection produces a clear `ValidationError` naming the alias and the connection; an Azure connection **missing** `api-version` is rejected by `ValidateConnection` with `ErrAzureAPIVersionRequired`, and (integration) `ConnectionRepo.Create`/`Update` returns that error and persists nothing.
- **Error** — `Validate` with a nil `ConnectionChecker` skips the connection check without panicking; an unknown alias is **not** double-reported by the connection check (Phase 1 owns that error).
- **Edge case** — `api-version` present but empty string is rejected (treated as missing); a workflow mixing a no-connection Claude profile and a real-connection Azure profile validates the real one and skips the empty one; kind-matching from Phase 1 still fails an `agent_call`→chat profile (regression assertion that this task did not weaken it).

## Acceptance Criteria

- `domain/workflow/validation.go` checks, via an injected `ConnectionChecker` (not a `ConnectionStore`), that every non-empty profile `ConnectionRef` names an existing connection, failing `workflow_validate` with a clear error before any run.
- Empty `ConnectionRef` (claude-cli subscription) passes validation with no existence check.
- `provider.ValidateConnection` enforces Azure `api-version` in `Extras` at connection create/update; the `ConnectionRepo` calls it before sealing/persisting.
- Phase 1 kind-matching is unchanged and still enforced.
- Domain imports no adapter; all five TDD scenario categories pass; existing tests still pass.
