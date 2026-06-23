package engine_test

import (
	"context"
	"errors"
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

// --- Edge: a non-archived workflow with a wired resolver never blocks ---

// TestTrigger_NotArchivedResolver_NeverBlocks verifies a wired resolver that
// reports the workflow as not-archived triggers normally. IsArchived is part of
// the IDResolver contract, so the check always runs when a resolver is wired —
// it simply permits a not-archived workflow.
func TestTrigger_NotArchivedResolver_NeverBlocks(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	res := newFakeResolverByName() // IsArchived reports not-archived
	res.register("alpha", uuid.New())
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	_, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.NoError(t, err)
}

// --- Error: IsArchived error fails CLOSED (refuses the trigger) ---

// TestTrigger_IsArchivedError_FailsClosed verifies that when IsArchived returns
// an error the engine REFUSES the trigger and creates no instance. The archived
// flag is a deliberate "do not run this" invariant, so an inability to confirm
// the archived state must not silently run a possibly-archived workflow: the
// trigger fails closed and the error is surfaced.
func TestTrigger_IsArchivedError_FailsClosed(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	id := uuid.New()

	// An erroring resolver: IsArchived returns a non-nil error.
	res := &errArchiveResolver{fakeResolverByName: newFakeResolverByName()}
	res.register("alpha", id)
	h.engine.SetWorkflowResolver(res)
	h.repo.put(workflow.Private(caller), sharedWf("alpha"))

	_, _, err := h.engine.Trigger(ctxAs(caller), "alpha", nil, "normal")
	require.Error(t, err,
		"IsArchived error must fail closed — the trigger is refused (do-not-run invariant)")
	assert.Empty(t, h.instances.instances, "no instance created when the archive check errors")
}

// errArchiveResolver always returns an error from IsArchived, simulating a
// transient registry unavailability on the archive-check seam.
type errArchiveResolver struct {
	*fakeResolverByName
}

func (r *errArchiveResolver) IsArchived(_ context.Context, _ string) (bool, error) {
	return false, errors.New("registry unavailable")
}
