package http

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// errNoCredential signals that the request carried no usable credential, so the
// caller must be rejected with 401. Any OTHER non-nil error from resolve is a
// server-state failure (e.g. the local default identity could not be
// established) and maps to 500 — distinguishing "you are not authenticated"
// from "we could not check".
var errNoCredential = errors.New("auth: no credential")

// AuthMiddleware enforces authentication for every request surface. It is the
// single authz boundary: REST, WS, and MCP all pass through it, so domain
// services can trust identity.FromContext unconditionally.
type AuthMiddleware struct {
	mode     identity.Mode
	cookie   string                     // session cookie name (org mode)
	sessions identity.SessionRepository // nil in local mode
	tokens   identity.TokenRepository   // personal API tokens (org-mode bearer auth)
	users    instance.UserRepository

	// toucher coalesces per-request last-used bookkeeping into a single owned,
	// cancellable flusher goroutine (org-mode bearer path only). Nil disables it.
	toucher *touchFlusher

	// local-mode default identity, resolved once (the row never changes).
	localOnce sync.Once
	localID   identity.Identity
	localErr  error
}

// NewAuthMiddleware builds the middleware. In local mode sessions/tokens may be
// nil (the bearer branch is org-mode only). tokens powers the
// "Authorization: Bearer arb_..." branch for browserless clients.
func NewAuthMiddleware(mode identity.Mode, cookieName string, sessions identity.SessionRepository, tokens identity.TokenRepository, users instance.UserRepository) *AuthMiddleware {
	m := &AuthMiddleware{mode: mode, cookie: cookieName, sessions: sessions, tokens: tokens, users: users}
	// The bearer last-used flusher is only relevant in org mode with a token repo.
	if mode == identity.ModeOrg && tokens != nil {
		m.toucher = newTouchFlusher(tokens, time.NewTicker(touchFlushInterval).C)
	}
	return m
}

// Stop shuts down the owned last-used flusher goroutine, draining any pending
// touches synchronously. Safe to call when no flusher was started (no-op). Wire
// this into the server lifecycle (main.go) so the goroutine is not leaked.
func (m *AuthMiddleware) Stop() {
	if m.toucher != nil {
		m.toucher.Stop()
	}
}

// Handler is the chi-compatible middleware. Mount it at the top of /api/v1 and
// wrap the /mcp handler with it (Task 8).
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.resolve(r)
		if err != nil {
			if errors.Is(err, errNoCredential) {
				m.reject(w, r)
				return
			}
			// Server-state failure (e.g. cannot establish the local default
			// identity): this is a 500, not a 401. WriteError logs it.
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not establish caller identity")
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), id)))
	})
}

// resolve returns the caller identity for the request, or an error: errNoCredential
// (map to 401) when no usable credential is present, or any other error (map to
// 500) for a server-state failure that prevents establishing identity.
func (m *AuthMiddleware) resolve(r *http.Request) (identity.Identity, error) {
	if m.mode == identity.ModeLocal {
		m.localOnce.Do(func() {
			uid, err := m.users.GetDefaultUser(r.Context())
			if err != nil {
				m.localErr = err
				// Log ONCE (guarded by sync.Once): the default identity could not be
				// established, so local mode will fail every request until restart.
				slog.Error("auth: could not resolve local default user", "error", err)
				return
			}
			m.localID = identity.Identity{UserID: uid, Username: "local", Role: identity.RoleAdmin}
		})
		if m.localErr != nil {
			// Server-state failure, NOT a missing-credential 401.
			return identity.Identity{}, m.localErr
		}
		return m.localID, nil
	}

	// org mode. Credentials are evaluated in order: a present-and-valid session
	// cookie wins; otherwise a present bearer token; otherwise reject. A request
	// may carry either — never both required.

	// --- Session-cookie branch (browser only) ---
	if c, err := r.Cookie(m.cookie); err == nil && c.Value != "" {
		if sess, serr := m.sessions.Get(r.Context(), c.Value); serr == nil {
			if user, uerr := m.users.GetByID(r.Context(), sess.UserID); uerr == nil {
				return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, nil
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
					// Best-effort, non-blocking last-used bookkeeping. Instead of a
					// per-request fire-and-forget goroutine (unbounded, unowned), we
					// coalesce the touch into a map flushed by one owned, cancellable
					// goroutine (see touchFlusher). Never delays or fails the request.
					if m.toucher != nil {
						m.toucher.touch(tok.ID)
					}
					return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, nil
				}
			}
		}
		// A malformed/revoked/garbage bearer (or a non-arb scheme) falls through
		// to the rejection below — never silently treated as anonymous-allowed.
	}

	return identity.Identity{}, errNoCredential
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
