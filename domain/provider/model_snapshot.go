package provider

import (
	"context"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ModelPinSnapshot holds an owned coherent model map and any D8 native route
// exceptions. OpaqueRoutes never claims an immutable underlying model version.
// An absent exception means strict exact-model enforcement.
type ModelPinSnapshot struct {
	Models       map[string]string
	OpaqueRoutes map[string]string
	// OpaqueConnections holds, for every OpaqueRoutes alias, the profile
	// connection_ref captured with it. The connection is the D8 deployment
	// locator: a later profile edit to another connection must not move the run.
	OpaqueConnections map[string]string
}

// ModelSnapshotter captures, from ONE coherent generation of the profile
// registry, the specific version (or D8 native configured ID) each requested
// profile alias resolves to.
// The engine calls it once per trigger and persists the result on the
// instance, so every later node, retry, resume and child-input preparation of
// that run executes on the models pinned at trigger.
//
// wf is the exact workflow captured by this trigger. Route validation must
// use its nodes, never unrelated saved manifests or active runs. A nil workflow
// is an error, including when aliases is empty.
//
// Models holds exactly the requested aliases. OpaqueRoutes contains only pinned
// native Azure/Bedrock exceptions, keyed by alias with canonical route families.
// Unknown aliases, unsupported mutable models or store faults return an error
// and no partial snapshot.
type ModelSnapshotter interface {
	SnapshotModels(ctx context.Context, aliases []string, wf *workflow.Workflow) (ModelPinSnapshot, error)
}
