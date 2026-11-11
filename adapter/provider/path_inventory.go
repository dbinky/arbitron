package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// InventoryProfileSource is the trusted (non-impersonating) profile read the
// inventory consumes; ProfileService.ListInternal implements it.
type InventoryProfileSource interface {
	ListInternal(context.Context) ([]provider.Profile, error)
}

// InventoryDeps are the real inputs an ExecutionPathInventory reads. Every
// field is required: an inventory missing an input could only ever report a
// vacuous (zero-consumer) snapshot.
type InventoryDeps struct {
	Profiles    InventoryProfileSource
	Connections provider.ConnectionStore
	Workflows   modelcatalog.WorkflowScopeSource
	// ActiveRuns lists the snapshots of runs that still resolve profiles live;
	// their node overrides are routes even when no workflow on disk names them.
	ActiveRuns instance.ExecutionSnapshotSource
	Policy     modelidentity.PolicySource
	// Fingerprint is the keyed (HMAC) closure over the installation secret.
	// Path keys, configuration revisions and the snapshot digest go through it
	// so sensitive configuration is never exposed in clear.
	Fingerprint func([]byte) string
	// Harness describes each harness backend's binary and account files; its
	// bytes feed the path configuration revision so an upgraded binary or a
	// switched account needs new proof.
	Harness HarnessFingerprinter
	// DefaultAgentBackend is the deployment floor the agent executor and the
	// interactive selector apply after node and profile.
	DefaultAgentBackend string
	// These are the same selectors used by execution, including the deployment's
	// interactive opt-in map. A batch adapter is not proof of stream support.
	Factory     *Factory
	Interactive engine.InteractiveStreamSelector
}

// ExecutionPathInventory enumerates every route a model alias actually executes
// on: each profile's own route, each workflow node backend override of an
// agent_call (node > profile > deployment default, as AgentCallExecutor applies
// it) in every workflow on disk and in every live-resolving active run's
// snapshot, interactive consumers, unreferenced connections and the deployment
// harness. Its digest is the revision the shared ExecutionGate rechecks before
// an advancement commits.
type ExecutionPathInventory struct {
	d InventoryDeps
}

// NewExecutionPathInventory validates that every input is present.
func NewExecutionPathInventory(d InventoryDeps) (*ExecutionPathInventory, error) {
	switch {
	case d.Profiles == nil:
		return nil, errors.New("path inventory: profile source is required")
	case d.Connections == nil:
		return nil, errors.New("path inventory: connection store is required")
	case d.Workflows == nil:
		return nil, errors.New("path inventory: workflow scope source is required")
	case d.ActiveRuns == nil:
		return nil, errors.New("path inventory: active-run snapshot source is required")
	case d.Policy == nil:
		return nil, errors.New("path inventory: identity policy source is required")
	case d.Fingerprint == nil:
		return nil, errors.New("path inventory: fingerprint closure is required")
	case d.Harness == nil:
		return nil, errors.New("path inventory: harness fingerprinter is required")
	case d.Factory == nil:
		return nil, errors.New("path inventory: batch factory is required")
	case d.Interactive == nil:
		return nil, errors.New("path inventory: interactive selector is required")
	case d.DefaultAgentBackend == "":
		return nil, errors.New("path inventory: deployment agent backend is required")
	}
	return &ExecutionPathInventory{d: d}, nil
}

// Revision returns the current snapshot digest. It is the ExecutionGate's
// revision source: any unreadable input is an error, never an empty digest.
func (i *ExecutionPathInventory) Revision(ctx context.Context) (string, error) {
	snap, err := i.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return snap.Revision, nil
}

// Snapshot reads every input once and returns the paths in use with the
// revision token. Any read fault returns ErrInventoryIncomplete and no paths.
func (i *ExecutionPathInventory) Snapshot(ctx context.Context) (modelcatalog.PathSnapshot, error) {
	return i.scan(ctx, nil)
}

// PathsForProfile returns the routes a profile would take before it is saved:
// its own default route plus every saved node override that already names its
// alias. A profile already stored under the alias is replaced by p, so an edit
// is proved on the route it would use. The revision is that of the prospective
// world; only the paths of p's alias are returned.
func (i *ExecutionPathInventory) PathsForProfile(ctx context.Context, p provider.Profile) (modelcatalog.PathSnapshot, error) {
	if p.Alias == "" {
		return modelcatalog.PathSnapshot{}, errors.New("path inventory: a prospective profile needs an alias")
	}
	snap, err := i.scan(ctx, &p)
	if err != nil {
		return snap, err
	}
	own := snap.Paths[:0:0]
	for _, path := range snap.Paths {
		if containsString(path.Aliases, p.Alias) {
			own = append(own, path)
		}
	}
	snap.Paths = own
	return snap, nil
}

// ConnectionProvider reads only connection metadata, even before a following
// profile has a model. Unrelated workflow, run and catalog faults cannot change
// whether the stricter LunaRoute profile policy applies.
func (i *ExecutionPathInventory) ConnectionProvider(ctx context.Context, p provider.Profile) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.ConnectionRef == "" {
		return "", nil
	}
	conn, err := i.d.Connections.Get(ctx, p.ConnectionRef)
	if err != nil {
		return "", fmt.Errorf("%w: connection metadata: %w", modelcatalog.ErrInventoryIncomplete, err)
	}
	return conn.Provider, nil
}

// VersionRequests uses the supplied coherent profile, not another profile read.
// It enumerates global routes for profile validation. Policy, catalog and
// harness proof availability do not affect these fixed-version requests.
func (i *ExecutionPathInventory) VersionRequests(ctx context.Context, p provider.Profile) ([]modelidentity.VersionRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Alias == "" {
		return nil, errors.New("path inventory: a prospective profile needs an alias")
	}
	var conn provider.Connection
	if p.ConnectionRef != "" {
		var err error
		conn, err = i.d.Connections.Get(ctx, p.ConnectionRef)
		if err != nil {
			return nil, fmt.Errorf("%w: connection %q: %w", modelcatalog.ErrInventoryIncomplete, p.ConnectionRef, err)
		}
	}
	manifests, err := i.manifests(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: workflows: %w", modelcatalog.ErrInventoryIncomplete, err)
	}
	runs, err := i.d.ActiveRuns.ListExecutionSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: active runs: %w", modelcatalog.ErrInventoryIncomplete, err)
	}
	workflows := make([]*workflow.Workflow, 0, len(manifests)+len(runs))
	for _, m := range manifests {
		workflows = append(workflows, m.Workflow)
	}
	for _, run := range runs {
		wf, err := workflowFromSnapshot(run.WorkflowSnapshot)
		if err != nil {
			return nil, fmt.Errorf("%w: active run %s: %w", modelcatalog.ErrInventoryIncomplete, run.InstanceID, err)
		}
		workflows = append(workflows, wf)
	}
	return i.versionRequests(ctx, p, conn, workflows)
}

// VersionRequestsForWorkflow reads only this trigger's connection and workflow.
// Global inventory faults remain fatal to discovery and profile validation,
// while pin capture is independent of unrelated saved manifests and runs.
func (i *ExecutionPathInventory) VersionRequestsForWorkflow(ctx context.Context, p provider.Profile, wf *workflow.Workflow) ([]modelidentity.VersionRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, errors.New("path inventory: trigger workflow is required")
	}
	if p.Alias == "" {
		return nil, errors.New("path inventory: a prospective profile needs an alias")
	}
	var conn provider.Connection
	if p.ConnectionRef != "" {
		var err error
		conn, err = i.d.Connections.Get(ctx, p.ConnectionRef)
		if err != nil {
			return nil, fmt.Errorf("%w: connection %q: %w", modelcatalog.ErrInventoryIncomplete, p.ConnectionRef, err)
		}
	}
	return i.versionRequests(ctx, p, conn, []*workflow.Workflow{wf})
}

func (i *ExecutionPathInventory) versionRequests(ctx context.Context, p provider.Profile, conn provider.Connection, workflows []*workflow.Workflow) ([]modelidentity.VersionRequest, error) {
	requests := make(map[modelidentity.VersionRequest]bool)
	add := func(p provider.Profile, backend, mode string, kind provider.ProfileKind) error {
		b := routeBinding(p, backend, conn, i.d.Connections)
		if err := i.validateRoute(mode, string(kind), b); err != nil {
			return err
		}
		requests[modelidentity.VersionRequest{Model: p.Model, RouteProvider: b.Provider, ConnectionRef: p.ConnectionRef, Backend: backend}] = true
		return nil
	}
	if err := add(p, i.profileBackend(p, ""), modelcatalog.ExecutionModeBatch, p.Kind); err != nil {
		return nil, err
	}
	byAlias := map[string]provider.Profile{p.Alias: p}
	for _, wf := range workflows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := i.addConsumers(add, byAlias, wf); err != nil {
			return nil, err
		}
	}
	out := make([]modelidentity.VersionRequest, 0, len(requests))
	for req := range requests {
		out = append(out, req)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Backend < out[b].Backend })
	return out, nil
}

func (i *ExecutionPathInventory) validateRoute(mode, kind string, b provider.ResolvedBinding) error {
	if kind != string(b.Kind) {
		return fmt.Errorf("%w: route requires %q, profile has %q", engine.ErrBindingKindMismatch, kind, b.Kind)
	}
	switch mode {
	case modelcatalog.ExecutionModeInteractive:
		_, err := i.d.Interactive.StreamProviderFor(b)
		return err
	case modelcatalog.ExecutionModeBatch:
		switch b.Kind {
		case provider.ProfileKindChat:
			_, err := i.d.Factory.ChatProviderFor(b)
			return err
		case provider.ProfileKindAgent:
			_, err := i.d.Factory.AgentProviderFor(b)
			return err
		}
	}
	return fmt.Errorf("path inventory: unsupported kind %q or mode %q", b.Kind, mode)
}

// WithBinding supplies the candidate model for the same path: a copy of the
// path's binding (endpoint, backend, tools and the lazy key reveal) with Model
// set, after verifying its configuration against the current inputs. The path
// itself stays model-free.
func (i *ExecutionPathInventory) WithBinding(ctx context.Context, path modelcatalog.ExecutionPath, model string) (provider.ResolvedBinding, error) {
	if err := ctx.Err(); err != nil {
		return provider.ResolvedBinding{}, err
	}
	if model == "" {
		return provider.ResolvedBinding{}, errors.New("path inventory: a candidate model is required")
	}
	if path.Binding.RevealKey == nil {
		return provider.ResolvedBinding{}, errors.New("path inventory: path was not produced by the inventory")
	}
	// Rebuild just this route from current connection/account/config inputs.
	// Prospective profiles need not exist in Snapshot yet, but their proof must
	// still name the exact configuration that will receive this check.
	conns := map[string]provider.Connection{}
	if path.ConnectionRef != "" {
		conn, err := i.d.Connections.Get(ctx, path.ConnectionRef)
		if err != nil {
			return provider.ResolvedBinding{}, err
		}
		conns[path.ConnectionRef] = conn
	}
	builder := pathBuilder{fp: i.d.Fingerprint, harness: i.d.Harness, store: i.d.Connections, conns: conns, byKey: map[string]*modelcatalog.ExecutionPath{}}
	p := provider.Profile{Kind: path.Binding.Kind, ConnectionRef: path.ConnectionRef,
		AllowedTools: path.Binding.AllowedTools, ContextWindow: path.Binding.ContextWindow, Effort: path.Binding.Effort}
	if err := builder.add(p, path.Backend, path.Mode, provider.ProfileKind(path.Kind)); err != nil {
		return provider.ResolvedBinding{}, err
	}
	current := builder.byKey[path.Key]
	if current == nil || current.ConfigRevision != path.ConfigRevision || current.RouteProvider != path.RouteProvider {
		return provider.ResolvedBinding{}, provider.ErrExecutionRevisionStale
	}
	b := current.Binding
	b.Model = model
	if err := i.validateRoute(path.Mode, path.Kind, b); err != nil {
		return provider.ResolvedBinding{}, err
	}
	return b, nil
}

// scan reads every input once. overlay, when set, is a prospective profile
// that replaces the stored profile of its alias (or joins the set).
func (i *ExecutionPathInventory) scan(ctx context.Context, overlay *provider.Profile) (modelcatalog.PathSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return modelcatalog.PathSnapshot{}, err
	}
	profiles, err := i.d.Profiles.ListInternal(ctx)
	if err != nil {
		return incomplete("profiles", err)
	}
	if overlay != nil {
		merged := make([]provider.Profile, 0, len(profiles)+1)
		for _, p := range profiles {
			if p.Alias != overlay.Alias {
				merged = append(merged, p)
			}
		}
		profiles = append(merged, *overlay)
	}
	conns, err := i.d.Connections.List(ctx)
	if err != nil {
		return incomplete("connections", err)
	}
	policy, err := i.d.Policy.Snapshot(ctx)
	if err != nil {
		return incomplete("identity policy", err)
	}
	manifests, err := i.manifests(ctx)
	if err != nil {
		return incomplete("workflows", err)
	}
	runs, err := i.d.ActiveRuns.ListExecutionSnapshots(ctx)
	if err != nil {
		return incomplete("active runs", err)
	}

	b := pathBuilder{fp: i.d.Fingerprint, harness: i.d.Harness, store: i.d.Connections, byKey: map[string]*modelcatalog.ExecutionPath{}}
	b.conns = make(map[string]provider.Connection, len(conns))
	connRevisions := make(map[string]int64, len(conns))
	for _, c := range conns {
		b.conns[c.Name] = c
		connRevisions[c.Name] = c.ConfigRevision
	}
	byAlias := make(map[string]provider.Profile, len(profiles))
	profileRevisions := make(map[string]int64, len(profiles))
	referenced := map[string]bool{}
	for _, p := range profiles {
		byAlias[p.Alias] = p
		profileRevisions[p.Alias] = p.Revision
		if p.ConnectionRef != "" {
			referenced[p.ConnectionRef] = true
		}
		if err := b.add(p, i.profileBackend(p, ""), modelcatalog.ExecutionModeBatch, p.Kind); err != nil {
			return incomplete("profile "+p.Alias, err)
		}
	}

	workflowHashes := make(map[string]string, len(manifests))
	for _, m := range manifests {
		workflowHashes[workflowKey(m.Scope, m.Name)] = i.d.Fingerprint(append([]byte("workflow\x00"), m.Content...))
		if err := i.addConsumers(b.add, byAlias, m.Workflow); err != nil {
			return incomplete("workflow "+workflowKey(m.Scope, m.Name), err)
		}
	}
	// A run's snapshot is immutable, so it contributes routes, not a hash the
	// gate re-reads: triggering a run on routes already held keeps the revision.
	for _, r := range runs {
		wf, err := workflowFromSnapshot(r.WorkflowSnapshot)
		if err == nil {
			err = i.addConsumers(b.add, byAlias, wf)
		}
		if err != nil {
			return incomplete("active run "+r.InstanceID.String(), err)
		}
	}

	for _, c := range conns {
		if referenced[c.Name] {
			continue
		}
		if err := b.add(provider.Profile{Kind: provider.ProfileKindChat, ConnectionRef: c.Name}, "", modelcatalog.ExecutionModeBatch, provider.ProfileKindChat); err != nil {
			return incomplete("connection "+c.Name, err)
		}
	}

	// The deployment itself configures a subscription harness, even when no
	// profile or workflow consumes it yet.
	if err := b.add(provider.Profile{Kind: provider.ProfileKindAgent}, i.d.DefaultAgentBackend, modelcatalog.ExecutionModeBatch, provider.ProfileKindAgent); err != nil {
		return incomplete("deployment harness", err)
	}
	paths := b.paths()
	digest, err := json.Marshal(struct {
		Policy      int64             `json:"policy"`
		Connections map[string]int64  `json:"connections"`
		Profiles    map[string]int64  `json:"profiles"`
		Workflows   map[string]string `json:"workflows"`
		Default     string            `json:"default_agent_backend"`
		Paths       [][3]string       `json:"paths"`
	}{policy.Revision, connRevisions, profileRevisions, workflowHashes, i.d.DefaultAgentBackend, pathDigestRows(paths)})
	if err != nil {
		return incomplete("digest", err)
	}
	revision := i.d.Fingerprint(append([]byte("execution-revision\x00"), digest...))
	if revision == "" {
		return incomplete("digest", errors.New("fingerprint produced an empty revision"))
	}
	return modelcatalog.PathSnapshot{
		Revision: revision,
		ExecutionRevision: provider.ExecutionRevision{
			ID:             revision,
			PolicyRevision: policy.Revision,
			Connections:    connRevisions,
			WorkflowHashes: workflowHashes,
		},
		Paths: paths,
	}, nil
}

// profileBackend applies the agent executor's precedence (node > profile >
// deployment default). Chat profiles have no backend axis.
func (i *ExecutionPathInventory) profileBackend(p provider.Profile, node string) string {
	if p.Kind != provider.ProfileKindAgent {
		return ""
	}
	for _, b := range []string{node, p.Backend, i.d.DefaultAgentBackend} {
		if b != "" {
			return b
		}
	}
	return ""
}

// addConsumers shares runtime node precedence and mode enumeration between
// proof inventory and policy-independent fixed-version requests. It ranges over
// workflow.ModelConsumers, the same enumeration run pin capture uses, so the
// parallel_group resolver contributes its interactive agent route.
func (i *ExecutionPathInventory) addConsumers(add func(provider.Profile, string, string, provider.ProfileKind) error, byAlias map[string]provider.Profile, wf *workflow.Workflow) error {
	for _, c := range workflow.ModelConsumers(wf) {
		p, ok := byAlias[c.Alias]
		if !ok {
			continue // the runtime fails this consumer with ErrUnknownProfile
		}
		mode, kind := modelcatalog.ExecutionModeBatch, provider.ProfileKindChat
		if c.Mode == workflow.ConsumerModeInteractive {
			mode = modelcatalog.ExecutionModeInteractive
		}
		if c.Kind == workflow.ConsumerKindAgent {
			kind = provider.ProfileKindAgent
		}
		if err := add(p, i.profileBackend(p, c.NodeBackend), mode, kind); err != nil {
			return err
		}
	}
	return nil
}

// workflowFromSnapshot decodes a run's snapshot the way the engine's graph
// walker does before executing it.
func workflowFromSnapshot(snapshot map[string]any) (*workflow.Workflow, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(raw, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow snapshot: %w", err)
	}
	return &wf, nil
}

func (i *ExecutionPathInventory) manifests(ctx context.Context) ([]modelcatalog.WorkflowManifest, error) {
	scopes, err := i.d.Workflows.ListScopes(ctx)
	if err != nil {
		return nil, err
	}
	var out []modelcatalog.WorkflowManifest
	for _, s := range scopes {
		ms, err := i.d.Workflows.LoadManifests(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("scope %s: %w", workflowKey(s, ""), err)
		}
		out = append(out, ms...)
	}
	return out, nil
}

func incomplete(what string, err error) (modelcatalog.PathSnapshot, error) {
	return modelcatalog.PathSnapshot{}, fmt.Errorf("%w: %s: %w", modelcatalog.ErrInventoryIncomplete, what, err)
}

func workflowKey(s workflow.Scope, name string) string {
	prefix := "shared"
	if s.Kind == workflow.ScopePrivate {
		prefix = "users/" + s.OwnerID.String()
	}
	if name == "" {
		return prefix
	}
	return prefix + "/" + name
}

// pathBuilder accumulates paths by key, merging the aliases that share one.
type pathBuilder struct {
	fp      func([]byte) string
	harness HarnessFingerprinter
	store   provider.ConnectionStore
	conns   map[string]provider.Connection
	byKey   map[string]*modelcatalog.ExecutionPath
}

// add records the route profile p takes on backend. The route identity covers
// kind, backend, connection, route provider, tools and axes; the configuration
// revision adds the connection's stored revision and endpoint. Neither includes
// the model, so a model-only advancement keeps confirmed evidence valid.
func (b *pathBuilder) add(p provider.Profile, backend, mode string, kind provider.ProfileKind) error {
	var conn provider.Connection
	if p.ConnectionRef != "" {
		c, ok := b.conns[p.ConnectionRef]
		if !ok {
			return fmt.Errorf("connection %q: %w", p.ConnectionRef, provider.ErrConnectionNotFound)
		}
		conn = c
	}
	binding := routeBinding(p, backend, conn, b.store)
	binding.Model = ""
	route := provider.Canonicalize(binding.Provider)
	identity, err := json.Marshal(struct {
		Kind          string   `json:"kind"`
		BindingKind   string   `json:"binding_kind"`
		Mode          string   `json:"mode"`
		Backend       string   `json:"backend"`
		Connection    string   `json:"connection"`
		RouteProvider string   `json:"route_provider"`
		Tools         []string `json:"tools"`
		Context       string   `json:"context"`
		Effort        string   `json:"effort"`
	}{string(kind), string(p.Kind), mode, backend, p.ConnectionRef, route, p.AllowedTools, string(p.ContextWindow), string(p.Effort)})
	if err != nil {
		return err
	}
	harness, err := b.harness(harnessBackend(p, backend))
	if err != nil {
		return fmt.Errorf("harness fingerprint: %w", err)
	}
	config, err := json.Marshal(struct {
		Identity json.RawMessage   `json:"identity"`
		Revision int64             `json:"revision"`
		BaseURL  string            `json:"base_url"`
		Extras   map[string]string `json:"extras"`
		Harness  []byte            `json:"harness"`
	}{identity, conn.ConfigRevision, conn.BaseURL, conn.Extras, harness})
	if err != nil {
		return err
	}
	key := b.fp(append([]byte("path-key\x00"), identity...))
	cfg := b.fp(append([]byte("path-config\x00"), config...))
	if key == "" || cfg == "" {
		return errors.New("fingerprint produced an empty path revision")
	}

	path, ok := b.byKey[key]
	if !ok {
		path = &modelcatalog.ExecutionPath{
			Key: key, Kind: string(kind), Mode: mode, Backend: backend, ConnectionRef: p.ConnectionRef,
			RouteProvider: route, ConfigRevision: cfg, Binding: binding,
		}
		b.byKey[key] = path
	}
	if p.Alias != "" && !containsString(path.Aliases, p.Alias) {
		path.Aliases = append(path.Aliases, p.Alias)
	}
	return nil
}

// routeBinding retains actual connection metadata, never the follow target.
// Codex authenticates independently and must never reveal a connection key; LunaRoute bindings are guarded.
func routeBinding(p provider.Profile, backend string, conn provider.Connection, store provider.ConnectionStore) provider.ResolvedBinding {
	b := provider.ResolvedBinding{
		Kind: p.Kind, Provider: ProviderClaudeCLI, Model: p.Model, Backend: backend,
		ConnectionRef: p.ConnectionRef, BackendExplicit: strings.TrimSpace(p.Backend) != "",
		AllowedTools: slices.Clone(p.AllowedTools), ContextWindow: p.ContextWindow, Effort: p.Effort,
		RevealKey: func(context.Context) (string, error) { return "", nil },
	}
	if p.ConnectionRef != "" {
		b.Provider, b.BaseURL = conn.Provider, conn.BaseURL
		b.Extras = make(map[string]string, len(conn.Extras))
		for k, v := range conn.Extras {
			b.Extras[k] = v
		}
		if backend != workflow.BackendCodex {
			b.RevealKey = func(ctx context.Context) (string, error) { return store.Reveal(ctx, p.ConnectionRef) }
		}
	} else if backend == workflow.BackendCodex {
		b.Provider = ""
	}
	// The codex no-op reveal above fails closed for LunaRoute until its
	// authenticated backend path is enabled in phase 4.
	return provider.GuardLunaRouteReveal(b)
}

// harnessBackend names the harness binary a route runs: the agent backend, or
// the claude CLI for a chat profile with no connection. Connection-backed chat
// routes call an HTTP endpoint and have no local harness.
func harnessBackend(p provider.Profile, backend string) string {
	if backend != "" {
		return backend
	}
	if p.ConnectionRef == "" {
		return workflow.BackendClaudeCode
	}
	return ""
}

func (b *pathBuilder) paths() []modelcatalog.ExecutionPath {
	out := make([]modelcatalog.ExecutionPath, 0, len(b.byKey))
	for _, p := range b.byKey {
		sort.Strings(p.Aliases)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// pathDigestRows is the part of each path the snapshot digest covers.
func pathDigestRows(paths []modelcatalog.ExecutionPath) [][3]string {
	rows := make([][3]string, len(paths))
	for i, p := range paths {
		aliases, _ := json.Marshal(p.Aliases)
		rows[i] = [3]string{p.Key, p.ConfigRevision, string(aliases)}
	}
	return rows
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
