package main

import (
	"os"
	"path/filepath"
	"testing"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Saving a profile consumed by an interactive_agent must reach the root's
// shared interactive selector and refuse before accepting or persisting it.
func TestLunaRouteInteractiveAgentRefusedAtRoot(t *testing.T) {
	gw := newFakeLunaRouteGateway(t, "lr_live_int", "glm-5.3")
	ds := bootLunaRouteRoot(t, "", gw, func(ws string) {
		writeWorkflow(t, ws, "lr-int", `{"name":"lr-int","nodes":[
  {"id":"talk","type":"interactive_agent","entrypoint":true,
   "config":{"model_profile":"lr-int","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, ws, "lr-int")
		require.NoError(t, os.WriteFile(filepath.Join(ws, "users", localDefaultUser.String(), "lr-int", "templates", "o.md"), []byte("orchestrate"), 0o600))
	})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_live_int"))
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "lr-int", Kind: provider.ProfileKindAgent, Backend: "codex", ConnectionRef: "lr", Model: "glm-5.3"})
	assert.ErrorIs(t, err, providersadapter.ErrUnsupportedAgentBackend)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "LunaRoute agent profiles have no interactive streaming path")
	}
	var saved int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='lr-int'`).Scan(&saved))
	assert.Zero(t, saved, "refused profile must not be persisted")
	assert.Empty(t, gw.Requests(), "refusal must happen before network activity")
}
