package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- fakes ----

type fakeUserRepo struct {
	defaultID uuid.UUID
	byID      map[uuid.UUID]*instance.User
	bySubject map[string]*instance.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{defaultID: uuid.New(), byID: map[uuid.UUID]*instance.User{}}
}

func (r *fakeUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return r.defaultID, nil }

func (r *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, instance.ErrNotFound
}

func (r *fakeUserRepo) GetBySubject(_ context.Context, subject string) (*instance.User, error) {
	if subject == "" {
		return nil, instance.ErrNotFound
	}
	if u, ok := r.bySubject[subject]; ok {
		return u, nil
	}
	return nil, instance.ErrNotFound
}

func (r *fakeUserRepo) Create(_ context.Context, u *instance.User) error {
	r.byID[u.ID] = u
	if u.Subject != nil {
		if r.bySubject == nil {
			r.bySubject = map[string]*instance.User{}
		}
		r.bySubject[*u.Subject] = u
	}
	return nil
}

func (r *fakeUserRepo) UpdateUsername(_ context.Context, id uuid.UUID, username string) error {
	if u, ok := r.byID[id]; ok {
		u.Username = username
	}
	return nil
}

func (r *fakeUserRepo) List(_ context.Context) ([]instance.User, error) {
	out := make([]instance.User, 0, len(r.byID))
	for _, u := range r.byID {
		out = append(out, *u)
	}
	return out, nil
}

func (r *fakeUserRepo) UpdateRole(_ context.Context, id uuid.UUID, role string) error {
	if u, ok := r.byID[id]; ok {
		u.Role = identity.Role(role)
	}
	return nil
}

func (r *fakeUserRepo) CountAdmins(_ context.Context) (int, error) {
	n := 0
	for _, u := range r.byID {
		if u.Role == identity.RoleAdmin {
			n++
		}
	}
	return n, nil
}

type fakeSessionRepo struct {
	byToken map[string]*identity.Session
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{byToken: map[string]*identity.Session{}}
}

func (r *fakeSessionRepo) Create(_ context.Context, s *identity.Session) error {
	r.byToken[s.ID] = s
	return nil
}

func (r *fakeSessionRepo) Get(_ context.Context, id string) (*identity.Session, error) {
	if s, ok := r.byToken[id]; ok {
		return s, nil
	}
	return nil, identity.ErrNotFound
}

func (r *fakeSessionRepo) Delete(_ context.Context, id string) error {
	delete(r.byToken, id)
	return nil
}

func (r *fakeSessionRepo) DeleteExpired(context.Context, time.Time) (int, error) {
	return 0, nil
}

// fakeTokenRepo is an in-memory identity.TokenRepository for middleware tests.
// It records GetByHash call counts (to prove the ParsePrefix pre-filter) and
// signals TouchLastUsed via a channel so the async best-effort path is testable.
type fakeTokenRepo struct {
	byHash     map[string]*identity.Token
	getByHashN int
	touchErr   error
	touchedIDs chan uuid.UUID
}

func newFakeTokenRepo() *fakeTokenRepo {
	return &fakeTokenRepo{byHash: map[string]*identity.Token{}, touchedIDs: make(chan uuid.UUID, 4)}
}

func (r *fakeTokenRepo) Create(_ context.Context, t *identity.Token) error {
	r.byHash[t.TokenHash] = t
	return nil
}

func (r *fakeTokenRepo) ListByUser(_ context.Context, _ uuid.UUID) ([]identity.Token, error) {
	return nil, nil
}

func (r *fakeTokenRepo) GetByHash(_ context.Context, hash string) (*identity.Token, error) {
	r.getByHashN++
	if t, ok := r.byHash[hash]; ok {
		return t, nil
	}
	return nil, identity.ErrNotFound
}

func (r *fakeTokenRepo) Delete(_ context.Context, id, _ uuid.UUID) error {
	for h, t := range r.byHash {
		if t.ID == id {
			delete(r.byHash, h)
			return nil
		}
	}
	return identity.ErrNotFound
}

func (r *fakeTokenRepo) TouchLastUsed(_ context.Context, id uuid.UUID) error {
	r.touchedIDs <- id
	return r.touchErr
}

// identityProbe is a handler that records the identity it observed.
type identityProbe struct {
	called bool
	id     identity.Identity
}

func (p *identityProbe) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.called = true
		p.id = identity.MustFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}
}

// errUserRepo wraps fakeUserRepo but returns an error from GetDefaultUser,
// simulating a DB that is unavailable at startup in local mode.
type errUserRepo struct{ *fakeUserRepo }

func (r *errUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) {
	return uuid.Nil, errors.New("db unavailable")
}

// ---- tests ----

func TestAuthMiddleware_Local_InjectsAdmin(t *testing.T) {
	users := newFakeUserRepo()
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, users.defaultID, probe.id.UserID)
	assert.Equal(t, identity.RoleAdmin, probe.id.Role)
}

func TestAuthMiddleware_Org_ValidSession(t *testing.T) {
	users := newFakeUserRepo()
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "bob", Role: identity.RoleUser}
	sessions := newFakeSessionRepo()
	sessions.byToken["good-token"] = &identity.Session{ID: "good-token", UserID: uid}

	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "good-token"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, uid, probe.id.UserID)
	assert.Equal(t, "bob", probe.id.Username)
	assert.Equal(t, identity.RoleUser, probe.id.Role)
}

func TestAuthMiddleware_Org_NoCookie_401(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo())
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "handler must never run unauthenticated")
}

func TestAuthMiddleware_Org_EmptyCookieValue_401(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo())
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: ""})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
}

func TestAuthMiddleware_Org_ExpiredSession_401(t *testing.T) {
	// fake session repo returns ErrNotFound for unknown/expired tokens.
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo())
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "expired"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
}

func TestAuthMiddleware_Org_UserGone_401(t *testing.T) {
	users := newFakeUserRepo() // no users registered
	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uuid.New()}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "a deleted user cannot ride a stale session")
}

func TestAuthMiddleware_Org_BrowserNav_Redirects(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo())
	probe := &identityProbe{}
	h := mw.Handler(probe.handler())

	t.Run("html GET redirects to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, "/auth/login?next=%2F", rec.Header().Get("Location"))
	})

	t.Run("api GET returns 401 not redirect", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Accept", "text/html") // even with html accept, /api never redirects
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("ws upgrade returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/abc/ws", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("/mcp with html accept returns 401 not redirect", func(t *testing.T) {
		// The MCP path must never redirect to login — MCP clients do not follow
		// redirects and would silently send credentials to the wrong endpoint.
		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Empty(t, rec.Header().Get("Location"), "/mcp must not redirect")
	})

	t.Run("POST with html accept returns 401 not redirect", func(t *testing.T) {
		// isBrowserNavigation returns false for any non-GET, so form POSTs to
		// browser paths must never receive a redirect — they would lose the
		// request body and confuse the client.
		req := httptest.NewRequest(http.MethodPost, "/workflows", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Empty(t, rec.Header().Get("Location"), "POST must not redirect")
	})

	t.Run("deep-link with query params fully encoded in next", func(t *testing.T) {
		// A user deep-linking to /instances/abc?tab=logs before auth must be
		// redirected back to that exact URL after login. RequestURI() returns the
		// path+query, and url.QueryEscape encodes both ? and & so the next
		// parameter is a single valid query value. The auth handler's sanitizeNext
		// decodes it and verifies it starts with /.
		req := httptest.NewRequest(http.MethodGet, "/instances/abc?tab=logs&page=2", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t,
			"/auth/login?next=%2Finstances%2Fabc%3Ftab%3Dlogs%26page%3D2",
			rec.Header().Get("Location"),
			"query parameters in the original URL must be fully encoded into the next param",
		)
	})
}

func TestAuthMiddleware_Org_ForgedCookie_NeverAuthenticates(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo())
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "forged-value-hashes-to-nothing"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
}

func TestAuthMiddleware_Local_NeverReadsCookie(t *testing.T) {
	users := newFakeUserRepo()
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "bogus"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, identity.RoleAdmin, probe.id.Role, "local mode ignores cookies entirely")
}

// TestAuthMiddleware_Local_DefaultUserError_FailsClosed verifies that if
// GetDefaultUser returns an error (e.g. DB unavailable at startup), local mode
// fails closed: every request gets 401 rather than a panic or a 500.  The
// sync.Once caches the error permanently, so subsequent requests also fail.
func TestAuthMiddleware_Local_DefaultUserError_FailsClosed(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, &errUserRepo{newFakeUserRepo()})
	probe := &identityProbe{}

	for i := range 3 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "request %d must be rejected", i)
		assert.False(t, probe.called, "handler must never run when GetDefaultUser fails")
	}
}

// ---- bearer-token branch (Phase 2) ----

// seedBearer registers user U and a token owned by U, returning the plaintext.
func seedBearer(t *testing.T, users *fakeUserRepo, tokens *fakeTokenRepo, role identity.Role) (uuid.UUID, string) {
	t.Helper()
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "carol", Role: role}
	plaintext, prefix, hash, err := identity.GenerateToken()
	require.NoError(t, err)
	tokens.byHash[hash] = &identity.Token{ID: uuid.New(), UserID: uid, Label: "cli", Prefix: prefix, TokenHash: hash}
	return uid, plaintext
}

// Happy path: a valid bearer token resolves to the owning user's identity.
func TestAuthMiddleware_Bearer_HappyPath(t *testing.T) {
	users := newFakeUserRepo()
	tokens := newFakeTokenRepo()
	uid, plaintext := seedBearer(t, users, tokens, identity.RoleUser)

	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, uid, probe.id.UserID)
	assert.Equal(t, "carol", probe.id.Username)
	assert.Equal(t, identity.RoleUser, probe.id.Role)

	// TouchLastUsed runs exactly once and names the matched token's ID.
	hash := identity.HashToken(plaintext)
	select {
	case touchedID := <-tokens.touchedIDs:
		assert.Equal(t, tokens.byHash[hash].ID, touchedID, "TouchLastUsed must be called with the matched token's ID")
	case <-time.After(time.Second):
		t.Fatal("TouchLastUsed was never invoked")
	}
}

// Input validation: non-arb / non-Bearer / missing headers → 401 with NO DB hit
// for the non-arb shapes (proves the ParsePrefix pre-filter).
func TestAuthMiddleware_Bearer_Validation(t *testing.T) {
	for _, hdr := range []string{"Bearer not-an-arb-token", "Basic abc123", ""} {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header %q", hdr)
		assert.False(t, probe.called)
		assert.Equal(t, 0, tokens.getByHashN, "non-arb header %q must not hit GetByHash", hdr)
	}
}

// Error/failure: a well-shaped arb token whose hash is absent → 401; a token
// whose user is gone → 401. Never 500.
func TestAuthMiddleware_Bearer_ErrorPaths(t *testing.T) {
	t.Run("absent token hash", func(t *testing.T) {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		plaintext, _, _, err := identity.GenerateToken()
		require.NoError(t, err)
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, 1, tokens.getByHashN, "a well-shaped arb token must be looked up")
		assert.False(t, probe.called)
	})

	t.Run("token user deleted", func(t *testing.T) {
		users := newFakeUserRepo() // user intentionally NOT registered
		tokens := newFakeTokenRepo()
		plaintext, prefix, hash, err := identity.GenerateToken()
		require.NoError(t, err)
		tokens.byHash[hash] = &identity.Token{ID: uuid.New(), UserID: uuid.New(), Prefix: prefix, TokenHash: hash}
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, probe.called)
	})
}

// Boundary: session wins when both present; bearer works with no cookie; a
// TouchLastUsed error still yields 200; a stale/forged session cookie does not
// block a valid bearer (falls through as the code comment promises).
func TestAuthMiddleware_Bearer_Boundary(t *testing.T) {
	t.Run("stale cookie + valid bearer -> bearer wins", func(t *testing.T) {
		// This is the "stale cookie must not block a valid Authorization header"
		// path from the middleware comment. The session repo has no matching
		// entry, so the cookie branch falls through to bearer resolution.
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		uid, plaintext := seedBearer(t, users, tokens, identity.RoleUser)
		sessions := newFakeSessionRepo() // empty — cookie lookup will fail

		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "stale-or-forged"})
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		assert.Equal(t, uid, probe.id.UserID, "bearer identity must be used when cookie lookup fails")
	})

	t.Run("valid session + garbage bearer -> session wins", func(t *testing.T) {
		users := newFakeUserRepo()
		uid := uuid.New()
		users.byID[uid] = &instance.User{ID: uid, Username: "bob", Role: identity.RoleUser}
		sessions := newFakeSessionRepo()
		sessions.byToken["good"] = &identity.Session{ID: "good", UserID: uid}
		tokens := newFakeTokenRepo()
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "good"})
		req.Header.Set("Authorization", "Bearer arb_dead_beef")
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, uid, probe.id.UserID)
		assert.Equal(t, 0, tokens.getByHashN, "session matched first; bearer never consulted")
	})

	t.Run("touch error still 200", func(t *testing.T) {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		tokens.touchErr = assertErr("touch boom")
		_, plaintext := seedBearer(t, users, tokens, identity.RoleUser)
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		select {
		case <-tokens.touchedIDs:
		case <-time.After(time.Second):
			t.Fatal("TouchLastUsed not invoked")
		}
	})
}

// Isolation/security: a revoked token (removed from the repo) → 401 immediately;
// in local mode a bogus bearer header is ignored (default admin, no repo call);
// REST/MCP/WS all behave identically under bearer.
func TestAuthMiddleware_Bearer_Security(t *testing.T) {
	t.Run("revoked token -> 401", func(t *testing.T) {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		_, plaintext := seedBearer(t, users, tokens, identity.RoleUser)
		// Revoke: clear the repo so GetByHash -> ErrNotFound.
		tokens.byHash = map[string]*identity.Token{}
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, probe.called)
	})

	t.Run("local mode ignores bearer", func(t *testing.T) {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, tokens, users)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer arb_whatever_bogus")
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, identity.RoleAdmin, probe.id.Role)
		assert.Equal(t, 0, tokens.getByHashN, "local mode never consults the token repo")
	})

	t.Run("REST/MCP/WS identical under bearer", func(t *testing.T) {
		for _, path := range []string{"/api/v1/instances", "/mcp", "/api/v1/instances/abc/ws"} {
			// valid bearer passes and resolves to the token owner's identity on
			// every surface — not just 200, but the correct UserID and Username.
			users := newFakeUserRepo()
			tokens := newFakeTokenRepo()
			uid, plaintext := seedBearer(t, users, tokens, identity.RoleUser)
			mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users)
			probe := &identityProbe{}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+plaintext)
			rec := httptest.NewRecorder()
			mw.Handler(probe.handler()).ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, "valid bearer on %s", path)
			require.True(t, probe.called, "handler not reached on %s", path)
			assert.Equal(t, uid, probe.id.UserID, "identity.UserID must be token owner on %s", path)
			assert.Equal(t, "carol", probe.id.Username, "identity.Username must be token owner on %s", path)

			// revoked bearer → 401 on the same surface.
			tokens2 := newFakeTokenRepo()
			mw2 := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens2, newFakeUserRepo())
			probe2 := &identityProbe{}
			req2 := httptest.NewRequest(http.MethodGet, path, nil)
			req2.Header.Set("Authorization", "Bearer "+plaintext)
			rec2 := httptest.NewRecorder()
			mw2.Handler(probe2.handler()).ServeHTTP(rec2, req2)
			assert.Equal(t, http.StatusUnauthorized, rec2.Code, "revoked bearer on %s", path)
			assert.False(t, probe2.called)
		}
	})
}

// assertErr is a tiny error helper for the touch-error case.
type assertErr string

func (e assertErr) Error() string { return string(e) }
