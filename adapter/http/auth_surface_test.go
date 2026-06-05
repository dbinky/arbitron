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
	auth := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	return buildSurfaceRouter(auth, nil, mcp), mcp
}

func orgSurface(t *testing.T) (http.Handler, *fakeSessionRepo, *fakeUserRepo, *surfaceMCP) {
	t.Helper()
	users := newFakeUserRepo()
	sessions := newFakeSessionRepo()
	mcp := &surfaceMCP{}
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	authHandler := NewAuthHandler(oidc, sessions, users, instance.NewProvisioner(users, nil, identity.GroupConfig{DefaultGroup: "default"}), config.SessionConfig{TTLSeconds: 3600, CookieName: "arbitron_session"})
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

// Security: the unauthenticated /auth/* group is rate-limited per client IP, so
// a flood from one IP is throttled (429) once it exceeds the per-minute budget.
func TestSurface_AuthRoutes_RateLimitedPerIP(t *testing.T) {
	router, _, _, _ := orgSurface(t)
	const ip = "203.0.113.7"

	var got429 bool
	// One request over the budget must trip the limiter.
	for i := 0; i < authRateLimitPerMinute+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
		req.Header.Set("X-Real-IP", ip) // RealIP middleware keys the limiter on this
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	assert.True(t, got429, "exceeding the per-IP budget on /auth/* must return 429")
}

// A different client IP must not be throttled by another IP's burst (per-IP, not
// global).
func TestSurface_AuthRoutes_RateLimitIsPerIP(t *testing.T) {
	router, _, _, _ := orgSurface(t)

	// Exhaust IP A's budget.
	for i := 0; i < authRateLimitPerMinute+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
		req.Header.Set("X-Real-IP", "203.0.113.10")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	// IP B's first request must still pass (redirect to IdP).
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	req.Header.Set("X-Real-IP", "203.0.113.11")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusFound, rec.Code, "a distinct IP must not be throttled by another IP's burst")
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

// TestSurface_OrgMode_Bearer_E2E pins the bearer-token path through the full chi
// router + auth middleware — the surface tests above use nil tokens (session-only
// paths), so this is the only test that proves bearer reaches REST+MCP and that a
// revoked token is immediately rejected at the router seam.
func TestSurface_OrgMode_Bearer_E2E(t *testing.T) {
	users := newFakeUserRepo()
	sessions := newFakeSessionRepo()
	tokens := newFakeTokenRepo()
	mcp := &surfaceMCP{}

	_, plaintext := seedBearer(t, users, tokens, identity.RoleUser)

	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, tokens, users, identity.GroupConfig{DefaultGroup: "default"})
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	authHandler := NewAuthHandler(oidc, sessions, users, instance.NewProvisioner(users, nil, identity.GroupConfig{DefaultGroup: "default"}),
		config.SessionConfig{TTLSeconds: 3600, CookieName: "arbitron_session"})
	router := buildSurfaceRouter(auth, authHandler, mcp)

	t.Run("valid bearer passes REST and MCP", func(t *testing.T) {
		// REST: /api/v1/health is the lightest authenticated endpoint.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "valid bearer must pass REST through the router")

		// MCP: handler must be reached (auth doesn't block).
		mcp.reached = false
		req2 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		req2.Header.Set("Authorization", "Bearer "+plaintext)
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		assert.Equal(t, http.StatusOK, rec2.Code, "valid bearer must pass /mcp through the router")
		assert.True(t, mcp.reached, "MCP tool handler must be reached with a valid bearer")
	})

	t.Run("revoked bearer fails closed on REST and MCP", func(t *testing.T) {
		// Simulate revocation: clear the fake token store (same effect as
		// TokenService.Revoke → TokenRepo.Delete → GetByHash returns ErrNotFound).
		tokens.byHash = map[string]*identity.Token{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "revoked bearer must be 401 on REST")

		mcp.reached = false
		req2 := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		req2.Header.Set("Authorization", "Bearer "+plaintext)
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, req2)
		assert.Equal(t, http.StatusUnauthorized, rec2.Code, "revoked bearer must be 401 on /mcp")
		assert.False(t, mcp.reached, "MCP handler must not run after token revocation")
	})
}

// Wiring smoke: ModeFromString + middleware construction match what main.go does.
func TestSurface_ModeWiring(t *testing.T) {
	users := newFakeUserRepo()
	assert.Equal(t, identity.ModeLocal, identity.ModeFromString("local"))
	assert.Equal(t, identity.ModeOrg, identity.ModeFromString("org"))

	// A local middleware injects an admin without any session repo.
	mw := NewAuthMiddleware(identity.ModeFromString("local"), "arbitron_session", nil, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)
	require.True(t, probe.called)
	assert.True(t, probe.id.IsAdmin())
	_ = context.Background()
}
