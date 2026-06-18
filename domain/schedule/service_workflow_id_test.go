package schedule_test

import (
	"context"
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
