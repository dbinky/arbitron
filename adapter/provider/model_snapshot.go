package provider

import (
	"context"
	"fmt"
	"reflect"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ModelSnapshotter implements provider.ModelSnapshotter over the live profile
// cache. It captures one profile generation, then validates each model on every
// actual route of the supplied workflow. The caller holds the execution read
// lease; this adapter must not reacquire it. No credential is revealed.
type ModelSnapshotter struct {
	cache    *ProfileCache
	store    provider.ConnectionStore
	versions provider.VersionResolver
	routes   provider.WorkflowVersionRequestSource
}

var _ provider.ModelSnapshotter = (*ModelSnapshotter)(nil)

// NewModelSnapshotter binds the coherent cache, metadata store, version resolver
// and route-only inventory. The store may be nil for subscription-only profiles.
// A nil cache is a construction fault, rejected before any trigger can run.
// Nil and typed-nil route sources both make SnapshotModels return an error,
// including for workflows with no model aliases.
func NewModelSnapshotter(cache *ProfileCache, store provider.ConnectionStore, versions provider.VersionResolver, routes provider.WorkflowVersionRequestSource) *ModelSnapshotter {
	if cache == nil {
		panic("provider: NewModelSnapshotter requires a non-nil profile cache")
	}
	if routes != nil {
		value := reflect.ValueOf(routes)
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				routes = nil
			}
		}
	}
	return &ModelSnapshotter{cache: cache, store: store, versions: versions, routes: routes}
}

// SnapshotModels captures each configured model and any native D8 exception,
// validating the complete route set before returning an owned snapshot.
func (s *ModelSnapshotter) SnapshotModels(ctx context.Context, aliases []string, wf *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	var snapshot provider.ModelPinSnapshot
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if wf == nil {
		return snapshot, fmt.Errorf("provider: model snapshot workflow is required")
	}
	if s.routes == nil {
		return snapshot, fmt.Errorf("provider: model snapshot route source is not configured")
	}
	if s.versions == nil {
		return snapshot, fmt.Errorf("provider: model snapshot: %w: version resolver", provider.ErrSelectionPortsMissing)
	}
	profiles, err := s.cache.SnapshotProfiles(aliases)
	if err != nil {
		return snapshot, err
	}
	out := provider.ModelPinSnapshot{
		Models:            make(map[string]string, len(profiles)),
		OpaqueRoutes:      make(map[string]string),
		OpaqueConnections: make(map[string]string),
	}
	for alias, p := range profiles {
		var routeProvider string
		if p.ConnectionRef != "" {
			if s.store == nil {
				return snapshot, fmt.Errorf("provider: profile %q references connection %q but no connection store is configured", alias, p.ConnectionRef)
			}
			conn, err := s.store.Get(ctx, p.ConnectionRef)
			if err != nil {
				return snapshot, fmt.Errorf("provider: profile %q references connection %q: %w", alias, p.ConnectionRef, err)
			}
			routeProvider = conn.Provider
		}
		requests, err := s.routes.VersionRequestsForWorkflow(ctx, p, wf)
		if err != nil {
			return snapshot, fmt.Errorf("profile %q routes: %w", alias, err)
		}
		if len(requests) == 0 {
			return snapshot, fmt.Errorf("profile %q: no execution routes for model snapshot", alias)
		}
		for _, req := range requests {
			if req.Model != p.Model || req.ConnectionRef != p.ConnectionRef || p.ConnectionRef != "" && req.RouteProvider != routeProvider {
				return snapshot, fmt.Errorf("profile %q: route does not match captured profile", alias)
			}
		}
		versions := &snapshotVersionResolver{delegate: s.versions}
		opaque, err := provider.ValidatePinRoutes(ctx, p, requests, versions)
		if err != nil {
			return snapshot, fmt.Errorf("profile %q: %w", alias, err)
		}
		if opaque != "" {
			if p.ConnectionRef == "" {
				return snapshot, fmt.Errorf("profile %q: native route exception requires a connection", alias)
			}
			out.Models[alias] = p.Model
			out.OpaqueRoutes[alias] = opaque
			out.OpaqueConnections[alias] = p.ConnectionRef
		} else {
			out.Models[alias] = versions.model
		}
	}
	return out, nil
}

// snapshotVersionResolver retains the executable spelling proved on each strict
// route in the shared validation pass. All routes must agree on one referent.
type snapshotVersionResolver struct {
	delegate provider.VersionResolver
	model    string
}

func (v *snapshotVersionResolver) SpecificVersion(ctx context.Context, req provider.VersionRequest) (string, error) {
	model, err := v.delegate.SpecificVersion(ctx, req)
	if err != nil {
		return "", err
	}
	if model == "" || v.model != "" && model != v.model {
		return "", fmt.Errorf("routes do not resolve to one specific model")
	}
	v.model = model
	return model, nil
}
