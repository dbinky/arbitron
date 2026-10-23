package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGroupTaskInstance builds a queued parallel_group task child.
func newGroupTaskInstance(parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, kind instance.GroupTaskKind) *instance.Instance {
	inst := newLinkedInstance(instance.SpawnLink{})
	inst.ParentInstanceID = &parentID
	inst.ParentNodeID = &nodeID
	inst.SpawnMode = instance.SpawnModeGroupTask
	inst.GroupRunID = &runID
	inst.GroupTaskKey = &key
	inst.GroupTaskKind = kind
	inst.SpawnDepth = 1
	return inst
}

func createGroupTask(t *testing.T, repo *InstanceRepo, parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, state instance.State) uuid.UUID {
	t.Helper()
	c := newGroupTaskInstance(parentID, nodeID, runID, key, instance.GroupTaskKindBranch)
	c.State = state
	// Production persists the roster before any child can become terminal.
	// These link/query fixtures must obey that same atomic-capture invariant.
	_, err := repo.db.Exec(`INSERT INTO group_runs (id, parent_instance_id, node_id, attempt,
		form, completion_rule, unfinished_behavior, gather_mode, state, created_at, updated_at)
		SELECT ?, ?, ?, 1, 'branches', 'all_succeed', 'cancel_unfinished', 'results',
		'launching', strftime('%Y-%m-%dT%H:%M:%SZ'), strftime('%Y-%m-%dT%H:%M:%SZ')
		WHERE NOT EXISTS (SELECT 1 FROM group_runs WHERE id=?)`, runID.String(), parentID.String(), nodeID, runID.String())
	require.NoError(t, err)
	_, err = repo.db.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind,
		child_instance_id, updated_at)
		SELECT ?, COUNT(*), ?, 'branch', ?, strftime('%Y-%m-%dT%H:%M:%SZ')
		FROM group_tasks WHERE group_run_id=?`, runID.String(), key, c.ID.String(), runID.String())
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

func assertGroupLink(t *testing.T, got *instance.Instance, parentID uuid.UUID, nodeID string, runID uuid.UUID, key string, kind instance.GroupTaskKind) {
	t.Helper()
	require.NotNil(t, got.ParentInstanceID, "parent_instance_id lost")
	assert.Equal(t, parentID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID, "parent_node_id lost")
	assert.Equal(t, nodeID, *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeGroupTask, got.SpawnMode)
	require.NotNil(t, got.GroupRunID, "group_run_id lost")
	assert.Equal(t, runID, *got.GroupRunID)
	require.NotNil(t, got.GroupTaskKey, "group_task_key lost")
	assert.Equal(t, key, *got.GroupTaskKey)
	assert.Equal(t, kind, got.GroupTaskKind)
	assert.True(t, got.IsGroupTask())
	assert.False(t, got.IsRoot())
}

// rawLinkColumns bypasses the repository to inspect the stored tuple.
func rawLinkColumns(t *testing.T, db testDB, id uuid.UUID) (pid, pnode, mode, run, key, kind any) {
	t.Helper()
	require.NoError(t, db.DB.QueryRow(`SELECT parent_instance_id, parent_node_id, spawn_mode,
		group_run_id, group_task_key, group_task_kind FROM instances WHERE id = ?`,
		id.String()).Scan(&pid, &pnode, &mode, &run, &key, &kind))
	return
}

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
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	for _, want := range []string{"group_run_id", "group_task_key", "group_task_kind", "subtree_cancel_requested_at"} {
		notNull, ok := cols[want]
		require.True(t, ok, "migration 019 must add instances.%s", want)
		assert.False(t, notNull, "instances.%s must be nullable", want)
	}
	for _, table := range []string{"group_runs", "group_tasks"} {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		assert.Equal(t, 1, n, "migration 019 must create table %s", table)
	}
	for _, idx := range []string{"idx_instances_group_run", "idx_group_runs_unsettled", "idx_group_tasks_terminal_seq"} {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n))
		assert.Equal(t, 1, n, "migration 019 must create index %s", idx)
	}
}

// Upgrade a populated 018 database, with FK enforcement enabled, to catch
// accidental table rebuilds that silently cascade-delete execution history.
func TestMigration019_PreservesExistingExecutionHistory(t *testing.T) {
	db, err := Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	entries, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() >= "019_" || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		migration, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, err)
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	var fk int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	require.Equal(t, 1, fk)
	id, executionID := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot,
		parent_instance_id, parent_node_id, spawn_mode)
		VALUES (?, ?, 'legacy', '{}', ?, 'spawn-1', 'wait_for_child')`, id.String(), spawnTestOwner, uuid.New().String())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO node_executions (id, instance_id, node_id, status, output)
		VALUES (?, ?, 'script-1', 'succeeded', '{"value":"retained"}')`, executionID.String(), id.String())
	require.NoError(t, err)
	migration, err := migrationsFS.ReadFile("migrations/019_parallel_groups.up.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	var output string
	require.NoError(t, db.QueryRow(`SELECT output FROM node_executions WHERE id = ?`, executionID.String()).Scan(&output))
	assert.JSONEq(t, `{"value":"retained"}`, output)
	// The current repository reads the current schema: apply every later
	// migration too, so the legacy row must also survive the rest of the chain.
	for _, entry := range entries {
		if entry.Name() <= "019_parallel_groups.up.sql" || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		later, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, err)
		_, err = db.Exec(string(later))
		require.NoError(t, err, entry.Name())
	}
	got, err := NewInstanceRepo(db).Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
	assert.False(t, got.IsGroupTask())
}

func TestMigration019_DownDropsGroupTables(t *testing.T) {
	db := setupTestDB(t)
	root := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, NewInstanceRepo(db.DB).Create(context.Background(), root))
	down, err := migrationsFS.ReadFile("migrations/019_parallel_groups.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	for _, table := range []string{"group_runs", "group_tasks"} {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n))
		assert.Zero(t, n, "%s must be gone after down", table)
	}
	var n int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE id = ?`, root.ID.String()).Scan(&n))
	assert.Equal(t, 1, n, "instances rows must survive down")
}

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
	parent, runID := uuid.New().String(), uuid.New().String()
	require.NoError(t, insertRun(runID, parent, "group-1", 1, "running", nil, nil, 0))
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-1", 1, "running", nil, nil, 0), "unique parent/node/attempt")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-2", 1, "finished", nil, nil, 0), "unknown state")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-3", 1, "running", `{"outcome":"success"}`, nil, 0), "decision while running")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-4", 1, "decided", `{}`, `{}`, 0), "final output before done")
	assert.Error(t, insertRun(uuid.New().String(), parent, "group-5", 1, "decided", `{}`, nil, 1), "ready without output")
	assert.NoError(t, insertRun(uuid.New().String(), parent, "group-6", 1, "gathering", `{}`, nil, 0))

	insertTask := func(pos int, key, child, live string, seq, at any) error {
		_, err := db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
			child_instance_id, live_status, terminal_seq, terminal_at, updated_at)
			VALUES (?, ?, ?, 'branch', 'entry', ?, ?, ?, ?, ?)`, runID, pos, key, child, live, seq, at, now)
		return err
	}
	reservedChild := uuid.New().String()
	require.NoError(t, insertTask(0, "a", reservedChild, "running", nil, nil))
	assert.Error(t, insertTask(7, "duplicate-child", reservedChild, "running", nil, nil), "a reserved child belongs to one task")
	require.NoError(t, insertTask(1, "b", uuid.New().String(), "completed", 1, now))
	assert.Error(t, insertTask(2, "c", uuid.New().String(), "unfinished", nil, nil), "unfinished is frozen-only")
	assert.Error(t, insertTask(3, "d", uuid.New().String(), "running", 2, nil), "seq requires time")
	assert.Error(t, insertTask(4, "e", uuid.New().String(), "failed", nil, nil), "terminal requires seq")
	assert.Error(t, insertTask(5, "f", uuid.New().String(), "cancelled", 1, now), "unique terminal seq")
	assert.Error(t, insertTask(6, "a", uuid.New().String(), "running", nil, nil), "unique task key")
	assert.Error(t, insertTask(0, "g", uuid.New().String(), "running", nil, nil), "unique position")
	_, err := db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
		child_instance_id, updated_at) VALUES (?, 9, 'h', 'spawn', 'x', ?, ?)`, runID, uuid.New().String(), now)
	assert.Error(t, err, "unknown kind")
	_, err = db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind, origin,
		child_instance_id, updated_at) VALUES (?, 0, 'a', 'branch', 'x', ?, ?)`, uuid.New().String(), uuid.New().String(), now)
	assert.Error(t, err, "missing run FK")
	otherRun := uuid.New().String()
	require.NoError(t, insertRun(otherRun, parent, "other-group", 1, "running", nil, nil, 0))
	_, err = db.DB.Exec(`INSERT INTO group_tasks (group_run_id, position, task_key, kind,
		child_instance_id, live_status, terminal_seq, terminal_at, updated_at)
		VALUES (?, 0, 'other', 'workflow', ?, 'completed', 1, ?, ?)`, otherRun, uuid.New().String(), now, now)
	assert.Error(t, err, "terminal sequence uniqueness spans runs, not just tasks of one run")
}

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
		assert.Equal(t, 1, got.SpawnDepth)
	}
}

func TestInstanceRepo_GroupBranchEntryWrittenInTheCreatingStatement(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	child := newGroupTaskInstance(parent.ID, "group-1", uuid.New(), "a", instance.GroupTaskKindBranch)
	entry := "a1"
	child.CurrentNodeID = &entry
	_, err := db.DB.Exec(`CREATE TABLE branch_entry_at_insert (node_id TEXT);
		CREATE TRIGGER capture_branch_entry AFTER INSERT ON instances BEGIN
		INSERT INTO branch_entry_at_insert VALUES (NEW.current_node_id);
		END;`)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, child))
	var atInsert *string
	require.NoError(t, db.DB.QueryRow(`SELECT node_id FROM branch_entry_at_insert`).Scan(&atInsert))
	assert.Equal(t, &entry, atInsert, "the queue may start the branch immediately after Create")
	got, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, &entry, got.CurrentNodeID)
}

func TestInstanceRepo_GroupLinkWrittenInTheCreatingStatement(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(context.Background(), parent))
	runID := uuid.New()
	child := newGroupTaskInstance(parent.ID, "group-1", runID, "a", instance.GroupTaskKindWorkflow)
	// Observe the INSERT itself: a SELECT after Create alone would also pass
	// if production wrongly populated the link in a follow-up UPDATE.
	_, err := db.DB.Exec(`CREATE TABLE group_link_at_insert AS SELECT id,
		parent_instance_id, parent_node_id, spawn_mode, group_run_id, group_task_key,
		group_task_kind FROM instances WHERE 0;
		CREATE TRIGGER capture_group_link AFTER INSERT ON instances BEGIN
		INSERT INTO group_link_at_insert VALUES (NEW.id, NEW.parent_instance_id,
		NEW.parent_node_id, NEW.spawn_mode, NEW.group_run_id, NEW.group_task_key, NEW.group_task_kind);
		END;`)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), child))
	pid, pnode, mode, run, key, kind := rawLinkColumns(t, db, child.ID)
	assert.Equal(t, []any{parent.ID.String(), "group-1", nil, runID.String(), "a", "workflow"},
		[]any{pid, pnode, mode, run, key, kind}, "stored tuple after Create")
	require.NoError(t, db.DB.QueryRow(`SELECT parent_instance_id, parent_node_id, spawn_mode,
		group_run_id, group_task_key, group_task_kind FROM group_link_at_insert WHERE id = ?`,
		child.ID.String()).Scan(&pid, &pnode, &mode, &run, &key, &kind))
	assert.Equal(t, parent.ID.String(), pid)
	assert.Equal(t, "group-1", pnode)
	assert.Nil(t, mode)
	assert.Equal(t, runID.String(), run)
	assert.Equal(t, "a", key)
	assert.Equal(t, "workflow", kind)
}

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
			require.NoError(t, repo.Create(ctx, inst))
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
	_, err := db.DB.Exec(`UPDATE instances SET group_run_id = ?, group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`, uuid.New().String(), legacy.String())
	assert.Error(t, err, "group link alongside legacy mode")
	_, err = db.DB.Exec(`UPDATE instances SET group_run_id = ?, group_task_key = 'a', group_task_kind = 'branch' WHERE id = ?`, uuid.New().String(), root.ID.String())
	assert.Error(t, err, "group link without parent")
	_, err = db.DB.Exec(`UPDATE instances SET parent_instance_id = ?, parent_node_id = 'g', group_run_id = ?, group_task_kind = 'branch' WHERE id = ?`, parent.ID.String(), uuid.New().String(), root.ID.String())
	assert.Error(t, err, "group link without task key")
	_, err = db.DB.Exec(`UPDATE instances SET group_task_kind = 'branches' WHERE id = ?`, root.ID.String())
	assert.Error(t, err, "unknown kind")
}

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
	assert.Empty(t, cands)
}

func TestListActiveChildren_IncludesGroupTasks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	running := createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateRunning)
	queued := createGroupTask(t, repo, parent.ID, "group-1", runID, "b", instance.StateQueued)
	createGroupTask(t, repo, parent.ID, "group-1", runID, "c", instance.StateCompleted)
	legacy := childOf(t, repo, parent.ID, "spawn-1", instance.SpawnModeFireAndForget,
		instance.StateRunning, uuid.MustParse(spawnTestOwner), instance.VisibilityPrivate, "")
	got, err := repo.ListActiveChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{running, queued, legacy}, got)
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
	assert.Equal(t, 2, counts[parent.ID])
	roots, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeRoots})
	require.NoError(t, err)
	rootIDs := idsOf(roots)
	assert.True(t, rootIDs[parent.ID])
	assert.False(t, rootIDs[a])
	assert.False(t, rootIDs[b])
	children, err := repo.List(ctx, instance.ListFilters{Unconstrained: true, ParentScope: instance.ParentScopeChildren, ParentInstanceID: &parent.ID})
	require.NoError(t, err)
	assert.Equal(t, map[uuid.UUID]bool{a: true, b: true}, idsOf(children))
}
