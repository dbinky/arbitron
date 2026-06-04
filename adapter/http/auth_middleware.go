package http

import (
	"net/http"
	"net/url"
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
	cookie   string                     // session cookie name (org mode)
	sessions identity.SessionRepository // nil in local mode
	users    instance.UserRepository

	// local-mode default identity, resolved once (the row never changes).
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
			return identity.Identity{}, false
		}
		return m.localID, true
	}

	// org mode: session cookie only (the bearer-token branch is Phase 2).
	c, err := r.Cookie(m.cookie)
	if err != nil || c.Value == "" {
		return identity.Identity{}, false
	}
	sess, err := m.sessions.Get(r.Context(), c.Value)
	if err != nil {
		// ErrNotFound (absent/expired) or any DB error -> unauthenticated.
		return identity.Identity{}, false
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
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
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
