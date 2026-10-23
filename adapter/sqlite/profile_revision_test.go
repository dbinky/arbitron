package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	provideradapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Resetting CreateProfile's revision to one lets a request captured before a
// delete advance a different incarnation of the same alias.
func TestProfileRevisionRecreateRejectsStaleAdvance(t *testing.T) {
	for _, deletion := range []string{"repository", "conditional", "second_writer"} {
		t.Run(deletion, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "profiles.db")
			db, err := Open(path)
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, RunMigrations(db))
			repo := NewProfileRepo(db)
			require.NoError(t, repo.Create(ctx, followingProfile("worker")))
			seen, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			stale := advanceReq("stale", seen, "vendor/version-y")
			switch deletion {
			case "repository":
				require.NoError(t, repo.Delete(ctx, seen.Alias))
			case "conditional":
				require.NoError(t, repo.DeleteExpected(ctx, seen))
			case "second_writer":
				other, err := Open(path)
				require.NoError(t, err)
				defer other.Close()
				_, err = other.ExecContext(ctx, `DELETE FROM profiles WHERE alias = ?`, seen.Alias)
				require.NoError(t, err)
			}
			require.NoError(t, repo.Create(ctx, seen)) // caller revision is ignored
			recreated, err := repo.Get(ctx, seen.Alias)
			require.NoError(t, err)
			_, err = repo.CommitAdvance(ctx, stale)
			require.ErrorIs(t, err, provider.ErrAdvanceConflict)
			got, err := repo.Get(ctx, seen.Alias)
			require.NoError(t, err)
			assert.Equal(t, recreated, got)
			assert.Greater(t, got.Revision, seen.Revision)
			assert.Zero(t, journalCount(t, testDB{DB: db}, seen.Alias))

			// A fresh proposal still succeeds. Another recreation supersedes its
			// pending publication even when the new profile chooses the old model.
			adv, err := repo.CommitAdvance(ctx, advanceReq("fresh", got, stale.Model))
			require.NoError(t, err)
			require.NoError(t, repo.Delete(ctx, seen.Alias))
			require.NoError(t, repo.Create(ctx, seen))
			cache := provideradapter.NewProfileCache(repo)
			require.NoError(t, cache.PublishCommitted(ctx, []provider.Advancement{adv}))
			assert.Equal(t, 1, journalCount(t, testDB{DB: db}, seen.Alias))
		})
	}
}

func TestCatalogCycleRecreatedProfileConflictDoesNotBlockOtherProposals(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	c := catalogCycle(t, db)
	profiles := NewProfileRepo(db.DB)
	require.NoError(t, profiles.Create(ctx, followingProfile("healthy")))
	healthy, err := profiles.Get(ctx, "healthy")
	require.NoError(t, err)
	proposal := c.Advances[0]
	proposal.ID, proposal.Expected = "healthy-advance", healthy
	c.Advances = append(c.Advances, proposal)
	require.NoError(t, profiles.DeleteExpected(ctx, c.Advances[0].Expected))
	require.NoError(t, profiles.Create(ctx, c.Advances[0].Expected))
	recreated, err := profiles.Get(ctx, "worker")
	require.NoError(t, err)

	report, err := NewModelCatalogRepo(db.DB).CommitCycle(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, "advance_conflict", report.PathStatus["profile:worker"])
	require.Len(t, report.Changes, 1)
	assert.Equal(t, "healthy", report.Changes[0].Alias)
	assert.Equal(t, recreated, cProfile(t, db))
	got, err := profiles.Get(ctx, "healthy")
	require.NoError(t, err)
	assert.Equal(t, proposal.Model, got.Model)
	assert.Zero(t, journalCount(t, db, "worker"))
	assert.Equal(t, 1, journalCount(t, db, "healthy"))
}

func TestProfileRevisionFloorSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profiles.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	repo := NewProfileRepo(db)
	p := followingProfile("worker")
	require.NoError(t, repo.Create(ctx, p))
	require.NoError(t, repo.Update(ctx, p))
	before, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, p.Alias))
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, RunMigrations(db))
	repo = NewProfileRepo(db)
	require.NoError(t, repo.Create(ctx, p))
	after, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Greater(t, after.Revision, before.Revision)
	require.NoError(t, repo.Create(ctx, followingProfile("fresh")))
	fresh, err := repo.Get(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, int64(1), fresh.Revision)
}

func TestProfileRevisionFloorMigrationRetainsJournalHighWater(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	p := followingProfile("worker")
	require.NoError(t, repo.Create(ctx, p))
	before, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	adv, err := repo.CommitAdvance(ctx, advanceReq("before-upgrade", before, "vendor/version-y"))
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, p.Alias))
	// Simulate an installation predating 027: the deleted alias is now known
	// only through its journal. Applying pending migrations must retain that floor.
	down, err := migrationsFS.ReadFile("migrations/031_profile_revision_floor.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	_, err = db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 31`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db.DB))
	require.NoError(t, RunMigrations(db.DB))
	require.NoError(t, repo.Create(ctx, p))
	after, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Greater(t, after.Revision, adv.Revision)
	cache := provideradapter.NewProfileCache(repo)
	require.NoError(t, cache.PublishCommitted(ctx, []provider.Advancement{adv}))
}

func TestProfileRevisionFloorWriteFaultPreservesProfile(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	before, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	_, err = db.DB.Exec(`CREATE TRIGGER reject_profile_floor BEFORE INSERT ON profile_revision_floor
BEGIN SELECT RAISE(ABORT, 'floor write fault'); END`)
	require.NoError(t, err)
	require.Error(t, repo.Delete(ctx, before.Alias))
	after, err := repo.Get(ctx, before.Alias)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = db.DB.Exec(`DROP TRIGGER reject_profile_floor`)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(ctx, before.Alias))
	require.NoError(t, repo.Create(ctx, before))
	after, err = repo.Get(ctx, before.Alias)
	require.NoError(t, err)
	assert.Greater(t, after.Revision, before.Revision)
}
