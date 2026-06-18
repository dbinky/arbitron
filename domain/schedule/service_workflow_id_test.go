package schedule_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolverByName is an in-memory schedule.WorkflowResolverID for Create. A miss
// returns workflow.ErrNotFound (the registry sentinel).
type resolverByName struct {
	nameToID map[string]uuid.UUID
}

func (r resolverByName) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	id, ok := r.nameToID[name]
	if !ok {
		return uuid.Nil, workflow.ErrNotFound
	}
	return id, nil
}

// errOnResolveID is a schedule.WorkflowResolverID that always returns a fixed
// error from ResolveID, to probe the "transient error swallowed at Create" path.
type errOnResolveID struct{ err error }

func (r errOnResolveID) ResolveID(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, r.err
}

// --- happy: Create resolves name->id and stamps WorkflowID. ---

func TestService_Create_StampsWorkflowID(t *testing.T) {
	repo := newSvcRepo()
	idA := uuid.New()
	res := resolverByName{nameToID: map[string]uuid.UUID{"report": idA}}
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true}, schedule.WithResolverID(res))

	got, err := svc.Create(ctxAs(user()), schedule.CreateInput{
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
	})
	require.NoError(t, err)
	assert.Equal(t, idA, got.WorkflowID, "Create must resolve the name to the stable id and stamp it")
	assert.Equal(t, "report", got.WorkflowName, "the display label is retained")
}

// --- error: an unresolved name leaves WorkflowID Nil without failing Create. ---

func TestService_Create_UnresolvedName_NilWorkflowID(t *testing.T) {
	repo := newSvcRepo()
	res := resolverByName{nameToID: map[string]uuid.UUID{}} // no rows
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true}, schedule.WithResolverID(res))

	got, err := svc.Create(ctxAs(user()), schedule.CreateInput{
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
	})
	require.NoError(t, err, "an unresolved name must not fail Create (best-effort id stamp)")
	assert.Equal(t, uuid.Nil, got.WorkflowID, "an unresolved name leaves WorkflowID Nil")
}

// --- error: a transient resolver error leaves WorkflowID Nil without failing Create. ---

// TestService_Create_TransientResolverError_NilWorkflowID verifies that a
// non-ErrNotFound error from ResolveID (e.g. a DB timeout) is silently swallowed
// by Create — the id stamp is best-effort. The schedule is created with Nil
// WorkflowID and fires by name until a backfill or re-create stamps the id.
func TestService_Create_TransientResolverError_NilWorkflowID(t *testing.T) {
	repo := newSvcRepo()
	res := errOnResolveID{err: errors.New("registry timeout")}
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true}, schedule.WithResolverID(res))

	got, err := svc.Create(ctxAs(user()), schedule.CreateInput{
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
	})
	require.NoError(t, err, "a transient resolver error must not fail Create (best-effort id stamp)")
	assert.Equal(t, uuid.Nil, got.WorkflowID,
		"transient resolver error leaves WorkflowID Nil — same behaviour as ErrNotFound")
}

// --- edge: no resolver wired (local mode) leaves WorkflowID Nil. ---

func TestService_Create_NoResolver_NilWorkflowID(t *testing.T) {
	repo := newSvcRepo()
	svc := schedule.NewService(repo, &fakeCanTrigger{allow: true})

	got, err := svc.Create(ctxAs(user()), schedule.CreateInput{
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
	})
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.WorkflowID, "no resolver => Nil id (local mode)")
}
