package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A delivered decision does not settle containment while a dead parent still
// owns active group work. Completed parents keep legitimate independent work.
func TestGroupRunRepo_ListUnsettledIncludesDeadParentsActiveTasks(t *testing.T) {
	for _, parentState := range []instance.State{instance.StateRunning, instance.StateCompleted, instance.StateFailed, instance.StateCancelled} {
		for _, childState := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused, instance.StateCompleted, instance.StateFailed, instance.StateCancelled, "purged"} {
			t.Run(string(parentState)+"/"+string(childState), func(t *testing.T) {
				db := setupTestDB(t).DB
				groups, instances, run := terminalRun(t, db, "winner", "independent", "also-independent")
				ctx := context.Background()
				require.NoError(t, instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))
				loaded := terminalRoster(t, groups, run.ID)
				d, ready := engine.DecideGroup(loaded, time.Now())
				require.True(t, ready)
				won, err := groups.Decide(ctx, run.ID, *d)
				require.NoError(t, err)
				require.True(t, won)
				won, err = groups.SetFinalOutput(ctx, run.ID, "success", map[string]any{"winner": "winner"}, instance.GroupRunDecided)
				require.NoError(t, err)
				require.True(t, won)
				require.NoError(t, groups.MarkActionsApplied(ctx, run.ID))
				require.NoError(t, groups.MarkContinuationDelivered(ctx, run.ID))
				require.NoError(t, instances.UpdateState(ctx, run.ParentInstanceID, parentState))
				for _, task := range run.Tasks[1:] {
					if childState == "purged" {
						_, err = db.Exec(`DELETE FROM instances WHERE id = ?`, task.ChildInstanceID.String())
						require.NoError(t, err)
					} else {
						require.NoError(t, instances.UpdateState(ctx, task.ChildInstanceID, childState))
					}
				}
				ids, err := groups.ListUnsettled(ctx)
				require.NoError(t, err)
				deadParent := parentState == instance.StateFailed || parentState == instance.StateCancelled
				activeChild := childState == instance.StateQueued || childState == instance.StateRunning || childState == instance.StateWaiting || childState == instance.StatePaused
				if deadParent && activeChild {
					assert.Equal(t, []uuid.UUID{run.ID}, ids, "select once even with two active tasks")
				} else {
					assert.Empty(t, ids, "neither legitimate independent work nor absent/terminal tasks require cleanup")
				}
			})
		}
	}
}
