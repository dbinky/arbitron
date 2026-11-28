package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// rdPeer is a non-admin user who owns no runs. REST calls go through the
// production router (ds.httpDeps()) with org-mode bearer authentication, so
// the identity comes from a real stored token, never from an injected
// context. ctx carries the same identity for MCP calls.
type rdPeer struct {
	ctx    context.Context
	router http.Handler
	token  string
}

func newRDPeer(t *testing.T, ds *domainStack, username string) *rdPeer {
	t.Helper()
	user := &instance.User{ID: uuid.New(), Username: username, Role: identity.RoleUser}
	require.NoError(t, ds.userRepo.Create(adminCtxForLocalUser(t, ds), user))
	peerCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: user.ID, Username: username, Role: identity.RoleUser})
	token, _, err := ds.tokenSvc.Mint(peerCtx, "runner-detail")
	require.NoError(t, err)
	deps := ds.httpDeps()
	deps.Auth = adapthttp.NewAuthMiddleware(identity.ModeOrg, "session", ds.sessionRepo, ds.tokenRepo, ds.userRepo, identity.GroupConfig{DefaultGroup: "default"})
	t.Cleanup(deps.Auth.Stop)
	return &rdPeer{ctx: peerCtx, router: adapthttp.NewRouterFromDeps(deps), token: token}
}

// get performs one authenticated GET as the peer.
func (p *rdPeer) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	w := httptest.NewRecorder()
	p.router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// rdPrivate makes run id private: only its owner and admins can view it.
func rdPrivate(t *testing.T, ds *domainStack, id uuid.UUID) {
	t.Helper()
	_, err := ds.db.Exec(`UPDATE instances SET visibility = 'private' WHERE id = ?`, id.String())
	require.NoError(t, err)
}
