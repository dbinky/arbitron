package modelidentity_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteSourceConfirmedVersions(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
			got, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{
				Model: model, RouteProvider: "lunaroute", ConnectionRef: "lr", Backend: backend,
			})
			require.NoError(t, err)
			assert.Equal(t, model, got)
		}
	}
	for _, selected := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		for _, reported := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash", "", "claude-sonnet-5-6"} {
			assert.Equal(t, selected == reported, modelidentity.LunaRouteExecutedModelAccepted(selected, reported))
		}
	}
}

func TestLunaRouteRevokedIdentitiesRefused(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-lr1", "claude-opus-lr29", "claude-haiku-lr37", "claude-sonnet-lr", "claude-sonnet-lr-next", "deepseek-4.1-flash-1017", "glm-999.9", "lunaroute/glm-5.3", "glm-5.3-background"} {
		t.Run(model, func(t *testing.T) {
			for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
				got, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: model, RouteProvider: "lunaroute", Backend: backend})
				require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
				assert.Empty(t, got)
				assert.Contains(t, err.Error(), "reviewed fixed LunaRoute id")
			}
			for _, reported := range []string{model, "claude-sonnet-5-6", "glm-5.3", ""} {
				assert.False(t, modelidentity.LunaRouteExecutedModelAccepted(model, reported))
			}
		})
	}
}
