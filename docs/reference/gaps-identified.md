# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

This tracks the review/refinement of the **final org-readiness gaps** (schedule
"run now", SSO group→department mapping, schedule & run visibility). Add new
findings to `## Open Issues`; move resolved ones to `## Fixed Previously`; log
genuinely out-of-scope ideas under `## Won't Fix (Beyond Current Scope)`.

---

## Open Issues

These are seeded carry-forward items to VERIFY during review (each may turn out
fine — confirm with a test or reading, then move to `## Fixed Previously` noting
the area/pass, or fix if a real defect).

- **Membership-collision rename in the renamer.** `adapter/sqlite/department_renamer.go` does `UPDATE OR IGNORE user_departments` then `DELETE FROM user_departments WHERE department = from`, because `OR IGNORE` alone leaves the verbatim `(user_id, from)` row when the user already holds the target department. Verify the `DELETE` purges exactly those orphans, loses no membership, preserves the RowsAffected/idempotence contract, and that `instances`/`schedules`/`published_workflows` (plain `UPDATE`) are correct. → **FIXED** (Area #6 Pass 1): existing collision test discarded `rows`; added assertion that pure collision returns `int64(0)` (UPDATE OR IGNORE skips, DELETE is uncounted cleanup). Added `TestRenameDepartment_PartialCollision_CountsNonColliding` — two users, one colliding and one not; asserts total = 1 (non-colliding user only) and both users end up with the friendly department with no verbatim remnants. (Area #6 Pass 1)

- **Hand-maintained sqlc for instances.** Migration `014` columns (`trigger_source`, `schedule_id`) were added by hand to `adapter/sqlite/generated/{models.go,instances.sql.go}` and all four Scan sites. Verify positional scans stay aligned, the `queries/instances.sql` source matches the generated code, and a future real `sqlc generate` would be purely additive. → **VERIFIED** (Area #8 Pass 1): All four scan sites (CreateInstance/GetInstance/ListInstances/ListRunningInstances) counted and confirmed aligned. `queries/instances.sql` source matches the generated column lists. Added `TestInstanceHandler_List_OriginFields` to verify `triggerSource`/`scheduleId` appear in the HTTP List JSON response with correct string values. (Area #8 Pass 1)


- **Env-overlay precedence for the mapping.** `ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>` overlays `auth.rbac.mapping` (mirrors the `LEADS_` precedent). Verify a value set in both YAML and env resolves with the documented precedence and that keys normalize consistently with the YAML path. (Area #11 / #19) → **FIXED** (Area #11 Pass 1+2): Added `TestLoad_RBACMapping_EnvOverlay_WinsOverYAML` (same key in YAML `sales` + env `engineering`; env wins) and `TestLoad_RBACMapping_EnvAndYAML_DifferentKeys_Coexist` (additive: different keys coexist). Added `GroupClaim` whitespace normalization in `normalize()` (consistent with existing Mode/TeamDomain trimming) + tests `TestLoad_RBAC_GroupClaim_WhitespaceTrimmedAtLoad` and `TestLoad_RBAC_GroupClaim_WhitespaceOnlyEqualsUnset`. OIDC provider stores GroupClaim verbatim (no trim) while edge verifier trims — normalization at load makes both paths consistent.

- **SchedulesPanel placement & read-only.** The panel renders in `WorkflowReview` (design-sanctioned, not a gap). Confirm it is workflow-scoped, exposes no `button` role, and degrades to empty/error without breaking the view. → **VERIFIED** (Area #10 Pass 1): all invariants confirmed by existing tests (workflow-scoped, no button, empty/error states). Added `renders run_now label without a schedule reference when scheduleId is absent (deleted-schedule run)` to `OriginBadge.test.tsx` — `run_now` with no `scheduleId` renders the label without a broken "From schedule" title attribute. (Area #10 Pass 1)

- **Run-now origin reaches the DB.** `Runner.Fire` injects `OriginRunNow`. Confirm a run-now fire persists `trigger_source='run_now'` + `schedule_id` (not only in memory), surfaces on the instance summary, and still does NOT advance `LastFiredAt`. (Area #20) → **VERIFIED** (Area #20): All three unit-level layers confirmed: `TestRunner_Fire_RunNowOrigin_StampsRunNowOrigin` (origin injected into trigger context), `TestEngine_Trigger_StampsRunNowOrigin` (engine stamps on instance), `TestInstanceRepoOrigin_RunNow_RoundTrip` (persisted + surfaces in summary), `TestService_RunNow_NeverAdvancesCronPosition` (SetLastFired never called). Full Runner→Engine→DB integration not exercised — see Live/integration paths item.

- **Live/integration paths unverified.** No test exercises (a) run-now over a real REST/MCP round-trip into a live engine, (b) `edge` mode with a real Cloudflare Access token carrying a real group claim → department scoping, or (c) the startup reconciler against a populated, production-shaped DB. → **CLOSED** (a) + (c): (a) is now tested — `TestRunNowSlice_HappyPath_202` / `_SkipWhenBusy_NoActiveRun_202` / `_NonOwner_403` / `_NotFound_404` in `adapter/http/schedule_runnow_slice_test.go` exercise the full HTTP→Service→Runner→SQLite chain with real repos, real schedule lookup, real creator-identity resolution via SQLite, real authz gate, and verified `OriginRunNow` in the trigger context; (c) was closed in the previous pass (`adapter/sqlite/reconcile_integration_test.go`). (b) requires a real Cloudflare Access environment (live JWKS, valid CF-issued JWT) and is an infrastructure-only gap beyond the unit-test review loop.

---

## Fixed Previously

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
