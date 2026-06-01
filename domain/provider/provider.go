// Package provider defines the slim profile registry: the alias indirection
// and capability contract that node kinds resolve a backend through. Phase 2
// adds ConnectionRef + default params; Phase 1 is alias + kind + model only.
package provider

import "errors"

// ProfileKind is the capability contract a profile selects: a no-tool chat
// backend or a tool-using agent backend.
type ProfileKind string

const (
	// ProfileKindChat selects a ChatProvider backend (reasoning, no tools).
	ProfileKindChat ProfileKind = "chat"
	// ProfileKindAgent selects an AgentProvider backend (agentic, tools).
	ProfileKindAgent ProfileKind = "agent"
)

// Profile is the slim Phase 1 shape: an alias mapped to a capability kind and a
// model-id passed to the resolved backend.
type Profile struct {
	Alias string
	Kind  ProfileKind
	Model string // model-id passed to the resolved backend
}

// ProfileResolver maps a node's alias to a Profile. Driven port; the engine and
// workflow validation depend on it, adapters implement it.
type ProfileResolver interface {
	Resolve(alias string) (Profile, error)
}

// ErrUnknownProfile is returned by resolvers when an alias has no registry
// entry. Validation and executors match it with errors.Is.
var ErrUnknownProfile = errors.New("provider: unknown profile alias")
