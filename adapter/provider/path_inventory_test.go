package provider_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- inventory fixtures ------------------------------------------------------

type invProfiles struct {
	profiles []provider.Profile
	err      error
}

func (f *invProfiles) ListInternal(ctx context.Context) ([]provider.Profile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]provider.Profile(nil), f.profiles...), nil
}

type invConnections struct {
	fakeConnectionStore
	list    []provider.Connection
	listErr error
}

func (f *invConnections) List(context.Context) ([]provider.Connection, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]provider.Connection(nil), f.list...), nil
}

type invPolicy struct {
	policy modelidentity.IdentityPolicy
	err    error
}

func (f *invPolicy) Snapshot(context.Context) (modelidentity.IdentityPolicy, error) {
	return f.policy, f.err
}

type invWorkflows struct {
	scopes   []workflow.Scope
	scopeErr error
	byScope  map[workflow.Scope][]modelcatalog.WorkflowManifest
	loadErr  error
}

func (f *invWorkflows) ListScopes(context.Context) ([]workflow.Scope, error) {
	return f.scopes, f.scopeErr
}

func (f *invWorkflows) LoadManifests(_ context.Context, s workflow.Scope) ([]modelcatalog.WorkflowManifest, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.byScope[s], nil
}

type invSnapshots struct {
	snaps []instance.ExecutionSnapshot
	err   error
}

func (f *invSnapshots) ListExecutionSnapshots(context.Context) ([]instance.ExecutionSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]instance.ExecutionSnapshot(nil), f.snaps...), nil
}

// testFingerprint stands in for the root HMAC closure: deterministic and
// input-sensitive, so every revision assertion observes real input changes.
func testFingerprint(b []byte) string {
	sum := sha256.Sum256(append([]byte("test-key"), b...))
	return hex.EncodeToString(sum[:])
}

type invFixture struct {
	profiles *invProfiles
	conns    *invConnections
	policy   *invPolicy
	wfs      modelcatalog.WorkflowScopeSource
	runs     *invSnapshots
	backend  string
	harness  *invHarness
}

// invHarness is a controllable HarnessFingerprinter keyed by backend.
type invHarness struct {
	state map[string]string
	err   error
}

func (h *invHarness) fingerprint(backend string) ([]byte, error) {
	if h.err != nil {
		return nil, h.err
	}
	return []byte(h.state[backend]), nil
}

func newInvFixture() *invFixture {
	conns := &invConnections{fakeConnectionStore: *newFakeConnectionStore()}
	return &invFixture{
		profiles: &invProfiles{},
		conns:    conns,
		policy:   &invPolicy{policy: modelidentity.IdentityPolicy{Revision: 3}},
		wfs:      &invWorkflows{},
		runs:     &invSnapshots{},
		backend:  workflow.BackendClaudeCode,
		harness:  &invHarness{state: map[string]string{}},
	}
}

func (f *invFixture) addConn(c provider.Connection) {
	f.conns.list = append(f.conns.list, c)
	f.conns.put(c, "secret-"+c.Name)
}

func (f *invFixture) build(t *testing.T) *adapterprovider.ExecutionPathInventory {
	t.Helper()
	factory, interactive := inventorySelectors()
	inv, err := adapterprovider.NewExecutionPathInventory(adapterprovider.InventoryDeps{
		Factory: factory, Interactive: interactive,
		Profiles:            f.profiles,
		Connections:         f.conns,
		Workflows:           f.wfs,
		ActiveRuns:          f.runs,
		Policy:              f.policy,
		Fingerprint:         testFingerprint,
		Harness:             f.harness.fingerprint,
		DefaultAgentBackend: f.backend,
	})
	require.NoError(t, err)
	return inv
}

func pathFor(t *testing.T, snap modelcatalog.PathSnapshot, alias, backend string) modelcatalog.ExecutionPath {
	t.Helper()
	for _, p := range snap.Paths {
		if p.Backend != backend {
			continue
		}
		for _, a := range p.Aliases {
			if a == alias {
				return p
			}
		}
	}
	t.Fatalf("no path for alias %q on backend %q in %+v", alias, backend, snap.Paths)
	return modelcatalog.ExecutionPath{}
}

func backendsOf(snap modelcatalog.PathSnapshot, alias string) []string {
	var out []string
	for _, p := range snap.Paths {
		for _, a := range p.Aliases {
			if a == alias && !slices.Contains(out, p.Backend) {
				out = append(out, p.Backend)
			}
		}
	}
	sort.Strings(out)
	return out
}

func agentNode(id, alias, backend string) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: alias, Backend: backend}}
}

// --- happy ----------------------------------------------------------------

// The inventory reads private and shared workflows from the real on-disk
// repository and applies the agent executor's node > profile > deployment
// backend precedence.
func TestInventoryActualRuntimeRoutes(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	owner := uuid.New()
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "priv", &workflow.Workflow{
		Name: "priv", Nodes: []workflow.Node{agentNode("n1", "coder", workflow.BackendOpencode)},
	}))
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "shared-wf", &workflow.Workflow{
		Name: "shared-wf", Nodes: []workflow.Node{
			agentNode("n1", "coder", workflow.BackendPi),
			agentNode("n2", "reviewer", ""), // inherits the profile backend
		},
	}))

	f := newInvFixture()
	f.wfs = repo
	f.addConn(provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", ConfigRevision: 4})
	f.profiles.profiles = []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, ConnectionRef: "or", Model: "a/b", Revision: 1},
		{Alias: "reviewer", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: "gpt-5", Revision: 2},
	}

	snap, err := f.build(t).Snapshot(ctx)

	require.NoError(t, err)
	inventoriedBackends := backendsOf(snap, "coder")
	// Deployment default (profile has no backend) plus both node overrides.
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendOpencode, workflow.BackendPi}, inventoriedBackends)
	assert.Equal(t, []string{workflow.BackendCodex}, backendsOf(snap, "reviewer"))

	op := pathFor(t, snap, "coder", workflow.BackendOpencode)
	assert.Equal(t, "or", op.ConnectionRef)
	assert.Equal(t, "openrouter", op.RouteProvider)
	assert.Equal(t, string(provider.ProfileKindAgent), op.Kind)
	assert.Equal(t, workflow.BackendOpencode, op.Binding.Backend)
	assert.Equal(t, "https://openrouter.ai/api/v1", op.Binding.BaseURL)
	assert.Empty(t, op.Binding.Model, "the candidate model is supplied later, never inventoried")
	key, err := op.Binding.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "secret-or", key)

	assert.NotEmpty(t, snap.Revision)
	assert.Equal(t, snap.Revision, snap.ExecutionRevision.ID)
	assert.Equal(t, int64(3), snap.ExecutionRevision.PolicyRevision)
	assert.Equal(t, map[string]int64{"or": 4}, snap.ExecutionRevision.Connections)
	assert.Len(t, snap.ExecutionRevision.WorkflowHashes, 2)
	assert.Contains(t, snap.ExecutionRevision.WorkflowHashes, "users/"+owner.String()+"/priv")
	assert.Contains(t, snap.ExecutionRevision.WorkflowHashes, "shared/shared-wf")
}

// --- success ----------------------------------------------------------------

func TestInventoryIncludesSubscriptionPinnedAndUnreferencedConnections(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "unused", Provider: "anthropic-api", BaseURL: "https://api.anthropic.com", ConfigRevision: 1})
	f.profiles.profiles = []provider.Profile{
		{Alias: "chat", Kind: provider.ProfileKindChat, Model: "claude-opus-5-5", Revision: 1},
		{Alias: "pinned", Kind: provider.ProfileKindAgent, SelectionMode: provider.SelectionPinned, Model: "claude-sonnet-5-5", Revision: 1},
	}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	chat := pathFor(t, snap, "chat", "")
	assert.Equal(t, adapterprovider.ProviderClaudeCLI, chat.RouteProvider)
	assert.Equal(t, string(provider.ProfileKindChat), chat.Kind)
	pathFor(t, snap, "pinned", workflow.BackendClaudeCode)

	var unused *modelcatalog.ExecutionPath
	for i := range snap.Paths {
		if snap.Paths[i].ConnectionRef == "unused" {
			unused = &snap.Paths[i]
		}
	}
	require.NotNil(t, unused, "a connection with no profile is still an execution path")
	assert.Empty(t, unused.Aliases)
	assert.Equal(t, "anthropic-api", unused.RouteProvider)
	assert.Equal(t, string(provider.ProfileKindChat), unused.Kind)
}

// Codex runs on its own subscription: the inventory never reveals a key for a
// path, and a codex path's binding does not reveal another connection's key.
func TestInventoryNeverRevealsKeysWhileInventorying(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "or", Provider: "openrouter", ConfigRevision: 1})
	f.profiles.profiles = []provider.Profile{
		{Alias: "cx", Kind: provider.ProfileKindAgent, ConnectionRef: "or", Backend: workflow.BackendCodex, Model: "gpt-5", Revision: 1},
		{Alias: "or-chat", Kind: provider.ProfileKindChat, ConnectionRef: "or", Model: "a/b", Revision: 1},
	}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	assert.Zero(t, f.conns.revealCalls)
	cx := pathFor(t, snap, "cx", workflow.BackendCodex)
	key, err := cx.Binding.RevealKey(context.Background())
	require.NoError(t, err)
	assert.Empty(t, key)
	assert.Zero(t, f.conns.revealCalls, "Codex ignores even a non-empty connection credential")
}

// --- failure ----------------------------------------------------------------

// A profile whose connection is gone cannot execute; the inventory does not
// pretend that path is absent.
func TestInventoryMissingConnectionIsIncomplete(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{
		{Alias: "x", Kind: provider.ProfileKindChat, ConnectionRef: "gone", Model: "m", Revision: 1},
	}

	_, err := f.build(t).Snapshot(context.Background())

	assert.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
}

// --- error ------------------------------------------------------------------

func TestInventoryReadFaultsReportIncompleteNeverEmpty(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(f *invFixture){
		"profiles":    func(f *invFixture) { f.profiles.err = boom },
		"connections": func(f *invFixture) { f.conns.listErr = boom },
		"policy":      func(f *invFixture) { f.policy.err = boom },
		"scopes":      func(f *invFixture) { f.wfs = &invWorkflows{scopeErr: boom} },
		"manifests": func(f *invFixture) {
			f.wfs = &invWorkflows{scopes: []workflow.Scope{workflow.Shared()}, loadErr: boom}
		},
		"active runs": func(f *invFixture) { f.runs.err = boom },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInvFixture()
			f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindChat, Model: "m", Revision: 1}}
			breakIt(f)
			inv := f.build(t)

			snap, err := inv.Snapshot(context.Background())

			assert.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
			assert.ErrorIs(t, err, boom)
			assert.Empty(t, snap.Paths)
			assert.Empty(t, snap.Revision)
			rev, err := inv.Revision(context.Background())
			assert.Error(t, err)
			assert.Empty(t, rev)
		})
	}
}

func TestInventoryEmptyFingerprintIsUnavailable(t *testing.T) {
	f := newInvFixture()
	factory, interactive := inventorySelectors()
	inv, err := adapterprovider.NewExecutionPathInventory(adapterprovider.InventoryDeps{
		Factory: factory, Interactive: interactive,
		Profiles: f.profiles, Connections: f.conns, Workflows: f.wfs, ActiveRuns: f.runs, Policy: f.policy,
		Harness: f.harness.fingerprint, Fingerprint: func([]byte) string { return "" }, DefaultAgentBackend: f.backend,
	})
	require.NoError(t, err)

	_, err = inv.Snapshot(context.Background())

	assert.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
}

func TestNewExecutionPathInventoryRejectsMissingInputs(t *testing.T) {
	f := newInvFixture()
	factory, interactive := inventorySelectors()
	full := adapterprovider.InventoryDeps{
		Factory: factory, Interactive: interactive, DefaultAgentBackend: f.backend,
		Profiles: f.profiles, Connections: f.conns, Workflows: f.wfs, ActiveRuns: f.runs, Policy: f.policy, Fingerprint: testFingerprint, Harness: f.harness.fingerprint,
	}
	cases := map[string]func(d *adapterprovider.InventoryDeps){
		"deployment backend":   func(d *adapterprovider.InventoryDeps) { d.DefaultAgentBackend = "" },
		"factory":              func(d *adapterprovider.InventoryDeps) { d.Factory = nil },
		"interactive selector": func(d *adapterprovider.InventoryDeps) { d.Interactive = nil },
		"profiles":             func(d *adapterprovider.InventoryDeps) { d.Profiles = nil },
		"connections":          func(d *adapterprovider.InventoryDeps) { d.Connections = nil },
		"workflows":            func(d *adapterprovider.InventoryDeps) { d.Workflows = nil },
		"active runs":          func(d *adapterprovider.InventoryDeps) { d.ActiveRuns = nil },
		"policy":               func(d *adapterprovider.InventoryDeps) { d.Policy = nil },
		"fingerprint":          func(d *adapterprovider.InventoryDeps) { d.Fingerprint = nil },
		"harness":              func(d *adapterprovider.InventoryDeps) { d.Harness = nil },
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			d := full
			drop(&d)
			inv, err := adapterprovider.NewExecutionPathInventory(d)
			assert.Error(t, err)
			assert.Nil(t, inv)
		})
	}
}

// --- edge -------------------------------------------------------------------

// Revision scopes: a model-only edit (new profile revision) or a policy-only
// edit changes the snapshot revision but keeps every path's ConfigRevision, so
// confirmed evidence needs no re-probe; a rotated credential changes both.
func TestInventoryRevisionScopes(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", ConfigRevision: 1})
	f.profiles.profiles = []provider.Profile{
		{Alias: "a", Kind: provider.ProfileKindChat, ConnectionRef: "or", Model: "x/1", Revision: 1},
	}
	inv := f.build(t)
	ctx := context.Background()
	base, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	baseCfg := pathFor(t, base, "a", "").ConfigRevision
	baseKey := pathFor(t, base, "a", "").Key

	again, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, base.Revision, again.Revision, "an unchanged inventory is deterministic")

	f.profiles.profiles[0].Model, f.profiles.profiles[0].Revision = "x/2", 2
	modelOnly, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, base.Revision, modelOnly.Revision)
	assert.Equal(t, baseCfg, pathFor(t, modelOnly, "a", "").ConfigRevision)

	f.policy.policy.Revision = 4
	policyOnly, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, modelOnly.Revision, policyOnly.Revision)
	assert.Equal(t, baseCfg, pathFor(t, policyOnly, "a", "").ConfigRevision)

	f.conns.list[0].ConfigRevision = 2 // credential rotated
	rotated, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, policyOnly.Revision, rotated.Revision)
	rp := pathFor(t, rotated, "a", "")
	assert.NotEqual(t, baseCfg, rp.ConfigRevision)
	assert.Equal(t, baseKey, rp.Key, "the path identity survives rotation")
}

// A workflow content edit changes the snapshot revision through its hash.
func TestInventoryWorkflowEditChangesRevision(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	wf := &workflow.Workflow{Name: "w", Nodes: []workflow.Node{agentNode("n", "a", "")}}
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "w", wf))
	f := newInvFixture()
	f.wfs = repo
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	inv := f.build(t)
	before, err := inv.Snapshot(ctx)
	require.NoError(t, err)

	wf.Nodes[0].Config.Backend = workflow.BackendCodex
	require.NoError(t, repo.Update(ctx, workflow.Shared(), "w", wf))
	after, err := inv.Snapshot(ctx)

	require.NoError(t, err)
	assert.NotEqual(t, before.Revision, after.Revision)
	assert.NotEqual(t, before.ExecutionRevision.WorkflowHashes["shared/w"], after.ExecutionRevision.WorkflowHashes["shared/w"])
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendCodex}, backendsOf(after, "a"))
}

// A node backend equal to the effective default adds no duplicate path, and a
// node naming an unknown alias or a non-model node contributes nothing.
func TestInventoryDeduplicatesAndIgnoresNonConsumers(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	f.wfs = &invWorkflows{
		scopes: []workflow.Scope{workflow.Shared()},
		byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{workflow.Shared(): {{
			Scope: workflow.Shared(), Name: "w", Content: []byte("w"),
			Workflow: &workflow.Workflow{Name: "w", Nodes: []workflow.Node{
				agentNode("same", "a", workflow.BackendClaudeCode),
				agentNode("ghost", "nope", workflow.BackendPi),
				{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{Backend: workflow.BackendPi}},
			}},
		}}},
	}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode}, backendsOf(snap, "a"))
	assert.Len(t, snap.Paths, 1)
}

// The interactive executor ignores node.Config.Backend (it routes on the
// profile backend floored to the deployment default), so an interactive node's
// backend field never invents a path the runtime does not take.
func TestInventoryInteractiveNodeUsesProfileBackend(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	f.wfs = &invWorkflows{
		scopes: []workflow.Scope{workflow.Shared()},
		byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{workflow.Shared(): {{
			Scope: workflow.Shared(), Name: "w", Content: []byte("w"),
			Workflow: &workflow.Workflow{Name: "w", Nodes: []workflow.Node{
				{ID: "i", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "a", Backend: workflow.BackendCodex}},
			}},
		}}},
	}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode}, backendsOf(snap, "a"))
}

func TestInventoryCancelledContext(t *testing.T) {
	f := newInvFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.build(t).Snapshot(ctx)

	assert.ErrorIs(t, err, context.Canceled)
}

// node > profile > deployment: a node override beats an explicit profile
// backend, and the profile backend beats the deployment default.
func TestInventoryBackendPrecedence(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: "m", Revision: 1}}
	f.wfs = &invWorkflows{
		scopes: []workflow.Scope{workflow.Shared()},
		byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{workflow.Shared(): {{
			Scope: workflow.Shared(), Name: "w", Content: []byte("w"),
			Workflow: &workflow.Workflow{Name: "w", Nodes: []workflow.Node{agentNode("n", "a", workflow.BackendOpencode)}},
		}}},
	}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendCodex, workflow.BackendOpencode}, backendsOf(snap, "a"))
	assert.NotContains(t, backendsOf(snap, "a"), workflow.BackendClaudeCode)
}

// --- active-run snapshots ---------------------------------------------------

func runSnapshot(nodes ...workflow.Node) instance.ExecutionSnapshot {
	ns := make([]any, 0, len(nodes))
	for _, n := range nodes {
		ns = append(ns, map[string]any{
			"id": n.ID, "type": string(n.Type),
			"config": map[string]any{"model_profile": n.Config.ModelProfile, "backend": n.Config.Backend},
		})
	}
	return instance.ExecutionSnapshot{InstanceID: uuid.New(), WorkflowSnapshot: map[string]any{"name": "snap", "nodes": ns}}
}

// Happy: a live-resolving run executes from its trigger-time snapshot, so a node
// override that exists only there (the workflow was since edited, renamed or
// deleted) is still a route the advanced model will run on.
func TestInventoryIncludesActiveSnapshotOnlyOverrides(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	f.runs.snaps = []instance.ExecutionSnapshot{runSnapshot(agentNode("n", "a", workflow.BackendPi))}

	snap, err := f.build(t).Snapshot(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendPi}, backendsOf(snap, "a"))
	assert.Empty(t, snap.ExecutionRevision.WorkflowHashes, "an immutable run snapshot is not a file the gate re-reads")
}

// Edge: a run whose routes the inventory already holds changes nothing, so
// triggering another ordinary run does not invalidate gathered evidence; a run
// adding a new route does change the revision; an unknown alias or a non-agent
// node in a snapshot adds no path, as in a manifest.
func TestInventoryActiveRunsChangeRevisionOnlyThroughRoutes(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	inv := f.build(t)
	ctx := context.Background()
	base, err := inv.Snapshot(ctx)
	require.NoError(t, err)

	f.runs.snaps = []instance.ExecutionSnapshot{
		runSnapshot(agentNode("same", "a", "")),
		runSnapshot(agentNode("ghost", "nope", workflow.BackendPi)),
	}
	same, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, base.Revision, same.Revision)
	require.Len(t, same.Paths, len(base.Paths))
	for i := range base.Paths {
		assert.Equal(t, base.Paths[i].Key, same.Paths[i].Key)
		assert.Equal(t, base.Paths[i].ConfigRevision, same.Paths[i].ConfigRevision)
		assert.Equal(t, base.Paths[i].Aliases, same.Paths[i].Aliases)
	}

	f.runs.snaps = append(f.runs.snaps, runSnapshot(workflow.Node{ID: "i", Type: workflow.NodeTypeInteractiveAgent, Config: workflow.NodeConfig{ModelProfile: "a", Backend: workflow.BackendCodex}}))
	interactive, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, same.Revision, interactive.Revision, "interactive launch is a distinct route even on the same backend")
	require.Len(t, interactive.Paths, len(same.Paths)+1)

	f.runs.snaps = append(f.runs.snaps, runSnapshot(agentNode("o", "a", workflow.BackendOpencode)))
	widened, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, base.Revision, widened.Revision)
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendOpencode}, backendsOf(widened, "a"))
}

// Error: a snapshot that does not parse as a workflow cannot be enumerated, so
// the inventory is incomplete rather than missing that run's routes.
func TestInventoryUnparseableActiveSnapshotIsIncomplete(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "a", Kind: provider.ProfileKindAgent, Model: "m", Revision: 1}}
	f.runs.snaps = []instance.ExecutionSnapshot{{InstanceID: uuid.New(), WorkflowSnapshot: map[string]any{"nodes": "not-a-list"}}}

	snap, err := f.build(t).Snapshot(context.Background())

	assert.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
	assert.Empty(t, snap.Paths)
}

// --- harness fingerprints -----------------------------------------------------

func TestInventoryHarnessEditsChangeOnlyThatBackendsConfigRevision(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{
		{Alias: "cc", Kind: provider.ProfileKindAgent, Backend: workflow.BackendClaudeCode, Model: "m"},
		{Alias: "cx", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: "m"},
	}
	inv := f.build(t)
	before, err := inv.Snapshot(context.Background())
	require.NoError(t, err)

	f.harness.state[workflow.BackendCodex] = "codex binary upgraded"
	after, err := inv.Snapshot(context.Background())
	require.NoError(t, err)

	assert.NotEqual(t, before.Revision, after.Revision, "the gate token covers harness edits")
	cx0, cx1 := pathFor(t, before, "cx", workflow.BackendCodex), pathFor(t, after, "cx", workflow.BackendCodex)
	assert.Equal(t, cx0.Key, cx1.Key, "the route identity survives a harness edit")
	assert.NotEqual(t, cx0.ConfigRevision, cx1.ConfigRevision, "a harness edit demands new proof")
	cc0, cc1 := pathFor(t, before, "cc", workflow.BackendClaudeCode), pathFor(t, after, "cc", workflow.BackendClaudeCode)
	assert.Equal(t, cc0.ConfigRevision, cc1.ConfigRevision, "another harness is unaffected")
}

func TestInventoryHarnessReadFaultIsIncomplete(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "cx", Kind: provider.ProfileKindAgent, Backend: workflow.BackendCodex, Model: "m"}}
	f.harness.err = errors.New("auth.json: permission denied")

	snap, err := f.build(t).Snapshot(context.Background())

	assert.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
	assert.Empty(t, snap.Paths)
}

func TestInventoryBareClaudeProfileUsesClaudeHarness(t *testing.T) {
	f := newInvFixture()
	f.profiles.profiles = []provider.Profile{{Alias: "chat", Kind: provider.ProfileKindChat, Model: "m"}}
	inv := f.build(t)
	before, err := inv.Snapshot(context.Background())
	require.NoError(t, err)

	f.harness.state[workflow.BackendClaudeCode] = "claude upgraded"
	after, err := inv.Snapshot(context.Background())
	require.NoError(t, err)

	assert.NotEqual(t, before.Paths[0].ConfigRevision, after.Paths[0].ConfigRevision,
		"a connectionless chat profile runs the claude binary")
}

// --- prospective routes and candidate bindings --------------------------------

// PathsForProfile builds the routes a profile that does not exist yet would
// take: its own default route plus the saved node overrides that already name
// its alias. It never returns another alias's routes.
func TestInventoryPathsForProfileBuildsProspectiveRoutes(t *testing.T) {
	ctx := context.Background()
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", ConfigRevision: 4})
	f.profiles.profiles = []provider.Profile{
		{Alias: "other", Kind: provider.ProfileKindAgent, Backend: workflow.BackendPi, ConnectionRef: "or", Model: "x/y", Revision: 1},
	}
	f.wfs = &invWorkflows{
		scopes: []workflow.Scope{workflow.Shared()},
		byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{workflow.Shared(): {{
			Scope: workflow.Shared(), Name: "wf", Content: []byte("wf"),
			Workflow: &workflow.Workflow{Nodes: []workflow.Node{
				agentNode("n1", "fresh", workflow.BackendOpencode),
				agentNode("n2", "other", workflow.BackendCodex),
			}},
		}}},
	}
	inv := f.build(t)

	snap, err := inv.PathsForProfile(ctx, provider.Profile{Alias: "fresh", Kind: provider.ProfileKindAgent, ConnectionRef: "or", Model: "a/b", Revision: 0})

	require.NoError(t, err)
	assert.Equal(t, []string{workflow.BackendClaudeCode, workflow.BackendOpencode}, backendsOf(snap, "fresh"))
	assert.Empty(t, backendsOf(snap, "other"), "another alias's routes are not part of the prospective profile")
	assert.NotEmpty(t, snap.Revision)
	key, err := pathFor(t, snap, "fresh", workflow.BackendOpencode).Binding.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "secret-or", key)

	// The saved world is untouched: the prospective profile was not persisted.
	saved, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	assert.Empty(t, backendsOf(saved, "fresh"))
}

// A prospective edit of an existing alias replaces the saved route instead of
// adding to it, so the proof is built for the connection the edit would use.
func TestInventoryPathsForProfileEditReplacesSavedRoute(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "one", Provider: "openrouter", BaseURL: "https://one.example/v1", ConfigRevision: 1})
	f.addConn(provider.Connection{Name: "two", Provider: "openrouter", BaseURL: "https://two.example/v1", ConfigRevision: 1})
	saved := provider.Profile{Alias: "coder", Kind: provider.ProfileKindAgent, ConnectionRef: "one", Model: "a/b", Revision: 3}
	f.profiles.profiles = []provider.Profile{saved}
	edited := saved
	edited.ConnectionRef = "two"

	snap, err := f.build(t).PathsForProfile(context.Background(), edited)

	require.NoError(t, err)
	require.Len(t, snap.Paths, 1)
	assert.Equal(t, "two", snap.Paths[0].ConnectionRef)
}

func TestInventoryPathsForProfileFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := newInvFixture()
	inv := f.build(t)

	_, err := inv.PathsForProfile(ctx, provider.Profile{Kind: provider.ProfileKindChat})
	require.Error(t, err, "a prospective profile needs an alias to name its consumers")

	_, err = inv.PathsForProfile(ctx, provider.Profile{Alias: "x", Kind: provider.ProfileKindChat, ConnectionRef: "missing"})
	require.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
	require.ErrorIs(t, err, provider.ErrConnectionNotFound)

	f.profiles.err = errors.New("boom")
	_, err = inv.PathsForProfile(ctx, provider.Profile{Alias: "x", Kind: provider.ProfileKindChat})
	require.ErrorIs(t, err, modelcatalog.ErrInventoryIncomplete)
}

// WithBinding supplies the candidate model for the same path and leaves the
// inventoried path's own binding model-free.
func TestInventoryWithBindingSuppliesCandidateModel(t *testing.T) {
	ctx := context.Background()
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", ConfigRevision: 4})
	f.profiles.profiles = []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, ConnectionRef: "or", Model: "a/b", Revision: 1, AllowedTools: []string{"Read"}},
	}
	inv := f.build(t)
	snap, err := inv.Snapshot(ctx)
	require.NoError(t, err)
	path := pathFor(t, snap, "coder", workflow.BackendClaudeCode)

	b, err := inv.WithBinding(ctx, path, "a/c")

	require.NoError(t, err)
	assert.Equal(t, "a/c", b.Model)
	assert.Equal(t, "openrouter", b.Provider)
	assert.Equal(t, workflow.BackendClaudeCode, b.Backend)
	assert.Equal(t, []string{"Read"}, b.AllowedTools)
	key, err := b.RevealKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "secret-or", key)
	assert.Empty(t, path.Binding.Model, "the path itself is not mutated")

	_, err = inv.WithBinding(ctx, path, "")
	require.Error(t, err, "an empty candidate model can never be proved")
	_, err = inv.WithBinding(ctx, modelcatalog.ExecutionPath{Key: "k"}, "a/c")
	require.Error(t, err, "a path with no binding was not produced by the inventory")
}
