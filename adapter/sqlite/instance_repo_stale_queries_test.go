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

// These are characterization tests for a latent data-loss bug: six sqlc-derived
// instance queries were hand-frozen in generated/*.sql.go with a stale column
// list that omitted the trigger_source / schedule_id / workflow_id columns added
// by later migrations. Because rowToInstance reads those generated-struct fields,
// any query whose Scan does not populate them silently returns zero-valued
// TriggerSource / ScheduleID / WorkflowID on the domain Instance.
//
// Each test below drives a genuinely production-wired repo method and asserts the
// three identity fields round-trip. They fail before the generated-query fix and
// pass after.

// newProvenancedInstance builds an instance carrying a non-default trigger
// source, schedule id, and workflow id, in the given state.
func newProvenancedInstance(state instance.State, sid, wid uuid.UUID) *instance.Instance {
	return &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowID:       wid,
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            state,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
		TriggerSource:    instance.OriginSchedule,
		ScheduleID:       &sid,
	}
}

func assertProvenance(t *testing.T, got *instance.Instance, sid, wid uuid.UUID) {
	t.Helper()
	assert.Equal(t, instance.OriginSchedule, got.TriggerSource, "trigger_source lost")
	require.NotNil(t, got.ScheduleID, "schedule_id lost (nil)")
	assert.Equal(t, sid, *got.ScheduleID, "schedule_id wrong")
	assert.Equal(t, wid, got.WorkflowID, "workflow_id lost")
}

// NextEligible is the live dispatcher pick. It must not drop provenance.
func TestNextEligible_PreservesProvenance(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	sid, wid := uuid.New(), uuid.New()

	inst := newProvenancedInstance(instance.StateQueued, sid, wid)
	require.NoError(t, instRepo.Create(context.Background(), inst))
	require.NoError(t, queueRepo.SetQueued(context.Background(), inst.ID, time.Now().UTC()))

	got, err := queueRepo.NextEligible(context.Background(), 0, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, inst.ID, got.ID)
	assertProvenance(t, got, sid, wid)
}

// ListWaitingWithTimer feeds the timer reaper; its instances must keep identity.
func TestListWaitingWithTimer_PreservesProvenance(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	sid, wid := uuid.New(), uuid.New()

	inst := newProvenancedInstance(instance.StateWaiting, sid, wid)
	require.NoError(t, instRepo.Create(context.Background(), inst))
	until := time.Now().UTC().Add(time.Hour)
	require.NoError(t, instRepo.SetWaiting(context.Background(), inst.ID, &until))

	rows, err := queueRepo.ListWaitingWithTimer(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertProvenance(t, &rows[0], sid, wid)
}

// ListWaitingWithWebhook feeds the webhook resumer; identity must survive.
func TestListWaitingWithWebhook_PreservesProvenance(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	sid, wid := uuid.New(), uuid.New()

	inst := newProvenancedInstance(instance.StateWaiting, sid, wid)
	require.NoError(t, instRepo.Create(context.Background(), inst))
	tok := "tok-abc"
	hash := "hash-abc"
	require.NoError(t, instRepo.SetWebhookFields(context.Background(), inst.ID, &tok, &hash))

	rows, err := queueRepo.ListWaitingWithWebhook(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertProvenance(t, &rows[0], sid, wid)
}

// ListRunning is used by reconciliation/recovery on startup. Its generated Scan
// dropped only workflow_id (trigger_source / schedule_id were present), so this
// test specifically guards the workflow_id round-trip.
func TestListRunning_PreservesProvenance(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	sid, wid := uuid.New(), uuid.New()

	inst := newProvenancedInstance(instance.StateRunning, sid, wid)
	require.NoError(t, instRepo.Create(context.Background(), inst))

	rows, err := instRepo.ListRunning(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertProvenance(t, &rows[0], sid, wid)
}

// The same four generated queries characterized above (NextEligible,
// ListWaitingWithTimer, ListWaitingWithWebhook, ListRunning) went stale a
// second time: migration 018 added parent_instance_id / parent_node_id /
// spawn_mode, and none of the four was updated to select or scan them. Every
// Instance these paths return therefore reports a zero-value spawn link
// (IsRoot() == true, SpawnMode == "") regardless of what is actually stored,
// the exact same silent-drop failure mode the provenance tests above pin for
// trigger_source / schedule_id / workflow_id.

func assertSpawnLink(t *testing.T, got *instance.Instance, parentID uuid.UUID, parentNodeID string, mode instance.SpawnMode) {
	t.Helper()
	require.NotNil(t, got.ParentInstanceID, "parent_instance_id lost (nil)")
	assert.Equal(t, parentID, *got.ParentInstanceID, "parent_instance_id wrong")
	require.NotNil(t, got.ParentNodeID, "parent_node_id lost (nil)")
	assert.Equal(t, parentNodeID, *got.ParentNodeID, "parent_node_id wrong")
	assert.Equal(t, mode, got.SpawnMode, "spawn_mode lost or wrong")
}

func TestListRunning_PreservesSpawnLink(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, instRepo.Create(ctx, parent))
	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
	})
	child.State = instance.StateRunning
	require.NoError(t, instRepo.Create(ctx, child))

	rows, err := instRepo.ListRunning(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertSpawnLink(t, &rows[0], parent.ID, "spawn-1", instance.SpawnModeFireAndForget)
}

func TestNextEligible_PreservesSpawnLink(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	parent.State = instance.StateRunning
	require.NoError(t, instRepo.Create(ctx, parent))
	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild,
	})
	require.NoError(t, instRepo.Create(ctx, child))
	require.NoError(t, queueRepo.SetQueued(ctx, child.ID, time.Now().UTC()))

	got, err := queueRepo.NextEligible(ctx, 0, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	assertSpawnLink(t, got, parent.ID, "spawn-1", instance.SpawnModeWaitForChild)
}

func TestListWaitingWithTimer_PreservesSpawnLink(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, instRepo.Create(ctx, parent))
	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget,
	})
	require.NoError(t, instRepo.Create(ctx, child))
	until := time.Now().UTC().Add(time.Hour)
	require.NoError(t, instRepo.SetWaiting(ctx, child.ID, &until))

	rows, err := queueRepo.ListWaitingWithTimer(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertSpawnLink(t, &rows[0], parent.ID, "spawn-1", instance.SpawnModeFireAndForget)
}

func TestListWaitingWithWebhook_PreservesSpawnLink(t *testing.T) {
	db := setupTestDB(t)
	instRepo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()

	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, instRepo.Create(ctx, parent))
	child := newLinkedInstance(instance.SpawnLink{
		ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeWaitForChild,
	})
	child.State = instance.StateWaiting
	require.NoError(t, instRepo.Create(ctx, child))
	tok := "tok-spawn"
	hash := "hash-spawn"
	require.NoError(t, instRepo.SetWebhookFields(ctx, child.ID, &tok, &hash))

	rows, err := queueRepo.ListWaitingWithWebhook(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertSpawnLink(t, &rows[0], parent.ID, "spawn-1", instance.SpawnModeWaitForChild)
}
