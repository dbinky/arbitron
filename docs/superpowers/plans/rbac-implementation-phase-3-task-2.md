# RBAC Phase 3 — Task 2: Trigger Stamping + Per-Trigger Department Override

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In `engine.Trigger`, after the workflow + its publication are resolved, stamp the new instance's `Visibility` and `Department` from the publication, apply a validated optional per-trigger `department` override, and persist both on the instance.

**Architecture:** `engine.Trigger` already resolves the workflow via `workflow.LoadVisible`. Phase 2 extends `LoadVisible` to also surface the resolved **publication** (its `Visibility` + `Department`); private workflows (no publication) resolve to `VisibilityPrivate`. Phase 3 adds a stamping step that maps the publication onto the instance, plus a pure validation helper for the override. The override is validated against the triggering identity's **effective departments** (Phase 1 `identity.Identity.Departments` / `InDepartment`); for non-interactive triggers (webhook/API token) the triggering user's **persisted** departments are already loaded into the identity by the Phase 1 auth middleware, so the same check applies.

**Tech Stack:** Go 1.23+, testify.

**Cross-phase dependencies (consumed contracts — DO NOT build here):**
- **Phase 1:** `identity.Identity.Departments []string` and `id.InDepartment(dept string) bool`. The auth middleware populates `Departments` for interactive AND non-interactive (token/webhook-via-token) callers from persisted membership.
- **Phase 2:** `workflow.LoadVisible` returns the resolved publication metadata. The exact surface Phase 2 lands is one of:
  - a third return value `pub workflow.Publication` (with `pub.Visibility workflow.Visibility` and `pub.Department string`), **or**
  - the existing `Scope` return enriched so the engine can read visibility/department.
  This task is written against a small **engine-local adapter** (`resolvePublication`) so the exact Phase-2 shape is isolated to one function. If Phase 2's signature differs, only `resolvePublication` changes.

**Cross-task dependency:** Requires Phase 3 Task 1 (the `instance.Visibility` type + `Instance.Department`/`Instance.Visibility` fields + repo persistence).

---

### Task 2.1: Per-trigger override validation (pure helper)

**Files:**
- Create: `domain/engine/trigger_stamp.go`
- Test: `domain/engine/trigger_stamp_test.go`

- [ ] **Step 1: Write the failing test**

Create `domain/engine/trigger_stamp_test.go`:

```go
package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func idWithDepts(depts ...string) identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser, Departments: depts}
}

// A public workflow: no override -> visibility public, department = the
// triggerer's stamping department (first effective dept) for dashboard
// attribution.
func TestResolveStamp_Public_NoOverride_UsesTriggererDept(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "engineering", got.Department, "public run stamped with triggerer's dept for attribution")
}

// A public workflow with no triggerer departments stamps an empty department.
func TestResolveStamp_Public_NoDepts_EmptyDepartment(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "", got.Department)
}

// A department workflow: department comes from the publication.
func TestResolveStamp_Department_UsesPublicationDept(t *testing.T) {
	id := idWithDepts("engineering", "platform")
	got, err := resolveStamp(id, instance.VisibilityDepartment, "platform", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
	assert.Equal(t, "platform", got.Department)
}

// A private workflow: visibility private, no department, override ignored on the
// audience but still validated when present.
func TestResolveStamp_Private_NoDepartment(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPrivate, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility)
	assert.Equal(t, "", got.Department)
}

// Override within the caller's effective departments is accepted and replaces
// the stamped department for a department-visible run.
func TestResolveStamp_Override_Accepted(t *testing.T) {
	id := idWithDepts("engineering", "platform")
	got, err := resolveStamp(id, instance.VisibilityDepartment, "engineering", "platform")
	require.NoError(t, err)
	assert.Equal(t, "platform", got.Department, "override within caller's depts replaces stamped department")
}

// Override naming a department the caller does NOT belong to is rejected — a
// caller can never widen beyond their own departments.
func TestResolveStamp_Override_OutsideDepartments_Rejected(t *testing.T) {
	id := idWithDepts("engineering")
	_, err := resolveStamp(id, instance.VisibilityPublic, "", "finance")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidDepartmentOverride)
}

// Override is normalized/compared case-insensitively the same way membership is
// (Phase 1 normalizes to lowercase); an exact-case member match is accepted.
func TestResolveStamp_Override_Public_Accepted(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "engineering")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "engineering", got.Department, "override sets the stamped attribution department for a public run")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run TestResolveStamp -v`
Expected: FAIL — `undefined: resolveStamp`, `undefined: ErrInvalidDepartmentOverride`.

- [ ] **Step 3: Implement the stamping helper**

Create `domain/engine/trigger_stamp.go`:

```go
package engine

import (
	"errors"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// ErrInvalidDepartmentOverride is returned when a per-trigger department
// override names a department the triggering caller does not belong to. It is a
// validation error (maps to HTTP 400 at the adapter seam), NOT a forbidden/authz
// failure — the caller is asking for an impossible scope, not a privileged one.
var ErrInvalidDepartmentOverride = errors.New("engine: department override must be one of the caller's departments")

// stamp is the (visibility, department) pair the engine writes onto a new
// instance at trigger time.
type stamp struct {
	Visibility instance.Visibility
	Department string
}

// resolveStamp computes the instance visibility+department stamp for a trigger.
//
//   - pubVisibility/pubDepartment: the resolved workflow publication's audience
//     (private => VisibilityPrivate with empty department).
//   - override: the optional per-trigger department from the request ("" when
//     absent). When non-empty it MUST be one of the caller's effective
//     departments, else ErrInvalidDepartmentOverride.
//
// Stamping rules (spec §Instance Visibility, design §Trigger Stamping):
//   - public:     Visibility=public; Department = override, else the triggerer's
//                 first effective department (dashboard attribution), else "".
//   - department: Visibility=department; Department = override, else the
//                 publication's department.
//   - private:    Visibility=private; Department = "" (override ignored for the
//                 audience but still validated above).
//
// The override never widens beyond the caller's own departments.
func resolveStamp(id identity.Identity, pubVisibility instance.Visibility, pubDepartment, override string) (stamp, error) {
	if override != "" && !id.InDepartment(override) {
		return stamp{}, ErrInvalidDepartmentOverride
	}

	switch pubVisibility {
	case instance.VisibilityPublic:
		dept := override
		if dept == "" {
			dept = firstDepartment(id)
		}
		return stamp{Visibility: instance.VisibilityPublic, Department: dept}, nil
	case instance.VisibilityDepartment:
		dept := override
		if dept == "" {
			dept = pubDepartment
		}
		return stamp{Visibility: instance.VisibilityDepartment, Department: dept}, nil
	default: // private (or unknown -> fail closed to private/owner-only)
		return stamp{Visibility: instance.VisibilityPrivate, Department: ""}, nil
	}
}

// firstDepartment returns the caller's first effective department, or "" when
// they belong to none (claimless callers -> default group is supplied by Phase 1,
// so this is "" only for an identity with no Departments slice).
func firstDepartment(id identity.Identity) string {
	if len(id.Departments) == 0 {
		return ""
	}
	return id.Departments[0]
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./domain/engine/ -run TestResolveStamp -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/trigger_stamp.go domain/engine/trigger_stamp_test.go
git commit -m "feat(rbac): pure trigger-stamp resolver with validated department override"
```

---

### Task 2.2: Wire stamping into `engine.Trigger`

**Files:**
- Modify: `domain/engine/engine.go` (`Trigger` signature + body; `resolvePublication` adapter)
- Modify: callers of `Trigger` — `domain/engine/engine.go:Rerun`, `adapter/http/workflow_handler.go`, `adapter/mcp/tools_instance.go` (override param wiring is Task 6; here they pass `""`)
- Test: `domain/engine/engine_trigger_stamp_test.go`

- [ ] **Step 1: Write the failing wiring test**

Create `domain/engine/engine_trigger_stamp_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxDepts(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Username: "u", Role: identity.RoleUser, Departments: depts,
	})
}

// A department-published workflow stamps the run with visibility=department and
// the publication's department.
func TestTrigger_StampsDepartmentFromPublication(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	// The harness's scopeAwareRepo serves the workflow; the publication lookup
	// is seeded so resolvePublication returns department/engineering.
	h.repo.put(workflow.Shared(), sharedWf("triage"))
	h.setPublication("triage", instance.VisibilityDepartment, "engineering")

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller, "engineering"), "triage", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityDepartment, inst.Visibility)
	assert.Equal(t, "engineering", inst.Department)
}

// A public workflow stamps visibility=public with the triggerer's department
// for attribution.
func TestTrigger_StampsPublicWithTriggererDept(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("pub-wf"))
	h.setPublication("pub-wf", instance.VisibilityPublic, "")

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller, "platform"), "pub-wf", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, inst.Visibility)
	assert.Equal(t, "platform", inst.Department)
}

// A private workflow (no publication) stamps private with empty department —
// matching today's owner-only behavior.
func TestTrigger_PrivateWorkflow_StampsPrivate(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("secret")) // no publication record

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller, "engineering"), "secret", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPrivate, inst.Visibility)
	assert.Equal(t, "", inst.Department)
}

// An override within the caller's departments replaces the stamped department.
func TestTrigger_OverrideAccepted(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("triage"))
	h.setPublication("triage", instance.VisibilityDepartment, "engineering")

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller, "engineering", "platform"), "triage", nil, "normal", "platform")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "platform", inst.Department)
}

// An override outside the caller's departments is rejected and no instance is
// created.
func TestTrigger_OverrideRejected_NoInstance(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("triage"))
	h.setPublication("triage", instance.VisibilityDepartment, "engineering")

	_, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller, "engineering"), "triage", nil, "normal", "finance")
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrInvalidDepartmentOverride)
	assert.Empty(t, h.instances.instances, "no instance is created when the override is invalid")
}

// The existing Trigger entrypoint still works (override defaults to "").
func TestTrigger_BackwardCompatEntrypoint(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("pub-wf"))
	h.setPublication("pub-wf", instance.VisibilityPublic, "")

	id, _, err := h.engine.Trigger(ctxDepts(caller, "platform"), "pub-wf", nil, "normal")
	require.NoError(t, err)
	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, inst.Visibility)
}
```

Add the publication-seeding seam to the harness in `domain/engine/engine_trigger_scope_test.go` (extend `scopeAwareRepo` and `triggerHarness`):

```go
// publications maps workflow name -> (visibility, department) the Phase-2
// publication lookup would return. Absent name => private (no publication).
// Added for Phase 3 trigger-stamp tests.
func (h *triggerHarness) setPublication(name string, vis instance.Visibility, dept string) {
	h.repo.mu.Lock()
	defer h.repo.mu.Unlock()
	if h.repo.publications == nil {
		h.repo.publications = map[string]struct {
			vis  instance.Visibility
			dept string
		}{}
	}
	h.repo.publications[name] = struct {
		vis  instance.Visibility
		dept string
	}{vis, dept}
}
```

and add the `publications` field to the `scopeAwareRepo` struct plus a `PublicationOf(name)` method it exposes to `resolvePublication` (see Step 3's adapter contract). Import `domain/instance` in that test file if not already.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run 'TestTrigger_Stamps|TestTrigger_Private|TestTrigger_Override|TestTrigger_BackwardCompat' -v`
Expected: FAIL — `TriggerWithDepartment` undefined; harness lacks `setPublication`/`publications`.

- [ ] **Step 3: Add the `resolvePublication` adapter + `TriggerWithDepartment`, refactor `Trigger`**

In `domain/engine/engine.go`, add the publication adapter (the SINGLE place that depends on the Phase-2 surface):

```go
// resolvePublication returns the resolved workflow's audience (visibility +
// department) for stamping. It isolates the Phase-2 publication surface from the
// rest of the engine: a private workflow (no publication record) resolves to
// VisibilityPrivate with an empty department. When Phase 2 enriches LoadVisible
// (third return value or a richer Scope), only this function changes.
func (e *Engine) resolvePublication(scope workflow.Scope, workflowName string) (instance.Visibility, string) {
	// Phase 2 contract: a workflow resolved from the Shared scope MAY have a
	// publication record carrying visibility+department. Private-scope hits are
	// always private. Replace the body below with the real Phase-2 lookup
	// (e.workflows / a PublishedStore the engine is given). Until Phase 2 lands,
	// this returns private for everything, which is the safe owner-only default.
	if scope.Kind != workflow.ScopeShared {
		return instance.VisibilityPrivate, ""
	}
	if e.publications == nil {
		return instance.VisibilityPublic, "" // shared-without-store => public (local-mode openness)
	}
	pub, ok := e.publications.PublicationOf(workflowName)
	if !ok {
		return instance.VisibilityPublic, ""
	}
	return pub.Visibility, pub.Department
}
```

Add a `publications` port field on `Engine` and a `SetPublications` wiring setter (nil-safe). Define the minimal port in `domain/engine` so the engine does not import `domain/workflow` internals beyond the `Visibility` mirror:

```go
// PublicationLookup is the minimal read surface Phase 3's trigger stamping needs
// from the Phase-2 publication store. It returns the published workflow's
// audience for stamping. Implemented by the sqlite published-workflow repo.
type PublicationLookup interface {
	PublicationOf(workflowName string) (Publication, bool)
}

// Publication is the audience metadata stamped onto a run.
type Publication struct {
	Visibility instance.Visibility
	Department string
}
```

(Place `PublicationLookup`/`Publication` in a small `domain/engine/publication.go`. If Phase 2 already defines an equivalent port, adapt `resolvePublication` to it instead and delete this stub — note this in the commit.)

Refactor `Trigger` to delegate to a new `TriggerWithDepartment`:

```go
// Trigger preserves the existing entrypoint (no department override).
func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	return e.TriggerWithDepartment(ctx, workflowName, variables, priority, "")
}

// TriggerWithDepartment loads a workflow, validates inputs, stamps the run's
// visibility+department (applying the optional validated department override),
// creates the instance, and enqueues it.
func (e *Engine) TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority string, departmentOverride string) (uuid.UUID, bool, error) {
	// ... existing ctx nil-guard, triggerOwner, LoadVisible body unchanged ...
	// CHANGE: capture the scope from LoadVisible (currently discarded as `_`):
	//   wf, scope, err := workflow.LoadVisible(ctx, e.workflows, owner, workflowName)
	//
	// After the workflow + inputs are resolved and BEFORE building `inst`,
	// compute the stamp:
	//
	//   vis, dept := e.resolvePublication(scope, workflowName)
	//   callerID, _ := identity.FromContext(ctx) // zero-value Identity for system triggers
	//   st, stErr := resolveStamp(callerID, vis, dept, departmentOverride)
	//   if stErr != nil {
	//       return uuid.Nil, false, stErr
	//   }
	//
	// Then set the two new fields on the instance literal:
	//   Department: st.Department,
	//   Visibility: st.Visibility,
}
```

Implement the full method by copying the existing `Trigger` body, changing the `LoadVisible` call to bind `scope`, inserting the stamp computation, and adding `Department`/`Visibility` to the `inst := &instance.Instance{...}` literal. A system trigger (no identity) yields a zero-value `identity.Identity` whose `Departments` is nil; `resolveStamp` then stamps an empty attribution department (private/public unchanged), matching local-mode default-group behavior.

- [ ] **Step 4: Update internal `Trigger` caller (`Rerun`) — no change needed**

`Rerun` calls `e.Trigger(...)`, which now delegates to `TriggerWithDepartment(..., "")`. Confirm `Rerun` compiles unchanged.

- [ ] **Step 5: Run the wiring tests to verify they pass**

Run: `go test ./domain/engine/ -run 'TestTrigger' -v`
Expected: PASS (new stamping tests AND all pre-existing `engine_trigger_scope_test.go` tests stay green — stamping is additive and private/public defaults preserve old behavior).

- [ ] **Step 6: Run the full engine package**

Run: `go test ./domain/engine/...`
Expected: PASS.

- [ ] **Step 7: Build the module (HTTP/MCP callers still compile)**

Run: `go build ./...`
Expected: success — `adapter/http/workflow_handler.go` and `adapter/mcp/tools_instance.go` still call the 4-arg `Trigger`; the override param surface lands in Task 6.

- [ ] **Step 8: Commit**

```bash
git add domain/engine/engine.go domain/engine/publication.go \
        domain/engine/engine_trigger_stamp_test.go domain/engine/engine_trigger_scope_test.go
git commit -m "feat(rbac): stamp instance visibility+department at trigger with validated override"
```

---

## Definition of done (Task 2)

- `resolveStamp` is a pure, fully tested helper; `ErrInvalidDepartmentOverride` is the validation sentinel.
- `engine.TriggerWithDepartment` stamps `Visibility`+`Department` per the publication and override; `engine.Trigger` delegates with `""`.
- Public runs stamp the triggerer's department for attribution; department runs use the publication (or override) department; private runs stamp private/empty.
- An out-of-department override is rejected and creates no instance.
- All pre-existing trigger-scope tests remain green; `go build ./...` succeeds.
