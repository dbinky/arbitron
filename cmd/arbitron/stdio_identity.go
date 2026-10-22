package main

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
)

// stdioContext is the identity producer for the stdio MCP transport. Stdio is
// not behind the HTTP auth middleware and carries no credential, so it is only
// authenticated where the process itself is the trust boundary: in local mode
// the operator who launched it is the seeded default user, resolved exactly as
// AuthMiddleware resolves a local HTTP request. In org and edge modes there is
// no credential to verify, so the context is returned unchanged and
// workflow.Service rejects authoring with ErrUnauthenticated rather than
// guessing a principal.
func (ds *domainStack) stdioContext(ctx context.Context) (context.Context, error) {
	if identity.ModeFromString(ds.cfg.Auth.Mode) != identity.ModeLocal {
		ds.logger.Warn("MCP stdio carries no credential outside local auth mode; authoring tools will be denied")
		return ctx, nil
	}
	uid, err := ds.userRepo.GetDefaultUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving local stdio identity: %w", err)
	}
	return identity.WithIdentity(ctx, identity.LocalAdminIdentity(uid, ds.groupCfg)), nil
}
