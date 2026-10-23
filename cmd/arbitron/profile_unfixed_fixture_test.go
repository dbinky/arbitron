package main

import (
	"context"
	"errors"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/require"
)

// storeProfileUnchecked persists p exactly as given, bypassing the admin
// write-time fixed-version check (D15), then recaches. Trigger-time rejection
// tests need a mutable-alias profile that the admin surface now refuses to
// store: a connection edit or a pre-D15 row can still produce one. The recache
// is an unchanged resubmit of the stored row, which skips selection checks.
func storeProfileUnchecked(t *testing.T, ctx context.Context, ds *domainStack, p provider.Profile) {
	t.Helper()
	repo := adaptsqlite.NewProfileRepo(ds.db)
	prior, err := repo.Get(ctx, p.Alias)
	switch {
	case err == nil:
		_, err = repo.UpdateExpected(ctx, prior, p)
	case errors.Is(err, provider.ErrProfileNotFound):
		err = repo.Create(ctx, p)
	}
	require.NoError(t, err)
	stored, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	_, _, err = ds.profileSvc.Update(ctx, stored)
	require.NoError(t, err)
}

// storeConfiguredFollowing persists the configured YAML entry alias as the
// following row an earlier proved create (or refresh) leaves behind. A first
// boot refuses an explicit following start it cannot prove (no catalog yet, and
// these fixtures create the connection after boot), so the helper first asserts
// the seed skipped it, then stores it for refresh fixtures that start from an
// existing following row.
func storeConfiguredFollowing(t *testing.T, ctx context.Context, ds *domainStack, alias string) {
	t.Helper()
	repo := adaptsqlite.NewProfileRepo(ds.db)
	_, err := repo.Get(ctx, alias)
	require.ErrorIs(t, err, provider.ErrProfileNotFound, "boot must refuse an unproved explicit following seed")
	for _, p := range configadapter.ProfilesFromConfig(ds.cfg.Profiles) {
		if p.Alias == alias {
			require.Equal(t, provider.SelectionFollowing, p.SelectionMode)
			require.NoError(t, repo.Create(ctx, p))
			// Reload the stack's own cache (the advancement publisher) so the
			// row starts at revision 1, as a real create leaves it.
			cache, ok := ds.refreshDeps.Publisher.(interface{ Reload(context.Context) error })
			require.True(t, ok, "the root publishes advancements through the profile cache")
			require.NoError(t, cache.Reload(ctx))
			return
		}
	}
	t.Fatalf("no configured profile %q", alias)
}
