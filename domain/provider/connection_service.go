package provider

import "context"

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
	store ConnectionStore
}

// NewConnectionService wraps the existing ConnectionStore with the admin gate.
func NewConnectionService(store ConnectionStore) *ConnectionService {
	return &ConnectionService{store: store}
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
func (s *ConnectionService) Create(ctx context.Context, c Connection, apiKey string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.store.Create(ctx, c, apiKey)
}

// Update changes a connection's metadata (admin-only). A nil apiKey preserves the
// stored key; a non-nil one re-seals and replaces it.
func (s *ConnectionService) Update(ctx context.Context, c Connection, apiKey *string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.store.Update(ctx, c, apiKey)
}

// Delete removes a connection by name (admin-only).
func (s *ConnectionService) Delete(ctx context.Context, name string) error {
	if err := requireAdmin(ctx); err != nil {
		return err
	}
	return s.store.Delete(ctx, name)
}
