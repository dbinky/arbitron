package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These fixtures persist the roster BEFORE creating a child, just as the real
// launcher does. Producer tests never seed receipts via RecordTaskTerminal.
func terminalRun(t *testing.T, db *sql.DB, keys ...string) (*GroupRunRepo, *InstanceRepo, *instance.GroupRun) {
	t.Helper()
	ctx := context.Background()
	groups, instances := NewGroupRunRepo(db), NewInstanceRepo(db)
	parent := &instance.Instance{ID: uuid.New(), OwnerID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), WorkflowName: "terminal-parent", State: instance.StateWaiting, Priority: instance.PriorityNormal}
	require.NoError(t, instances.Create(ctx, parent))
	run := sampleGroupRun(parent.ID, "g", keys...)
	run.CompletionRule, run.UnfinishedBehavior = "first_success", "finish_independently"
	require.NoError(t, groups.CreateWithTasks(ctx, run))
	for _, task := range run.Tasks {
		node, key, rid, pid := "g", task.Key, run.ID, parent.ID
		child := &instance.Instance{
			ID: task.ChildInstanceID, OwnerID: parent.OwnerID, WorkflowName: "terminal-child",
			State: instance.StateQueued, Priority: instance.PriorityNormal,
			ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
			GroupRunID: &rid, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
			Variables: map[string]any{"workspace": "/private/child", "value": key, "nested": map[string]any{"workspace": "keep", "n": float64(42)}},
		}
		require.NoError(t, instances.Create(ctx, child))
		require.NoError(t, groups.MarkTaskLaunched(ctx, run.ID, key))
	}
	return groups, instances, run
}

func terminalRoster(t *testing.T, groups *GroupRunRepo, id uuid.UUID) *instance.GroupRun {
	t.Helper()
	run, err := groups.Get(context.Background(), id)
	require.NoError(t, err)
	return run
}

func TestGroupTerminal_ProductionMethodsCaptureResults(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a", "b")
	ctx := context.Background()
	for _, i := range []int{1, 0} {
		require.NoError(t, instances.SetCompleted(ctx, run.Tasks[i].ChildInstanceID))
	}
	got := terminalRoster(t, groups, run.ID)
	for _, task := range got.Tasks {
		require.NotNil(t, task.TerminalSeq, "terminal persistence must capture the receipt without any callback")
		require.NotNil(t, task.TerminalAt)
		assert.Equal(t, instance.GroupTaskStatusCompleted, task.LiveStatus)
		assert.Equal(t, map[string]any{"value": task.Key, "nested": map[string]any{"workspace": "keep", "n": float64(42)}}, task.Result)
		for name := range variable.ReservedInputNames {
			assert.NotContains(t, task.Result, name, "extend SQL removal when reserved inputs grow")
		}
		assert.Nil(t, task.Failure)
	}
	require.Less(t, *got.Tasks[1].TerminalSeq, *got.Tasks[0].TerminalSeq)

	// Delayed/reversed callbacks cannot substitute their payload or order.
	for _, i := range []int{0, 1} {
		seq, first, err := groups.RecordTaskTerminal(ctx, run.ID, run.Tasks[i].Key, instance.GroupTaskStatusFailed, map[string]any{"wrong": true}, nil, time.Now())
		require.NoError(t, err)
		assert.False(t, first)
		assert.Equal(t, *got.Tasks[i].TerminalSeq, seq)
	}
	assert.Equal(t, got.Tasks, terminalRoster(t, groups, run.ID).Tasks)
	decision, ready := engine.DecideGroup(got, time.Now())
	require.True(t, ready)
	assert.Equal(t, "b", decision.WinnerKey)
}

// The receipt trigger hardcodes json_remove(variables,'$.workspace'). The
// fixture above only ever carries "workspace", so a loop over the Go set
// asserts nothing about names that were never written. Seed EVERY reserved
// name so growing variable.ReservedInputNames without extending the trigger
// fails here.
func TestGroupTerminal_ReceiptStripsEveryReservedInputName(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a")
	ctx := context.Background()
	vars := map[string]any{"value": "a", "nested": map[string]any{"workspace": "keep"}}
	for name := range variable.ReservedInputNames {
		vars[name] = "/private/" + name
	}
	require.NoError(t, instances.UpdateVariables(ctx, run.Tasks[0].ChildInstanceID, vars))
	require.NoError(t, instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))

	got := terminalRoster(t, groups, run.ID).Tasks[0]
	require.NotNil(t, got.TerminalSeq)
	for name := range variable.ReservedInputNames {
		assert.NotContains(t, got.Result, name,
			"receipt result leaks reserved input %q: extend the 019 group_task_terminal_* json_remove", name)
	}
	assert.Equal(t, "a", got.Result["value"])
	assert.Equal(t, map[string]any{"workspace": "keep"}, got.Result["nested"], "only top-level reserved names are stripped")
}

func TestGroupTerminal_CommitOrderNotIdenticalTimestampOrAuthorOrder(t *testing.T) {
	db := setupTestDB(t).DB
	groups, _, run := terminalRun(t, db, "a", "b")
	// The same instances table is also updated by raw queue SQL. Fixed display
	// timestamps remove the wall clock entirely from this ordering proof.
	stamp := "2026-10-16T10:00:00Z"
	for _, i := range []int{1, 0} {
		_, err := db.Exec(`UPDATE instances SET state='completed', completed_at=?, updated_at=? WHERE id=?`, stamp, stamp, run.Tasks[i].ChildInstanceID.String())
		require.NoError(t, err)
	}
	got := terminalRoster(t, groups, run.ID)
	require.NotNil(t, got.Tasks[0].TerminalSeq)
	require.NotNil(t, got.Tasks[1].TerminalSeq)
	assert.Equal(t, got.Tasks[0].TerminalAt, got.Tasks[1].TerminalAt)
	assert.Less(t, *got.Tasks[1].TerminalSeq, *got.Tasks[0].TerminalSeq)
	decision, ready := engine.DecideGroup(got, time.Now())
	require.True(t, ready)
	assert.Equal(t, "b", decision.WinnerKey)
}

func TestGroupTerminal_FailureTraceAndCancellation(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "failed", "cancelled", "generic")
	ctx := context.Background()
	traces := NewNodeExecutionRepo(db)
	child := run.Tasks[0].ChildInstanceID
	for _, row := range []struct {
		node    string
		attempt int
		message string
		offset  int
	}{
		{"bad", 1, "old", 1}, {"other", 99, "wrong node", 99},
		{"bad", 3, "third attempt", 2}, {"bad", 2, "later but lower attempt", 20},
		{"bad", 3, "latest matching attempt", 3},
	} {
		require.NoError(t, traces.Create(ctx, &instance.NodeExecution{ID: uuid.New(), InstanceID: child, NodeID: row.node, Attempt: row.attempt, Status: instance.NodeStatusFailed, Error: map[string]any{"error": row.message}, StartedAt: time.Unix(int64(row.offset), 0)}))
	}
	require.NoError(t, instances.SetFailed(ctx, child, "bad"))
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[1].ChildInstanceID, instance.StateCancelled))
	require.NoError(t, instances.SetFailed(ctx, run.Tasks[2].ChildInstanceID, "missing-trace"))
	got := terminalRoster(t, groups, run.ID)
	require.NotNil(t, got.Tasks[0].TerminalSeq)
	assert.Equal(t, map[string]any{"state": "failed", "failedNodeId": "bad", "error": "latest matching attempt", "attempt": float64(3)}, got.Tasks[0].Failure)
	for i, state := range []string{"failed", "cancelled", "failed"} {
		task := got.Tasks[i]
		assert.Equal(t, instance.GroupTaskStatus(state), task.LiveStatus)
		require.NotNil(t, task.Failure)
		assert.Equal(t, state, task.Failure["state"])
		assert.Equal(t, task.Key, task.Result["value"], "failure still exposes available final variables")
	}
	assert.Equal(t, float64(0), got.Tasks[1].Failure["attempt"])
	assert.NotEmpty(t, got.Tasks[1].Failure["error"])
	assert.Equal(t, float64(0), got.Tasks[2].Failure["attempt"])
	assert.NotEmpty(t, got.Tasks[2].Failure["error"])
}

func TestGroupTerminal_FailureWithoutNodeSelectsLatestTraceDeterministically(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a")
	ctx := context.Background()
	traces := NewNodeExecutionRepo(db)
	for _, row := range []struct{ id, message string }{
		{"00000000-0000-0000-0000-000000000002", "chosen by id"},
		{"00000000-0000-0000-0000-000000000001", "not insertion order"},
	} {
		require.NoError(t, traces.Create(ctx, &instance.NodeExecution{
			ID: uuid.MustParse(row.id), InstanceID: run.Tasks[0].ChildInstanceID,
			NodeID: "bad", Attempt: 2, Status: instance.NodeStatusFailed,
			Error: map[string]any{"error": row.message}, StartedAt: time.Unix(1, 0),
		}))
	}
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateFailed))
	assert.Equal(t, map[string]any{"state": "failed", "failedNodeId": "bad", "error": "chosen by id", "attempt": float64(2)}, terminalRoster(t, groups, run.ID).Tasks[0].Failure)
}

func TestGroupTerminal_TerminalInsertWithoutRosterRollsBack(t *testing.T) {
	db := setupTestDB(t).DB
	_, instances, run := terminalRun(t, db, "a")
	child, err := instances.Get(context.Background(), run.Tasks[0].ChildInstanceID)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM instances WHERE id=?`, child.ID.String())
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM group_tasks`)
	require.NoError(t, err)
	child.State = instance.StateCompleted
	require.ErrorContains(t, instances.Create(context.Background(), child), "roster")
	var count, clock int64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM instances WHERE id=?`, child.ID.String()).Scan(&count))
	require.NoError(t, db.QueryRow(`SELECT value FROM group_terminal_clock`).Scan(&clock))
	assert.Zero(t, count)
	assert.Zero(t, clock)
}

func TestGroupTerminal_StorageFailureRollsBackStateAndClock(t *testing.T) {
	for name, sabotage := range map[string]string{
		"missing roster":      `DELETE FROM group_tasks`,
		"mismatched link":     `UPDATE group_tasks SET task_key='wrong'`,
		"missing clock":       `DELETE FROM group_terminal_clock`,
		"receipt write abort": `CREATE TRIGGER abort_receipt BEFORE UPDATE OF terminal_seq ON group_tasks BEGIN SELECT RAISE(ABORT, 'receipt storage failed'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t).DB
			groups, instances, run := terminalRun(t, db, "a")
			_, err := db.Exec(sabotage)
			require.NoError(t, err)
			require.Error(t, instances.SetCompleted(context.Background(), run.Tasks[0].ChildInstanceID), "no success may commit without its receipt")
			child, err := instances.Get(context.Background(), run.Tasks[0].ChildInstanceID)
			require.NoError(t, err)
			assert.Equal(t, instance.StateQueued, child.State)
			for _, task := range terminalRoster(t, groups, run.ID).Tasks {
				assert.Nil(t, task.TerminalSeq)
			}
			if name != "missing clock" {
				var clock int64
				require.NoError(t, db.QueryRow(`SELECT value FROM group_terminal_clock`).Scan(&clock))
				assert.Zero(t, clock, "allocator increment shares the state transaction")
			}
		})
	}
}

func TestGroupTerminal_MissingTriggerCannotBeHiddenByCallbackAllocation(t *testing.T) {
	for _, trigger := range []string{"group_task_terminal_update", "group_task_terminal_insert"} {
		t.Run(trigger, func(t *testing.T) {
			db := setupTestDB(t).DB
			groups, instances, run := terminalRun(t, db, "a")
			_, err := db.Exec(`DROP TRIGGER ` + trigger)
			require.NoError(t, err)
			if trigger == "group_task_terminal_insert" {
				child, err := instances.Get(context.Background(), run.Tasks[0].ChildInstanceID)
				require.NoError(t, err)
				_, err = db.Exec(`DELETE FROM instances WHERE id=?`, child.ID.String())
				require.NoError(t, err)
				child.State = instance.StateCompleted
				require.NoError(t, instances.Create(context.Background(), child))
			} else {
				require.NoError(t, instances.SetCompleted(context.Background(), run.Tasks[0].ChildInstanceID))
			}
			_, _, err = groups.RecordTaskTerminal(context.Background(), run.ID, "a", instance.GroupTaskStatusCompleted, map[string]any{"fabricated": true}, nil, time.Now())
			require.ErrorContains(t, err, "receipt")
			assert.Nil(t, terminalRoster(t, groups, run.ID).Tasks[0].TerminalSeq)
		})
	}
}

func TestGroupTerminal_CallbackCannotPreemptProductionTransition(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a")
	_, _, err := groups.RecordTaskTerminal(context.Background(), run.ID, "a", instance.GroupTaskStatusCompleted, nil, nil, time.Now())
	require.ErrorContains(t, err, "receipt")
	assert.Nil(t, terminalRoster(t, groups, run.ID).Tasks[0].TerminalSeq)
	require.NoError(t, instances.SetCompleted(context.Background(), run.Tasks[0].ChildInstanceID))
	require.NotNil(t, terminalRoster(t, groups, run.ID).Tasks[0].TerminalSeq)
}

func TestGroupTerminal_CancelledTaskCannotBecomeWinnerAndReplayIsImmutable(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a", "b")
	ctx := context.Background()
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateCancelled))
	before := terminalRoster(t, groups, run.ID).Tasks[0]
	require.Error(t, instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))
	require.Error(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateRunning))
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateCancelled))
	require.NoError(t, instances.UpdateVariables(ctx, run.Tasks[0].ChildInstanceID, map[string]any{"late": true}))
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateCancelled))
	assert.Equal(t, before, terminalRoster(t, groups, run.ID).Tasks[0])
	child, err := instances.Get(ctx, run.Tasks[0].ChildInstanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCancelled, child.State)
	require.NoError(t, instances.SetCompleted(ctx, run.Tasks[1].ChildInstanceID))
	decision, ready := engine.DecideGroup(terminalRoster(t, groups, run.ID), time.Now())
	require.True(t, ready)
	assert.Equal(t, "b", decision.WinnerKey)
}

func TestGroupTerminal_InsertCapturesTerminalAndLegacyBehaviorIsUnchanged(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a")
	child, err := instances.Get(context.Background(), run.Tasks[0].ChildInstanceID)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM instances WHERE id=?`, child.ID.String())
	require.NoError(t, err)
	child.State = instance.StateCompleted
	require.NoError(t, instances.Create(context.Background(), child))
	got := terminalRoster(t, groups, run.ID).Tasks[0]
	require.NotNil(t, got.TerminalSeq)
	assert.Equal(t, "a", got.Result["value"])
	assert.Equal(t, instance.GroupTaskStatusCompleted, got.LiveStatus)
	// The immutable terminal rule is GROUP-only, not a change to legacy runs.
	require.NoError(t, instances.SetCompleted(context.Background(), run.ParentInstanceID))
	require.NoError(t, instances.UpdateState(context.Background(), run.ParentInstanceID, instance.StateCancelled))
}

func TestGroupTerminal_DurableClockSharedWithFixturesAndNeverReusesDeletedMaximum(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a")
	ctx := context.Background()
	manual := mustCreateRun(t, groups, sampleGroupRun(uuid.New(), "manual", "m"))
	seq, first, err := groups.RecordTaskTerminal(ctx, manual.ID, "m", instance.GroupTaskStatusCompleted, map[string]any{"manual": true}, nil, time.Now())
	require.NoError(t, err)
	require.True(t, first)
	require.NoError(t, instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))
	produced := terminalRoster(t, groups, run.ID).Tasks[0]
	require.NotNil(t, produced.TerminalSeq)
	require.Less(t, seq, *produced.TerminalSeq)
	_, err = db.Exec(`DELETE FROM group_runs WHERE id=?`, run.ID.String())
	require.NoError(t, err)
	next := mustCreateRun(t, groups, sampleGroupRun(uuid.New(), "next", "m"))
	seq, first, err = groups.RecordTaskTerminal(ctx, next.ID, "m", instance.GroupTaskStatusCompleted, nil, nil, time.Now())
	require.NoError(t, err)
	require.True(t, first)
	assert.Greater(t, seq, *produced.TerminalSeq, "deleting the highest receipt cannot rewind the durable allocator")
}

func TestGroupTerminal_RosterSnapshotsAcrossCommitsAndRestartWithoutCallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.db")
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	groups, instances, run := terminalRun(t, db, "a", "b")
	// Explicit barriers, not elapsed sleeps: observe before, between and after
	// the two commits from the exact transactional read the coordinator uses.
	commitB, bDone, commitA, aDone := make(chan struct{}), make(chan error, 1), make(chan struct{}), make(chan error, 1)
	releaseB, releaseA := sync.OnceFunc(func() { close(commitB) }), sync.OnceFunc(func() { close(commitA) })
	t.Cleanup(releaseB)
	t.Cleanup(releaseA)
	awaitCommit := func(done <-chan error) {
		t.Helper()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("terminal commit did not finish after its barrier was released")
		}
	}
	go func() {
		<-commitB
		bDone <- instances.SetCompleted(context.Background(), run.Tasks[1].ChildInstanceID)
		<-commitA
		aDone <- instances.SetCompleted(context.Background(), run.Tasks[0].ChildInstanceID)
	}()
	before := terminalRoster(t, groups, run.ID)
	_, ready := engine.DecideGroup(before, time.Now())
	assert.False(t, ready)
	releaseB()
	awaitCommit(bDone)
	between := terminalRoster(t, groups, run.ID)
	require.NotNil(t, between.Tasks[1].TerminalSeq)
	assert.Nil(t, between.Tasks[0].TerminalSeq)
	d, ready := engine.DecideGroup(between, time.Now())
	require.True(t, ready)
	assert.Equal(t, "b", d.WinnerKey)
	releaseA()
	awaitCommit(aDone)
	require.NoError(t, db.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, RunMigrations(reopened))
	after := terminalRoster(t, NewGroupRunRepo(reopened), run.ID)
	d, ready = engine.DecideGroup(after, time.Now())
	require.True(t, ready)
	assert.Equal(t, "b", d.WinnerKey, "recovery without callbacks consumes the same durable order")
	assert.Equal(t, "a", after.Tasks[0].Result["value"])
	assert.Equal(t, "b", after.Tasks[1].Result["value"])
}
