package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathInventoryDefaultHarnessRootWiring(t *testing.T) {
	ds, _ := bootInventoryStack(t, workflow.BackendCodex)
	snap, err := ds.pathInventory.Snapshot(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Paths, 1, "real config must feed a no-profile subscription path")
	assert.Equal(t, workflow.BackendCodex, snap.Paths[0].Backend)
	assert.Equal(t, "batch", snap.Paths[0].Mode)
	binding, err := ds.pathInventory.WithBinding(context.Background(), snap.Paths[0], "gpt-5-sol")
	require.NoError(t, err)
	assert.Equal(t, "gpt-5-sol", binding.Model)
	assert.Equal(t, workflow.BackendCodex, binding.Backend)
}

func TestPathInventoryModesAndVersionRequestsRootWiring(t *testing.T) {
	t.Setenv(codexInteractiveVerifiedEnv, "false")
	ds, workspace := bootInventoryStack(t, workflow.BackendCodex)
	ctx := context.Background()
	admin := identity.WithIdentity(ctx, identity.Identity{Role: identity.RoleAdmin})
	saved, _, err := ds.profileSvc.Create(admin, provider.Profile{Alias: "coder", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol"})
	require.NoError(t, err)
	before, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	// Neither this owner nor this workflow is registered with a public catalog.
	dir := filepath.Join(workspace, "users", uuid.NewString(), "private-only")
	require.NoError(t, os.MkdirAll(dir, 0755))
	raw, err := json.Marshal(workflow.Workflow{Name: "private-only", Nodes: []workflow.Node{{
		ID: "interactive", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "coder", Backend: workflow.BackendPi},
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), raw, 0644))
	after, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	modes := map[string]bool{}
	for _, path := range after.Paths {
		for _, alias := range path.Aliases {
			if alias != "coder" {
				continue
			}
			modes[path.Mode] = true
			_, err := ds.pathInventory.WithBinding(ctx, path, "gpt-5-sol")
			if path.Mode == "interactive" {
				require.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)
			} else {
				require.NoError(t, err)
			}
		}
	}
	assert.Equal(t, map[string]bool{"batch": true, "interactive": true}, modes)
	// Drive the shared root gate through the durable sink, not just a callback flag.
	replacement := saved
	replacement.Model = "gpt-5-astra"
	repo := sqlite.NewProfileRepo(ds.db)
	err = ds.executionGate.WithCurrent(ctx, before.Revision, func(ctx context.Context) error {
		return repo.Update(ctx, replacement)
	})
	require.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	durable, err := repo.Get(ctx, saved.Alias)
	require.NoError(t, err)
	assert.Equal(t, "gpt-5-sol", durable.Model)
	// Pins/selection must reject an unsupported route even if batch is supported.
	requests, err := ds.pathInventory.VersionRequests(ctx, saved)
	require.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)
	assert.Empty(t, requests)
}

func TestPathInventoryInteractiveOptInUsesRootSelector(t *testing.T) {
	t.Setenv(codexInteractiveVerifiedEnv, "true")
	ds, workspace := bootInventoryStack(t, workflow.BackendCodex)
	ctx := context.Background()
	admin := identity.WithIdentity(ctx, identity.Identity{Role: identity.RoleAdmin})
	p, _, err := ds.profileSvc.Create(admin, provider.Profile{Alias: "enabled-codex", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol"})
	require.NoError(t, err)
	dir := filepath.Join(workspace, "shared", "interactive")
	require.NoError(t, os.MkdirAll(dir, 0755))
	raw, err := json.Marshal(workflow.Workflow{Name: "interactive", Nodes: []workflow.Node{{
		ID: "i", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: p.Alias},
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), raw, 0644))
	requests, err := ds.pathInventory.VersionRequests(ctx, p)
	require.NoError(t, err, "the root opt-in map must reach the inventory, not a hardcoded backend denylist")
	assert.Equal(t, []modelidentity.VersionRequest{{Model: p.Model, Backend: workflow.BackendCodex}}, requests)
	snap, err := ds.pathInventory.PathsForProfile(ctx, p)
	require.NoError(t, err)
	require.Len(t, snap.Paths, 2)
	for _, path := range snap.Paths {
		binding, err := ds.pathInventory.WithBinding(ctx, path, p.Model)
		require.NoError(t, err)
		assert.Equal(t, p.Model, binding.Model)
		assert.Equal(t, workflow.BackendCodex, binding.Backend)
	}
}

func TestPathInventoryVersionRequestsRootPolicyOutage(t *testing.T) {
	ds, _ := bootInventoryStack(t, workflow.BackendCodex)
	ctx := context.Background()
	p := provider.Profile{Alias: "coherent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol"}
	// The supplied coherent profile is the producer, not a second live-cache read.
	requests, err := ds.pathInventory.VersionRequests(ctx, p)
	require.NoError(t, err)
	require.Equal(t, []modelidentity.VersionRequest{{Model: p.Model, Backend: workflow.BackendCodex}}, requests)
	fixed, err := modelidentity.NewVersionResolver().SpecificVersion(ctx, requests[0])
	require.NoError(t, err)
	assert.Equal(t, p.Model, fixed)
	_, err = ds.db.Exec(`DROP TABLE model_identity_policy`)
	require.NoError(t, err)
	requests, err = ds.pathInventory.VersionRequests(ctx, p)
	require.NoError(t, err, "fixed-version routing cannot depend on tier policy")
	require.Len(t, requests, 1)
	fixed, err = modelidentity.NewVersionResolver().SpecificVersion(ctx, requests[0])
	require.NoError(t, err)
	assert.Equal(t, p.Model, fixed)
	_, err = ds.pathInventory.Snapshot(ctx)
	require.Error(t, err, "following eligibility still needs policy")
}
