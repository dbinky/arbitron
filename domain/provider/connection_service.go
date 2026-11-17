package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrConnectionInUse is returned by ConnectionService.Delete when one or more
// profiles still reference the connection by name. The schema deliberately has no
// FK (so a profile may be authored before its connection exists — create-before-
// reference is a feature), so this reverse-direction invariant is enforced here in
// the domain service, fail-closed, the way the FK would have been on delete.
// Callers match it with errors.Is.
var ErrConnectionInUse = errors.New("connection in use by profiles")

// ProfileLister is the read-only port ConnectionService uses to enforce the
// delete-time in-use check: it lists profiles so Delete can reject removing a
// connection any profile still references. It is intentionally a single-method
// consumer-side interface (not the full ProfileStore) so the connection service
// depends only on the read it needs; the sqlite ProfileRepo and the in-memory
// stores satisfy it directly.
type ProfileLister interface {
	List(ctx context.Context) ([]Profile, error)
}

// ConnectionService is a thin admin gate over the ConnectionStore so connection
// MUTATION and metadata READS become admin-only (spec §6). Every method requires
// an authenticated admin; a non-admin is ErrForbidden and a missing identity is
// ErrUnauthenticated (the shared requireAdmin helper from authz.go). It reuses
// the same authz rule as ProfileService so MCP and any future REST surface never
// drift.
//
// Reveal is deliberately NOT exposed here: the plaintext key decryption path
// stays internal to the runtime resolver, which keeps using the raw store with
// no authz on the engine path. Only the management handlers move to this service.
type ConnectionService struct {
	store    ConnectionStore
	profiles ProfileLister
	gate     *ExecutionGate
}

// SetMutationGate injects the shared execution gate. Create, Update,
// UpdatePatch and Delete then hold its exclusive lease for the whole store write,
// so a connection edit can never interleave with a model advancement or a run's
// snapshot capture. A nil gate (unit tests of the pure admin rule) writes
// directly; the root wiring test proves production injects one.
func (s *ConnectionService) SetMutationGate(g *ExecutionGate) { s.gate = g }

// mutate runs fn under the exclusive execution lease when a gate is injected.
func (s *ConnectionService) mutate(ctx context.Context, fn func(context.Context) error) error {
	if s.gate == nil {
		return fn(ctx)
	}
	return s.gate.WithMutation(ctx, fn)
}

// NewConnectionService wraps the existing ConnectionStore with the admin gate.
// The ProfileLister backs the delete-time in-use check (the reverse-direction
// invariant the no-FK schema leaves to the domain).
func NewConnectionService(store ConnectionStore, profiles ProfileLister) *ConnectionService {
	return &ConnectionService{store: store, profiles: profiles}
}

// List returns all connections' metadata (admin-only). The store returns
// metadata only — no key material.
func (s *ConnectionService) List(ctx context.Context) ([]Connection, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// Get returns one connection's metadata by name (admin-only).
func (s *ConnectionService) Get(ctx context.Context, name string) (Connection, error) {
	if err := requireAdmin(ctx); err != nil {
		return Connection{}, err
	}
	return s.store.Get(ctx, name)
}

// Create persists a new connection (admin-only), forwarding the plaintext key to
// the store, which seals it immediately. The key is never retained here.
//
// The pure create rules (provider-specific shape, LunaRoute key shape and hosted
// endpoint normalization) are applied here, so the accepted data and the
// canonical connection the store receives do not depend on which store is
// injected. c is taken by value and only an error is returned: the canonical row
// is read back from the store by the caller that needs it. The store re-applies
// the same rules itself (SQLite does, ahead of sealing and the URL policy), so a
// store used without this service stays protected.
func (s *ConnectionService) Create(ctx context.Context, c Connection, apiKey string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	if err := ValidateConnection(c); err != nil {
		return err
	}
	c, err := PrepareLunaRouteCreate(c, apiKey)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(ctx context.Context) error { return s.store.Create(ctx, c, apiKey) })
}

// Update changes a connection's metadata (admin-only). A nil apiKey preserves the
// stored key; a non-nil one re-seals and replaces it. It is a FULL replace of the
// metadata (including extras); UpdatePatch is the merge-on-update path that
// preserves omitted extras.
func (s *ConnectionService) Update(ctx context.Context, c Connection, apiKey *string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.mutate(ctx, func(ctx context.Context) error { return s.store.Update(ctx, c, apiKey) })
}

// UpdatePatch is the merge-on-update path for a connection (admin-only). When
// extras is nil ("absent" at the boundary) the stored extras are PRESERVED rather
// than blanked; a non-nil extras replaces the map (an explicit set, including an
// empty map to clear). A nil apiKey preserves the sealed key as before.
//
// SECURITY: this closes the extras-full-replace footgun on the credential record.
// The sealed api_key is correctly merge-preserved on Update, but a partial update
// that omitted extras previously blanked load-bearing non-secret metadata
// (bedrock access-key-id/region, azure api-version) while keeping the encrypted
// key — desyncing a still-valid credential from the routing/identity it must be
// paired with. The merge lives HERE in the service so MCP and a future REST
// surface inherit it, mirroring the authz gate and the profile merge.
func (s *ConnectionService) UpdatePatch(ctx context.Context, c Connection, extras *map[string]string, apiKey *string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	// The read-merge-write runs under one lease so the merged extras are never
	// stale relative to a concurrent advancement or snapshot.
	return s.mutate(ctx, func(ctx context.Context) error {
		if extras != nil {
			c.Extras = *extras
		} else {
			// Absent extras: preserve the stored map so a partial update never desyncs
			// the sealed key from its non-secret credential metadata.
			existing, err := s.store.Get(ctx, c.Name)
			if err != nil {
				return err
			}
			c.Extras = existing.Extras
		}
		return s.store.Update(ctx, c, apiKey)
	})
}

// Delete removes a connection by name (admin-only). It is fail-closed against
// orphaning: if any profile still references the connection by name, the delete
// is rejected with ErrConnectionInUse naming the referencing profiles, so the
// operator learns at delete time rather than at a far-removed workflow-run/
// validate failure. The no-FK schema keeps create-before-reference working; this
// service enforces the delete-while-referenced invariant the FK would have.
func (s *ConnectionService) Delete(ctx context.Context, name string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.mutate(ctx, func(ctx context.Context) error {
		refs, err := s.referencingProfiles(ctx, name)
		if err != nil {
			return err
		}
		if len(refs) > 0 {
			return fmt.Errorf("%w: connection %q in use by profiles [%s]", ErrConnectionInUse, name, strings.Join(refs, ", "))
		}
		return s.store.Delete(ctx, name)
	})
}

// referencingProfiles returns the sorted aliases of profiles whose ConnectionRef
// equals name. A lister error fails closed (the delete is aborted) so a transient
// read failure never lets an in-use connection be removed.
func (s *ConnectionService) referencingProfiles(ctx context.Context, name string) ([]string, error) {
	profiles, err := s.profiles.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking profiles referencing connection %q: %w", name, err)
	}
	var refs []string
	for _, p := range profiles {
		if p.ConnectionRef == name {
			refs = append(refs, p.Alias)
		}
	}
	sort.Strings(refs)
	return refs, nil
}
