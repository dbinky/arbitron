package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootPinStack boots the REAL composition root with one workflow whose only
// alias-bearing node (an llm_call that no edge reaches, so no provider call is
// ever made) names the "pin-alias" profile.
func bootPinStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))
	writeWorkflow(t, workspaceDir, "pin-wf", `{
  "name": "pin-wf",
  "description": "pins",
  "inputs": [],
  "nodes": [
    {"id": "t1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}},
    {"id": "orphan", "type": "llm_call", "config": {"model_profile": "pin-alias"}}
  ],
  "edges": []
}`)
	writeWorkflow(t, workspaceDir, "no-models", `{
  "name": "no-models", "nodes": [
    {"id": "t1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "started", "operation": "set", "value": true}]}}
  ], "edges": []
}`)
	configPath := filepath.Join(root, "config.yaml")
	cfg := "server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds
}

// TestModelPinsTriggerRootWiring drives the real initDomain: a trigger captures
// the live profile's model through the wired ModelSnapshotter, persists it with
// version 1 into the migrated SQLite row, and a later edit of the live profile
// does not change that run's persisted pin. A mutable alias fails the trigger
// before any instance row exists. Dropping eng.SetModelSnapshotter from main.go
// makes the first assertion read version 0 and fail.
func TestModelPinsTriggerRootWiring(t *testing.T) {
	ds := bootPinStack(t)
	ctx := adminCtxForLocalUser(t, ds)

	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-wf", map[string]any{}, "normal")
	require.NoError(t, err)
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion, "the root must wire the snapshotter, or every run is legacy")
	assert.Equal(t, map[string]string{"pin-alias": "claude-opus-4-1"}, inst.ModelPins)
	var routes string
	require.NoError(t, ds.db.QueryRow(`SELECT model_pin_routes FROM instances WHERE id = ?`, id.String()).Scan(&routes))
	assert.JSONEq(t, `{}`, routes, "the real trigger must persist strict pin routes atomically")
	assert.Empty(t, inst.ModelPinRoutes)

	// Live edit to Y: the already-created run keeps X in SQLite; a fresh run pins Y.
	_, _, err = ds.profileSvc.Update(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "claude-opus-4-5"})
	require.NoError(t, err)
	old, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-1", old.ModelPins["pin-alias"])
	id2, _, err := ds.eng.Trigger(ctx, "pin-wf", map[string]any{}, "normal")
	require.NoError(t, err)
	fresh, err := ds.instanceRepo.Get(ctx, id2)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-5", fresh.ModelPins["pin-alias"])

	// A mutable alias has no fixed version: the trigger fails and creates no row.
	storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "sonnet"})
	before := countInstances(t, ds)
	_, _, err = ds.eng.Trigger(ctx, "pin-wf", map[string]any{}, "normal")
	require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	assert.Equal(t, before, countInstances(t, ds), "a failed pin must leave no instance row")
}

// Invalid snapshotter replacement is refused immediately at the real root,
// including for a workflow that has no models to pin. The original capture
// dependency must remain usable after the refused replacement.
func TestModelPinsNoSnapshotterWiring(t *testing.T) {
	for _, hasAlias := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-alias", true: "alias"}[hasAlias], func(t *testing.T) {
			ds := bootPinStack(t)
			ctx := adminCtxForLocalUser(t, ds)
			name := "pin-wf"
			if hasAlias {
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
				require.NoError(t, err)
			} else {
				name = "no-models"
				id, _, err := ds.eng.Trigger(ctx, name, nil, "normal")
				require.NoError(t, err)
				var pins, routes string
				var version int
				require.NoError(t, ds.db.QueryRow(`SELECT model_pins, model_pins_version, model_pin_routes FROM instances WHERE id = ?`, id.String()).Scan(&pins, &version, &routes))
				assert.JSONEq(t, `{}`, pins)
				assert.JSONEq(t, `{}`, routes)
				assert.Equal(t, instance.ModelPinsVersionSnapshot, version)
			}

			before := countInstances(t, ds)
			for _, snapshotter := range []provider.ModelSnapshotter{nil, (*providersadapter.ModelSnapshotter)(nil)} {
				require.PanicsWithValue(t, "engine: SetModelSnapshotter requires a non-nil snapshotter", func() {
					ds.eng.SetModelSnapshotter(snapshotter)
				})
			}
			assert.Equal(t, before, countInstances(t, ds), "a refused dependency must create nothing")
			id, _, err := ds.eng.Trigger(ctx, name, nil, "normal")
			require.NoError(t, err)
			saved, err := ds.instanceRepo.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
			require.NotNil(t, saved.ModelPins)
			if hasAlias {
				assert.Equal(t, map[string]string{"pin-alias": "claude-opus-4-1"}, saved.ModelPins)
			} else {
				assert.Empty(t, saved.ModelPins)
			}
		})
	}
}

func countInstances(t *testing.T, ds *domainStack) int {
	t.Helper()
	var n int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&n))
	return n
}

// TestModelPinsTriggerWaitsOnSharedGate holds the stack's exclusive mutation
// lease (an admin edit or advancement commit in flight) and shows a real trigger
// queues behind it, then persists a version-1 pin. Dropping eng.SetModelPinGate
// from main.go lets the trigger finish while the lease is held.
func TestModelPinsTriggerWaitsOnSharedGate(t *testing.T) {
	t.Parallel()
	ds := bootPinStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)

	held, release, mutDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(mutDone)
		_ = ds.executionGate.WithMutation(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	type result struct {
		id  uuid.UUID
		err error
	}
	got := make(chan result, 1)
	go func() {
		id, _, err := ds.eng.Trigger(ctx, "pin-wf", map[string]any{}, "normal")
		got <- result{id, err}
	}()
	select {
	case <-got:
		close(release)
		t.Fatal("trigger completed while the exclusive gate lease was held")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	<-mutDone
	r := <-got
	require.NoError(t, r.err)
	inst, err := ds.instanceRepo.Get(ctx, r.id)
	require.NoError(t, err)
	require.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
}
