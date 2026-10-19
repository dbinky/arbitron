# Parallel Workflow Gathering — Phase 2 Task 2: Migration 019 and group-task link persistence

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Add migration `019_parallel_groups` (the three `instances` group-link columns plus the `group_runs` / `group_tasks` tables) and make `InstanceRepo` persist and read back a parallel_group task child's link — `spawn_mode` NULL + `group_run_id` / `group_task_key` / `group_task_kind` — as `SpawnMode = SpawnModeGroupTask`, on every full-row read path.
**Architecture:** Additive SQLite migration (ALTER TABLE ADD COLUMN only on `instances` — never a rebuild — plus two new tables with schema-enforced invariants). The hand-written `createInstanceSQL` gains three columns; the shared `instanceColumnsWithLink` / `scanInstanceWithLink` read path gains a group-link decoder (`nullStringToGroupLink`) with the same ALL-OR-NONE discipline as the 018 spawn link. The four generated full-row projections that cannot see the new columns (`ListRunning`, `NextEligible`, `ListWaitingWithTimer`, `ListWaitingWithWebhook`) are replaced by hand-written statements over `instanceColumnsWithLink`, so no read path reports a group task as a root. Legacy spawn queries keep filtering on `spawn_mode` and therefore never match a group task; the cancel cascade, orphan sweep, child counts and roots filter key on `parent_instance_id` and therefore DO cover group tasks unchanged.
**Tech Stack:** Go 1.25.5, `database/sql`, `modernc.org/sqlite` (SQLite ≥ 3.37: a column CHECK added by `ADD COLUMN` is evaluated against existing rows and may reference other columns), testify. `sqlc` is not installed: all SQL is hand-written; `adapter/sqlite/generated/` is not edited.
**Spec / Design:** `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §1 (durable group-run record; "Children carry group/task linkage and a branch-kind marker in addition to existing spawn ancestry") and §2 last bullet ("Extend `SpawnMode.Valid`, link persistence and its SQLite CHECK to admit internal `group_task`"); contract "Persistence of group-task links (P2-T2)".

## Atomic-terminal dependency

P2-T16 is the owner of production terminal capture: it extends migration 019 with a
single durable clock and terminal-state triggers and adapts `RecordTaskTerminal` to the
same allocator. Finish it before P2-T10/T13/T14. The callback is a wake accelerator;
wall-clock timestamp sorting and callback receipt order cannot determine first success.
No extra public field or port is introduced. P3-T5 still finalizes file refs BEFORE the
successful terminal write, so the atomic receipt cannot advertise success without files.


**Interfaces**
- Consumes:
  - P2-T1 (`domain/instance/group_run.go`, `spawn_link.go`, `instance.go`): `instance.SpawnModeGroupTask`, `instance.GroupTaskKind` + `GroupTaskKindBranch/Workflow` + `Valid()`, `Instance.GroupRunID *uuid.UUID`, `Instance.GroupTaskKey *string`, `Instance.GroupTaskKind`, `Instance.IsGroupTask()`, `SpawnMode.Valid()` (admits `group_task`).
  - Existing: `adapter/sqlite/instance_repo.go:31-49` `createInstanceSQL`, `:51-134` `(*InstanceRepo).Create`, `:136-147` `Get`, `:370-385` `ListRunning`, `:659-687` `ListActiveChildren`, `:701-730` `ListOrphanedActiveChildren`, `:746-765` `FindChildBySpawnNode`, `:777-790` `ClaimWaitingParent`, `:803-844` `ListWaitingOnTerminalChild`, `:849-944` `rowToInstance`; `adapter/sqlite/instance_spawn_rows.go:11-24` `instanceColumnsWithLink`, `:35-37` `rowScanner`, `:41-94` `scanInstanceWithLink`; `adapter/sqlite/convert.go:245-257` `nullStringToSpawnLink`, `:263-272` `nullStringToParentID`, `:55-80` string/NullString helpers; `adapter/sqlite/queue_repo.go:61-74` `NextEligible`, `:109-139` `ListWaitingWithTimer/Webhook`; `adapter/sqlite/migrate.go:16-79` `RunMigrations` (embeds `migrations/*.sql`, one transaction per file); test helpers `setupTestDB` (`integration_test.go:20`), `newLinkedInstance` (`instance_spawn_link_test.go:17`), `childOf` (`instance_spawn_queries_test.go:17`), `waitingParent` (`instance_spawn_wait_test.go:17`), `writeRawLink` (`instance_spawn_link_integrity_test.go:21`), `assertSpawnLink` (`instance_repo_stale_queries_test.go:132`), `idsOf` (`instance_repo_parent_scope_test.go:36`), `spawnTestOwner` (`instance_spawn_link_test.go:13`).
- Produces:
  - Migration `adapter/sqlite/migrations/019_parallel_groups.{up,down}.sql`: `instances.group_run_id TEXT` (CHECK: only with a complete parent link, a task key, a kind, and `spawn_mode IS NULL`), `instances.group_task_key TEXT`, `instances.group_task_kind TEXT CHECK (IS NULL OR IN ('branch','workflow'))`, index `idx_instances_group_run`, `instances.subtree_cancel_requested_at TEXT` (nullable; P2-T12's durable subtree-cancellation intent on a terminal run); tables `group_runs` (UNIQUE `(parent_instance_id, node_id, attempt)`, state CHECK incl. `gathering` / `resolving_conflicts`, partial index `idx_group_runs_unsettled`) and `group_tasks` (PK `(group_run_id, position)`, UNIQUE `(group_run_id, task_key)`, UNIQUE `child_instance_id`, global UNIQUE index `idx_group_tasks_terminal_seq`). Consumed by P2-T3 (`GroupRunRepo`). Phase 3 migration 020 adds `group_tasks.finalized_ref` and the gather-report column — they are deliberately absent here.
  - `InstanceRepo.Create` writes a complete group-task link; `Get`, `ListRunning`, `QueueRepo.NextEligible`, `ListWaitingWithTimer`, `ListWaitingWithWebhook` read it back as `SpawnMode == SpawnModeGroupTask` with the three group fields (and now also carry `SpawnDepth`).
  - Unexported helpers (package `sqlite`): `func groupLinkComplete(inst *instance.Instance) bool`, `type groupTaskLink struct`, `func nullStringToGroupLink(parentRaw, nodeRaw, modeRaw, runRaw, keyRaw, kindRaw sql.NullString) (*groupTaskLink, error)`, `func queryInstancesWithLink(ctx context.Context, db *sql.DB, q string, args ...any) ([]instance.Instance, error)`, consts `listRunningWithLinkSQL`, `nextEligibleWithLinkSQL`, `listWaitingWithTimerWithLinkSQL`, `listWaitingWithWebhookWithLinkSQL`.
  - Behavioural contract later tasks rely on: `FindChildBySpawnNode` (any mode) and `ListWaitingOnTerminalChild` NEVER return a group task; `ListActiveChildren`, `ListOrphanedActiveChildren`, `ChildCountsByParents`, `List` with `ParentScopeChildren` DO include group tasks; `ParentScopeRoots` never does. An incomplete group link writes NO link (the row is a root) instead of failing the INSERT.

**Wiring & data provenance**
- Composition root: no new wiring line. Migration 019 is embedded by `//go:embed migrations/*.sql` (`adapter/sqlite/migrate.go:11`) and applied by `adaptsqlite.RunMigrations(db)` in `cmd/arbitron/main.go:365`; the repos are the existing `adaptsqlite.NewInstanceRepo(db)` (`main.go:373`) and `adaptsqlite.NewQueueRepo(db)` (`main.go:797`). P2-T14's end-to-end test (`cmd/arbitron/group_e2e_test.go`) exercises the persisted group link through `initDomain`.
- Producers of the persisted values: `Instance.ParentInstanceID/ParentNodeID/SpawnMode/GroupRunID/GroupTaskKey/GroupTaskKind` are stamped by `Engine.StartGroupTask` (P2-T7, `domain/engine/engine_group_task.go`) — workflow kind through the `Instance` literal in `domain/engine/engine.go:505-560` (P2-T7 must copy the three group fields from the `SpawnLink` there; if it does not, `groupLinkComplete` is false and the child is persisted as a ROOT — `TestInstanceRepo_IncompleteGroupLinkWritesNoLinkAtAll` pins that outcome), branch kind through its own snapshot trigger path. The run id and task key come from the roster row persisted by `GroupRunRepository.CreateWithTasks` (P2-T3) from `GroupLauncher.Launch` (P2-T8).
- Consumers of the read side: `Engine.OnChildTerminal` group dispatch (P2-T13, via `InstanceRepository.Get` — `domain/engine/spawn_wake.go:39`), the group coordinator (P2-T10), the cancel cascade (`domain/engine/cancel_cascade.go`, which P2-T12 moves from `ListActiveChildren` to `ListChildren`; both key on `parent_instance_id` and so cover group tasks), Phase 4's graph-state reader.
- Provenance probes: `TestMigration019_AddsGroupColumnsTablesAndIndexes` fails if the migration file is missing from the embedded set; `TestInstanceRepo_GroupLinkWrittenInTheCreatingStatement` fails if `Create` drops any group field; `TestListRunning_PreservesGroupLink` / `TestNextEligible_PreservesGroupLink` fail if a projection reverts to the generated query.

### Task 2: Migration 019 and group-task link persistence
**Files:**
- Create: `adapter/sqlite/migrations/019_parallel_groups.up.sql`, `adapter/sqlite/migrations/019_parallel_groups.down.sql`
- Modify (cycle A): `adapter/sqlite/instance_repo.go:31-49` (`createInstanceSQL`), `:80-88` (link variables in `Create`), `:114-118` (INSERT arguments), insert after `:134` (`groupLinkComplete`)
- Modify (cycle A): `adapter/sqlite/instance_spawn_rows.go:11-24` (`instanceColumnsWithLink`), `:39-94` (`scanInstanceWithLink`)
- Modify (cycle A): `adapter/sqlite/convert.go` — insert before `:259` (`nullStringToParentID`)
- Modify (cycle A): `adapter/sqlite/queries/instances.sql:6-7` (`CreateInstance` column list — pinned by `TestCreateInstanceSQL_MatchesTheSourceOfTruthQuery`, `instance_spawn_link_integrity_test.go:160`)
- Modify (cycle B): `adapter/sqlite/instance_spawn_rows.go:3-9` (imports) + append `queryInstancesWithLink`; `adapter/sqlite/instance_repo.go` `ListRunning` (base `:370-385`) and the link comment in `rowToInstance` (base `:905-909`); `adapter/sqlite/queue_repo.go:61-74` (`NextEligible`), `:109-139` (`ListWaitingWithTimer`, `ListWaitingWithWebhook`); `adapter/sqlite/queries/instances.sql:61-62`, `adapter/sqlite/queries/queue.sql:13-14` (documentation notes)
- Test: `adapter/sqlite/instance_group_link_test.go` (cycle A), `adapter/sqlite/instance_group_link_projection_test.go` (cycle B)

Design notes the implementer must keep (the tests pin them):
- NEVER rebuild or drop `instances`: with `PRAGMA foreign_keys=ON` (`adapter/sqlite/db.go:39`) a rebuild cascade-deletes `node_executions`. That is why `spawn_mode` cannot be widened to `'group_task'` and a group task stores it as NULL.
- No foreign key from `group_runs.parent_instance_id` / `instances.group_run_id` (018 / 014 precedent: a purged parent must not cascade-delete history), and none from `group_tasks.child_instance_id` (the id is RESERVED before the child row exists).
- `terminal_seq` is GLOBAL: assigned by P2-T3 as `(SELECT COALESCE(MAX(terminal_seq),0)+1 FROM group_tasks)` inside the conditional UPDATE; the UNIQUE index serves that MAX and forbids duplicates. A terminal `live_status` exists exactly when `terminal_seq` does.
- The down migration drops the two tables and the index; the four `instances` columns stay (forward-only column removal — `adapter/sqlite/migrations/README.md`).
- `instances.subtree_cancel_requested_at` is written and read ONLY by P2-T12 (`InstanceRepo.MarkSubtreeCancelRequested`, `listOrphanedActiveChildrenSQL`); this task adds the column and nothing in `createInstanceSQL` / `instanceColumnsWithLink` touches it.

#### Cycle A — migration, write path, `Get`

- [x] Step 1: Write the failing tests. Create `adapter/sqlite/instance_group_link_test.go`:

```go
package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGroupTaskInstance builds a queued parallel_group task child of parentID
// at group node nodeID, owned by run runID under task key.
func newGroupTaskInstance(parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, kind instance.GroupTaskKind) *instance.Instance {
	inst := newLinkedInstance(instance.SpawnLink{})
	p := parentID
	n := nodeID
	r := runID
	k := key
	inst.ParentInstanceID = &p
	inst.ParentNodeID = &n
	inst.SpawnMode = instance.SpawnModeGroupTask
	inst.GroupRunID = &r
	inst.GroupTaskKey = &k
	inst.GroupTaskKind = kind
	inst.SpawnDepth = 1
	return inst
}

// createGroupTask persists a group-task child in the given state.
func createGroupTask(t *testing.T, repo *InstanceRepo, parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, state instance.State) uuid.UUID {
	t.Helper()
	c := newGroupTaskInstance(parentID, nodeID, runID, key, instance.GroupTaskKindBranch)
	c.State = state
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

func assertGroupLink(t *testing.T, got *instance.Instance, parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, kind instance.GroupTaskKind) {
	t.Helper()
	require.NotNil(t, got.ParentInstanceID, "parent_instance_id lost")
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID, "parent_node_id lost")
	assert.Equal(t, nodeID, *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeGroupTask, got.SpawnMode, "a group-task row must read back as SpawnModeGroupTask")
	require.NotNil(t, got.GroupRunID, "group_run_id lost")
	assert.Equal(t, runID, *got.GroupRunID)
	require.NotNil(t, got.GroupTaskKey, "group_task_key lost")
	assert.Equal(t, key, *got.GroupTaskKey)
	assert.Equal(t, kind, got.GroupTaskKind)
	assert.True(t, got.IsGroupTask())
	assert.False(t, got.IsRoot())
}

// rawLinkColumns reads the stored link tuple, bypassing the repository.
func rawLinkColumns(t *testing.T, db testDB, id uuid.UUID) (pid, pnode, mode, run, key, kind any) {
	t.Helper()
	require.NoError(t, db.DB.QueryRow(
		`SELECT parent_instance_id, parent_node_id, spawn_mode, group_run_id, group_task_key, group_task_kind
		 FROM instances WHERE id = ?`, id.String()).Scan(&pid, &pnode, &mode, &run, &key, &kind))
	return
}

// ---------- migration ----------

func TestMigration019_AddsGroupColumnsTablesAndIndexes(t *testing.T) {
	db := setupTestDB(t)

	cols := map[string]bool{}
	rows, err := db.DB.Query(`PRAGMA table_info(instances)`)
	require.NoError(t, err)
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt any
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk))
		cols[name] = notNull == 1
	}
	require.NoError(t, rows.Close())
	for _, want := range []string{"group_run_id", "group_task_key", "group_task_kind", "subtree_cancel_requested_at"} {
		notNull, ok := cols[want]
		require.True(t, ok, "migration 019 must add instances.%s", want)
		assert.False(t, notNull, "instances.%s must be NULLABLE: every root and spawn child has none", want)
	}

	for _, table := range []string{"group_runs", "group_tasks"} {
		var n int
		require.NoError(t, db.DB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		assert.Equal(t, 1, n, "migration 019 must create table %s", table)
	}
	for _, idx := range []string{"idx_instances_group_run", "idx_group_runs_unsettled", "idx_group_tasks_terminal_seq"} {
		var n int
		require.NoError(t, db.DB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n))
		assert.Equal(t, 1, n, "migration 019 must create index %s", idx)
	}
}

// The down step must execute cleanly against a fully migrated database, drop
// both group tables and leave instances (and its rows) intact.
func TestMigration019_DownDropsGroupTables(t *testing.T) {
	db := setupTestDB(t)
	root := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, NewInstanceRepo(db.DB).Create(context.Background(), root))

	down, err := migrationsFS.ReadFile("migrations/019_parallel_groups.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err, "019 down migration must execute without error")

	for _, table := range []string{"group_runs", "group_tasks"} {
		var n int
		require.NoError(t, db.DB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		assert.Equal(t, 0, n, "%s must be gone after the 019 down migration", table)
	}
	var n int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE id = ?`, root.ID.String()).Scan(&n))
	assert.Equal(t, 1, n, "instances rows must survive the 019 down migration")
}

// The group tables enforce their own invariants, so a second writer (or a
// repository bug) cannot persist a state the coordinator would misread.
func TestMigration019_GroupTableInvariants(t *testing.T) {
	db := setupTestDB(t)
	const now = "2026-10-16T12:00:00.000000000Z"
	insertRun := func(id, parent, node string, attempt int, state string, decision, finalOutput any, ready int) error {
		_, err := db.DB.Exec(`INSERT INTO group_runs (id, parent_instance_id, node_id, attempt, form,
			completion_rule, unfinished_behavior, gather_mode, state, decision, final_output,
			continuation_ready, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'branches', 'all_succeed', 'cancel_unfinished', 'results', ?, ?, ?, ?, ?, ?)`,
			id, parent, node, attempt, state, decision, finalOutput, ready, now, now)
		return err
	}
	parent := uuid.New().String()
	runID := uuid.New().String()
	require.NoError(t, insertRun(runID, parent, "group-1", 1, "running", nil, nil, 0))

	assert.Error(t, insertRun(uuid.New().String(), parent, "group-1", 1, "running", nil, nil, 0),
		"(parent, node, attempt) is unique")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-2", 1, "finished", nil, nil, 0),
		"an unknown run state is rejected")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-3", 1, "running", `{"outcome":"success"}`, nil, 0),
		"a decision cannot exist while the run is still launching/running")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-4", 1, "decided", `{}`, `{}`, 0),
		"a final output exists only in state done")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-5", 1, "decided", `{}`, nil, 1),
		"continuation readiness requires a final output")
	assert.NoError(t, insertRun(uuid.New().String(), parent, "group-6", 1, "gathering", `{}`, nil, 0),
		"Phase 3 states are admitted")

	insertTask := func(pos int, key, child, live string, seq, at any) error {
		_, err := db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
			child_instance_id, live_status, terminal_seq, terminal_at, updated_at)
			VALUES (?, ?, ?, 'branch', 'entry', ?, ?, ?, ?, ?)`, runID, pos, key, child, live, seq, at, now)
		return err
	}
	require.NoError(t, insertTask(0, "a", uuid.New().String(), "running", nil, nil))
	require.NoError(t, insertTask(1, "b", uuid.New().String(), "completed", 1, now))

	assert.Error(t, insertTask(2, "c", uuid.New().String(), "unfinished", nil, nil),
		"'unfinished' is frozen-only and never a live status")
	assert.Error(t, insertTask(3, "d", uuid.New().String(), "running", 2, nil),
		"a terminal seq always comes with a terminal time")
	assert.Error(t, insertTask(4, "e", uuid.New().String(), "failed", nil, nil),
		"a terminal live status requires a recorded terminal seq")
	assert.Error(t, insertTask(5, "f", uuid.New().String(), "cancelled", 1, now),
		"terminal seqs are globally unique")
	assert.Error(t, insertTask(6, "a", uuid.New().String(), "running", nil, nil),
		"task keys are unique within a run")
	assert.Error(t, insertTask(0, "g", uuid.New().String(), "running", nil, nil),
		"positions are unique within a run")
	_, err := db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
		child_instance_id, updated_at) VALUES (?, 9, 'h', 'spawn', 'x', ?, ?)`, runID, uuid.New().String(), now)
	assert.Error(t, err, "an unknown task kind is rejected")
	_, err = db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
		child_instance_id, updated_at) VALUES (?, 0, 'a', 'branch', 'x', ?, ?)`, uuid.New().String(), uuid.New().String(), now)
	assert.Error(t, err, "a task must belong to an existing run (FK)")
}

// ---------- success: round trip ----------

func TestInstanceRepo_GroupTaskLinkRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()

	for _, kind := range []instance.GroupTaskKind{instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow} {
		child := newGroupTaskInstance(parent.ID, "group-1", runID, "task-"+string(kind), kind)
		require.NoError(t, repo.Create(ctx, child))

		got, err := repo.Get(ctx, child.ID)
		require.NoError(t, err)
		assertGroupLink(t, got, parent.ID, "group-1", runID, "task-"+string(kind), kind)
		assert.Equal(t, 1, got.SpawnDepth, "the depth counter rides the same insert")
	}
}

// The group columns must be on the row the moment Create returns (the
// launcher enqueues right after), and spawn_mode must be NULL — the 018 CHECK
// cannot hold 'group_task'.
func TestInstanceRepo_GroupLinkWrittenInTheCreatingStatement(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(context.Background(), parent))
	runID := uuid.New()

	child := newGroupTaskInstance(parent.ID, "group-1", runID, "a", instance.GroupTaskKindWorkflow)
	require.NoError(t, repo.Create(context.Background(), child))

	pid, pnode, mode, run, key, kind := rawLinkColumns(t, db, child.ID)
	assert.Equal(t, parent.ID.String(), pid)
	assert.Equal(t, "group-1", pnode)
	assert.Nil(t, mode, "a group task stores spawn_mode NULL")
	assert.Equal(t, runID.String(), run)
	assert.Equal(t, "a", key)
	assert.Equal(t, "workflow", kind)
}

// ---------- failure: incomplete group link never written ----------

func TestInstanceRepo_IncompleteGroupLinkWritesNoLinkAtAll(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	cases := map[string]func(*instance.Instance){
		"nil group run id":   func(i *instance.Instance) { i.GroupRunID = nil },
		"uuid.Nil run id":    func(i *instance.Instance) { n := uuid.Nil; i.GroupRunID = &n },
		"nil task key":       func(i *instance.Instance) { i.GroupTaskKey = nil },
		"empty task key":     func(i *instance.Instance) { e := ""; i.GroupTaskKey = &e },
		"empty task kind":    func(i *instance.Instance) { i.GroupTaskKind = "" },
		"bogus task kind":    func(i *instance.Instance) { i.GroupTaskKind = "branches" },
		"empty parent node":  func(i *instance.Instance) { e := ""; i.ParentNodeID = &e },
		"nil parent node id": func(i *instance.Instance) { i.ParentNodeID = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			inst := newGroupTaskInstance(parent.ID, "group-1", uuid.New(), "a", instance.GroupTaskKindBranch)
			mutate(inst)
			require.NoError(t, repo.Create(ctx, inst), "an incomplete link is dropped, never an INSERT error")

			pid, pnode, mode, run, key, kind := rawLinkColumns(t, db, inst.ID)
			for col, v := range map[string]any{"parent_instance_id": pid, "parent_node_id": pnode,
				"spawn_mode": mode, "group_run_id": run, "group_task_key": key, "group_task_kind": kind} {
				assert.Nil(t, v, "%s must stay NULL for an incomplete group link", col)
			}
			got, err := repo.Get(ctx, inst.ID)
			require.NoError(t, err)
			assert.True(t, got.IsRoot())
			assert.False(t, got.IsGroupTask())
		})
	}
}

// Group fields on a LEGACY-mode instance are ignored: the legacy link is
// written exactly as before and the group columns stay NULL.
func TestInstanceRepo_LegacyLinkIgnoresGroupFields(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	inst := newGroupTaskInstance(parent.ID, "spawn-1", uuid.New(), "a", instance.GroupTaskKindBranch)
	inst.SpawnMode = instance.SpawnModeWaitForChild
	require.NoError(t, repo.Create(ctx, inst))

	_, _, mode, run, key, kind := rawLinkColumns(t, db, inst.ID)
	assert.Equal(t, "wait_for_child", mode)
	assert.Nil(t, run)
	assert.Nil(t, key)
	assert.Nil(t, kind)

	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assertSpawnLink(t, got, parent.ID, "spawn-1", instance.SpawnModeWaitForChild)
	assert.Nil(t, got.GroupRunID)
	assert.False(t, got.IsGroupTask())
}

// ---------- schema integrity (019 CHECKs) ----------

func TestGroupLinkSchema_RejectsContradictoryOrPartialTuples(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	legacy := childOf(t, repo, parent.ID, "spawn-1", instance.SpawnModeWaitForChild,
		instance.StateRunning, uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")
	root := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, root))

	_, err := db.DB.Exec(`UPDATE instances SET group_run_id = ?, group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`,
		uuid.New().String(), legacy.String())
	assert.Error(t, err, "a group run id next to a legacy spawn_mode must be rejected")

	_, err = db.DB.Exec(`UPDATE instances SET group_run_id = ?, group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`,
		uuid.New().String(), root.ID.String())
	assert.Error(t, err, "a group run id without a parent link must be rejected")

	_, err = db.DB.Exec(`UPDATE instances SET parent_instance_id = ?, parent_node_id = 'g', group_run_id = ?, group_task_kind = 'branch' WHERE id = ?`,
		parent.ID.String(), uuid.New().String(), root.ID.String())
	assert.Error(t, err, "a group run id without a task key must be rejected")

	_, err = db.DB.Exec(`UPDATE instances SET group_task_kind = 'branches' WHERE id = ?`, root.ID.String())
	assert.Error(t, err, "an unknown task kind must be rejected")
}

// ---------- read side: collapse and corruption ----------

// A task key/kind without a group run id (planted by raw SQL — Create never
// writes it) is not a group task: the row reads as a root.
func TestGroupLinkRead_KeyWithoutRunIDReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New()
	writeRawLink(t, db, id, uuidToString(uuid.New()), "group-1", nil)
	_, err := db.DB.Exec(`UPDATE instances SET group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`, id.String())
	require.NoError(t, err)

	got, err := NewInstanceRepo(db.DB).Get(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot())
	assert.Nil(t, got.GroupRunID)
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
}

// A corrupt group run id is damage, not absence: it must surface as an error.
func TestGroupLinkRead_MalformedGroupRunIDIsAnError(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New()
	writeRawLink(t, db, id, uuidToString(uuid.New()), "group-1", nil)
	_, err := db.DB.Exec(`UPDATE instances SET group_run_id = 'not-a-uuid', group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`, id.String())
	require.NoError(t, err)

	_, err = NewInstanceRepo(db.DB).Get(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group_run_id")
}

// ---------- legacy spawn queries never match group tasks ----------

func TestLegacySpawnQueries_NeverMatchGroupTasks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := waitingParent(t, repo, "group-1")
	createGroupTask(t, repo, parent, "group-1", uuid.New(), "a", instance.StateCompleted)

	for _, mode := range []instance.SpawnMode{instance.SpawnModeWaitForChild, instance.SpawnModeFireAndForget, instance.SpawnModeGroupTask} {
		got, err := repo.FindChildBySpawnNode(ctx, parent, "group-1", mode)
		require.NoError(t, err)
		assert.Nil(t, got, "FindChildBySpawnNode(%q) must never return a group task", mode)
	}

	cands, err := repo.ListWaitingOnTerminalChild(ctx)
	require.NoError(t, err)
	assert.Empty(t, cands, "the single-child reconciler must never resume a parent through a group task")
}

// ---------- cancel cascade / hierarchy DO include group tasks ----------

func TestListActiveChildren_IncludesGroupTasks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := uuid.MustParse(spawnTestOwner)

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	running := createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateRunning)
	queued := createGroupTask(t, repo, parent.ID, "group-1", runID, "b", instance.StateQueued)
	createGroupTask(t, repo, parent.ID, "group-1", runID, "c", instance.StateCompleted)
	legacy := childOf(t, repo, parent.ID, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateRunning, owner, instance.VisibilityPrivate, "")

	got, err := repo.ListActiveChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{running, queued, legacy}, got,
		"the cancel cascade must reach every active group task, not only spawn children")
}

func TestListOrphanedActiveChildren_IncludesGroupTasks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	parent.State = instance.StateCancelled
	require.NoError(t, repo.Create(ctx, parent))
	orphan := createGroupTask(t, repo, parent.ID, "group-1", uuid.New(), "a", instance.StateWaiting)

	got, err := repo.ListOrphanedActiveChildren(ctx)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{orphan}, got)
}

func TestChildCountsAndParentScope_CoverGroupTasks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	a := createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateRunning)
	b := createGroupTask(t, repo, parent.ID, "group-1", runID, "b", instance.StateCompleted)

	counts, err := repo.ChildCountsByParents(ctx, []uuid.UUID{parent.ID}, instance.ChildVisibility{Unconstrained: true})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[parent.ID], "the hierarchy expander counts group tasks")

	roots, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeRoots})
	require.NoError(t, err)
	rootIDs := idsOf(roots)
	assert.True(t, rootIDs[parent.ID])
	assert.False(t, rootIDs[a], "a group task is never a root")
	assert.False(t, rootIDs[b], "a group task is never a root")

	pid := parent.ID
	children, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeChildren, ParentInstanceID: &pid})
	require.NoError(t, err)
	assert.Equal(t, map[uuid.UUID]bool{a: true, b: true}, idsOf(children))
}
```

- [x] Step 2: Run them, confirm they fail.

```bash
go test ./adapter/sqlite/ -count=1 -run 'Migration019|GroupLink|GroupTask|LegacySpawnQueries|ListActiveChildren_Includes|ListOrphanedActiveChildren_Includes|ChildCountsAndParentScope'
```
Expected (the migration does not exist and `Create` still tries to write `spawn_mode='group_task'`):
```
--- FAIL: TestMigration019_AddsGroupColumnsTablesAndIndexes (0.01s)
        	Messages:   	migration 019 must add instances.group_run_id
--- FAIL: TestMigration019_DownDropsGroupTables (0.01s)
        	            	open migrations/019_parallel_groups.down.sql: file does not exist
--- FAIL: TestInstanceRepo_GroupTaskLinkRoundTrip (0.01s)
        	            	creating instance: constraint failed: CHECK constraint failed: spawn_mode IS NULL OR spawn_mode IN ('fire_and_forget','wait_for_child') (275)
...
FAIL	github.com/arbitron/arbitron/adapter/sqlite
```

- [x] Step 3: Create `adapter/sqlite/migrations/019_parallel_groups.up.sql`:

```sql
-- Parallel workflow gathering Phase 2: durable group runs, their ordered task
-- rosters, and the group-task half of a child's link back to its parent.
--
-- ---------------------------------------------------------------------------
-- 1. instances: group-task link columns
-- ---------------------------------------------------------------------------
-- A parallel_group task child carries the SAME parent_instance_id /
-- parent_node_id ancestry as a spawn child (so the cancel cascade, the
-- hierarchy read and the roots filter cover it unchanged) plus:
--   group_run_id    the group run that owns the task (group_runs.id)
--   group_task_key  the task's key within that run
--   group_task_kind 'branch' | 'workflow'
--
-- spawn_mode stays NULL for a group task. Migration 018's CHECK admits only
-- 'fire_and_forget' / 'wait_for_child', and widening it would mean rebuilding
-- instances — which, with PRAGMA foreign_keys=ON, cascade-deletes every
-- node_executions row. Leaving spawn_mode NULL has a second benefit: every
-- legacy spawn query filters on spawn_mode and therefore can never match a
-- group task. The read side maps (parent set AND group_run_id set) to the
-- internal SpawnMode 'group_task'.
--
-- The CHECK on group_run_id makes the group tuple ALL-OR-NONE at the schema
-- level: a row may only carry a group run id when it also carries a complete
-- parent link, a task key and a task kind, and NO legacy spawn_mode. (SQLite
-- evaluates a column CHECK added by ADD COLUMN against existing rows; every
-- existing row has group_run_id NULL and passes.) group_task_key and
-- group_task_kind are added first so the CHECK can reference them.
--
-- REFERENTIAL INTEGRITY: there is deliberately NO FOREIGN KEY on
-- group_run_id, the same choice 018 records for parent_instance_id: a child's
-- execution history must never be cascade-deleted through its group run.
ALTER TABLE instances ADD COLUMN group_task_key  TEXT;  -- NULL => not a group task
ALTER TABLE instances ADD COLUMN group_task_kind TEXT
    CHECK (group_task_kind IS NULL OR group_task_kind IN ('branch','workflow'));
ALTER TABLE instances ADD COLUMN group_run_id    TEXT   -- NULL => not a group task
    CHECK (group_run_id IS NULL OR (
        spawn_mode IS NULL
        AND parent_instance_id IS NOT NULL AND parent_instance_id <> ''
        AND parent_node_id IS NOT NULL AND parent_node_id <> ''
        AND group_task_key IS NOT NULL AND group_task_key <> ''
        AND group_task_kind IS NOT NULL
    ));

-- Children of one group run (live inspection, Phase 4 read model).
CREATE INDEX IF NOT EXISTS idx_instances_group_run ON instances (group_run_id);

-- Durable subtree-cancellation intent on a TERMINAL run (consumed by P2-T12).
-- Cancelling a completed/failed run never changes its own state (its outcome is
-- final), but it must still stop the run's active descendants (a
-- finish_independently loser, a fire-and-forget child). Engine.Cancel stamps
-- this column BEFORE its best-effort in-process walk, and
-- InstanceRepo.ListOrphanedActiveChildren treats a stamped ancestor like a
-- cancelled one for every stranded run created at or before the stamp, so a
-- crash or a storage error part-way through the walk is finished by the
-- existing orphan sweep. Same layout as created_at
-- (strftime('%Y-%m-%dT%H:%M:%SZ')), so lexical comparison is chronological.
-- NULL => no subtree cancellation was requested.
ALTER TABLE instances ADD COLUMN subtree_cancel_requested_at TEXT;

-- ---------------------------------------------------------------------------
-- 2. group_runs: one row per execution (attempt) of a parallel_group node
-- ---------------------------------------------------------------------------
-- There is deliberately NO FOREIGN KEY on parent_instance_id (018 / 014
-- precedent): a purged parent must not cascade-delete the group record that
-- explains its children.
--
-- form / completion_rule / unfinished_behavior / gather_mode are NOT
-- CHECK-constrained: their vocabulary is owned by domain/workflow and
-- validated by workflow.ParseGroupConfig before a run is created. A CHECK here
-- would turn any later vocabulary addition into a table rebuild, and
-- group_tasks references this table ON DELETE CASCADE, so a rebuild would wipe
-- every roster. state IS constrained (it is this table's own lifecycle) and
-- already admits Phase 3's 'gathering' / 'resolving_conflicts'.
--
-- JSON columns: config, body_ownership, start_variables (objects, '{}' when
-- empty); decision (GroupDecision) and final_output are NULL until set. The
-- NULL-ness of decision / final_output IS the "not yet" marker the CAS
-- operations test (Decide: decision IS NULL; SetFinalOutput: final_output IS
-- NULL).
--
-- Timestamps are fixed-width UTC RFC3339 with nanoseconds
-- ("2006-01-02T15:04:05.000000000Z"), written by the repository, so lexical
-- order equals chronological order.
CREATE TABLE IF NOT EXISTS group_runs (
    id                     TEXT PRIMARY KEY,
    parent_instance_id     TEXT NOT NULL CHECK (parent_instance_id <> ''),
    node_id                TEXT NOT NULL CHECK (node_id <> ''),
    attempt                INTEGER NOT NULL CHECK (attempt >= 1),
    form                   TEXT NOT NULL,
    completion_rule        TEXT NOT NULL,
    unfinished_behavior    TEXT NOT NULL,
    gather_mode            TEXT NOT NULL,
    config                 TEXT NOT NULL DEFAULT '{}',
    body_ownership         TEXT NOT NULL DEFAULT '{}',
    start_variables        TEXT NOT NULL DEFAULT '{}',
    workspace_snapshot     TEXT NOT NULL DEFAULT '',
    state                  TEXT NOT NULL CHECK (state IN
        ('launching','running','decided','gathering','resolving_conflicts','done')),
    decision               TEXT,
    final_output           TEXT,
    continuation_port      TEXT NOT NULL DEFAULT ''
        CHECK (continuation_port IN ('','success','failure')),
    continuation_ready     INTEGER NOT NULL DEFAULT 0 CHECK (continuation_ready IN (0,1)),
    continuation_delivered INTEGER NOT NULL DEFAULT 0 CHECK (continuation_delivered IN (0,1)),
    actions_applied        INTEGER NOT NULL DEFAULT 0 CHECK (actions_applied IN (0,1)),
    launch_error           TEXT NOT NULL DEFAULT '',
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    -- One run per (parent, node, attempt): the executor's re-entry guard.
    UNIQUE (parent_instance_id, node_id, attempt),
    -- A decision exists only once the run has left launching/running.
    CHECK (decision IS NULL OR state NOT IN ('launching','running')),
    -- The final output is written exactly with the transition to done.
    CHECK (final_output IS NULL OR state = 'done'),
    CHECK (continuation_ready = 0 OR final_output IS NOT NULL)
);

-- The reconciliation sweep (GroupRunRepository.ListUnsettled) reads exactly
-- this predicate; the partial index keeps it proportional to unsettled runs.
CREATE INDEX IF NOT EXISTS idx_group_runs_unsettled ON group_runs (created_at, id)
    WHERE state <> 'done' OR continuation_delivered = 0 OR actions_applied = 0;

-- ---------------------------------------------------------------------------
-- 3. group_tasks: the ordered roster, persisted in full BEFORE any enqueue
-- ---------------------------------------------------------------------------
-- child_instance_id is the id RESERVED for the task's child before the child
-- row exists, so it cannot reference instances(id); it is UNIQUE across all
-- runs so GetByChild is a single index probe and a child can never belong to
-- two rosters.
--
-- live_status admits only LIVE statuses: 'unfinished' is a decision-time
-- verdict stored inside group_runs.decision, never a child's status.
--
-- terminal_seq is the GLOBAL terminal-intake order. RecordTaskTerminal assigns
-- it as (SELECT COALESCE(MAX(terminal_seq), 0) + 1 FROM group_tasks) inside the
-- single UPDATE that also requires terminal_seq IS NULL, so it is assigned at
-- most once per task and is strictly increasing across every run (hence also
-- within each run). The UNIQUE index both serves that MAX and makes a
-- duplicate seq impossible.
--
-- Phase 3 (migration 020) adds finalized_ref; it is intentionally absent here.
CREATE TABLE IF NOT EXISTS group_tasks (
    group_run_id             TEXT NOT NULL REFERENCES group_runs(id) ON DELETE CASCADE,
    position                 INTEGER NOT NULL CHECK (position >= 0),
    task_key                 TEXT NOT NULL CHECK (task_key <> ''),
    kind                     TEXT NOT NULL CHECK (kind IN ('branch','workflow')),
    origin                   TEXT NOT NULL DEFAULT '',
    child_instance_id        TEXT NOT NULL CHECK (child_instance_id <> ''),
    launch_state             TEXT NOT NULL DEFAULT 'pending'
        CHECK (launch_state IN ('pending','launched','launch_failed')),
    launch_error             TEXT NOT NULL DEFAULT '',
    live_status              TEXT NOT NULL DEFAULT 'not_started' CHECK (live_status IN
        ('not_started','queued','running','waiting','paused','completed','failed','cancelled')),
    terminal_seq             INTEGER CHECK (terminal_seq IS NULL OR terminal_seq >= 1),
    terminal_at              TEXT,
    result                   TEXT,
    failure                  TEXT,
    cancel_requested         INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1)),
    blocking                 INTEGER NOT NULL DEFAULT 1 CHECK (blocking IN (0,1)),
    continuing_independently INTEGER NOT NULL DEFAULT 0 CHECK (continuing_independently IN (0,1)),
    updated_at               TEXT NOT NULL,
    PRIMARY KEY (group_run_id, position),
    UNIQUE (group_run_id, task_key),
    UNIQUE (child_instance_id),
    CHECK ((terminal_seq IS NULL) = (terminal_at IS NULL)),
    -- A terminal live status exists exactly when the terminal was recorded
    -- (and sequenced) by RecordTaskTerminal.
    CHECK ((live_status IN ('completed','failed','cancelled')) = (terminal_seq IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_group_tasks_terminal_seq ON group_tasks (terminal_seq);
```

- [x] Step 4: Create `adapter/sqlite/migrations/019_parallel_groups.down.sql`:

```sql
-- Reverse 019. The two group tables are dropped (children first: group_tasks
-- references group_runs). The four instances columns (three group-link columns
-- plus subtree_cancel_requested_at) are deliberately LEFT IN
-- PLACE: SQLite cannot DROP COLUMN portably on the older versions some
-- deployments run, and rebuilding instances is never acceptable (with
-- PRAGMA foreign_keys=ON it cascade-deletes node_executions). Forward-only
-- column removal is the project convention — see migrations/README.md and the
-- 012 / 014 / 017 / 018 down migrations.
DROP INDEX IF EXISTS idx_group_tasks_terminal_seq;
DROP TABLE IF EXISTS group_tasks;
DROP INDEX IF EXISTS idx_group_runs_unsettled;
DROP TABLE IF EXISTS group_runs;
DROP INDEX IF EXISTS idx_instances_group_run;
```

- [x] Step 5: Write the group link in `Create`. In `adapter/sqlite/instance_repo.go`:

(a) Replace `createInstanceSQL` and its doc comment (lines 31-49) with:

```go
// createInstanceSQL writes the row and the spawn link in ONE statement.
//
// The link MUST ride this INSERT rather than a follow-up UPDATE: the trigger
// path creates AND enqueues (Engine.TriggerWithDepartment), so control returns
// to the caller only after the child may already be running. A child that
// reached a terminal state before a follow-up link write landed would leave its
// waiting parent with nothing to match, and the parent would hang forever.
//
// This is hand-written rather than sqlc-generated for the same reason
// CountActiveByWorkflowID is: it lets the column set evolve without a
// regeneration step. queries/instances.sql carries the matching source-of-truth
// SQL.
//
// The group-task columns (migration 019) ride the SAME insert for the same
// reason the spawn link does: the group launcher enqueues the child right
// after creating it, and a fast child's terminal must already be attributable
// to its roster row.
const createInstanceSQL = `INSERT INTO instances (
	id, owner_id, workflow_name, workflow_snapshot, state, priority,
	variables, original_variables, queued_at, department, visibility,
	trigger_source, schedule_id, workflow_id,
	parent_instance_id, parent_node_id, spawn_mode, spawn_depth,
	group_run_id, group_task_key, group_task_kind
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING created_at, updated_at`
```

(b) Replace the link variables (lines 80-88, from `// A PARTIAL link is never written` through the closing `}` of the `if inst.ParentInstanceID != nil && …` block) with:

```go
	// A PARTIAL link is never written: a row with a parent id but no node id
	// could not be matched by the wake and would strand its parent forever. All
	// three columns are written together or all three stay NULL.
	//
	// A GROUP-TASK link (migration 019) is written as parent id + node id +
	// spawn_mode NULL + the three group columns — never as spawn_mode
	// 'group_task', which the 018 CHECK rejects. It is all-or-none too: an
	// incomplete group tuple writes NO link at all (the row is a root), the same
	// outcome as an incomplete legacy link, rather than a row the coordinator
	// could not attribute to a roster entry. Group fields on a legacy-mode
	// instance are ignored; only a group-task instance writes them.
	var parentID, parentNode, spawnMode sql.NullString
	var groupRunID, groupTaskKey, groupTaskKind sql.NullString
	if inst.ParentInstanceID != nil && inst.ParentNodeID != nil {
		switch {
		case inst.SpawnMode == instance.SpawnModeGroupTask:
			if groupLinkComplete(inst) {
				parentID = parentIDToNullString(inst.ParentInstanceID)
				parentNode = ptrToNullString(inst.ParentNodeID)
				groupRunID = sql.NullString{String: inst.GroupRunID.String(), Valid: true}
				groupTaskKey = ptrToNullString(inst.GroupTaskKey)
				groupTaskKind = sql.NullString{String: string(inst.GroupTaskKind), Valid: true}
			}
		case inst.SpawnMode.Valid():
			parentID = parentIDToNullString(inst.ParentInstanceID)
			parentNode = ptrToNullString(inst.ParentNodeID)
			spawnMode = sql.NullString{String: string(inst.SpawnMode), Valid: true}
		}
	}
```

(c) Append the three group arguments to the INSERT: replace lines 114-118 (from `// Rides the SAME insert as the link:` through `).Scan(&createdAtRaw, &updatedAtRaw)`) with:

```go
		// Rides the SAME insert as the link: the depth counter is the runtime
		// bound on spawn recursion, so there must be no window in which a child
		// row exists with a depth of 0 that a grandchild could read.
		inst.SpawnDepth,
		groupRunID,
		groupTaskKey,
		groupTaskKind,
	).Scan(&createdAtRaw, &updatedAtRaw)
```

(d) Insert after the end of `Create` (after line 134, before `func (r *InstanceRepo) Get`):

```go
// groupLinkComplete reports whether a group-task instance carries every part
// of its group link: a non-empty parent node (the 019 CHECK requires it), a
// non-nil group run id, a non-empty task key and a valid task kind.
func groupLinkComplete(inst *instance.Instance) bool {
	return inst.ParentNodeID != nil && *inst.ParentNodeID != "" &&
		inst.GroupRunID != nil && *inst.GroupRunID != uuid.Nil &&
		inst.GroupTaskKey != nil && *inst.GroupTaskKey != "" &&
		inst.GroupTaskKind.Valid()
}
```

- [x] Step 6: Read the group link back. In `adapter/sqlite/instance_spawn_rows.go`, replace `instanceColumnsWithLink` and its doc comment (lines 11-24) with:

```go
// instanceColumnsWithLink is the explicit, ordered column list for every
// full-row instance read that must also carry the spawn link. It is written out
// (never `SELECT *`) so the scan order below is pinned by this file rather than
// by the physical column order of the table.
//
// The first 23 columns are exactly generated.Instance's field order, so a row
// scanned with this list can be assembled into a generated.Instance and handed
// to the existing rowToInstance converter — the link columns (018 spawn link +
// depth, 019 group-task link) are the only thing scanned separately. That keeps
// ONE conversion routine for instances.
const instanceColumnsWithLink = `id, owner_id, workflow_name, workflow_snapshot, state, priority, ` +
	`variables, original_variables, current_node_id, failed_node_id, created_at, updated_at, ` +
	`completed_at, queued_at, waiting_until, webhook_token, webhook_secret_hash, ` +
	`webhook_received_at, department, visibility, trigger_source, schedule_id, workflow_id, ` +
	`parent_instance_id, parent_node_id, spawn_mode, spawn_depth, ` +
	`group_run_id, group_task_key, group_task_kind`
```

and replace `scanInstanceWithLink` (lines 39-94, doc comment through the closing `}`) with:

```go
// scanInstanceWithLink reads one row selected with instanceColumnsWithLink and
// converts it to a domain Instance, including the parent/child spawn link and,
// for a parallel_group task child, the group-task link.
func scanInstanceWithLink(s rowScanner) (*instance.Instance, error) {
	var row generated.Instance
	var parentID, parentNode, spawnMode sql.NullString
	var spawnDepth int
	var groupRunID, groupTaskKey, groupTaskKind sql.NullString
	if err := s.Scan(
		&row.ID,
		&row.OwnerID,
		&row.WorkflowName,
		&row.WorkflowSnapshot,
		&row.State,
		&row.Priority,
		&row.Variables,
		&row.OriginalVariables,
		&row.CurrentNodeID,
		&row.FailedNodeID,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.CompletedAt,
		&row.QueuedAt,
		&row.WaitingUntil,
		&row.WebhookToken,
		&row.WebhookSecretHash,
		&row.WebhookReceivedAt,
		&row.Department,
		&row.Visibility,
		&row.TriggerSource,
		&row.ScheduleID,
		&row.WorkflowID,
		&parentID,
		&parentNode,
		&spawnMode,
		&spawnDepth,
		&groupRunID,
		&groupTaskKey,
		&groupTaskKind,
	); err != nil {
		return nil, err
	}

	inst, err := rowToInstance(row)
	if err != nil {
		return nil, err
	}

	// ALL-OR-NONE on the read side: an incomplete tuple collapses to no link
	// (a root run) rather than being partially populated. See
	// nullStringToSpawnLink.
	pid, nodeID, mode, err := nullStringToSpawnLink(parentID, parentNode, spawnMode)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	inst.ParentInstanceID = pid
	inst.ParentNodeID = nodeID
	inst.SpawnMode = mode
	inst.SpawnDepth = spawnDepth

	// No legacy link: the row may be a parallel_group task (spawn_mode NULL,
	// group columns set). Same ALL-OR-NONE discipline — see
	// nullStringToGroupLink.
	if mode == "" {
		gl, err := nullStringToGroupLink(parentID, parentNode, spawnMode, groupRunID, groupTaskKey, groupTaskKind)
		if err != nil {
			return nil, fmt.Errorf("converting instance row: %w", err)
		}
		if gl != nil {
			inst.ParentInstanceID = &gl.parentID
			inst.ParentNodeID = &gl.parentNodeID
			inst.SpawnMode = instance.SpawnModeGroupTask
			inst.GroupRunID = &gl.runID
			inst.GroupTaskKey = &gl.taskKey
			inst.GroupTaskKind = gl.kind
		}
	}
	return inst, nil
}
```

In `adapter/sqlite/convert.go`, insert immediately before `// nullStringToParentID maps a nullable parent_instance_id column back to` (line 259):

```go
// groupTaskLink is the decoded group-task half of an instance row (migration
// 019): the parent ancestry plus the owning group run, task key and kind.
type groupTaskLink struct {
	parentID     uuid.UUID
	parentNodeID string
	runID        uuid.UUID
	taskKey      string
	kind         instance.GroupTaskKind
}

// nullStringToGroupLink decodes the group-task link of an instance row, or
// returns (nil, nil) when the row is not a COMPLETE group task.
//
// The decision table mirrors nullStringToSpawnLink's ALL-OR-NONE rule:
//   - a legacy spawn_mode wins: the row is a spawn child and its group columns
//     (which the 019 CHECK forbids alongside spawn_mode anyway) are ignored;
//   - no group_run_id: not a group task;
//   - a group_run_id that is not a UUID is an ERROR — corrupt, not missing;
//   - a group_run_id with no parent id, no/empty parent node, no/empty task key
//     or an unrecognized kind collapses to no link (a root), the one
//     interpretation every read path already handles safely.
func nullStringToGroupLink(parentRaw, nodeRaw, modeRaw, runRaw, keyRaw, kindRaw sql.NullString) (*groupTaskLink, error) {
	if modeRaw.Valid && modeRaw.String != "" {
		return nil, nil
	}
	if !runRaw.Valid || runRaw.String == "" {
		return nil, nil
	}
	runID, err := uuid.Parse(runRaw.String)
	if err != nil {
		return nil, fmt.Errorf("parsing group_run_id: %w", err)
	}
	parentID, err := nullStringToParentID(parentRaw)
	if err != nil {
		return nil, err
	}
	node := nullStringToString(nodeRaw)
	key := nullStringToString(keyRaw)
	kind := instance.GroupTaskKind(nullStringToString(kindRaw))
	if parentID == nil || node == "" || key == "" || !kind.Valid() {
		return nil, nil
	}
	return &groupTaskLink{
		parentID:     *parentID,
		parentNodeID: node,
		runID:        runID,
		taskKey:      key,
		kind:         kind,
	}, nil
}
```

- [x] Step 7: Keep the documented source-of-truth INSERT in lockstep (otherwise `TestCreateInstanceSQL_MatchesTheSourceOfTruthQuery` fails). In `adapter/sqlite/queries/instances.sql`, replace lines 6-7 (the `INSERT INTO instances (…)` / `VALUES (…)` pair under `-- name: CreateInstance :one`) with:

```sql
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id, workflow_id, parent_instance_id, parent_node_id, spawn_mode, spawn_depth, group_run_id, group_task_key, group_task_kind)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
```

- [x] Step 8: Run the cycle-A tests and the whole package, confirm they pass.

```bash
go test ./adapter/sqlite/ -count=1 -run 'Migration019|GroupLink|GroupTask|LegacySpawnQueries|ListActiveChildren_Includes|ListOrphanedActiveChildren_Includes|ChildCountsAndParentScope' -v
go test ./adapter/sqlite/ -count=1
```
Expected:
```
--- PASS: TestMigration019_AddsGroupColumnsTablesAndIndexes (0.01s)
--- PASS: TestMigration019_DownDropsGroupTables (0.01s)
--- PASS: TestMigration019_GroupTableInvariants (0.01s)
--- PASS: TestInstanceRepo_GroupTaskLinkRoundTrip (0.01s)
--- PASS: TestInstanceRepo_GroupLinkWrittenInTheCreatingStatement (0.01s)
--- PASS: TestInstanceRepo_IncompleteGroupLinkWritesNoLinkAtAll (0.01s)
--- PASS: TestInstanceRepo_LegacyLinkIgnoresGroupFields (0.01s)
--- PASS: TestGroupLinkSchema_RejectsContradictoryOrPartialTuples (0.01s)
--- PASS: TestGroupLinkRead_KeyWithoutRunIDReadsAsRoot (0.01s)
--- PASS: TestGroupLinkRead_MalformedGroupRunIDIsAnError (0.01s)
--- PASS: TestLegacySpawnQueries_NeverMatchGroupTasks (0.01s)
--- PASS: TestListActiveChildren_IncludesGroupTasks (0.01s)
--- PASS: TestListOrphanedActiveChildren_IncludesGroupTasks (0.01s)
--- PASS: TestChildCountsAndParentScope_CoverGroupTasks (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.4XXs
ok  	github.com/arbitron/arbitron/adapter/sqlite	3.XXXs
```
(The subtests of `TestInstanceRepo_IncompleteGroupLinkWritesNoLinkAtAll` also print `--- PASS` lines.)

#### Cycle B — every full-row projection carries the group link

- [x] Step 9: Write the failing projection tests. Create `adapter/sqlite/instance_group_link_projection_test.go`:

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

// These tests pin that EVERY full-row instance projection — not only Get —
// carries the 019 group-task link. The generated sqlc statements behind
// ListRunning / NextEligible / ListWaitingWith* never selected the group
// columns (nor spawn_depth), so a group-task child read through them would
// report IsRoot() == true: the silent-drop failure mode
// instance_repo_stale_queries_test.go documents for the 018 link.

// ---------- full-row projections carry the group link ----------

func TestListRunning_PreservesGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateRunning)

	rows, err := repo.ListRunning(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertGroupLink(t, &rows[0], parent.ID, "group-1", runID, "a", instance.GroupTaskKindBranch)
	assert.Equal(t, 1, rows[0].SpawnDepth, "ListRunning now carries spawn_depth too")
}

func TestNextEligible_PreservesGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	parent.State = instance.StateWaiting
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	child := createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateQueued)
	require.NoError(t, queueRepo.SetQueued(ctx, child, time.Now().UTC()))

	got, err := queueRepo.NextEligible(ctx, 0, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, child, got.ID)
	assertGroupLink(t, got, parent.ID, "group-1", runID, "a", instance.GroupTaskKindBranch)
}

// The hand-written NextEligible must keep the generated statement's caps and
// "nothing eligible" semantics.
func TestNextEligible_HandWrittenKeepsCapsAndEmptySemantics(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	got, err := queueRepo.NextEligible(ctx, 0, 0)
	require.NoError(t, err)
	assert.Nil(t, got, "an empty queue returns (nil, nil)")

	running := newLinkedInstance(instance.SpawnLink{})
	running.State = instance.StateRunning
	require.NoError(t, repo.Create(ctx, running))
	queued := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, queued))
	require.NoError(t, queueRepo.SetQueued(ctx, queued.ID, time.Now().UTC()))

	got, err = queueRepo.NextEligible(ctx, 1, 0)
	require.NoError(t, err)
	assert.Nil(t, got, "global cap 1 with one running instance admits nothing")
	got, err = queueRepo.NextEligible(ctx, 0, 1)
	require.NoError(t, err)
	assert.Nil(t, got, "per-user cap 1 with the same owner running admits nothing")
	got, err = queueRepo.NextEligible(ctx, 2, 2)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, queued.ID, got.ID)
}

func TestListWaitingProjections_PreserveGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()

	timer := createGroupTask(t, repo, parent.ID, "group-1", runID, "timer", instance.StateQueued)
	until := time.Now().UTC().Add(time.Hour)
	require.NoError(t, repo.SetWaiting(ctx, timer, &until))

	hook := createGroupTask(t, repo, parent.ID, "group-1", runID, "hook", instance.StateWaiting)
	tok, hash := "tok-group", "hash-group"
	require.NoError(t, repo.SetWebhookFields(ctx, hook, &tok, &hash))

	timers, err := queueRepo.ListWaitingWithTimer(ctx)
	require.NoError(t, err)
	require.Len(t, timers, 1)
	assertGroupLink(t, &timers[0], parent.ID, "group-1", runID, "timer", instance.GroupTaskKindBranch)

	hooks, err := queueRepo.ListWaitingWithWebhook(ctx)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assertGroupLink(t, &hooks[0], parent.ID, "group-1", runID, "hook", instance.GroupTaskKindBranch)
}

// ---------- error ----------

func TestGroupLinkProjections_ClosedDBIsAnError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	require.NoError(t, db.DB.Close())
	ctx := context.Background()

	_, err := repo.ListRunning(ctx)
	assert.Error(t, err)
	_, err = queueRepo.NextEligible(ctx, 0, 0)
	assert.Error(t, err)
	_, err = queueRepo.ListWaitingWithTimer(ctx)
	assert.Error(t, err)
	_, err = queueRepo.ListWaitingWithWebhook(ctx)
	assert.Error(t, err)
	err = repo.Create(ctx, newGroupTaskInstance(uuid.New(), "group-1", uuid.New(), "a", instance.GroupTaskKindBranch))
	assert.Error(t, err)
}
```

- [x] Step 10: Run them, confirm they fail.

```bash
go test ./adapter/sqlite/ -count=1 -run 'PreservesGroupLink|PreserveGroupLink|NextEligible_HandWritten|ClosedDBIsAnError'
```
Expected (the generated queries never select the group columns, so the group child reads as a root):
```
--- FAIL: TestListRunning_PreservesGroupLink (0.01s)
        	Error:      	Expected value not to be nil.
        	Messages:   	parent_instance_id lost
--- FAIL: TestNextEligible_PreservesGroupLink (0.01s)
        	Error:      	Expected value not to be nil.
        	Messages:   	parent_instance_id lost
--- FAIL: TestListWaitingProjections_PreserveGroupLink (0.01s)
        	Error:      	Expected value not to be nil.
        	Messages:   	parent_instance_id lost
FAIL
FAIL	github.com/arbitron/arbitron/adapter/sqlite
```
(`TestNextEligible_HandWrittenKeepsCapsAndEmptySemantics` and `TestGroupLinkProjections_ClosedDBIsAnError` already pass: they are regression guards for the rewrite below.)

- [x] Step 11: Add the shared multi-row scanner. In `adapter/sqlite/instance_spawn_rows.go`, add `"context"` to the import block (lines 3-9) so it reads:

```go
import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
)
```
and append at the end of the file:

```go
// queryInstancesWithLink runs a full-row query whose SELECT list is
// instanceColumnsWithLink and converts every row with scanInstanceWithLink. An
// empty result is an empty, non-nil slice.
func queryInstancesWithLink(ctx context.Context, db *sql.DB, q string, args ...any) ([]instance.Instance, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []instance.Instance{}
	for rows.Next() {
		inst, err := scanInstanceWithLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inst)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [x] Step 12: Hand-write `ListRunning`. In `adapter/sqlite/instance_repo.go`, replace `func (r *InstanceRepo) ListRunning` (base lines 370-385; after cycle A it sits at lines 409-424) with:

```go
// listRunningWithLinkSQL replaces the generated ListRunningInstances so the
// recovery path sees the full link — spawn_depth and the 019 group-task
// columns, which the generated query never selected — instead of reporting a
// group task as a root. queries/instances.sql carries the matching
// source-of-truth SQL.
const listRunningWithLinkSQL = `SELECT ` + instanceColumnsWithLink + ` FROM instances WHERE state = 'running'`

func (r *InstanceRepo) ListRunning(ctx context.Context) ([]instance.Instance, error) {
	instances, err := queryInstancesWithLink(ctx, r.db, listRunningWithLinkSQL)
	if err != nil {
		return nil, fmt.Errorf("listing running instances: %w", err)
	}
	return instances, nil
}
```

and in `rowToInstance`, replace the spawn-link comment (base lines 905-909; after cycle A lines 944-948, the five lines starting `// Spawn link (migration 018), read as an ALL-OR-NONE tuple.`) with:

```go
	// Spawn link (migration 018), read as an ALL-OR-NONE tuple. Callers that scan
	// the link columns separately (scanInstanceWithLink) overwrite these with the
	// same conversion right after; callers whose generated query never selected
	// the columns get the correct root/no-link zero value here rather than
	// silently dropping it.
	//
	// generated.Instance has NO group-task columns (migration 019), so a
	// group-task row converted here alone reads as a root. Every full-row read
	// that can meet a group task therefore goes through scanInstanceWithLink
	// (Get, ListRunning, and the QueueRepo projections); rowToInstance is only
	// the shared base conversion.
```

- [x] Step 13: Hand-write the queue projections. In `adapter/sqlite/queue_repo.go`, replace `NextEligible` (lines 61-74) with:

```go
// The four full-row queue projections below are HAND-WRITTEN over
// instanceColumnsWithLink instead of using the generated NextEligible /
// ListWaitingWithTimer / ListWaitingWithWebhook. The generated statements
// predate spawn_depth and the 019 group-task columns, so a parallel_group task
// child read through them reported IsRoot() == true — the same silent-drop
// failure mode instance_repo_stale_queries_test.go pins for the 018 link. The
// WHERE / ORDER BY clauses are copied verbatim from queries/queue.sql, which
// remains the readable source of truth.
const nextEligibleWithLinkSQL = `SELECT ` + instanceColumnsWithLink + ` FROM instances i
WHERE i.state = 'queued'
  AND (?1 = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running') < ?1)
  AND (?2 = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = i.owner_id) < ?2)
ORDER BY
  CASE i.priority
    WHEN 'high' THEN 1
    WHEN 'normal' THEN 2
    WHEN 'low' THEN 3
  END,
  i.queued_at ASC
LIMIT 1`

const listWaitingWithTimerWithLinkSQL = `SELECT ` + instanceColumnsWithLink +
	` FROM instances WHERE state = 'waiting' AND waiting_until IS NOT NULL AND webhook_token IS NULL`

const listWaitingWithWebhookWithLinkSQL = `SELECT ` + instanceColumnsWithLink +
	` FROM instances WHERE state = 'waiting' AND webhook_token IS NOT NULL`

func (r *QueueRepo) NextEligible(ctx context.Context, globalCap int, perUserCap int) (*instance.Instance, error) {
	row := r.db.QueryRowContext(ctx, nextEligibleWithLinkSQL, int64(globalCap), int64(perUserCap))
	inst, err := scanInstanceWithLink(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting next eligible instance: %w", err)
	}
	return inst, nil
}
```

and replace `ListWaitingWithTimer` + `ListWaitingWithWebhook` (lines 109-139) with:

```go
func (r *QueueRepo) ListWaitingWithTimer(ctx context.Context) ([]instance.Instance, error) {
	instances, err := queryInstancesWithLink(ctx, r.db, listWaitingWithTimerWithLinkSQL)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with timer: %w", err)
	}
	return instances, nil
}

func (r *QueueRepo) ListWaitingWithWebhook(ctx context.Context) ([]instance.Instance, error) {
	instances, err := queryInstancesWithLink(ctx, r.db, listWaitingWithWebhookWithLinkSQL)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with webhook: %w", err)
	}
	return instances, nil
}
```

(`queue_repo.go` still uses `generated` for the counts, `SetQueued` and `ListQueued`, so its imports are unchanged. `ListQueued` (lines 141-158) is intentionally left on the generated query: its only consumer, `domain/queue/positions.go:33`, reads ids/priorities, and it never selected the 018 link either.)

- [x] Step 14: Document the hand-written statements in the sqlc sources. In `adapter/sqlite/queries/instances.sql`, replace lines 61-62 (`-- name: ListRunningInstances :many` + its SELECT) with:

```sql
-- name: ListRunningInstances :many
-- NOTE: executed hand-written by InstanceRepo.ListRunning
-- (listRunningWithLinkSQL over instanceColumnsWithLink) so spawn_depth and the
-- 019 group-task columns are read in the same statement.
SELECT * FROM instances WHERE state = 'running';
```

In `adapter/sqlite/queries/queue.sql`, replace lines 13-14 (`-- name: NextEligible :one` + `SELECT * FROM instances i`) with:

```sql
-- name: NextEligible :one
-- NOTE: NextEligible, ListWaitingWithTimer and ListWaitingWithWebhook are
-- executed hand-written by QueueRepo (nextEligibleWithLinkSQL,
-- listWaitingWithTimerWithLinkSQL, listWaitingWithWebhookWithLinkSQL over
-- instanceColumnsWithLink) so spawn_depth and the 019 group-task columns are
-- read in the same statement. Keep the predicates here in lockstep.
SELECT * FROM instances i
```

- [x] Step 15: Run everything, confirm it passes; gofmt and vet.

```bash
go test ./adapter/sqlite/ -count=1 -run 'PreservesGroupLink|PreserveGroupLink|NextEligible_HandWritten|ClosedDBIsAnError|PreservesSpawnLink|PreservesProvenance' -v
go test ./adapter/sqlite/ ./domain/... ./cmd/... -count=1
gofmt -l adapter/sqlite
go vet ./adapter/sqlite/
```
Expected:
```
--- PASS: TestListRunning_PreservesGroupLink (0.02s)
--- PASS: TestNextEligible_PreservesGroupLink (0.01s)
--- PASS: TestNextEligible_HandWrittenKeepsCapsAndEmptySemantics (0.01s)
--- PASS: TestListWaitingProjections_PreserveGroupLink (0.01s)
--- PASS: TestGroupLinkProjections_ClosedDBIsAnError (0.01s)
--- PASS: TestNextEligible_PreservesProvenance (0.01s)
--- PASS: TestListWaitingWithTimer_PreservesProvenance (0.01s)
--- PASS: TestListWaitingWithWebhook_PreservesProvenance (0.01s)
--- PASS: TestListRunning_PreservesProvenance (0.01s)
--- PASS: TestListRunning_PreservesSpawnLink (0.01s)
--- PASS: TestNextEligible_PreservesSpawnLink (0.01s)
--- PASS: TestListWaitingWithTimer_PreservesSpawnLink (0.01s)
--- PASS: TestListWaitingWithWebhook_PreservesSpawnLink (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/sqlite	0.5XXs
```
then `ok` for every package of the second command (the pre-existing `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` appears only under `./adapter/...`/`./...` and fails identically on the base commit); `gofmt -l` and `go vet` print nothing. (The `-run` pattern deliberately also re-runs the pre-existing provenance / spawn-link projection tests of `instance_repo_stale_queries_test.go`: the hand-written statements must keep everything the generated ones carried.)

### Execution evidence and self-review

- Cycle A RED: the specified tests failed on missing 019 columns/tables and the 018 `spawn_mode` CHECK before implementation. Cycle A GREEN: focused tests and the full SQLite package passed.
- Cycle B RED: ListRunning, NextEligible and both waiting projections lost the parent/group link through the generated queries. Cycle B GREEN: all projection, legacy spawn-link and provenance guards passed.
- Spec-compliance FIRST: matched the Phase 2 design and alignment contract. Added only migration 019, complete group-link persistence/decoding and the four specified hand-written projections. No generated files, instance rebuild, subtree-cancel behavior or Phase 3 fields were added. ListQueued remains unchanged as specified.
- Anti-green-theater SECOND: real embedded migrations and SQLite repos feed stored rows into the actual read sinks, including hierarchy/cancel-query results. Strengthened the INSERT-order guard with an AFTER INSERT capture: a temporary production mutation that inserted NULL group fields then populated them by UPDATE failed this test, even though the eventual row was complete. Removed the mutation. Added a populated-018 upgrade test that checks FK enforcement and retained node execution output, cross-run terminal-sequence uniqueness and reserved-child uniqueness; every projection asserts nonempty linkage and depth.
- Wiring/provenance: verified `cmd/arbitron/main.go` already applies embedded migrations and constructs these InstanceRepo/QueueRepo implementations. Group-link stamping/roster production is explicitly owned by P2-T7/T8 and full execution wiring/sink coverage by P2-T14; this storage slice does not enable group execution early or substitute empty production inputs.
- Code-quality THIRD: retained the shared base converter and ordered projection, contextual errors and neighboring nullable/UUID conventions; clarified comments and formatted the added code. No temporary mutation remains.
- Final verification after review: focused tests, `go test ./adapter/sqlite/ ./domain/... ./cmd/... -count=1`, `gofmt -l adapter/sqlite`, `go vet ./adapter/sqlite/` and `git diff --check` all passed. No unrelated suite exemption was needed.

- [x] Step 16: Commit.

```bash
git add adapter/sqlite/migrations/019_parallel_groups.up.sql adapter/sqlite/migrations/019_parallel_groups.down.sql adapter/sqlite/instance_repo.go adapter/sqlite/instance_spawn_rows.go adapter/sqlite/convert.go adapter/sqlite/queue_repo.go adapter/sqlite/queries/instances.sql adapter/sqlite/queries/queue.sql adapter/sqlite/instance_group_link_test.go adapter/sqlite/instance_group_link_projection_test.go
git commit -m "feat(sqlite): migration 019 parallel groups and group-task link persistence on every instance read path" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
