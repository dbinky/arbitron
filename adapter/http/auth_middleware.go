package http

import (
	"context"
	"errors"
	"fmt"
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

// CfAccessJWTHeader is the request header Cloudflare Access sets with the
// edge-issued identity JWT, consulted only in edge mode. Exported so the
// composition root (main.go) wires the same canonical header into the edge
// middleware constructor.
const CfAccessJWTHeader = "Cf-Access-Jwt-Assertion"

// emailProvisioner is the tiny provisioning port the edge branch depends on so
// the middleware does not import the concrete *instance.Provisioner type beyond
// what it needs. *instance.Provisioner satisfies it.
type emailProvisioner interface {
	ResolveByEmail(ctx context.Context, subject, email string, groups []string) (*instance.User, error)
}

// Compile-time assertion that the concrete provisioner satisfies the port.
var _ emailProvisioner = (*instance.Provisioner)(nil)

// orgDeps holds the fields consulted ONLY in org mode (session-cookie + bearer
// token auth). It is non-nil exactly when mode == ModeOrg, making the per-mode
// invariant explicit: a reader (and the wire-time check) sees which fields
// belong to org mode, and a half-wired constructor is obvious at the call site.
type orgDeps struct {
	cookie   string                     // session cookie name
	sessions identity.SessionRepository // session store
	tokens   identity.TokenRepository   // personal API tokens (bearer auth)
	// toucher coalesces per-request last-used bookkeeping into a single owned,
	// cancellable flusher goroutine (bearer path only). Nil disables it.
	toucher *touchFlusher
}

// edgeDeps holds the fields consulted ONLY in edge mode (stateless per-request
// verification of a Cloudflare Access JWT). Non-nil exactly when mode ==
// ModeEdge.
type edgeDeps struct {
	header      string
	verifier    identity.EdgeVerifier
	provisioner emailProvisioner
}

// localDeps holds the runtime state for local mode: the single default identity,
// resolved once (the row never changes). Non-nil exactly when mode == ModeLocal.
type localDeps struct {
	once sync.Once
	id   identity.Identity
	err  error
}

// AuthMiddleware enforces authentication for every request surface. It is the
// single authz boundary: REST, WS, and MCP all pass through it, so domain
// services can trust identity.FromContext unconditionally.
//
// The mode-specific fields live in named sub-structs (org/edge/local), exactly
// one of which is non-nil per the active mode. This makes the nil-invariant the
// two constructors rely on explicit and self-documenting, and a wire-time check
// (assertModeWired) turns a half-wired mode into a construction-time panic
// rather than a nil-deref on the request path.
type AuthMiddleware struct {
	mode   identity.Mode
	users  instance.UserRepository
	groups identity.GroupConfig // RBAC: default group + leads mapping

	org   *orgDeps   // non-nil iff mode == ModeOrg
	edge  *edgeDeps  // non-nil iff mode == ModeEdge
	local *localDeps // non-nil iff mode == ModeLocal
}

// assertModeWired enforces the per-mode field invariant at construction time: it
// panics if the active mode's sub-struct (and its required ports) is not wired.
// A panic here is a programmer/wiring error surfaced at startup, never a
// request-path nil-deref. It is the wire-time fallback the named sub-structs
// document.
func (m *AuthMiddleware) assertModeWired() {
	switch m.mode {
	case identity.ModeEdge:
		if m.edge == nil || m.edge.verifier == nil || m.edge.provisioner == nil {
			panic("auth: edge mode requires a verifier and provisioner")
		}
	case identity.ModeOrg:
		if m.org == nil || m.org.sessions == nil {
			panic("auth: org mode requires a session repository")
		}
	case identity.ModeLocal:
		if m.local == nil {
			panic("auth: local mode requires local state")
		}
	default:
		panic(fmt.Sprintf("auth: unknown mode %q", m.mode))
	}
	if m.users == nil {
		panic("auth: a user repository is required in every mode")
	}
}

// NewAuthMiddleware builds the middleware. In local mode sessions/tokens may be
// nil (the bearer branch is org-mode only). tokens powers the
// "Authorization: Bearer arb_..." branch for browserless clients.
func NewAuthMiddleware(mode identity.Mode, cookieName string, sessions identity.SessionRepository, tokens identity.TokenRepository, users instance.UserRepository, groups identity.GroupConfig) *AuthMiddleware {
	m := &AuthMiddleware{mode: mode, users: users, groups: groups}
	switch mode {
	case identity.ModeOrg:
		od := &orgDeps{cookie: cookieName, sessions: sessions, tokens: tokens}
		// The bearer last-used flusher is only relevant in org mode with a token repo.
		if tokens != nil {
			od.toucher = newTouchFlusher(tokens, time.NewTicker(touchFlushInterval).C)
		}
		m.org = od
	case identity.ModeLocal:
		m.local = &localDeps{}
	}
	m.assertModeWired()
	return m
}

// NewEdgeAuthMiddleware builds the middleware for edge mode (auth.mode ==
// "edge"). Verification is stateless per request: the headerName header (e.g.
// "Cf-Access-Jwt-Assertion") is verified by verifier, the resulting email is
// provisioned by prov, and the user is enriched into the request identity. No
// session or token repository is consulted in edge mode, so they are nil. users
// powers enrich's ListDepartments; groups supplies the default-group fallback.
func NewEdgeAuthMiddleware(headerName string, verifier identity.EdgeVerifier, prov emailProvisioner, users instance.UserRepository, groups identity.GroupConfig) *AuthMiddleware {
	m := &AuthMiddleware{
		mode:   identity.ModeEdge,
		users:  users,
		groups: groups,
		edge: &edgeDeps{
			header:      headerName,
			verifier:    verifier,
			provisioner: prov,
		},
	}
	m.assertModeWired()
	return m
}

// Stop shuts down the owned last-used flusher goroutine, draining any pending
// touches synchronously. Safe to call when no flusher was started (no-op). Wire
// this into the server lifecycle (main.go) so the goroutine is not leaked.
func (m *AuthMiddleware) Stop() {
	if m.org != nil && m.org.toucher != nil {
		m.org.toucher.Stop()
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
	if m.mode == identity.ModeEdge {
		raw := r.Header.Get(m.edge.header)
		if raw == "" {
			// No token at all: fail closed (401). Do NOT consult the verifier.
			return identity.Identity{}, errNoCredential
		}
		edgeID, verr := m.edge.verifier.Verify(r.Context(), raw)
		if verr != nil {
			// Invalid/forged/expired token: fail closed. NOT a server error — do
			// not log the raw token; the verifier already wrapped a safe error.
			return identity.Identity{}, errNoCredential
		}
		if edgeID.Email == "" {
			// Defense-in-depth: a conforming EdgeVerifier never returns an empty
			// email with a nil error, but if a broken one does, the empty email is a
			// credential failure (401), not a server fault (500).
			return identity.Identity{}, errNoCredential
		}
		// Thread the verified subject + group claim into provisioning so the user is
		// scoped to the department(s) their SSO group resolves to and bootstrap-admin
		// matching prefers the stable verified subject. A nil/empty Groups (no claim)
		// preserves the default-group fallback; an empty Subject falls back to email.
		user, perr := m.edge.provisioner.ResolveByEmail(r.Context(), edgeID.Subject, edgeID.Email, edgeID.Groups)
		if perr != nil {
			// Provisioning/DB failure is a SERVER fault, not an auth denial: surface a
			// non-errNoCredential error so Handler maps it to 500.
			return identity.Identity{}, fmt.Errorf("edge: provisioning %q: %w", edgeID.Email, perr)
		}
		// No session cookie or bearer token is consulted in edge mode (the
		// session/token repos are nil). enrich is mode-agnostic.
		return m.enrich(r.Context(), user), nil
	}

	if m.mode == identity.ModeLocal {
		m.local.once.Do(func() {
			uid, err := m.users.GetDefaultUser(r.Context())
			if err != nil {
				m.local.err = err
				// Log ONCE (guarded by sync.Once): the default identity could not be
				// established, so local mode will fail every request until restart.
				slog.Error("auth: could not resolve local default user", "error", err)
				return
			}
			// Local mode: the single user is admin in the default group, so all
			// RBAC predicates pass trivially and today's behavior is preserved.
			m.local.id = identity.LocalAdminIdentity(uid, m.groups)
		})
		if m.local.err != nil {
			// Server-state failure, NOT a missing-credential 401.
			return identity.Identity{}, m.local.err
		}
		return m.local.id, nil
	}

	// org mode. Credentials are evaluated in order: a present-and-valid session
	// cookie wins; otherwise a present bearer token; otherwise reject. A request
	// may carry either — never both required.

	// --- Session-cookie branch (browser only) ---
	if c, err := r.Cookie(m.org.cookie); err == nil && c.Value != "" {
		if sess, serr := m.org.sessions.Get(r.Context(), c.Value); serr == nil {
			if user, uerr := m.users.GetByID(r.Context(), sess.UserID); uerr == nil {
				return m.enrich(r.Context(), user), nil
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
		if _, ok := identity.ParsePrefix(raw); ok && m.org.tokens != nil {
			hash := identity.HashToken(raw)
			if tok, terr := m.org.tokens.GetByHash(r.Context(), hash); terr == nil {
				if user, uerr := m.users.GetByID(r.Context(), tok.UserID); uerr == nil {
					// Best-effort, non-blocking last-used bookkeeping. Instead of a
					// per-request fire-and-forget goroutine (unbounded, unowned), we
					// coalesce the touch into a map flushed by one owned, cancellable
					// goroutine (see touchFlusher). Never delays or fails the request.
					if m.org.toucher != nil {
						m.org.toucher.touch(tok.ID)
					}
					return m.enrich(r.Context(), user), nil
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
// In edge mode there is no Arbitron login page (auth happens at the Cloudflare
// Access edge), so the redirect is suppressed — returning 401 directly instead.
// A redirect to /auth/login in edge mode would loop: /auth/login is never
// mounted when authHandler is nil, so the browser would re-hit the middleware
// and redirect again indefinitely.
func (m *AuthMiddleware) reject(w http.ResponseWriter, r *http.Request) {
	if m.mode != identity.ModeEdge && isBrowserNavigation(r) {
		http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

// enrich builds the request identity for an org-mode user: it loads the user's
// persisted departments, unions them with the config-derived led departments,
// and stamps the (already live-evaluated) role. Subject/email drive lead
// matching only — they are never placed on the Identity. A ListDepartments
// failure is non-fatal: the caller still authenticates with their led
// departments (plus, defensively, the default group), so a transient read error
// does not lock a user out — but it is logged.
func (m *AuthMiddleware) enrich(ctx context.Context, u *instance.User) identity.Identity {
	subject := ""
	if u.Subject != nil {
		subject = *u.Subject
	}
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	stored, err := m.users.ListDepartments(ctx, u.ID)
	if err != nil {
		slog.Error("auth: could not load user departments", "user", u.ID, "error", err)
		stored = nil
	}
	// Build the runtime Identity via the single shared domain builder so the
	// request path and the scheduler and spawn paths (userIdentityResolver) can never
	// diverge on the field set — in particular LedDepartments, an authz input.
	return identity.BuildIdentity(identity.UserIdentityInput{
		UserID:            u.ID,
		Username:          u.Username,
		Role:              u.Role,
		Subject:           subject,
		Email:             email,
		StoredDepartments: stored,
	}, m.groups)
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
