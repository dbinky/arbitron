package workflow

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// LocalRegistry is the Null-Object Registry for local/unconfigured (single-user)
// mode. It carries NO global identity store: there is no UUID registry, no
// cross-scope name uniqueness, and no archived flag. It exists so the admin
// lifecycle ops have ONE code path — the registry-coordinated path — with the
// local mode chosen ONCE at boot by injecting this Null-Object, rather than a
// nil-check scattered across every op.
//
// Contract by op (preserving the EXACT prior nil-registry behavior):
//
//   - The registry-COORDINATED admin ops (Rename, Archive/Unarchive,
//     TransferOwnership, Duplicate) detect local mode via isLocalRegistry and
//     short-circuit to "not configured" (ErrUnauthorized) BEFORE touching this
//     object — they are not available without a global identity store.
//   - Create / Publish / the startup backfills likewise short-circuit on local
//     mode and never reach this object.
//   - Delete is the ONE op that flows THROUGH the registry in both modes. Its
//     local semantics — delete the caller's private folder, mapping a shared-only
//     name to ErrSharedReadOnly and an unknown name to ErrNotFound — are
//     reproduced here in Resolve, so Delete needs no local-mode branch.
//
// Every id-targeted mutator (Register, Rename, SetArchived, SetOwner, Delete) and
// id->name lookup (ResolveName) is a no-op / not-found, because there is no
// durable identity to mutate or resolve. ResolveID and List report not-found /
// empty for the same reason. Only Resolve carries real local-mode logic.
type LocalRegistry struct {
	repo Repository
}

// Compile-time assertion that LocalRegistry satisfies the Registry port.
var _ Registry = (*LocalRegistry)(nil)

// NewLocalRegistry builds the Null-Object registry for local mode over the same
// definition repo the Service uses, so Delete's local scope resolution can probe
// folder existence.
func NewLocalRegistry(repo Repository) *LocalRegistry {
	return &LocalRegistry{repo: repo}
}

// isLocalRegistry reports whether r is the local-mode Null-Object (or absent).
// The registry-coordinated ops use it to preserve their prior nil-registry
// "local mode" behavior now that the field is unconditionally populated.
func isLocalRegistry(r Registry) bool {
	if r == nil {
		return true
	}
	_, ok := r.(*LocalRegistry)
	return ok
}

// localMode reports whether the service is running without a real identity
// registry (no registry wired, or the local-mode Null-Object wired).
func (s *Service) localMode() bool {
	return isLocalRegistry(s.registry)
}

// Resolve reproduces the prior nil-registry Delete scope resolution as a single
// lookup so Delete has no local-mode branch: a workflow that exists in the
// caller's private scope resolves to a private-scope entry (with an ephemeral id
// that the Null-Object Delete ignores); a name that exists ONLY in the shared
// scope maps to ErrSharedReadOnly (shared workflows are read-only in local mode,
// matching translateWrite); an unknown name is ErrNotFound.
func (r *LocalRegistry) Resolve(ctx context.Context, name string) (RegistryEntry, error) {
	id, err := caller(ctx)
	if err != nil {
		return RegistryEntry{}, err
	}
	priv := Private(id.UserID)
	exists, err := r.repo.Exists(ctx, priv, name)
	if err != nil {
		return RegistryEntry{}, err
	}
	if exists {
		// Ephemeral id: there is no durable registry row in local mode. Delete uses
		// only the scope; the id flows into the no-op LocalRegistry.Delete.
		return RegistryEntry{
			ID:      uuid.New(),
			Name:    name,
			Scope:   ScopePrivate,
			OwnerID: id.UserID,
		}, nil
	}
	// No private copy: map a shared-only name to ErrSharedReadOnly (mirrors
	// translateWrite), otherwise a plain not-found.
	sharedExists, sErr := r.repo.Exists(ctx, Shared(), name)
	if sErr != nil {
		return RegistryEntry{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if sharedExists {
		return RegistryEntry{}, fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
	}
	return RegistryEntry{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Register is a no-op success: local mode keeps no durable identity rows.
func (r *LocalRegistry) Register(context.Context, RegistryEntry) error { return nil }

// ResolveID always reports not-found: there is no durable name->id mapping.
func (r *LocalRegistry) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	return uuid.Nil, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// ResolveName always reports not-found: there is no durable id->name mapping.
func (r *LocalRegistry) ResolveName(_ context.Context, id uuid.UUID) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Rename is unreachable (the Rename op short-circuits on local mode); a no-op
// not-found keeps the Null-Object total.
func (r *LocalRegistry) Rename(_ context.Context, id uuid.UUID, _ string) error {
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

// SetArchived is unreachable (Archive short-circuits on local mode).
func (r *LocalRegistry) SetArchived(_ context.Context, id uuid.UUID, _ bool) error {
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

// SetOwner is unreachable (TransferOwnership short-circuits on local mode).
func (r *LocalRegistry) SetOwner(_ context.Context, id, _ uuid.UUID) error {
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Delete is a no-op success: there is no durable identity row to remove. The
// folder removal is the only durable effect of a local Delete.
func (r *LocalRegistry) Delete(context.Context, uuid.UUID) error { return nil }

// List returns no entries: local mode keeps no durable identity rows.
func (r *LocalRegistry) List(context.Context) ([]RegistryEntry, error) {
	return []RegistryEntry{}, nil
}
