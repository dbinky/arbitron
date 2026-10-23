package main

import (
	"context"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/require"
)

// The non-empty gate must not strand the tail of an interrupted initial seed.
// Reopening the DB also proves recovery is durable, not process-local state.
func TestSeedProfilesResumesAfterPartialPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seed.db")
	db, err := adaptsqlite.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewProfileRepo(db)
	input := []provider.Profile{
		{Alias: "first", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"},
		{Alias: "second", Kind: provider.ProfileKindChat, Model: "claude-opus-4-5"},
	}
	_, err = db.Exec(`CREATE TRIGGER fail_seed BEFORE INSERT ON profiles
		WHEN NEW.alias = 'second' BEGIN SELECT RAISE(ABORT, 'seed disk fault'); END`)
	require.NoError(t, err)
	n, _, err := SeedProfiles(ctx, repo, testSeeder(repo), input, nil)
	require.ErrorContains(t, err, "seed disk fault")
	require.Equal(t, 1, n)
	first, err := repo.Get(ctx, "first")
	require.NoError(t, err)
	first.Model = "claude-sonnet-4-6"
	require.NoError(t, repo.Update(ctx, first))
	_, err = db.Exec(`DROP TRIGGER fail_seed`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = adaptsqlite.Open(path)
	require.NoError(t, err)
	repo = adaptsqlite.NewProfileRepo(db)
	// A changed config cannot replace the original unfinished seed snapshot.
	n, skipped, err := SeedProfiles(ctx, repo, testSeeder(repo), []provider.Profile{
		{Alias: "second", Kind: provider.ProfileKindChat, Model: "claude-opus-4-6"},
		{Alias: "new-config", Kind: provider.ProfileKindChat},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n, "resume the missing original entry despite a non-empty DB")
	require.Zero(t, skipped)
	second, err := repo.Get(ctx, "second")
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-5", second.Model)
	require.Equal(t, int64(1), second.Revision)
	first, err = repo.Get(ctx, "first")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", first.Model)
	require.Equal(t, int64(2), first.Revision)
	_, err = repo.Get(ctx, "new-config")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)

	// Completion survives deleting every profile; later config never resurrects it.
	require.NoError(t, repo.Delete(ctx, "first"))
	require.NoError(t, repo.Delete(ctx, "second"))
	n, _, err = SeedProfiles(ctx, repo, testSeeder(repo), input, nil)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestSeedProfilesRecoversAcknowledgementFailure(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewProfileRepo(db)
	input := []provider.Profile{{Alias: "first", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"}}
	_, err = db.Exec(`CREATE TRIGGER fail_ack BEFORE DELETE ON profile_seed_pending
		BEGIN SELECT RAISE(ABORT, 'seed ack fault'); END`)
	require.NoError(t, err)
	n, _, err := SeedProfiles(ctx, repo, testSeeder(repo), input, nil)
	require.ErrorContains(t, err, "seed ack fault")
	require.Equal(t, 1, n)
	first, err := repo.Get(ctx, "first")
	require.NoError(t, err)
	first.Model = "claude-sonnet-4-6"
	require.NoError(t, repo.Update(ctx, first))
	_, err = db.Exec(`DROP TRIGGER fail_ack`)
	require.NoError(t, err)
	// No seeder is needed to acknowledge an already committed/admin-edited row.
	n, _, err = SeedProfiles(ctx, adaptsqlite.NewProfileRepo(db), nil, nil, nil)
	require.NoError(t, err)
	require.Zero(t, n)
	first, err = repo.Get(ctx, "first")
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", first.Model)
	require.Equal(t, int64(2), first.Revision)
	pending, err := repo.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestSeedProfilesSnapshotFailureWritesNothing(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewProfileRepo(db)
	_, err = db.Exec(`CREATE TRIGGER fail_snapshot BEFORE INSERT ON profile_seed_pending
		WHEN NEW.alias = 'second' BEGIN SELECT RAISE(ABORT, 'seed snapshot fault'); END`)
	require.NoError(t, err)
	_, _, err = SeedProfiles(ctx, repo, nil, []provider.Profile{
		{Alias: "first", Kind: provider.ProfileKindChat},
		{Alias: "second", Kind: provider.ProfileKindChat},
	}, nil)
	require.ErrorContains(t, err, "seed snapshot fault")
	count, err := repo.Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_snapshot`)
	require.NoError(t, err)
	// Both the pending rows and the initialized marker must have rolled back.
	n, _, err := SeedProfiles(ctx, repo, testSeeder(repo), []provider.Profile{{Alias: "retry", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-6"}}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = repo.Get(ctx, "first")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)
}

type unavailableSeedCatalog struct {
	provider.EligibleCandidateSource
	t *testing.T
}

func (s unavailableSeedCatalog) Newest(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	require.Equal(s.t, "pending", p.Alias)
	require.Empty(s.t, p.Model)
	require.Equal(s.t, &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}, p.FollowTarget)
	return provider.EligibleCandidate{}, context.DeadlineExceeded
}

// VersionRequests answers the pinned "existing" row's own route, so it passes
// the trigger grammar as any pinned seed must (paired:12).
func (s unavailableSeedCatalog) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}

func TestSeedProfilesDiscoveryTimeoutPreservesRowsAndPending(t *testing.T) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewProfileRepo(db)
	svc := testSeeder(repo)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), unavailableSeedCatalog{t: t})
	input := []provider.Profile{
		{Alias: "existing", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5-20250929"},
		{Alias: "pending", Kind: provider.ProfileKindChat, SelectionMode: provider.SelectionFollowing,
			FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}},
	}
	var causes []error
	n, skipped, err := SeedProfiles(ctx, repo, svc, input, func(_ string, err error) { causes = append(causes, err) })
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 1, skipped)
	require.Len(t, causes, 1)
	require.ErrorIs(t, causes[0], context.DeadlineExceeded)
	before, err := repo.List(ctx)
	require.NoError(t, err)
	n, skipped, err = SeedProfiles(ctx, repo, svc, nil, nil)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 1, skipped)
	after, err := repo.List(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after, "discovery failure cannot change existing rows")
	pending, err := repo.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].Alias)
}
