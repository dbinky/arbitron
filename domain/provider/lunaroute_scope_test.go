package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These legacy fixture sources now also expose the connection-only metadata
// seam; their route methods remain unchanged.
func (s *lrPolicySource) ConnectionProvider(ctx context.Context, p Profile) (string, error) {
	_, err := s.VersionRequests(ctx, p)
	return s.provider, err
}
func (s lrRouteSource) ConnectionProvider(context.Context, Profile) (string, error) {
	return s.requests[0].RouteProvider, nil
}

type scopedPolicySource struct {
	EligibleCandidateSource
	provider                                string
	metadataErr                             error
	catalogErr                              error
	metadataCalls, routeCalls, catalogCalls int
}

func (s *scopedPolicySource) ConnectionProvider(_ context.Context, p Profile) (string, error) {
	if p.ConnectionRef != "opaque-connection" {
		panic("connection metadata must use the actual reference")
	}
	s.metadataCalls++
	return s.provider, s.metadataErr
}
func (s *scopedPolicySource) VersionRequests(_ context.Context, p Profile) ([]VersionRequest, error) {
	s.routeCalls++
	return []VersionRequest{{Model: p.Model, RouteProvider: s.provider, ConnectionRef: p.ConnectionRef}}, nil
}
func (s *scopedPolicySource) Newest(context.Context, Profile) (EligibleCandidate, error) {
	s.catalogCalls++
	return EligibleCandidate{}, s.catalogErr
}

func TestLunaRouteScopeFollowingBeforeCatalog(t *testing.T) {
	for _, routing := range []string{"lunaroute", "openrouter"} {
		t.Run(routing, func(t *testing.T) {
			catalogErr := errors.New("catalog unavailable")
			src := &scopedPolicySource{provider: routing, catalogErr: catalogErr}
			p := Profile{Alias: "scope", Kind: ProfileKindChat, ConnectionRef: "opaque-connection", SelectionMode: SelectionFollowing, FollowTarget: &FollowTarget{Provider: "zhipuai", Tier: "glm"}}
			_, err := ValidateSelection(context.Background(), p, nil, src, nil)
			if routing == "lunaroute" {
				require.ErrorIs(t, err, ErrSelectionPortsMissing)
				assert.Zero(t, src.catalogCalls)
				_, err = ValidateSelection(context.Background(), p, modelidentity.NewVersionResolver(), src, nil)
				require.ErrorIs(t, err, ErrLunaRoutePinnedOnly)
				assert.Zero(t, src.catalogCalls)
			} else {
				require.ErrorIs(t, err, catalogErr)
				assert.Equal(t, 1, src.catalogCalls)
			}
			assert.Positive(t, src.metadataCalls)
			assert.Zero(t, src.routeCalls, "connection metadata must not enumerate model-dependent routes")
		})
	}
}

func TestLunaRouteScopeUnchangedEdits(t *testing.T) {
	for _, routing := range []string{"lunaroute", "openrouter"} {
		t.Run(routing, func(t *testing.T) {
			ctx, svc, _, store, p := lrPolicyFixture(t)
			p.Kind, p.Backend, p.ConnectionRef = ProfileKindChat, "", "opaque-connection"
			p.Effort = EffortHigh
			store.row = p
			src := &scopedPolicySource{provider: routing}
			svc.SetModelSelection(modelidentity.NewVersionResolver(), src)
			_, eff, err := svc.Update(ctx, p)
			require.NoError(t, err)
			assert.Equal(t, EffortHigh, eff.Effort)
			assert.Equal(t, 1, store.writes)
			if routing == "lunaroute" {
				assert.Positive(t, src.routeCalls)
			} else {
				assert.Zero(t, src.routeCalls)
			}
			assert.Positive(t, src.metadataCalls)
			src.metadataErr = errors.New("connection metadata unavailable")
			_, _, err = svc.Update(ctx, p)
			require.ErrorIs(t, err, src.metadataErr)
			assert.Equal(t, 1, store.writes, "metadata failure must not persist an unchecked edit")
		})
	}
}
