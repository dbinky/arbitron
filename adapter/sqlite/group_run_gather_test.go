package sqlite

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gatherRepoFixture struct {
	repo   *GroupRunRepo
	parent *instance.Instance
	run    *instance.GroupRun
}

func newGatherRepoFixture(t *testing.T) gatherRepoFixture {
	t.Helper()
	db := setupTestDB(t)
	ctx := context.Background()
	owner, err := NewUserRepo(db.DB).GetDefaultUser(ctx)
	require.NoError(t, err)
	parent := newTestInstance(t, owner)
	require.NoError(t, NewInstanceRepo(db.DB).Create(ctx, parent))
	repo := NewGroupRunRepo(db.DB)
	run := newGatherRepoRun(parent.ID, "grp")
	require.NoError(t, repo.CreateWithTasks(ctx, run))
	return gatherRepoFixture{repo: repo, parent: parent, run: run}
}

func newGatherRepoRun(parentID uuid.UUID, nodeID string) *instance.GroupRun {
	runID := uuid.New()
	return &instance.GroupRun{
		ID: runID, ParentInstanceID: parentID, NodeID: nodeID, Attempt: 1,
		Form: "branches", CompletionRule: "all_succeed", UnfinishedBehavior: "wait_for_remaining",
		GatherMode: instance.GatherModeResultsAndFiles, Config: map[string]any{"form": "branches"},
		BodyOwnership: map[string][]string{"a": {"a1"}, "b": {"b1"}}, StartVariables: map[string]any{"x": "y"},
		WorkspaceSnapshot: runID.String(), State: instance.GroupRunLaunching,
		Tasks: []instance.GroupTask{
			{GroupRunID: runID, Position: 0, Key: "a", Kind: instance.GroupTaskKindBranch, Origin: "a1", ChildInstanceID: uuid.New(), LaunchState: instance.GroupTaskPending, LiveStatus: instance.GroupTaskStatusNotStarted, Blocking: true},
			{GroupRunID: runID, Position: 1, Key: "b", Kind: instance.GroupTaskKindBranch, Origin: "b1", ChildInstanceID: uuid.New(), LaunchState: instance.GroupTaskPending, LiveStatus: instance.GroupTaskStatusNotStarted, Blocking: true},
		},
	}
}

func sampleGatherReport(runID, parentID uuid.UUID) instance.GatherReport {
	started := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	return instance.GatherReport{
		Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStateResolving,
		Handle: map[string]any{"snapshotHandle": runID.String(), "gatherId": uuid.New().String()}, MergeIndex: 1,
		Tasks:         []instance.GatherTaskReport{{Key: "a", Merged: true, Reason: "merged cleanly"}, {Key: "b", Reason: "conflicts", ConflictPaths: []string{"c.txt"}}},
		Attempts:      []instance.ResolutionAttempt{{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"c.txt"}, StartedAt: started}},
		ActiveSession: &instance.ResolutionSessionRef{GroupRunID: runID, MergeIndex: 1, Attempt: 1, InstanceID: parentID, NodeID: "grp"},
	}
}

func TestMigration020_AddsGatherColumnsOnce(t *testing.T) {
	db := setupTestDB(t)
	count := func(table, col string) int {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n))
		return n
	}
	assert.Equal(t, 1, count("group_runs", "gather_json"))
	assert.Equal(t, 1, count("group_tasks", "finalized_ref"))
	require.NoError(t, RunMigrations(db.DB))
}

func TestGroupRunRepo_FreshRunHasNoGatherState(t *testing.T) {
	f := newGatherRepoFixture(t)
	got, err := f.repo.Get(context.Background(), f.run.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Gather)
	for _, task := range got.Tasks {
		assert.Empty(t, task.FinalizedRef)
	}
}

func TestGroupRunRepo_SetGatherReport_RoundTripsThroughEveryReadPath(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	want := sampleGatherReport(f.run.ID, f.parent.ID)
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, want))
	ref := "refs/arbitron/tasks/" + f.run.Tasks[1].ChildInstanceID.String()
	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", ref))
	byID, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	byNode, err := f.repo.GetByParentNode(ctx, f.parent.ID, "grp")
	require.NoError(t, err)
	byChild, err := f.repo.GetByChild(ctx, f.run.Tasks[1].ChildInstanceID)
	require.NoError(t, err)
	list, err := f.repo.ListByParent(ctx, f.parent.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	for name, got := range map[string]*instance.GroupRun{"Get": byID, "GetByParentNode": byNode, "GetByChild": byChild, "ListByParent": &list[0]} {
		require.NotNil(t, got, name)
		require.NotNil(t, got.Gather, name)
		assert.Equal(t, want.Normalized(), *got.Gather, name)
		assert.Empty(t, got.Tasks[0].FinalizedRef, name)
		assert.Equal(t, ref, got.Tasks[1].FinalizedRef, name)
	}
}

func TestGroupRunRepo_SetGatherReport_ReplacesPreviousReport(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	first := sampleGatherReport(f.run.ID, f.parent.ID)
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, first))
	second := first.Clone()
	second.State, second.MergeIndex, second.ActiveSession = instance.GatherStateApplied, 2, nil
	ended := first.Attempts[0].StartedAt.Add(time.Minute)
	second.Attempts[0].Outcome, second.Attempts[0].EndedAt = instance.ResolutionOutcomeResolved, &ended
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, second))
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Gather)
	assert.Equal(t, second.Normalized(), *got.Gather)
}

func TestGroupRunRepo_SetGatherReport_NormalizesNilLists(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, instance.GatherReport{Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStatePending}))
	var raw string
	require.NoError(t, f.repo.db.QueryRow(`SELECT gather_json FROM group_runs WHERE id = ?`, f.run.ID.String()).Scan(&raw))
	assert.Contains(t, raw, `"tasks":[]`)
	assert.Contains(t, raw, `"attempts":[]`)
}

func TestGroupRunRepo_SetGatherReport_Errors(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	require.ErrorIs(t, f.repo.SetGatherReport(ctx, uuid.New(), sampleGatherReport(f.run.ID, f.parent.ID)), instance.ErrGroupRunNotFound)
	bad := sampleGatherReport(f.run.ID, f.parent.ID)
	bad.State = "sideways"
	require.ErrorIs(t, f.repo.SetGatherReport(ctx, f.run.ID, bad), instance.ErrInvalidGatherReport)
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Gather)
}

func TestGroupRunRepo_SetGatherReport_FrozenOnceDone(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	final := sampleGatherReport(f.run.ID, f.parent.ID)
	final.State, final.ActiveSession = instance.GatherStateApplied, nil
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, final))
	ok, err := f.repo.TransitionState(ctx, f.run.ID, instance.GroupRunLaunching, instance.GroupRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = f.repo.Decide(ctx, f.run.ID, instance.GroupDecision{Outcome: "success", Rule: "all_succeed", Behavior: "wait_for_remaining", Reason: "all tasks completed", DecidedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = f.repo.TransitionState(ctx, f.run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = f.repo.SetFinalOutput(ctx, f.run.ID, "success", map[string]any{"outcome": "success"}, instance.GroupRunGathering)
	require.NoError(t, err)
	require.True(t, ok)
	late := final.Clone()
	late.Error = "a late writer"
	require.ErrorIs(t, f.repo.SetGatherReport(ctx, f.run.ID, late), instance.ErrGatherReportFrozen)
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Gather)
	assert.Equal(t, final.Normalized(), *got.Gather)
}

func TestGroupRunRepo_SetTaskFinalizedRef_IsKeyAddressed(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	ref := "refs/arbitron/tasks/" + f.run.Tasks[1].ChildInstanceID.String()
	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", ref))
	got, err := f.repo.GetByChild(ctx, f.run.Tasks[1].ChildInstanceID)
	require.NoError(t, err)
	require.Len(t, got.Tasks, 2)
	assert.Equal(t, "a", got.Tasks[0].Key)
	assert.Empty(t, got.Tasks[0].FinalizedRef)
	assert.Equal(t, ref, got.Tasks[1].FinalizedRef)
	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", ref))
	require.ErrorContains(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", ref+"-again"), "immutable")
	got, err = f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, ref, got.Tasks[1].FinalizedRef)
}

func TestGroupRunRepo_SetTaskFinalizedRef_ConcurrentPublication(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	refs := []string{"refs/arbitron/tasks/first", "refs/arbitron/tasks/second"}
	errs := make([]error, len(refs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "a", refs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	successes := 0
	for i := range refs {
		if errs[i] == nil {
			successes++
			assert.Equal(t, refs[i], got.Tasks[0].FinalizedRef)
		} else {
			assert.ErrorContains(t, errs[i], "immutable")
		}
	}
	assert.Equal(t, 1, successes)
}

func TestGroupRunRepo_SetTaskFinalizedRef_Errors(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	require.ErrorIs(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "zzz", "refs/arbitron/tasks/x"), instance.ErrGroupRunNotFound)
	require.ErrorIs(t, f.repo.SetTaskFinalizedRef(ctx, uuid.New(), "a", "refs/arbitron/tasks/x"), instance.ErrGroupRunNotFound)
	require.Error(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "a", ""))
	require.Error(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "", "refs/arbitron/tasks/x"))
}

func TestGroupRunRepo_ListByParent_HydratesEveryRun(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	second := newGatherRepoRun(f.parent.ID, "grp-2")
	require.NoError(t, f.repo.CreateWithTasks(ctx, second))
	require.NoError(t, f.repo.SetGatherReport(ctx, second.ID, instance.GatherReport{Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStatePending}))
	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, second.ID, "a", "refs/arbitron/tasks/second-a"))
	list, err := f.repo.ListByParent(ctx, f.parent.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byNode := map[string]instance.GroupRun{}
	for _, r := range list {
		byNode[r.NodeID] = r
	}
	assert.Nil(t, byNode["grp"].Gather)
	require.NotNil(t, byNode["grp-2"].Gather)
	assert.Equal(t, instance.GatherStatePending, byNode["grp-2"].Gather.State)
	assert.Equal(t, "refs/arbitron/tasks/second-a", byNode["grp-2"].Tasks[0].FinalizedRef)
	assert.Empty(t, byNode["grp"].Tasks[0].FinalizedRef)
}

func TestMigration020_PreservesPopulatedRoster(t *testing.T) {
	db, err := Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	entries, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() >= "020_" || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, err)
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	ctx := context.Background()
	run := newGatherRepoRun(uuid.New(), "legacy")
	repo := NewGroupRunRepo(db)
	require.NoError(t, repo.CreateWithTasks(ctx, run))
	migration, err := migrationsFS.ReadFile("migrations/020_group_gather.up.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	got, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, run, got, "additive upgrade must preserve every existing run/task field")
}

func TestGroupRunRepo_GatherWriteFailureLeavesPreviousState(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	original := sampleGatherReport(f.run.ID, f.parent.ID)
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, original))
	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", "refs/arbitron/tasks/original"))
	before, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	for _, sql := range []string{
		`CREATE TRIGGER reject_report BEFORE UPDATE OF gather_json ON group_runs BEGIN SELECT RAISE(ABORT, 'injected report failure'); END`,
		`CREATE TRIGGER reject_ref BEFORE UPDATE OF finalized_ref ON group_tasks BEGIN SELECT RAISE(ABORT, 'injected ref failure'); END`,
	} {
		_, err := f.repo.db.Exec(sql)
		require.NoError(t, err)
	}
	changed := original.Clone()
	changed.Error = "must not persist"
	require.ErrorContains(t, f.repo.SetGatherReport(ctx, f.run.ID, changed), "injected report failure")
	require.ErrorContains(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", "refs/arbitron/tasks/original"), "injected ref failure")
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Gather)
	assert.Equal(t, original.Normalized(), *got.Gather)
	assert.Equal(t, "refs/arbitron/tasks/original", got.Tasks[1].FinalizedRef)
	assert.Equal(t, before.UpdatedAt, got.UpdatedAt, "failed writes leave the existing timestamp intact")
}

func TestGroupRunRepo_GatherStorageErrors(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	report := sampleGatherReport(f.run.ID, f.parent.ID)
	bad := report.Clone()
	bad.Handle["unsupported"] = make(chan int)
	require.ErrorContains(t, f.repo.SetGatherReport(ctx, f.run.ID, bad), "marshalling gather report")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, f.repo.SetGatherReport(cancelled, f.run.ID, report), context.Canceled)
	require.ErrorIs(t, f.repo.SetTaskFinalizedRef(cancelled, f.run.ID, "a", "ref"), context.Canceled)
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Gather)
	assert.Empty(t, got.Tasks[0].FinalizedRef)
	for _, repo := range []*GroupRunRepo{nil, NewGroupRunRepo(nil)} {
		require.ErrorIs(t, repo.SetGatherReport(ctx, f.run.ID, report), errNoDB)
		require.ErrorIs(t, repo.SetTaskFinalizedRef(ctx, f.run.ID, "a", "ref"), errNoDB)
	}
	require.NoError(t, f.repo.db.Close())
	require.Error(t, f.repo.SetGatherReport(ctx, f.run.ID, report))
	require.Error(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "a", "ref"))
}

func TestGroupRunRepo_GatherCorruptionFailsEveryReadWithoutLeakingTransaction(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := f.repo.db.ExecContext(ctx, `UPDATE group_runs SET gather_json = '{' WHERE id = ?`, f.run.ID.String())
	require.NoError(t, err)
	_, err = f.repo.Get(ctx, f.run.ID)
	require.ErrorContains(t, err, "decode gather report")
	_, err = f.repo.GetByParentNode(ctx, f.parent.ID, "grp")
	require.ErrorContains(t, err, "decode gather report")
	_, err = f.repo.GetByChild(ctx, f.run.Tasks[0].ChildInstanceID)
	require.ErrorContains(t, err, "decode gather report")
	_, err = f.repo.ListByParent(ctx, f.parent.ID)
	require.ErrorContains(t, err, "decode gather report")
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, sampleGatherReport(f.run.ID, f.parent.ID)))
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Gather)
}

func TestGroupRunRepo_SatisfiesThePort(t *testing.T) {
	var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)
}

func TestGroupRunRepo_ListResultsResourceRunsIncludesSettledRunsWithoutSnapshots(t *testing.T) {
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a", "b")
	ctx := context.Background()
	ids, err := groups.ListResultsResourceRuns(ctx)
	require.NoError(t, err)
	require.Empty(t, ids)
	require.NoError(t, instances.SetCompleted(ctx, run.Tasks[0].ChildInstanceID))
	require.NoError(t, instances.SetCompleted(ctx, run.Tasks[1].ChildInstanceID))
	_, err = db.Exec(`UPDATE group_runs SET state='done', workspace_snapshot='', continuation_delivered=1, actions_applied=1 WHERE id=?`, run.ID.String())
	require.NoError(t, err)
	ids, err = groups.ListUnsettled(ctx)
	require.NoError(t, err)
	require.Empty(t, ids, "ordinary recovery no longer lists this run")
	ids, err = groups.ListResultsResourceRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{run.ID}, ids, "completed task inventory must survive snapshot deletion and deduplicate siblings")
	_, err = db.Exec(`UPDATE group_runs SET gather_mode='results_and_files' WHERE id=?`, run.ID.String())
	require.NoError(t, err)
	ids, err = groups.ListResultsResourceRuns(ctx)
	require.NoError(t, err)
	require.Empty(t, ids, "files-mode retention has a different policy")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = groups.ListResultsResourceRuns(cancelled)
	require.ErrorIs(t, err, context.Canceled)
}

// ReplaceGatherReport is the CAS both gather_json writers use (paired seam 6):
// a report read back from the store is a valid expected value, and any
// intervening write makes the older read stale.
func TestGroupRunRepo_ReplaceGatherReport_IsACompareAndSwap(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	first := sampleGatherReport(f.run.ID, f.parent.ID)
	first.Handle["mergeCount"] = 3 // an int in memory, a float64 once decoded

	require.ErrorIs(t, f.repo.ReplaceGatherReport(ctx, f.run.ID, &first, first), instance.ErrGatherReportStale,
		"nothing stored yet: only a nil expectation matches")
	require.NoError(t, f.repo.ReplaceGatherReport(ctx, f.run.ID, nil, first))

	read, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	gathererCopy, callbackCopy := read.Gather.Clone(), read.Gather.Clone()

	ended := time.Date(2026, 10, 16, 9, 45, 0, 0, time.UTC)
	callbackCopy.Attempts[0].EndedAt = &ended
	callbackCopy.Attempts[0].Outcome = instance.ResolutionOutcomeAbandoned
	require.NoError(t, f.repo.ReplaceGatherReport(ctx, f.run.ID, read.Gather, callbackCopy), "a hydrated report matches its stored form")

	gathererCopy.Attempts[0].Outcome = instance.ResolutionOutcomeResolved
	require.ErrorIs(t, f.repo.ReplaceGatherReport(ctx, f.run.ID, read.Gather, gathererCopy), instance.ErrGatherReportStale)
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.ResolutionOutcomeAbandoned, got.Gather.Attempts[0].Outcome, "the stale write changed nothing")

	require.ErrorIs(t, f.repo.ReplaceGatherReport(ctx, uuid.New(), nil, first), instance.ErrGroupRunNotFound)
	bad := first.Clone()
	bad.State = "sideways"
	require.ErrorIs(t, f.repo.ReplaceGatherReport(ctx, f.run.ID, got.Gather, bad), instance.ErrInvalidGatherReport)
}
