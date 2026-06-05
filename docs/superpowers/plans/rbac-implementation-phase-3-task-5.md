# RBAC Phase 3 — Task 5: Lead-Intervention & Admin-Override Audit Events

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a **lead** intervenes on an instance they do not own, record an `instance.lead_intervention` audit event; when an **admin** acts outside ownership, record `admin.override`. Reuse the Phase 2 audit service. Best-effort: an audit write failure must never undo the intervention that already succeeded.

**Architecture:** The engine already has a best-effort `recordIntervention` (writes the legacy `instance.Intervention` attribution row + publishes `instance.intervened`). Phase 3 adds a parallel **domain audit** emission alongside it, classifying the actor (lead vs admin vs owner) to choose the audit action. The audit service is the Phase-2 `audit.Service` consumed via a small engine port so the engine does not import the audit adapter. The classification reuses the Task-4 identity predicates.

**Tech Stack:** Go 1.23+, testify.

**Cross-phase dependencies (consumed contract — DO NOT build here):**
- **Phase 2 audit:** `domain/audit` provides a `Service` whose method is `Record(ctx context.Context, action, target, detail string)` (VOID, best-effort, nil-safe — a store failure is logged and swallowed inside the service, never returned) and reads the actor from context. The engine's `AuditRecorder` port below MUST match this void signature so `*audit.Service` satisfies it directly with no adapter. Events `workflow.publish`, `workflow.visibility_change`, `admin.override` are introduced in Phase 2. Phase 3 ADDS the `instance.lead_intervention` action and reuses `admin.override`.
- This task is written against an engine-local `AuditRecorder` port so the exact `audit.Service` shape is isolated to the wiring file. If Phase 2's method signature differs, only the adapter at the wiring seam changes.

**Cross-task dependency:** Requires Task 4 (`canIntervene` and the identity predicates for actor classification).

---

### Task 5.1: Actor classification + `AuditRecorder` port

**Files:**
- Create: `domain/engine/audit_intervention.go`
- Test: `domain/engine/audit_intervention_test.go`

- [ ] **Step 1: Write the failing classification test**

Create `domain/engine/audit_intervention_test.go` (white-box, `package engine`):

```go
package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func mkInst2(owner uuid.UUID, dept string) *instance.Instance {
	return &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: instance.VisibilityDepartment, Department: dept}
}

// Owner self-action => no audit action (empty string => caller skips emission).
func TestAuditAction_OwnerSelf_None(t *testing.T) {
	o := uuid.New()
	id := identity.Identity{UserID: o, Role: identity.RoleUser, Departments: []string{"engineering"}}
	assert.Equal(t, "", auditActionFor(id, mkInst2(o, "engineering")))
}

// A lead of the run's department intervening on a non-owned run => lead_intervention.
func TestAuditAction_Lead_LeadIntervention(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser, Departments: []string{"engineering"}, LedDepartments: []string{"engineering"}}
	assert.Equal(t, AuditActionLeadIntervention, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}

// An admin acting on a non-owned run => admin.override.
func TestAuditAction_Admin_Override(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin}
	assert.Equal(t, AuditActionAdminOverride, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}

// An admin who also owns the run is a self-action => no audit.
func TestAuditAction_AdminOwner_None(t *testing.T) {
	o := uuid.New()
	id := identity.Identity{UserID: o, Role: identity.RoleAdmin}
	assert.Equal(t, "", auditActionFor(id, mkInst2(o, "engineering")))
}

// Admin takes precedence over lead when both apply (admin overriding their own
// led department is still classified admin.override — admin is the broader power).
func TestAuditAction_AdminAndLead_AdminWins(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin, LedDepartments: []string{"engineering"}}
	assert.Equal(t, AuditActionAdminOverride, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run TestAuditAction_ -v`
Expected: FAIL — `undefined: auditActionFor`, `AuditActionLeadIntervention`, `AuditActionAdminOverride`.

- [ ] **Step 3: Implement classification + port**

Create `domain/engine/audit_intervention.go`:

```go
package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// Audit action strings emitted by the engine for non-owner interventions. They
// match the Phase-2 audit vocabulary (admin.override) and extend it
// (instance.lead_intervention).
const (
	AuditActionLeadIntervention = "instance.lead_intervention"
	AuditActionAdminOverride    = "admin.override"
)

// AuditRecorder is the minimal write surface the engine needs from the Phase-2
// audit service. Record reads the actor from ctx and persists an event. It is
// best-effort and VOID: a store failure is logged and swallowed inside the audit
// service, never propagated, so auditing can never fail the underlying
// intervention. This matches *audit.Service.Record exactly (Phase 2), so the
// service satisfies this port with no adapter.
type AuditRecorder interface {
	Record(ctx context.Context, action, target, detail string)
}

// auditActionFor classifies a non-owner intervention into an audit action, or
// "" when no audit event should be written (owner self-action). Admin precedence
// over lead: an admin acting on a non-owned run is admin.override even if they
// also lead the run's department.
func auditActionFor(id identity.Identity, inst *instance.Instance) string {
	if id.UserID == inst.OwnerID {
		return "" // owner self-action: not audited
	}
	if id.IsAdmin() {
		return AuditActionAdminOverride
	}
	if id.LeadsDepartment(inst.Department) {
		return AuditActionLeadIntervention
	}
	return "" // a peer cannot reach a mutate path (canIntervene already denied)
}
```

- [ ] **Step 4: Run the classification tests to verify they pass**

Run: `go test ./domain/engine/ -run TestAuditAction_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/audit_intervention.go domain/engine/audit_intervention_test.go
git commit -m "feat(rbac): classify non-owner interventions into lead_intervention/admin.override audit actions"
```

---

### Task 5.2: Emit the audit event from the intervention path

**Files:**
- Modify: `domain/engine/engine.go` (`AuditRecorder` field + `SetAuditRecorder` + emit in `recordIntervention`)
- Test: `domain/engine/engine_audit_emit_test.go`

- [ ] **Step 1: Write the failing emission test**

Create `domain/engine/engine_audit_emit_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedAudit struct {
	action, target, detail string
}

type fakeAuditRecorder struct {
	mu      sync.Mutex
	events  []recordedAudit
	failErr error
}

func (a *fakeAuditRecorder) Record(_ context.Context, action, target, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failErr != nil {
		// Simulate the audit service swallowing a store failure internally: it
		// records nothing and returns nothing. The intervention must still stand.
		return
	}
	a.events = append(a.events, recordedAudit{action, target, detail})
}

func (a *fakeAuditRecorder) all() []recordedAudit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recordedAudit(nil), a.events...)
}

// A lead pausing a non-owned department run emits instance.lead_intervention.
func TestAudit_LeadIntervention_Emitted(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(leadCtx(uuid.New(), "engineering"), run))

	events := audit.all()
	require.Len(t, events, 1)
	assert.Equal(t, engine.AuditActionLeadIntervention, events[0].action)
	assert.Equal(t, run.String(), events[0].target, "target is the instance id")
}

// An admin canceling a non-owned run emits admin.override.
func TestAudit_AdminOverride_Emitted(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateQueued)
	require.NoError(t, f.engine.Cancel(adminCtx(uuid.New()), run))

	events := audit.all()
	require.Len(t, events, 1)
	assert.Equal(t, engine.AuditActionAdminOverride, events[0].action)
}

// The owner intervening on their own run emits NO audit event.
func TestAudit_OwnerSelf_NoEvent(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(memberCtx(owner, "engineering"), run))
	assert.Empty(t, audit.all(), "owner self-action is not audited")
}

// An audit write failure does NOT fail the intervention (best-effort).
func TestAudit_RecordFailure_DoesNotFailIntervention(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{failErr: errors.New("audit db down")}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	err := f.engine.Pause(leadCtx(uuid.New(), "engineering"), run)
	require.NoError(t, err, "a failed audit write must not fail the pause that already succeeded")
	assert.Equal(t, instance.StatePaused, currentState(t, f, run))
}

// No recorder wired => no panic (nil-safe).
func TestAudit_NoRecorder_NilSafe(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(leadCtx(uuid.New(), "engineering"), run))
}
```

(`seedDeptInstance`, `leadCtx`, `memberCtx` come from Task 4's `engine_intervention_split_test.go` — they are in the same `engine_test` package, so they are reused; do not redeclare.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run TestAudit_ -v`
Expected: FAIL — `SetAuditRecorder` undefined; no audit emission in `recordIntervention`.

- [ ] **Step 3: Add the `auditRecorder` field + setter to the Engine**

In `domain/engine/engine.go`, add a field to the `Engine` struct (after `interventions instance.InterventionRepository`):

```go
	auditRecorder    AuditRecorder
```

and a nil-safe setter near the other `Set*` methods:

```go
// SetAuditRecorder wires the Phase-2 audit service for non-owner interventions
// (lead_intervention / admin.override). When unset, no domain audit events are
// written (the legacy intervention attribution row + event are unaffected).
func (e *Engine) SetAuditRecorder(r AuditRecorder) {
	e.auditRecorder = r
}
```

- [ ] **Step 4: Emit the audit event inside `recordIntervention`**

In `domain/engine/engine.go`, in `recordIntervention`, after the existing identity guard and the legacy attribution write, add the best-effort audit emission. The existing early-return `if !ok || id.UserID == inst.OwnerID { return }` already covers owner self-actions and no-identity, so the audit classification only runs for non-owner actors:

```go
	// Phase 3: emit a domain audit event for the non-owner intervention
	// (lead_intervention or admin.override). Best-effort: a write failure is
	// logged, never returned, so the already-succeeded intervention stands.
	if e.auditRecorder != nil {
		if auditAction := auditActionFor(id, inst); auditAction != "" {
			detail := fmt.Sprintf(`{"action":%q}`, action)
			// Void best-effort: the audit service logs+swallows any store failure
			// internally, so there is nothing to handle here.
			e.auditRecorder.Record(ctx, auditAction, inst.ID.String(), detail)
		}
	}
```

Place this block inside `recordIntervention` after the `e.interventions` write block and before (or after) the `e.eventBus.Publish(...)` call. Confirm `fmt` is already imported in engine.go (it is); no `slog` is needed here since the void recorder handles failures internally.

- [ ] **Step 5: Run the emission tests to verify they pass**

Run: `go test ./domain/engine/ -run TestAudit_ -v`
Expected: PASS.

- [ ] **Step 6: Run the full engine package — confirm legacy attribution still works**

Run: `go test ./domain/engine/...`
Expected: PASS — the legacy `engine_intervention_attr_test.go` (intervention rows + `instance.intervened` events) is unaffected; audit emission is additive and gated on a wired recorder.

- [ ] **Step 7: Commit**

```bash
git add domain/engine/engine.go domain/engine/engine_audit_emit_test.go
git commit -m "feat(rbac): emit best-effort lead_intervention/admin.override audit on non-owner intervention"
```

---

### Task 5.3: Wire the Phase-2 audit service into the engine at composition

**Files:**
- Modify: the server composition root that constructs the engine (e.g. `cmd/arbitron/serve.go` or `internal/server/*.go` — locate with `grep -rn "engine.NewEngine\|SetInterventions" --include=*.go cmd internal adapter`)
- Test: covered by the existing composition smoke test, plus `go build`

- [ ] **Step 1: Locate the composition root**

Run: `grep -rn "engine.NewEngine\|\.SetInterventions(\|\.SetDismissals(" --include=*.go cmd internal adapter`
Expected: the single place the engine is built and its `Set*` seams are wired (the same place that wires interventions/dismissals).

- [ ] **Step 2: Wire `SetAuditRecorder` next to `SetInterventions`**

At that composition site, after the engine is constructed and the Phase-2 audit
service is available, pass it directly — `*audit.Service.Record` has exactly the
void `Record(ctx, action, target, detail string)` signature of `engine.AuditRecorder`:

```go
	// Phase 2 provides auditService (*audit.Service); its void Record satisfies
	// engine.AuditRecorder directly, no adapter needed.
	eng.SetAuditRecorder(auditService)
```

- [ ] **Step 3: Build the server**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Run the engine + composition tests**

Run: `go test ./domain/engine/... ./cmd/... 2>/dev/null; go test ./domain/engine/...`
Expected: PASS (engine package green; composition compiles).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(rbac): wire Phase-2 audit service into engine for intervention auditing"
```

---

## Definition of done (Task 5)

- `auditActionFor` classifies non-owner interventions: lead => `instance.lead_intervention`, admin => `admin.override`, owner => none (admin precedence over lead).
- `recordIntervention` emits the audit event best-effort; a write failure never fails the intervention; nil recorder is safe.
- The Phase-2 audit service is wired at composition.
- Engine tests prove emission for lead/admin, no emission for owner, and best-effort behavior; `go build ./...` is green.
