package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRunnerRegistryRepo_CapabilitiesRoundTrip(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredAnthropicSubscription, "", "anthropic")
	src.Capabilities = []runner.ExecutionCapability{{Op: runner.OpChat, Provider: "test", Models: []string{"exact"}, Tools: []string{"Read"}, ExecutedModel: true}}
	for _, model := range []string{"exact", "updated"} {
		src.Capabilities[0].Models[0] = model
		require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))
		_, sources, err := repo.GetRunner(ctx, runner.BuiltInRunnerID)
		require.NoError(t, err)
		require.Len(t, sources, 1)
		require.Equal(t, src.Capabilities, sources[0].Capabilities)
	}
}
