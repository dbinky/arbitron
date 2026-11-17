package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteFixedIdentitySaveRefusals(t *testing.T) {
	ds, call := lunaRouteMCP(t)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	for _, backend := range []string{"", "claude-code", "opencode", "codex", "pi"} {
		for i, model := range []string{"glm-999.9", "claude-sonnet-5-lr999", "claude-sonnet-5-lr1", "deepseek-4.1-flash-1017", "lunaroute/glm-5.3", "glm-5.3-background"} {
			t.Run(backend+"/"+model, func(t *testing.T) {
				kind := "agent"
				if backend == "" {
					kind = "chat"
				}
				alias := fmt.Sprintf("refused-%s-%d", backend, i)
				bad, text := call("profile_create", map[string]any{"alias": alias, "kind": kind, "connection_ref": "lr", "backend": backend, "model": model})
				require.True(t, bad)
				if backend == "claude-code" || backend == "opencode" {
					assert.Contains(t, text, "LunaRoute on "+backend+" not yet supported")
				} else {
					assert.Contains(t, text, "reviewed fixed LunaRoute id")
				}
				assert.NotContains(t, text, lrRootKey)
				var rows int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias=?`, alias).Scan(&rows))
				assert.Zero(t, rows, "a refused selection cannot enter the pin/launch path")
			})
		}
	}
}
