package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snapshotRun(state instance.State, pins map[string]string, version int, backend string) *instance.Instance {
	inst := pinnedInstance(pins, version)
	inst.State = state
	inst.WorkflowSnapshot = map[string]any{
		"name": "wf",
		"nodes": []any{map[string]any{
			"id": "n", "type": "agent_call",
			"config": map[string]any{"model_profile": "worker", "backend": backend},
		}},
	}
	return inst
}

// Historical rows are fixture data, never an ordinary post-migration Create.
func createLegacySnapshotRun(t *testing.T, repo *InstanceRepo, inst *instance.Instance) {
	t.Helper()
	row := *inst
	row.ModelPins = map[string]string{}
	row.ModelPinsVersion = instance.ModelPinsVersionSnapshot
	require.NoError(t, repo.Create(context.Background(), &row))
	_, err := repo.db.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, inst.ID.String())
	require.NoError(t, err)
}

func snapshotIDs(snaps []instance.ExecutionSnapshot) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, s.InstanceID)
	}
	return out
}

// Happy/success: every live-resolving (legacy NULL-pin) run that can still
// execute a node — queued, running, waiting, paused, or failed and therefore
// retryable — is returned with its exact workflow snapshot. Completed and
// cancelled runs never execute again, and a pinned run executes only its pins,
// so neither consumes an advanced profile model.
func TestListExecutionSnapshotsReturnsLiveResolvingRuns(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	want := map[uuid.UUID]string{}
	for _, st := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused, instance.StateFailed} {
		inst := snapshotRun(st, nil, instance.ModelPinsVersionLegacy, "backend-"+string(st))
		createLegacySnapshotRun(t, repo, inst)
		want[inst.ID] = "backend-" + string(st)
	}
	for _, st := range []instance.State{instance.StateCompleted, instance.StateCancelled} {
		createLegacySnapshotRun(t, repo, snapshotRun(st, nil, instance.ModelPinsVersionLegacy, "terminal"))
	}
	require.NoError(t, repo.Create(ctx, snapshotRun(instance.StateRunning, map[string]string{"worker": "vendor/x-1"}, instance.ModelPinsVersionSnapshot, "pinned")))

	snaps, err := repo.ListExecutionSnapshots(ctx)

	require.NoError(t, err)
	assert.ElementsMatch(t, keysOf(want), snapshotIDs(snaps))
	for _, s := range snaps {
		nodes := s.WorkflowSnapshot["nodes"].([]any)
		cfg := nodes[0].(map[string]any)["config"].(map[string]any)
		assert.Equal(t, want[s.InstanceID], cfg["backend"], "the stored snapshot is returned unchanged")
	}
}

// Edge: no runs is an empty, successful listing.
func TestListExecutionSnapshotsEmpty(t *testing.T) {
	db := setupTestDB(t)

	snaps, err := NewInstanceRepo(db.DB).ListExecutionSnapshots(context.Background())

	require.NoError(t, err)
	assert.Empty(t, snaps)
}

// An unreadable legacy workflow still fails inventory: its execution routes
// cannot safely be inferred from the live workflow.
func TestListExecutionSnapshotsCorruptRowFails(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"unparseable snapshot": `UPDATE instances SET workflow_snapshot = '{truncated' WHERE id = ?`,
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewInstanceRepo(db.DB)
			inst := snapshotRun(instance.StateRunning, nil, instance.ModelPinsVersionLegacy, "pi")
			createLegacySnapshotRun(t, repo, inst)
			_, err := db.DB.Exec(corrupt, inst.ID.String())
			require.NoError(t, err)

			snaps, err := repo.ListExecutionSnapshots(ctx)

			require.Error(t, err)
			assert.Nil(t, snaps)
		})
	}
}

// Error: a cancelled context fails the read.
func TestListExecutionSnapshotsCancelledContext(t *testing.T) {
	db := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewInstanceRepo(db.DB).ListExecutionSnapshots(ctx)

	assert.ErrorIs(t, err, context.Canceled)
}

func keysOf(m map[uuid.UUID]string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A corrupt SQLite storage type must neither abort the inventory nor turn a
// NULL payload into a legacy run. The inventory is read-only: it skips the
// corrupt run and leaves its state and offending value untouched.
func TestListExecutionSnapshotsIsolatesRawPinVersionTypes(t *testing.T) {
	for _, tc := range []struct {
		name, storage string
		version       any
	}{
		{"text", "text", "broken"},
		{"real", "real", 0.5},
		{"blob resembling legacy", "blob", []byte("0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewInstanceRepo(db.DB)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel() // a cursor held during quarantine would deadlock
			good := snapshotRun(instance.StateRunning, nil, 0, "pi")
			createLegacySnapshotRun(t, repo, good)
			require.NoError(t, repo.Create(ctx, snapshotRun(instance.StateRunning, map[string]string{}, 1, "pinned")))
			var badIDs []uuid.UUID
			for _, state := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused, instance.StateFailed} {
				for _, pins := range []any{nil, `{}`} {
					bad := snapshotRun(state, map[string]string{}, 1, "corrupt")
					require.NoError(t, repo.Create(ctx, bad))
					_, err := db.DB.Exec(`UPDATE instances SET model_pins = ?, model_pins_version = ?, current_node_id = 'n' WHERE id = ?`, pins, tc.version, bad.ID.String())
					require.NoError(t, err)
					badIDs = append(badIDs, bad.ID)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				snaps, err := repo.ListExecutionSnapshots(ctx)
				require.NoError(t, err)
				require.Len(t, snaps, 1)
				assert.Equal(t, good.ID, snaps[0].InstanceID)
				assert.Equal(t, good.WorkflowSnapshot, snaps[0].WorkflowSnapshot)
				for i, id := range badIDs {
					var state, storage string
					var pins sql.NullString
					var version any
					require.NoError(t, db.DB.QueryRow(`SELECT state, model_pins, model_pins_version, typeof(model_pins_version) FROM instances WHERE id = ?`, id.String()).Scan(&state, &pins, &version, &storage))
					assert.Equal(t, string([]instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused, instance.StateFailed}[i/2]), state, "inventory reads never transition a run")
					assert.Equal(t, tc.storage, storage)
					assert.Equal(t, tc.version, version)
					assert.Equal(t, i%2 == 1, pins.Valid)
					if pins.Valid {
						assert.Equal(t, `{}`, pins.String)
					}
				}
			}
			healthy, err := repo.Get(ctx, good.ID)
			require.NoError(t, err)
			assert.Equal(t, instance.StateRunning, healthy.State)
		})
	}
}
