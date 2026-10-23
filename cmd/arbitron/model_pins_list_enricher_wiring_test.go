package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The queue row describes what a run executes. After its profile moves to Y,
// a run pinned to X at trigger must still list X, while a fresh run lists Y
// (G23). The row is read through Engine.ListEnriched on the real root, so the
// pins must reach the enricher from InstanceRepo.List, not from a test fake.
func TestModelPinsWiringListEnricherShowsPinnedModel(t *testing.T) {
	const pinned, advanced = "claude-sonnet-4-5", "claude-opus-4-5"
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '1.3.0'; exit 0; fi\n" +
		"cat > /dev/null\n" +
		"echo 'pi refused the mission' >&2\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ds := bootSinkRoot(t, "", func(ws string) {
		writeWorkflow(t, ws, "pin-list", `{"name":"pin-list","inputs":[],"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"pi-agent","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, ws, "pin-list")
	})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "pi-anthropic", Provider: provider.CanonicalAnthropicAPI, BaseURL: "https://api.anthropic.com"}, "pi-list-secret"))
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pi-agent", Kind: provider.ProfileKindAgent, Backend: "pi", ConnectionRef: "pi-anthropic", Model: pinned})
	require.NoError(t, err)

	failRun := func() instance.Instance {
		t.Helper()
		id, _, err := ds.eng.Trigger(ctx, "pin-list", map[string]any{}, "normal")
		require.NoError(t, err)
		var inst *instance.Instance
		require.Eventually(t, func() bool {
			inst, err = ds.instanceRepo.Get(ctx, id)
			return err == nil && inst.State.IsTerminal()
		}, 10*time.Second, 50*time.Millisecond)
		require.Equal(t, instance.StateFailed, inst.State, "the failed row keeps a binding for its failed node")
		return *inst
	}
	before := failRun()
	require.Equal(t, map[string]string{"pi-agent": pinned}, before.ModelPins)

	next := advanced
	_, _, err = ds.profileSvc.UpdatePatch(ctx, "pi-agent", provider.ProfilePatch{Model: &next})
	require.NoError(t, err)
	after := failRun()
	require.Equal(t, map[string]string{"pi-agent": advanced}, after.ModelPins)

	rows, err := ds.eng.ListEnriched(ctx, instance.ListFilters{Limit: 50})
	require.NoError(t, err)
	models := map[string]string{}
	for _, r := range rows {
		models[r.ID.String()] = r.Model
		if r.ID == before.ID || r.ID == after.ID {
			assert.Equal(t, "agent_call", r.NodeType)
			assert.Equal(t, "pi", r.Backend, "backend still resolves live")
			assert.Equal(t, provider.CanonicalAnthropicAPI, r.Provider, "provider still resolves live")
		}
	}
	assert.Equal(t, pinned, models[before.ID.String()], "a run pinned before the edit lists its pinned model")
	assert.Equal(t, advanced, models[after.ID.String()], "a run triggered after the edit lists the new model")
}
