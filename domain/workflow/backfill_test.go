package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listingFakeRepo augments fakeRepo's List to return all summaries per scope so
// BackfillRegistry can enumerate workflows. (fakeRepo.List already returns
// per-scope summaries; this exercises both scopes through the service backfill.)

// TestBackfillRegistry_RegistersUnregistered verifies the startup backfill scans
// both scopes and registers every workflow lacking a registry row, assigning and
// persisting a stable id.
func TestBackfillRegistry_RegistersUnregistered(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()

	// Two legacy private workflows + one shared, all id-less.
	for _, n := range []string{"p1", "p2"} {
		w := wf(n)
		w.ID = uuid.Nil
		repo.bucket(workflow.Private(owner))[n] = w
	}
	sh := wf("s1")
	sh.ID = uuid.Nil
	repo.bucket(workflow.Shared())["s1"] = sh

	require.NoError(t, svc.BackfillRegistry(ctxWith(owner, false)))

	assert.Equal(t, 3, reg.count(), "all three workflows registered")
	// Each folder now carries the persisted id.
	for _, n := range []string{"p1", "p2"} {
		assert.NotEqual(t, uuid.Nil, repo.bucket(workflow.Private(owner))[n].ID, n)
	}
	assert.NotEqual(t, uuid.Nil, repo.bucket(workflow.Shared())["s1"].ID)
}

// TestBackfillRegistry_Idempotent verifies running the backfill twice yields no
// duplicate rows and no error, and that ids stay stable.
func TestBackfillRegistry_Idempotent(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()

	w := wf("p1")
	w.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["p1"] = w

	require.NoError(t, svc.BackfillRegistry(ctxWith(owner, false)))
	firstID := repo.bucket(workflow.Private(owner))["p1"].ID
	require.NotEqual(t, uuid.Nil, firstID)

	require.NoError(t, svc.BackfillRegistry(ctxWith(owner, false)))
	assert.Equal(t, 1, reg.count(), "no duplicate rows on second pass")
	assert.Equal(t, firstID, repo.bucket(workflow.Private(owner))["p1"].ID, "id stable")
}

// TestBackfillRegistry_PreservesExistingIDs verifies that a workflow already
// carrying an id is registered under that same id (not re-allocated).
func TestBackfillRegistry_PreservesExistingIDs(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()

	existing := uuid.New()
	w := wf("p1")
	w.ID = existing
	repo.bucket(workflow.Private(owner))["p1"] = w

	require.NoError(t, svc.BackfillRegistry(ctxWith(owner, false)))
	id, err := reg.ResolveID(ctxWith(owner, false), "p1")
	require.NoError(t, err)
	assert.Equal(t, existing, id, "registered under the workflow's existing id")
}

// TestBackfillRegistry_NoRegistry_NoOp verifies the backfill is a safe no-op when
// no registry is wired (local/unconfigured deployments).
func TestBackfillRegistry_NoRegistry_NoOp(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	w := wf("p1")
	w.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["p1"] = w

	require.NoError(t, svc.BackfillRegistry(ctxWith(owner, false)))
}
