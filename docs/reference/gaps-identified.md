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

- **Membership-collision rename in the renamer.** `adapter/sqlite/department_renamer.go` does `UPDATE OR IGNORE user_departments` then `DELETE FROM user_departments WHERE department = from`, because `OR IGNORE` alone leaves the verbatim `(user_id, from)` row when the user already holds the target department. Verify the `DELETE` purges exactly those orphans, loses no membership, preserves the RowsAffected/idempotence contract, and that `instances`/`schedules`/`published_workflows` (plain `UPDATE`) are correct. (Area #6 / #15)

- **Hand-maintained sqlc for instances.** Migration `014` columns (`trigger_source`, `schedule_id`) were added by hand to `adapter/sqlite/generated/{models.go,instances.sql.go}` and all four Scan sites. Verify positional scans stay aligned, the `queries/instances.sql` source matches the generated code, and a future real `sqlc generate` would be purely additive. (Area #8)


- **Env-overlay precedence for the mapping.** `ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>` overlays `auth.rbac.mapping` (mirrors the `LEADS_` precedent). Verify a value set in both YAML and env resolves with the documented precedence and that keys normalize consistently with the YAML path. (Area #11 / #19)

- **SchedulesPanel placement & read-only.** The panel renders in `WorkflowReview` (design-sanctioned, not a gap). Confirm it is workflow-scoped, exposes no `button` role, and degrades to empty/error without breaking the view. (Area #10 / #18)

- **Run-now origin reaches the DB.** `Runner.Fire` injects `OriginRunNow`. Confirm a run-now fire persists `trigger_source='run_now'` + `schedule_id` (not only in memory), surfaces on the instance summary, and still does NOT advance `LastFiredAt`. (Area #20)

- **Live/integration paths unverified.** No test exercises (a) run-now over a real REST/MCP round-trip into a live engine, (b) `edge` mode with a real Cloudflare Access token carrying a real group claim → department scoping, or (c) the startup reconciler against a populated, production-shaped DB. These need a smoke test or VM/CI run. (Cross-cutting)

---

## Fixed Previously

- **Sentinel coverage for run-now sentinels.** `ErrCreatorUnresolved` and `ErrRunNowUnavailable` were missing from both the HTTP (`writeScheduleError`) and MCP (`scheduleResultError`) sentinel exhaustiveness tests. `ErrCreatorUnresolved` (→ 409/conflict) was added to the main `scheduleSentinels` slice in both files; `ErrRunNowUnavailable` (→ 503/run-now unavailable) received dedicated tests since it is intentionally a 5xx and requires different assertions. (Area #2 Pass 1)

- **Missing admin-can-fire tests at adapter level.** Both HTTP (`TestScheduleHandler_RunNow_NonOwner_403`) and MCP (`TestScheduleTools_RunNow_NonOwnerForbidden`) tested only the "non-owner is denied" half of the owner-or-admin authz contract; the "admin CAN fire another user's schedule" complement was absent at the adapter level. Added `TestScheduleHandler_RunNow_Admin_202` and `TestScheduleTools_RunNow_AdminCanFire`, each asserting 202/no-error and `firer.calls == 1`. (Area #2 Pass 2)

- **Edge group-claim shape airtightness.** `extractGroups` was verified correct but lacked tests for empty array, blank-only array, numeric scalar, and JSON-null claim values. Added `TestVerify_GroupClaimEdgeCases` (table test, 4 sub-cases) confirming each yields nil groups and no verification error — none is ever a 401. (Area #3 Pass 1)

- **GroupClaim config normalization and case-sensitivity.** No tests verified that a whitespace-padded `GroupClaim` (common YAML operator mistake) is normalized at construction time, nor that case-mismatched claim names fail open to nil groups instead of a 401. Added `TestNew_GroupClaimWhitespaceTrimmed` (tests `strings.TrimSpace` in `New()`) and `TestVerify_GroupClaimCaseSensitive` (documents fail-open behavior on case mismatch). (Area #3 Pass 2)

- **Blank mapping value in `ResolveDepartments`.** When a `Mapping` entry exists but maps to `""` or whitespace, the code correctly falls through to verbatim (preventing an empty department), but this subtle `mapping▸verbatim` priority chain edge was untested. Added `TestResolveDepartments_BlankMappingValue_FallsBackToVerbatim` (2 sub-cases). (Area #4 Pass 1)

- **Mapped+verbatim same department dedup.** During interim migration a user's token can carry both the old GUID claim (mapped to "engineering") and the new verbatim claim "Engineering" (normalizes to "engineering"). No test verified these dedup to one occurrence. Added `TestResolveDepartments_MappedAndVerbatimSameDept_Deduped`. (Area #4 Pass 2)

---

## Won't Fix (Beyond Current Scope)

- **Multi-instance / horizontal scale-out** — explicit non-goal of the spec; the scheduler is in-process and the datastore is embedded SQLite. No distributed-coordination machinery.
- **Additional provider adapters** — demand-driven non-goal; no new adapter unless a concrete out-of-family provider is required.
- **Admin-via-group** — admin is the bootstrap email allow-list only, by design; group claims drive department scoping exclusively.
- **Dashboard-driven schedule mutation** — all schedule create/edit/enable/disable/delete/run-now stay on REST + MCP; the dashboard surface is read-only by design.
