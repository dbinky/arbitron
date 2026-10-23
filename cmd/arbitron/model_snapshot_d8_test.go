package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sink in this task is the trigger's durable snapshot, not native execution
// (P1T4). Unreachable model nodes still require real workflow-scoped validation.
const d8CaptureWorkflow = `{"name":"capture","nodes":[
 {"id":"start","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"captured","operation":"set","value":true}]}},
 {"id":"azure","type":"llm_call","config":{"model_profile":"azure"}},
 {"id":"bedrock","type":"interactive_llm","config":{"model_profile":"bedrock"}}
 ],"edges":[]}`

func installD8CaptureProfiles(t *testing.T, ds *domainStack) context.Context {
	t.Helper()
	ctx := adminCtxForLocalUser(t, ds)
	for _, route := range []string{"azure", "bedrock"} {
		require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: route, Provider: route, BaseURL: "https://native.example", Extras: map[string]string{"api-version": "2024-10-21", "region": "us-east-1", "access-key-id": "access"}}, "secret"))
		_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: route, Kind: provider.ProfileKindChat, ConnectionRef: route, Model: "Configured/" + route + ":ID"})
		require.NoError(t, err)
	}
	return ctx
}

func TestD8SnapshotRoutesRoundTrip(t *testing.T) {
	ds, closeStack := bootSinkRootWithClose(t, "", func(dir string) { writeWorkflow(t, dir, "capture", d8CaptureWorkflow) })
	ctx := installD8CaptureProfiles(t, ds)
	id, _, err := ds.eng.Trigger(ctx, "capture", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	wantModels := map[string]string{"azure": "Configured/azure:ID", "bedrock": "Configured/bedrock:ID"}
	wantRoutes := map[string]string{"azure": "azure", "bedrock": "bedrock"}
	saved, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
	require.Equal(t, wantModels, saved.ModelPins)
	require.Equal(t, wantRoutes, saved.ModelPinRoutes)
	assert.Equal(t, true, saved.Variables["captured"])
	// Live edits and returned-map mutation cannot rewrite an existing capture.
	saved.ModelPins["azure"], saved.ModelPinRoutes["azure"] = "mutated", "bedrock"
	modelY := "New/Deployment:ID"
	_, _, err = ds.profileSvc.UpdatePatch(ctx, "azure", provider.ProfilePatch{Model: &modelY})
	require.NoError(t, err)
	freshID, _, err := ds.eng.Trigger(ctx, "capture", nil, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, freshID).State)
	// Retry uses the persisted snapshot, not today's profile.
	require.NoError(t, ds.instanceRepo.UpdateState(ctx, id, instance.StateFailed))
	require.NoError(t, ds.eng.Retry(ctx, id, nil))
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	dbPath := ds.cfg.Database.Path
	closeStack()
	db, err := sqlite.Open(dbPath)
	require.NoError(t, err)
	defer db.Close()
	repo := sqlite.NewInstanceRepo(db)
	old, err := repo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, old.ModelPinsVersion)
	assert.Equal(t, wantModels, old.ModelPins)
	assert.Equal(t, wantRoutes, old.ModelPinRoutes)
	fresh, err := repo.Get(ctx, freshID)
	require.NoError(t, err)
	assert.Equal(t, modelY, fresh.ModelPins["azure"])
	assert.Equal(t, wantRoutes, fresh.ModelPinRoutes)
}

func TestSnapshotUsesOnlyCapturedWorkflow(t *testing.T) {
	var workspace string
	ds := bootSinkRoot(t, "", func(dir string) { workspace = dir; writeWorkflow(t, dir, "capture", d8CaptureWorkflow) })
	ctx := installD8CaptureProfiles(t, ds)
	// This unrelated saved manifest is invalid for the alias. Global profile
	// validation must see it, but trigger capture must not.
	incompatible := `{"name":"unrelated","nodes":[{"id":"wrong","type":"agent_call","entrypoint":true,"config":{"model_profile":"azure","backend":"codex"}}],"edges":[]}`
	writeWorkflow(t, workspace, "unrelated", incompatible)
	id, _, err := ds.eng.Trigger(ctx, "capture", nil, "normal")
	require.NoError(t, err)
	saved := waitTerminal(t, ds, id)
	assert.Equal(t, map[string]string{"azure": "azure", "bedrock": "bedrock"}, saved.ModelPinRoutes)
	assert.Equal(t, "Configured/azure:ID", saved.ModelPins["azure"])
	assert.Equal(t, true, saved.Variables["captured"])
	// The incompatible route in the captured workflow still rejects the trigger.
	writeWorkflow(t, workspace, "capture", incompatible)
	before := countInstances(t, ds)
	invalid, started, err := ds.eng.Trigger(ctx, "capture", nil, "normal")
	require.Error(t, err)
	assert.Equal(t, uuid.Nil, invalid)
	assert.False(t, started)
	assert.Equal(t, before, countInstances(t, ds))
}

func TestD8SnapshotInvalidCaptureCreatesNothing(t *testing.T) {
	for _, fault := range []string{"following", "missing model", "missing alias", "connection read", "write"} {
		t.Run(fault, func(t *testing.T) {
			ds := bootSinkRoot(t, "", func(dir string) { writeWorkflow(t, dir, "capture", d8CaptureWorkflow) })
			ctx := installD8CaptureProfiles(t, ds)
			switch fault {
			case "following":
				storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "azure", Kind: provider.ProfileKindChat, ConnectionRef: "azure", Model: "Configured/azure:ID", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openai", Tier: "sol"}})
			case "missing model":
				storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "azure", Kind: provider.ProfileKindChat, ConnectionRef: "azure"})
			case "missing alias":
				require.NoError(t, sqlite.NewProfileRepo(ds.db).Delete(ctx, "azure"))
				require.NoError(t, ds.profileResolver.(interface{ Reload(context.Context) error }).Reload(ctx))
			case "connection read":
				_, err := ds.db.Exec(`ALTER TABLE connections RENAME TO unavailable_connections`)
				require.NoError(t, err)
			case "write":
				_, err := ds.db.Exec(`CREATE TRIGGER reject_capture BEFORE INSERT ON instances BEGIN SELECT RAISE(ABORT, 'D8 write fault'); END`)
				require.NoError(t, err)
			}
			before := countInstances(t, ds)
			id, started, err := ds.eng.Trigger(ctx, "capture", nil, "normal")
			require.Error(t, err)
			if fault == "write" {
				assert.ErrorContains(t, err, "D8 write fault")
			}
			assert.Equal(t, uuid.Nil, id)
			assert.False(t, started)
			assert.Equal(t, before, countInstances(t, ds))
		})
	}
}
