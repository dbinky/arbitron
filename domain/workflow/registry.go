package workflow

import (
	"context"

	"github.com/google/uuid"
)

// RegistryEntry is one row in the workflow identity registry: the stable id, the
// human-facing name (globally unique), the scope it lives in, its owner (the zero
// UUID for a shared entry), and whether it is archived.
type RegistryEntry struct {
	ID       uuid.UUID
	Name     string
	Scope    ScopeKind
	OwnerID  uuid.UUID // zero (uuid.Nil) for shared
	Archived bool
}

// WorkflowRegistry is the authoritative source of workflow identity and global
// name uniqueness. The filesystem definition store stays name-keyed and holds the
// definition body; the registry resolves name↔id and enforces UNIQUE(name).
//
// Register and Rename return ErrAlreadyExists when the target name is already
// held by another entry. ResolveID, ResolveName, and the id-targeted mutators
// (Rename, SetArchived, SetOwner, Delete) return ErrNotFound when no entry
// matches.
type WorkflowRegistry interface {
	// Register inserts a new identity row. Returns ErrAlreadyExists if the name
	// is already registered.
	Register(ctx context.Context, e RegistryEntry) error
	// ResolveID returns the id registered for name, or ErrNotFound.
	ResolveID(ctx context.Context, name string) (uuid.UUID, error)
	// ResolveName returns the current name for id, or ErrNotFound.
	ResolveName(ctx context.Context, id uuid.UUID) (string, error)
	// Rename changes the name of the entry id. Returns ErrAlreadyExists if
	// newName is already held by another entry, or ErrNotFound if id is unknown.
	Rename(ctx context.Context, id uuid.UUID, newName string) error
	// SetArchived sets the archived flag of the entry id, or ErrNotFound.
	SetArchived(ctx context.Context, id uuid.UUID, archived bool) error
	// SetOwner reassigns the owner of the entry id, or ErrNotFound.
	SetOwner(ctx context.Context, id, owner uuid.UUID) error
	// Delete removes the entry id, or ErrNotFound.
	Delete(ctx context.Context, id uuid.UUID) error
	// List returns all registered entries.
	List(ctx context.Context) ([]RegistryEntry, error)
}
