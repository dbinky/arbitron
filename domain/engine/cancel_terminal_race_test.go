package engine_test

import (
	"context"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Schedule a real terminal write after the engine's authorized read, before
// its cancellation write. The SQLite terminal trigger and receipts stay real.
type cancelWriteRaceRepo struct {
	instance.InstanceRepository
	beforeWrite func(uuid.UUID)
	writeErrors map[uuid.UUID]error
}

func (r *cancelWriteRaceRepo) UpdateState(ctx context.Context, id uuid.UUID, state instance.State) error {
	if r.beforeWrite != nil {
		r.beforeWrite(id)
	}
	if err := r.writeErrors[id]; err != nil {
		return err
	}
	return r.InstanceRepository.UpdateState(ctx, id, state)
}

func TestCancelTerminalRace_StopsDescendantsWithoutChangingTheWinningReceipt(t *testing.T) {
	for _, initial := range []instance.State{instance.StateRunning, instance.StateWaiting, instance.StatePaused, instance.StateQueued} {
		for _, terminal := range []instance.State{instance.StateCompleted, instance.StateFailed} {
			for _, direct := range []bool{false, true} {
				position := "intermediate"
				if direct {
					position = "root"
				}
				t.Run(string(initial)+"/"+string(terminal)+"/"+position, func(t *testing.T) {
					ctx := context.Background()
					db, err := adaptsqlite.Open(":memory:")
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, db.Close()) })
					require.NoError(t, adaptsqlite.RunMigrations(db))
					instances := adaptsqlite.NewInstanceRepo(db)
					groups := adaptsqlite.NewGroupRunRepo(db)
					owner, err := adaptsqlite.NewUserRepo(db).GetDefaultUser(ctx)
					require.NoError(t, err)
					ctx = identity.WithIdentity(ctx, identity.Identity{UserID: owner, Role: identity.RoleUser})
					parent := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "parent", State: instance.StateCompleted, Priority: instance.PriorityNormal}
					require.NoError(t, instances.Create(ctx, parent))
					runID, taskID, node, key := uuid.New(), uuid.New(), "g", "branch"
					run := &instance.GroupRun{
						ID: runID, ParentInstanceID: parent.ID, NodeID: node,
						Form: "branches", CompletionRule: "all_succeed", UnfinishedBehavior: "cancel_unfinished", GatherMode: "results",
						Tasks: []instance.GroupTask{{Key: key, Kind: instance.GroupTaskKindBranch, ChildInstanceID: taskID}},
					}
					require.NoError(t, groups.CreateWithTasks(ctx, run))
					task := &instance.Instance{
						ID: taskID, OwnerID: owner, WorkflowName: "parent", State: initial, Priority: instance.PriorityNormal,
						ParentInstanceID: &parent.ID, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
						GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
						Variables: map[string]any{"answer": "original"},
					}
					require.NoError(t, instances.Create(ctx, task))
					leaf := &instance.Instance{
						ID: uuid.New(), OwnerID: owner, WorkflowName: "leaf", State: instance.StateQueued, Priority: instance.PriorityNormal,
						ParentInstanceID: &taskID, ParentNodeID: &node, SpawnMode: instance.SpawnModeFireAndForget,
					}
					require.NoError(t, instances.Create(ctx, leaf))
					var winning instance.GroupTask
					repo := &cancelWriteRaceRepo{InstanceRepository: instances}
					repo.beforeWrite = func(id uuid.UUID) {
						if id != taskID {
							return
						}
						// Only one racing completion, through the real repository.
						repo.beforeWrite = nil
						require.NoError(t, instances.UpdateState(ctx, taskID, terminal))
						fresh, err := groups.Get(ctx, runID)
						require.NoError(t, err)
						winning = fresh.Tasks[0]
					}
					bus := newMockEventBus()
					eng := engine.NewEngine(nil, repo, nil, nil, nil, nil, bus, newMockQueueManager(), nil, "")
					eng.SetSpawnLinks(instances)
					target := parent.ID
					if direct {
						target = taskID
					}
					cancelErr := eng.Cancel(ctx, target)
					gotLeaf, err := instances.Get(ctx, leaf.ID)
					require.NoError(t, err)
					assert.Equal(t, instance.StateCancelled, gotLeaf.State, "a completion racing cancellation must not strand the leaf until the sweep")
					require.NoError(t, cancelErr)
					gotTask, err := instances.Get(ctx, taskID)
					require.NoError(t, err)
					assert.Equal(t, terminal, gotTask.State)
					fresh, err := groups.Get(ctx, runID)
					require.NoError(t, err)
					require.NotNil(t, winning.TerminalSeq)
					assert.Equal(t, winning, fresh.Tasks[0], "the first terminal receipt remains immutable")
					assert.Empty(t, bus.eventsOfType(taskID, engine.EventTypeInstanceCancelled), "no false cancellation event for a lost write")
					assert.Len(t, bus.eventsOfType(leaf.ID, engine.EventTypeInstanceCancelled), 1)
				})
			}
		}
	}
}

func TestCancelTerminalRace_WriteFailureStillHaltsActiveOrUnauthorizedBranches(t *testing.T) {
	for _, loseAuthorization := range []bool{false, true} {
		name := "active storage failure"
		if loseAuthorization {
			name = "terminal row no longer authorized"
		}
		t.Run(name, func(t *testing.T) {
			c := newCascadeFixture(t)
			root := c.runInDepartment(t, instance.StateCompleted, "eng")
			mid := c.runInDepartment(t, instance.StateRunning, "eng")
			leaf := c.runInDepartment(t, instance.StateQueued, "eng")
			c.links.link(root, mid)
			c.links.link(mid, leaf)
			repo := &cancelWriteRaceRepo{InstanceRepository: c.f.instanceRepo, writeErrors: map[uuid.UUID]error{mid: assert.AnError}}
			if loseAuthorization {
				repo.beforeWrite = func(id uuid.UUID) {
					if id == mid {
						c.f.instanceRepo.mu.Lock()
						c.f.instanceRepo.instances[mid].State = instance.StateCompleted
						c.f.instanceRepo.instances[mid].Department = "finance"
						c.f.instanceRepo.mu.Unlock()
					}
				}
			}
			eng := engine.NewEngine(nil, repo, nil, nil, nil, nil, c.f.eventBus, c.f.queueManager, nil, "")
			eng.SetSpawnLinks(c.links)
			ctx := identity.WithIdentity(context.Background(), identity.Identity{
				UserID: uuid.New(), Role: identity.RoleUser, Departments: []string{"eng", "finance"}, LedDepartments: []string{"eng"},
			})
			assert.ErrorIs(t, eng.Cancel(ctx, root), instance.ErrCannotCancel)
			assert.Equal(t, instance.StateQueued, c.stateOf(t, leaf), "a generic failure must not become permission to cross this branch")
			assert.Empty(t, c.f.eventBus.eventsOfType(mid, engine.EventTypeInstanceCancelled))
			assert.Empty(t, c.f.eventBus.eventsOfType(leaf, engine.EventTypeInstanceCancelled))
		})
	}
}
