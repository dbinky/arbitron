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
