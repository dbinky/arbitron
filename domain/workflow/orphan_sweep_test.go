package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sweepBackfiller is a fakeRefBackfiller extended with the orphan-sweep seam:
// referenced records the NON-NULL workflow_id values present in reference rows,
// and cleared records the ids ClearReferenceID was asked to reconcile to NULL.
type sweepBackfiller struct {
	referenced    []uuid.UUID
	referencedErr error
	cleared       []uuid.UUID
	clearErr      error
}

func (f *sweepBackfiller) UnresolvedReferenceNames(context.Context) ([]string, error) {
	return nil, nil
}
func (f *sweepBackfiller) SetReferenceID(context.Context, string, uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *sweepBackfiller) ReferencedWorkflowIDs(context.Context) ([]uuid.UUID, error) {
	return f.referenced, f.referencedErr
}
func (f *sweepBackfiller) ClearReferenceID(_ context.Context, id uuid.UUID) (int64, error) {
	if f.clearErr != nil {
		return 0, f.clearErr
	}
	f.cleared = append(f.cleared, id)
	return 1, nil
}

// --- Happy path: a dangling workflow_id is reconciled to NULL ---

// TestSweepOrphans_DanglingID_ClearedToNull verifies the boot-sweep reconciles a
// reference row whose non-NULL workflow_id no longer resolves in the registry
// (the identity was deleted) back to NULL — the accepted "dangling label"
// transient state — while leaving a still-valid id untouched.
func TestSweepOrphans_DanglingID_ClearedToNull(t *testing.T) {
	reg := newFakeRegistry()
	ctx := ctxWith(uuid.New(), true)

	// A live identity that still resolves.
	liveID := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: liveID, Name: "live", Scope: workflow.ScopeShared,
	}))
	// A dangling id with no registry row.
	danglingID := uuid.New()

	bf := &sweepBackfiller{referenced: []uuid.UUID{liveID, danglingID}}

	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(bf)

	require.NoError(t, svc.SweepOrphans(ctx))

	assert.Equal(t, []uuid.UUID{danglingID}, bf.cleared,
		"only the dangling id must be reconciled to NULL; the live id is left intact")
}

// --- Idempotent: no dangling ids → nothing cleared ---

func TestSweepOrphans_NoDangling_NoOp(t *testing.T) {
	reg := newFakeRegistry()
	ctx := ctxWith(uuid.New(), true)
	liveID := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: liveID, Name: "live", Scope: workflow.ScopeShared,
	}))

	bf := &sweepBackfiller{referenced: []uuid.UUID{liveID}}
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(bf)

	require.NoError(t, svc.SweepOrphans(ctx))
	assert.Empty(t, bf.cleared, "no dangling ids means nothing is reconciled")
}

// --- No-op when unwired (local/unconfigured mode) ---

func TestSweepOrphans_NoRegistryOrBackfiller_NoOp(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil) // no registry, no backfiller
	require.NoError(t, svc.SweepOrphans(ctxWith(uuid.New(), true)),
		"sweep is a no-op without a registry + reference backfiller (local mode)")
}

// --- Error: a clear failure propagates (the sweep surfaces it) ---

func TestSweepOrphans_ClearError_Propagates(t *testing.T) {
	reg := newFakeRegistry()
	ctx := ctxWith(uuid.New(), true)
	danglingID := uuid.New()

	clearErr := errors.New("update failed")
	bf := &sweepBackfiller{referenced: []uuid.UUID{danglingID}, clearErr: clearErr}

	svc := workflow.NewWorkflowService(newFakeRepo(), nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(bf)

	err := svc.SweepOrphans(ctx)
	require.ErrorIs(t, err, clearErr, "a ClearReferenceID failure must propagate from the sweep")
}
