package provider

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePinRoutes_LunaRouteCodexKeepsProviderAndGrammar(t *testing.T) {
	ctx := context.Background()
	versions := modelidentity.NewVersionResolver()
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	p := Profile{Alias: "c", Kind: ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3", Backend: "codex"}
	requests := func(model string) []VersionRequest {
		return []VersionRequest{{Model: model, RouteProvider: "lunaroute", ConnectionRef: "lr", Backend: "codex"}}
	}
	opaque, err := ValidatePinRoutes(ctx, p, requests(p.Model), versions, caps)
	require.NoError(t, err)
	assert.Empty(t, opaque)
	for _, model := range []string{"gpt-6.1-sol", "glm-999.9", "muse-spark-open", "claude-sonnet-5-lr1"} {
		p.Model = model
		_, err = ValidatePinRoutes(ctx, p, requests(model), versions, caps)
		require.ErrorIs(t, err, ErrSpecificVersionUnavailable)
		require.ErrorIs(t, err, ErrLunaRouteModelSpelling)
	}
	p.Model = "glm-5.3"
	glm := fullGLM()
	glm.Backends = map[string]SupportFact{"codex": {Support: SupportUnsupported, Evidence: "ev"}}
	_, err = ValidatePinRoutes(ctx, p, requests(p.Model), versions, fakeLRCaps{"glm-5.3": glm})
	require.ErrorIs(t, err, ErrLunaRouteIncompatible)
	p.Effort = EffortNone
	_, err = ValidatePinRoutes(ctx, p, requests(p.Model), versions, caps)
	require.ErrorIs(t, err, ErrLunaRouteAxisUnsupported, "none would become minimal on codex")
}

func TestValidatePinRoutes_NonLunaRouteCodexCarveOutUnchanged(t *testing.T) {
	p := Profile{Alias: "s", Kind: ProfileKindAgent, ConnectionRef: "or", Model: "gpt-6.1-sol", Backend: "codex"}
	_, err := ValidatePinRoutes(t.Context(), p, []VersionRequest{{Model: p.Model, RouteProvider: "openrouter", ConnectionRef: "or", Backend: "codex"}}, modelidentity.NewVersionResolver(), nil)
	require.NoError(t, err)
}
