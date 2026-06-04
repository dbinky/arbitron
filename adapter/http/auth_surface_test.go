package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// surfaceMCP is a stub MCP handler that records whether it was reached -- proving
// the middleware rejects anonymous /mcp calls BEFORE any tool runs.
type surfaceMCP struct{ reached bool }

func (m *surfaceMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.reached = true
	w.WriteHeader(http.StatusOK)
}

// buildSurfaceRouter constructs a router with the given auth middleware and a
// stub MCP handler. It deliberately omits handler-heavy deps: the surfaces we
// exercise (the inline /health closure, the /mcp stub, the WS upgrade GET, and
// /auth/*) do not require a full repo stack, and anonymous org-mode requests are
// rejected at the middleware before any handler is invoked.
func buildSurfaceRouter(auth *AuthMiddleware, authHandler *AuthHandler, mcp http.Handler) http.Handler {
	// A stub dashboard so unmatched routes (browser navigations) fall through to
	// it rather than chi's default 404 -- this is what the SPA does in production.
	dash := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>dashboard</html>"))
	})
	return NewRouterFromDeps(RouterDeps{
		Config:           &config.Config{},
		Auth:             auth,
		AuthHandler:      authHandler,
		MCPHandler:       mcp,
		DashboardHandler: dash,
	})
}

func localSurface(t *testing.T) (http.Handler, *surfaceMCP) {
	t.Helper()
	users := newFakeUserRepo()
	mcp := &surfaceMCP{}
	auth := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, users)
	return buildSurfaceRouter(auth, nil, mcp), mcp
}

func orgSurface(t *testing.T) (http.Handler, *fakeSessionRepo, *fakeUserRepo, *surfaceMCP) {
	t.Helper()
	users := newFakeUserRepo()
	sessions := newFakeSessionRepo()
	mcp := &surfaceMCP{}
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, users)
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	authHandler := NewAuthHandler(oidc, sessions, users, config.SessionConfig{TTLSeconds: 3600, CookieName: "arbitron_session"})
	return buildSurfaceRouter(auth, authHandler, mcp), sessions, users, mcp
}

func TestSurface_LocalMode_AllPassThrough(t *testing.T) {
	router, mcp := localSurface(t)

	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("mcp", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, mcp.reached, "local mode passes the MCP call through")
	})
}

func TestSurface_OrgMode_ValidSession_AllPassThrough(t *testing.T) {
	router, sessions, users, mcp := orgSurface(t)
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "bob", Role: identity.RoleUser}
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid}
	cookie := &http.Cookie{Name: "arbitron_session", Value: "tok"}

	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("mcp", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, mcp.reached)
	})
}

func TestSurface_AuthRoutesOutsideGuard(t *testing.T) {
	router, _, _, _ := orgSurface(t)

	t.Run("login reachable without session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code, "login redirects to IdP, never 401")
	})

	t.Run("logout reachable without session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("callback reachable (bad state -> 400 from handler, not 401)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=y", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestSurface_OrgMode_Anonymous_AllRejected(t *testing.T) {
	router, _, _, mcp := orgSurface(t)

	t.Run("rest health 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("mcp 401, tool never reached", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, mcp.reached, "anonymous MCP must be rejected before any tool runs")
	})

	t.Run("ws upgrade 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/abc/ws", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestSurface_OrgMode_BrowserNavRedirects(t *testing.T) {
	router, _, _, _ := orgSurface(t)

	t.Run("html nav redirects", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, "/auth/login?next=%2F", rec.Header().Get("Location"))
	})

	t.Run("api never redirects", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestSurface_MCP_DestructiveTools_RequireAuth(t *testing.T) {
	router, _, _, mcp := orgSurface(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workflow_delete"}}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, mcp.reached, "destructive MCP tools are gated by auth")
}

// Wiring smoke: ModeFromString + middleware construction match what main.go does.
func TestSurface_ModeWiring(t *testing.T) {
	users := newFakeUserRepo()
	assert.Equal(t, identity.ModeLocal, identity.ModeFromString("local"))
	assert.Equal(t, identity.ModeOrg, identity.ModeFromString("org"))

	// A local middleware injects an admin without any session repo.
	mw := NewAuthMiddleware(identity.ModeFromString("local"), "arbitron_session", nil, users)
	probe := &identityProbe{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)
	require.True(t, probe.called)
	assert.True(t, probe.id.IsAdmin())
	_ = context.Background()
}
