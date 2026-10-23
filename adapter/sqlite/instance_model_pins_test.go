package sqlite

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pinnedInstance(pins map[string]string, version int) *instance.Instance {
	inst := newLinkedInstance(instance.SpawnLink{})
	inst.ModelPins = pins
	inst.ModelPinsVersion = version
	return inst
}

// TestPinsSQLiteRoundTrip creates a run on a migrated file DB, reopens the DB
// and loads the exact immutable map and version. A no-alias run is
// non-NULL "{}"/1, never NULL.
func TestPinsSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pins.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	repo := NewInstanceRepo(db)

	withPins := pinnedInstance(map[string]string{"worker": "vendor/version-x", "judge": "vendor/version-z"}, 1)
	empty := pinnedInstance(map[string]string{}, 1)
	require.NoError(t, repo.Create(ctx, withPins))
	require.NoError(t, repo.Create(ctx, empty))
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	repo = NewInstanceRepo(db)

	got, err := repo.Get(ctx, withPins.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ModelPinsVersion)
	assert.Equal(t, map[string]string{"worker": "vendor/version-x", "judge": "vendor/version-z"}, got.ModelPins)

	got, err = repo.Get(ctx, empty.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ModelPinsVersion)
	require.NotNil(t, got.ModelPins, "an empty snapshot is a real (empty) map, not a legacy record")
	assert.Empty(t, got.ModelPins)

	var raw *string
	require.NoError(t, db.QueryRow(`SELECT model_pins FROM instances WHERE id = ?`, empty.ID.String()).Scan(&raw))
	require.NotNil(t, raw, "an empty pin set must persist as non-NULL JSON")
	assert.Equal(t, "{}", *raw)
}

// TestPinsLegacyRowReadsAsNullVersionZero emulates a row predating migration
// 020. Existing NULL/0 is readable but cannot be manufactured by Create.
func TestPinsLegacyRowReadsAsNullVersionZero(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	legacy := pinnedInstance(map[string]string{}, 1)
	require.NoError(t, repo.Create(ctx, legacy))
	_, err := db.DB.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, legacy.ID.String())
	require.NoError(t, err)

	var raw *string
	var version int
	require.NoError(t, db.DB.QueryRow(`SELECT model_pins, model_pins_version FROM instances WHERE id = ?`, legacy.ID.String()).Scan(&raw, &version))
	assert.Nil(t, raw, "a historical row must retain model_pins NULL")
	assert.Equal(t, 0, version)

	got, err := repo.Get(ctx, legacy.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ModelPins)
	assert.Equal(t, 0, got.ModelPinsVersion)
}

// TestPinsCorruptDiscriminatorFailsRetrieval: NULL with version 1, non-NULL
// with version 0, invalid JSON, a non-object payload and an unsupported
// version are corruption, not legacy. Each fails Get before any provider call.
func TestPinsCorruptDiscriminatorFailsRetrieval(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		pins    any
		version any
	}{
		{"null with version 1", nil, 1},
		{"blob version 0", nil, []byte("0")},
		{"blob version 1", `{"worker":"vendor/version-x"}`, []byte("1")},
		{"payload with version 0", `{"worker":"vendor/version-x"}`, 0},
		{"invalid json", `{not json`, 1},
		{"json array", `["worker"]`, 1},
		{"json null literal", `null`, 1},
		{"unsupported version", `{"worker":"vendor/version-x"}`, 2},
		{"negative version", `{"worker":"vendor/version-x"}`, -1},
		{"empty model value", `{"worker":""}`, 1},
		{"empty alias key", `{"":"vendor/version-x"}`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			repo := NewInstanceRepo(db.DB)
			inst := pinnedInstance(map[string]string{}, 1)
			require.NoError(t, repo.Create(ctx, inst))
			_, err := db.DB.Exec(`UPDATE instances SET model_pins = ?, model_pins_version = ? WHERE id = ?`, tc.pins, tc.version, inst.ID.String())
			require.NoError(t, err)

			_, err = repo.Get(ctx, inst.ID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "model pins")
		})
	}
}

// TestPinsCreateRejectsInconsistentInstance: Create never writes a
// discriminator combination Get would call corruption.
func TestPinsCreateRejectsInconsistentInstance(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	for name, inst := range map[string]*instance.Instance{
		"new legacy record":       pinnedInstance(nil, 0),
		"pins without version":    pinnedInstance(map[string]string{"worker": "vendor/version-x"}, 0),
		"version without pins":    pinnedInstance(nil, 1),
		"unsupported version":     pinnedInstance(map[string]string{}, 2),
		"empty model in snapshot": pinnedInstance(map[string]string{"worker": ""}, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, repo.Create(ctx, inst))
			var n int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE id = ?`, inst.ID.String()).Scan(&n))
			assert.Zero(t, n, "a rejected create must leave no row")
		})
	}
}

// TestPinsSurviveOrdinaryUpdates: state/variable/node updates never overwrite pins.
func TestPinsSurviveOrdinaryUpdates(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	inst := pinnedInstance(map[string]string{"worker": "vendor/version-x"}, 1)
	require.NoError(t, repo.Create(ctx, inst))

	require.NoError(t, repo.UpdateState(ctx, inst.ID, instance.StateRunning))
	require.NoError(t, repo.UpdateVariables(ctx, inst.ID, map[string]any{"a": 1}))

	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"worker": "vendor/version-x"}, got.ModelPins)
	assert.Equal(t, 1, got.ModelPinsVersion)
}

// TestMigration020AppliesOnDBThatSkippedIt: a DB already recording later
// migrations (021+) with 020 absent gains the columns, keeps its old rows as
// NULL/0 and never backfills pins. A fresh DB gets the same shape.
func TestMigration020AppliesOnDBThatSkippedIt(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	old := pinnedInstance(map[string]string{}, 1)
	require.NoError(t, repo.Create(ctx, old))

	_, err := db.DB.Exec(`ALTER TABLE instances DROP COLUMN model_pins`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`ALTER TABLE instances DROP COLUMN model_pins_version`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 24`)
	require.NoError(t, err)

	require.NoError(t, RunMigrations(db.DB))

	var raw *string
	var version int
	require.NoError(t, db.DB.QueryRow(`SELECT model_pins, model_pins_version FROM instances WHERE id = ?`, old.ID.String()).Scan(&raw, &version))
	assert.Nil(t, raw, "020 must not backfill historical pins (D1)")
	assert.Equal(t, 0, version)
	got, err := repo.Get(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.ModelPinsVersion)
}

// Every full-row SQL path must retain the discriminator, including queue
// selection and recovery reads that use generated queries rather than Get.
func TestPinsAllFullRowReads(t *testing.T) {
	ctx := context.Background()
	for _, corrupt := range []bool{false, true} {
		for _, path := range []string{"generated-get", "running", "eligible", "queued", "timer", "webhook"} {
			t.Run(fmt.Sprintf("%s/corrupt=%t", path, corrupt), func(t *testing.T) {
				db := setupTestDB(t)
				repo := NewInstanceRepo(db.DB)
				queue := NewQueueRepo(db.DB)
				inst := pinnedInstance(map[string]string{"worker": "claude-opus-4-1"}, 1)
				require.NoError(t, repo.Create(ctx, inst))
				if corrupt {
					_, err := db.DB.Exec(`UPDATE instances SET model_pins = NULL WHERE id = ?`, inst.ID.String())
					require.NoError(t, err)
				}
				var got *instance.Instance
				var err error
				switch path {
				case "generated-get":
					var row generated.Instance
					row, err = generated.New(db.DB).GetInstance(ctx, inst.ID.String())
					require.NoError(t, err)
					got, err = rowToInstance(row)
				case "eligible":
					got, err = queue.NextEligible(ctx, 5, 5)
				default:
					var rows []instance.Instance
					switch path {
					case "running":
						require.NoError(t, repo.UpdateState(ctx, inst.ID, instance.StateRunning))
						rows, err = repo.ListRunning(ctx)
					case "queued":
						rows, err = queue.ListQueued(ctx)
					case "timer":
						until := time.Now().Add(time.Hour)
						require.NoError(t, repo.SetWaiting(ctx, inst.ID, &until))
						rows, err = queue.ListWaitingWithTimer(ctx)
					case "webhook":
						require.NoError(t, repo.UpdateState(ctx, inst.ID, instance.StateWaiting))
						token, hash := "token", "hash"
						require.NoError(t, repo.SetWebhookFields(ctx, inst.ID, &token, &hash))
						rows, err = queue.ListWaitingWithWebhook(ctx)
					}
					if corrupt {
						assert.Empty(t, rows, "corrupt pins must never enter an execution batch")
					} else if err == nil {
						require.Len(t, rows, 1)
						got = &rows[0]
					}
				}
				if corrupt {
					if path == "generated-get" {
						require.ErrorContains(t, err, "model pins")
					} else {
						if path == "queued" {
							require.NoError(t, err, "the queue-position read only skips")
						} else {
							var report *instance.CorruptPinsError
							require.ErrorAs(t, err, &report, "admission/recovery readers report the run for the engine to fail")
							assert.Equal(t, []uuid.UUID{inst.ID}, report.IDs)
						}
						assert.Nil(t, got)
						var state string
						require.NoError(t, db.DB.QueryRow(`SELECT state FROM instances WHERE id = ?`, inst.ID.String()).Scan(&state))
						assert.NotEqual(t, "failed", state, "a read never transitions the run")
						_, err := repo.Get(ctx, inst.ID)
						require.ErrorContains(t, err, "model pins", "reporting must not manufacture legacy pins")
					}
					return
				}
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, inst.ModelPins, got.ModelPins)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, got.ModelPinsVersion)
			})
		}
	}
}

func TestMigration020DownPairPresent(t *testing.T) {
	_, err := migrationsFS.ReadFile("migrations/024_instance_model_pins.down.sql")
	require.NoError(t, err)
}
