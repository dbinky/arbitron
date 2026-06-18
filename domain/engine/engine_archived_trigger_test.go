package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveResolver is an engine.WorkflowResolver that ALSO reports the archived
// flag for a name, satisfying the engine's optional archive-check seam. A name
// is archived only if explicitly marked. Unknown names resolve to ErrNotFound.
type archiveResolver struct {
	*fakeResolverByName
	archived map[string]bool
}

func newArchiveResolver() *archiveResolver {
	return &archiveResolver{
		fakeResolverByName: newFakeResolverByName(),
		archived:           map[string]bool{},
	}
}

func (r *archiveResolver) markArchived(name string) { r.archived[name] = true }

func (r *archiveResolver) IsArchived(_ context.Context, name string) (bool, error) {
	return r.archived[name], nil
}

// --- Error: triggering an archived workflow is refused ---

// TestTrigger_Archived_Refused verifies that triggering an archived workflow
// returns workflow.ErrArchived and creates NO instance.
func TestTrigger_Archived_Refused(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	id := uuid.New()
	res := newArchiveResolver()
	res.register("alpha", id)
	res.markArchived("alpha")
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	_, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.ErrorIs(t, err, workflow.ErrArchived)

	assert.Empty(t, h.instances.instances, "no instance created for an archived trigger")
}

// --- Happy path: a non-archived workflow still triggers ---

func TestTrigger_NotArchived_Succeeds(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	id := uuid.New()
	res := newArchiveResolver()
	res.register("alpha", id)
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	_, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.NoError(t, err)
}

// --- Edge: a resolver with no archive-check seam never blocks (local mode) ---

// TestTrigger_ResolverWithoutArchiveCheck_NeverBlocks verifies a plain resolver
// that does not implement the archive-check seam triggers normally (best-effort:
// the engine cannot know the archived state, so it does not block).
func TestTrigger_ResolverWithoutArchiveCheck_NeverBlocks(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	res := newFakeResolverByName() // no IsArchived method
	res.register("alpha", uuid.New())
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	_, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.NoError(t, err)
}
