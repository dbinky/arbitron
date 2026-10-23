package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No profile or follow-policy classification is needed to record an exact
// native-model execution. In particular, SpecificModel is deliberately empty.
func nativeCatalogCycle() modelcatalog.CycleCommit {
	at := time.Date(2026, 10, 18, 12, 0, 0, 123, time.UTC)
	return modelcatalog.CycleCommit{
		Report: modelcatalog.RefreshReport{ID: "native-discovery", At: at},
		Catalog: modelcatalog.CatalogSnapshot{FetchedAt: at, Slices: []modelcatalog.CatalogSlice{{
			Provider: "vendor", Status: modelcatalog.SliceStatusOK, Raw: json.RawMessage(`{"models":{"version-y":{}}}`),
			Models:  []modelidentity.CatalogModel{{CatalogProvider: "vendor", NativeID: "version-y", CanonicalModelID: "vendor/y", ReleaseDate: "2026-10-17", SourceRef: "https://source.invalid/native"}},
			Entries: []modelidentity.Entry{{Provider: "vendor", NativeID: "version-y", CanonicalID: "vendor/y", ReleaseDate: "2026-10-17", PolicyRevision: 1}},
		}}},
		Evidence:          []modelcatalog.UsabilityEvidence{{CanonicalID: "vendor/y", NativeModel: "version-y", PathKey: "native-path", ConfigRevision: "config-1", CheckedAt: at, ProofRef: "native-execution:version-y", State: modelcatalog.UsabilityConfirmed}},
		ProbeCursor:       map[string]string{"native-path@config-1": "vendor/y"},
		WebhookConfigured: boolPointer(true),
	}
}

func TestCatalogNativeUnknownTierSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "native.db")
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	repo := NewModelCatalogRepo(db)
	c := nativeCatalogCycle()
	first, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	require.Len(t, first.NewlyUsable, 1)
	assert.Equal(t, c.Catalog.Slices[0].Entries[0], first.NewlyUsable[0].Entry)
	assert.Equal(t, c.Evidence, first.NewlyUsable[0].Paths)
	assert.Empty(t, first.Changes)
	frozen := catalogEvents(t, testDB{DB: db})
	require.Len(t, frozen, 1)
	assert.Equal(t, modelcatalog.EventPending, frozen[0].State)
	assert.Empty(t, frozen[0].Payload.Tier)
	assert.Empty(t, frozen[0].Payload.Changes)
	assert.Equal(t, c.Evidence, frozen[0].Payload.Paths)
	assert.Equal(t, c.Report.At, frozen[0].Payload.FirstConfirmedAt)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	repo = NewModelCatalogRepo(db)
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, c.Catalog, view.Catalog)
	assert.Equal(t, c.Catalog.Slices[0].Models, view.HistoricalModels)
	assert.Equal(t, c.Catalog.Slices[0].Entries, view.HistoricalEntries)
	assert.Equal(t, c.Evidence, view.Evidence)
	assert.Equal(t, c.ProbeCursor, view.ProbeCursor)
	assert.Equal(t, frozen, catalogEvents(t, testDB{DB: db}))
	public := modelcatalog.ProjectCatalog(view)
	require.Len(t, public.Entries, 1)
	assert.Equal(t, "version-y", public.Entries[0].NativeID)
	assert.Empty(t, public.Entries[0].Tier)
	assert.Empty(t, public.Entries[0].SpecificModel)
	assert.Equal(t, modelcatalog.UsabilityConfirmed, public.Entries[0].Usability)
	require.Len(t, public.Entries[0].Paths, 1)
	assert.Equal(t, c.Evidence[0].PathKey, public.Entries[0].Paths[0].PathKey)
	assert.Equal(t, c.Evidence[0].ConfigRevision, public.Entries[0].Paths[0].ConfigRevision)
	assert.Equal(t, c.Evidence[0].CheckedAt, public.Entries[0].Paths[0].CheckedAt)

	// Replaying the committed discovery, then classifying and advancing a new
	// following alias later, must not rewrite the original no-change event.
	replay, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, first, replay)
	profiles := NewProfileRepo(db)
	require.NoError(t, profiles.Create(ctx, followingProfile("later-follower")))
	p, err := profiles.Get(ctx, "later-follower")
	require.NoError(t, err)
	c.Report.ID, c.Report.At = "later-advance", c.Report.At.Add(time.Hour)
	e := &c.Catalog.Slices[0].Entries[0]
	e.SpecificModel, e.Tier, e.OrderKey, e.Immutable = "vendor/version-y", "premium", "2", true
	c.Evidence[0].NativeModel = e.SpecificModel
	c.Evidence[0].PathKey, c.Evidence[0].ConfigRevision = "path", "config-1"
	req := advanceReq("later-native-advance", p, e.SpecificModel)
	req.CanonicalID = e.CanonicalID
	c.Advances = []provider.AdvanceRequest{req}
	next, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	require.Len(t, next.Changes, 1)
	assert.Empty(t, next.NewlyUsable)
	persisted, err := profiles.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Equal(t, e.SpecificModel, persisted.Model)
	assert.Equal(t, frozen, catalogEvents(t, testDB{DB: db}))
}

func TestCatalogNativeDiscoveryRejectsUnidentifiedProof(t *testing.T) {
	for _, name := range []string{"empty_proof", "empty_native", "wrong_native", "wrong_canonical", "empty_path", "empty_config", "specific_model_mismatch"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := nativeCatalogCycle()
			switch name {
			case "empty_proof":
				c.Evidence[0].ProofRef = ""
			case "empty_native":
				c.Evidence[0].NativeModel = ""
			case "wrong_native":
				c.Evidence[0].NativeModel = "unrelated-model"
			case "wrong_canonical":
				c.Evidence[0].CanonicalID = "vendor/unrelated"
			case "empty_path":
				c.Evidence[0].PathKey = ""
			case "empty_config":
				c.Evidence[0].ConfigRevision = ""
			case "specific_model_mismatch":
				c.Catalog.Slices[0].Entries[0].SpecificModel = "vendor/version-y"
			}
			_, err := repo.CommitCycle(ctx, c)
			require.Error(t, err)
			assert.Empty(t, catalogEvents(t, db))
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.HistoricalModels)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.Reports)
			assert.Empty(t, view.ProbeCursor)
		})
	}
}

func TestCatalogNativeDiscoveryWriteFaultRollsBackAndReplays(t *testing.T) {
	for _, table := range []string{"model_announcement_events", "model_refresh_reports", "model_probe_cursors"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			db := setupTestDB(t)
			repo := NewModelCatalogRepo(db.DB)
			c := nativeCatalogCycle()
			_, err := db.DB.Exec(`CREATE TRIGGER native_fault BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'injected'); END`)
			require.NoError(t, err)
			_, err = repo.CommitCycle(ctx, c)
			require.ErrorContains(t, err, "injected")
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.HistoricalModels)
			assert.Empty(t, view.Evidence)
			assert.Empty(t, view.ProbeCursor)
			assert.Empty(t, view.Reports)
			assert.Empty(t, catalogEvents(t, db))
			_, err = db.DB.Exec(`DROP TRIGGER native_fault`)
			require.NoError(t, err)
			first, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			require.Len(t, first.NewlyUsable, 1)
			frozen := catalogEvents(t, db)
			again, err := repo.CommitCycle(ctx, c)
			require.NoError(t, err)
			assert.Equal(t, first, again)
			assert.Equal(t, frozen, catalogEvents(t, db))
		})
	}
}
