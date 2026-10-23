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

// Execution readers never change a row with a corrupt pin record. The
// inventory (ListExecutionSnapshots) and the queue-position read skip it; the
// queue-admission and recovery readers return the healthy rows together with an
// *instance.CorruptPinsError naming it, so the engine can drive the complete
// terminal path. Get continues to reject it, and every read leaves its state
// and stored record unchanged (state invariance), even for a running row.
func TestPinsCorruptBatchReporting(t *testing.T) {
	for _, reader := range []string{"running", "next", "queued", "timer", "webhook", "inventory"} {
		for _, pins := range []struct {
			name    string
			raw     any
			version any
		}{
			{"NULL snapshot", nil, 1}, {"legacy payload", `{}`, 0},
			{"blob version 0", nil, []byte("0")},
			{"blob version 1", `{"worker":"vendor/version-x"}`, []byte("1")},
			{"invalid JSON", `{broken`, 1}, {"JSON null", `null`, 1},
			{"array", `[]`, 1}, {"unknown version", `{}`, 2},
			{"empty model", `{"worker":""}`, 1}, {"empty alias", `{"":"m"}`, 1},
		} {
			t.Run(reader+"/"+pins.name, func(t *testing.T) {
				db := setupTestDB(t)
				repo, queue := NewInstanceRepo(db.DB), NewQueueRepo(db.DB)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				state := instance.StateQueued
				if reader == "running" || reader == "inventory" {
					state = instance.StateRunning
				}
				if reader == "timer" || reader == "webhook" {
					state = instance.StateWaiting
				}
				bad := snapshotRun(state, map[string]string{}, 1, "pi")
				good := snapshotRun(state, map[string]string{}, 1, "pi")
				require.NoError(t, repo.Create(ctx, bad))
				createLegacySnapshotRun(t, repo, good)
				_, err := db.DB.Exec(`UPDATE instances SET model_pins = ?, model_pins_version = ?, priority = 'high', current_node_id = 'n' WHERE id = ?`, pins.raw, pins.version, bad.ID.String())
				require.NoError(t, err)
				if reader == "timer" {
					_, err = db.DB.Exec(`UPDATE instances SET waiting_until = '2030-01-01T00:00:00Z'`)
					require.NoError(t, err)
				}
				if reader == "webhook" {
					_, err = db.DB.Exec(`UPDATE instances SET webhook_token = id`)
					require.NoError(t, err)
				}
				// Any write attempted by a read fails loudly.
				_, err = db.DB.Exec(`CREATE TRIGGER reads_never_write BEFORE UPDATE ON instances BEGIN SELECT RAISE(ABORT, 'read wrote a row'); END`)
				require.NoError(t, err)
				for attempt := 0; attempt < 2; attempt++ {
					switch reader {
					case "inventory":
						snaps, err := repo.ListExecutionSnapshots(ctx)
						require.NoError(t, err)
						require.Len(t, snaps, 1)
						assert.Equal(t, good.ID, snaps[0].InstanceID)
					case "next":
						got, err := queue.NextEligible(ctx, 1, 1)
						var corrupt *instance.CorruptPinsError
						require.ErrorAs(t, err, &corrupt)
						assert.Equal(t, []uuid.UUID{bad.ID}, corrupt.IDs)
						assert.Nil(t, got)
					default:
						read := map[string]func(context.Context) ([]instance.Instance, error){
							"running": repo.ListRunning, "queued": queue.ListQueued,
							"timer": queue.ListWaitingWithTimer, "webhook": queue.ListWaitingWithWebhook,
						}[reader]
						got, err := read(ctx)
						if reader == "queued" {
							require.NoError(t, err, "the queue-position read only skips")
						} else {
							var corrupt *instance.CorruptPinsError
							require.ErrorAs(t, err, &corrupt)
							assert.Equal(t, []uuid.UUID{bad.ID}, corrupt.IDs)
						}
						require.Len(t, got, 1)
						assert.Equal(t, good.ID, got[0].ID)
						assert.Nil(t, got[0].ModelPins)
						assert.Zero(t, got[0].ModelPinsVersion)
					}
				}
				var gotState string
				var raw sql.NullString
				var version any
				require.NoError(t, db.DB.QueryRow(`SELECT state, model_pins, model_pins_version FROM instances WHERE id = ?`, bad.ID.String()).Scan(&gotState, &raw, &version))
				assert.Equal(t, string(state), gotState, "a read must never transition the corrupt run")
				assert.Equal(t, pins.raw != nil, raw.Valid)
				if pins.raw != nil {
					assert.Equal(t, pins.raw, raw.String)
				}
				switch expected := pins.version.(type) {
				case int:
					assert.Equal(t, int64(expected), version)
				default:
					assert.Equal(t, expected, version)
				}
				_, err = repo.Get(ctx, bad.ID)
				require.ErrorContains(t, err, "model pins")
			})
		}
	}
}
