# RBAC Phase 3 — Task 4: Split canView (read) from canIntervene (mutate)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `canView` so a department peer can READ a department/public run (get/trace/variables), and introduce `canIntervene` (owner / lead / admin) for the mutate paths — a department peer who can view but is not owner/lead/admin is denied with **403 (ErrForbidden)**, not 404.

**Architecture:** `domain/engine/authz.go` gains two identity-aware predicates that take the loaded `*instance.Instance` (they need `Visibility`+`Department`, not just `OwnerID`). Reads (`GetInstance` and the `authorizeView` sub-resources) consult `canView`. Mutations (`Pause/Resume/Cancel/Retry/Rerun/SetPriority`) consult a new `authorizeIntervention` that returns:
- `ErrNotFound` when the caller cannot even **view** (existence stays hidden — unchanged posture), and
- `ErrForbidden` when the caller **can view but cannot intervene** (a department peer). This is the deliberate behavioral change: a peer learns the run exists (they can already see it in their list) but is told 403 on a control action.

**Tech Stack:** Go 1.23+, testify.

**Cross-phase dependencies:**
- **Phase 1:** `identity.Identity.InDepartment`, `LeadsDepartment`, `IsAdmin`.
- **Phase 3 Task 1:** `Instance.Visibility`/`Department`.

**Behavioral-change callout (update existing tests):** The current `authorizeIntervention` returns `ErrNotFound` for every non-owner non-admin (existence not leaked). After this task, a non-owner who is a **department peer with view access** gets `ErrForbidden`; only a caller with **no view access** still gets `ErrNotFound`. The existing tests `TestIntervention_NonOwner_RefusedNoStateChange` and `TestIntervention_NoIdentity_FailClosed` in `engine_intervention_authz_test.go` seed private (no-department) instances, so those intruders still have NO view access and STILL get `ErrNotFound` — they remain green. Re-run them to confirm; only add new peer cases.

---

### Task 4.1: `canView`/`canIntervene` predicates

**Files:**
- Modify: `domain/engine/authz.go`
- Test: `domain/engine/authz_predicates_test.go`

- [ ] **Step 1: Write the failing predicate test**

Create `domain/engine/authz_predicates_test.go` (white-box, `package engine`):

```go
package engine

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func member(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: depts,
	})
}

func lead(u uuid.UUID, led ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: led, LedDepartments: led,
	})
}

func admin(u uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: u, Role: identity.RoleAdmin})
}

func inst(owner uuid.UUID, vis instance.Visibility, dept string) *instance.Instance {
	return &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: vis, Department: dept}
}

// --- canView ---

func TestCanView_Owner(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(o), inst(o, instance.VisibilityPrivate, "")))
}
func TestCanView_Admin(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(admin(uuid.New()), inst(o, instance.VisibilityPrivate, "")))
}
func TestCanView_Public(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(uuid.New(), "finance"), inst(o, instance.VisibilityPublic, "engineering")))
}
func TestCanView_DepartmentMember(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanView_DepartmentNonMember_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(member(uuid.New(), "finance"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanView_OthersPrivate_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(member(uuid.New(), "engineering"), inst(o, instance.VisibilityPrivate, "engineering")))
}
func TestCanView_NoIdentity_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(context.Background(), inst(o, instance.VisibilityPublic, "")))
}

// --- canIntervene ---

func TestCanIntervene_Owner(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(member(o, "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Admin(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(admin(uuid.New()), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Lead(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(lead(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Peer_Denied(t *testing.T) {
	o := uuid.New()
	// A department MEMBER who is not owner and not lead: can view, cannot intervene.
	assert.False(t, canIntervene(member(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_LeadOfOtherDept_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canIntervene(lead(uuid.New(), "finance"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_NoIdentity_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canIntervene(context.Background(), inst(o, instance.VisibilityDepartment, "engineering")))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run 'TestCanView|TestCanIntervene' -v`
Expected: FAIL — current `canView(ctx, ownerID uuid.UUID)` has a different signature; `canIntervene` undefined.

- [ ] **Step 3: Rewrite the predicates in authz.go**

In `domain/engine/authz.go`, replace the existing `canView` (lines 25–35) with the two instance-aware predicates:

```go
// canView reports whether the context identity may READ an instance: admins and
// the owner always; a public run is visible to everyone; a department run is
// visible to members of that department. Missing identity => false (fail closed).
func canView(ctx context.Context, inst *instance.Instance) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	if id.IsAdmin() || id.UserID == inst.OwnerID {
		return true
	}
	if inst.Visibility == instance.VisibilityPublic {
		return true
	}
	return inst.Visibility == instance.VisibilityDepartment && id.InDepartment(inst.Department)
}

// canIntervene reports whether the context identity may MUTATE an instance
// (cancel/pause/resume/retry/rerun/priority): the owner, a lead of the run's
// department, or an admin. A department peer (member, not owner, not lead) is
// denied even though canView is true. Missing identity => false.
func canIntervene(ctx context.Context, inst *instance.Instance) bool {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false
	}
	return id.IsAdmin() || id.UserID == inst.OwnerID || id.LeadsDepartment(inst.Department)
}
```

Add `"github.com/arbitron/arbitron/domain/instance"` to the imports of `authz.go`. Keep `ownerConstraint` (still referenced nowhere after Task 3? — if `ListInstances` no longer calls it, delete `ownerConstraint` and its `uuid` import if now unused; run `go build` to confirm).

- [ ] **Step 4: Run the predicate tests to verify they pass**

Run: `go test ./domain/engine/ -run 'TestCanView|TestCanIntervene' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/engine/authz.go domain/engine/authz_predicates_test.go
git commit -m "feat(rbac): split canView (read +public/+department) from canIntervene (owner/lead/admin)"
```

---

### Task 4.2: Wire `canView` into reads, `canIntervene` into mutates

**Files:**
- Modify: `domain/engine/engine.go` (`GetInstance`, `authorizeView`, `authorizeIntervention`)
- Test: `domain/engine/engine_intervention_split_test.go`

- [ ] **Step 1: Write the failing read/intervene-split test**

Create `domain/engine/engine_intervention_split_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func memberCtx(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: depts,
	})
}

func leadCtx(u uuid.UUID, led ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: led, LedDepartments: led,
	})
}

// seedDeptInstance puts a department-visible instance owned by owner.
func seedDeptInstance(f *testFixtures, owner uuid.UUID, dept string, state instance.State) uuid.UUID {
	id := uuid.New()
	failedNode := "n1"
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID: id, OwnerID: owner, WorkflowName: "wf", State: state,
		Priority: instance.PriorityNormal, Variables: map[string]any{"k": "v"},
		OriginalVariables: map[string]any{"k": "v"}, FailedNodeID: &failedNode,
		Visibility: instance.VisibilityDepartment, Department: dept,
	}
	f.instanceRepo.mu.Unlock()
	return id
}

// A peer CAN read a department run (Get/Trace/Variables succeed).
func TestSplit_Peer_CanRead(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	peer := memberCtx(uuid.New(), "engineering")

	got, err := f.engine.GetInstance(peer, run)
	require.NoError(t, err)
	assert.Equal(t, run, got.ID, "a department peer can read a department run")

	_, err = f.engine.GetVariables(peer, run)
	require.NoError(t, err, "a department peer can read variables")
}

// A peer CANNOT intervene on a department run -> ErrForbidden (403), not 404.
func TestSplit_Peer_CannotIntervene_Forbidden(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	peer := memberCtx(uuid.New(), "engineering")

	running := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	err := f.engine.Pause(peer, running)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrForbidden, "a viewing-but-non-owner peer is forbidden, not not-found")
	assert.NotErrorIs(t, err, instance.ErrNotFound)
	assert.Equal(t, instance.StateRunning, currentState(t, f, running), "no state change on a forbidden intervention")
}

// The owner can intervene on their own department run.
func TestSplit_Owner_CanIntervene(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(memberCtx(owner, "engineering"), run))
	assert.Equal(t, instance.StatePaused, currentState(t, f, run))
}

// A department lead can intervene on a run they do not own.
func TestSplit_Lead_CanIntervene(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(leadCtx(uuid.New(), "engineering"), run))
	assert.Equal(t, instance.StatePaused, currentState(t, f, run))
}

// A caller with NO view access (different department, private run) still gets
// ErrNotFound on an intervention — existence is not leaked.
func TestSplit_NoView_StillNotFound(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	// Private run: only the owner can view; an outsider gets 404.
	priv := seedInstance(f, owner, instance.StateRunning) // private (seedInstance sets no visibility)
	outsider := memberCtx(uuid.New(), "engineering")
	err := f.engine.Pause(outsider, priv)
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no view access => existence hidden (404), not 403")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/engine/ -run TestSplit_ -v`
Expected: FAIL — `GetInstance`/`authorizeIntervention` still call the old `canView(ctx, ownerID)`; peers get `ErrNotFound`, not `ErrForbidden`, and cannot read department runs.

- [ ] **Step 3: Update `GetInstance` to use the new `canView`**

In `domain/engine/engine.go`, in `GetInstance`, replace `if !canView(ctx, inst.OwnerID) {` with `if !canView(ctx, inst) {`.

- [ ] **Step 4: Update `authorizeView` (sub-resource reads) — no signature change**

`authorizeView` delegates to `GetInstance`, so it inherits the new `canView` automatically. Confirm no direct `canView(ctx, ...OwnerID)` call remains in `authorizeView`.

- [ ] **Step 5: Rewrite `authorizeIntervention` to split 404 vs 403**

In `domain/engine/engine.go`, replace the body of `authorizeIntervention` (lines ~322–334) with:

```go
func (e *Engine) authorizeIntervention(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	// No view access => hide existence (404), preserving the non-leak posture.
	if !canView(ctx, inst) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	// Viewable but not owner/lead/admin (a department peer) => 403, not 404.
	if !canIntervene(ctx, inst) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	}
	return inst, nil
}
```

Confirm `instance.ErrForbidden` exists (it is the aliased `identity.ErrForbidden`, per `domain/instance/user_service.go`). If `domain/instance` does not yet export `ErrForbidden`, add `var ErrForbidden = identity.ErrForbidden` in `domain/instance` (it already exists per the authz_sentinel_test.go reference — verify with `grep`).

- [ ] **Step 6: Run the split tests to verify they pass**

Run: `go test ./domain/engine/ -run TestSplit_ -v`
Expected: PASS.

- [ ] **Step 7: Re-run the legacy intervention tests — confirm posture preserved**

Run: `go test ./domain/engine/ -run TestIntervention_ -v`
Expected: PASS. `TestIntervention_NonOwner_RefusedNoStateChange` and `TestIntervention_NoIdentity_FailClosed` seed **private** instances (no department), so those intruders have no view access and still get `ErrNotFound`. If any legacy test seeds a department/public instance and asserts `ErrNotFound` for a peer, update it to `ErrForbidden` and note the behavioral change in the commit.

- [ ] **Step 8: Run the full engine package**

Run: `go test ./domain/engine/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add domain/engine/engine.go domain/engine/engine_intervention_split_test.go
git commit -m "feat(rbac): peer can read department runs; peer intervention returns 403 not 404"
```

---

## Definition of done (Task 4)

- `canView(ctx, *Instance)` grants owner/admin + public + department-member read.
- `canIntervene(ctx, *Instance)` grants owner/lead/admin mutate only.
- Reads (`GetInstance`, trace, variables, history, interventions) use `canView`.
- `authorizeIntervention` returns `ErrNotFound` for no-view, `ErrForbidden` for view-but-no-intervene.
- Legacy private-instance intervention tests stay green; new peer tests prove read-yes / mutate-403.
