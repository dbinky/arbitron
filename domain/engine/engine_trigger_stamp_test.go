package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
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

// setPublication wires a fakePubLookup onto the harness engine carrying the
// (visibility, department) the Phase-2 publication lookup would return for name.
func setPublication(h *triggerHarness, name string, vis workflow.Visibility, dept string) {
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		name: {Publisher: uuid.New(), Visibility: vis, Department: dept},
	}})
}

// A department-published workflow stamps the run with visibility=department and
// the publication's department.
func TestTrigger_StampsDepartmentFromPublication(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("triage"))
	setPublication(h, "triage", workflow.VisibilityDepartment, "engineering")

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
	setPublication(h, "pub-wf", workflow.VisibilityPublic, "")

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
	setPublication(h, "triage", workflow.VisibilityDepartment, "engineering")

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
	setPublication(h, "triage", workflow.VisibilityDepartment, "engineering")

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
	setPublication(h, "pub-wf", workflow.VisibilityPublic, "")

	id, _, err := h.engine.Trigger(ctxDepts(caller, "platform"), "pub-wf", nil, "normal")
	require.NoError(t, err)
	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, inst.Visibility)
}

// ---------------------------------------------------------------------------
// FA #15: Trigger stamping robustness — missing stamp scenarios
// ---------------------------------------------------------------------------

// A system trigger (scheduler/webhook — no context identity) on a public
// workflow stamps visibility=public with an empty attribution department.
// firstDepartment of a zero identity is ""; this must persist to the instance.
func TestTrigger_SystemTrigger_PublicWorkflow_EmptyAttributionDept(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("pub-wf"))
	setPublication(h, "pub-wf", workflow.VisibilityPublic, "")

	// context.Background() has no identity → system/webhook trigger path.
	id, _, err := h.engine.Trigger(context.Background(), "pub-wf", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, inst.Visibility)
	assert.Equal(t, "", inst.Department,
		"system trigger has no effective departments; attribution department must be empty")
}

// An admin trigger on a department-visibility workflow they are NOT a member of
// stamps the run with the PUBLICATION's department (not the admin's — they have
// none). The admin override audit path fires; the stamp comes from pubDept.
func TestTrigger_AdminNonMember_DeptWorkflow_StampsPublicationDept(t *testing.T) {
	h := newTriggerHarness()
	admin := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	setPublication(h, "eng-wf", workflow.VisibilityDepartment, "engineering")

	// Admin belongs to NO departments.
	adminCtx := identity.WithIdentity(context.Background(),
		identity.Identity{UserID: admin, Username: "adm", Role: identity.RoleAdmin})
	id, _, err := h.engine.Trigger(adminCtx, "eng-wf", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityDepartment, inst.Visibility)
	assert.Equal(t, "engineering", inst.Department,
		"admin override must stamp the publication's department, not the admin's (empty) membership")
}

// ---------------------------------------------------------------------------
// FA #28: cross-package visibility constant equality
// ---------------------------------------------------------------------------

// TestWorkflowAndInstanceVisibilityConstants_AreEqual verifies that the
// workflow-side and instance-side visibility constant strings are identical.
// The engine uses these interchangeably: a workflow publication's Visibility
// is directly assigned to the instance stamp in resolveStamp. A silent
// divergence (e.g., workflow uses "dept" but instance uses "department") would
// break the entire visibility gating system without failing any isolated unit test.
func TestWorkflowAndInstanceVisibilityConstants_AreEqual(t *testing.T) {
	assert.Equal(t, string(workflow.VisibilityPrivate), string(instance.VisibilityPrivate),
		"workflow.VisibilityPrivate must equal instance.VisibilityPrivate (shared DB CHECK constraint value)")
	assert.Equal(t, string(workflow.VisibilityDepartment), string(instance.VisibilityDepartment),
		"workflow.VisibilityDepartment must equal instance.VisibilityDepartment")
	assert.Equal(t, string(workflow.VisibilityPublic), string(instance.VisibilityPublic),
		"workflow.VisibilityPublic must equal instance.VisibilityPublic")
}

// ---------------------------------------------------------------------------
// FA #29: read-gating + list-authz cross-surface consistency
// ---------------------------------------------------------------------------

// TestReadGating_DeptWorkflow_ListConsistency verifies that the workflow
// canSeePublication gate (implicit in Trigger → LoadVisible) and the instance
// listConstraint gate (ListInstances) enforce the SAME department rule.
// An engineering member who can trigger a dept-visible engineering workflow
// must also see the resulting instance in their list. A sales member who
// cannot access the engineering workflow must also be unable to see the instance.
func TestReadGating_DeptWorkflow_ListConsistency(t *testing.T) {
	h := newTriggerHarness()

	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	setPublication(h, "eng-wf", workflow.VisibilityDepartment, "engineering")

	engUID := uuid.New()
	engCtx := ctxDepts(engUID, "engineering")

	// Engineering member triggers the dept-visible workflow. Trigger calls
	// LoadVisible → canSeePublication — if this succeeds, the workflow gate passed.
	instID, _, err := h.engine.Trigger(engCtx, "eng-wf", nil, "normal")
	require.NoError(t, err, "engineering member must be able to trigger a dept-visible engineering workflow")

	// List as engineering member: instance must appear (listConstraint passes).
	list, err := h.engine.ListInstances(engCtx, instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(list, instID),
		"engineering member who triggered a dept-visible workflow must also see the instance in ListInstances")

	// List as sales member: instance must NOT appear (listConstraint denies).
	salesCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Username: "s", Role: identity.RoleUser,
		Departments: []string{"sales"},
	})
	saleList, err := h.engine.ListInstances(salesCtx, instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(saleList, instID),
		"sales member must not see an engineering dept-visible instance — both workflow and instance gates must deny consistently")
}

// ---------------------------------------------------------------------------
// FA #32: trigger override + list authz — audience verification
// ---------------------------------------------------------------------------

// TestTrigger_Override_ListAudienceConsistency verifies the end-to-end chain:
// 1. Caller with [engineering, platform] triggers with override "platform"
// 2. Instance is stamped Visibility=department, Department="platform"
// 3. A platform member can list the instance
// 4. An engineering member (wrong dept) cannot list it
// 5. An engineering-only member cannot override to platform (no widening)
func TestTrigger_Override_ListAudienceConsistency(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()

	// A public-org workflow with a department publication for "engineering".
	h.repo.put(workflow.Shared(), sharedWf("wf"))
	setPublication(h, "wf", workflow.VisibilityDepartment, "engineering")

	// Trigger with override to "platform" (caller is in both engineering and platform).
	instID, _, err := h.engine.TriggerWithDepartment(
		ctxDepts(caller, "engineering", "platform"),
		"wf", nil, "normal", "platform")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), instID)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityDepartment, inst.Visibility)
	assert.Equal(t, "platform", inst.Department, "override must stamp platform, not engineering")

	// Platform member can list the instance (same dept).
	platformList, err := h.engine.ListInstances(
		ctxDepts(uuid.New(), "platform"),
		instance.ListFilters{})
	require.NoError(t, err)
	assert.True(t, containsID(platformList, instID),
		"platform member must see the platform-overridden instance in their list")

	// Engineering member cannot list it (wrong dept after override).
	engList, err := h.engine.ListInstances(
		ctxDepts(uuid.New(), "engineering"),
		instance.ListFilters{})
	require.NoError(t, err)
	assert.False(t, containsID(engList, instID),
		"engineering member must NOT see the instance — the override moved it to platform")

	// Scope-widening guard: engineering-only caller cannot override to platform.
	_, _, wErr := h.engine.TriggerWithDepartment(
		ctxDepts(uuid.New(), "engineering"),
		"wf", nil, "normal", "platform")
	assert.ErrorIs(t, wErr, engine.ErrInvalidDepartmentOverride,
		"override to a dept outside the caller's set must be rejected")
}
