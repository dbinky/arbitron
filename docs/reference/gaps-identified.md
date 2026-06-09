# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of the **final org-readiness gaps** (schedule
"run now", SSO group→department mapping, schedule & run visibility). Add new
findings to `## Open Issues`; move resolved ones to `## Fixed Previously`; log
genuinely out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

*(none)*

---

## Fixed Previously

- **Membership-collision rename in the renamer.** `adapter/sqlite/department_renamer.go` does `UPDATE OR IGNORE user_departments` then `DELETE FROM user_departments WHERE department = from`. Verified `DELETE` purges exactly the orphans, RowsAffected/idempotence contract holds, and the other three tables (plain `UPDATE`) are correct. Added `TestRenameDepartment_PartialCollision_CountsNonColliding`. (Area #6 Pass 1)

- **Hand-maintained sqlc for instances.** All four scan sites aligned; `queries/instances.sql` matches generated code; a future real `sqlc generate` is purely additive. Added `TestInstanceHandler_List_OriginFields`. (Area #8 Pass 1)

- **Env-overlay precedence for the mapping.** `ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>` correctly overrides YAML for the same key; different keys coexist. Added `TestLoad_RBACMapping_EnvOverlay_WinsOverYAML`, `_DifferentKeys_Coexist`, plus `GroupClaim` whitespace normalization + two tests. (Area #11 Pass 1+2)

- **SchedulesPanel placement & read-only.** Panel is workflow-scoped, has no `button` role, degrades to empty/error gracefully. Added deleted-schedule run test to `OriginBadge.test.tsx`. (Area #10 Pass 1)

- **Run-now origin reaches the DB.** `Runner.Fire` injects `OriginRunNow`; confirmed `trigger_source='run_now'` + `schedule_id` persists; `LastFiredAt` is never advanced. All three unit layers verified. (Area #20)

- **Live/integration paths.** All three sub-items closed:
  - **(a)** `TestRunNowSlice_*` in `adapter/http/schedule_runnow_slice_test.go` — full HTTP→Service→Runner→SQLite chain, real authz gate, verified `OriginRunNow` in trigger context.
  - **(b)** `TestEdgeSlice_GroupClaim_MappedToDepartment` + `TestEdgeSlice_MissingGroupClaim_FallsBackToDefault` in `adapter/http/edge_identity_slice_test.go` — real RSA-signed JWT with group claim → real verifier (GroupClaim configured) → real provisioner (GroupConfig.Mapping) → mapped department in identity; absent claim → default group, not 401.
  - **(c)** `TestReconcileMappings_Integration_*` in `adapter/sqlite/reconcile_integration_test.go` — full `domain.ReconcileMappings` + real `DepartmentRenamer` + SQLite against all four tables.

- **Sentinel coverage for run-now sentinels.** `ErrCreatorUnresolved` and `ErrRunNowUnavailable` were missing from both the HTTP (`writeScheduleError`) and MCP (`scheduleResultError`) sentinel exhaustiveness tests. `ErrCreatorUnresolved` (→ 409/conflict) was added to the main `scheduleSentinels` slice in both files; `ErrRunNowUnavailable` (→ 503/run-now unavailable) received dedicated tests since it is intentionally a 5xx and requires different assertions. (Area #2 Pass 1)

- **Missing admin-can-fire tests at adapter level.** Both HTTP (`TestScheduleHandler_RunNow_NonOwner_403`) and MCP (`TestScheduleTools_RunNow_NonOwnerForbidden`) tested only the "non-owner is denied" half of the owner-or-admin authz contract; the "admin CAN fire another user's schedule" complement was absent at the adapter level. Added `TestScheduleHandler_RunNow_Admin_202` and `TestScheduleTools_RunNow_AdminCanFire`, each asserting 202/no-error and `firer.calls == 1`. (Area #2 Pass 2)

- **Edge group-claim shape airtightness.** `extractGroups` was verified correct but lacked tests for empty array, blank-only array, numeric scalar, and JSON-null claim values. Added `TestVerify_GroupClaimEdgeCases` (table test, 4 sub-cases) confirming each yields nil groups and no verification error — none is ever a 401. (Area #3 Pass 1)

- **GroupClaim config normalization and case-sensitivity.** No tests verified that a whitespace-padded `GroupClaim` (common YAML operator mistake) is normalized at construction time, nor that case-mismatched claim names fail open to nil groups instead of a 401. Added `TestNew_GroupClaimWhitespaceTrimmed` (tests `strings.TrimSpace` in `New()`) and `TestVerify_GroupClaimCaseSensitive` (documents fail-open behavior on case mismatch). (Area #3 Pass 2)

- **Blank mapping value in `ResolveDepartments`.** When a `Mapping` entry exists but maps to `""` or whitespace, the code correctly falls through to verbatim (preventing an empty department), but this subtle `mapping▸verbatim` priority chain edge was untested. Added `TestResolveDepartments_BlankMappingValue_FallsBackToVerbatim` (2 sub-cases). (Area #4 Pass 1)

- **Mapped+verbatim same department dedup.** During interim migration a user's token can carry both the old GUID claim (mapped to "engineering") and the new verbatim claim "Engineering" (normalizes to "engineering"). No test verified these dedup to one occurrence. Added `TestResolveDepartments_MappedAndVerbatimSameDept_Deduped`. (Area #4 Pass 2)

- **OIDC path missing group-claim-never-confers-admin test.** `TestResolveByEmail_GroupClaimNeverConfersAdmin` documented the invariant for the edge path but no symmetric test existed for the OIDC `Resolve()` path. Added `TestResolve_GroupClaimNeverConfersAdmin` asserting `RoleUser` even when `Groups: ["admins","superuser"]` with an empty bootstrap list, plus a sanity assertion that groups still drove department scoping. (Area #5 Pass 1)

- **Edge path missing department-replace-on-re-login test.** The OIDC path had `TestResolve_ReplacesDepartmentsOnEachLogin`; the edge `ResolveByEmail` path had no equivalent. Added `TestResolveByEmail_DepartmentsReplacedOnReLogin`: first login snapshots `["sales"]`, re-login with `["engineering"]` must store `["engineering"]` only. (Area #5 Pass 1)

- **SchedulesPanel workflowName dep change untested.** `SchedulesPanel` uses `useEffect([workflowName])` but no test verified re-fetch on prop change. If the dep array were accidentally emptied, stale data would silently persist. Added "re-fetches when workflowName prop changes" test using `rerender`. (Area #10 Pass 2)

- **`List` extra opts are silently ignored — 0-or-1 contract undocumented.** The variadic `List(ctx, ...opts)` only reads `opts[0]`; passing two opts ignores the second. No test documented this contract. Added `TestService_List_ExtraOptsIgnored` confirming only `opts[0].WorkflowName` is used. (Area #9 Pass 2)

- **`creatorId` injection via query param untested.** HTTP `GET /schedules` accepted `workflowName` from query params but no test verified that passing `?creatorId=<other-uuid>` is silently ignored (the security invariant "CreatorID is service-set, never from request input"). Added `TestScheduleHandler_List_CreatorIDNotFromInput`: two users' schedules seeded; non-admin calling with injected `?creatorId=<otherUser>` still receives only their own schedule. (Area #9 Pass 1)

- **scheduleId omitempty robustness untested.** `summaryDTO.ScheduleID` has `omitempty` but no test verified that a manual run (nil ScheduleID) actually omits the key from JSON vs. emitting `"scheduleId":null`. Added `TestInstanceHandler_List_ManualOrigin_ScheduleIDAbsent` asserting the key is absent for nil ScheduleID and `triggerSource="manual"` is always present. (Area #8 Pass 2)

- **WithOrigin override semantics untested.** No test verified that a second `WithOrigin` call on a context already carrying an origin overwrites the prior value. Documents the context.WithValue layering guarantee so each trigger call's origin cannot bleed into a nested trigger. Added `TestWithOrigin_OverwritesPriorOrigin` in `domain/instance/origin_test.go`. (Area #7 Pass 2)

- **Engine origin-stamping had no test coverage.** `domain/engine/engine.go` lines 346–351 read `OriginFromContext` and stamp `TriggerSource`/`ScheduleID` on every instance — zero tests covered this. Added three tests in `domain/engine/engine_test.go`: `TestEngine_Trigger_StampsManualOrigin` (plain `context.Background()` → `OriginManual`, nil `ScheduleID`), `TestEngine_Trigger_StampsScheduleOrigin`, and `TestEngine_Trigger_StampsRunNowOrigin` (each injects an `Origin` via `WithOrigin` and asserts both fields survive to the stored instance). Confirmed `domain/engine` does not import `domain/schedule`. (Area #7 Pass 1)

- **Nil logger in ReconcileMappings panics if guard removed.** `ReconcileMappings` has `if rows > 0 && log != nil` but all existing tests passed `discardLog()`. A nil logger with rows>0 would panic if the guard were inadvertently removed. Added `TestReconcileMappings_NilLogger_DoesNotPanic` using `nil` logger with a mapping entry that returns rows=1; confirms no panic and that the rename still executes. (Area #6 Pass 2)

- **Bootstrap list construction-time normalization untested.** `NewProvisioner` normalizes bootstrap list entries (trim+lowercase) at construction but all matching tests supplied pre-normalized entries. A misconfigured "  Admin@Corp.Example  " would silently not match. Added `TestNewProvisioner_BootstrapListNormalized` covering whitespace-padded subject + email + blank-after-trim entry (blank must not produce phantom `""` match). (Area #5 Pass 2)

---

## Won't Fix (Beyond Current Scope)

- **Multi-instance / horizontal scale-out** — explicit non-goal of the spec; the scheduler is in-process and the datastore is embedded SQLite. No distributed-coordination machinery.
- **Additional provider adapters** — demand-driven non-goal; no new adapter unless a concrete out-of-family provider is required.
- **Admin-via-group** — admin is the bootstrap email allow-list only, by design; group claims drive department scoping exclusively.
- **Dashboard-driven schedule mutation** — all schedule create/edit/enable/disable/delete/run-now stay on REST + MCP; the dashboard surface is read-only by design.
