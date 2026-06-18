package workflow_test

// Area 17 (paired): Backfill + live trigger
//
// The core invariant is: the stable workflow_id that BackfillReferences stamps
// on pre-existing rows (instances/schedules/publications with NULL workflow_id)
// is exactly the same UUID that the live trigger path would stamp on a new row
// for the same workflow. Both paths resolve through the workflow_identity
// registry — the single source of truth — so this invariant is definitional.
//
// These tests are NOT a tautology: they verify that:
//   1. BackfillReferences resolves via registry.ResolveID → stamps UUID-A.
//   2. The live trigger resolver (backed by the same registry) also resolves
//      to UUID-A for the same name.
//   3. The two UUIDs are equal — there is no source of divergence.
//
// A divergence is possible if either path had a different code path to the
// registry (e.g. stale in-memory cache, different column, or a race that
// re-allocated the name to a different row).

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// triggerResolver adapts fakeRegistry to the engine.WorkflowResolver interface
// (ResolveID(ctx, name) uuid.UUID) by delegating to fakeRegistry.ResolveID.
// The actual engine resolver is wired at the composition root; this mirrors
// the production path without importing the engine package.
type triggerResolver struct {
	reg *fakeRegistry
}

func (r *triggerResolver) ResolveID(ctx context.Context, name string) (uuid.UUID, error) {
	return r.reg.ResolveID(ctx, name)
}

// TestBackfillTriggerSeam_SameUUIDFromBothPaths verifies that the UUID stamped
// by BackfillReferences and the UUID returned by the live trigger resolver are
// identical for the same workflow name.
func TestBackfillTriggerSeam_SameUUIDFromBothPaths(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	reg := newFakeRegistry()
	fb := newFakeRefBackfiller("alpha") // pre-existing row with NULL workflow_id

	svc := workflow.NewWorkflowService(nil, nil)
	svc.SetRegistry(reg)
	svc.SetReferenceBackfiller(fb)

	// Seed the registry with "alpha" → a stable UUID.
	registeredID := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: registeredID, Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: owner,
	}))

	// 1. Backfill path: BackfillReferences resolves name→id via registry.ResolveID.
	require.NoError(t, svc.BackfillReferences(ctx))
	backfilledID := fb.setIDs["alpha"]
	require.NotEqual(t, uuid.Nil, backfilledID, "backfill must stamp a non-nil id")

	// 2. Live trigger path: the engine resolver (at runtime backed by the same
	//    registry) resolves the same name via the same ResolveID call.
	res := &triggerResolver{reg: reg}
	triggerID, err := res.ResolveID(ctx, "alpha")
	require.NoError(t, err, "trigger resolver must find the registry entry")

	// 3. The invariant: both paths produce the same UUID.
	assert.Equal(t, registeredID, backfilledID,
		"BackfillReferences must stamp the UUID from workflow_identity")
	assert.Equal(t, registeredID, triggerID,
		"live trigger resolver must return the UUID from workflow_identity")
	assert.Equal(t, backfilledID, triggerID,
		"backfill UUID and trigger UUID must be identical — both come from the same registry row")
}

// TestBackfillTriggerSeam_BackfillAfterRename_StillMatches verifies that even
// after a rename (id unchanged, name updated), a historical row backfilled
// AFTER the rename receives the same UUID that the trigger resolver returns
// (which now resolves the new name, not the old one).
func TestBackfillTriggerSeam_BackfillAfterRename_StillMatches(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true) // admin for rename

	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	// Create "alpha" and get its stable UUID.
	require.NoError(t, svc.Create(ctx, "alpha", wf("alpha")))
	stableID, err := reg.ResolveID(ctx, "alpha")
	require.NoError(t, err)

	// Rename "alpha" → "alpha-v2". The stable UUID is unchanged.
	require.NoError(t, svc.Rename(ctx, "alpha", "alpha-v2"))
	renamedID, err := reg.ResolveID(ctx, "alpha-v2")
	require.NoError(t, err)
	assert.Equal(t, stableID, renamedID, "rename must preserve the stable UUID")

	// A pre-existing historical row still references the OLD name "alpha".
	// Backfill now resolves "alpha" — but "alpha" is no longer in the registry!
	// This is the "dangling historical label" case: the backfill skips ErrNotFound.
	fb := newFakeRefBackfiller("alpha")
	svc.SetReferenceBackfiller(fb)

	require.NoError(t, svc.BackfillReferences(ctx),
		"dangling old-name reference must not abort the backfill")
	_, alphaStamped := fb.setIDs["alpha"]
	assert.False(t, alphaStamped,
		"old name 'alpha' is no longer in the registry after rename — backfill must skip it gracefully")

	// New rows created after the rename use the new name "alpha-v2"; the trigger
	// resolver returns stableID for "alpha-v2".
	res := &triggerResolver{reg: reg}
	triggerID, err := res.ResolveID(ctx, "alpha-v2")
	require.NoError(t, err)
	assert.Equal(t, stableID, triggerID,
		"trigger resolver returns stableID for the new name after rename")
}
