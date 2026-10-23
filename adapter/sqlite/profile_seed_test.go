package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ReplaceProfileSeed rewrites only an unfinished entry's original; it never
// adds an entry or revives a completed one.
func TestReplaceProfileSeedRewritesOnlyUnfinishedEntries(t *testing.T) {
	ctx := context.Background()
	repo := NewProfileRepo(setupTestDB(t).DB)
	_, err := repo.PendingProfileSeed(ctx, []provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat},
		{Alias: "done", Kind: provider.ProfileKindChat, Model: "claude-sonnet-5-5"},
	})
	require.NoError(t, err)
	require.NoError(t, repo.CompleteProfileSeed(ctx, "done"))

	require.NoError(t, repo.ReplaceProfileSeed(ctx, provider.Profile{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet-5-5"}))
	require.NoError(t, repo.ReplaceProfileSeed(ctx, provider.Profile{Alias: "done", Kind: provider.ProfileKindChat, Model: "claude-opus-5-5"}))
	require.NoError(t, repo.ReplaceProfileSeed(ctx, provider.Profile{Alias: "new", Kind: provider.ProfileKindChat, Model: "claude-opus-5-5"}))

	pending, err := repo.PendingProfileSeed(ctx, nil)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "default-chat", pending[0].Alias)
	assert.Equal(t, "claude-sonnet-5-5", pending[0].Model)
}
