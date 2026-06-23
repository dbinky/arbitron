package workflow

import "github.com/google/uuid"

// ScopeKind distinguishes a caller's private workspace from the shared
// (published) space.
type ScopeKind int

const (
	// ScopePrivate addresses basePath/users/<OwnerID>/<name>.
	ScopePrivate ScopeKind = iota
	// ScopeShared addresses basePath/shared/<name>; OwnerID is ignored.
	ScopeShared
)

// Scope selects the storage root a Repository operation acts on.
// For ScopeShared the OwnerID field is ignored.
type Scope struct {
	Kind    ScopeKind
	OwnerID uuid.UUID
}

// Private builds a Scope addressing the given owner's private workspace.
func Private(ownerID uuid.UUID) Scope { return Scope{Kind: ScopePrivate, OwnerID: ownerID} }

// Shared is the shared/published scope.
func Shared() Scope { return Scope{Kind: ScopeShared} }
