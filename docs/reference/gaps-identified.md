# Gaps Identified

Issues found during review iterations. Only the user can move items to the Won't Fix sections.

## Open Issues

_(none)_

## Fixed Previously

### RBAC-INTERACT-001: Department leads can participate in interactive sessions they don't own
**Fixed**: The behavior is intentional — participation routes through `canIntervene`, consistent with all other mutate actions. Two explicit tests added to `conversation_authz_test.go`: `TestConversationAuthz_DepartmentLead_CanParticipate` pins lead-can behavior, and `TestConversationAuthz_DepartmentPeer_Forbidden` pins that a plain department peer (viewer) gets ErrForbidden (not ErrNotFound). A design note was added to `docs/superpowers/specs/rbac-design-phase-3-instance-visibility.md` explaining the rationale and the escape hatch if narrowing is ever needed.

### RBAC-DASH-002: Dashboard window is hardcoded to 24h on the frontend
**Fixed**: The frontend `DashboardView.tsx` already contained `/** Fixed observation window for v1 (design default). */` documenting the intentional 24h default. The constant is named `WINDOW` and the comment makes the design decision explicit. No selector is added for v1; the backend accepts a dynamic `window` param when the UI is extended in a future phase.

### RBAC-AUDIT-003: Admin-override audit on the engine trigger path is deferred
**Fixed**: `domain/engine/engine.go TriggerWithDepartment` now emits `admin.override` via the existing `auditRecorder` seam when an admin triggers a department-visible workflow they are not a member of (sole-reason logic: `callerID.IsAdmin() && !callerID.InDepartment(pubDept)`). Three tests added to `engine_audit_emit_test.go`: emit when admin is out-of-dept, no event when admin is in-dept, nil-safe when no recorder is wired.

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
