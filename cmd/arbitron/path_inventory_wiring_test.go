package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPathInventoryRootWiring drives the real initDomain: the stack's inventory
// reads a workflow written straight to disk (never registered), the shared
// ExecutionGate rechecks that inventory's revision, a credential rotated through
// the production admin service makes an earlier revision stale, and an
// unreadable manifest makes the revision unavailable rather than empty.
func TestPathInventoryRootWiring(t *testing.T) {
	ds, workspace := bootInventoryStack(t)
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ctx := context.Background()

	require.NoError(t, ds.connSvc.Create(admin, provider.Connection{Name: "inv-conn", Provider: "openrouter", BaseURL: "https://openrouter.example/v1"}, "sk-one"))
	storeProfileUnchecked(t, admin, ds, provider.Profile{Alias: "inv-agent", Kind: provider.ProfileKindAgent, ConnectionRef: "inv-conn", Backend: workflow.BackendOpencode, Model: "vendor/m-1"})

	wfDir := filepath.Join(workspace, "shared", "disk-only")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	manifest, err := json.Marshal(workflow.Workflow{Name: "disk-only", Nodes: []workflow.Node{{
		ID: "n", Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: "inv-agent", Backend: workflow.BackendPi},
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), manifest, 0o644))

	snap, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	var backends []string
	for _, p := range snap.Paths {
		for _, a := range p.Aliases {
			if a == "inv-agent" {
				backends = append(backends, p.Backend)
				assert.Equal(t, "inv-conn", p.ConnectionRef)
			}
		}
	}
	assert.ElementsMatch(t, []string{workflow.BackendOpencode, workflow.BackendPi}, backends)
	assert.Contains(t, snap.ExecutionRevision.WorkflowHashes, "shared/disk-only")
	assert.Equal(t, snap.Revision, snap.ExecutionRevision.ID)

	ran := false
	require.NoError(t, ds.executionGate.WithCurrent(ctx, snap.Revision, func(context.Context) error { ran = true; return nil }))
	assert.True(t, ran, "the gate's revision source is the stack's inventory")

	rotated := "sk-two"
	require.NoError(t, ds.connSvc.Update(admin, provider.Connection{Name: "inv-conn", Provider: "openrouter", BaseURL: "https://openrouter.example/v1"}, &rotated))
	ran = false
	err = ds.executionGate.WithCurrent(ctx, snap.Revision, func(context.Context) error { ran = true; return nil })
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	assert.False(t, ran, "evidence gathered before the credential rotation must not commit")

	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), []byte("{truncated"), 0o644))
	fresh, err := ds.pathInventory.Revision(ctx)
	assert.Error(t, err)
	assert.Empty(t, fresh)
	err = ds.executionGate.WithCurrent(ctx, snap.Revision, func(context.Context) error { ran = true; return nil })
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
	assert.False(t, ran)
}

// TestPathInventoryActiveRunRootWiring: the stack's inventory reads active runs
// from the real InstanceRepo. A live-resolving (legacy NULL-pin) failed run, so
// still retryable, whose snapshot overrides a backend no workflow on disk names
// contributes that route and moves the gate's revision. A pinned run's override
// never executes the advanced model and contributes nothing.
func TestPathInventoryActiveRunRootWiring(t *testing.T) {
	ds, _ := bootInventoryStack(t)
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ctx := context.Background()
	_, _, err := ds.profileSvc.Create(admin, provider.Profile{Alias: "run-agent", Kind: provider.ProfileKindAgent, Backend: workflow.BackendClaudeCode, Model: "claude-opus-5-5"})
	require.NoError(t, err)
	before, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode}, inventoryBackends(before, "run-agent"))

	run := func(id uuid.UUID, state instance.State, pins map[string]string, version int, backend string) *instance.Instance {
		return &instance.Instance{
			ID: id, OwnerID: localDefaultUser, WorkflowName: "since-deleted",
			State: state, Priority: instance.PriorityNormal, Variables: map[string]any{},
			ModelPins: pins, ModelPinsVersion: version,
			WorkflowSnapshot: map[string]any{"name": "since-deleted", "nodes": []any{map[string]any{
				"id": "n", "type": string(workflow.NodeTypeAgentCall),
				"config": map[string]any{"model_profile": "run-agent", "backend": backend},
			}}},
		}
	}
	require.NoError(t, ds.instanceRepo.Create(ctx, run(uuid.New(), instance.StateRunning, map[string]string{"run-agent": "claude-opus-5-5"}, instance.ModelPinsVersionSnapshot, workflow.BackendOpencode)))
	pinnedOnly, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, before.Revision, pinnedOnly.Revision, "a pinned run never executes the advanced model")

	// Emulate a historical NULL/0 row; ordinary Create cannot make legacy runs.
	legacyID := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, run(legacyID, instance.StateFailed, map[string]string{}, instance.ModelPinsVersionSnapshot, workflow.BackendCodex)))
	_, err = ds.db.Exec(`UPDATE instances SET model_pins = NULL, model_pins_version = 0 WHERE id = ?`, legacyID.String())
	require.NoError(t, err)
	after, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendCodex}, inventoryBackends(after, "run-agent"))

	ran := false
	err = ds.executionGate.WithCurrent(ctx, before.Revision, func(context.Context) error { ran = true; return nil })
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	assert.False(t, ran, "evidence gathered before the legacy run's route appeared must not commit")
	require.NoError(t, ds.executionGate.WithCurrent(ctx, after.Revision, func(context.Context) error { ran = true; return nil }))
	assert.True(t, ran)
}

func bootInventoryStack(t *testing.T, backend ...string) (*domainStack, string) {
	t.Helper()
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x6e}, 32)))
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	path := filepath.Join(root, "config.yaml")
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + workspace + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + "\n"
	if len(backend) > 0 {
		contents += "profiles: []\nllm:\n  defaultagentbackend: " + backend[0] + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	ctx := context.Background()
	ds, err := initDomain(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	require.NotNil(t, ds.pathInventory)
	return ds, workspace
}

func inventoryBackends(snap modelcatalog.PathSnapshot, alias string) []string {
	var out []string
	for _, p := range snap.Paths {
		for _, a := range p.Aliases {
			if a == alias {
				out = append(out, p.Backend)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestPathInventoryHarnessRootWiring: the stack's inventory reads the real
// harness account files, so switching the codex account after evidence was
// gathered makes the gate reject that evidence, while the route key is stable.
func TestPathInventoryHarnessRootWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	auth := filepath.Join(home, ".codex", "auth.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(auth), 0o755))
	require.NoError(t, os.WriteFile(auth, []byte(`{"account":"one"}`), 0o600))
	ds, _ := bootInventoryStack(t)
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ctx := context.Background()
	storeProfileUnchecked(t, admin, ds, provider.Profile{Alias: "cx-agent", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: "gpt-x"})

	before, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(auth, []byte(`{"account":"two"}`), 0o600))
	after, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)

	find := func(s modelcatalog.PathSnapshot) modelcatalog.ExecutionPath {
		for _, p := range s.Paths {
			if p.Backend == workflow.BackendCodex {
				return p
			}
		}
		t.Fatal("no codex path in inventory")
		return modelcatalog.ExecutionPath{}
	}
	assert.Equal(t, find(before).Key, find(after).Key)
	assert.NotEqual(t, find(before).ConfigRevision, find(after).ConfigRevision)
	ran := false
	err = ds.executionGate.WithCurrent(ctx, before.Revision, func(context.Context) error { ran = true; return nil })
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
	assert.False(t, ran, "evidence gathered under the earlier account must not commit")
}
