package engine_test

import (
	"context"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With timestamp comparison restored, recovery cancels both children. With
// the real creation/intent producer absent, it cannot cancel the covered child.
func TestOrphanReconciler_SameSecondIntentReachesOnlyCoveredDescendants(t *testing.T) {
	for _, mode := range []instance.SpawnMode{instance.SpawnModeFireAndForget, instance.SpawnModeWaitForChild, instance.SpawnModeGroupTask} {
		for _, terminal := range []instance.State{instance.StateCompleted, instance.StateFailed} {
			t.Run(string(mode)+"/"+string(terminal), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "cancel.db")
				db, err := adaptsqlite.Open(path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				require.NoError(t, adaptsqlite.RunMigrations(db))
				repo := adaptsqlite.NewInstanceRepo(db)
				groups := adaptsqlite.NewGroupRunRepo(db)
				owner, err := adaptsqlite.NewUserRepo(db).GetDefaultUser(ctx)
				require.NoError(t, err)
				root := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: "parent", State: terminal, Priority: instance.PriorityNormal}
				require.NoError(t, repo.Create(ctx, root))
				root, err = repo.Get(ctx, root.ID)
				require.NoError(t, err)
				node := "group"
				mid := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: "mid", State: instance.StateCompleted,
					Priority: instance.PriorityNormal, ParentInstanceID: &root.ID, ParentNodeID: &node, SpawnMode: instance.SpawnModeFireAndForget}
				require.NoError(t, repo.Create(ctx, mid))
				before, after := uuid.New(), uuid.New()
				run := &instance.GroupRun{ID: uuid.New(), ParentInstanceID: mid.ID, NodeID: node,
					Form: "branches", CompletionRule: "all_succeed", UnfinishedBehavior: "cancel_unfinished", GatherMode: "results",
					Tasks: []instance.GroupTask{
						{Key: "before", Kind: instance.GroupTaskKindBranch, ChildInstanceID: before},
						{Position: 1, Key: "after", Kind: instance.GroupTaskKindBranch, ChildInstanceID: after},
					}}
				if mode == instance.SpawnModeGroupTask {
					require.NoError(t, groups.CreateWithTasks(ctx, run))
				}
				create := func(id uuid.UUID, key string) {
					child := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: id, OwnerID: owner, WorkflowName: "child", State: instance.StateWaiting,
						Priority: instance.PriorityNormal, ParentInstanceID: &mid.ID, ParentNodeID: &node, SpawnMode: mode}
					if mode == instance.SpawnModeGroupTask {
						child.GroupRunID, child.GroupTaskKey, child.GroupTaskKind = &run.ID, &key, instance.GroupTaskKindBranch
					}
					require.NoError(t, repo.Create(ctx, child))
				}
				create(before, "before")
				require.NoError(t, repo.MarkSubtreeCancelRequested(ctx, root.ID)) // interrupted before its walk
				create(after, "after")
				_, err = db.Exec(`UPDATE instances SET created_at = (SELECT subtree_cancel_requested_at FROM instances WHERE id = ?) WHERE id IN (?, ?)`,
					root.ID.String(), before.String(), after.String())
				require.NoError(t, err)
				// Recover through new repositories after reopening the durable store.
				require.NoError(t, db.Close())
				db, err = adaptsqlite.Open(path)
				require.NoError(t, err)
				require.NoError(t, adaptsqlite.RunMigrations(db))
				repo = adaptsqlite.NewInstanceRepo(db)
				groups = adaptsqlite.NewGroupRunRepo(db)
				bus := newMockEventBus()
				eng := wireModelPins(engine.NewEngine(nil, repo, nil, nil, nil, nil, bus, newMockQueueManager(), nil, ""))
				eng.SetSpawnLinks(repo)
				reconciler := engine.NewOrphanedChildReconciler(repo, repo, fixedIdentityResolver{}, eng)
				for range 2 { // replay must retain the same boundary and terminal receipt
					require.NoError(t, reconciler.Reconcile(ctx))
				}
				covered, err := repo.Get(ctx, before)
				require.NoError(t, err)
				assert.Equal(t, instance.StateCancelled, covered.State)
				later, err := repo.Get(ctx, after)
				require.NoError(t, err)
				assert.Equal(t, instance.StateWaiting, later.State)
				unchanged, err := repo.Get(ctx, root.ID)
				require.NoError(t, err)
				assert.Equal(t, root, unchanged, "recovery must preserve the terminal ancestor's outcome")
				assert.Len(t, bus.eventsOfType(before, engine.EventTypeInstanceCancelled), 1)
				assert.Empty(t, bus.eventsOfType(after, engine.EventTypeInstanceCancelled))
				if mode == instance.SpawnModeGroupTask {
					got, err := groups.Get(ctx, run.ID)
					require.NoError(t, err)
					assert.Equal(t, instance.GroupTaskStatusCancelled, got.Tasks[0].LiveStatus)
					assert.NotNil(t, got.Tasks[0].TerminalSeq)
					assert.Nil(t, got.Tasks[1].TerminalSeq)
				}
			})
		}
	}
}
