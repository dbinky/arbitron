package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type foreignModelGateway struct{ calls int }

func (g *foreignModelGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	g.calls++
	body := `{"model":"gpt-foreign","choices":[{"message":{"content":"pong"}}]}`
	ct := "application/json"
	if strings.Contains(readAll(r), `"stream":true`) {
		body = "data: {\"model\":\"gpt-foreign\",\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n"
		ct = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func readAll(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// A legacy (version 0) run resolves through the real PinningResolver to an
// unpinned binding whose chat request carries no executed-model requirement.
// The LunaRoute chat provider must still refuse a foreign reported identity,
// for both the blocking and streaming calls.
func TestLunaRouteChatIdentityLegacyRunWiring(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr-chat", Provider: "lunaroute"}, "lr_live_chat"))
	require.NoError(t, adaptsqlite.NewProfileRepo(ds.db).Create(ctx, provider.Profile{Alias: "lr-chat", Kind: provider.ProfileKindChat, ConnectionRef: "lr-chat", Model: "glm-5.3"}))
	resolver, ok := ds.profileResolver.(*providersadapter.DynamicProfileResolver)
	require.True(t, ok)
	require.NoError(t, resolver.Reload(ctx))

	id := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: id, OwnerID: localDefaultUser, WorkflowName: "legacy", State: instance.StateCompleted, Priority: instance.PriorityNormal,
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ModelPins:        map[string]string{"lr-chat": "glm-5.3"},
	}))
	// New runs are always snapshots; downgrade the stored row to a run that
	// predates pins, as the interactive wiring tests do.
	_, err := ds.db.ExecContext(ctx, `UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, id.String())
	require.NoError(t, err)
	pins := provider.NewPinningResolver(ds.profileResolver, ds.pinLookup, engine.InstanceIDFromContext).ForRun()
	binding, err := pins.Resolve(engine.ContextWithInstanceID(ctx, id), "lr-chat")
	require.NoError(t, err)
	require.False(t, binding.Pinned, "version-0 runs resolve live")
	require.False(t, binding.RequiresModelEvidence())

	gw := &foreignModelGateway{}
	caps := provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{
		"glm-5.3": {ID: "glm-5.3", OutputTokens: provider.LimitFact{Tokens: 1000, Evidence: "ev#out"}},
	})
	factory := providersadapter.NewFactory(providersadapter.Deps{HTTPClient: &http.Client{Transport: gw}, CapabilityRules: caps})
	chat, err := factory.ChatProviderFor(binding)
	require.NoError(t, err)
	req := engine.ChatRequest{Prompt: "ping", MaxTokens: 1000, RequireExecutedModel: binding.RequiresModelEvidence()}

	resp, err := chat.Chat(context.Background(), req)
	require.ErrorContains(t, err, "execution evidence")
	assert.Nil(t, resp)

	ch, err := chat.StreamChat(context.Background(), req)
	require.NoError(t, err)
	var streamErr error
	var tokens string
	for c := range ch {
		tokens += c.Token
		if c.Error != nil {
			streamErr = c.Error
		}
	}
	require.ErrorContains(t, streamErr, "execution evidence")
	assert.Empty(t, tokens)
	assert.Equal(t, 2, gw.calls, "the foreign answers were actually requested and refused")
}
