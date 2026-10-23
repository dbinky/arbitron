package probe

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsabilityNativeUnknownTier(t *testing.T) {
	const model = "vendor/native-preview-20261018"
	entry := modelidentity.Entry{CanonicalID: "openrouter/" + model, Provider: "openrouter", NativeID: model}
	path := usabilityPath()
	path.RouteProvider, path.Binding.Provider = "openrouter", "openrouter"
	for _, actual := range []string{model, "fallback", ""} {
		t.Run("returned="+actual, func(t *testing.T) {
			lane, call := chatLaneFixture(t, func(b provider.ResolvedBinding) string {
				require.Equal(t, model, b.Model)
				return actual
			})
			ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), path, entry)
			require.NoError(t, err)
			assert.Equal(t, actual == model, ev.Confirmed())
			assert.Equal(t, model, call.req.Model)
			assert.True(t, call.req.RequireExecutedModel)
			if actual != model {
				assert.Empty(t, ev.ProofRef)
			}
		})
	}
}

// D8 is enforced before dispatch, including when a lane or a persisted pinned
// exception could otherwise produce a plausible exact-model success.
func TestUsabilityD8RejectsConfirmation(t *testing.T) {
	for _, route := range []string{"azure", "azure-openai", "bedrock", "amazon-bedrock"} {
		for _, backend := range []string{"", "claude-code", "pi", "opencode"} {
			t.Run(route+"/"+backend, func(t *testing.T) {
				p := usabilityPath()
				p.RouteProvider, p.Binding.Provider, p.Backend = route, route, backend
				p.Binding.Pinned, p.Binding.OpaquePinRoute = true, provider.Canonicalize(route)
				if backend != "" {
					p.Kind = string(provider.ProfileKindAgent)
				}
				called := false
				lane := fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
					called = true
					return LaneResult{NativeModel: candidateSpelling(usabilityEntry()), ProofRef: "request-echo"}, nil
				}}
				ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), p, usabilityEntry())
				assert.ErrorIs(t, err, provider.ErrInvalidSelection)
				assert.False(t, ev.Confirmed())
				assert.False(t, called, "native opaque routes must not reach an exact-evidence lane")
			})
		}
	}
}
