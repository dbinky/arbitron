package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type batchSeedSource struct {
	expected  map[string]provider.Profile
	gate      *provider.ExecutionGate
	policy    int64
	prepared  int
	rechecked int
	onPrepare func(provider.Profile) error
	onRecheck func(provider.Profile) error
}

func (s *batchSeedSource) check(p provider.Profile) error {
	if !reflect.DeepEqual(s.expected[p.Alias], p) {
		return errors.New("seed lost prospective configuration")
	}
	return nil
}
func (s *batchSeedSource) Newest(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	want := s.expected[p.Alias].Clone()
	want.Model = ""
	if !reflect.DeepEqual(want, p) {
		return provider.EligibleCandidate{}, errors.New("seed newest lost prospective configuration")
	}
	return s.proof(s.expected[p.Alias]), nil
}
func (s *batchSeedSource) proof(p provider.Profile) provider.EligibleCandidate {
	return provider.EligibleCandidate{Model: p.Model, CanonicalID: "anthropic/" + p.Model, PathRevision: "seed-proof", ExecutionRevision: provider.ExecutionRevision{ID: "seed-proof", PolicyRevision: s.policy}}
}
func (s *batchSeedSource) ValidateStart(ctx context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	if err := s.check(p); err != nil {
		return provider.EligibleCandidate{}, err
	}
	read := make(chan error, 1)
	go func() { read <- s.gate.WithRead(ctx, func(context.Context) error { return nil }) }()
	select {
	case err := <-read:
		if err != nil {
			return provider.EligibleCandidate{}, err
		}
	case <-time.After(5 * time.Second):
		return provider.EligibleCandidate{}, errors.New("seed discovery holds mutation lease")
	}
	if s.onPrepare != nil {
		if err := s.onPrepare(p); err != nil {
			return provider.EligibleCandidate{}, err
		}
	}
	s.prepared++
	return s.proof(p), nil
}
func (s *batchSeedSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if err := s.check(p); err != nil {
		return nil, err
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}
func (s *batchSeedSource) RevalidateStart(_ context.Context, p provider.Profile, c provider.EligibleCandidate) error {
	if err := s.check(p); err != nil {
		return err
	}
	if !reflect.DeepEqual(c, s.proof(p)) {
		return errors.New("seed lost exact candidate proof")
	}
	s.rechecked++
	if s.onRecheck != nil {
		return s.onRecheck(p)
	}
	return nil
}
func batchSeedFixture(t *testing.T) (*sql.DB, *sqlite.ProfileRepo, *provider.ProfileService, *adapterprovider.ProfileCache, *batchSeedSource, []provider.Profile) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "seed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	repo := sqlite.NewProfileRepo(db)
	cache := adapterprovider.NewProfileCache(repo)
	require.NoError(t, cache.Load(context.Background()))
	source := &batchSeedSource{gate: provider.NewExecutionGate(), expected: map[string]provider.Profile{}}
	require.NoError(t, db.QueryRow(`SELECT revision FROM model_identity_policy WHERE id=1`).Scan(&source.policy))
	input := []provider.Profile{}
	for _, alias := range []string{"first", "second"} {
		p := provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-6", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}, AllowedTools: []string{"Read"}, ContextWindow: provider.ContextDefault, Effort: provider.EffortHigh}
		source.expected[alias] = p.Clone()
		p.Model = ""
		input = append(input, p)
	}
	svc := provider.NewProfileService(repo, testRules(), cache)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), source)
	svc.SetAdvancementDependencies(nil, nil, source.gate)
	return db, repo, svc, cache, source, input
}

func TestFollowingPrepareDoesNotHoldMutationLeaseSeedBatch(t *testing.T) {
	_, repo, svc, cache, src, input := batchSeedFixture(t)
	src.onPrepare = func(provider.Profile) error {
		n, err := repo.Count(context.Background())
		if err != nil {
			return err
		}
		if n != 0 {
			return errors.New("seed persisted before whole batch preparation")
		}
		return nil
	}
	src.onRecheck = func(provider.Profile) error {
		if src.prepared != len(input) {
			return errors.New("seed revalidated before whole batch preparation")
		}
		n, err := repo.Count(context.Background())
		if err != nil {
			return err
		}
		if n != 0 {
			return errors.New("seed inserted before rechecking all batch proofs")
		}
		return nil
	}
	var skippedErrors []error
	n, skipped, err := SeedProfiles(context.Background(), repo, svc, input, func(_ string, err error) { skippedErrors = append(skippedErrors, err) })
	require.NoError(t, err)
	require.Empty(t, skippedErrors)
	assert.Equal(t, 2, n)
	assert.Zero(t, skipped)
	assert.Equal(t, 2, src.rechecked)
	for _, p := range src.expected {
		stored, err := repo.Get(context.Background(), p.Alias)
		require.NoError(t, err)
		assert.Equal(t, p.Model, stored.Model)
		assert.Equal(t, p.FollowTarget, stored.FollowTarget)
		assert.Equal(t, p.AllowedTools, stored.AllowedTools)
		assert.Equal(t, int64(1), stored.Revision)
		cached, err := cache.SnapshotProfiles([]string{p.Alias})
		require.NoError(t, err)
		assert.Equal(t, stored, cached[p.Alias])
	}
	pending, err := repo.PendingProfileSeed(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, pending)
	// A catalog outage on later boots must not revalidate or overwrite existing rows.
	svc.SetModelSelection(nil, nil)
	n, skipped, err = SeedProfiles(context.Background(), repo, svc, input, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, skipped)
}

func TestFollowingSeedRechecksOriginalPendingMembership(t *testing.T) {
	_, repo, svc, cache, src, input := batchSeedFixture(t)
	input = input[:1]
	src.onPrepare = func(p provider.Profile) error { return repo.CompleteProfileSeed(context.Background(), p.Alias) }
	n, _, err := SeedProfiles(context.Background(), repo, svc, input, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = repo.Get(context.Background(), "first")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	_, err = cache.SnapshotProfiles([]string{"first"})
	assert.Error(t, err)
}

func TestFollowingSeedRejectsStaleProofAndRetainsOriginal(t *testing.T) {
	_, repo, svc, cache, src, input := batchSeedFixture(t)
	fault := provider.ErrExecutionRevisionStale
	src.onRecheck = func(p provider.Profile) error {
		if p.Alias == "first" {
			return fault
		}
		return nil
	}
	var causes []error
	n, skipped, err := SeedProfiles(context.Background(), repo, svc, input, func(_ string, err error) { causes = append(causes, err) })
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, skipped)
	require.Len(t, causes, 1)
	assert.ErrorIs(t, causes[0], fault)
	_, err = repo.Get(context.Background(), "first")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	_, err = cache.SnapshotProfiles([]string{"first"})
	assert.Error(t, err)
	pending, err := repo.PendingProfileSeed(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, input[0], pending[0])
	src.onRecheck = nil
	n, skipped, err = SeedProfiles(context.Background(), repo, svc, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Zero(t, skipped)
	stored, err := repo.Get(context.Background(), "first")
	require.NoError(t, err)
	assert.Equal(t, src.expected["first"].Model, stored.Model)
	cached, err := cache.SnapshotProfiles([]string{"first"})
	require.NoError(t, err)
	assert.Equal(t, stored, cached["first"])
}

func TestFollowingSeedPreservesConcurrentAdminAlias(t *testing.T) {
	_, repo, svc, cache, src, input := batchSeedFixture(t)
	input = input[:1]
	replacement := src.expected["first"].Clone()
	replacement.Model = "claude-sonnet-4-5"
	replacement.SelectionMode = provider.SelectionPinned
	replacement.FollowTarget = nil
	creator := provider.NewProfileService(repo, testRules(), cache)
	creator.SetModelSelection(modelidentity.NewVersionResolver(), seedPinnedRoutes{})
	creator.SetAdvancementDependencies(nil, nil, src.gate)
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	src.onPrepare = func(provider.Profile) error {
		_, _, err := creator.Create(admin, replacement)
		return err
	}
	n, skipped, err := SeedProfiles(context.Background(), repo, svc, input, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, skipped)
	assert.Zero(t, src.rechecked)
	stored, err := repo.Get(context.Background(), replacement.Alias)
	require.NoError(t, err)
	assert.Equal(t, replacement.Model, stored.Model)
	assert.Equal(t, provider.SelectionPinned, stored.SelectionMode)
	assert.Equal(t, int64(1), stored.Revision)
	cached, err := cache.SnapshotProfiles([]string{replacement.Alias})
	require.NoError(t, err)
	assert.Equal(t, stored, cached[replacement.Alias], "seeding cannot publish its stale prospective row over the concurrent admin's binding")
	pending, err := repo.PendingProfileSeed(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestSeedProfilesRequiresMutationGate(t *testing.T) {
	db, repo, svc, _, _, input := batchSeedFixture(t)
	svc.SetAdvancementDependencies(nil, nil, nil)
	n, _, err := SeedProfiles(context.Background(), repo, svc, input, nil)
	require.ErrorContains(t, err, "profile seed mutation gate not configured")
	assert.Zero(t, n)
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM profile_seed_state`).Scan(&rows))
	assert.Zero(t, rows, "wiring fault must precede even the seed snapshot write")
	count, err := repo.Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, count)
}
