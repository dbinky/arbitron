package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const spawnTestOwner = "00000000-0000-0000-0000-000000000001"

// newLinkedInstance builds a queued instance carrying the given spawn link.
// Pass a zero SpawnLink for a root run.
func newLinkedInstance(link instance.SpawnLink) *instance.Instance {
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse(spawnTestOwner),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	if link.Valid() {
		p := link.ParentInstanceID
		n := link.ParentNodeID
		inst.ParentInstanceID = &p
		inst.ParentNodeID = &n
		inst.SpawnMode = link.Mode
	}
	return inst
}

// ---------- migration ----------

func TestMigration018_AddsLinkColumnsAndIndex(t *testing.T) {
	db := setupTestDB(t)

	cols := map[string]bool{}
	rows, err := db.DB.Query(`PRAGMA table_info(instances)`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk))
		cols[name] = notNull == 1
	}
	for _, want := range []string{"parent_instance_id", "parent_node_id", "spawn_mode"} {
		notNull, ok := cols[want]
		require.True(t, ok, "migration 018 must add column %s", want)
		assert.False(t, notNull, "%s must be NULLABLE: every pre-existing and manually triggered run has a null link", want)
	}

	var idx int
	require.NoError(t, db.DB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_instances_parent'`).Scan(&idx))
	assert.Equal(t, 1, idx, "migration 018 must create idx_instances_parent for parent-keyed lookups")
}

// ---------- success: round trip ----------

func TestInstanceRepo_SpawnLinkRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	link := instance.SpawnLink{
		ParentInstanceID: parent.ID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeWaitForChild,
	}
	child := newLinkedInstance(link)
	require.NoError(t, repo.Create(ctx, child))

	got, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ParentInstanceID, "the link must be readable back off the child row")
	assert.Equal(t, parent.ID, *got.ParentInstanceID)
	require.NotNil(t, got.ParentNodeID)
	assert.Equal(t, "spawn-1", *got.ParentNodeID)
	assert.Equal(t, instance.SpawnModeWaitForChild, got.SpawnMode)
	assert.False(t, got.IsRoot())
}

// TestInstanceRepo_LinkWrittenInTheCreatingStatement is the anti-regression
// guard for the ordering invariant: the link must exist on the row the moment
// Create returns, because TriggerWithDepartment enqueues immediately after and
// the child may reach a terminal state before any follow-up write could land.
func TestInstanceRepo_LinkWrittenInTheCreatingStatement(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))

	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID,
		ParentNodeID:     "spawn-1",
		Mode:             instance.SpawnModeFireAndForget,
	})
	require.NoError(t, repo.Create(ctx, child))

	// Read the raw row, bypassing the repo, to prove no second statement was
	// needed to populate the link.
	var pid, pnode, mode any
	require.NoError(t, db.DB.QueryRow(
		`SELECT parent_instance_id, parent_node_id, spawn_mode FROM instances WHERE id = ?`,
		child.ID.String()).Scan(&pid, &pnode, &mode))
	assert.Equal(t, parent.ID.String(), pid)
	assert.Equal(t, "spawn-1", pnode)
	assert.Equal(t, "fire_and_forget", mode)
}

// ---------- edge case: null links ----------

func TestInstanceRepo_RootInstanceHasNullLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	root := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, root))

	got, err := repo.Get(ctx, root.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ParentInstanceID)
	assert.Nil(t, got.ParentNodeID)
	assert.Equal(t, instance.SpawnMode(""), got.SpawnMode)
	assert.True(t, got.IsRoot(), "a run with no spawning parent must read back as a root")
}

// TestInstanceRepo_PreExistingRowReadsAsRoot: a row inserted WITHOUT the link
// columns (the shape every instance created before this feature has) must read
// back as a root rather than erroring.
func TestInstanceRepo_PreExistingRowReadsAsRoot(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, visibility, trigger_source)
		 VALUES (?, ?, 'legacy', '{}', 'completed', 'normal', '{}', '{}', 'private', 'manual')`,
		id.String(), spawnTestOwner)
	require.NoError(t, err)

	got, err := repo.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, got.IsRoot())
}

// ---------- error ----------

func TestInstanceRepo_GetMissingInstanceIsNotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	_, err := repo.Get(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// TestInstanceRepo_CreateStillStampsEveryPreExistingField proves the
// hand-written INSERT did not silently drop a column the generated query wrote.
func TestInstanceRepo_CreateStillStampsEveryPreExistingField(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	sid := uuid.New()
	wid := uuid.New()
	inst := newLinkedInstance(instance.SpawnLink{})
	inst.WorkflowID = wid
	inst.Department = "engineering"
	inst.Visibility = instance.VisibilityDepartment
	inst.TriggerSource = instance.OriginSchedule
	inst.ScheduleID = &sid
	inst.Priority = instance.PriorityHigh
	inst.OriginalVariables = map[string]any{"seed": "v"}
	inst.Variables = map[string]any{"seed": "v"}
	require.NoError(t, repo.Create(ctx, inst))
	assert.False(t, inst.CreatedAt.IsZero(), "Create must populate CreatedAt from the RETURNING clause")
	assert.False(t, inst.UpdatedAt.IsZero(), "Create must populate UpdatedAt from the RETURNING clause")

	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, wid, got.WorkflowID)
	assert.Equal(t, "engineering", got.Department)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource)
	require.NotNil(t, got.ScheduleID)
	assert.Equal(t, sid, *got.ScheduleID)
	assert.Equal(t, instance.PriorityHigh, got.Priority)
	assert.Equal(t, map[string]any{"seed": "v"}, got.OriginalVariables)
}
