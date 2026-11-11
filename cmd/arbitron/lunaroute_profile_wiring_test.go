package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteProfileSaveWiring(t *testing.T) {
	ds, call := lunaRouteMCP(t)
	isErr, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": "lr_live_one"})
	require.False(t, isErr, text)
	for _, tc := range []struct{ alias, backend, model, want string }{
		{"lr-default", "", "glm-5.3", "set backend to claude-code, opencode, codex or pi explicitly"},
		{"lr-numeric", "", "claude-sonnet-5-lr999", "reviewed fixed LunaRoute id"},
		{"lr-revoked", "", "claude-sonnet-5-lr1", "reviewed fixed LunaRoute id"},
		{"lr-opus", "", "claude-opus-lr29", "reviewed fixed LunaRoute id"},
		{"lr-haiku", "", "claude-haiku-lr37", "reviewed fixed LunaRoute id"},
		{"lr-weights", "", "deepseek-4.1-flash-1017", "reviewed fixed LunaRoute id"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			kind := "agent"
			if tc.backend == "" && tc.alias != "lr-default" {
				kind = "chat"
			}
			args := map[string]any{"alias": tc.alias, "kind": kind, "connection_ref": "lr", "model": tc.model}
			if tc.backend != "" {
				args["backend"] = tc.backend
			}
			isErr, text := call("profile_create", args)
			assert.True(t, isErr)
			assert.Contains(t, text, tc.want)
			assert.Contains(t, text, `model "`+tc.model+`"`)
			assert.NotContains(t, text, "lr_live_one")
		})
	}
	var n int
	require.NoError(t, ds.db.QueryRow("SELECT COUNT(*) FROM profiles WHERE alias LIKE 'lr-%'").Scan(&n))
	assert.Zero(t, n, "no refused LunaRoute profile persisted")
}
