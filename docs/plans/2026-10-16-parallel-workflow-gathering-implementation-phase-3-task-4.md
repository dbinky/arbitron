# Parallel Workflow Gathering — Phase 3 Task 4: Durable gather report and finalized task refs (P3-B storage)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Persist the gatherer's crash-recovery state and immutable merge/resolution report on the group run, and each task's finalized working-area ref on its roster row, with migration **020**, the contract domain types in `domain/instance/group_gather.go`, and two new `GroupRunRepository` methods implemented in SQLite and in every fake.
**Architecture:** Migration `020_group_gather` adds two nullable columns — `group_runs.gather_json` (JSON `instance.GatherReport`) and `group_tasks.finalized_ref`. `adapter/sqlite/group_run_gather.go` adds hand-written SQL for `SetGatherReport` (validated, normalized, refused once the run is `done` so the final report is immutable) and `SetTaskFinalizedRef` (addressed by run id AND task key), plus the package func `hydrateGather(ctx, q groupQueryer, run)`, which every read path of P2-T3's `GroupRunRepo` (`Get`, `GetByParentNode`, `GetByChild` via `loadGroupRun`; `ListByParent` directly) calls on the read's own transaction before returning so no reader ever sees a run without its gather state. The domain types carry their vocabulary as constants plus `Validate`/`Normalized`/`Clone`.
**Tech Stack:** Go 1.25.5, `database/sql` + `modernc.org/sqlite` (hand-written SQL — `sqlc` is not installed; `adapter/sqlite/generated/` is not touched), `encoding/json`, `github.com/google/uuid`, testify; tests run against real in-memory SQLite with all migrations applied (`setupTestDB`, `adapter/sqlite/integration_test.go:20-28`).
**Spec / Design:** spec "Completed runs retain task outcomes, gathered results, and merge-resolution outcomes for inspection"; `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §1 bullet 4 ("Persist operation identity, current merge index, staging refs, conflict paths, applied marker, and rollback handle"), §1 bullet 2 ("Persist the task's ref before terminal notification"), §3 last paragraph (report for every task, conflicted paths, resolution attempts/outcomes, final success/failure and rollback status), Data Provenance row "Final refs retained before task completion/cleanup; merge progress and reports" (**P3-B**, storage half).

Schema/repo facts this task builds on (verified against the P2-T2 and P2-T3 plans): migration `019_parallel_groups.up.sql` (P2-T2 Step 3) creates `group_runs` (`id TEXT PRIMARY KEY`, `state TEXT` CHECK incl. `'done'`, `updated_at TEXT NOT NULL`, …) and `group_tasks` (`group_run_id TEXT NOT NULL REFERENCES group_runs(id) ON DELETE CASCADE`, `task_key TEXT NOT NULL`, UNIQUE `(group_run_id, task_key)`, …) and deliberately omits `gather_json` / `finalized_ref` ("Phase 3 (migration 020) adds finalized_ref"). Because `group_tasks` cascades from `group_runs`, 020 uses ADD COLUMN only — never a rebuild of either table. P2-T3's `type GroupRunRepo struct { db *sql.DB; now func() time.Time }` is built by `NewGroupRunRepo(db *sql.DB)`; its timestamps use `groupTime(t)` (fixed-width `groupTimeLayout`) and `r.nowUTC()`; its read paths run inside `r.withTx` and load through the package funcs `loadGroupRun` / `queryGroupRuns` / `queryGroupTasks` over a `groupQueryer`, and its `groupRunColumns` / `groupTaskColumns` do not read the Phase 3 columns. The DB is opened with `SetMaxOpenConns(1)` (`adapter/sqlite/db.go:34`), so `hydrateGather` MUST run on the read's `*sql.Tx`, never on `r.db` inside a transaction (it would wait forever for the held connection).

Migration 020 ownership: this task creates `020_group_gather.{up,down}.sql` with the two ADD COLUMN statements only. P3-T8 (Step 5) is the SINGLE owner of the `notifications` rebuild that admits `'conflict_resolution'`: it APPENDS that rebuild to the end of `020_group_gather.up.sql` and PREPENDS its revert to the top of `020_group_gather.down.sql`. This task must not touch `notifications`.

**Interfaces**
- Consumes:
  - P2-T1 `domain/instance/group_run.go`: `GroupRun`, `GroupTask` (incl. `FinalizedRef string`, `Key`, `Position`), `GroupRunState` constants (`GroupRunLaunching`, `GroupRunRunning`, `GroupRunDecided`, `GroupRunGathering`, `GroupRunDone`), `GroupDecision`, `ErrGroupRunNotFound`, `GroupRunRepository`.
  - P2-T3 `adapter/sqlite/group_run_repo.go`: `GroupRunRepo`, `NewGroupRunRepo`, `CreateWithTasks`, `Get`, `GetByParentNode`, `GetByChild`, `ListByParent`, `TransitionState`, `Decide`, `SetFinalOutput`, and the internals `ready`, `nowUTC`, `groupTime`, `groupQueryer`, `loadGroupRun`.
  - P2-T8 `domain/engine/group_fakes_test.go`: `memGroupRuns` (`begin`, `runLocked`, `taskLocked`, `copyGroupRun`, `seedRun`, `get`).
  - Existing: `adapter/sqlite/migrate.go:16-79` `RunMigrations` (one transaction per `NNN_*.up.sql`, sorted), `adapter/sqlite/convert.go:13-15` `uuidToString`, `adapter/sqlite/integration_test.go:20-28` `setupTestDB`, `:73-88` `newTestInstance`, `adapter/sqlite/user_repo.go` `NewUserRepo(...).GetDefaultUser`, `adapter/sqlite/instance_repo.go:24-29` `NewInstanceRepo`.
- Produces:
  - `adapter/sqlite/migrations/020_group_gather.up.sql` / `.down.sql`.
  - `domain/instance/group_gather.go` — exactly the contract types: `GatherReport{Mode, State, Handle, MergeIndex, Tasks, Attempts, ActiveSession, RollbackStatus, Error}`, `GatherTaskReport{Key, Merged, Reason, ConflictPaths}`, `ResolutionAttempt{MergeIndex, Attempt, TaskKey, ConflictPaths, Outcome, StartedAt, EndedAt}`, `ResolutionSessionRef{GroupRunID, MergeIndex, Attempt, InstanceID, NodeID}` with the contract's json tags; plus (additive refinements) constants `GatherModeResults`, `GatherModeResultsAndFiles`, `GatherStatePending|Merging|Resolving|Applied|Failed|RolledBack|NotApplicable`, `RollbackStatusRolledBack`, `RollbackStatusFailed`, `ResolutionOutcomeResolved|StillConflicted|Abandoned|Cancelled|Error`; `var ErrInvalidGatherReport`, `var ErrGatherReportFrozen`; `func (r GatherReport) Validate() error`, `func (r GatherReport) Normalized() GatherReport`, `func (r GatherReport) Clone() GatherReport`.
  - `domain/instance/group_run.go` edits: `GroupRun` gains `Gather *GatherReport`; `GroupRunRepository` gains `SetGatherReport(ctx context.Context, runID uuid.UUID, report GatherReport) error` and `SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error` (contract signatures).
  - `adapter/sqlite/group_run_gather.go`: `func (r *GroupRunRepo) SetGatherReport(ctx context.Context, runID uuid.UUID, report instance.GatherReport) error`, `func (r *GroupRunRepo) SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error`, `func hydrateGather(ctx context.Context, q groupQueryer, run *instance.GroupRun) error` (package func; `groupQueryer` is P2-T3's `*sql.DB`/`*sql.Tx` interface).
  - Semantics later tasks rely on: `SetGatherReport` replaces the whole report; invalid ⇒ `ErrInvalidGatherReport` and nothing stored; unknown run ⇒ `ErrGroupRunNotFound`; run in state `done` ⇒ `ErrGatherReportFrozen` (P3-T6 always persists the report BEFORE `SetFinalOutput(..., GroupRunGathering)`); stored JSON never has `null` task/attempt lists. `SetTaskFinalizedRef` replaces; unknown run or key ⇒ `ErrGroupRunNotFound`; empty key or ref ⇒ error. Reads return `Gather == nil` until a report is written and `FinalizedRef == ""` until a ref is written.

**Wiring & data provenance**
- Composition root: none new. The `groupRunRepo := adaptsqlite.NewGroupRunRepo(db)` value P2-T14 constructs in `cmd/arbitron/main.go:initDomain` is the repository P3-T11 hands to `engine.NewGroupFileFinalizer` (P3-T5) and `engine.NewGroupGatherer` (P3-T6); `cmd/arbitron/group_files_e2e_test.go` (P3-T11) reads the persisted report through `initDomain`. Migration 020 is applied by the existing `RunMigrations` call at start-up (no wiring).
- Producers of every input:
  - `report` — P3-T6 `FileGatherer.persist` (`domain/engine/group_gatherer.go`), P3-T8 resolution bridge (`ActiveSession`, attempts), P3-T10 reconciler (`AbortGather`).
  - `report.Handle` — the `engine.GatherHandle` returned by P3-T3 `WorkingArea.BeginGather`, JSON-encoded by P3-T6.
  - `ref` for `SetTaskFinalizedRef` — P3-T2 `WorkingArea.FinalizeTask`, called by P3-T5 `GroupFileFinalizer.FinalizeTerminal`; `key` — the task's roster `GroupTask.Key` (P2-T8 launcher), found by the finalizer via the finishing instance id.
  - `runID` — `instance.Instance.GroupRunID` (P2-T1/T2) of the task, or the run loaded by the executor (P2-T11).
- Provenance probes: `TestGroupRunRepo_SetGatherReport_RoundTripsThroughEveryReadPath` fails if any of the four read paths skips `hydrateGather`; `TestGroupRunRepo_SetTaskFinalizedRef_IsKeyAddressed` fails if the key argument is ignored; `TestGroupRunRepo_SetGatherReport_FrozenOnceDone` fails if a late writer can rewrite the final report.

### Task 4: Gather report + finalized refs storage
**Files:**
- Create: `domain/instance/group_gather.go`
- Create: `adapter/sqlite/migrations/020_group_gather.up.sql`
- Create: `adapter/sqlite/migrations/020_group_gather.down.sql`
- Create: `adapter/sqlite/group_run_gather.go`
- Modify: `domain/instance/group_run.go` (P2-T1) — the `GroupRun` struct (add one field) and the `GroupRunRepository` interface (add two methods)
- Modify: `adapter/sqlite/group_run_repo.go` (P2-T3) — the four read methods (`Get`, `GetByParentNode`, `GetByChild`, `ListByParent`) call `hydrateGather`
- Modify: `domain/engine/group_fakes_test.go` (P2-T8) — `memGroupRuns` gains the two methods; `copyGroupRun` deep-copies `Gather`
- Modify: every other `instance.GroupRunRepository` implementation found by the grep in Step 8
- Test:   `domain/instance/group_gather_test.go`
- Test:   `adapter/sqlite/group_run_gather_test.go`

#### Cycle 1 — domain types

- [x] Step 1: Write the failing domain tests. Create `domain/instance/group_gather_test.go`:

```go
package instance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fullGatherReport() GatherReport {
	started := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	return GatherReport{
		Mode:       GatherModeResultsAndFiles,
		State:      GatherStateResolving,
		Handle:     map[string]any{"snapshotHandle": "h", "nested": map[string]any{"k": []any{"v"}}},
		MergeIndex: 1,
		Tasks: []GatherTaskReport{
			{Key: "a", Merged: true, Reason: "merged cleanly"},
			{Key: "b", Merged: false, Reason: "conflicts", ConflictPaths: []string{"c.txt"}},
		},
		Attempts: []ResolutionAttempt{
			{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"c.txt"}, Outcome: ResolutionOutcomeStillConflicted, StartedAt: started, EndedAt: &ended},
			{MergeIndex: 1, Attempt: 2, TaskKey: "b", ConflictPaths: []string{"c.txt"}, StartedAt: ended},
		},
		ActiveSession: &ResolutionSessionRef{GroupRunID: uuid.New(), MergeIndex: 1, Attempt: 2, InstanceID: uuid.New(), NodeID: "grp"},
	}
}

func TestGatherReport_ValidReportPasses(t *testing.T) {
	require.NoError(t, fullGatherReport().Validate())
	require.NoError(t, GatherReport{Mode: GatherModeResults, State: GatherStateNotApplicable}.Validate())
}

func TestGatherReport_ValidateListsEveryProblem(t *testing.T) {
	ended := time.Now()
	r := GatherReport{
		Mode:           "files",
		State:          "half-done",
		MergeIndex:     -1,
		RollbackStatus: "maybe",
		Tasks:          []GatherTaskReport{{Key: ""}, {Key: "a"}, {Key: "a"}},
		Attempts: []ResolutionAttempt{
			{TaskKey: "", Attempt: 0, MergeIndex: -1, Outcome: "won"},
			{TaskKey: "a", Attempt: 1, EndedAt: &ended, Outcome: "won"},
		},
		ActiveSession: &ResolutionSessionRef{},
	}
	err := r.Validate()
	require.ErrorIs(t, err, ErrInvalidGatherReport)
	for _, want := range []string{
		`mode "files"`, `state "half-done"`, "mergeIndex is negative", `rollbackStatus "maybe"`,
		"tasks[0] has no key", `task "a" appears twice`, "attempts[0] has no taskKey",
		"attempts[0].attempt must be >= 1", "attempts[0].mergeIndex is negative", `attempts[0].outcome "won"`,
		`attempts[1].outcome "won"`, "activeSession needs",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestGatherReport_CloneIsDeep(t *testing.T) {
	orig := fullGatherReport()
	cp := orig.Clone()
	require.Equal(t, orig, cp)

	cp.Handle["nested"].(map[string]any)["k"].([]any)[0] = "mutated"
	cp.Tasks[1].ConflictPaths[0] = "mutated"
	cp.Attempts[0].ConflictPaths[0] = "mutated"
	*cp.Attempts[0].EndedAt = time.Time{}
	cp.ActiveSession.NodeID = "mutated"

	assert.Equal(t, fullGatherReport().Tasks, orig.Tasks)
	assert.Equal(t, "v", orig.Handle["nested"].(map[string]any)["k"].([]any)[0])
	assert.Equal(t, "c.txt", orig.Attempts[0].ConflictPaths[0])
	assert.False(t, orig.Attempts[0].EndedAt.IsZero())
	assert.Equal(t, "grp", orig.ActiveSession.NodeID)
}

func TestGatherReport_NormalizedNeverNullLists(t *testing.T) {
	r := GatherReport{Mode: GatherModeResults, State: GatherStateNotApplicable,
		Attempts: []ResolutionAttempt{{TaskKey: "a", Attempt: 1}}}.Normalized()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	assert.JSONEq(t, `{"mode":"results","state":"not_applicable","mergeIndex":0,"tasks":[],
		"attempts":[{"mergeIndex":0,"attempt":1,"taskKey":"a","conflictPaths":[],"outcome":"","startedAt":"0001-01-01T00:00:00Z"}]}`, string(raw))
}

// Pins the wire names the gatherer, the group output and Phase 4 read.
func TestGatherReport_JSONShape(t *testing.T) {
	r := fullGatherReport()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"mode", "state", "handle", "mergeIndex", "tasks", "attempts", "activeSession"} {
		assert.Contains(t, m, k)
	}
	assert.NotContains(t, m, "rollbackStatus", "omitempty when no rollback happened")
	assert.NotContains(t, m, "error")
	task := m["tasks"].([]any)[1].(map[string]any)
	assert.Equal(t, []any{"c.txt"}, task["conflictPaths"])
	session := m["activeSession"].(map[string]any)
	for _, k := range []string{"groupRunId", "mergeIndex", "attempt", "instanceId", "nodeId"} {
		assert.Contains(t, session, k)
	}
	var back GatherReport
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, r.Tasks, back.Tasks)
	assert.Equal(t, r.ActiveSession, back.ActiveSession)
	assert.True(t, errors.Is(GatherReport{}.Validate(), ErrInvalidGatherReport))
}
```

- [x] Step 2: Run them; confirm they fail to compile.

Confirmed the planned undefined GatherReport / gather vocabulary compile failure before implementation.

```bash
go test ./domain/instance/ -count=1 -run GatherReport
```
Expected:
```
# github.com/arbitron/arbitron/domain/instance [github.com/arbitron/arbitron/domain/instance.test]
domain/instance/group_gather_test.go:14:25: undefined: GatherReport
domain/instance/group_gather_test.go:17:9: undefined: GatherReport
domain/instance/group_gather_test.go:18:15: undefined: GatherModeResultsAndFiles
domain/instance/group_gather_test.go:19:15: undefined: GatherStateResolving
...
FAIL	github.com/arbitron/arbitron/domain/instance [build failed]
```

- [x] Step 3: Implement. Create `domain/instance/group_gather.go`:

```go
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Gather modes recorded on a GatherReport. The values mirror
// workflow.GroupGatherResults / workflow.GroupGatherResultsAndFiles; they are
// redeclared (not imported) to keep domain/instance free of domain/workflow,
// the same one-directional edge Visibility keeps.
const (
	GatherModeResults         = "results"
	GatherModeResultsAndFiles = "results_and_files"
)

// GatherReport.State vocabulary.
const (
	GatherStatePending       = "pending"        // file gather prepared, no merge attempted yet
	GatherStateMerging       = "merging"        // merging eligible task refs in author order
	GatherStateResolving     = "resolving"      // waiting on an interactive conflict resolution
	GatherStateApplied       = "applied"        // staged result applied to the parent's area
	GatherStateFailed        = "failed"         // gather failed; see Error / RollbackStatus
	GatherStateRolledBack    = "rolled_back"    // gather aborted and the parent's area restored
	GatherStateNotApplicable = "not_applicable" // nothing to gather (results mode, failed decision)
)

// GatherReport.RollbackStatus vocabulary ("" = no rollback was needed).
const (
	RollbackStatusRolledBack = "rolled_back"
	RollbackStatusFailed     = "rollback_failed"
)

// ResolutionAttempt.Outcome vocabulary ("" while the attempt is open, or ended
// by an ordinary session end and awaiting the gatherer's verification).
const (
	ResolutionOutcomeResolved        = "resolved"
	ResolutionOutcomeStillConflicted = "still_conflicted"
	ResolutionOutcomeAbandoned       = "abandoned"
	ResolutionOutcomeCancelled       = "cancelled"
	ResolutionOutcomeError           = "error"
)

// ErrInvalidGatherReport is returned (wrapped, with every problem listed) by
// GatherReport.Validate and therefore by GroupRunRepository.SetGatherReport.
var ErrInvalidGatherReport = errors.New("invalid gather report")

// ErrGatherReportFrozen is returned by GroupRunRepository.SetGatherReport once
// the group run is done: the final gathered output, including its gather
// report, is immutable after the first continuation is prepared.
var ErrGatherReportFrozen = errors.New("gather report is frozen: the group run is done")

// GatherReport is the durable record of a group's gather phase. It is the
// crash-recovery state of the gatherer (Handle, MergeIndex, ActiveSession)
// and, once the run is done, the immutable merge/resolution report exposed in
// the group output and in run inspection.
type GatherReport struct {
	Mode  string `json:"mode"`  // "results" | "results_and_files"
	State string `json:"state"` // pending|merging|resolving|applied|failed|rolled_back|not_applicable
	// Handle is the working-area gather handle (engine.GatherHandle as a JSON
	// object) so a restarted gatherer resumes the same staging area.
	Handle         map[string]any        `json:"handle,omitempty"`
	MergeIndex     int                   `json:"mergeIndex"` // index into Tasks of the next/current merge
	Tasks          []GatherTaskReport    `json:"tasks"`
	Attempts       []ResolutionAttempt   `json:"attempts"`
	ActiveSession  *ResolutionSessionRef `json:"activeSession,omitempty"`
	RollbackStatus string                `json:"rollbackStatus,omitempty"` // "", "rolled_back", "rollback_failed"
	Error          string                `json:"error,omitempty"`
}

// GatherTaskReport is one task's line in the report, in author order.
type GatherTaskReport struct {
	Key           string   `json:"key"`
	Merged        bool     `json:"merged"`
	Reason        string   `json:"reason"`
	ConflictPaths []string `json:"conflictPaths,omitempty"`
}

// ResolutionAttempt records one interactive resolution session.
type ResolutionAttempt struct {
	MergeIndex    int        `json:"mergeIndex"`
	Attempt       int        `json:"attempt"`
	TaskKey       string     `json:"taskKey"`
	ConflictPaths []string   `json:"conflictPaths"`
	Outcome       string     `json:"outcome"` // resolved|still_conflicted|abandoned|cancelled|error ("" while open or awaiting verification)
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
}

// ResolutionSessionRef identifies the open resolution session, if any.
type ResolutionSessionRef struct {
	GroupRunID uuid.UUID `json:"groupRunId"`
	MergeIndex int       `json:"mergeIndex"`
	Attempt    int       `json:"attempt"`
	InstanceID uuid.UUID `json:"instanceId"`
	NodeID     string    `json:"nodeId"`
}

// Validate checks the report against its vocabulary and invariants and
// returns an error wrapping ErrInvalidGatherReport listing every problem.
func (r GatherReport) Validate() error {
	var problems []string
	switch r.Mode {
	case GatherModeResults, GatherModeResultsAndFiles:
	default:
		problems = append(problems, fmt.Sprintf("mode %q is not results or results_and_files", r.Mode))
	}
	switch r.State {
	case GatherStatePending, GatherStateMerging, GatherStateResolving, GatherStateApplied,
		GatherStateFailed, GatherStateRolledBack, GatherStateNotApplicable:
	default:
		problems = append(problems, fmt.Sprintf("state %q is unknown", r.State))
	}
	if r.MergeIndex < 0 {
		problems = append(problems, "mergeIndex is negative")
	}
	switch r.RollbackStatus {
	case "", RollbackStatusRolledBack, RollbackStatusFailed:
	default:
		problems = append(problems, fmt.Sprintf("rollbackStatus %q is unknown", r.RollbackStatus))
	}
	seen := map[string]bool{}
	for i, t := range r.Tasks {
		if t.Key == "" {
			problems = append(problems, fmt.Sprintf("tasks[%d] has no key", i))
		} else if seen[t.Key] {
			problems = append(problems, fmt.Sprintf("task %q appears twice", t.Key))
		}
		seen[t.Key] = true
	}
	for i, a := range r.Attempts {
		if a.TaskKey == "" {
			problems = append(problems, fmt.Sprintf("attempts[%d] has no taskKey", i))
		}
		if a.Attempt < 1 {
			problems = append(problems, fmt.Sprintf("attempts[%d].attempt must be >= 1", i))
		}
		if a.MergeIndex < 0 {
			problems = append(problems, fmt.Sprintf("attempts[%d].mergeIndex is negative", i))
		}
		switch a.Outcome {
		case "":
			// Open, or ended and awaiting the gatherer's verification: an
			// ordinary session end (P3-T8 OnResolutionSessionEnded) records
			// EndedAt and leaves the outcome to the gatherer's conflict check
			// (P3-T6), which then writes resolved / still_conflicted.
		case ResolutionOutcomeResolved, ResolutionOutcomeStillConflicted, ResolutionOutcomeAbandoned,
			ResolutionOutcomeCancelled, ResolutionOutcomeError:
		default:
			problems = append(problems, fmt.Sprintf("attempts[%d].outcome %q is unknown", i, a.Outcome))
		}
	}
	if s := r.ActiveSession; s != nil {
		if s.GroupRunID == uuid.Nil || s.InstanceID == uuid.Nil || s.NodeID == "" || s.Attempt < 1 || s.MergeIndex < 0 {
			problems = append(problems, "activeSession needs groupRunId, instanceId, nodeId, attempt >= 1 and mergeIndex >= 0")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidGatherReport, strings.Join(problems, "; "))
	}
	return nil
}

// Normalized returns a deep copy whose nil slices are empty, so the stored
// JSON always carries "tasks":[] and "attempts":[] (never null).
func (r GatherReport) Normalized() GatherReport {
	out := r.Clone()
	if out.Tasks == nil {
		out.Tasks = []GatherTaskReport{}
	}
	if out.Attempts == nil {
		out.Attempts = []ResolutionAttempt{}
	}
	for i := range out.Attempts {
		if out.Attempts[i].ConflictPaths == nil {
			out.Attempts[i].ConflictPaths = []string{}
		}
	}
	return out
}

// Clone returns a deep copy sharing no maps, slices or pointers with r.
func (r GatherReport) Clone() GatherReport {
	out := r
	if r.Handle != nil {
		raw, err := json.Marshal(r.Handle)
		if err == nil {
			var h map[string]any
			if json.Unmarshal(raw, &h) == nil {
				out.Handle = h
			}
		}
	}
	if r.Tasks != nil {
		out.Tasks = make([]GatherTaskReport, len(r.Tasks))
		for i, t := range r.Tasks {
			t.ConflictPaths = cloneStrings(t.ConflictPaths)
			out.Tasks[i] = t
		}
	}
	if r.Attempts != nil {
		out.Attempts = make([]ResolutionAttempt, len(r.Attempts))
		for i, a := range r.Attempts {
			a.ConflictPaths = cloneStrings(a.ConflictPaths)
			if a.EndedAt != nil {
				ended := *a.EndedAt
				a.EndedAt = &ended
			}
			out.Attempts[i] = a
		}
	}
	if r.ActiveSession != nil {
		s := *r.ActiveSession
		out.ActiveSession = &s
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
```

- [x] Step 4: Run them; confirm they pass.

```bash
go test ./domain/instance/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/instance	0.XXXs
```

#### Cycle 2 — port extension, SQLite storage, fakes

- [x] Step 5: Write the failing repository tests. Create `adapter/sqlite/group_run_gather_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatherRepoFixture persists a parent instance and one two-task group run at
// node "grp" through the real repositories.
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
		ID:                 runID,
		ParentInstanceID:   parentID,
		NodeID:             nodeID,
		Attempt:            1,
		Form:               "branches",
		CompletionRule:     "all_succeed",
		UnfinishedBehavior: "wait_for_remaining",
		GatherMode:         instance.GatherModeResultsAndFiles,
		Config:             map[string]any{"form": "branches"},
		BodyOwnership:      map[string][]string{"a": {"a1"}, "b": {"b1"}},
		StartVariables:     map[string]any{"x": "y"},
		WorkspaceSnapshot:  runID.String(),
		State:              instance.GroupRunLaunching,
		Tasks: []instance.GroupTask{
			{GroupRunID: runID, Position: 0, Key: "a", Kind: instance.GroupTaskKindBranch, Origin: "a1",
				ChildInstanceID: uuid.New(), LaunchState: instance.GroupTaskPending, LiveStatus: instance.GroupTaskStatusNotStarted, Blocking: true},
			{GroupRunID: runID, Position: 1, Key: "b", Kind: instance.GroupTaskKindBranch, Origin: "b1",
				ChildInstanceID: uuid.New(), LaunchState: instance.GroupTaskPending, LiveStatus: instance.GroupTaskStatusNotStarted, Blocking: true},
		},
	}
}

func sampleGatherReport(runID, parentID uuid.UUID) instance.GatherReport {
	started := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	return instance.GatherReport{
		Mode:       instance.GatherModeResultsAndFiles,
		State:      instance.GatherStateResolving,
		Handle:     map[string]any{"snapshotHandle": runID.String(), "gatherId": uuid.New().String()},
		MergeIndex: 1,
		Tasks: []instance.GatherTaskReport{
			{Key: "a", Merged: true, Reason: "merged cleanly"},
			{Key: "b", Merged: false, Reason: "conflicts", ConflictPaths: []string{"c.txt"}},
		},
		Attempts: []instance.ResolutionAttempt{
			{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"c.txt"}, StartedAt: started},
		},
		ActiveSession: &instance.ResolutionSessionRef{GroupRunID: runID, MergeIndex: 1, Attempt: 1, InstanceID: parentID, NodeID: "grp"},
	}
}

func TestMigration020_AddsGatherColumnsOnce(t *testing.T) {
	db := setupTestDB(t)
	count := func(table, col string) int {
		var n int
		require.NoError(t, db.DB.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n))
		return n
	}
	assert.Equal(t, 1, count("group_runs", "gather_json"))
	assert.Equal(t, 1, count("group_tasks", "finalized_ref"))
	require.NoError(t, RunMigrations(db.DB), "re-running migrations is a no-op")
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
		require.NotNil(t, got.Gather, name)
		assert.Equal(t, want.Normalized(), *got.Gather, name)
	}
}

func TestGroupRunRepo_SetGatherReport_ReplacesPreviousReport(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	first := sampleGatherReport(f.run.ID, f.parent.ID)
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, first))
	second := first.Clone()
	second.State = instance.GatherStateApplied
	second.MergeIndex = 2
	second.ActiveSession = nil
	ended := first.Attempts[0].StartedAt.Add(time.Minute)
	second.Attempts[0].Outcome = instance.ResolutionOutcomeResolved
	second.Attempts[0].EndedAt = &ended
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, second))

	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, second.Normalized(), *got.Gather)
}

func TestGroupRunRepo_SetGatherReport_NormalizesNilLists(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID,
		instance.GatherReport{Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStatePending}))
	var raw string
	require.NoError(t, f.repo.db.QueryRow(`SELECT gather_json FROM group_runs WHERE id = ?`, f.run.ID.String()).Scan(&raw))
	assert.Contains(t, raw, `"tasks":[]`)
	assert.Contains(t, raw, `"attempts":[]`)
}

func TestGroupRunRepo_SetGatherReport_Errors(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()

	err := f.repo.SetGatherReport(ctx, uuid.New(), sampleGatherReport(f.run.ID, f.parent.ID))
	require.ErrorIs(t, err, instance.ErrGroupRunNotFound)

	bad := sampleGatherReport(f.run.ID, f.parent.ID)
	bad.State = "sideways"
	require.ErrorIs(t, f.repo.SetGatherReport(ctx, f.run.ID, bad), instance.ErrInvalidGatherReport)
	got, err := f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Gather, "an invalid report is never stored")
}

func TestGroupRunRepo_SetGatherReport_FrozenOnceDone(t *testing.T) {
	f := newGatherRepoFixture(t)
	ctx := context.Background()
	final := sampleGatherReport(f.run.ID, f.parent.ID)
	final.State = instance.GatherStateApplied
	final.ActiveSession = nil
	require.NoError(t, f.repo.SetGatherReport(ctx, f.run.ID, final))

	ok, err := f.repo.TransitionState(ctx, f.run.ID, instance.GroupRunLaunching, instance.GroupRunRunning)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = f.repo.Decide(ctx, f.run.ID, instance.GroupDecision{Outcome: "success", Rule: "all_succeed",
		Behavior: "wait_for_remaining", Reason: "all tasks completed", DecidedAt: time.Now().UTC()})
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
	assert.Equal(t, final.Normalized(), *got.Gather, "the final report is immutable")
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
	assert.Empty(t, got.Tasks[0].FinalizedRef, "only the addressed task changes")
	assert.Equal(t, ref, got.Tasks[1].FinalizedRef)

	require.NoError(t, f.repo.SetTaskFinalizedRef(ctx, f.run.ID, "b", ref+"-again"))
	got, err = f.repo.Get(ctx, f.run.ID)
	require.NoError(t, err)
	assert.Equal(t, ref+"-again", got.Tasks[1].FinalizedRef, "re-finalizing replaces the ref")
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
	require.NoError(t, f.repo.SetGatherReport(ctx, second.ID,
		instance.GatherReport{Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStatePending}))
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

func TestGroupRunRepo_SatisfiesThePort(t *testing.T) {
	var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)
}
```

- [x] Step 6: Extend the port. In `domain/instance/group_run.go` (created by P2-T1):
  1. In `type GroupRun struct`, directly after the `LaunchError string` field, add:
     ```go
     	// Gather is the durable file-gather state and, once the run is done, its
     	// immutable merge/resolution report (P3-T4). nil until first written.
     	Gather *GatherReport
     ```
  2. In `type GroupRunRepository interface`, directly after `MarkContinuationDelivered(ctx context.Context, runID uuid.UUID) error`, add:
     ```go
     	// SetGatherReport replaces the run's gather report (validated; never-null
     	// lists). ErrGroupRunNotFound for an unknown run, ErrGatherReportFrozen once
     	// the run is done, ErrInvalidGatherReport for a malformed report.
     	SetGatherReport(ctx context.Context, runID uuid.UUID, report GatherReport) error
     	// SetTaskFinalizedRef records the immutable working-area ref of the task
     	// (runID, key). Replaces an earlier ref. ErrGroupRunNotFound for an unknown
     	// run or key.
     	SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error
     ```

- [x] Step 7: Run the repository tests; confirm they fail to compile.

Confirmed missing SetGatherReport / SetTaskFinalizedRef and the port conformance compile failure before implementation.

```bash
go test ./adapter/sqlite/ -count=1 -run 'GroupRunRepo|Migration020'
```
Expected (the first lines; if P2-T3 declared `var _ instance.GroupRunRepository = (*GroupRunRepo)(nil)` the first line instead reads `cannot use (*GroupRunRepo)(nil) … does not implement instance.GroupRunRepository (missing method SetGatherReport)`):
```
# github.com/arbitron/arbitron/adapter/sqlite [github.com/arbitron/arbitron/adapter/sqlite.test]
adapter/sqlite/group_run_gather_test.go:106:28: f.repo.SetGatherReport undefined (type *GroupRunRepo has no field or method SetGatherReport)
adapter/sqlite/group_run_gather_test.go:128:28: f.repo.SetGatherReport undefined (type *GroupRunRepo has no field or method SetGatherReport)
...
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] Step 8: Add migration 020. Create `adapter/sqlite/migrations/020_group_gather.up.sql`:

```sql
-- Parallel workflow gathering Phase 3: durable file-gather state.
--
-- group_runs.gather_json: the JSON-encoded instance.GatherReport — the
--   gatherer's crash-recovery state (working-area handle, merge index, open
--   resolution session) and, once the run is done, the immutable
--   merge/resolution report. NULL until a gather report is first written
--   (every results-only run before Phase 3, and every run until its gather
--   begins).
-- group_tasks.finalized_ref: the immutable working-area ref (for the git
--   adapter "refs/arbitron/tasks/<task instance id>") recording the task's
--   final files. Written by the terminal finalizer BEFORE the task completes;
--   NULL for results-only tasks, failed/cancelled tasks and tasks that
--   finished after the group decided.
--
-- Both columns are additive and nullable, so every existing row reads back
-- exactly as before.
ALTER TABLE group_runs ADD COLUMN gather_json TEXT;
ALTER TABLE group_tasks ADD COLUMN finalized_ref TEXT;
```

Create `adapter/sqlite/migrations/020_group_gather.down.sql`:

```sql
-- SQLite cannot DROP COLUMN portably on the older versions some deployments
-- run, and a table rebuild is out of scope for a down step. gather_json and
-- finalized_ref are nullable and ignored by pre-Phase-3 code, so leaving them
-- in place is harmless. Forward-only column removal is the project convention
-- — see the 012 / 014 / 017 / 018 down migrations and migrations/README.md.
SELECT 1;
```

- [x] Step 9: Implement the repository methods. Create `adapter/sqlite/group_run_gather.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// setGatherReportSQL stores the report unless the run is already done: the
// final gathered output, report included, is immutable after continuation is
// prepared (SetFinalOutput moves the run to 'done').
// updated_at is written by the repository in P2-T3's fixed-width
// groupTimeLayout (never strftime), so lexical order stays chronological.
const setGatherReportSQL = `UPDATE group_runs
SET gather_json = ?, updated_at = ?
WHERE id = ? AND state != 'done'`

const setTaskFinalizedRefSQL = `UPDATE group_tasks SET finalized_ref = ?
WHERE group_run_id = ? AND task_key = ?`

const selectGatherJSONSQL = `SELECT gather_json FROM group_runs WHERE id = ?`

const selectFinalizedRefsSQL = `SELECT task_key, finalized_ref FROM group_tasks
WHERE group_run_id = ? AND finalized_ref IS NOT NULL`

// SetGatherReport implements instance.GroupRunRepository. The report is
// validated, normalized (never-null lists) and replaces any previous report.
// Unknown run => ErrGroupRunNotFound; a done run => ErrGatherReportFrozen.
func (r *GroupRunRepo) SetGatherReport(ctx context.Context, runID uuid.UUID, report instance.GatherReport) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(report.Normalized())
	if err != nil {
		return fmt.Errorf("marshalling gather report: %w", err)
	}
	res, err := r.db.ExecContext(ctx, setGatherReportSQL, string(raw), groupTime(r.nowUTC()), uuidToString(runID))
	if err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	if n == 1 {
		return nil
	}
	exists, err := r.groupRunExists(ctx, runID)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
	}
	return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
}

// SetTaskFinalizedRef implements instance.GroupRunRepository: it records the
// task's finalized working-area ref (addressed by run id AND task key).
// Re-finalizing replaces the ref. Unknown run or key => ErrGroupRunNotFound.
func (r *GroupRunRepo) SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	if key == "" || ref == "" {
		return errors.New("set task finalized ref: task key and ref are required")
	}
	res, err := r.db.ExecContext(ctx, setTaskFinalizedRefSQL, ref, uuidToString(runID), key)
	if err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupRunNotFound)
	}
	return nil
}

// hydrateGather fills run.Gather and every task's FinalizedRef from the
// Phase 3 columns. Every read path (Get, GetByParentNode, GetByChild through
// loadGroupRun; ListByParent directly) calls it with the read's own *sql.Tx,
// so the gather state belongs to the same snapshot as the run and no reader can
// observe a run without it. It must never use r.db while a transaction is open:
// the database has a single connection (SetMaxOpenConns(1)).
func hydrateGather(ctx context.Context, q groupQueryer, run *instance.GroupRun) error {
	if run == nil {
		return nil
	}
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, selectGatherJSONSQL, uuidToString(run.ID)).Scan(&raw); err != nil {
		return fmt.Errorf("load gather report for %s: %w", run.ID, err)
	}
	run.Gather = nil
	if raw.Valid && raw.String != "" {
		var rep instance.GatherReport
		if err := json.Unmarshal([]byte(raw.String), &rep); err != nil {
			return fmt.Errorf("decode gather report for %s: %w", run.ID, err)
		}
		run.Gather = &rep
	}
	rows, err := q.QueryContext(ctx, selectFinalizedRefsSQL, uuidToString(run.ID))
	if err != nil {
		return fmt.Errorf("load finalized refs for %s: %w", run.ID, err)
	}
	defer rows.Close()
	refs := map[string]string{}
	for rows.Next() {
		var key, ref string
		if err := rows.Scan(&key, &ref); err != nil {
			return fmt.Errorf("scan finalized ref for %s: %w", run.ID, err)
		}
		refs[key] = ref
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load finalized refs for %s: %w", run.ID, err)
	}
	for i := range run.Tasks {
		run.Tasks[i].FinalizedRef = refs[run.Tasks[i].Key]
	}
	return nil
}

func (r *GroupRunRepo) groupRunExists(ctx context.Context, runID uuid.UUID) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM group_runs WHERE id = ?`, uuidToString(runID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check group run %s: %w", runID, err)
	}
	return true, nil
}
```

- [x] Step 10: Hydrate every read path, on the read's own transaction. In `adapter/sqlite/group_run_repo.go` (P2-T3):
  - `loadGroupRun` (serves `Get`, `GetByParentNode`, `GetByChild`; called with the `*sql.Tx` of each method's `withTx`) — replace its last two lines
    ```go
    	run.Tasks = tasks
    	return &run, nil
    ```
    with
    ```go
    	run.Tasks = tasks
    	if err := hydrateGather(ctx, q, &run); err != nil {
    		return nil, err
    	}
    	return &run, nil
    ```
    (the `(nil, nil)` "no such run" return earlier in `loadGroupRun` is untouched).
  - `ListByParent` — inside its `withTx` closure, replace
    ```go
    		for i := range runs {
    			runs[i].Tasks = byRun[runs[i].ID]
    		}
    		out = runs
    ```
    with
    ```go
    		for i := range runs {
    			runs[i].Tasks = byRun[runs[i].ID]
    			if err := hydrateGather(ctx, tx, &runs[i]); err != nil {
    				return fmt.Errorf("listing group runs of %s: %w", parentID, err)
    			}
    		}
    		out = runs
    ```
  Never call `hydrateGather(ctx, r.db, …)` from inside a `withTx` closure: with `SetMaxOpenConns(1)` that blocks forever. `TestGroupRunRepo_SetGatherReport_RoundTripsThroughEveryReadPath` and `TestGroupRunRepo_ListByParent_HydratesEveryRun` fail if either call is missing.

- [x] Step 11: Update every fake. Find all implementations:

Search found only GroupRunRepo and memGroupRuns; both now implement the extended port, including deep report copies.

```bash
grep -rln "func (.*) MarkContinuationDelivered(" --include=*.go .
```
Expected hits: `adapter/sqlite/group_run_repo.go` (done above) and `domain/engine/group_fakes_test.go` (P2-T8's `memGroupRuns`); any further hit (a fake added by another task) gets the same two methods with the same semantics, storing the report on the run keyed by `runID` and the ref on the task keyed by `(runID, key)`.

In `domain/engine/group_fakes_test.go`, inside `copyGroupRun` immediately before its `sort.SliceStable(cp.Tasks, …)` line, add:
```go
	if r.Gather != nil {
		g := r.Gather.Clone()
		cp.Gather = &g
	}
```
and append these methods after `func (m *memGroupRuns) MarkContinuationDelivered`:
```go
func (m *memGroupRuns) SetGatherReport(_ context.Context, runID uuid.UUID, report instance.GatherReport) error {
	// No call detail: P3-T8 asserts callCount("SetGatherReport") (exact entry match).
	if err := m.begin("SetGatherReport", ""); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	if run.State == instance.GroupRunDone {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
	}
	rep := report.Normalized()
	run.Gather = &rep
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) SetTaskFinalizedRef(_ context.Context, runID uuid.UUID, key, ref string) error {
	if err := m.begin("SetTaskFinalizedRef", key); err != nil {
		return err
	}
	if key == "" || ref == "" {
		return fmt.Errorf("set task finalized ref: task key and ref are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.runLocked(runID); err != nil {
		return err
	}
	_, task, err := m.taskLocked(runID, key)
	if err != nil {
		// Same contract as the SQLite repo: an unknown key is ErrGroupRunNotFound
		// (P2-T8's taskLocked returns an unwrapped error for a missing key).
		return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupRunNotFound)
	}
	task.FinalizedRef = ref
	return nil
}
```
(`GroupTask.FinalizedRef` is a plain string, so the existing `copyGroupRun` task loop already copies it.)

- [x] Step 12: Run everything touched, then the whole module build, gofmt and vet.

Completion pass after P3-T11 (`62faa02`): the production-producer/wiring blocker is resolved. `initDomain` constructs `GroupFileFinalizer` and `FileGatherer` with the same real SQLite `groupRunRepo` and Git WorkingArea, installing them before executor freeze. `GroupFileFinalizer.FinalizeTerminal` persists refs from `WorkingArea.FinalizeTask`; `FileGatherer.persist` writes actual merge reports. `TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder` passes with `-race`, asserting nonempty persisted refs/report and downstream parent variables containing the real gathered task files. No new implementation was needed; the earlier strict-TDD cycles remain recorded above.

Completion self-review, in the required order:
1. Spec compliance: reviewed the domain vocabulary/JSON, frozen-report guard, run-and-key addressed ref replacement, migration ownership, transaction-local hydration and repository implementations against this task. The later notification migration belongs to P3-T8; no extra feature or production change was added here.
2. Anti-green-theater: temporarily removed `loadGroupRun`'s hydration call. The real-root test failed: the parent routed failure rather than success and the downstream `check` output was absent. Restored hydration; the same test passes with `-race`. The existing storage tests additionally assert nonempty reports/refs through all four read paths and detect ignored run/key arguments. No mutation remains.
3. Code quality: reviewed neighboring repository/finalizer/gatherer conventions, preserving existing timestamps, deep-copy helpers and read transactions. No cleanup was necessary. After review, reran all specified package tests, module build, gofmt and vet, plus focused instance/SQLite race tests and the real-root race test: all pass; `git diff --check` is clean.

Additional completion verification:
```bash
go test ./domain/instance/ ./adapter/sqlite/ -race -count=1 -run 'GatherReport|GroupRunRepo|Migration020'
go test ./cmd/arbitron/ -race -count=1 -v -run '^TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder$'
```

> RESOLVED (P3-T11); historical blocker: Storage verification below is green, but the mission's wired-and-fed completion gate requires P3-T5's GroupFileFinalizer and P3-T6's FileGatherer to produce real finalized refs/reports, plus P3-T11's initDomain wiring/sink test. Neither producer exists yet: production has no SetGatherReport or SetTaskFinalizedRef call sites; cmd/arbitron/main.go:989-1002 still wires the results-only filesystem workspace/coordinator/executor. Migration 020 and read hydration use the existing production repository, but test-supplied reports/refs do not prove the runtime sink. Keep this completion gate unchecked until those planned dependencies land; do not re-fail it on intervening passes. The next independent task is P3-T5.

Verification this pass: both strict-TDD compile failures were confirmed before implementation; all planned domain and repository tests now pass. The full instance/SQLite/engine package tests, module build, formatting, vet and diff checks pass. Focused instance/SQLite gather/repository tests and engine launcher/coordinator/executor tests also pass with the race detector. Storage error coverage includes an additive upgrade of a populated roster, injected SQL aborts preserving previous report/ref/timestamp, marshal errors, cancelled contexts, unavailable databases and corrupt JSON read failures without transaction leaks.

Self-review, in required order:
1. Spec compliance: matched the exact report vocabulary/JSON, normalization, frozen-after-done guard, run-and-key addressed ref replacement, both transaction-local hydration seams and every repository implementation. No notification rebuild or future gatherer behavior was added. A red/green Clone regression exposed JSON coercing integer/typed-slice handle values; Clone now reuses the existing variable.DeepCopyMap primitive.
2. Anti-green-theater: strengthened the nonempty report sink test to assert task refs through all four real SQLite read paths, while multi-run tests detect ignoring runID/key. Temporarily removed both production hydration calls: round-trip and multi-run tests failed on absent persisted reports. Restored both calls; no mutation remains. The missing runtime producers/wiring are explicitly blocked above, not replaced by test fixtures or an unsupported-mode bypass.
3. Code quality: reviewed neighboring repository/fake/deep-copy conventions; retained the existing fixed-width timestamps and read transactions, reused deep-copy code, and formatted/vetted changes. All task/package/race checks rerun after review pass.

```bash
go test ./domain/instance/ ./adapter/sqlite/ ./domain/engine/ -count=1
go build ./...
gofmt -l domain/instance/ adapter/sqlite/ domain/engine/
go vet ./domain/instance/ ./adapter/sqlite/ ./domain/engine/
```
Expected (`gofmt -l` and `go vet` print nothing):
```
ok  	github.com/arbitron/arbitron/domain/instance	0.XXXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	X.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
and the focused run lists every new test as passing:
```bash
go test ./adapter/sqlite/ -count=1 -v -run 'GroupRunRepo|Migration020' 2>&1 | grep -E '^(--- |ok)'
```
```
--- PASS: TestMigration020_AddsGatherColumnsOnce (0.0Xs)
--- PASS: TestGroupRunRepo_FreshRunHasNoGatherState (0.0Xs)
--- PASS: TestGroupRunRepo_SetGatherReport_RoundTripsThroughEveryReadPath (0.0Xs)
--- PASS: TestGroupRunRepo_SetGatherReport_ReplacesPreviousReport (0.0Xs)
--- PASS: TestGroupRunRepo_SetGatherReport_NormalizesNilLists (0.0Xs)
--- PASS: TestGroupRunRepo_SetGatherReport_Errors (0.0Xs)
--- PASS: TestGroupRunRepo_SetGatherReport_FrozenOnceDone (0.0Xs)
--- PASS: TestGroupRunRepo_SetTaskFinalizedRef_IsKeyAddressed (0.0Xs)
--- PASS: TestGroupRunRepo_SetTaskFinalizedRef_Errors (0.0Xs)
--- PASS: TestGroupRunRepo_ListByParent_HydratesEveryRun (0.0Xs)
--- PASS: TestGroupRunRepo_SatisfiesThePort (0.00s)
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.XXXs
```

- [x] Step 13: Commit.

Initial implementation commit: committed the unblocked types, storage, migration, fake updates and regression tests with the verification/self-review evidence above, leaving Step 12 open for production producers/wiring. This completion pass closes Step 12 with the now-green production-root sink proof and records verification in a follow-up commit without changing production behavior.

```bash
git add domain/instance/group_gather.go domain/instance/group_gather_test.go domain/instance/group_run.go \
  adapter/sqlite/migrations/020_group_gather.up.sql adapter/sqlite/migrations/020_group_gather.down.sql \
  adapter/sqlite/group_run_gather.go adapter/sqlite/group_run_gather_test.go adapter/sqlite/group_run_repo.go \
  domain/engine/group_fakes_test.go
git commit -m "feat(groups): durable gather report and finalized task refs (migration 020)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
(Add any further fake file changed in Step 11 to the `git add` list.)
