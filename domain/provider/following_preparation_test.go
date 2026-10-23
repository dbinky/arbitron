package provider_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const preparationWait = 5 * time.Second

// Checking selection port: the entire prospective profile and the exact proof
// must survive discovery, route validation and the final read-only recheck.
type preparedSource struct {
	t          *testing.T
	expected   provider.Profile
	proof      provider.EligibleCandidate
	requests   []provider.VersionRequest
	discover   func(context.Context) error
	afterProof func()
	revalidate func(context.Context) error
	rechecks   int
}

func (s *preparedSource) Newest(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	want := s.expected.Clone()
	want.Model = ""
	if !assert.Equal(s.t, want, p) {
		return provider.EligibleCandidate{}, errors.New("incomplete newest prospective profile")
	}
	return s.proof.Clone(), nil
}
func (s *preparedSource) ValidateStart(ctx context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	if !assert.Equal(s.t, s.expected, p) {
		return provider.EligibleCandidate{}, errors.New("incomplete starting prospective profile")
	}
	if s.discover != nil {
		if err := s.discover(ctx); err != nil {
			return provider.EligibleCandidate{}, err
		}
	}
	proof := s.proof.Clone()
	if s.afterProof != nil {
		s.afterProof()
	}
	return proof, nil
}
func (s *preparedSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if !assert.Equal(s.t, s.expected, p) {
		return nil, errors.New("incomplete routing prospective profile")
	}
	return s.requests, nil
}
func (s *preparedSource) RevalidateStart(ctx context.Context, p provider.Profile, c provider.EligibleCandidate) error {
	if !assert.Equal(s.t, s.expected, p) || !assert.Equal(s.t, s.proof, c) {
		return errors.New("revalidation lost prospective profile or proof")
	}
	s.rechecks++
	if s.revalidate != nil {
		return s.revalidate(ctx)
	}
	return nil
}

func preparationDB(t *testing.T) (*sql.DB, *sqlite.ProfileRepo, *sqlite.ConnectionRepo, *provider.ExecutionGate, *adapterprovider.ProfileCache) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "profiles.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	crypto, err := adaptercrypto.NewAESGCM(make([]byte, 32))
	require.NoError(t, err)
	repo := sqlite.NewProfileRepo(db)
	conns := sqlite.NewConnectionRepo(db, crypto)
	require.NoError(t, conns.Create(admins(), provider.Connection{Name: "route", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, "original"))
	cache := adapterprovider.NewProfileCache(repo)
	require.NoError(t, cache.Load(admins()))
	return db, repo, conns, provider.NewExecutionGate(), cache
}
func preparedFixture(t *testing.T, db *sql.DB, conns *sqlite.ConnectionRepo, p provider.Profile) *preparedSource {
	t.Helper()
	var revision int64
	require.NoError(t, db.QueryRow(`SELECT revision FROM model_identity_policy WHERE id=1`).Scan(&revision))
	conn, err := conns.Get(admins(), "route")
	require.NoError(t, err)
	proof := provider.EligibleCandidate{Model: p.Model, CanonicalID: "anthropic/" + p.Model, PathRevision: "prospective", ExecutionRevision: provider.ExecutionRevision{ID: "prospective", PolicyRevision: revision, Connections: map[string]int64{"route": conn.ConfigRevision}}}
	return &preparedSource{t: t, expected: p, proof: proof, requests: []provider.VersionRequest{provider.VersionRequestFor(p, conn.Provider)}}
}

func TestFollowingPrepareDoesNotHoldMutationLease(t *testing.T) {
	for _, method := range []string{"create", "patch", "replace"} {
		t.Run(method, func(t *testing.T) {
			db, repo, conns, gate, cache := preparationDB(t)
			p := followingProfile("worker", "claude-sonnet-4-6")
			p.ConnectionRef = "route"
			p.ContextWindow = provider.ContextDefault
			p.Effort = provider.EffortDefault
			p.AllowedTools = []string{}
			if method != "create" {
				prior := p.Clone()
				prior.Model = "claude-sonnet-4-5"
				require.NoError(t, repo.Create(admins(), prior))
				p.Revision = 1
			}
			source := preparedFixture(t, db, conns, p)
			source.discover = func(ctx context.Context) error {
				read := make(chan error, 1)
				go func() { read <- gate.WithRead(ctx, func(context.Context) error { return nil }) }()
				select {
				case err := <-read:
					return err
				case <-time.After(preparationWait):
					return errors.New("discovery ran under the mutation lease")
				}
			}
			svc := provider.NewProfileService(repo, rules(), cache)
			svc.SetModelSelection(modelidentity.NewVersionResolver(), source)
			svc.SetAdvancementDependencies(nil, nil, gate)
			var got provider.Profile
			var err error
			switch method {
			case "create":
				got, _, err = svc.Create(admins(), p)
			case "patch":
				got, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{Model: &p.Model})
			case "replace":
				got, _, err = svc.Update(admins(), p)
			}
			require.NoError(t, err)
			stored, err := repo.Get(admins(), p.Alias)
			require.NoError(t, err)
			assert.Equal(t, got, stored)
			assert.Equal(t, p.Model, stored.Model)
			cached, err := cache.SnapshotProfiles([]string{p.Alias})
			require.NoError(t, err)
			assert.Equal(t, stored, cached[p.Alias])
			assert.Equal(t, 1, source.rechecks)
		})
	}
}

func TestFollowingWriteRejectsChangedProspectiveProof(t *testing.T) {
	for _, mode := range []string{"supplied", "catalog", "patch", "replace"} {
		t.Run(mode, func(t *testing.T) {
			db, repo, conns, gate, cache := preparationDB(t)
			p := followingProfile("worker", "claude-sonnet-4-6")
			p.ConnectionRef = "route"
			p.ContextWindow = provider.ContextDefault
			p.Effort = provider.EffortDefault
			p.AllowedTools = []string{}
			var prior provider.Profile
			if mode == "patch" || mode == "replace" {
				prior = p.Clone()
				prior.Model = "claude-sonnet-4-5"
				require.NoError(t, repo.Create(admins(), prior))
				var err error
				prior, err = repo.Get(admins(), p.Alias)
				require.NoError(t, err)
				p.Revision = prior.Revision
				require.NoError(t, cache.Load(admins()))
			}
			before, beforeErr := cache.SnapshotProfiles([]string{p.Alias})
			source := preparedFixture(t, db, conns, p)
			connections := provider.NewConnectionService(conns, repo)
			connections.SetMutationGate(gate)
			rotate := func() error {
				conn, err := connections.Get(admins(), "route")
				if err != nil {
					return err
				}
				key := "rotated"
				return connections.Update(admins(), conn, &key)
			}
			source.revalidate = func(ctx context.Context) error {
				conn, err := conns.Get(ctx, "route")
				if err != nil {
					return err
				}
				if conn.ConfigRevision != source.proof.ExecutionRevision.Connections["route"] {
					return provider.ErrExecutionRevisionStale
				}
				return nil
			}
			svc := provider.NewProfileService(repo, rules(), cache)
			svc.SetModelSelection(modelidentity.NewVersionResolver(), source)
			svc.SetAdvancementDependencies(nil, nil, gate)
			// Delay AFTER the proof has been captured, outside the write lease. The old
			// implementation holds that lease even during discovery; use a bounded leaf
			// callback so the regression fails rather than hanging the test process.
			captured, resume := make(chan struct{}), make(chan struct{})
			source.afterProof = func() { close(captured); <-resume }
			result := make(chan error, 1)
			go func() {
				next := p.Clone()
				if mode == "catalog" {
					next.Model = ""
				}
				var err error
				switch mode {
				case "patch":
					_, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{Model: &p.Model})
				case "replace":
					_, _, err = svc.Update(admins(), next)
				default:
					_, _, err = svc.Create(admins(), next)
				}
				result <- err
			}()
			select {
			case <-captured:
			case err := <-result:
				t.Fatalf("write finished before proof capture: %v", err)
			case <-time.After(preparationWait):
				t.Fatal("proof capture timed out")
			}
			rotated := make(chan error, 1)
			go func() { rotated <- rotate() }()
			select {
			case err := <-rotated:
				close(resume)
				require.NoError(t, err)
			case <-time.After(preparationWait):
				close(resume)
				<-result
				<-rotated
				t.Fatal("proof preparation held the mutation lease")
			}
			assert.ErrorIs(t, <-result, provider.ErrExecutionRevisionStale)
			rows, err := repo.List(admins())
			require.NoError(t, err)
			if prior.Alias == "" {
				assert.Empty(t, rows)
			} else {
				assert.Equal(t, []provider.Profile{prior}, rows)
			}
			after, afterErr := cache.SnapshotProfiles([]string{p.Alias})
			assert.Equal(t, before, after, "stale proof cannot publish an executable binding")
			if beforeErr != nil {
				assert.ErrorIs(t, afterErr, provider.ErrUnknownProfile)
			} else {
				require.NoError(t, afterErr)
			}
			assert.Equal(t, 1, source.rechecks)
		})
	}
}

func TestSelectionUsesRoutingProvider(t *testing.T) {
	for _, route := range []struct {
		name, provider, backend, model string
		wantErr                        bool
	}{
		{"Azure deployment", "azure-openai", "opencode", "opaque-deployment", true},
		{"deployment default Codex", "", "codex", "gpt-5.2-sol", false},
		{"node override", "anthropic", "pi", "claude-sonnet-4-6", false},
	} {
		t.Run(route.name, func(t *testing.T) {
			p := followingProfile("worker", route.model)
			p.FollowTarget = &provider.FollowTarget{Provider: "openai", Tier: "sol"}
			p.ConnectionRef = "actual-route"
			p.AllowedTools = []string{"Read"}
			p.Effort = provider.EffortLow
			request := provider.VersionRequestFor(p, route.provider)
			request.Backend = route.backend
			if route.provider == "" {
				request.ConnectionRef = ""
			}
			proof := selectionProof()
			proof.Model = p.Model
			source := &preparedSource{t: t, expected: p, proof: proof, requests: []provider.VersionRequest{request}}
			versions := &fakeVersions{}
			store := newMemProfileStore()
			svc, _ := newFollowingService(store, versions, source)
			_, _, err := svc.Create(admins(), p)
			if route.wantErr {
				require.ErrorIs(t, err, provider.ErrInvalidSelection)
				assert.Zero(t, store.creates, "following cannot use a D8 route, even with a permissive version source")
				assert.Empty(t, versions.seen)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []provider.VersionRequest{request}, versions.seen, "route provider/backend must come from actual prospective inventory, not follow target")
		})
	}
}
