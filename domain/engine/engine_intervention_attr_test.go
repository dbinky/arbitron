package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInterventionRepo records created rows and can be made to fail Create.
type fakeInterventionRepo struct {
	mu        sync.Mutex
	created   []instance.Intervention
	createErr error
}

func (r *fakeInterventionRepo) Create(_ context.Context, iv *instance.Intervention) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, *iv)
	return nil
}

func (r *fakeInterventionRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.Intervention, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []instance.Intervention
	for _, iv := range r.created {
		if iv.InstanceID == instanceID {
			out = append(out, iv)
		}
	}
	return out, nil
}

func (r *fakeInterventionRepo) rows() []instance.Intervention {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]instance.Intervention, len(r.created))
	copy(cp, r.created)
	return cp
}

var _ instance.InterventionRepository = (*fakeInterventionRepo)(nil)

// --- Happy path: admin pause on user A's instance writes one row + one event ---

func TestAttr_AdminPause_RecordsRowAndEvent(t *testing.T) {
	f := newTestFixtures()
	ivRepo := &fakeInterventionRepo{}
	f.engine.SetInterventions(ivRepo)

	owner := uuid.New()
	adminID := uuid.New()
	id := seedInstance(f, owner, instance.StateRunning)

	require.NoError(t, f.engine.Pause(adminCtx(adminID), id))

	rows := ivRepo.rows()
	require.Len(t, rows, 1)
	assert.Equal(t, adminID, rows[0].ActorUserID)
	assert.Equal(t, instance.ActionPause, rows[0].Action)
	assert.Equal(t, id, rows[0].InstanceID)

	events := f.eventBus.eventsOfType(id, "instance.intervened")
	require.Len(t, events, 1)
}

// --- Validation: SetPriority detail + Rerun detail ---

func TestAttr_SetPriorityDetail_AndRerunDetail(t *testing.T) {
	f := newTestFixtures()
	ivRepo := &fakeInterventionRepo{}
	f.engine.SetInterventions(ivRepo)
	f.workflowRepo.mu.Lock()
	f.workflowRepo.workflows["wf"] = &workflow.Workflow{
		Name:  "wf",
		Nodes: []workflow.Node{{ID: "n1", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
	f.workflowRepo.mu.Unlock()
	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (bool, error) { return false, nil }
	f.queueManager.mu.Unlock()

	owner := uuid.New()
	adminID := uuid.New()

	queued := seedInstance(f, owner, instance.StateQueued)
	require.NoError(t, f.engine.SetPriority(adminCtx(adminID), queued, "high"))

	terminal := seedInstance(f, owner, instance.StateCompleted)
	newID, err := f.engine.Rerun(adminCtx(adminID), terminal)
	require.NoError(t, err)

	rows := ivRepo.rows()
	var sp, rr *instance.Intervention
	for i := range rows {
		switch rows[i].Action {
		case instance.ActionSetPriority:
			sp = &rows[i]
		case instance.ActionRerun:
			rr = &rows[i]
		}
	}
	require.NotNil(t, sp)
	require.NotNil(t, sp.Detail)
	assert.Equal(t, "high", *sp.Detail)

	require.NotNil(t, rr)
	require.NotNil(t, rr.Detail)
	assert.Equal(t, newID.String(), *rr.Detail, "rerun records the new instance id as detail")
	assert.Equal(t, terminal, rr.InstanceID, "rerun is attributed on the source instance")
}

// --- Error/failure: repo Create error is non-fatal; the op still succeeds ---

func TestAttr_RepoError_OpStillSucceeds(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetInterventions(&fakeInterventionRepo{createErr: errors.New("disk full")})

	owner := uuid.New()
	id := seedInstance(f, owner, instance.StateRunning)

	// Pause must still succeed (attribution is best-effort) and commit the state.
	require.NoError(t, f.engine.Pause(adminCtx(uuid.New()), id))
	assert.Equal(t, instance.StatePaused, currentState(t, f, id))
}

// --- Boundary: owner self-action writes NO row and NO event ---

func TestAttr_OwnerSelfAction_NotLogged(t *testing.T) {
	f := newTestFixtures()
	ivRepo := &fakeInterventionRepo{}
	f.engine.SetInterventions(ivRepo)

	owner := uuid.New()
	id := seedInstance(f, owner, instance.StateRunning)

	require.NoError(t, f.engine.Pause(userCtx(owner), id))

	assert.Empty(t, ivRepo.rows(), "owner self-action is not logged")
	assert.Empty(t, f.eventBus.eventsOfType(id, "instance.intervened"), "no intervened event for self-action")
}

// --- Isolation/security: GetInterventions visible to owner+admin, not strangers ---

func TestAttr_GetInterventions_Scoped(t *testing.T) {
	f := newTestFixtures()
	ivRepo := &fakeInterventionRepo{}
	f.engine.SetInterventions(ivRepo)

	owner := uuid.New()
	adminID := uuid.New()
	id := seedInstance(f, owner, instance.StateRunning)
	require.NoError(t, f.engine.Pause(adminCtx(adminID), id)) // writes a row

	// Owner can see it.
	asOwner, err := f.engine.GetInterventions(userCtx(owner), id)
	require.NoError(t, err)
	require.Len(t, asOwner, 1)

	// An admin can see it.
	asAdmin, err := f.engine.GetInterventions(adminCtx(uuid.New()), id)
	require.NoError(t, err)
	require.Len(t, asAdmin, 1)

	// An unrelated non-admin gets ErrNotFound (existence not leaked).
	_, err = f.engine.GetInterventions(userCtx(uuid.New()), id)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}
