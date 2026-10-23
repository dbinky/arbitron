package provider

import (
	"context"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// VersionResolver resolves a profile's model to the fixed version a run pins.
// The contract lives in modelidentity so that package imports no provider code.
type VersionResolver = modelidentity.VersionResolver

// VersionRequest is the non-secret input to VersionResolver.
type VersionRequest = modelidentity.VersionRequest

// VersionRequestSource describes every actual execution route of a prospective
// profile, including deployment defaults and node overrides. Implementations
// read routing metadata only, never tier policy or discovery, and use the actual
// connection provider rather than the catalog follow target.
// Successful results must be non-empty; unresolved routes are errors.
type VersionRequestSource interface {
	VersionRequests(context.Context, Profile) ([]modelidentity.VersionRequest, error)
}

// WorkflowVersionRequestSource validates routes for one trigger's workflow.
// It must not enumerate saved workflows or active runs. A nil workflow fails.
type WorkflowVersionRequestSource interface {
	VersionRequestsForWorkflow(context.Context, Profile, *workflow.Workflow) ([]modelidentity.VersionRequest, error)
}

// ErrSpecificVersionUnavailable is returned for a mutable alias or opaque
// deployment that names no fixed version.
var ErrSpecificVersionUnavailable = modelidentity.ErrSpecificVersionUnavailable

// VersionRequestFor builds the request from one coherent profile plus the route
// provider its connection resolves to ("" for the no-connection subscription
// path). Credentials and Extras never enter the request.
func VersionRequestFor(p Profile, routeProvider string) VersionRequest {
	return VersionRequest{
		Model:         p.Model,
		RouteProvider: routeProvider,
		ConnectionRef: p.ConnectionRef,
		Backend:       EffectiveBackend(p),
	}
}
