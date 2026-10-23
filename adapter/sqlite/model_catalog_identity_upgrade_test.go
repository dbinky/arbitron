package sqlite

import (
	"context"
	"os"
	"testing"
	"time"

	adaptcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Omitting canonical reconciliation creates a second event for v4 after the
// old provider-prefixed row disappears. Failed transactions must not consume
// the first observation of the actually new v5 model.
func TestCatalogUpgradeDeduplicatesRetainedIdentityAndPreservesDelivery(t *testing.T) {
	for _, state := range []string{"suppressed", "announced", "pending", "awaiting_setting", "in_flight", "delivery_unknown"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			fixture, err := os.ReadFile("testdata/catalog_before_routing_prefix.sql")
			require.NoError(t, err)
			_, err = db.DB.Exec(string(fixture))
			require.NoError(t, err)
			_, err = db.DB.Exec(`UPDATE model_announcement_events SET state=?,claim_id='original-claim',lease_until='2026-10-20T00:00:00Z',attempts=2,last_code='original-code'`, state)
			require.NoError(t, err)
			var payload string
			require.NoError(t, db.DB.QueryRow(`SELECT payload FROM model_announcement_events`).Scan(&payload))
			repo := NewModelCatalogRepo(db.DB)
			before, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			mapper, err := modelidentity.NewIdentityMapper(NewModelIdentityPolicyRepo(db.DB))
			require.NoError(t, err)
			at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
			normalizer, err := adaptcatalog.NewNormalizer(mapper, func() time.Time { return at })
			require.NoError(t, err)
			snap, err := normalizer.Normalize(ctx, "https://models.dev/api.json", []byte(`{"openrouter":{"id":"openrouter","models":{"deepseek/deepseek-v4-flash":{"id":"deepseek/deepseek-v4-flash"},"deepseek/deepseek-v5-flash":{"id":"deepseek/deepseek-v5-flash"}}}}`))
			require.NoError(t, err)
			c := modelcatalog.CycleCommit{Catalog: snap, Report: modelcatalog.RefreshReport{ID: "upgraded", At: at}}
			for _, model := range []string{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v5-flash"} {
				c.Evidence = append(c.Evidence, modelcatalog.UsabilityEvidence{CanonicalID: model, NativeModel: model, PathKey: "current-path", ConfigRevision: "current-config", ProofRef: "current-proof", CheckedAt: at, State: modelcatalog.UsabilityConfirmed})
			}
			_, err = db.DB.Exec(`CREATE TRIGGER upgrade_write_fault BEFORE INSERT ON model_refresh_reports BEGIN SELECT RAISE(ABORT,'upgrade write fault'); END`)
			require.NoError(t, err)
			_, err = repo.CommitCycle(ctx, c)
			require.ErrorContains(t, err, "upgrade write fault")
			after, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after, "upgrade, evidence and new discovery must roll back together")
			assert.Len(t, catalogEvents(t, db), 1)
			_, err = db.DB.Exec(`DROP TRIGGER upgrade_write_fault`)
			require.NoError(t, err)
			for _, id := range []string{"upgraded", "next-cycle"} {
				c.Report.ID = id
				report, err := repo.CommitCycle(ctx, c)
				require.NoError(t, err)
				if id == "upgraded" {
					require.Len(t, report.NewlyUsable, 1)
					assert.Equal(t, "deepseek/deepseek-v5-flash", report.NewlyUsable[0].Entry.CanonicalID)
				} else {
					assert.Empty(t, report.NewlyUsable)
				}
			}
			var count int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&count))
			assert.Equal(t, 2, count, "one old v4 observation plus one genuinely new v5")
			assert.Len(t, catalogEvents(t, db), 2)
			var gotPayload, gotState, claim, lease, code string
			var attempts int
			require.NoError(t, db.DB.QueryRow(`SELECT payload,state,claim_id,lease_until,attempts,last_code FROM model_announcement_events WHERE id='legacy-event'`).Scan(&gotPayload, &gotState, &claim, &lease, &attempts, &code))
			assert.Equal(t, payload, gotPayload)
			assert.Equal(t, state, gotState)
			assert.Equal(t, "original-claim", claim)
			assert.Equal(t, "2026-10-20T00:00:00Z", lease)
			assert.Equal(t, 2, attempts)
			assert.Equal(t, "original-code", code)
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM model_usability_evidence WHERE canonical_id='deepseek/deepseek-v4-flash' AND path_key='legacy-path'`).Scan(&count))
			assert.Zero(t, count, "deduplication must never promote legacy route proof to the new identity")
		})
	}
}
