package provider

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// swapStore serves a mutable profile list so a test can change what the next
// Load observes.
type swapStore struct {
	fixedProfileStore
	err error
}

func (s *swapStore) List(ctx context.Context) ([]provider.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fixedProfileStore.List(ctx)
}

func TestPublishCommittedConfirmsCommittedRevision(t *testing.T) {
	st := &swapStore{fixedProfileStore: fixedProfileStore{profiles: []provider.Profile{{Alias: "a", Model: "x", Revision: 1}}}}
	c := NewProfileCache(st)
	require.NoError(t, c.Load(context.Background()))
	st.profiles = []provider.Profile{{Alias: "a", Model: "y", Revision: 2}}

	require.NoError(t, c.PublishCommitted(context.Background(), []provider.Advancement{{Alias: "a", ToModel: "y", Revision: 2}}))
	p, _ := c.get("a")
	assert.Equal(t, "y", p.Model, "publication must have reloaded the cache")
}

func TestPublishCommittedRejectsUnconfirmedState(t *testing.T) {
	adv := provider.Advancement{Alias: "a", ToModel: "y", Revision: 2}
	for name, profiles := range map[string][]provider.Profile{
		"stale revision": {{Alias: "a", Model: "x", Revision: 1}},
		"wrong model":    {{Alias: "a", Model: "z", Revision: 2}},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewProfileCache(&swapStore{fixedProfileStore: fixedProfileStore{profiles: profiles}})
			require.Error(t, c.PublishCommitted(context.Background(), []provider.Advancement{adv}))
		})
	}
}

func TestPublishCommittedAcceptsLaterAdminEdit(t *testing.T) {
	c := NewProfileCache(&swapStore{fixedProfileStore: fixedProfileStore{profiles: []provider.Profile{{Alias: "a", Model: "w", Revision: 3}}}})
	require.NoError(t, c.PublishCommitted(context.Background(), []provider.Advancement{{Alias: "a", ToModel: "y", Revision: 2}}))
}

func TestPublishCommittedReconcilesDeletedProfile(t *testing.T) {
	ctx := context.Background()
	adv := provider.Advancement{Alias: "a", ToModel: "y", Revision: 2}
	st := &swapStore{fixedProfileStore: fixedProfileStore{profiles: []provider.Profile{
		{Alias: "a", Model: "y", Revision: 2}, {Alias: "b", Model: "z", Revision: 3},
	}}}
	c := NewProfileCache(st)
	require.NoError(t, c.Load(ctx))
	st.profiles = st.profiles[1:]

	// Reconciliation must reload authoritative absence, not retain or restore Y.
	require.NoError(t, c.PublishCommitted(ctx, []provider.Advancement{adv}))
	_, err := c.SnapshotProfiles([]string{"a"})
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	profiles, err := c.SnapshotProfiles([]string{"b"})
	require.NoError(t, err)
	assert.Equal(t, st.profiles[0], profiles["b"])

	// A deleted first alias must not hide a later unconfirmed batch member.
	err = c.PublishCommitted(ctx, []provider.Advancement{adv, {Alias: "b", ToModel: "new", Revision: 4}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `publishing "b"`)
}

func TestPublishCommittedReloadFailureIsNotConfirmation(t *testing.T) {
	c := NewProfileCache(&swapStore{err: assert.AnError})
	require.ErrorIs(t, c.PublishCommitted(context.Background(), []provider.Advancement{{Alias: "a", ToModel: "y", Revision: 2}}), assert.AnError)
}

// generationStore serves every alias at one generation, advanced atomically as
// a single CommitCycle transaction advances several following profiles.
type generationStore struct {
	fixedProfileStore
	gen atomic.Int64
}

func (s *generationStore) List(context.Context) ([]provider.Profile, error) {
	g := s.gen.Load()
	model := fmt.Sprintf("m-%d", g)
	return []provider.Profile{{Alias: "a", Model: model, Revision: g}, {Alias: "b", Model: model, Revision: g}}, nil
}

// Paired seam Area 3 run pins ↔ Area 4 refresh advancement. A trigger captures
// all of its aliases from one cache generation while publication swaps in the
// next one, so no run pins alias a after a multi-alias CAS and alias b before.
func TestSnapshotProfilesNeverMixesPublishedGenerations(t *testing.T) {
	ctx := context.Background()
	st := &generationStore{}
	st.gen.Store(1)
	c := NewProfileCache(st)
	require.NoError(t, c.Load(ctx))

	const generations = 2000
	done := make(chan error, 1)
	go func() {
		defer close(done)
		for g := int64(2); g <= generations; g++ {
			st.gen.Store(g)
			model := fmt.Sprintf("m-%d", g)
			if err := c.PublishCommitted(ctx, []provider.Advancement{{Alias: "a", ToModel: model, Revision: g}, {Alias: "b", ToModel: model, Revision: g}}); err != nil {
				done <- err
				return
			}
		}
	}()
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return
		default:
		}
		snap, err := c.SnapshotProfiles([]string{"a", "b"})
		require.NoError(t, err)
		require.Equal(t, snap["a"].Model, snap["b"].Model, "one trigger captured two cache generations")
	}
}
