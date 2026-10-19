package sqlite

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGroupRunRepo(t *testing.T) (*GroupRunRepo, testDB) {
	t.Helper()
	db := setupTestDB(t)
	return NewGroupRunRepo(db.DB), db
}

// sampleGroupRun builds a launching run of parentID at nodeID with one branch
// task per key, in the given (author) order.
func sampleGroupRun(parentID uuid.UUID, nodeID string, keys ...string) *instance.GroupRun {
	run := &instance.GroupRun{
		ID:                 uuid.New(),
		ParentInstanceID:   parentID,
		NodeID:             nodeID,
		Form:               "branches",
		CompletionRule:     "all_succeed",
		UnfinishedBehavior: "cancel_unfinished",
		GatherMode:         "results",
		Config: map[string]any{
			"form":  "branches",
			"tasks": []any{map[string]any{"key": "a", "entry": "a-1"}},
		},
		BodyOwnership:     map[string][]string{"a": {"a-1", "a-2"}},
		StartVariables:    map[string]any{"repo": "acme/widgets", "nested": map[string]any{"n": float64(1)}},
		WorkspaceSnapshot: "/ws/.group-snapshots/x",
	}
	for i, k := range keys {
		run.Tasks = append(run.Tasks, instance.GroupTask{
			Position:        i,
			Key:             k,
			Kind:            instance.GroupTaskKindBranch,
			Origin:          k + "-entry",
			ChildInstanceID: uuid.New(),
		})
	}
	return run
}

func mustCreateRun(t *testing.T, repo *GroupRunRepo, run *instance.GroupRun) *instance.GroupRun {
	t.Helper()
	require.NoError(t, repo.CreateWithTasks(context.Background(), run))
	return run
}

func taskKeys(run *instance.GroupRun) []string {
	out := make([]string, 0, len(run.Tasks))
	for _, t := range run.Tasks {
		out = append(out, t.Key)
	}
	return out
}

// ---------- create / get ----------

func TestGroupRunRepo_CreateAndGetRoundTrip(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	parent := uuid.New()
	// Author order deliberately NOT alphabetical: reads must follow Position.
	run := mustCreateRun(t, repo, sampleGroupRun(parent, "group-1", "c", "a", "b"))
	run.Tasks[1].Kind = instance.GroupTaskKindBranch

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)

	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, parent, got.ParentInstanceID)
	assert.Equal(t, "group-1", got.NodeID)
	assert.Equal(t, 1, got.Attempt)
	assert.Equal(t, "branches", got.Form)
	assert.Equal(t, "all_succeed", got.CompletionRule)
	assert.Equal(t, "cancel_unfinished", got.UnfinishedBehavior)
	assert.Equal(t, "results", got.GatherMode)
	assert.Equal(t, run.Config, got.Config)
	assert.Equal(t, map[string][]string{"a": {"a-1", "a-2"}}, got.BodyOwnership)
	assert.Equal(t, run.StartVariables, got.StartVariables)
	assert.Equal(t, "/ws/.group-snapshots/x", got.WorkspaceSnapshot)
	assert.Equal(t, instance.GroupRunLaunching, got.State)
	assert.Nil(t, got.Decision)
	assert.Nil(t, got.FinalOutput)
	assert.False(t, got.ContinuationReady)
	assert.False(t, got.ContinuationDelivered)
	assert.False(t, got.ActionsApplied)
	assert.True(t, run.CreatedAt.Equal(got.CreatedAt), "created_at round-trips at full precision")
	assert.True(t, run.UpdatedAt.Equal(got.UpdatedAt))

	require.Equal(t, []string{"c", "a", "b"}, taskKeys(got), "roster in author (Position) order")
	for i, task := range got.Tasks {
		assert.Equal(t, run.ID, task.GroupRunID)
		assert.Equal(t, i, task.Position)
		assert.Equal(t, run.Tasks[i].ChildInstanceID, task.ChildInstanceID)
		assert.Equal(t, run.Tasks[i].Key+"-entry", task.Origin)
		assert.Equal(t, instance.GroupTaskKindBranch, task.Kind)
		assert.Equal(t, instance.GroupTaskPending, task.LaunchState)
		assert.Equal(t, instance.GroupTaskStatusNotStarted, task.LiveStatus)
		assert.True(t, task.Blocking, "every task blocks the parent until a decision")
		assert.Nil(t, task.TerminalSeq)
		assert.Nil(t, task.TerminalAt)
		assert.Equal(t, "", task.FinalizedRef, "Phase 2 never sets a finalized ref")
	}
}

func TestGroupRunRepo_CreateNormalizesAndWritesBack(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	run := sampleGroupRun(uuid.New(), "group-1", "a")
	run.Attempt = 0
	run.State = ""
	run.Tasks[0].Blocking = false // a caller forgetting it must not create a non-blocking roster
	require.NoError(t, repo.CreateWithTasks(context.Background(), run))

	assert.Equal(t, 1, run.Attempt)
	assert.Equal(t, instance.GroupRunLaunching, run.State)
	assert.False(t, run.CreatedAt.IsZero())
	assert.Equal(t, run.ID, run.Tasks[0].GroupRunID)
	assert.Equal(t, instance.GroupTaskPending, run.Tasks[0].LaunchState)
	assert.Equal(t, instance.GroupTaskStatusNotStarted, run.Tasks[0].LiveStatus)
	assert.True(t, run.Tasks[0].Blocking)

	got, err := repo.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, got.Tasks[0].Blocking)
}

// Nil/empty JSON fields round-trip as nil, never as an error.
func TestGroupRunRepo_CreateWithEmptyMaps(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	run := sampleGroupRun(uuid.New(), "group-1", "a")
	run.Config, run.BodyOwnership, run.StartVariables, run.WorkspaceSnapshot = nil, nil, nil, ""
	mustCreateRun(t, repo, run)

	got, err := repo.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Config)
	assert.Nil(t, got.BodyOwnership)
	assert.Nil(t, got.StartVariables)
	assert.Equal(t, "", got.WorkspaceSnapshot, "a parent with no workspace has no snapshot handle")
}

func TestGroupRunRepo_CreateRejectsInvalidRuns(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	cases := map[string]func(*instance.GroupRun){
		"nil id":              func(r *instance.GroupRun) { r.ID = uuid.Nil },
		"nil parent":          func(r *instance.GroupRun) { r.ParentInstanceID = uuid.Nil },
		"empty node":          func(r *instance.GroupRun) { r.NodeID = "" },
		"negative attempt":    func(r *instance.GroupRun) { r.Attempt = -1 },
		"decided state":       func(r *instance.GroupRun) { r.State = instance.GroupRunDecided },
		"bogus state":         func(r *instance.GroupRun) { r.State = "finished" },
		"carries decision":    func(r *instance.GroupRun) { r.Decision = &instance.GroupDecision{Outcome: "success"} },
		"carries output":      func(r *instance.GroupRun) { r.FinalOutput = map[string]any{} },
		"carries ready flag":  func(r *instance.GroupRun) { r.ContinuationReady = true },
		"no tasks":            func(r *instance.GroupRun) { r.Tasks = nil },
		"position gap":        func(r *instance.GroupRun) { r.Tasks[1].Position = 2 },
		"empty key":           func(r *instance.GroupRun) { r.Tasks[0].Key = "" },
		"duplicate key":       func(r *instance.GroupRun) { r.Tasks[1].Key = r.Tasks[0].Key },
		"bad kind":            func(r *instance.GroupRun) { r.Tasks[0].Kind = "branches" },
		"nil child":           func(r *instance.GroupRun) { r.Tasks[0].ChildInstanceID = uuid.Nil },
		"duplicate child":     func(r *instance.GroupRun) { r.Tasks[1].ChildInstanceID = r.Tasks[0].ChildInstanceID },
		"foreign run id":      func(r *instance.GroupRun) { r.Tasks[0].GroupRunID = uuid.New() },
		"already launched":    func(r *instance.GroupRun) { r.Tasks[0].LaunchState = instance.GroupTaskLaunched },
		"already running":     func(r *instance.GroupRun) { r.Tasks[0].LiveStatus = instance.GroupTaskStatusRunning },
		"pre-set seq":         func(r *instance.GroupRun) { s := int64(1); r.Tasks[0].TerminalSeq = &s },
		"pre-set cancel flag": func(r *instance.GroupRun) { r.Tasks[0].CancelRequested = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			run := sampleGroupRun(uuid.New(), "group-1", "a", "b")
			mutate(run)
			id := run.ID
			err := repo.CreateWithTasks(ctx, run)
			require.Error(t, err)
			assert.NotErrorIs(t, err, instance.ErrGroupRunExists)
			if id != uuid.Nil {
				_, gerr := repo.Get(ctx, id)
				assert.ErrorIs(t, gerr, instance.ErrGroupRunNotFound, "nothing may be persisted for an invalid run")
			}
		})
	}
	require.Error(t, repo.CreateWithTasks(ctx, nil))
}

func TestGroupRunRepo_CreateDuplicateParentNodeAttemptIsErrGroupRunExists(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	parent := uuid.New()
	first := mustCreateRun(t, repo, sampleGroupRun(parent, "group-1", "a"))

	dup := sampleGroupRun(parent, "group-1", "a")
	err := repo.CreateWithTasks(ctx, dup)
	require.ErrorIs(t, err, instance.ErrGroupRunExists)
	_, gerr := repo.Get(ctx, dup.ID)
	assert.ErrorIs(t, gerr, instance.ErrGroupRunNotFound)

	same := sampleGroupRun(parent, "group-1", "a")
	same.ID = first.ID
	same.NodeID = "group-9"
	assert.ErrorIs(t, repo.CreateWithTasks(ctx, same), instance.ErrGroupRunExists, "a reused run id is an existing run")

	second := sampleGroupRun(parent, "group-1", "a")
	second.Attempt = 2
	require.NoError(t, repo.CreateWithTasks(ctx, second), "a new attempt of the same node is a distinct run")
	other := sampleGroupRun(parent, "group-2", "a")
	require.NoError(t, repo.CreateWithTasks(ctx, other), "another node of the same parent is a distinct run")
}

// The roster insert is atomic with the run insert: a roster conflict leaves
// NO run behind.
func TestGroupRunRepo_CreateIsAtomic(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	first := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	clash := sampleGroupRun(uuid.New(), "group-1", "x", "y")
	clash.Tasks[1].ChildInstanceID = first.Tasks[0].ChildInstanceID
	err := repo.CreateWithTasks(ctx, clash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already belongs to a group run")
	_, gerr := repo.Get(ctx, clash.ID)
	assert.ErrorIs(t, gerr, instance.ErrGroupRunNotFound, "the run row must roll back with its roster")
	assert.Equal(t, instance.GroupTaskLaunchState(""), clash.Tasks[0].LaunchState, "the caller's struct is written back only on success")
	assert.True(t, clash.CreatedAt.IsZero())
}

func TestGroupRunRepo_GetMissingIsNotFound(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	_, err := repo.Get(context.Background(), uuid.New())
	assert.ErrorIs(t, err, instance.ErrGroupRunNotFound)
}

// ---------- lookups ----------

func TestGroupRunRepo_GetByParentNode(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	parent := uuid.New()

	got, err := repo.GetByParentNode(ctx, parent, "group-1")
	require.NoError(t, err, "\"no run yet\" is the first-entry signal, not an error")
	assert.Nil(t, got)

	mustCreateRun(t, repo, sampleGroupRun(parent, "group-1", "a"))
	second := sampleGroupRun(parent, "group-1", "a")
	second.Attempt = 2
	mustCreateRun(t, repo, second)
	mustCreateRun(t, repo, sampleGroupRun(parent, "group-2", "z"))
	mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	got, err = repo.GetByParentNode(ctx, parent, "group-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, second.ID, got.ID, "the latest attempt wins")
	assert.Equal(t, 2, got.Attempt)
	assert.Equal(t, []string{"a"}, taskKeys(got), "the roster is loaded")

	got, err = repo.GetByParentNode(ctx, parent, "group-3")
	require.NoError(t, err)
	assert.Nil(t, got, "the node id is part of the key")
	got, err = repo.GetByParentNode(ctx, uuid.New(), "group-1")
	require.NoError(t, err)
	assert.Nil(t, got, "the parent id is part of the key")
}

func TestGroupRunRepo_GetByChild(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	r1 := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))
	r2 := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	got, err := repo.GetByChild(ctx, r1.Tasks[1].ChildInstanceID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, r1.ID, got.ID)
	task, ok := got.TaskByChild(r1.Tasks[1].ChildInstanceID)
	require.True(t, ok)
	assert.Equal(t, "b", task.Key)

	got, err = repo.GetByChild(ctx, r2.Tasks[0].ChildInstanceID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, r2.ID, got.ID)

	got, err = repo.GetByChild(ctx, uuid.New())
	require.NoError(t, err, "a non-group child is not an error")
	assert.Nil(t, got)
}

func TestGroupRunRepo_ListByParent(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	parent := uuid.New()
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tick := 0
	repo.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Millisecond) }

	empty, err := repo.ListByParent(ctx, parent)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	g1 := mustCreateRun(t, repo, sampleGroupRun(parent, "group-1", "a", "b"))
	g2 := mustCreateRun(t, repo, sampleGroupRun(parent, "group-2", "x"))
	mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	runs, err := repo.ListByParent(ctx, parent)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, g1.ID, runs[0].ID, "oldest first")
	assert.Equal(t, []string{"a", "b"}, taskKeys(&runs[0]))
	assert.Equal(t, g2.ID, runs[1].ID)
	assert.Equal(t, []string{"x"}, taskKeys(&runs[1]), "each run carries only its own roster")
}

// ---------- launch bookkeeping ----------

func TestGroupRunRepo_MarkTaskLaunched(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b", "c"))

	require.NoError(t, repo.MarkTaskLaunched(ctx, run.ID, "a"))
	require.NoError(t, repo.MarkTaskLaunched(ctx, run.ID, "a"), "idempotent")

	// A fast child whose terminal was already recorded keeps that status.
	_, _, err := repo.RecordTaskTerminal(ctx, run.ID, "b", instance.GroupTaskStatusCompleted, nil, nil, time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.MarkTaskLaunched(ctx, run.ID, "b"))

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupTaskLaunched, got.Tasks[0].LaunchState)
	assert.Equal(t, instance.GroupTaskStatusQueued, got.Tasks[0].LiveStatus, "a just-launched task shows as queued")
	assert.Equal(t, instance.GroupTaskLaunched, got.Tasks[1].LaunchState)
	assert.Equal(t, instance.GroupTaskStatusCompleted, got.Tasks[1].LiveStatus, "launch never downgrades a recorded status")
	assert.Equal(t, instance.GroupTaskPending, got.Tasks[2].LaunchState, "only the named task changes")

	require.NoError(t, repo.MarkTaskLaunchFailed(ctx, run.ID, "c", "boom"))
	err = repo.MarkTaskLaunched(ctx, run.ID, "c")
	require.Error(t, err, "a failed launch cannot become launched")
	assert.Contains(t, err.Error(), "launch_failed")

	assert.ErrorIs(t, repo.MarkTaskLaunched(ctx, run.ID, "zz"), instance.ErrGroupTaskNotFound)
	assert.ErrorIs(t, repo.MarkTaskLaunched(ctx, uuid.New(), "a"), instance.ErrGroupRunNotFound)
}

func TestGroupRunRepo_MarkTaskLaunchFailed(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b", "c"))

	require.NoError(t, repo.MarkTaskLaunchFailed(ctx, run.ID, "b", "child workflow \"x\" not found"))
	require.NoError(t, repo.MarkTaskLaunchFailed(ctx, run.ID, "c", "depth exceeded"))
	require.NoError(t, repo.MarkTaskLaunchFailed(ctx, run.ID, "c", ""))

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupTaskLaunchFailed, got.Tasks[1].LaunchState)
	assert.Equal(t, "child workflow \"x\" not found", got.Tasks[1].LaunchError)
	assert.Equal(t, "launch failed", got.Tasks[2].LaunchError, "an empty reason is replaced, never stored blank")
	assert.Equal(t, "task b: child workflow \"x\" not found", got.LaunchError, "the FIRST failure is recorded on the run")

	require.NoError(t, repo.MarkTaskLaunched(ctx, run.ID, "a"))
	err = repo.MarkTaskLaunchFailed(ctx, run.ID, "a", "late")
	require.Error(t, err, "a launched task cannot be marked failed")
	assert.Contains(t, err.Error(), "launched")

	assert.ErrorIs(t, repo.MarkTaskLaunchFailed(ctx, run.ID, "zz", "x"), instance.ErrGroupTaskNotFound)
	assert.ErrorIs(t, repo.MarkTaskLaunchFailed(ctx, uuid.New(), "a", "x"), instance.ErrGroupRunNotFound)
}

// ---------- state CAS ----------

func TestGroupRunRepo_TransitionStateCAS(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	ok, err := repo.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = repo.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning)
	require.NoError(t, err)
	assert.False(t, ok, "a stale from-state loses the CAS without an error")

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunRunning, got.State)

	_, err = repo.TransitionState(ctx, run.ID, instance.GroupRunRunning, instance.GroupRunDecided)
	assert.Error(t, err, "entering decided requires Decide")
	_, err = repo.TransitionState(ctx, run.ID, instance.GroupRunDone, instance.GroupRunRunning)
	assert.Error(t, err, "done is final")
	_, err = repo.TransitionState(ctx, run.ID, "bogus", instance.GroupRunRunning)
	assert.Error(t, err)
	_, err = repo.TransitionState(ctx, run.ID, instance.GroupRunRunning, instance.GroupRunRunning)
	assert.Error(t, err)

	_, err = repo.TransitionState(ctx, uuid.New(), instance.GroupRunLaunching, instance.GroupRunRunning)
	assert.ErrorIs(t, err, instance.ErrGroupRunNotFound)

	// Settling a run under a cancelled parent: running -> done with no output.
	ok, err = repo.TransitionState(ctx, run.ID, instance.GroupRunRunning, instance.GroupRunDone)
	require.NoError(t, err)
	assert.True(t, ok)
}

// ---------- terminal intake ----------

func TestGroupRunRepo_RecordTaskTerminal(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))
	other := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "z"))
	at := time.Date(2026, 10, 16, 12, 0, 0, 123456789, time.UTC)

	seq, first, err := repo.RecordTaskTerminal(ctx, run.ID, "b", instance.GroupTaskStatusFailed,
		map[string]any{"partial": "yes"}, map[string]any{"state": "failed", "error": "boom"}, at)
	require.NoError(t, err)
	assert.True(t, first)
	assert.Equal(t, int64(1), seq)

	// Second intake of the same terminal: same seq, nothing overwritten.
	seq2, first2, err := repo.RecordTaskTerminal(ctx, run.ID, "b", instance.GroupTaskStatusCompleted,
		map[string]any{"other": true}, nil, at.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, first2)
	assert.Equal(t, seq, seq2)

	seqA, _, err := repo.RecordTaskTerminal(ctx, run.ID, "a", instance.GroupTaskStatusCompleted, map[string]any{"x": float64(1)}, nil, at)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seqA)
	seqZ, _, err := repo.RecordTaskTerminal(ctx, other.ID, "z", instance.GroupTaskStatusCancelled, nil, nil, at)
	require.NoError(t, err)
	assert.Equal(t, int64(3), seqZ, "the sequence is global across runs")

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	b := got.Tasks[1]
	require.NotNil(t, b.TerminalSeq)
	assert.Equal(t, int64(1), *b.TerminalSeq)
	require.NotNil(t, b.TerminalAt)
	assert.True(t, at.Equal(*b.TerminalAt), "terminal time keeps sub-second precision")
	assert.Equal(t, instance.GroupTaskStatusFailed, b.LiveStatus)
	assert.Equal(t, map[string]any{"partial": "yes"}, b.Result, "a failed task keeps its available variables")
	assert.Equal(t, map[string]any{"state": "failed", "error": "boom"}, b.Failure)
	assert.Equal(t, map[string]any{"x": float64(1)}, got.Tasks[0].Result)
	assert.Nil(t, got.Tasks[0].Failure)

	_, _, err = repo.RecordTaskTerminal(ctx, run.ID, "a", instance.GroupTaskStatusRunning, nil, nil, at)
	assert.Error(t, err, "only terminal statuses are recorded")
	_, _, err = repo.RecordTaskTerminal(ctx, run.ID, "a", instance.GroupTaskStatusUnfinished, nil, nil, at)
	assert.Error(t, err)
	_, _, err = repo.RecordTaskTerminal(ctx, run.ID, "zz", instance.GroupTaskStatusCompleted, nil, nil, at)
	assert.ErrorIs(t, err, instance.ErrGroupTaskNotFound)
	_, _, err = repo.RecordTaskTerminal(ctx, uuid.New(), "a", instance.GroupTaskStatusCompleted, nil, nil, at)
	assert.ErrorIs(t, err, instance.ErrGroupRunNotFound)
}

func TestGroupRunRepo_RecordTaskTerminalZeroTimeUsesNow(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	fixed := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	repo.now = func() time.Time { return fixed }
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	_, _, err := repo.RecordTaskTerminal(context.Background(), run.ID, "a", instance.GroupTaskStatusCompleted, nil, nil, time.Time{})
	require.NoError(t, err)
	got, err := repo.Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Tasks[0].TerminalAt)
	assert.True(t, fixed.Equal(*got.Tasks[0].TerminalAt))
}

// N goroutines record N distinct terminals (each twice, concurrently): every
// task gets exactly one seq, exactly one caller per task wins, the seqs are
// 1..N with no gaps or duplicates, and a duplicate caller sees the winner's seq.
func TestGroupRunRepo_RecordTaskTerminalConcurrent(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	const n = 16
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("t%02d", i)
	}
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", keys...))

	type res struct {
		key   string
		seq   int64
		first bool
		err   error
	}
	results := make(chan res, 2*n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, k := range keys {
		for dup := 0; dup < 2; dup++ {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				<-start
				seq, first, err := repo.RecordTaskTerminal(ctx, run.ID, k, instance.GroupTaskStatusCompleted, nil, nil, time.Now())
				results <- res{k, seq, first, err}
			}(k)
		}
	}
	close(start)
	wg.Wait()
	close(results)

	winners := map[string]int{}
	seqByKey := map[string]int64{}
	for r := range results {
		require.NoError(t, r.err)
		if r.first {
			winners[r.key]++
		}
		if prev, ok := seqByKey[r.key]; ok {
			assert.Equal(t, prev, r.seq, "both intakes of %s must report the same seq", r.key)
		}
		seqByKey[r.key] = r.seq
	}
	var seqs []int
	for _, k := range keys {
		assert.Equal(t, 1, winners[k], "exactly one first=true per task (%s)", k)
		seqs = append(seqs, int(seqByKey[k]))
	}
	sort.Ints(seqs)
	for i, s := range seqs {
		assert.Equal(t, i+1, s, "seqs are exactly 1..N")
	}

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	for _, task := range got.Tasks {
		require.NotNil(t, task.TerminalSeq)
		assert.Equal(t, seqByKey[task.Key], *task.TerminalSeq, "the stored seq is the one reported")
	}
}

// ---------- live status ----------

func TestGroupRunRepo_UpdateTaskLiveStatus(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))

	require.NoError(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "a", instance.GroupTaskStatusRunning))
	require.NoError(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "a", instance.GroupTaskStatusPaused))

	_, _, err := repo.RecordTaskTerminal(ctx, run.ID, "b", instance.GroupTaskStatusCancelled, nil, nil, time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "b", instance.GroupTaskStatusRunning),
		"a late non-terminal event on a finished task is a silent no-op")

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupTaskStatusPaused, got.Tasks[0].LiveStatus)
	assert.Equal(t, instance.GroupTaskStatusCancelled, got.Tasks[1].LiveStatus, "a terminal live status is never downgraded")

	assert.Error(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "a", instance.GroupTaskStatusUnfinished), "unfinished is frozen-only")
	assert.Error(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "a", instance.GroupTaskStatusCompleted), "terminals go through RecordTaskTerminal")
	assert.Error(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "a", "bogus"))
	assert.ErrorIs(t, repo.UpdateTaskLiveStatus(ctx, run.ID, "zz", instance.GroupTaskStatusRunning), instance.ErrGroupTaskNotFound)
	assert.ErrorIs(t, repo.UpdateTaskLiveStatus(ctx, uuid.New(), "a", instance.GroupTaskStatusRunning), instance.ErrGroupRunNotFound)
}

// ---------- decision CAS ----------

func sampleDecision(outcome, reason string) instance.GroupDecision {
	return instance.GroupDecision{
		Outcome: outcome, Rule: "all_succeed", Behavior: "cancel_unfinished", Reason: reason,
		DecidedAt: time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC),
		Tasks: []instance.FrozenTask{
			{Key: "a", Kind: instance.GroupTaskKindBranch, Origin: "a-entry", Status: instance.GroupTaskStatusCompleted,
				Result: map[string]any{"x": float64(1)}},
			{Key: "b", Kind: instance.GroupTaskKindBranch, Origin: "b-entry", Status: instance.GroupTaskStatusUnfinished,
				CancelRequested: true},
		},
	}
}

func TestGroupRunRepo_DecideCAS(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))
	_, err := repo.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning)
	require.NoError(t, err)

	d := sampleDecision("failure", "task a failed")
	won, err := repo.Decide(ctx, run.ID, d)
	require.NoError(t, err)
	assert.True(t, won)

	won, err = repo.Decide(ctx, run.ID, sampleDecision("success", "late success"))
	require.NoError(t, err)
	assert.False(t, won, "the second decision loses the CAS")

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunDecided, got.State)
	require.NotNil(t, got.Decision)
	assert.Equal(t, d, *got.Decision, "the first decision is stored verbatim and never replaced")
	for _, task := range got.Tasks {
		assert.False(t, task.Blocking, "a decision releases every task in the same transaction")
	}
}

func TestGroupRunRepo_DecideFromLaunchingAndRejections(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	won, err := repo.Decide(ctx, run.ID, instance.GroupDecision{Outcome: "failure", Reason: "launch failed: boom"})
	require.NoError(t, err)
	assert.True(t, won, "a launch failure decides straight from launching")
	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Decision)
	assert.False(t, got.Decision.DecidedAt.IsZero(), "a zero DecidedAt is stamped")

	_, err = repo.Decide(ctx, run.ID, instance.GroupDecision{Outcome: "maybe"})
	assert.Error(t, err)
	_, err = repo.Decide(ctx, uuid.New(), sampleDecision("success", "x"))
	assert.ErrorIs(t, err, instance.ErrGroupRunNotFound)

	// A settled (done) run can never be decided.
	settled := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))
	_, err = repo.TransitionState(ctx, settled.ID, instance.GroupRunLaunching, instance.GroupRunDone)
	require.NoError(t, err)
	won, err = repo.Decide(ctx, settled.ID, sampleDecision("success", "x"))
	require.NoError(t, err)
	assert.False(t, won)
}

func TestGroupRunRepo_DecideConcurrentExactlyOneWins(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	winner := ""
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			reason := fmt.Sprintf("caller-%d", i)
			won, err := repo.Decide(ctx, run.ID, sampleDecision("success", reason))
			assert.NoError(t, err)
			if won {
				mu.Lock()
				wins++
				winner = reason
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	assert.Equal(t, 1, wins)

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Decision)
	assert.Equal(t, winner, got.Decision.Reason, "the stored decision is the winner's")
}

// ---------- post-decision actions ----------

func TestGroupRunRepo_TaskActionFlags(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))

	require.NoError(t, repo.MarkTaskCancelRequested(ctx, run.ID, "a"))
	require.NoError(t, repo.MarkTaskCancelRequested(ctx, run.ID, "a"), "idempotent")
	require.NoError(t, repo.MarkTaskContinuingIndependently(ctx, run.ID, "b"))

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, got.Tasks[0].CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusNotStarted, got.Tasks[0].LiveStatus, "intent is not confirmation")
	assert.True(t, got.Tasks[0].Blocking, "cancel intent alone does not release the task")
	assert.True(t, got.Tasks[1].ContinuingIndependently)
	assert.False(t, got.Tasks[1].Blocking, "an independent task never blocks the parent")
	assert.False(t, got.Tasks[1].CancelRequested)

	assert.ErrorIs(t, repo.MarkTaskCancelRequested(ctx, run.ID, "zz"), instance.ErrGroupTaskNotFound)
	assert.ErrorIs(t, repo.MarkTaskContinuingIndependently(ctx, uuid.New(), "a"), instance.ErrGroupRunNotFound)
}

func TestGroupRunRepo_RunFlags(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	require.NoError(t, repo.MarkActionsApplied(ctx, run.ID))
	require.NoError(t, repo.MarkActionsApplied(ctx, run.ID), "idempotent")
	require.NoError(t, repo.MarkContinuationDelivered(ctx, run.ID))

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, got.ActionsApplied)
	assert.True(t, got.ContinuationDelivered)

	assert.ErrorIs(t, repo.MarkActionsApplied(ctx, uuid.New()), instance.ErrGroupRunNotFound)
	assert.ErrorIs(t, repo.MarkContinuationDelivered(ctx, uuid.New()), instance.ErrGroupRunNotFound)
}

// ---------- final output ----------

func TestGroupRunRepo_SetFinalOutputOnce(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))

	ok, err := repo.SetFinalOutput(ctx, run.ID, "success", map[string]any{"winner": "a"}, instance.GroupRunDecided)
	require.NoError(t, err)
	assert.False(t, ok, "a run that is not in the from-state loses")

	_, err = repo.Decide(ctx, run.ID, sampleDecision("success", "all tasks completed"))
	require.NoError(t, err)
	out := map[string]any{"outcome": "success", "tasks": []any{map[string]any{"key": "a"}}}
	ok, err = repo.SetFinalOutput(ctx, run.ID, "success", out, instance.GroupRunDecided)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.SetFinalOutput(ctx, run.ID, "failure", map[string]any{"outcome": "failure"}, instance.GroupRunDecided)
	require.NoError(t, err)
	assert.False(t, ok, "the output is written exactly once")

	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunDone, got.State)
	assert.Equal(t, out, got.FinalOutput, "the first output is immutable")
	assert.Equal(t, "success", got.ContinuationPort)
	assert.True(t, got.ContinuationReady)
	assert.False(t, got.ContinuationDelivered, "delivery is a separate step")

	_, err = repo.SetFinalOutput(ctx, run.ID, "sideways", nil, instance.GroupRunDecided)
	assert.Error(t, err)
	_, err = repo.SetFinalOutput(ctx, run.ID, "success", nil, instance.GroupRunDone)
	assert.Error(t, err)
	_, err = repo.SetFinalOutput(ctx, uuid.New(), "success", nil, instance.GroupRunDecided)
	assert.ErrorIs(t, err, instance.ErrGroupRunNotFound)
}

// Phase 3 finalizes from gathering; a nil output still counts as "set".
func TestGroupRunRepo_SetFinalOutputFromGatheringWithNilOutput(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))
	_, err := repo.Decide(ctx, run.ID, sampleDecision("success", "ok"))
	require.NoError(t, err)
	ok, err := repo.TransitionState(ctx, run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = repo.SetFinalOutput(ctx, run.ID, "failure", nil, instance.GroupRunGathering)
	require.NoError(t, err)
	assert.True(t, ok)
	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.FinalOutput, "a nil output is stored as an empty object")
	assert.Empty(t, got.FinalOutput)
	assert.Equal(t, "failure", got.ContinuationPort)
}

// ---------- reconciliation sweep ----------

func TestGroupRunRepo_ListUnsettledSemantics(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tick := 0
	repo.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Millisecond) }

	empty, err := repo.ListUnsettled(ctx)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	finalize := func(run *instance.GroupRun) {
		_, err := repo.Decide(ctx, run.ID, sampleDecision("success", "ok"))
		require.NoError(t, err)
		ok, err := repo.SetFinalOutput(ctx, run.ID, "success", map[string]any{}, instance.GroupRunDecided)
		require.NoError(t, err)
		require.True(t, ok)
	}
	launching := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "g", "a"))

	settled := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "g", "a"))
	finalize(settled)
	require.NoError(t, repo.MarkActionsApplied(ctx, settled.ID))
	require.NoError(t, repo.MarkContinuationDelivered(ctx, settled.ID))

	undelivered := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "g", "a"))
	finalize(undelivered)
	require.NoError(t, repo.MarkActionsApplied(ctx, undelivered.ID))

	unapplied := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "g", "a"))
	finalize(unapplied)
	require.NoError(t, repo.MarkContinuationDelivered(ctx, unapplied.ID))

	// Decided and flagged, but not done: still unsettled (state != done).
	decided := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "g", "a"))
	_, err = repo.Decide(ctx, decided.ID, sampleDecision("failure", "x"))
	require.NoError(t, err)
	require.NoError(t, repo.MarkActionsApplied(ctx, decided.ID))
	require.NoError(t, repo.MarkContinuationDelivered(ctx, decided.ID))

	got, err := repo.ListUnsettled(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{launching.ID, undelivered.ID, unapplied.ID, decided.ID}, got,
		"every unsettled run, oldest first; the fully settled run is excluded")
}

// ---------- error paths ----------

func TestGroupRunRepo_ClosedDBReturnsErrors(t *testing.T) {
	repo, db := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a"))
	require.NoError(t, db.DB.Close())
	id := run.ID

	assert.Error(t, repo.CreateWithTasks(ctx, sampleGroupRun(uuid.New(), "group-1", "a")))
	_, err := repo.Get(ctx, id)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, instance.ErrGroupRunNotFound, "a storage failure is never reported as not-found")
	_, err = repo.GetByParentNode(ctx, run.ParentInstanceID, "group-1")
	assert.Error(t, err, "a storage failure must not read as \"no run yet\"")
	_, err = repo.GetByChild(ctx, run.Tasks[0].ChildInstanceID)
	assert.Error(t, err)
	_, err = repo.ListByParent(ctx, run.ParentInstanceID)
	assert.Error(t, err)
	_, err = repo.ListUnsettled(ctx)
	assert.Error(t, err)
	assert.Error(t, repo.MarkTaskLaunched(ctx, id, "a"))
	assert.Error(t, repo.MarkTaskLaunchFailed(ctx, id, "a", "x"))
	_, err = repo.TransitionState(ctx, id, instance.GroupRunLaunching, instance.GroupRunRunning)
	assert.Error(t, err)
	_, _, err = repo.RecordTaskTerminal(ctx, id, "a", instance.GroupTaskStatusCompleted, nil, nil, time.Now())
	assert.Error(t, err)
	assert.Error(t, repo.UpdateTaskLiveStatus(ctx, id, "a", instance.GroupTaskStatusRunning))
	_, err = repo.Decide(ctx, id, sampleDecision("success", "x"))
	assert.Error(t, err)
	assert.Error(t, repo.MarkTaskCancelRequested(ctx, id, "a"))
	assert.Error(t, repo.MarkTaskContinuingIndependently(ctx, id, "a"))
	assert.Error(t, repo.MarkActionsApplied(ctx, id))
	_, err = repo.SetFinalOutput(ctx, id, "success", nil, instance.GroupRunDecided)
	assert.Error(t, err)
	assert.Error(t, repo.MarkContinuationDelivered(ctx, id))
}

// Provenance: the repository's only producer is the *sql.DB handed to
// NewGroupRunRepo by the composition root (P2-T14). A repository wired
// without one must fail loudly on every call, never panic or pretend success.
func TestGroupRunRepo_NilDBIsAnErrorNotAPanic(t *testing.T) {
	repo := NewGroupRunRepo(nil)
	ctx := context.Background()
	id := uuid.New()

	assert.ErrorIs(t, repo.CreateWithTasks(ctx, sampleGroupRun(uuid.New(), "g", "a")), errNoDB)
	_, err := repo.Get(ctx, id)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.GetByParentNode(ctx, id, "g")
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.GetByChild(ctx, id)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.ListByParent(ctx, id)
	assert.ErrorIs(t, err, errNoDB)
	assert.ErrorIs(t, repo.MarkTaskLaunched(ctx, id, "a"), errNoDB)
	assert.ErrorIs(t, repo.MarkTaskLaunchFailed(ctx, id, "a", "boom"), errNoDB)
	_, _, err = repo.RecordTaskTerminal(ctx, id, "a", instance.GroupTaskStatusCompleted, nil, nil, time.Time{})
	assert.ErrorIs(t, err, errNoDB)
	assert.ErrorIs(t, repo.UpdateTaskLiveStatus(ctx, id, "a", instance.GroupTaskStatusRunning), errNoDB)
	_, err = repo.Decide(ctx, id, sampleDecision("success", "ok"))
	assert.ErrorIs(t, err, errNoDB)
	assert.ErrorIs(t, repo.MarkTaskContinuingIndependently(ctx, id, "a"), errNoDB)
	assert.ErrorIs(t, repo.MarkContinuationDelivered(ctx, id), errNoDB)
	_, err = repo.ListUnsettled(ctx)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.TransitionState(ctx, id, instance.GroupRunLaunching, instance.GroupRunRunning)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.SetFinalOutput(ctx, id, "success", nil, instance.GroupRunDecided)
	assert.ErrorIs(t, err, errNoDB)
	assert.ErrorIs(t, repo.MarkActionsApplied(ctx, id), errNoDB)
	assert.ErrorIs(t, repo.MarkTaskCancelRequested(ctx, id, "a"), errNoDB)
}

func TestGroupRunRepo_DecisionRollsBackIfRosterReleaseFails(t *testing.T) {
	repo, db := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))
	_, err := db.DB.Exec(`CREATE TRIGGER reject_roster_release BEFORE UPDATE OF blocking ON group_tasks
		WHEN NEW.blocking = 0 BEGIN SELECT RAISE(ABORT, 'roster release failed'); END`)
	require.NoError(t, err)

	won, err := repo.Decide(ctx, run.ID, sampleDecision("success", "ok"))
	require.ErrorContains(t, err, "roster release failed")
	assert.False(t, won)
	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunLaunching, got.State)
	assert.Nil(t, got.Decision, "the decision must roll back with the roster update")
	for _, task := range got.Tasks {
		assert.True(t, task.Blocking)
	}
}

func TestGroupRunRepo_LateTerminalPreservesFrozenDecisionAndOutput(t *testing.T) {
	repo, _ := newGroupRunRepo(t)
	ctx := context.Background()
	run := mustCreateRun(t, repo, sampleGroupRun(uuid.New(), "group-1", "a", "b"))
	decision := sampleDecision("success", "first success")
	won, err := repo.Decide(ctx, run.ID, decision)
	require.NoError(t, err)
	require.True(t, won)
	output := map[string]any{"winner": "a", "result": "frozen result"}
	won, err = repo.SetFinalOutput(ctx, run.ID, "success", output, instance.GroupRunDecided)
	require.NoError(t, err)
	require.True(t, won)

	seq, first, err := repo.RecordTaskTerminal(ctx, run.ID, "b", instance.GroupTaskStatusCompleted,
		map[string]any{"result": "late result"}, nil, time.Now())
	require.NoError(t, err)
	require.True(t, first)
	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunDone, got.State)
	require.NotNil(t, got.Decision)
	assert.Equal(t, decision, *got.Decision)
	assert.Equal(t, output, got.FinalOutput)
	assert.Equal(t, "success", got.ContinuationPort)
	assert.Equal(t, instance.GroupTaskStatusCompleted, got.Tasks[1].LiveStatus)
	assert.Equal(t, map[string]any{"result": "late result"}, got.Tasks[1].Result)
	require.NotNil(t, got.Tasks[1].TerminalSeq)
	assert.Equal(t, seq, *got.Tasks[1].TerminalSeq)
}
