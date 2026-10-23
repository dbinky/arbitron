package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Expose only CRUD, deliberately withholding the conditional mutation port.
type unconditionalProfileStore struct{ provider.ProfileStore }

func TestProfileMutationMissingPortFailsClosed(t *testing.T) {
	for _, method := range []string{"full", "patch", "delete"} {
		t.Run(method, func(t *testing.T) {
			p := provider.Profile{Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-sonnet-4-5"}
			store := newMemProfileStore(p)
			reload := &fakeReloader{}
			svc := provider.NewProfileService(unconditionalProfileStore{store}, rules(), reload)
			var err error
			switch method {
			case "full":
				_, _, err = svc.Update(admins(), p)
			case "patch":
				_, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{})
			case "delete":
				err = svc.Delete(admins(), p.Alias)
			}
			require.ErrorIs(t, err, provider.ErrProfileMutationUnavailable)
			assert.Zero(t, store.writeCount())
			assert.Zero(t, reload.calls)
			got, err := store.Get(admins(), p.Alias)
			require.NoError(t, err)
			assert.Equal(t, p, got)
		})
	}
}

// Return a real prior row after a competing write has already changed it.
// The conditional port must receive that same prior, not a newly loaded row.
type staleProfileRead struct {
	*memProfileStore
	newer provider.Profile
}

func (s *staleProfileRead) Get(ctx context.Context, alias string) (provider.Profile, error) {
	prior, err := s.memProfileStore.Get(ctx, alias)
	if err == nil {
		err = s.memProfileStore.Update(ctx, s.newer)
	}
	return prior, err
}

func TestProfileMutationFollowingToolsRace(t *testing.T) {
	for _, method := range []string{"full", "patch", "delete"} {
		t.Run(method, func(t *testing.T) {
			old := followingProfile("worker", "claude-sonnet-4-5")
			old.Revision = 1
			old.AllowedTools = []string{"Read"}
			newer := old.Clone()
			newer.Model = "claude-sonnet-4-6"
			newer.Revision++
			store := &staleProfileRead{newMemProfileStore(old), newer}
			next := old.Clone()
			next.AllowedTools = []string{"Glob"}
			source := &configurationSource{t: t, expected: next}
			reload := &fakeReloader{}
			svc := provider.NewProfileService(store, rules(), reload)
			svc.SetModelSelection(&fakeVersions{}, source)
			var err error
			switch method {
			case "full":
				_, _, err = svc.Update(admins(), next)
			case "patch":
				_, _, err = svc.UpdatePatch(admins(), old.Alias, provider.ProfilePatch{AllowedTools: &next.AllowedTools})
			case "delete":
				err = svc.Delete(admins(), old.Alias)
			}
			require.ErrorIs(t, err, provider.ErrAdvanceConflict)
			got, err := store.memProfileStore.Get(admins(), old.Alias)
			require.NoError(t, err)
			assert.Equal(t, newer, got)
			assert.Equal(t, 1, store.writeCount(), "only the competing update may write")
			assert.Zero(t, reload.calls)
		})
	}
}
