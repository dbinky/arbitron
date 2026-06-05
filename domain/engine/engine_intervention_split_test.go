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
