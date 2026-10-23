package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogInvalidSliceRetainsMembershipButCannotAdvance(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	proposals := c.Advances
	c.Advances = nil
	_, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	c.Report.ID = "invalid-cycle"
	c.Catalog.Slices[0].Status = modelcatalog.SliceStatusInvalid
	c.Catalog.Slices[0].Models = nil
	c.Catalog.Slices[0].Entries = nil
	c.Advances = proposals
	report, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Empty(t, report.Changes)
	assert.Equal(t, "invalid_slice", report.PathStatus["profile:worker"])
	assert.Equal(t, proposals[0].Expected, cProfile(t, db))
	assert.Zero(t, journalCount(t, db, "worker"))
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.Catalog.Slices[0].Entries, 1)
}

func TestCatalogUnknownPreservesConfirmationAndCursorWithoutConfirmingUnprobed(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	c.Advances = nil
	_, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	c.Report.ID = "degraded"
	c.Report.PathStatus["path"] = "budget_exhausted"
	c.Evidence[0].State = "unknown"
	c.Evidence[0].CheckedAt = c.Evidence[0].CheckedAt.Add(time.Hour)
	c.Evidence[0].ProofRef = ""
	model := c.Catalog.Slices[0].Models[0]
	model.NativeID = "not-probed"
	model.CanonicalModelID = "vendor/not-probed"
	c.Catalog.Slices[0].Models = append(c.Catalog.Slices[0].Models, model)
	entry := c.Catalog.Slices[0].Entries[0]
	entry.NativeID = "not-probed"
	entry.CanonicalID = "vendor/not-probed"
	c.Catalog.Slices[0].Entries = append(c.Catalog.Slices[0].Entries, entry)
	c.ProbeCursor["path/config-1"] = "vendor/not-probed"
	report, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Empty(t, report.NewlyUsable)
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.Evidence, 1)
	assert.True(t, view.Evidence[0].Confirmed())
	assert.Equal(t, c.ProbeCursor, view.ProbeCursor)
	assert.Len(t, catalogEvents(t, db), 1)
	assert.Equal(t, "budget_exhausted", view.Reports[1].PathStatus["path"])
}

func TestCatalogPublicationReopenUsesOriginalJournalNotCurrentProfile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "direct.db")
	sqlDB, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(sqlDB))
	db := testDB{DB: sqlDB}
	profiles := NewProfileRepo(sqlDB)
	require.NoError(t, profiles.Create(ctx, followingProfile("worker")))
	req := advanceReq("direct-reopen", cProfile(t, db), "vendor/version-y")
	req.At = req.At.Add(123456789 * time.Nanosecond)
	original, err := profiles.CommitAdvance(ctx, req)
	require.NoError(t, err)
	newer := cProfile(t, db)
	newer.Model = "vendor/version-z"
	require.NoError(t, profiles.Update(ctx, newer))
	require.NoError(t, sqlDB.Close())
	sqlDB, err = Open(path)
	require.NoError(t, err)
	defer sqlDB.Close()
	repo := NewModelCatalogRepo(sqlDB)
	pending, err := repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, []provider.Advancement{original}, pending)
	reports, err := repo.Recover(ctx)
	require.NoError(t, err)
	assert.Empty(t, reports)
	_, err = sqlDB.Exec(`CREATE TRIGGER direct_ack_fault BEFORE INSERT ON model_publication_acks BEGIN SELECT RAISE(ABORT,'fault'); END`)
	require.NoError(t, err)
	require.Error(t, repo.Acknowledge(ctx, []string{original.ID}))
	pending, err = repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Equal(t, []provider.Advancement{original}, pending)
	_, err = sqlDB.Exec(`DROP TRIGGER direct_ack_fault`)
	require.NoError(t, err)
	require.NoError(t, repo.Acknowledge(ctx, []string{original.ID}))
	pending, err = repo.Unpublished(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestCatalogConcurrentCycleReplayHasOneJournalAndEvent(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	type result struct {
		report modelcatalog.RefreshReport
		err    error
	}
	results := make(chan result, 4)
	for range 4 {
		go func() { report, err := repo.CommitCycle(ctx, c); results <- result{report, err} }()
	}
	var first modelcatalog.RefreshReport
	for i := range 4 {
		got := <-results
		require.NoError(t, got.err)
		if i == 0 {
			first = got.report
		}
		assert.Equal(t, first, got.report)
	}
	assert.Equal(t, 1, journalCount(t, db, "worker"))
	assert.Len(t, catalogEvents(t, db), 1)
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Len(t, view.Reports, 1)
}

func TestCatalogCycleRejectsSecondWriterRevisionBeforeAllWrites(t *testing.T) {
	for _, source := range []string{"policy", "connection"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "revision.db")
			sqlDB, err := Open(path)
			require.NoError(t, err)
			defer sqlDB.Close()
			require.NoError(t, RunMigrations(sqlDB))
			db := testDB{DB: sqlDB}
			c := catalogCycle(t, db)
			_, err = sqlDB.Exec(`INSERT INTO connections(name,provider,base_url,api_key_cipher,api_key_nonce) VALUES('remote','openai','https://api.invalid',X'01',X'02')`)
			require.NoError(t, err)
			c.Advances[0].ExecutionRevision.Connections = map[string]int64{"remote": 1}
			other, err := Open(path)
			require.NoError(t, err)
			defer other.Close()
			if source == "policy" {
				_, err = other.Exec(`UPDATE model_identity_policy SET revision=revision+1`)
			} else {
				_, err = other.Exec(`UPDATE connections SET config_revision=config_revision+1 WHERE name='remote'`)
			}
			require.NoError(t, err)
			repo := NewModelCatalogRepo(sqlDB)
			_, err = repo.CommitCycle(ctx, c)
			require.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			assert.Equal(t, c.Advances[0].Expected, cProfile(t, db))
			assert.Zero(t, journalCount(t, db, "worker"))
			assert.Empty(t, catalogEvents(t, db))
			view, err := repo.Snapshot(ctx)
			require.NoError(t, err)
			assert.Empty(t, view.Reports)
			assert.Empty(t, view.Catalog.Slices)
			assert.Empty(t, view.ProbeCursor)
		})
	}
}

func TestCatalogSchemaPersistsCapturedPolicyRevision(t *testing.T) {
	db := setupTestDB(t)
	c := catalogCycle(t, db)
	c.Advances = nil
	_, err := NewModelCatalogRepo(db.DB).CommitCycle(context.Background(), c)
	require.NoError(t, err)
	var revision int64
	require.NoError(t, db.DB.QueryRow(`SELECT policy_revision FROM model_catalog_models WHERE provider='vendor' AND native_id='version-y'`).Scan(&revision))
	assert.Equal(t, c.Catalog.Slices[0].Entries[0].PolicyRevision, revision)
}

func TestCatalogHistoricalReleaseDateControlsEqualVersionOrdering(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewModelCatalogRepo(db.DB)
	c := catalogCycle(t, db)
	c.Advances = nil
	_, err := repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	c.Report.ID = "offering-removed"
	c.Catalog.Slices[0].Models = nil
	c.Catalog.Slices[0].Entries = nil
	_, err = repo.CommitCycle(ctx, c)
	require.NoError(t, err)
	view, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.HistoricalEntries, 1)
	old := view.HistoricalEntries[0]
	candidate := old
	candidate.ReleaseDate = "2026-11-01"
	cmp, err := modelidentity.Compare(candidate, old)
	require.NoError(t, err)
	assert.Equal(t, 1, cmp)
	old.ReleaseDate = ""
	_, err = modelidentity.Compare(candidate, old)
	assert.ErrorIs(t, err, modelidentity.ErrUnorderable)
	// Malformed durable history is a read fault, not an empty successful view.
	_, err = db.DB.Exec(`UPDATE model_catalog_models SET entry_json='corrupt'`)
	require.NoError(t, err)
	_, err = repo.Snapshot(ctx)
	require.Error(t, err)
}
