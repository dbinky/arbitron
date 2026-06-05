# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

### RBAC-INTERACT-001: Department leads can participate in interactive sessions they don't own
**Area**: canView vs canIntervene split (`domain/engine/conversation.go` `loadAndValidate`)
**Detail**: Interactive-session participation was routed through `canIntervene`, so a department **lead** may now join/drive an interactive session for a run they did not trigger (previously owner/admin only). This is a defensible consequence of treating participation as a mutate action, but it was not an explicit spec decision. Confirm intent. If participation should stay narrower than other interventions (owner/admin only), introduce a dedicated gate rather than reusing `canIntervene`. Either way, add an explicit test pinning the chosen behavior (lead-can vs lead-cannot) and a one-line note in the Phase 3 design doc.

### RBAC-DASH-002: Dashboard window is hardcoded to 24h on the frontend
**Area**: Department selector + live refresh / Dashboard view (`dashboard/src` DashboardView)
**Detail**: The frontend always requests `window=24h`, though the backend `domain/dashboard` service accepts and clamps an arbitrary `window` to `[1h, 30d]`. Either expose a small window selector (e.g. 1h / 24h / 7d / 30d) wired to the existing backend param, or document the fixed 24h default as intentional for this phase. If adding a selector, cover it with a Vitest test asserting the request param changes and the tiles refetch.

### RBAC-AUDIT-003: Admin-override audit on the engine trigger path is deferred
**Area**: Workflow audit emission / Intervention audit emission (`domain/engine` Trigger gate)
**Detail**: `admin.override` audit events are currently emitted from the workflow-service read/list/copy paths and the intervention path only. An admin who *triggers* a department-visible workflow they are not a member of is not audited, because the engine intentionally has a single `SetAuditRecorder` seam and trigger-path override emission was deferred during plan alignment. Decide whether triggering a department workflow as an out-of-department admin is a consequential override worth recording; if so, emit it on the engine Trigger gate via the existing `auditRecorder` (no new seam), reusing the same admin-sole-reason logic, and add an engine test asserting the event lands. If not, log here as Won't Fix.

## Fixed Previously

_(none yet — as issues are fixed during iterations, move them here with a summary of what was wrong and how it was fixed)_

## Won't Fix (Beyond Current Scope)

_(items logged here are new functionality or enhancements that are outside the scope of the current review — only the user can move items to this section)_

<!--
Per the spec's non-goals, the following are explicitly OUT of scope for this feature
and should NOT be logged as open issues:
- Per-department queue fairness / concurrency quotas (deferred to a future spec)
- A custom in-app permission/role editor
- Cross-department sharing to a specific OTHER department
- Full per-read access auditing (only consequential mutations are audited)
- A SQLite→Postgres migration (RBAC was built Postgres-ready: ports, portable SQL,
  the idx_instances_dept_vis index; the swap is a separate load-test-triggered effort)
-->
