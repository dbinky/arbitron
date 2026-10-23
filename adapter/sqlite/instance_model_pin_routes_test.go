package sqlite

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is the storage boundary of D8, not an execution/version proof. Capture
// eligibility belongs to the snapshotter; execution enforcement belongs to P1T4.
func TestD8SnapshotRoutesRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routes.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	repo := NewInstanceRepo(db)
	pins := map[string]string{"deployment": "my-deployment", "native": "my-model"}
	routes := map[string]string{"deployment": "azure", "native": "bedrock"}
	conns := map[string]string{"deployment": "conn-a", "native": "conn-b"}
	inst := pinnedInstance(pins, 1)
	inst.ModelPinRoutes = routes
	inst.ModelPinRouteConnections = conns
	require.NoError(t, repo.Create(ctx, inst))

	// Ordinary updates and mutation of the caller's maps cannot overwrite pins.
	pins["deployment"], routes["deployment"], conns["deployment"] = "changed", "bedrock", "conn-z"
	require.NoError(t, repo.UpdateState(ctx, inst.ID, instance.StateRunning))
	require.NoError(t, repo.UpdateVariables(ctx, inst.ID, map[string]any{"changed": true}))
	require.NoError(t, repo.UpdateCurrentNode(ctx, inst.ID, "next"))
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	repo = NewInstanceRepo(db)
	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ModelPinsVersion)
	assert.Equal(t, map[string]string{"deployment": "my-deployment", "native": "my-model"}, got.ModelPins)
	require.Equal(t, map[string]string{"deployment": "azure", "native": "bedrock"}, got.ModelPinRoutes)
	require.Equal(t, map[string]string{"deployment": "conn-a", "native": "conn-b"}, got.ModelPinRouteConnections)
	got.ModelPinRoutes["deployment"] = "bedrock"
	again, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "azure", again.ModelPinRoutes["deployment"])
}

func TestPinsRoutesCreateValidationAndAtomicity(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	for name, routes := range map[string]map[string]string{
		"unknown family":      {"worker": "openai"},
		"empty family":        {"worker": ""},
		"noncanonical family": {"worker": "azure-openai"},
		"missing alias":       {"other": "azure"},
		"empty alias":         {"": "bedrock"},
	} {
		t.Run(name, func(t *testing.T) {
			inst := pinnedInstance(map[string]string{"worker": "deployment"}, 1)
			inst.ModelPinRoutes = routes
			require.ErrorContains(t, repo.Create(ctx, inst), "model pin routes")
			var n int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE id = ?`, inst.ID.String()).Scan(&n))
			assert.Zero(t, n)
		})
	}

	t.Run("connection without route", func(t *testing.T) {
		inst := pinnedInstance(map[string]string{"worker": "deployment"}, 1)
		inst.ModelPinRouteConnections = map[string]string{"worker": "conn-a"}
		require.ErrorContains(t, repo.Create(ctx, inst), "model pin routes")
	})

	inst := pinnedInstance(map[string]string{"worker": "deployment"}, 1)
	inst.ModelPinRoutes = map[string]string{"worker": "azure"}
	_, err := db.DB.Exec(`CREATE TRIGGER routes_write_fault BEFORE INSERT ON instances WHEN NEW.model_pin_routes IS NOT NULL BEGIN SELECT RAISE(ABORT, 'routes write fault'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.Create(ctx, inst), "routes write fault")
	var n int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&n))
	assert.Zero(t, n, "a failed route write must not leave a queued row")
}

func TestPinsRoutesAllFullRowReads(t *testing.T) {
	for _, path := range []string{"get", "generated-get", "running", "eligible", "queued", "timer", "webhook", "inventory"} {
		for _, raw := range []struct {
			name    string
			routes  any
			corrupt bool
		}{
			{"valid", `{"worker":"azure"}`, false},
			{"pre031 strict", nil, false},
			{"invalid JSON", `{broken`, true},
			{"array", `[]`, true},
			{"null literal", `null`, true},
			{"nonstring", `{"worker":1}`, true},
			{"unknown family", `{"worker":"openai"}`, true},
			{"noncanonical family", `{"worker":"azure-openai"}`, true},
			{"missing model", `{"other":"bedrock"}`, true},
			{"legacy exception", `{"worker":"azure"}`, true},
			{"connection record", `{"worker":{"family":"azure","connection":"conn-a"}}`, false},
			{"empty connection", `{"worker":{"family":"azure","connection":""}}`, true},
			{"unknown record field", `{"worker":{"family":"azure","connection":"conn-a","endpoint":"x"}}`, true},
			{"record unknown family", `{"worker":{"family":"openai","connection":"conn-a"}}`, true},
			{"record missing model", `{"other":{"family":"azure","connection":"conn-a"}}`, true},
		} {
			t.Run(path+"/"+raw.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				db := setupTestDB(t)
				repo, queue := NewInstanceRepo(db.DB), NewQueueRepo(db.DB)
				inst := pinnedInstance(map[string]string{"worker": "deployment"}, 1)
				require.NoError(t, repo.Create(ctx, inst))
				_, err := db.DB.Exec(`UPDATE instances SET model_pin_routes = ? WHERE id = ?`, raw.routes, inst.ID.String())
				require.NoError(t, err)
				if raw.name == "legacy exception" {
					_, err = db.DB.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, inst.ID.String())
					require.NoError(t, err)
				}
				var got *instance.Instance
				switch path {
				case "get":
					got, err = repo.Get(ctx, inst.ID)
				case "generated-get":
					var row generated.Instance
					row, err = generated.New(db.DB).GetInstance(ctx, inst.ID.String())
					require.NoError(t, err)
					got, err = rowToInstance(row)
				case "eligible":
					got, err = queue.NextEligible(ctx, 5, 5)
				case "inventory":
					require.NoError(t, repo.UpdateState(ctx, inst.ID, instance.StateRunning))
					var snapshots []instance.ExecutionSnapshot
					snapshots, err = repo.ListExecutionSnapshots(ctx)
					require.NoError(t, err)
					assert.Empty(t, snapshots, "version 1 never becomes a legacy live route")
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
					if !raw.corrupt {
						require.NoError(t, err)
						require.Len(t, rows, 1)
						got = &rows[0]
					} else {
						assert.Empty(t, rows)
					}
				}
				if raw.corrupt {
					switch path {
					case "get", "generated-get":
						require.ErrorContains(t, err, "model pin routes")
					case "queued", "inventory":
						require.NoError(t, err, "pure reads skip the corrupt run")
					default:
						// Admission/recovery readers report it for the engine to fail.
						var corrupt *instance.CorruptPinsError
						require.ErrorAs(t, err, &corrupt)
						assert.Equal(t, []uuid.UUID{inst.ID}, corrupt.IDs)
					}
					assert.Nil(t, got)
					var before, state string
					before = "queued"
					switch path {
					case "running", "inventory":
						before = "running"
					case "timer", "webhook":
						before = "waiting"
					}
					if path != "get" && path != "generated-get" {
						require.NoError(t, db.DB.QueryRow(`SELECT state FROM instances WHERE id = ?`, inst.ID.String()).Scan(&state))
						assert.Equal(t, before, state, "a read never transitions the run")
					}
					_, err = repo.Get(ctx, inst.ID)
					require.ErrorContains(t, err, "model pin routes", "the corrupt record is preserved")
					return
				}
				require.NoError(t, err)
				if path != "inventory" {
					require.NotNil(t, got)
					assert.Equal(t, inst.ModelPins, got.ModelPins)
					assert.Equal(t, 1, got.ModelPinsVersion)
					if raw.routes == nil {
						assert.Empty(t, got.ModelPinRoutes)
					} else {
						assert.Equal(t, map[string]string{"worker": "azure"}, got.ModelPinRoutes)
					}
					if raw.name == "connection record" {
						assert.Equal(t, map[string]string{"worker": "conn-a"}, got.ModelPinRouteConnections)
					} else {
						assert.Empty(t, got.ModelPinRouteConnections, "family-only rows carry no trigger connection")
					}
				}
			})
		}
	}
}

func TestPinsRoutesMigration031PreservesExistingRecords(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	strict := pinnedInstance(map[string]string{"worker": "claude-opus-4-1"}, 1)
	legacy := pinnedInstance(map[string]string{}, 1)
	require.NoError(t, repo.Create(ctx, strict))
	require.NoError(t, repo.Create(ctx, legacy))
	_, err := db.DB.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0, model_pin_routes = NULL WHERE id = ?`, legacy.ID.String())
	require.NoError(t, err)
	down, err := migrationsFS.ReadFile("migrations/035_model_pin_routes.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	_, err = db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 35`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db.DB))
	for _, inst := range []*instance.Instance{strict, legacy} {
		var raw sql.NullString
		require.NoError(t, db.DB.QueryRow(`SELECT model_pin_routes FROM instances WHERE id = ?`, inst.ID.String()).Scan(&raw))
		assert.False(t, raw.Valid, "031 must not backfill or reclassify existing pins")
		got, err := repo.Get(ctx, inst.ID)
		require.NoError(t, err)
		assert.Empty(t, got.ModelPinRoutes)
		if inst == legacy {
			assert.Nil(t, got.ModelPins)
			assert.Zero(t, got.ModelPinsVersion)
		} else {
			assert.Equal(t, strict.ModelPins, got.ModelPins)
			assert.Equal(t, 1, got.ModelPinsVersion)
		}
	}
	fresh := pinnedInstance(map[string]string{}, 1)
	require.NoError(t, repo.Create(ctx, fresh))
	var raw string
	require.NoError(t, db.DB.QueryRow(`SELECT model_pin_routes FROM instances WHERE id = ?`, fresh.ID.String()).Scan(&raw))
	assert.JSONEq(t, `{}`, raw)
}
