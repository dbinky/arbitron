package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inventorySelectors() (*adapterprovider.Factory, *adapterprovider.InteractiveStreamSelector) {
	factory := adapterprovider.NewFactory(adapterprovider.Deps{
		ClaudeChatConfig:  claudecli.Config{BinaryPath: "claude"},
		ClaudeAgentConfig: claudecli.Config{BinaryPath: "claude"},
	})
	stream := claudecli.NewStreamAgentAdapter(claudecli.Config{BinaryPath: "claude"})
	return factory, adapterprovider.NewInteractiveStreamSelector(factory, map[string]adapterprovider.AgentStreamFactory{workflow.BackendClaudeCode: func(provider.ResolvedBinding) engine.ChatProvider { return stream }}, workflow.BackendClaudeCode)
}

func TestInventoryDefaultHarnessWithoutProfiles(t *testing.T) {
	f := newInvFixture()
	f.backend = workflow.BackendCodex
	inv := f.build(t)
	snap, err := inv.Snapshot(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Paths, 1, "deployment subscription is a path even on an empty installation")
	path := snap.Paths[0]
	assert.Equal(t, "batch", path.Mode)
	assert.Equal(t, workflow.BackendCodex, path.Backend)
	assert.Equal(t, string(provider.ProfileKindAgent), path.Kind)
	assert.Empty(t, path.Aliases)
	binding, err := inv.WithBinding(context.Background(), path, "gpt-5-sol")
	require.NoError(t, err)
	assert.Equal(t, "gpt-5-sol", binding.Model)
	assert.Equal(t, workflow.BackendCodex, binding.Backend)
	key, err := binding.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Empty(t, key)
	f.harness.state[workflow.BackendCodex] = "changed account"
	changed, err := inv.Snapshot(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, path.ConfigRevision, changed.Paths[0].ConfigRevision)
}

func inventoryWorkflow(nodes ...workflow.Node) *invWorkflows {
	return &invWorkflows{scopes: []workflow.Scope{workflow.Shared()}, byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{
		workflow.Shared(): {{Scope: workflow.Shared(), Name: "routes", Content: []byte("routes"), Workflow: &workflow.Workflow{Nodes: nodes}}},
	}}
}

func TestInventoryExecutionModesUseActualSelector(t *testing.T) {
	for _, backend := range []string{workflow.BackendCodex, workflow.BackendOpencode, workflow.BackendClaudeCode} {
		t.Run(backend, func(t *testing.T) {
			f := newInvFixture()
			f.profiles.profiles = []provider.Profile{{Alias: "agent", Kind: provider.ProfileKindAgent, Backend: backend, Model: "gpt-5-sol"}, {Alias: "chat", Kind: provider.ProfileKindChat, Model: "claude-opus-5-5"}}
			f.wfs = inventoryWorkflow(workflow.Node{ID: "interactive", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "agent", Backend: workflow.BackendPi}})
			inv := f.build(t)
			snap, err := inv.Snapshot(context.Background())
			require.NoError(t, err, "an unsupported affected path must not erase unrelated providers")
			modes := map[string]modelcatalog.ExecutionPath{}
			for _, path := range snap.Paths {
				for _, alias := range path.Aliases {
					if alias == "agent" {
						modes[path.Mode] = path
					}
				}
			}
			require.Len(t, modes, 2)
			batch, interactive := modes["batch"], modes["interactive"]
			assert.Equal(t, backend, interactive.Backend, "interactive ignores node override")
			assert.NotEqual(t, batch.Key, interactive.Key)
			assert.NotEqual(t, batch.ConfigRevision, interactive.ConfigRevision)
			_, err = inv.WithBinding(context.Background(), batch, "gpt-5-sol")
			require.NoError(t, err)
			_, err = inv.WithBinding(context.Background(), interactive, "gpt-5-sol")
			if backend == workflow.BackendClaudeCode {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)
			}
			_, err = inv.WithBinding(context.Background(), pathFor(t, snap, "chat", ""), "claude-opus-5-5")
			require.NoError(t, err)
		})
	}
}

func TestInventoryVersionRequestsArePolicyIndependentActualRoutes(t *testing.T) {
	f := newInvFixture()
	f.backend = workflow.BackendCodex
	f.addConn(provider.Connection{Name: "azure", Provider: "azure-foundry", ConfigRevision: 7})
	f.profiles.err = errors.New("profile catalog unavailable")
	f.policy.err = errors.New("policy unavailable")
	f.conns.listErr = errors.New("unrelated connection listing unavailable")
	f.harness.err = errors.New("probe fingerprint unavailable")
	f.wfs = inventoryWorkflow(agentNode("override", "coherent", workflow.BackendOpencode), agentNode("unrelated", "other", workflow.BackendPi))
	f.runs.snaps = []instance.ExecutionSnapshot{runSnapshot(agentNode("active-only", "coherent", workflow.BackendPi))}
	inv := f.build(t)
	var source provider.VersionRequestSource = inv
	p := provider.Profile{Alias: "coherent", Kind: provider.ProfileKindAgent, Model: "gpt-5-sol", FollowTarget: &provider.FollowTarget{Provider: "openai", Tier: "frontier"}}
	requests, err := source.VersionRequests(context.Background(), p)
	require.NoError(t, err)
	assert.ElementsMatch(t, []modelidentity.VersionRequest{
		{Model: p.Model, Backend: workflow.BackendCodex},
		{Model: p.Model, RouteProvider: adapterprovider.ProviderClaudeCLI, Backend: workflow.BackendOpencode},
		{Model: p.Model, RouteProvider: adapterprovider.ProviderClaudeCLI, Backend: workflow.BackendPi},
	}, requests)
	p.ConnectionRef = "azure"
	requests, err = source.VersionRequests(context.Background(), p)
	require.NoError(t, err)
	assert.ElementsMatch(t, []modelidentity.VersionRequest{
		{Model: p.Model, RouteProvider: "azure-foundry", ConnectionRef: "azure", Backend: workflow.BackendCodex},
		{Model: p.Model, RouteProvider: "azure-foundry", ConnectionRef: "azure", Backend: workflow.BackendOpencode},
		{Model: p.Model, RouteProvider: "azure-foundry", ConnectionRef: "azure", Backend: workflow.BackendPi},
	}, requests, "catalog provider must never substitute for the real deployment provider")
	for _, req := range requests {
		_, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), req)
		require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
	}
	assert.Zero(t, f.conns.revealCalls)
}

func TestInventoryVersionRequestFailures(t *testing.T) {
	boom := errors.New("unreadable routing input")
	for _, failure := range []string{"missing connection", "scopes", "manifests", "active snapshots", "unsupported interactive", "cancelled", "missing alias"} {
		t.Run(failure, func(t *testing.T) {
			f := newInvFixture()
			p := provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, Model: "claude-opus-5-5", Backend: workflow.BackendOpencode}
			ctx := context.Background()
			switch failure {
			case "missing connection":
				p.ConnectionRef = "gone"
			case "scopes":
				f.wfs = &invWorkflows{scopeErr: boom}
			case "manifests":
				f.wfs = &invWorkflows{scopes: []workflow.Scope{workflow.Shared()}, loadErr: boom}
			case "active snapshots":
				f.runs.err = boom
			case "unsupported interactive":
				f.wfs = inventoryWorkflow(workflow.Node{Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: p.Alias}})
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "missing alias":
				p.Alias = ""
			}
			requests, err := f.build(t).VersionRequests(ctx, p)
			require.Error(t, err)
			assert.Empty(t, requests)
		})
	}
}

func TestInventoryIncompatibleNodeKindIsAffectedPathUnknown(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{
		{Alias: "wrong", Kind: provider.ProfileKindAgent, Model: "claude-opus-5-5"},
		{Alias: "valid", Kind: provider.ProfileKindChat, Model: "claude-opus-5-5"},
	}
	f.wfs = inventoryWorkflow(
		workflow.Node{ID: "wrong", Type: workflow.NodeTypeInteractiveLLM, Config: workflow.NodeConfig{ModelProfile: "wrong"}},
		workflow.Node{ID: "valid", Type: workflow.NodeTypeInteractiveLLM, Config: workflow.NodeConfig{ModelProfile: "valid"}},
	)
	inv := f.build(t)
	snap, err := inv.Snapshot(context.Background())
	require.NoError(t, err, "one incompatible consumer must not erase unrelated paths")
	seenMismatch, seenValid := false, false
	for _, path := range snap.Paths {
		if path.Mode != modelcatalog.ExecutionModeInteractive {
			continue
		}
		_, err := inv.WithBinding(context.Background(), path, "claude-opus-5-5")
		for _, alias := range path.Aliases {
			if alias == "wrong" {
				seenMismatch = true
				require.ErrorIs(t, err, engine.ErrBindingKindMismatch)
			}
			if alias == "valid" {
				seenValid = true
				require.NoError(t, err)
			}
		}
	}
	assert.True(t, seenMismatch)
	assert.True(t, seenValid)
	requests, err := inv.VersionRequests(context.Background(), f.profiles.profiles[0])
	require.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, requests)
	// Child-input preparation is a chat consumer too, even though spawning the
	// child is not itself an LLM node.
	f.wfs = inventoryWorkflow(workflow.Node{ID: "spawn", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{ModelProfile: "wrong"}})
	requests, err = f.build(t).VersionRequests(context.Background(), f.profiles.profiles[0])
	require.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, requests)
}

func TestInventoryProspectiveRevisionMatchesPersistedWorld(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "route", Provider: "openrouter", ConfigRevision: 4})
	inv := f.build(t)
	ctx := context.Background()
	current, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	p := provider.Profile{Alias: "new", Kind: provider.ProfileKindAgent, ConnectionRef: "route", Model: "deepseek/deepseek-v4-pro", Revision: 1}
	prepared, err := inv.PathsForProfile(ctx, p)
	require.NoError(t, err)
	assert.NotEqual(t, current.Revision, prepared.Revision)
	f.profiles.profiles = []provider.Profile{p}
	saved, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, prepared.ExecutionRevision, saved.ExecutionRevision)
	f.conns.list[0].ConfigRevision++
	stale, err := inv.PathsForProfile(ctx, p)
	require.NoError(t, err)
	assert.NotEqual(t, prepared.Revision, stale.Revision)
}
