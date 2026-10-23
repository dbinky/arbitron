package modelidentity

import (
	"context"
	"fmt"
	"reflect"

	"github.com/arbitron/arbitron/domain/identity"
)

// PolicyService is the admin-authorized face of the identity policy, plus the
// trusted internal read the mapper and refresh paths use.
type PolicyService struct {
	store PolicyStore
	gate  MutationGate
}

// NewPolicyService requires a real store; a missing dependency is a wiring error.
func NewPolicyService(store PolicyStore) *PolicyService {
	if store == nil {
		panic("modelidentity: nil policy store")
	}
	if v := reflect.ValueOf(store); v.Kind() == reflect.Pointer && v.IsNil() {
		panic("modelidentity: nil policy store")
	}
	return &PolicyService{store: store}
}

// SetMutationGate injects the shared execution gate. It must be called before
// any write; with no gate, Update fails rather than succeeding without it.
func (s *PolicyService) SetMutationGate(g MutationGate) { s.gate = g }

func requireAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return identity.ErrForbidden
	}
	return nil
}

// Get returns a copy of the current policy to an admin.
func (s *PolicyService) Get(ctx context.Context) (IdentityPolicy, error) {
	if err := requireAdmin(ctx); err != nil {
		return IdentityPolicy{}, err
	}
	return s.ReadInternal(ctx)
}

// Update replaces the whole rule set when expected is the current revision.
// An invalid, unauthorized or conflicting edit leaves the previous policy intact.
func (s *PolicyService) Update(ctx context.Context, expected int64, rules []PatternRule) (IdentityPolicy, error) {
	if err := requireAdmin(ctx); err != nil {
		return IdentityPolicy{}, err
	}
	if err := ValidateRules(rules); err != nil {
		return IdentityPolicy{}, err
	}
	normalized := NormalizeRules(rules)
	if s.gate == nil {
		return IdentityPolicy{}, ErrNoMutationGate
	}
	var stored IdentityPolicy
	err := s.gate.WithMutation(ctx, func(ctx context.Context) error {
		p, err := s.store.Replace(ctx, expected, normalized)
		if err != nil {
			return err
		}
		stored = p.clone()
		return nil
	})
	if err != nil {
		return IdentityPolicy{}, fmt.Errorf("updating identity policy: %w", err)
	}
	return stored, nil
}

// ReadInternal is the trusted read. A storage error is returned, never turned
// into an empty policy.
func (s *PolicyService) ReadInternal(ctx context.Context) (IdentityPolicy, error) {
	p, err := s.store.Snapshot(ctx)
	if err != nil {
		return IdentityPolicy{}, fmt.Errorf("reading identity policy: %w", err)
	}
	return p.clone(), nil
}

// Snapshot makes the service a PolicySource without a public registration.
func (s *PolicyService) Snapshot(ctx context.Context) (IdentityPolicy, error) {
	return s.ReadInternal(ctx)
}

var _ PolicySource = (*PolicyService)(nil)
