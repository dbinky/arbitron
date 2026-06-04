# RBAC Phase 2 — Task 6: Audit Event Emission (`workflow.publish`, `workflow.visibility_change`, `admin.override`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit the three Phase 2 audit events by wiring the `audit.Service` into the workflow service: a `workflow.publish` event on successful `Publish`, a `workflow.visibility_change` event (with from/to + department) on successful `SetVisibility`, and an `admin.override` event at the read gate when admin status is the SOLE reason access was granted. Recording is best-effort and never fails the underlying operation.

**Architecture:** The `WorkflowService` gains an optional `*audit.Service` (nil-safe). Emission happens AFTER the underlying operation succeeds (so a rolled-back publish is not audited) and the `Record` call itself is best-effort (Task 5 guarantees it never errors). The admin-override event fires inside the gate path, evaluated only when `id.IsAdmin()` was the deciding factor — i.e. a non-admin would NOT have seen the publication.

**Tech Stack:** Go, `domain/workflow`, `domain/audit`, `domain/identity`. `encoding/json` for detail payloads.

**Cross-phase dependency:** Uses `identity.Identity` (admin + departments) for the override decision; consistent with Tasks 3–4.

---

## Context you need

- After Task 4, `WorkflowService.Publish(ctx, privateName, publishedName, visibility, department)` and `SetVisibility(ctx, publishedName, visibility, department)` exist and succeed before any audit call.
- After Task 5, `audit.Service.Record(ctx, action, target, detail)` is best-effort and nil-safe; action constants `audit.ActionWorkflowPublish`, `ActionWorkflowVisibilityChange`, `ActionAdminOverride`.
- After Task 3, `CanSeePublication(id, pub)` is the gate predicate, and `canSeeShared` resolves a publication for a name. The "admin was the sole reason" check = `CanSeePublication` is true AND a copy of the identity with the admin bit cleared would be false.
- `WorkflowService` is constructed by `NewWorkflowService(repo, publishers)` in three places: `cmd/arbitron/main.go:330`, `adapter/http/workflow_handler.go:110`, `adapter/mcp/tools_workflow.go:29`. Adding an audit dependency must be optional (a setter, like `SetInterventions` on the engine) so those call sites are not all forced to change.

## Files

- Modify: `domain/workflow/service.go` (add `audit *audit.Service` field + `SetAudit` setter; emit in `Publish`/`SetVisibility`)
- Modify: `domain/workflow/gating.go` (add `adminOverrode` helper)
- Modify: `domain/workflow/service.go` `List`/`Load`/`Copy` + `domain/engine/engine.go` Trigger gate (emit `admin.override` when admin is sole reason)
- Modify: `cmd/arbitron/main.go` (construct `audit.Service` over `NewAuditRepo`, wire via `SetAudit` on the workflow service and the engine)
- Test: `domain/workflow/audit_emit_test.go` (new)

---

## Steps

- [ ] **Step 1: Write the failing publish-audit test**

Create `domain/workflow/audit_emit_test.go`:

```go
package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStore captures audit events for assertions.
type recordingStore struct{ events []audit.Event }

func (r *recordingStore) Append(_ context.Context, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *recordingStore) ListByActor(context.Context, string, int) ([]audit.Event, error) {
	return r.events, nil
}

func TestService_Publish_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f1")
	seedPrivate(repo, owner, "src")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	require.NoError(t, svc.Publish(
		ctxWithRoles(owner, false, []string{"engineering"}, nil),
		"src", "shared", workflow.VisibilityDepartment, "engineering"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowPublish, store.events[0].Action)
	assert.Equal(t, "shared", store.events[0].Target)
	assert.Contains(t, store.events[0].Detail, "department")
}

func TestService_Publish_FailedPublish_NoAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f2")
	// No private source seeded → Publish fails with ErrNotFound before recording.
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil),
		"missing", "shared", workflow.VisibilityDepartment, "engineering")
	require.Error(t, err)
	assert.Empty(t, store.events) // nothing audited for a failed publish
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./domain/workflow/ -run TestService_Publish_EmitsAuditEvent -v`
Expected: FAIL — `svc.SetAudit` undefined.

- [ ] **Step 3: Add the audit field, setter, and detail helper**

In `domain/workflow/service.go`, add an import for `"encoding/json"` and `"github.com/arbitron/arbitron/domain/audit"`, add the field to `WorkflowService`:

```go
	audit *audit.Service // optional; nil-safe best-effort audit emitter
```

Add the setter:

```go
// SetAudit wires the best-effort audit emitter. When unset (nil), no audit
// events are recorded (local/unconfigured deployments behave as today).
func (s *WorkflowService) SetAudit(a *audit.Service) {
	s.audit = a
}
```

Add a detail helper (private):

```go
// auditDetail marshals a small map to compact JSON for an audit event's detail,
// falling back to "{}" on the (practically impossible) marshal error so audit
// recording is never blocked.
func auditDetail(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
```

- [ ] **Step 4: Emit `workflow.publish` after a successful Publish**

At the very end of `Publish`, just before `return nil` (i.e. after `Upsert` succeeded — never on a rollback path), add:

```go
	s.audit.Record(ctx, audit.ActionWorkflowPublish, publishedName, auditDetail(map[string]string{
		"to":         string(visibility),
		"department": department,
	}))
	return nil
```

(`s.audit` is nil-safe per Task 5: `(*audit.Service)(nil).Record(...)` is a no-op.)

- [ ] **Step 5: Run to verify publish-audit tests pass**

Run: `go test ./domain/workflow/ -run 'TestService_Publish_(Emits|FailedPublish)' -v`
Expected: PASS.

- [ ] **Step 6: Write the failing visibility-change audit test**

```go
func TestService_SetVisibility_EmitsVisibilityChangeWithFromTo(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f3")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	repo.bucket(workflow.Shared())["shared"] = wf("shared")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	require.NoError(t, svc.SetVisibility(
		ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}),
		"shared", workflow.VisibilityPublic, ""))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowVisibilityChange, store.events[0].Action)
	assert.Equal(t, "shared", store.events[0].Target)
	// from=department, to=public must both appear in detail.
	assert.Contains(t, store.events[0].Detail, "department")
	assert.Contains(t, store.events[0].Detail, "public")
}
```

- [ ] **Step 7: Run to verify it fails**

Run: `go test ./domain/workflow/ -run TestService_SetVisibility_EmitsVisibilityChange -v`
Expected: FAIL — no event emitted.

- [ ] **Step 8: Emit `workflow.visibility_change` in SetVisibility**

In `SetVisibility`, capture the prior level from `cur` (the publication read before the flip), and after the `Upsert` succeeds, record:

```go
	s.audit.Record(ctx, audit.ActionWorkflowVisibilityChange, publishedName, auditDetail(map[string]string{
		"from":           string(cur.Visibility),
		"from_department": cur.Department,
		"to":             string(visibility),
		"to_department":  department,
	}))
	return nil
```

- [ ] **Step 9: Run to verify visibility-change audit passes**

Run: `go test ./domain/workflow/ -run TestService_SetVisibility_EmitsVisibilityChange -v`
Expected: PASS.

- [ ] **Step 10: Write the failing admin-override test**

```go
func TestService_Load_AdminOverride_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f4")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000f5")
	// A department workflow an admin (not in that dept) loads only via override.
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Admin is in NO departments → admin status is the SOLE reason for access.
	_, err := svc.Load(ctxWithRoles(adminID, true, nil, nil), "eng-wf")
	require.NoError(t, err)

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionAdminOverride, store.events[0].Action)
	assert.Equal(t, "eng-wf", store.events[0].Target)
}

func TestService_Load_MemberInDepartment_NoOverrideEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f6")
	member := uuid.MustParse("00000000-0000-0000-0000-0000000000f7")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// A genuine member would have seen it anyway → NOT an override.
	_, err := svc.Load(ctxWithRoles(member, true, []string{"engineering"}, nil), "eng-wf")
	require.NoError(t, err)
	assert.Empty(t, store.events)
}

func TestService_Load_PublicWorkflow_AdminNoOverrideEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f8")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000f9")
	repo.bucket(workflow.Shared())["pub-wf"] = wf("pub-wf")
	_ = pubs.Upsert(context.Background(), "pub-wf", publisher, "src", workflow.VisibilityPublic, "")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Public is visible to everyone → admin access is NOT an override.
	_, err := svc.Load(ctxWithRoles(adminID, true, nil, nil), "pub-wf")
	require.NoError(t, err)
	assert.Empty(t, store.events)
}
```

- [ ] **Step 11: Run to verify they fail**

Run: `go test ./domain/workflow/ -run 'TestService_Load_(AdminOverride|MemberInDepartment|PublicWorkflow)' -v`
Expected: FAIL — no override event emitted.

- [ ] **Step 12: Add the `adminOverrode` predicate**

In `domain/workflow/gating.go`:

```go
// adminOverrode reports whether admin status was the SOLE reason id can see p:
// access is granted, but a copy of the identity with admin cleared would be
// denied. This is the precise condition for an admin.override audit event
// (design doc §Audit Log).
func adminOverrode(id identity.Identity, p Publication) bool {
	if !id.IsAdmin() {
		return false
	}
	demoted := id
	demoted.Role = identity.RoleUser
	return CanSeePublication(id, p) && !CanSeePublication(demoted, p)
}
```

- [ ] **Step 13: Emit `admin.override` at the Load gate**

The cleanest seam: emit inside the service `Load`/`Copy` (and `List` per-entry, and the engine Trigger) right after a successful gate decision. Change `Load` so it resolves the publication itself (rather than delegating gating opaquely to `LoadVisibleFiltered`) ONLY for the override-detection side effect. Minimal approach — wrap the filter:

In `Load`, replace `s.sharedFilter(id)` with an override-aware filter built inline:

```go
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, _, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
	return wf, err
}

// auditingFilter is sharedFilter plus an admin.override emission: when access
// is granted ONLY because the caller is admin, a best-effort override event is
// recorded against the shared name.
func (s *WorkflowService) auditingFilter(id identity.Identity) SharedVisibilityFilter {
	if s.publishers == nil {
		return nil
	}
	return func(ctx context.Context, name string) (bool, error) {
		pub, ok, err := s.publishers.PublicationOf(ctx, name)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil // unpublished shared folder ⇒ open, nothing to audit
		}
		if !CanSeePublication(id, pub) {
			return false, nil
		}
		if adminOverrode(id, pub) {
			s.audit.Record(ctx, audit.ActionAdminOverride, name, auditDetail(map[string]string{
				"visibility": string(pub.Visibility),
				"department": pub.Department,
			}))
		}
		return true, nil
	}
}
```

Apply the same override emission in `Copy` (replace its `canSeeShared` call with the publication-resolving form that also fires `adminOverrode`), and in `List` (in the per-entry loop from Task 3, after `CanSeePublication` passes, call `if adminOverrode(id2, pub) { s.audit.Record(...) }`).

KEEP `sharedFilter` (non-auditing) for `ListFiles`, OR switch `ListFiles` to `auditingFilter` too for consistency — choose `auditingFilter` everywhere a shared read can occur, so admin overrides are uniformly captured.

- [ ] **Step 14: Run to verify override tests pass**

Run: `go test ./domain/workflow/ -run TestService_Load_ -v`
Expected: PASS (override fires only when admin is the sole reason; member-in-dept and public cases emit nothing).

- [ ] **Step 15: Emit `admin.override` on the engine Trigger gate (optional parity)**

In `domain/engine/engine.go`, the Trigger filter (Task 3) can also emit the override. Give the engine an optional `audit *audit.Service` via `SetAudit(a *audit.Service)` and, inside the trigger filter where `CanSeePublication` is checked, call `if e.audit != nil && admin-sole-reason { e.audit.Record(ctx, audit.ActionAdminOverride, name, ...) }` using the same `adminOverrode` logic (export `adminOverrode` from `domain/workflow` as `AdminOverrode` if the engine needs it, OR keep the override emission only in the workflow service and skip it for triggers). DECISION: export `workflow.AdminOverrode` and emit in the engine for parity, since a Phase-3 admin triggering a department workflow is a consequential override. Add one engine test mirroring `TestEngine_Trigger_DepartmentWorkflow_AllowedForMember` but with an admin-in-no-dept identity, asserting an override event landed in a recording store wired via `eng.SetAudit`.

(If exporting, rename `adminOverrode`→`AdminOverrode` in `gating.go` and update the service call sites.)

- [ ] **Step 16: Wire the audit service in `cmd/arbitron/main.go`**

Near `publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)` / `workflowSvc := workflow.NewWorkflowService(...)` (around `main.go:327-330`):

```go
	auditRepo := adaptsqlite.NewAuditRepo(db)
	auditSvc := audit.NewService(auditRepo)
	auditSvc.SetLogger(logger)
	workflowSvc.SetAudit(auditSvc)
	// ... and after the engine is built:
	eng.SetAudit(auditSvc) // only if Step 15's engine emission was implemented
```

Add the `domain/audit` import. Ensure the HTTP `WorkflowHandler` and MCP path use this same `workflowSvc` (the HTTP handler already receives it via `SetWorkflowService` in `server.go`; for MCP, ensure `workflowSvc(srv)` — updated in Task 4 — also calls `SetAudit`, or have `srv.deps` carry the configured `*audit.Service` and apply it).

- [ ] **Step 17: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 18: Commit**

```bash
git add domain/workflow/service.go domain/workflow/gating.go domain/workflow/audit_emit_test.go \
        domain/engine/engine.go cmd/arbitron/main.go adapter/mcp/tools_workflow.go
git commit -m "feat(rbac): emit publish/visibility_change/admin.override audit events

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Depends on:** Task 4 (`Publish`/`SetVisibility`), Task 5 (`audit.Service`), Task 3 (`CanSeePublication`).
- **Blocks:** nothing in Phase 2. Phase 3 reuses `audit.Service` (lead-intervention events) and assumes the trigger path is gated + override-audited.
