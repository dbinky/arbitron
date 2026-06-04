package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// AuthMiddleware enforces authentication for every request surface. It is the
// single authz boundary: REST, WS, and MCP all pass through it, so domain
// services can trust identity.FromContext unconditionally.
type AuthMiddleware struct {
	mode     identity.Mode
	cookie   string                     // session cookie name (org mode)
	sessions identity.SessionRepository // nil in local mode
	tokens   identity.TokenRepository   // personal API tokens (org-mode bearer auth)
	users    instance.UserRepository

	// local-mode default identity, resolved once (the row never changes).
	localOnce sync.Once
	localID   identity.Identity
	localErr  error
}

// NewAuthMiddleware builds the middleware. In local mode sessions/tokens may be
// nil (the bearer branch is org-mode only). tokens powers the
// "Authorization: Bearer arb_..." branch for browserless clients.
func NewAuthMiddleware(mode identity.Mode, cookieName string, sessions identity.SessionRepository, tokens identity.TokenRepository, users instance.UserRepository) *AuthMiddleware {
	return &AuthMiddleware{mode: mode, cookie: cookieName, sessions: sessions, tokens: tokens, users: users}
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

	// org mode. Credentials are evaluated in order: a present-and-valid session
	// cookie wins; otherwise a present bearer token; otherwise reject. A request
	// may carry either — never both required.

	// --- Session-cookie branch (browser only) ---
	if c, err := r.Cookie(m.cookie); err == nil && c.Value != "" {
		if sess, serr := m.sessions.Get(r.Context(), c.Value); serr == nil {
			if user, uerr := m.users.GetByID(r.Context(), sess.UserID); uerr == nil {
				return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, true
			}
		}
		// A present-but-invalid cookie falls through to the bearer attempt below;
		// a stale cookie must not block a valid Authorization header.
	}

	// --- Bearer-token branch (REST / MCP / WS; Phase 2) ---
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		raw := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		// Cheap pre-filter: only arb_<prefix>_<secret> shapes are ours, so a
		// non-arb header never hits the DB (no timing oracle on prefix existence).
		if _, ok := identity.ParsePrefix(raw); ok && m.tokens != nil {
			hash := identity.HashToken(raw)
			if tok, terr := m.tokens.GetByHash(r.Context(), hash); terr == nil {
				if user, uerr := m.users.GetByID(r.Context(), tok.UserID); uerr == nil {
					// best-effort, async, non-blocking; never delays or fails the
					// request. A revoked/absent token already fell through to 401.
					go func(tid uuid.UUID) {
						_ = m.tokens.TouchLastUsed(context.Background(), tid)
					}(tok.ID)
					return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, true
				}
			}
		}
		// A malformed/revoked/garbage bearer (or a non-arb scheme) falls through
		// to the rejection below — never silently treated as anonymous-allowed.
	}

	return identity.Identity{}, false
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
