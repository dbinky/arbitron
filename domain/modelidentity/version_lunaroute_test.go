package modelidentity_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteSourceConfirmedVersions(t *testing.T) {
	for _, model := range []string{"glm-5.3", "claude-sonnet-5-lr1"} {
		for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
			got, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{
				Model: model, RouteProvider: "lunaroute", ConnectionRef: "lr", Backend: backend,
			})
			require.NoError(t, err)
			assert.Equal(t, model, got)
		}
	}
	assert.True(t, modelidentity.LunaRouteExecutedModelAccepted("claude-sonnet-5-lr1", "claude-sonnet-5-6"))
	assert.False(t, modelidentity.LunaRouteExecutedModelAccepted("claude-sonnet-5-lr1", "glm-5.3"))
}
