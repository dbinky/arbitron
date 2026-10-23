package modelcatalog

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrInventoryIncomplete marks an execution-path inventory that could not read
// one of its inputs. It is never reported as an empty (vacuously eligible) one.
var ErrInventoryIncomplete = errors.New("modelcatalog: execution-path inventory incomplete")

const (
	ExecutionModeBatch       = "batch"
	ExecutionModeInteractive = "interactive"
)

// ExecutionPath is one actual route a model alias executes on: the required
// kind, the effective agent backend (node > profile > deployment default), the
// connection, route provider and launch mode. Unsupported routes remain in the
// inventory; WithBinding reports their selector error without hiding other paths.
//
// Key identifies the route and survives credential rotation; ConfigRevision
// additionally covers the connection's stored revision and endpoint, so a
// rotated credential or changed launch configuration needs new proof while an
// edit to only the model, the profile revision or the tier policy keeps it.
// Binding is internal only and never carries key material beyond the lazy
// RevealKey closure. Its Kind is the profile's actual kind; a mismatch with the
// required Kind remains an explicit unsupported path, not an omitted consumer.
type ExecutionPath struct {
	Key            string
	Kind           string
	Mode           string
	Backend        string
	ConnectionRef  string
	RouteProvider  string
	ConfigRevision string
	Aliases        []string
	Binding        provider.ResolvedBinding
}

// PathSnapshot is one coherent inventory: every path in use, the digest
// (Revision) the gate rechecks before a commit, and the persisted revisions the
// commit transaction validates.
type PathSnapshot struct {
	Revision          string
	ExecutionRevision provider.ExecutionRevision
	Paths             []ExecutionPath
}

// WorkflowManifest is one on-disk workflow definition: its raw manifest bytes
// (hashed into the snapshot revision) and the parsed definition.
type WorkflowManifest struct {
	Scope    workflow.Scope
	Name     string
	Content  []byte
	Workflow *workflow.Workflow
}

// WorkflowScopeSource is the trusted, caller-independent workflow input of the
// inventory. ListScopes enumerates every scope on disk, including owners no
// registry lists; LoadManifests returns every workflow in a scope and fails,
// rather than skipping, on a manifest it cannot read or parse.
type WorkflowScopeSource interface {
	ListScopes(context.Context) ([]workflow.Scope, error)
	LoadManifests(context.Context, workflow.Scope) ([]WorkflowManifest, error)
}
