// Package provider defines the slim profile registry: the alias indirection
// and capability contract that node kinds resolve a backend through. Phase 2
// adds ConnectionRef + default params; Phase 1 is alias + kind + model only.
package provider

import (
	"context"
	"errors"
)

// ProfileKind is the capability contract a profile selects: a no-tool chat
// backend or a tool-using agent backend.
type ProfileKind string

const (
	// ProfileKindChat selects a ChatProvider backend (reasoning, no tools).
	ProfileKindChat ProfileKind = "chat"
	// ProfileKindAgent selects an AgentProvider backend (agentic, tools).
	ProfileKindAgent ProfileKind = "agent"
)

// Profile is the registry entry an alias maps to. Phase 1 carried only
// Alias/Kind/Model; Phase 2 widens it with ConnectionRef (the connection that
// supplies the endpoint + key; empty means the claude-cli no-key subscription
// path) and Params (per-profile defaults such as temperature/max-tokens).
type Profile struct {
	Alias         string
	Kind          ProfileKind    // chat | agent (unchanged from P1)
	ConnectionRef string         // -> Connection.Name; empty = claude-cli no-key (subscription)
	Model         string         // model-id, Azure deployment name, or provider/model for opencode
	Params        map[string]any // default temperature, max tokens, ...
}

// ResolvedBinding is the fully resolved backend selection for an alias. It is
// the seam Phase 3 adapters consume. RevealKey is lazy: it decrypts the
// connection's key only when invoked, at call/launch time.
type ResolvedBinding struct {
	Kind      ProfileKind
	Provider  string            // "claude-cli" for the no-connection subscription path
	BaseURL   string            //
	Extras    map[string]string //
	Model     string            //
	Params    map[string]any    //
	RevealKey func(ctx context.Context) (string, error) // nil-safe; no-key path returns ""
}

// ProfileResolver maps a node's alias to a fully-resolved ResolvedBinding,
// running the alias -> profile -> connection -> binding chain. Driven port; the
// engine and workflow validation depend on it, adapters implement it.
type ProfileResolver interface {
	Resolve(alias string) (ResolvedBinding, error)
}

// ErrUnknownProfile is returned by resolvers when an alias has no registry
// entry. Validation and executors match it with errors.Is.
var ErrUnknownProfile = errors.New("provider: unknown profile alias")
