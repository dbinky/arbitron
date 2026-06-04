# Phase 1, Task 6: Authentication Middleware — Local-Mode Injection + Org-Mode Session Branch

## Goal

Create `adapter/http/auth_middleware.go` providing one mode-aware chi middleware that replaces the `TODO(phase-6)` block in `adapter/http/server.go`. In **local mode** it injects a fixed default-admin `identity.Identity` into every request context (current behavior preserved exactly — no cookie, no rejection). In **org mode** it reads the session cookie, looks the session up via `SessionRepository`, loads the user via `UserRepository`, and injects that `Identity`; an absent/invalid/expired credential yields `401` for API/MCP/WS and a redirect to `/auth/login` for browser-navigation routes. Unauthenticated requests never reach a handler.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §4.

## Dependencies

- Task 1 (config) — `cfg.Auth.Mode`, `cfg.Auth.Session.CookieName`.
- Task 2 (`domain/identity`) — `Identity`, `WithIdentity`, `SessionRepository`, `RoleAdmin`.
- Task 3 (`UserRepository` expansion) — `GetDefaultUser`, `GetByID` map a session to an `Identity`.
- Task 4 (SessionRepo) — the concrete session lookup behind the port.

(Task 5/OIDC is NOT required here — the middleware is on the request hot path and consults only the session store, never the IdP, per the pinned credential model.)

## Background (current state)

- `adapter/http/server.go` `NewRouterFromDeps` wires global middleware (`Recoverer`, `RequestID`, `RealIP`, `securityHeaders`) then a `TODO(phase-6)` comment (lines ~127–131) where auth belongs, then mounts `/api/v1`, `/mcp`, and the dashboard `NotFound`. `RouterDeps` is the dependency bundle; it carries `Config *config.Config` but no identity/session deps yet.
- `WriteError(w, status, code, msg)` is the house JSON error writer (used throughout `server.go`/`stream_handler.go`).
- `stream_handler.go` documents the absence of any per-request identity today; this middleware is what introduces it. The WS upgrade handlers (`streamHandler.HandleWebSocket`, `sysStreamHandler.HandleWebSocket`) run as normal chi handlers under `/api/v1`, so a middleware mounted at the top of the `/api/v1` group ALSO covers the WS upgrade (the cookie is sent on the upgrade GET). The `/mcp` mount is a sibling of `/api/v1`, so it must be wrapped separately (Task 8 does the mounting; this task delivers the middleware constructor and a `WrapHandler` helper).
- Default-user resolution: `UserRepository.GetDefaultUser(ctx)` returns the fixed `local` admin UUID. Local mode resolves it ONCE and caches it (the row never changes).

## Files to Create/Modify

### Create: `adapter/http/auth_middleware.go`

```go
package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// AuthMiddleware enforces authentication for every request surface. It is the
// single authz boundary: REST, WS, and MCP all pass through it, so domain
// services can trust identity.FromContext unconditionally.
type AuthMiddleware struct {
	mode     identity.Mode
	cookie   string                       // session cookie name (org mode)
	sessions identity.SessionRepository   // nil in local mode
	users    instance.UserRepository

	// local-mode default identity, resolved once.
	localOnce sync.Once
	localID   identity.Identity
	localErr  error
}

// NewAuthMiddleware builds the middleware. In local mode sessions may be nil.
func NewAuthMiddleware(mode identity.Mode, cookieName string, sessions identity.SessionRepository, users instance.UserRepository) *AuthMiddleware {
	return &AuthMiddleware{mode: mode, cookie: cookieName, sessions: sessions, users: users}
}

// Handler is the chi-compatible middleware. Mount it at the top of /api/v1 and
// wrap the /mcp handler with it (Task 8).
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := m.resolve(r)
		if !ok {
			m.reject(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), id)))
	})
}

// resolve returns the caller identity for the request, or ok=false to reject.
func (m *AuthMiddleware) resolve(r *http.Request) (identity.Identity, bool) {
	if m.mode == identity.ModeLocal {
		m.localOnce.Do(func() {
			uid, err := m.users.GetDefaultUser(r.Context())
			if err != nil {
				m.localErr = err
				return
			}
			m.localID = identity.Identity{UserID: uid, Username: "local", Role: identity.RoleAdmin}
		})
		if m.localErr != nil {
			return identity.Identity{}, false // 500-ish; reject() maps below
		}
		return m.localID, true
	}

	// org mode: session cookie only (bearer token branch is Phase 2).
	c, err := r.Cookie(m.cookie)
	if err != nil || c.Value == "" {
		return identity.Identity{}, false
	}
	sess, err := m.sessions.Get(r.Context(), c.Value)
	if err != nil {
		return identity.Identity{}, false // ErrNotFound (absent/expired) or DB error -> unauth
	}
	user, err := m.users.GetByID(r.Context(), sess.UserID)
	if err != nil {
		return identity.Identity{}, false
	}
	return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, true
}

// reject writes 401 for API/MCP/WS callers and redirects browser navigations to
// the login flow. A request is treated as a browser navigation when it is a GET
// that Accepts text/html and is not under /api or /mcp.
func (m *AuthMiddleware) reject(w http.ResponseWriter, r *http.Request) {
	if isBrowserNavigation(r) {
		http.Redirect(w, r, "/auth/login?next="+urlQueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

func isBrowserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
```

(`urlQueryEscape` = `url.QueryEscape`; import `net/url`. Keep the `context` import if used; trim unused imports.)

### Modify: `adapter/http/server.go`

1. Add to `RouterDeps`:

```go
// Auth, when non-nil, is mounted at the top of /api/v1 and wraps the /mcp
// handler so REST, WS, and MCP all inherit one authz boundary. Nil disables
// auth (legacy/tests) — production wiring always sets it (Task 8).
Auth *AuthMiddleware
```

2. Replace the `TODO(phase-6)` comment block (lines ~127–131) with mounting the middleware on the `/api/v1` group. Since the group is created via `r.Route("/api/v1", ...)`, apply the middleware as the first `r.Use(...)` inside that closure:

```go
r.Route("/api/v1", func(r chi.Router) {
	if d.Auth != nil {
		r.Use(d.Auth.Handler) // covers REST + the WS upgrade GETs in this group
	}
	r.Get("/ws", sysStreamHandler.HandleWebSocket)
	// ... existing routes ...
})
```

3. Wrap the `/mcp` mount with the same middleware (the MCP mount is OUTSIDE the `/api/v1` group, so it needs explicit wrapping):

```go
if d.MCPHandler != nil {
	mcp := d.MCPHandler
	if d.Auth != nil {
		mcp = d.Auth.Handler(mcp)
	}
	r.Mount("/mcp", mcp)
}
```

4. Update the stale WARNING comment above the `/mcp` mount and the `stream_handler.go` security note to reflect that auth now covers these surfaces in org mode (leave a note that per-user *visibility* scoping is Phase 5/later of Phase 6, distinct from authentication). The `/auth/*` routes (Task 7) are registered OUTSIDE the `/api/v1` group and the dashboard `NotFound`, so they are not behind `d.Auth`.

## Testing (strict TDD — five scenario categories)

Tests in `adapter/http/auth_middleware_test.go` using `httptest` + a fake `SessionRepository`/`UserRepository`. Write first.

- **Happy path (local)** — `TestAuthMiddleware_Local_InjectsAdmin`: a local-mode middleware over a handler that reads `identity.MustFromContext` returns `Role==RoleAdmin`, `UserID==defaultUUID`; no cookie is required; status 200. Confirms current behavior preserved.
- **Happy path (org)** — `TestAuthMiddleware_Org_ValidSession`: a fake session repo returns a session for the cookie token; the user repo returns a `user`-role user; the handler sees `Identity{UserID, Username, Role==RoleUser}`; status 200.
- **Input validation** — `TestAuthMiddleware_Org_NoCookie_401`: org mode, no cookie → `401 UNAUTHORIZED`, handler never invoked (assert a flag). `TestAuthMiddleware_Org_EmptyCookieValue_401`.
- **Error/failure** — `TestAuthMiddleware_Org_ExpiredSession_401`: session repo returns `identity.ErrNotFound` (expired/absent) → 401. `TestAuthMiddleware_Org_UserGone_401`: session valid but `GetByID` returns `instance.ErrNotFound` → 401 (a deleted user cannot ride a stale session).
- **Boundary** — `TestAuthMiddleware_Org_BrowserNav_Redirects`: a `GET /` with `Accept: text/html` and no cookie → `302` to `/auth/login?next=%2F`; a `GET /api/v1/instances` with no cookie → `401` (API path never redirects); a `GET /api/v1/instances/{id}/ws` (WS upgrade) with no cookie → `401`.
- **Isolation/security** — `TestAuthMiddleware_Org_ForgedCookie_NeverAuthenticates`: a cookie value that hashes to no stored session → 401 (the fake repo returns `ErrNotFound` for unknown tokens, mirroring the real hash-lookup). `TestAuthMiddleware_Local_NeverReadsCookie`: in local mode, a request carrying a bogus session cookie is STILL admin (local mode ignores cookies entirely) — proves the modes don't cross-contaminate. Add a surface-spanning assertion that a handler behind the middleware can call `identity.MustFromContext` without panicking (the middleware always sets identity before `next`).

## Acceptance Criteria

- `AuthMiddleware` injects a fixed default-admin identity in local mode (no credential) and a session-derived identity in org mode.
- Org-mode missing/expired/forged credential → 401 for API/MCP/WS, 302→`/auth/login` for browser navigation; the wrapped handler never runs unauthenticated.
- The middleware is mounted at the top of `/api/v1` and wraps the `/mcp` handler in `server.go`; `/auth/*` stays outside it.
- The legacy "no authentication" WARNING/security comments are updated.
- `go build ./...` and `go test ./adapter/http/...` pass.
