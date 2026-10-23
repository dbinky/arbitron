package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// validatePinnedRoute repeats the policy-independent fixed-version check on the
// actual live route, after backend precedence has been applied. Trigger-time
// validation cannot cover later connection/backend edits. This is a preflight
// guard, not a replacement for the adapter's exact execution evidence check.
func validatePinnedRoute(b provider.ResolvedBinding, backend string) error {
	if !b.Pinned {
		return nil
	}
	if b.OpaquePinRoute != "" {
		if backend == "" && b.Model != "" &&
			(b.OpaquePinRoute == provider.CanonicalAzure || b.OpaquePinRoute == provider.CanonicalBedrock) &&
			provider.Canonicalize(b.Provider) == b.OpaquePinRoute {
			return nil
		}
		return errors.New("provider: pinned native route exception is incompatible with live route")
	}
	req := modelidentity.VersionRequest{Model: b.Model, RouteProvider: b.Provider, Backend: backend}
	if backend == workflow.BackendCodex {
		// Codex always uses its subscription account, ignoring the connection.
		req.RouteProvider = ""
	}
	// The resolver is a pure grammar check: selectors have no request context,
	// and this call performs no discovery, policy lookup or credential access.
	if _, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), req); err != nil {
		return fmt.Errorf("provider: pinned model is incompatible with live route: %w", err)
	}
	return nil
}
