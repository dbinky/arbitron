package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preflight names every persisted pinned profile that will refuse triggers,
// with the grammar's reason, and skips following selections.
func TestPinPreflightNamesPinnedProfilesThatRefuse(t *testing.T) {
	store := newMemProfileStore(
		pinnedProfile("ok", "claude-sonnet-5-5"),
		pinnedProfile("empty", ""),
		pinnedProfile("alias", "sonnet"),
		followingProfile("follow", ""),
	)
	svc, _ := newFollowingService(store, modelidentity.NewVersionResolver(), &pinnedSource{})

	refusals, unchecked, err := svc.PinPreflight(context.Background())
	require.NoError(t, err)
	assert.Empty(t, unchecked)
	byAlias := map[string]provider.PinRefusal{}
	for _, r := range refusals {
		byAlias[r.Alias] = r
	}
	require.Len(t, byAlias, 2, "only the two unpinnable fixed-mode profiles")
	require.ErrorIs(t, byAlias["empty"].Err, provider.ErrSpecificVersionUnavailable)
	require.ErrorIs(t, byAlias["alias"].Err, provider.ErrSpecificVersionUnavailable)
	assert.Contains(t, byAlias["alias"].Err.Error(), "accepted spellings")
	assert.Equal(t, "sonnet", byAlias["alias"].Model)
}
