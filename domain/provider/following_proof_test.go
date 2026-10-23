package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowingPreparationRejectsMissingEvidence(t *testing.T) {
	for name, change := range map[string]func(*preparedSource){
		"no routes":                  func(s *preparedSource) { s.requests = nil },
		"different route model":      func(s *preparedSource) { s.requests[0].Model = "claude-sonnet-4-5" },
		"different candidate model":  func(s *preparedSource) { s.proof.Model = "different" },
		"missing canonical identity": func(s *preparedSource) { s.proof.CanonicalID = "" },
		"missing path revision":      func(s *preparedSource) { s.proof.PathRevision = "" },
		"mismatched token":           func(s *preparedSource) { s.proof.ExecutionRevision.ID = "different" },
		"missing token":              func(s *preparedSource) { s.proof.ExecutionRevision = provider.ExecutionRevision{} },
		"unsupported second route": func(s *preparedSource) {
			s.requests = append(s.requests, provider.VersionRequest{Model: s.expected.Model, RouteProvider: "azure-openai", ConnectionRef: "deployment"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := followingProfile("worker", "claude-sonnet-4-6")
			proof := selectionProof()
			proof.Model = p.Model
			source := &preparedSource{t: t, expected: p, proof: proof, requests: []provider.VersionRequest{provider.VersionRequestFor(p, "")}}
			change(source)
			store := newMemProfileStore()
			svc, reload := newFollowingService(store, modelidentity.NewVersionResolver(), source)
			_, _, err := svc.Create(admins(), p)
			require.Error(t, err)
			assert.Zero(t, store.writeCount())
			assert.Zero(t, reload.calls)
			assert.Zero(t, source.rechecks)
		})
	}
}

func TestFollowingCatalogCandidateRequiresFixedVersion(t *testing.T) {
	p := followingProfile("worker", "sonnet")
	proof := selectionProof()
	proof.Model = p.Model
	source := &preparedSource{t: t, expected: p, proof: proof, requests: []provider.VersionRequest{provider.VersionRequestFor(p, "")}}
	store := newMemProfileStore()
	svc, reload := newFollowingService(store, modelidentity.NewVersionResolver(), source)
	p.Model = ""
	_, _, err := svc.Create(admins(), p)
	require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	assert.Zero(t, store.writeCount())
	assert.Zero(t, reload.calls)
}

func TestFollowingMissingValidatedStoreDoesNotFallBack(t *testing.T) {
	p := followingProfile("worker", "claude-sonnet-4-6")
	store := newMemProfileStore()
	reload := &fakeReloader{}
	svc := provider.NewProfileService(unconditionalProfileStore{store}, rules(), reload)
	svc.SetModelSelection(&fakeVersions{}, &fakeSource{})
	_, _, err := svc.Create(admins(), p)
	require.ErrorIs(t, err, provider.ErrProfileMutationUnavailable)
	assert.Zero(t, store.writeCount())
	assert.Zero(t, reload.calls)
}

func TestFollowingValidatedWriteChecksPersistentProof(t *testing.T) {
	for _, method := range []string{"supplied", "catalog", "patch"} {
		t.Run(method, func(t *testing.T) {
			db, repo, conns, gate, cache := preparationDB(t)
			var sequence int
			var name, path string
			require.NoError(t, db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path))
			other, err := sqlite.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = other.Close() })
			p := followingProfile("worker", "claude-sonnet-4-6")
			p.ConnectionRef = "route"
			p.ContextWindow = provider.ContextDefault
			p.Effort = provider.EffortDefault
			p.AllowedTools = []string{}
			var prior provider.Profile
			if method == "patch" {
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
			source.revalidate = func(ctx context.Context) error {
				conn, err := conns.Get(ctx, "route")
				require.NoError(t, err)
				require.Equal(t, source.proof.ExecutionRevision.Connections["route"], conn.ConfigRevision)
				// Another process can change persistent state after the in-process recheck.
				// A green source cannot replace the SQL transaction's revision checks.
				_, err = other.Exec(`UPDATE connections SET config_revision=config_revision+1 WHERE name='route'`)
				return err
			}
			svc := provider.NewProfileService(repo, rules(), cache)
			svc.SetModelSelection(modelidentity.NewVersionResolver(), source)
			svc.SetAdvancementDependencies(nil, nil, gate)
			next := p.Clone()
			if method == "catalog" {
				next.Model = ""
			}
			if method == "patch" {
				_, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{Model: &p.Model})
			} else {
				_, _, err = svc.Create(admins(), next)
			}
			assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			rows, err := repo.List(admins())
			require.NoError(t, err)
			if prior.Alias == "" {
				assert.Empty(t, rows)
			} else {
				assert.Equal(t, []provider.Profile{prior}, rows)
			}
			after, afterErr := cache.SnapshotProfiles([]string{p.Alias})
			assert.Equal(t, before, after)
			if beforeErr != nil {
				assert.ErrorIs(t, afterErr, provider.ErrUnknownProfile)
			} else {
				require.NoError(t, afterErr)
			}
			assert.Equal(t, 1, source.rechecks)
		})
	}
}
