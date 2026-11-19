package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedSpawnFixture is a spawner fixture whose queue keeps every new run
// QUEUED (so a spawned child cannot race to completion before the test looks
// at it) and whose link store is the in-memory cascade fake.
func pausedSpawnFixture(t *testing.T) (*testFixtures, *fakeSpawnLinks, context.Context) {
	t.Helper()
	f := spawnerFixture(t)
	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
	f.queueManager.mu.Unlock()
	links := newFakeSpawnLinks(func(id uuid.UUID) instance.State {
		inst, err := f.instanceRepo.Get(context.Background(), id)
		if err != nil {
			return instance.StateFailed
		}
		return inst.State
	})
	links.setState = func(id uuid.UUID, st instance.State) {
		_ = f.instanceRepo.UpdateState(context.Background(), id, st)
	}
	f.engine.SetSpawnLinks(links)
	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Role: identity.RoleAdmin, Departments: []string{"default"},
	})
	return f, links, ctx
}

// pausedParentRow creates a paused parent waiting on an unexpired timer, so
// resuming it returns it to waiting without a graph walk.
func pausedParentRow(t *testing.T, f *testFixtures) uuid.UUID {
	t.Helper()
	future := time.Now().Add(time.Hour)
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StatePaused, Priority: instance.PriorityNormal, WaitingUntil: &future,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func spawnChildOf(t *testing.T, f *testFixtures, ctx context.Context, parent uuid.UUID) uuid.UUID {
	t.Helper()
	childID, err := f.engine.Spawn(ctx, engine.SpawnRequest{
		ChildWorkflow: "child",
		Variables:     map[string]any{"repo": "arbitron/arbitron"},
		Priority:      string(instance.PriorityNormal),
		Link: instance.SpawnLink{
			ParentInstanceID: parent, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild,
		},
	})
	require.NoError(t, err)
	return childID
}

func TestSpawn_ParentPaused_ChildStartsCascadePaused(t *testing.T) {
	f, links, ctx := pausedSpawnFixture(t)
	parent := pausedParentRow(t, f)

	child := spawnChildOf(t, f, ctx, parent)

	assert.Equal(t, instance.StatePaused, f.instanceRepo.get(child).State)
	by, err := links.CascadePausedBy(context.Background(), child)
	require.NoError(t, err)
	require.NotNil(t, by)
	assert.Equal(t, parent, *by)
}

func TestSpawn_ParentRunning_ChildNotPaused(t *testing.T) {
	f, links, ctx := pausedSpawnFixture(t)
	parent := spawnParent(t, f, 0)

	child := spawnChildOf(t, f, ctx, parent)

	assert.Equal(t, instance.StateQueued, f.instanceRepo.get(child).State)
	by, err := links.CascadePausedBy(context.Background(), child)
	require.NoError(t, err)
	assert.Nil(t, by)
}

func TestSpawn_ParentCascadePaused_ChildMarkedWithRootPauser(t *testing.T) {
	f, links, ctx := pausedSpawnFixture(t)
	grand := pausedParentRow(t, f)
	parent := pausedParentRow(t, f)
	links.link(grand, parent)
	links.pauseMarks = map[uuid.UUID]pauseMark{parent: {by: grand, from: instance.StateWaiting}}

	child := spawnChildOf(t, f, ctx, parent)
	links.link(parent, child)

	by, err := links.CascadePausedBy(context.Background(), child)
	require.NoError(t, err)
	require.NotNil(t, by)
	assert.Equal(t, grand, *by, "the child belongs to the pause that is holding its parent")

	require.NoError(t, f.engine.Resume(ctx, grand))

	assert.Equal(t, instance.StateWaiting, f.instanceRepo.get(parent).State)
	assert.Equal(t, instance.StateQueued, f.instanceRepo.get(child).State)
}
