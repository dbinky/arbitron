package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrReq(model, backend string) VersionRequest {
	return VersionRequest{Model: model, RouteProvider: "lunaroute", ConnectionRef: "lr", Backend: backend}
}

func TestValidatePinRoutes_LunaRoute(t *testing.T) {
	DisableLunaRouteBackendsForTest(t, "codex", "opencode")
	ctx := context.Background()
	versions := modelidentity.NewVersionResolver()
	caps := fakeLRCaps{"glm-5.3": fullGLM()}
	agent := Profile{Alias: "a", Kind: ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3", Backend: "codex"}
	_, err := ValidatePinRoutes(ctx, agent, []VersionRequest{lrReq(agent.Model, "codex")}, versions, caps)
	require.ErrorIs(t, err, ErrLunaRouteBackendUnsupported)
	assert.Contains(t, err.Error(), "LunaRoute on codex not yet supported")
	EnableLunaRouteBackendsForTest(t, "opencode")
	agent.Backend = "opencode"
	opaque, err := ValidatePinRoutes(ctx, agent, []VersionRequest{lrReq(agent.Model, agent.Backend)}, versions, caps)
	require.NoError(t, err)
	assert.Empty(t, opaque)
	noBackend := agent
	noBackend.Backend = ""
	_, err = ValidatePinRoutes(ctx, noBackend, []VersionRequest{lrReq(agent.Model, agent.Backend)}, versions, caps)
	require.ErrorIs(t, err, ErrLunaRouteBackendRequired)
	following := agent
	following.SelectionMode = SelectionFollowing
	following.FollowTarget = &FollowTarget{Provider: "zhipuai", Tier: "glm"}
	_, err = ValidatePinRoutes(ctx, following, []VersionRequest{lrReq(agent.Model, agent.Backend)}, versions, caps)
	require.ErrorIs(t, err, ErrLunaRoutePinnedOnly)
	_, err = ValidatePinRoutes(ctx, agent, []VersionRequest{lrReq(agent.Model, agent.Backend)}, versions, nil)
	require.ErrorIs(t, err, ErrSelectionPortsMissing)
	nonLR := Profile{Alias: "c", Kind: ProfileKindChat, ConnectionRef: "or", Model: "anthropic/claude-sonnet-4.5"}
	_, err = ValidatePinRoutes(ctx, nonLR, []VersionRequest{{Model: nonLR.Model, RouteProvider: "openrouter", ConnectionRef: "or"}}, versions, nil)
	require.NoError(t, err)
	glm := fullGLM()
	glm.Backends = map[string]SupportFact{"opencode": {Support: SupportUnsupported, Evidence: "ev"}}
	_, err = ValidatePinRoutes(ctx, agent, []VersionRequest{lrReq(agent.Model, agent.Backend)}, versions, fakeLRCaps{agent.Model: glm})
	var refusal *LunaRouteRefusal
	require.True(t, errors.As(err, &refusal))
	require.ErrorIs(t, err, ErrLunaRouteIncompatible)
}

type lrRouteSource struct {
	EligibleCandidateSource
	requests []VersionRequest
}

func (s lrRouteSource) VersionRequests(context.Context, Profile) ([]VersionRequest, error) {
	return s.requests, nil
}

func TestValidateSelection_ThreadsCaps(t *testing.T) {
	EnableLunaRouteBackendsForTest(t, "opencode")
	p := Profile{Alias: "a", Kind: ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3", Backend: "opencode", Effort: EffortLow}
	src := lrRouteSource{requests: []VersionRequest{lrReq(p.Model, p.Backend)}}
	_, err := ValidateSelection(context.Background(), p, modelidentity.NewVersionResolver(), src, fakeLRCaps{})
	require.ErrorIs(t, err, ErrLunaRouteAxisUnsupported)
	p.Effort = ""
	_, err = ValidateSelection(context.Background(), p, modelidentity.NewVersionResolver(), src, fakeLRCaps{})
	require.ErrorIs(t, err, ErrLunaRouteLimitsUnknown)
	_, err = ValidateSelection(context.Background(), p, modelidentity.NewVersionResolver(), src, fakeLRCaps{p.Model: fullGLM()})
	require.NoError(t, err)
}
