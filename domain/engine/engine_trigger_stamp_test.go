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
