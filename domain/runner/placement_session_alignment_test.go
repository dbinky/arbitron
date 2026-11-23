package runner

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPlaceSession_UnknownOrUnavailableSourceWaitsWithoutFabricatingLogout(t *testing.T) {
	for name, mutate := range map[string]func(*PoolSnapshot){
		"missing":          func(s *PoolSnapshot) { s.Sources = s.Sources[1:] },
		"auth unknown":     func(s *PoolSnapshot) { s.Sources[0].Health.Auth = AuthUnknown },
		"capacity unknown": func(s *PoolSnapshot) { s.Sources[0].Health.Capacity = "" },
		"unavailable":      func(s *PoolSnapshot) { s.Sources[0].UnavailableReason = "unsupported harness" },
		"unknown exhausted auth": func(s *PoolSnapshot) {
			s.Sources[0].Health.Auth = AuthUnknown
			s.Sources[0].Health.Capacity = CapacityExhausted
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := sessPool()
			mutate(&s)
			d := Place(sessReq("mac-a"), s, PlacementPolicy{})
			assert.False(t, d.Placed)
			assert.False(t, d.MoveRequired)
			require.NotNil(t, d.Wait)
			assert.Contains(t, d.Wait.Detail, "session pinned to mac-a")
		})
	}
}

func TestPlaceSession_HeadroomUnknownUsesSharedRankAndOnlyAllowedTriggersMove(t *testing.T) {
	s := sessPool()
	low := s.Sources[0]
	low.ID = "low"
	low.Health.Headroom = sessHeadroom(0.1)
	unknown := low
	unknown.ID = "unknown"
	unknown.Health.Headroom = nil
	s.Sources = []Source{low, unknown}
	d := Place(sessReq("mac-a"), s, PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, unknown.ID, d.Source)
	high := low
	high.ID = "high"
	high.Health.Headroom = sessHeadroom(0.8)
	s.Sources = append(s.Sources, high)
	d = Place(sessReq("mac-a"), s, PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, high.ID, d.Source)
	assert.False(t, d.MoveRequired)
}
