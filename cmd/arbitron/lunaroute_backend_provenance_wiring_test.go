package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seed a retained run below trigger/save checks, as an older deployment can
// leave such work queued. StartInstance exercises the root's actual executor,
// pin resolver, factory and launch wrapper, not a test-constructed executor.
func runLunaRouteProvenance(t *testing.T, nodeBackend string, contextWindow provider.ContextWindow) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "launches")
	bin := filepath.Join(dir, "opencode")
	require.NoError(t, os.WriteFile(bin, []byte(fmt.Sprintf("#!/bin/sh\necho launched >> %q\nexit 1\n", log)), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	gw := newFakeLunaRouteGateway(t, "lr_provenance", "glm-5.3")
	const name = "lr-intent"
	manifest := fmt.Sprintf(`{"name":%q,"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":"lr-legacy-intent","templateFile":"templates/p.md","backend":%q}}],"edges":[]}`, name, nodeBackend)
	caps := lunaRouteCapsFile(t, "lunaroute_models:\n  glm-5.3: {}\n")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+caps+"\nllm:\n  defaultagentbackend: opencode\n", gw, func(ws string) {
		writeWorkflow(t, ws, name, manifest)
		writePinTemplate(t, ws, name)
	})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr", Provider: "lunaroute"}, "lr_provenance"))
	require.NoError(t, adaptsqlite.NewProfileRepo(ds.db).Create(ctx, provider.Profile{Alias: "lr-legacy-intent", Kind: provider.ProfileKindAgent, ConnectionRef: "lr", Model: "glm-5.3", ContextWindow: contextWindow}))
	require.NoError(t, ds.profileResolver.(*providersadapter.DynamicProfileResolver).Reload(ctx))
	// A reveal attempt would hit corrupt ciphertext. Policy must win first.
	_, err := ds.db.ExecContext(ctx, `UPDATE connections SET api_key_cipher=X'00' WHERE name='lr'`)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal([]byte(manifest), &snapshot))
	id := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{ID: id, OwnerID: localDefaultUser, WorkflowName: name, State: instance.StateQueued, Priority: instance.PriorityNormal, WorkflowSnapshot: snapshot, Variables: map[string]any{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ModelPins: map[string]string{"lr-legacy-intent": "glm-5.3"}}))
	require.NoError(t, ds.eng.StartInstance(context.Background(), id))
	require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
	msg := nodeError(t, ds, id)
	assert.NotContains(t, msg, "decrypt")
	assert.NotContains(t, msg, "lr_provenance")
	assert.NoFileExists(t, log, "refusal precedes any subprocess including version checks")
	assert.Empty(t, gw.Requests(), "local refusals never diagnose online")
	return msg
}
func TestLunaRouteLegacyDefaultBackendRefused(t *testing.T) {
	assert.Contains(t, runLunaRouteProvenance(t, "", ""), provider.ErrLunaRouteBackendRequired.Error())
}
func TestLunaRouteExplicitNodeOverrideChecked(t *testing.T) {
	for _, tc := range []struct {
		context provider.ContextWindow
		want    error
	}{
		{"", provider.ErrLunaRouteLimitsUnknown},
		{provider.Context1M, provider.ErrLunaRouteAxisUnsupported},
	} {
		t.Run(string(tc.context), func(t *testing.T) {
			// Explicit intent reaches a stable typed conflict before corrupt
			// ciphertext can be revealed or a binary can launch.
			msg := runLunaRouteProvenance(t, "opencode", tc.context)
			assert.Contains(t, msg, tc.want.Error())
			assert.NotContains(t, msg, provider.ErrLunaRouteBackendRequired.Error())
		})
	}
}
