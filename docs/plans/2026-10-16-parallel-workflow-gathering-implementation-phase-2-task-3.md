# Parallel Workflow Gathering — Phase 2 Task 3: SQLite GroupRunRepo

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Implement `instance.GroupRunRepository` over the 019 `group_runs` / `group_tasks` tables as `sqlite.GroupRunRepo`: atomic run+roster creation, consistent reads, and compare-and-swap state transitions whose single conditional UPDATEs decide every race (terminal sequencing, the decision, the one-time final output).
**Architecture:** One new hand-written adapter file `adapter/sqlite/group_run_repo.go` (no sqlc). Each race-prone method is ONE conditional `UPDATE` whose `WHERE` clause is the CAS predicate; multi-row operations (create, decide, terminal intake, launch bookkeeping) run in one `*sql.Tx` and route every statement through that tx (the DB is `SetMaxOpenConns(1)`, so a stray `r.db` call inside a tx would deadlock). Reads load run + roster inside one transaction for a consistent snapshot. `terminal_seq` is a global sequence assigned in-statement as `MAX+1`, made collision-proof by the 019 UNIQUE index.
**Tech Stack:** Go 1.25.5, `database/sql`, `encoding/json`, `modernc.org/sqlite`, `github.com/google/uuid`, testify; tests use a real in-memory SQLite via `setupTestDB` and run clean under `-race`.
**Spec / Design:** `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §1 ("Repository operations must make child registration/enqueue recovery idempotent and serialize terminal observation with decision selection"; "Persist an ordering at terminal transition, with a deterministic tie-break") and §3 (`first_success` ordering; "Later terminals update live status only").

## Atomic-terminal dependency

P2-T16 is the owner of production terminal capture: it extends migration 019 with a
single durable clock and terminal-state triggers and adapts `RecordTaskTerminal` to the
same allocator. Finish it before P2-T10/T13/T14. The callback is a wake accelerator;
wall-clock timestamp sorting and callback receipt order cannot determine first success.
No extra public field or port is introduced. P3-T5 still finalizes file refs BEFORE the
successful terminal write, so the atomic receipt cannot advertise success without files.


**Interfaces**
- Consumes:
  - P2-T1 `domain/instance/group_run.go`: `GroupRun`, `GroupTask`, `GroupDecision`, `FrozenTask`, `GroupRunState` (+ `Valid`), `GroupTaskKind` (+ `Valid`), `GroupTaskLaunchState`, `GroupTaskStatus` (+ `IsLive`, `IsTerminal`), `ErrGroupRunExists`, `ErrGroupRunNotFound`, `ErrGroupTaskNotFound`, `GroupRunRepository`.
  - P2-T2 migration `adapter/sqlite/migrations/019_parallel_groups.up.sql` (tables, UNIQUE/CHECK constraints, `idx_group_tasks_terminal_seq`, `idx_group_runs_unsettled`).
  - Existing `adapter/sqlite`: `convert.go:13-35` (`uuidToString`, `parseUUID`, `parseTime`), `:44-53` (`nullStringToTimePtr`), `:149-169` (`toJSONString`, `fromJSONString`), `:196-216` (`toNullJSONString`, `fromNullJSONString`); `instance_spawn_rows.go:35-37` (`rowScanner`); `connection_repo.go:54-61` (`isUniqueViolation`); `integration_test.go:16-28` (`testDB`, `setupTestDB`); `db.go:34` (`SetMaxOpenConns(1)`).
- Produces (exported):
  - `type GroupRunRepo struct` (fields `db *sql.DB`, `now func() time.Time`)
  - `func NewGroupRunRepo(db *sql.DB) *GroupRunRepo`
  - `var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)` — all 17 methods with the contract's exact signatures.
- Semantics later tasks rely on (pinned by the tests):
  - `CreateWithTasks`: validates and normalizes (Attempt 0⇒1; State ""⇒`launching`, only `launching|running` accepted; no decision/output/continuation flags; ≥1 task; `Tasks[i].Position == i`; unique non-empty keys; valid kinds; unique non-nil reserved child ids; task `GroupRunID` Nil⇒run id; LaunchState ""⇒`pending` (only pending accepted); LiveStatus ""⇒`not_started` (only not_started accepted); no terminal/action state; `Blocking` forced true). Writes the normalized values + `CreatedAt/UpdatedAt` back into `run` ONLY after commit. `(parent,node,attempt)` or run-id conflict ⇒ `errors.Is(err, instance.ErrGroupRunExists)`; a reserved child id already in another roster ⇒ error mentioning "already belongs to a group run", nothing persisted.
  - `Get` ⇒ `ErrGroupRunNotFound`; `GetByParentNode` ⇒ highest attempt or `(nil, nil)`; `GetByChild` ⇒ owning run or `(nil, nil)`; `ListByParent` ⇒ oldest first, each with its own roster, `[]` when none. Rosters are always ordered by `Position`. A storage failure is never reported as not-found / "no run yet".
  - `MarkTaskLaunched`: `pending|launched` ⇒ `launched` (idempotent); a `not_started` live status becomes `queued`, any other live status is kept; `launch_failed` ⇒ error.
  - `MarkTaskLaunchFailed`: `pending|launch_failed` ⇒ `launch_failed` with the reason ("" ⇒ "launch failed"); records `"task <key>: <reason>"` on `GroupRun.LaunchError` if the run has none yet; `launched` ⇒ error.
  - `TransitionState`: CAS `from`→`to`; `(false, nil)` when the state is not `from`; refuses invalid states, `from == to`, `to == decided` (use `Decide`) and `from == done`; `to == done` IS allowed (settling a run under a cancelled/failed parent without output).
  - `RecordTaskTerminal`: terminal statuses only; first call assigns the next GLOBAL seq (strictly increasing across all runs), sets live status, result, failure, terminal time (zero `at` ⇒ now) and returns `first=true`; any later call returns the stored seq with `first=false` and writes nothing.
  - `UpdateTaskLiveStatus`: non-terminal live statuses only (`unfinished` and terminal statuses are rejected — terminals go through `RecordTaskTerminal`); a task whose live status is already terminal is silently left unchanged.
  - `Decide`: outcome must be `success|failure`; zero `DecidedAt` ⇒ now (stored UTC); CAS `state IN (launching,running) AND decision IS NULL` ⇒ `decided`, and in the same transaction every task's `Blocking=false`; a loser gets `(false, nil)` and the stored decision is untouched.
  - `MarkTaskCancelRequested` (intent only; live status unchanged); `MarkTaskContinuingIndependently` (also `Blocking=false`); `MarkActionsApplied`, `MarkContinuationDelivered` — idempotent; unknown run ⇒ `ErrGroupRunNotFound`, unknown key ⇒ `ErrGroupTaskNotFound`.
  - `SetFinalOutput`: port `success|failure`; `from` valid and not `done`; CAS `state = from AND final_output IS NULL` ⇒ `done` + output (nil ⇒ `{}`) + port + `continuation_ready=1`; second call `(false, nil)`.
  - `ListUnsettled`: ids with `state <> 'done' OR continuation_delivered = 0 OR actions_applied = 0`, oldest first. NOTE for P2-T10: a run that is decided+applied+delivered but never reaches `done` stays unsettled — settle it with `SetFinalOutput` or `TransitionState(…, done)`.
  - A repository built with a nil `*sql.DB` returns `errNoDB` from every method instead of panicking.

**Wiring & data provenance**
- Composition root: P2-T14 adds `groupRunRepo := adaptsqlite.NewGroupRunRepo(db)` in `cmd/arbitron/main.go:initDomain` at the head of its group-wiring block right after the `spawn_workflow` executor registration (main.go:940-949; the same `db` that backs `instanceRepo` at `main.go:373`; `db` from `adaptsqlite.Open` at `main.go:356`, migrated at `main.go:365`), and passes it to `engine.NewGroupLauncher` (P2-T8), `engine.NewGroupCoordinator` (P2-T10), `engine.NewParallelGroupExecutor` (P2-T11); Phase 4 passes it to `engine.NewGraphStateReader` (P4-T1). P2-T14's `cmd/arbitron/group_e2e_test.go` boots `initDomain` and fails if the repository is not wired.
- Producers of what it stores: the run/roster from `GroupLauncher.Launch` (P2-T8); launch marks from P2-T8 after `Engine.StartGroupTask` (P2-T7); terminal intake (`RecordTaskTerminal`, `UpdateTaskLiveStatus`) from `GroupCoordinator.Evaluate` (P2-T10) reading the child via `InstanceRepository.Get` (`domain/instance/ports.go:16`) and failure details via `NodeExecutionRepository` (`ports.go:58-69`); `Decide` input from `engine.DecideGroup` / `DecideLaunchFailure` (P2-T9); `SetFinalOutput` input from `engine.BuildGroupOutput` (P2-T9); `MarkContinuationDelivered` from `ParallelGroupExecutor` re-entry (P2-T11).
- Provenance probe: `TestGroupRunRepo_NilDBIsAnErrorNotAPanic` fails if the repository's only production input (the `*sql.DB` handed over by P2-T14) is absent and a call silently succeeds or panics.

### Task 3: SQLite GroupRunRepo
**Files:**
- Create: `adapter/sqlite/group_run_repo.go`
- Test: `adapter/sqlite/group_run_repo_test.go`

(No existing file is modified. The compile-time assertion lives in the new file, next to the type, rather than in `adapter/sqlite/assert.go`, so this task touches no shared file.)

- [x] Step 1: Write the failing repository tests. Create `adapter/sqlite/group_run_repo_test.go`:

```go
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
	_, err = repo.ListUnsettled(ctx)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.TransitionState(ctx, id, instance.GroupRunLaunching, instance.GroupRunRunning)
	assert.ErrorIs(t, err, errNoDB)
	_, err = repo.SetFinalOutput(ctx, id, "success", nil, instance.GroupRunDecided)
	assert.ErrorIs(t, err, errNoDB)
	assert.ErrorIs(t, repo.MarkActionsApplied(ctx, id), errNoDB)
	assert.ErrorIs(t, repo.MarkTaskCancelRequested(ctx, id, "a"), errNoDB)
}
```

- [x] Step 2: Run them, confirm they fail.

```bash
go test ./adapter/sqlite/ -run TestGroupRunRepo -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/group_run_repo_test.go:17:38: undefined: GroupRunRepo
adapter/sqlite/group_run_repo_test.go:20:9: undefined: NewGroupRunRepo
adapter/sqlite/group_run_repo_test.go:54:40: undefined: GroupRunRepo
...
adapter/sqlite/group_run_repo_test.go:882:85: undefined: errNoDB
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] Step 3: Implement the repository. Create `adapter/sqlite/group_run_repo.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupRunRepo implements instance.GroupRunRepository backed by SQLite
// (tables group_runs / group_tasks, migration 019).
//
// Every statement is HAND-WRITTEN (sqlc is not part of the toolchain; see the
// createInstanceSQL precedent). Each race-prone operation is a single
// conditional UPDATE whose WHERE clause is the compare-and-swap predicate, so
// "who won" is decided by SQLite's write serialization, never by a
// read-then-write in Go. Operations that touch more than one row run inside
// ONE transaction, and every statement of that transaction goes through the
// *sql.Tx: the database is opened with SetMaxOpenConns(1), so a stray r.db
// call inside a transaction would wait forever for the connection the
// transaction holds.
type GroupRunRepo struct {
	db  *sql.DB
	now func() time.Time
}

var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)

// NewGroupRunRepo creates a GroupRunRepo using the given database connection.
func NewGroupRunRepo(db *sql.DB) *GroupRunRepo {
	return &GroupRunRepo{db: db, now: time.Now}
}

// groupTimeLayout is fixed-width UTC RFC3339 with nanoseconds, so lexical
// ORDER BY on the stored strings equals chronological order (RFC3339Nano trims
// trailing zeros and would sort "…:05Z" after "…:05.5Z"). parseTime reads it.
const groupTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func groupTime(t time.Time) string { return t.UTC().Format(groupTimeLayout) }

// groupQueryer is satisfied by both *sql.DB and *sql.Tx.
type groupQueryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const groupRunColumns = `id, parent_instance_id, node_id, attempt, form, completion_rule, ` +
	`unfinished_behavior, gather_mode, config, body_ownership, start_variables, ` +
	`workspace_snapshot, state, decision, final_output, continuation_port, ` +
	`continuation_ready, continuation_delivered, actions_applied, launch_error, ` +
	`created_at, updated_at`

const groupTaskColumns = `group_run_id, position, task_key, kind, origin, child_instance_id, ` +
	`launch_state, launch_error, live_status, terminal_seq, terminal_at, result, failure, ` +
	`cancel_requested, blocking, continuing_independently`

// errNoDB is returned by every method of a repository constructed without a
// database, instead of a nil-pointer panic deep inside database/sql.
var errNoDB = errors.New("group run repository has no database")

func (r *GroupRunRepo) ready() error {
	if r == nil || r.db == nil {
		return errNoDB
	}
	return nil
}

// withTx runs fn in one transaction, committing only if fn succeeds.
func (r *GroupRunRepo) withTx(ctx context.Context, op string, fn func(tx *sql.Tx) error) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin transaction: %w", op, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", op, err)
	}
	return nil
}

// changed reports whether a conditional UPDATE matched at least one row —
// the CAS "did I win" signal.
func changed(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reading rows affected: %w", err)
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------
// CreateWithTasks
// ---------------------------------------------------------------------------

// CreateWithTasks implements instance.GroupRunRepository.
//
// The run row and EVERY roster row are inserted in ONE transaction: the
// roster must be complete before any child is enqueued, and a crash must
// never leave a run with half a roster. The run is validated and normalized
// first (Attempt 0 => 1, State "" => launching, task GroupRunID/LaunchState/
// LiveStatus defaulted, Blocking forced true) and the normalized values plus
// CreatedAt/UpdatedAt are written back to run only after the commit.
func (r *GroupRunRepo) CreateWithTasks(ctx context.Context, run *instance.GroupRun) error {
	if run == nil {
		return fmt.Errorf("creating group run: run is nil")
	}
	norm, err := normalizeNewGroupRun(*run)
	if err != nil {
		return fmt.Errorf("creating group run: %w", err)
	}
	now := r.nowUTC()
	if norm.CreatedAt.IsZero() {
		norm.CreatedAt = now
	}
	norm.CreatedAt = norm.CreatedAt.UTC()
	norm.UpdatedAt = norm.CreatedAt

	configJSON, err := toJSONString(norm.Config)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling config: %w", err)
	}
	ownershipJSON, err := ownershipToJSON(norm.BodyOwnership)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling body ownership: %w", err)
	}
	startJSON, err := toJSONString(norm.StartVariables)
	if err != nil {
		return fmt.Errorf("creating group run: marshalling start variables: %w", err)
	}

	err = r.withTx(ctx, "creating group run", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO group_runs (
			id, parent_instance_id, node_id, attempt, form, completion_rule,
			unfinished_behavior, gather_mode, config, body_ownership, start_variables,
			workspace_snapshot, state, launch_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuidToString(norm.ID), uuidToString(norm.ParentInstanceID), norm.NodeID, norm.Attempt,
			norm.Form, norm.CompletionRule, norm.UnfinishedBehavior, norm.GatherMode,
			configJSON, ownershipJSON, startJSON, norm.WorkspaceSnapshot, string(norm.State),
			norm.LaunchError, groupTime(norm.CreatedAt), groupTime(norm.UpdatedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("creating group run for parent %s node %q attempt %d: %w",
					norm.ParentInstanceID, norm.NodeID, norm.Attempt, instance.ErrGroupRunExists)
			}
			return fmt.Errorf("creating group run: %w", err)
		}
		for _, task := range norm.Tasks {
			_, err := tx.ExecContext(ctx, `INSERT INTO group_tasks (
				group_run_id, position, task_key, kind, origin, child_instance_id,
				launch_state, launch_error, live_status, blocking, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
				uuidToString(norm.ID), task.Position, task.Key, string(task.Kind), task.Origin,
				uuidToString(task.ChildInstanceID), string(task.LaunchState), task.LaunchError,
				string(task.LiveStatus), groupTime(norm.CreatedAt))
			if err != nil {
				if isUniqueViolation(err) && strings.Contains(err.Error(), "child_instance_id") {
					return fmt.Errorf("creating group run: task %q: child instance %s already belongs to a group run: %w",
						task.Key, task.ChildInstanceID, err)
				}
				return fmt.Errorf("creating group run: task %q: %w", task.Key, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	*run = norm
	return nil
}

// normalizeNewGroupRun validates a run about to be created and fills its
// defaults. It returns a copy; the caller's run is untouched on error.
func normalizeNewGroupRun(run instance.GroupRun) (instance.GroupRun, error) {
	if run.ID == uuid.Nil {
		return run, fmt.Errorf("run id is required")
	}
	if run.ParentInstanceID == uuid.Nil {
		return run, fmt.Errorf("parent instance id is required")
	}
	if run.NodeID == "" {
		return run, fmt.Errorf("node id is required")
	}
	if run.Attempt == 0 {
		run.Attempt = 1
	}
	if run.Attempt < 1 {
		return run, fmt.Errorf("attempt must be >= 1, got %d", run.Attempt)
	}
	if run.State == "" {
		run.State = instance.GroupRunLaunching
	}
	if run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning {
		return run, fmt.Errorf("a new group run must be launching or running, got %q", run.State)
	}
	if run.Decision != nil || run.FinalOutput != nil || run.ContinuationReady ||
		run.ContinuationDelivered || run.ActionsApplied || run.ContinuationPort != "" {
		return run, fmt.Errorf("a new group run cannot carry a decision, final output or continuation flags")
	}
	if len(run.Tasks) == 0 {
		return run, fmt.Errorf("a group run needs at least one task")
	}

	tasks := make([]instance.GroupTask, len(run.Tasks))
	keys := make(map[string]bool, len(run.Tasks))
	children := make(map[uuid.UUID]bool, len(run.Tasks))
	for i, t := range run.Tasks {
		if t.Position != i {
			return run, fmt.Errorf("task %d (%q): position must be %d (tasks are stored in author order)", i, t.Key, i)
		}
		if t.Key == "" {
			return run, fmt.Errorf("task %d: key is required", i)
		}
		if keys[t.Key] {
			return run, fmt.Errorf("task %q: duplicate key", t.Key)
		}
		keys[t.Key] = true
		if !t.Kind.Valid() {
			return run, fmt.Errorf("task %q: invalid kind %q", t.Key, t.Kind)
		}
		if t.ChildInstanceID == uuid.Nil {
			return run, fmt.Errorf("task %q: reserved child instance id is required", t.Key)
		}
		if children[t.ChildInstanceID] {
			return run, fmt.Errorf("task %q: duplicate child instance id %s", t.Key, t.ChildInstanceID)
		}
		children[t.ChildInstanceID] = true
		if t.GroupRunID == uuid.Nil {
			t.GroupRunID = run.ID
		}
		if t.GroupRunID != run.ID {
			return run, fmt.Errorf("task %q: belongs to group run %s, not %s", t.Key, t.GroupRunID, run.ID)
		}
		if t.LaunchState == "" {
			t.LaunchState = instance.GroupTaskPending
		}
		if t.LaunchState != instance.GroupTaskPending {
			return run, fmt.Errorf("task %q: a new roster entry must be pending, got %q", t.Key, t.LaunchState)
		}
		if t.LiveStatus == "" {
			t.LiveStatus = instance.GroupTaskStatusNotStarted
		}
		if t.LiveStatus != instance.GroupTaskStatusNotStarted {
			return run, fmt.Errorf("task %q: a new roster entry must be not_started, got %q", t.Key, t.LiveStatus)
		}
		if t.TerminalSeq != nil || t.TerminalAt != nil || t.Result != nil || t.Failure != nil ||
			t.CancelRequested || t.ContinuingIndependently || t.FinalizedRef != "" {
			return run, fmt.Errorf("task %q: a new roster entry cannot carry terminal or action state", t.Key)
		}
		t.Blocking = true
		tasks[i] = t
	}
	run.Tasks = tasks
	return run, nil
}

func (r *GroupRunRepo) nowUTC() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
}

// ownershipToJSON renders taskKey -> body node ids; nil/empty => "{}".
func ownershipToJSON(m map[string][]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ownershipFromJSON(s string) (map[string][]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string][]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// Get implements instance.GroupRunRepository. The run and its roster are read
// in ONE transaction so they are a consistent snapshot.
func (r *GroupRunRepo) Get(ctx context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx, `WHERE id = ?`, uuidToString(id))
		if err != nil {
			return fmt.Errorf("getting group run %s: %w", id, err)
		}
		if run == nil {
			return fmt.Errorf("group run %s: %w", id, instance.ErrGroupRunNotFound)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetByParentNode implements instance.GroupRunRepository: the LATEST attempt
// for (parent, node), or (nil, nil). "No run yet" is the executor's
// first-entry signal and is deliberately distinguishable from an error —
// treating a storage failure as "no run" would launch a second roster.
func (r *GroupRunRepo) GetByParentNode(ctx context.Context, parentID uuid.UUID, nodeID string) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run by parent node", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx,
			`WHERE parent_instance_id = ? AND node_id = ? ORDER BY attempt DESC LIMIT 1`,
			uuidToString(parentID), nodeID)
		if err != nil {
			return fmt.Errorf("getting group run of %s node %q: %w", parentID, nodeID, err)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetByChild implements instance.GroupRunRepository: the run whose roster
// reserved childID, or (nil, nil) when childID is not a group task.
func (r *GroupRunRepo) GetByChild(ctx context.Context, childID uuid.UUID) (*instance.GroupRun, error) {
	var out *instance.GroupRun
	err := r.withTx(ctx, "getting group run by child", func(tx *sql.Tx) error {
		run, err := loadGroupRun(ctx, tx,
			`WHERE id = (SELECT group_run_id FROM group_tasks WHERE child_instance_id = ?)`,
			uuidToString(childID))
		if err != nil {
			return fmt.Errorf("getting group run of child %s: %w", childID, err)
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListByParent implements instance.GroupRunRepository: every run of parentID
// (oldest first), each with its roster. An empty result is an empty slice.
func (r *GroupRunRepo) ListByParent(ctx context.Context, parentID uuid.UUID) ([]instance.GroupRun, error) {
	out := []instance.GroupRun{}
	err := r.withTx(ctx, "listing group runs by parent", func(tx *sql.Tx) error {
		runs, err := queryGroupRuns(ctx, tx,
			`WHERE parent_instance_id = ? ORDER BY created_at ASC, attempt ASC, id ASC`, uuidToString(parentID))
		if err != nil {
			return fmt.Errorf("listing group runs of %s: %w", parentID, err)
		}
		if len(runs) == 0 {
			return nil
		}
		tasks, err := queryGroupTasks(ctx, tx,
			`WHERE group_run_id IN (SELECT id FROM group_runs WHERE parent_instance_id = ?)
			 ORDER BY group_run_id, position`, uuidToString(parentID))
		if err != nil {
			return fmt.Errorf("listing group runs of %s: %w", parentID, err)
		}
		byRun := map[uuid.UUID][]instance.GroupTask{}
		for _, t := range tasks {
			byRun[t.GroupRunID] = append(byRun[t.GroupRunID], t)
		}
		for i := range runs {
			runs[i].Tasks = byRun[runs[i].ID]
		}
		out = runs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListUnsettled implements instance.GroupRunRepository. The predicate is
// exactly idx_group_runs_unsettled's, so the sweep reads only unsettled rows.
func (r *GroupRunRepo) ListUnsettled(ctx context.Context) ([]uuid.UUID, error) {
	if err := r.ready(); err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM group_runs
		WHERE state <> 'done' OR continuation_delivered = 0 OR actions_applied = 0
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing unsettled group runs: %w", err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing unsettled group runs: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing unsettled group runs: %w", err)
	}
	return out, nil
}

// loadGroupRun reads the single run selected by where/args plus its roster,
// or (nil, nil) when no run matches.
func loadGroupRun(ctx context.Context, q groupQueryer, where string, args ...any) (*instance.GroupRun, error) {
	runs, err := queryGroupRuns(ctx, q, where, args...)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	run := runs[0]
	tasks, err := queryGroupTasks(ctx, q, `WHERE group_run_id = ? ORDER BY position`, uuidToString(run.ID))
	if err != nil {
		return nil, err
	}
	run.Tasks = tasks
	return &run, nil
}

func queryGroupRuns(ctx context.Context, q groupQueryer, where string, args ...any) ([]instance.GroupRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+groupRunColumns+` FROM group_runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instance.GroupRun
	for rows.Next() {
		run, err := scanGroupRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanGroupRun(s rowScanner) (*instance.GroupRun, error) {
	var (
		idRaw, parentRaw, nodeID, form, rule, behavior, gather string
		configRaw, ownershipRaw, startRaw, snapshot, state     string
		decisionRaw, finalRaw                                  sql.NullString
		port, launchErr, createdRaw, updatedRaw                string
		attempt, ready, delivered, applied                     int
	)
	if err := s.Scan(&idRaw, &parentRaw, &nodeID, &attempt, &form, &rule, &behavior, &gather,
		&configRaw, &ownershipRaw, &startRaw, &snapshot, &state, &decisionRaw, &finalRaw,
		&port, &ready, &delivered, &applied, &launchErr, &createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	id, err := parseUUID(idRaw)
	if err != nil {
		return nil, fmt.Errorf("group run id: %w", err)
	}
	parentID, err := parseUUID(parentRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s parent id: %w", idRaw, err)
	}
	config, err := fromJSONString(configRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s config: %w", idRaw, err)
	}
	ownership, err := ownershipFromJSON(ownershipRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s body ownership: %w", idRaw, err)
	}
	start, err := fromJSONString(startRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s start variables: %w", idRaw, err)
	}
	var decision *instance.GroupDecision
	if decisionRaw.Valid && decisionRaw.String != "" {
		var d instance.GroupDecision
		if err := json.Unmarshal([]byte(decisionRaw.String), &d); err != nil {
			return nil, fmt.Errorf("group run %s decision: %w", idRaw, err)
		}
		decision = &d
	}
	final, err := fromNullJSONString(finalRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s final output: %w", idRaw, err)
	}
	createdAt, err := parseTime(createdRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s created_at: %w", idRaw, err)
	}
	updatedAt, err := parseTime(updatedRaw)
	if err != nil {
		return nil, fmt.Errorf("group run %s updated_at: %w", idRaw, err)
	}
	return &instance.GroupRun{
		ID:                    id,
		ParentInstanceID:      parentID,
		NodeID:                nodeID,
		Attempt:               attempt,
		Form:                  form,
		CompletionRule:        rule,
		UnfinishedBehavior:    behavior,
		GatherMode:            gather,
		Config:                config,
		BodyOwnership:         ownership,
		StartVariables:        start,
		WorkspaceSnapshot:     snapshot,
		State:                 instance.GroupRunState(state),
		Decision:              decision,
		FinalOutput:           final,
		ContinuationPort:      port,
		ContinuationReady:     ready == 1,
		ContinuationDelivered: delivered == 1,
		ActionsApplied:        applied == 1,
		LaunchError:           launchErr,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}, nil
}

func queryGroupTasks(ctx context.Context, q groupQueryer, where string, args ...any) ([]instance.GroupTask, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+groupTaskColumns+` FROM group_tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []instance.GroupTask
	for rows.Next() {
		var (
			runRaw, key, kind, origin, childRaw string
			launchState, launchErr, live        string
			position                            int
			seq                                 sql.NullInt64
			atRaw, resultRaw, failureRaw        sql.NullString
			cancel, blocking, independent       int
		)
		if err := rows.Scan(&runRaw, &position, &key, &kind, &origin, &childRaw, &launchState,
			&launchErr, &live, &seq, &atRaw, &resultRaw, &failureRaw, &cancel, &blocking, &independent); err != nil {
			return nil, err
		}
		runID, err := parseUUID(runRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q run id: %w", key, err)
		}
		childID, err := parseUUID(childRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q child id: %w", key, err)
		}
		at, err := nullStringToTimePtr(atRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q terminal_at: %w", key, err)
		}
		result, err := fromNullJSONString(resultRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q result: %w", key, err)
		}
		failure, err := fromNullJSONString(failureRaw)
		if err != nil {
			return nil, fmt.Errorf("group task %q failure: %w", key, err)
		}
		var seqPtr *int64
		if seq.Valid {
			v := seq.Int64
			seqPtr = &v
		}
		out = append(out, instance.GroupTask{
			GroupRunID:              runID,
			Position:                position,
			Key:                     key,
			Kind:                    instance.GroupTaskKind(kind),
			Origin:                  origin,
			ChildInstanceID:         childID,
			LaunchState:             instance.GroupTaskLaunchState(launchState),
			LaunchError:             launchErr,
			LiveStatus:              instance.GroupTaskStatus(live),
			TerminalSeq:             seqPtr,
			TerminalAt:              at,
			Result:                  result,
			Failure:                 failure,
			CancelRequested:         cancel == 1,
			Blocking:                blocking == 1,
			ContinuingIndependently: independent == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Existence helpers (only consulted after a conditional UPDATE matched 0 rows)
// ---------------------------------------------------------------------------

func runExists(ctx context.Context, q groupQueryer, runID uuid.UUID) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_runs WHERE id = ?`,
		uuidToString(runID)).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// missingRunErr distinguishes "the CAS lost" (run exists => nil) from "no such
// run" (ErrGroupRunNotFound).
func missingRunErr(ctx context.Context, q groupQueryer, runID uuid.UUID) error {
	ok, err := runExists(ctx, q, runID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	return nil
}

// taskRow reads one roster row's launch state and live status.
func taskRow(ctx context.Context, q groupQueryer, runID uuid.UUID, key string) (launch, live string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT launch_state, live_status FROM group_tasks
		WHERE group_run_id = ? AND task_key = ?`, uuidToString(runID), key).Scan(&launch, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return launch, live, true, nil
}

// missingTaskErr reports a missing task as ErrGroupRunNotFound when its run is
// missing too, otherwise ErrGroupTaskNotFound.
func missingTaskErr(ctx context.Context, q groupQueryer, runID uuid.UUID, key string) error {
	if err := missingRunErr(ctx, q, runID); err != nil {
		return err
	}
	return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupTaskNotFound)
}

// ---------------------------------------------------------------------------
// Launch bookkeeping
// ---------------------------------------------------------------------------

// MarkTaskLaunched implements instance.GroupRunRepository. Idempotent: an
// already-launched task stays launched. A task that never started is shown
// as queued from here on (its child was just enqueued); a live status the
// coordinator already recorded is left alone. A task whose launch already
// FAILED cannot be marked launched.
func (r *GroupRunRepo) MarkTaskLaunched(ctx context.Context, runID uuid.UUID, key string) error {
	return r.withTx(ctx, "marking group task launched", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET launch_state = 'launched',
			    live_status = CASE WHEN live_status = 'not_started' THEN 'queued' ELSE live_status END,
			    updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND launch_state IN ('pending','launched')`,
			groupTime(r.nowUTC()), uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		if ok {
			return nil
		}
		launch, _, found, err := taskRow(ctx, tx, runID, key)
		if err != nil {
			return fmt.Errorf("marking group task %q launched: %w", key, err)
		}
		if !found {
			return missingTaskErr(ctx, tx, runID, key)
		}
		return fmt.Errorf("marking group task %q launched: launch state is already %q", key, launch)
	})
}

// MarkTaskLaunchFailed implements instance.GroupRunRepository. It records the
// task's reason and, if the run has no launch error yet, the FIRST failure on
// the run ("task <key>: <reason>"), so GroupRun.LaunchError names what broke
// the launch barrier. A task that was already launched cannot be marked
// failed — its child exists and will report its own terminal.
func (r *GroupRunRepo) MarkTaskLaunchFailed(ctx context.Context, runID uuid.UUID, key, reason string) error {
	if reason == "" {
		reason = "launch failed"
	}
	return r.withTx(ctx, "marking group task launch failed", func(tx *sql.Tx) error {
		now := groupTime(r.nowUTC())
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET launch_state = 'launch_failed', launch_error = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND launch_state IN ('pending','launch_failed')`,
			reason, now, uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("marking group task %q launch failed: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("marking group task %q launch failed: %w", key, err)
		}
		if !ok {
			launch, _, found, err := taskRow(ctx, tx, runID, key)
			if err != nil {
				return fmt.Errorf("marking group task %q launch failed: %w", key, err)
			}
			if !found {
				return missingTaskErr(ctx, tx, runID, key)
			}
			return fmt.Errorf("marking group task %q launch failed: launch state is already %q", key, launch)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE group_runs
			SET launch_error = CASE WHEN launch_error = '' THEN ? ELSE launch_error END, updated_at = ?
			WHERE id = ?`, fmt.Sprintf("task %s: %s", key, reason), now, uuidToString(runID)); err != nil {
			return fmt.Errorf("recording group run launch error: %w", err)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// State machine
// ---------------------------------------------------------------------------

// TransitionState implements instance.GroupRunRepository as a CAS from -> to.
//
// Two transitions are refused because a dedicated CAS owns them: entering
// `decided` (Decide writes the decision in the same statement — a decided run
// without a decision could never be decided again) and leaving `done` (done
// is final; its output is immutable). Entering `done` IS allowed: it is how a
// run under a cancelled/failed parent is settled without a continuation.
func (r *GroupRunRepo) TransitionState(ctx context.Context, runID uuid.UUID, from, to instance.GroupRunState) (bool, error) {
	if !from.Valid() || !to.Valid() {
		return false, fmt.Errorf("transitioning group run %s: invalid state %q -> %q", runID, from, to)
	}
	if from == to {
		return false, fmt.Errorf("transitioning group run %s: from and to are both %q", runID, from)
	}
	if to == instance.GroupRunDecided {
		return false, fmt.Errorf("transitioning group run %s: use Decide to enter %q", runID, to)
	}
	if from == instance.GroupRunDone {
		return false, fmt.Errorf("transitioning group run %s: %q is final", runID, from)
	}
	if err := r.ready(); err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs SET state = ?, updated_at = ?
		WHERE id = ? AND state = ?`, string(to), groupTime(r.nowUTC()), uuidToString(runID), string(from))
	if err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return false, fmt.Errorf("transitioning group run %s: %w", runID, err)
	}
	if ok {
		return true, nil
	}
	if err := missingRunErr(ctx, r.db, runID); err != nil {
		return false, err
	}
	return false, nil
}

// RecordTaskTerminal implements instance.GroupRunRepository.
//
// The seq is assigned by ONE conditional UPDATE:
//
//	terminal_seq = (SELECT COALESCE(MAX(terminal_seq), 0) + 1 FROM group_tasks)
//	WHERE ... AND terminal_seq IS NULL
//
// SQLite executes the statement under its write lock, so concurrent intakes
// are serialized and each receives the next value; the UNIQUE index on
// terminal_seq makes a duplicate impossible even for a buggy second writer.
// The sequence is GLOBAL (strictly increasing across all runs), which also
// makes it strictly increasing within each run — the order first_success
// relies on. A second call for the same task matches 0 rows and returns the
// stored seq with first=false; nothing it carries is written.
func (r *GroupRunRepo) RecordTaskTerminal(ctx context.Context, runID uuid.UUID, key string,
	status instance.GroupTaskStatus, result, failure map[string]any, at time.Time) (int64, bool, error) {
	if !status.IsTerminal() {
		return 0, false, fmt.Errorf("recording group task %q terminal: %q is not a terminal status", key, status)
	}
	resultJSON, err := toNullJSONString(result)
	if err != nil {
		return 0, false, fmt.Errorf("recording group task %q terminal: marshalling result: %w", key, err)
	}
	failureJSON, err := toNullJSONString(failure)
	if err != nil {
		return 0, false, fmt.Errorf("recording group task %q terminal: marshalling failure: %w", key, err)
	}
	if at.IsZero() {
		at = r.nowUTC()
	}

	var seq int64
	var first bool
	err = r.withTx(ctx, "recording group task terminal", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks
			SET terminal_seq = (SELECT COALESCE(MAX(terminal_seq), 0) + 1 FROM group_tasks),
			    terminal_at = ?, live_status = ?, result = ?, failure = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ? AND terminal_seq IS NULL`,
			groupTime(at), string(status), resultJSON, failureJSON, groupTime(r.nowUTC()),
			uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		var stored sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT terminal_seq FROM group_tasks
			WHERE group_run_id = ? AND task_key = ?`, uuidToString(runID), key).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			return missingTaskErr(ctx, tx, runID, key)
		}
		if err != nil {
			return fmt.Errorf("recording group task %q terminal: %w", key, err)
		}
		if !stored.Valid {
			return fmt.Errorf("recording group task %q terminal: no sequence assigned", key)
		}
		seq = stored.Int64
		first = n == 1
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return seq, first, nil
}

// UpdateTaskLiveStatus implements instance.GroupRunRepository. Only
// non-terminal LIVE statuses are accepted (terminal ones are recorded, with
// their seq, by RecordTaskTerminal; "unfinished" is frozen-only), and a task
// whose live status is already terminal is left unchanged — a late "running"
// event can never resurrect a finished task.
func (r *GroupRunRepo) UpdateTaskLiveStatus(ctx context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus) error {
	if !status.IsLive() || status.IsTerminal() {
		return fmt.Errorf("updating group task %q live status: %q is not a non-terminal live status (terminals go through RecordTaskTerminal)", key, status)
	}
	return r.withTx(ctx, "updating group task live status", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE group_tasks SET live_status = ?, updated_at = ?
			WHERE group_run_id = ? AND task_key = ?
			  AND live_status NOT IN ('completed','failed','cancelled')`,
			string(status), groupTime(r.nowUTC()), uuidToString(runID), key)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		if ok {
			return nil
		}
		_, _, found, err := taskRow(ctx, tx, runID, key)
		if err != nil {
			return fmt.Errorf("updating group task %q live status: %w", key, err)
		}
		if !found {
			return missingTaskErr(ctx, tx, runID, key)
		}
		return nil // terminal status is sticky: not an error
	})
}

// Decide implements instance.GroupRunRepository.
//
// The CAS predicate is `state IN ('launching','running') AND decision IS
// NULL`; the winner's transaction also clears Blocking on every task, so no
// reader can ever see a decided run whose tasks still block the parent. A
// losing call (already decided, or any later state) returns (false, nil) and
// leaves the stored decision untouched — but losing the CAS is NOT permission
// for the caller to skip the decision's side effects (P2-T10 reconciles them).
func (r *GroupRunRepo) Decide(ctx context.Context, runID uuid.UUID, d instance.GroupDecision) (bool, error) {
	if d.Outcome != "success" && d.Outcome != "failure" {
		return false, fmt.Errorf("deciding group run %s: outcome must be \"success\" or \"failure\", got %q", runID, d.Outcome)
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = r.nowUTC()
	}
	d.DecidedAt = d.DecidedAt.UTC()
	raw, err := json.Marshal(d)
	if err != nil {
		return false, fmt.Errorf("deciding group run %s: marshalling decision: %w", runID, err)
	}

	var won bool
	err = r.withTx(ctx, "deciding group run", func(tx *sql.Tx) error {
		now := groupTime(r.nowUTC())
		res, err := tx.ExecContext(ctx, `UPDATE group_runs
			SET state = 'decided', decision = ?, updated_at = ?
			WHERE id = ? AND state IN ('launching','running') AND decision IS NULL`,
			string(raw), now, uuidToString(runID))
		if err != nil {
			return fmt.Errorf("deciding group run %s: %w", runID, err)
		}
		ok, err := changed(res)
		if err != nil {
			return fmt.Errorf("deciding group run %s: %w", runID, err)
		}
		if !ok {
			return missingRunErr(ctx, tx, runID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE group_tasks SET blocking = 0, updated_at = ?
			WHERE group_run_id = ?`, now, uuidToString(runID)); err != nil {
			return fmt.Errorf("releasing blocking tasks of group run %s: %w", runID, err)
		}
		won = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return won, nil
}

// ---------------------------------------------------------------------------
// Post-decision actions
// ---------------------------------------------------------------------------

// setTaskFlag sets one boolean roster column (plus any extra assignments) for
// (runID, key), reporting a missing task as ErrGroupTaskNotFound.
func (r *GroupRunRepo) setTaskFlag(ctx context.Context, op string, runID uuid.UUID, key, assignments string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_tasks SET `+assignments+`, updated_at = ?
		WHERE group_run_id = ? AND task_key = ?`, groupTime(r.nowUTC()), uuidToString(runID), key)
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, key, err)
	}
	ok, err := changed(res)
	if err != nil {
		return fmt.Errorf("%s %q: %w", op, key, err)
	}
	if ok {
		return nil
	}
	return missingTaskErr(ctx, r.db, runID, key)
}

// MarkTaskCancelRequested implements instance.GroupRunRepository. It records
// cancellation INTENT only; the task's live status reports "cancelled" only
// once its child actually is.
func (r *GroupRunRepo) MarkTaskCancelRequested(ctx context.Context, runID uuid.UUID, key string) error {
	return r.setTaskFlag(ctx, "marking group task cancel requested", runID, key, `cancel_requested = 1`)
}

// MarkTaskContinuingIndependently implements instance.GroupRunRepository. A
// task continuing independently never blocks the parent again.
func (r *GroupRunRepo) MarkTaskContinuingIndependently(ctx context.Context, runID uuid.UUID, key string) error {
	return r.setTaskFlag(ctx, "marking group task continuing independently", runID, key,
		`continuing_independently = 1, blocking = 0`)
}

// setRunFlag sets one boolean run column, reporting a missing run as
// ErrGroupRunNotFound. Idempotent.
func (r *GroupRunRepo) setRunFlag(ctx context.Context, op string, runID uuid.UUID, column string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs SET `+column+` = 1, updated_at = ? WHERE id = ?`,
		groupTime(r.nowUTC()), uuidToString(runID))
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, runID, err)
	}
	if ok {
		return nil
	}
	return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
}

// MarkActionsApplied implements instance.GroupRunRepository.
func (r *GroupRunRepo) MarkActionsApplied(ctx context.Context, runID uuid.UUID) error {
	return r.setRunFlag(ctx, "marking group run actions applied", runID, "actions_applied")
}

// MarkContinuationDelivered implements instance.GroupRunRepository.
func (r *GroupRunRepo) MarkContinuationDelivered(ctx context.Context, runID uuid.UUID) error {
	return r.setRunFlag(ctx, "marking group run continuation delivered", runID, "continuation_delivered")
}

// SetFinalOutput implements instance.GroupRunRepository as ONE conditional
// UPDATE: `state = from AND final_output IS NULL` -> state done, the output,
// the port and continuation_ready = 1. The output is written exactly once;
// a nil output is stored as an empty object so "set" stays distinguishable
// from "not yet set".
func (r *GroupRunRepo) SetFinalOutput(ctx context.Context, runID uuid.UUID, port string, output map[string]any, from instance.GroupRunState) (bool, error) {
	if port != "success" && port != "failure" {
		return false, fmt.Errorf("setting group run %s final output: port must be \"success\" or \"failure\", got %q", runID, port)
	}
	if !from.Valid() || from == instance.GroupRunDone {
		return false, fmt.Errorf("setting group run %s final output: invalid from state %q", runID, from)
	}
	outJSON, err := toJSONString(output)
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: marshalling: %w", runID, err)
	}
	if err := r.ready(); err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE group_runs
		SET state = 'done', final_output = ?, continuation_port = ?, continuation_ready = 1, updated_at = ?
		WHERE id = ? AND state = ? AND final_output IS NULL`,
		outJSON, port, groupTime(r.nowUTC()), uuidToString(runID), string(from))
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	ok, err := changed(res)
	if err != nil {
		return false, fmt.Errorf("setting group run %s final output: %w", runID, err)
	}
	if ok {
		return true, nil
	}
	if err := missingRunErr(ctx, r.db, runID); err != nil {
		return false, err
	}
	return false, nil
}
```

Implementation notes the tests pin:
- Every statement inside `withTx` uses the `*sql.Tx` (`tx` or a `groupQueryer` that is the tx): with `SetMaxOpenConns(1)` (`db.go:34`) an `r.db` call inside an open transaction blocks forever.
- The CAS result is `RowsAffected() > 0` (`changed`). SQLite counts a matched row even when the assigned value is unchanged, which is what makes the idempotent flag setters report "found" on repeat calls.
- A losing CAS consults existence (`missingRunErr` / `missingTaskErr`) only to tell "lost" (`false, nil`) from "no such run/task" (sentinel error); losing never mutates anything.
- Timestamps use the fixed-width `groupTimeLayout`, so `ORDER BY created_at` is chronological even within one second; `parseTime` (`convert.go:29`) reads them.

- [x] Step 4: Run the tests, confirm they pass — also under the race detector (the concurrency tests drive 32 and 8 goroutines through the CASes).

```bash
go test ./adapter/sqlite/ -run TestGroupRunRepo -count=1 -v
go test ./adapter/sqlite/ -run TestGroupRunRepo -count=1 -race
```
Expected:
```
--- PASS: TestGroupRunRepo_CreateAndGetRoundTrip (0.01s)
--- PASS: TestGroupRunRepo_CreateNormalizesAndWritesBack (0.01s)
--- PASS: TestGroupRunRepo_CreateWithEmptyMaps (0.01s)
--- PASS: TestGroupRunRepo_CreateRejectsInvalidRuns (0.01s)
--- PASS: TestGroupRunRepo_CreateDuplicateParentNodeAttemptIsErrGroupRunExists (0.01s)
--- PASS: TestGroupRunRepo_CreateIsAtomic (0.01s)
--- PASS: TestGroupRunRepo_GetMissingIsNotFound (0.01s)
--- PASS: TestGroupRunRepo_GetByParentNode (0.01s)
--- PASS: TestGroupRunRepo_GetByChild (0.01s)
--- PASS: TestGroupRunRepo_ListByParent (0.01s)
--- PASS: TestGroupRunRepo_MarkTaskLaunched (0.01s)
--- PASS: TestGroupRunRepo_MarkTaskLaunchFailed (0.01s)
--- PASS: TestGroupRunRepo_TransitionStateCAS (0.01s)
--- PASS: TestGroupRunRepo_RecordTaskTerminal (0.01s)
--- PASS: TestGroupRunRepo_RecordTaskTerminalZeroTimeUsesNow (0.01s)
--- PASS: TestGroupRunRepo_RecordTaskTerminalConcurrent (0.01s)
--- PASS: TestGroupRunRepo_UpdateTaskLiveStatus (0.01s)
--- PASS: TestGroupRunRepo_DecideCAS (0.01s)
--- PASS: TestGroupRunRepo_DecideFromLaunchingAndRejections (0.01s)
--- PASS: TestGroupRunRepo_DecideConcurrentExactlyOneWins (0.01s)
--- PASS: TestGroupRunRepo_TaskActionFlags (0.01s)
--- PASS: TestGroupRunRepo_RunFlags (0.01s)
--- PASS: TestGroupRunRepo_SetFinalOutputOnce (0.01s)
--- PASS: TestGroupRunRepo_SetFinalOutputFromGatheringWithNilOutput (0.01s)
--- PASS: TestGroupRunRepo_ListUnsettledSemantics (0.01s)
--- PASS: TestGroupRunRepo_ClosedDBReturnsErrors (0.01s)
--- PASS: TestGroupRunRepo_NilDBIsAnErrorNotAPanic (0.00s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.4XXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	1X.XXXs
```
(`TestGroupRunRepo_CreateRejectsInvalidRuns` additionally prints one indented `--- PASS` line per invalid-run subtest.)

- [x] Step 5: Run the whole package and the dependents; gofmt and vet.

Verification: focused tests (including `-race`), the package/dependent suite,
`gofmt -l adapter/sqlite`, `go vet ./adapter/sqlite/`, and `git diff --check`
passed. Self-review completed in spec → anti-green-theater → code-quality order.
Additional real-SQLite checks cover decision rollback on roster-update failure,
late terminals preserving frozen decisions/output, and nil-DB errors on all 17 methods.
Production construction/data producers remain explicitly owned by P2-T14/P2-T8/P2-T10;
P2-T16 owns replacing the provisional terminal allocator with atomic production receipts.

```bash
go test ./adapter/sqlite/ ./domain/... ./cmd/... -count=1
gofmt -l adapter/sqlite
go vet ./adapter/sqlite/
```
Expected: every package `ok`; `gofmt -l` and `go vet` print nothing. (`go test ./...` additionally shows the pre-existing, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`, which fails identically on the base commit.)

- [x] Step 6: Commit.

```bash
git add adapter/sqlite/group_run_repo.go adapter/sqlite/group_run_repo_test.go
git commit -m "feat(sqlite): GroupRunRepo with atomic roster creation and CAS decision, terminal-sequence and final-output transitions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
