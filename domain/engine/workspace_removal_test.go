// domain/engine/workspace_removal_test.go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingRemovalHook struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (h *recordingRemovalHook) WorkspaceRemoved(ctx context.Context, id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() == nil {
		h.ids = append(h.ids, id)
	}
}

func (h *recordingRemovalHook) removed() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.ids...)
}

type failingRemove struct {
	*fakeWorkspaceManager
	err error
}

func (f failingRemove) RemoveInstanceDir(uuid.UUID) error { return f.err }

func TestEngine_RemovalHookFiresOnCompletionCleanup(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	hook := &recordingRemovalHook{}
	f.engine.SetWorkspaceManager(fwm)
	f.engine.SetWorkspaceRemovalHook(hook)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	require.Eventually(t, func() bool { return len(hook.removed()) == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []uuid.UUID{id}, hook.removed())
	assert.Equal(t, []uuid.UUID{id}, fwm.removedIDs())
}

func TestWithWorkspaceRemovalHook_FiresOnlyAfterARemoval(t *testing.T) {
	hook := &recordingRemovalHook{}
	fwm := &fakeWorkspaceManager{}
	wm := engine.WithWorkspaceRemovalHook(fwm, hook)
	id := uuid.New()
	require.NoError(t, wm.RemoveInstanceDir(id))
	assert.Equal(t, []uuid.UUID{id}, fwm.removedIDs(), "the inner manager still removes the directory")
	assert.Equal(t, []uuid.UUID{id}, hook.removed())

	boom := errors.New("busy")
	bad := engine.WithWorkspaceRemovalHook(failingRemove{fakeWorkspaceManager: &fakeWorkspaceManager{}, err: boom}, hook)
	require.ErrorIs(t, bad.RemoveInstanceDir(uuid.New()), boom)
	assert.Len(t, hook.removed(), 1, "a failed removal notifies nobody")

	dir, err := wm.EnsureInstanceDir(id)
	require.NoError(t, err)
	assert.Contains(t, dir, id.String(), "other methods pass through")
}
