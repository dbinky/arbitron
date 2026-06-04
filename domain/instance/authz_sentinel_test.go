package instance

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
)

// The authz sentinels (ErrUnauthenticated/ErrForbidden) must be the SAME values
// as the canonical ones in domain/identity, so an errors.Is across the package
// boundary matches and the HTTP/MCP adapters map 401/403 correctly regardless of
// which domain service surfaced the failure.
func TestAuthzSentinels_AreCanonicalIdentityValues(t *testing.T) {
	t.Run("unauthenticated is identity.ErrUnauthenticated", func(t *testing.T) {
		if ErrUnauthenticated != identity.ErrUnauthenticated {
			t.Fatalf("instance.ErrUnauthenticated must equal identity.ErrUnauthenticated")
		}
		if !errors.Is(ErrUnauthenticated, identity.ErrUnauthenticated) {
			t.Fatalf("errors.Is must match across the package boundary")
		}
	})
	t.Run("forbidden is identity.ErrForbidden", func(t *testing.T) {
		if ErrForbidden != identity.ErrForbidden {
			t.Fatalf("instance.ErrForbidden must equal identity.ErrForbidden")
		}
		if !errors.Is(ErrForbidden, identity.ErrForbidden) {
			t.Fatalf("errors.Is must match across the package boundary")
		}
	})
}
