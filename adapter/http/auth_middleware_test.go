package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	defaultID   uuid.UUID
	byID        map[uuid.UUID]*instance.User
	bySubject   map[string]*instance.User
	depts       map[uuid.UUID][]string
	listDeptErr error // when non-nil, ListDepartments returns this error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{defaultID: uuid.New(), byID: map[uuid.UUID]*instance.User{}, depts: map[uuid.UUID][]string{}}
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

func (r *fakeUserRepo) ListDepartments(_ context.Context, userID uuid.UUID) ([]string, error) {
	if r.listDeptErr != nil {
		return nil, r.listDeptErr
	}
	return append([]string(nil), r.depts[userID]...), nil
}

func (r *fakeUserRepo) ReplaceDepartments(_ context.Context, userID uuid.UUID, departments []string) error {
	if r.depts == nil {
		r.depts = map[uuid.UUID][]string{}
	}
	r.depts[userID] = append([]string(nil), departments...)
	return nil
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
	s, ok := r.byToken[id]
	if !ok {
		return nil, identity.ErrNotFound
	}
	// Mirror real SessionRepo.Get: expires_at > now (strict). A zero ExpiresAt
	// means no expiry is set (used by legacy helper sessions in tests).
	if !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(time.Now()) {
		return nil, identity.ErrNotFound
	}
	return s, nil
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
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users, identity.GroupConfig{DefaultGroup: "default"})
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

	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "handler must never run unauthenticated")
}

func TestAuthMiddleware_Org_EmptyCookieValue_401(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "a deleted user cannot ride a stale session")
}

func TestAuthMiddleware_Org_BrowserNav_Redirects(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "forged-value-hashes-to-nothing"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
}

// TestAuthMiddleware_Org_ExpiredSessionPresent_401 covers the path where a
// session row IS present in the store but has a past ExpiresAt. The real
// SessionRepo.Get filters these server-side (expires_at > now in SQL); the fake
// must model the same behavior so this test does not false-pass.
func TestAuthMiddleware_Org_ExpiredSessionPresent_401(t *testing.T) {
	users := newFakeUserRepo()
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "alice", Role: identity.RoleUser}
	sessions := newFakeSessionRepo()
	sessions.byToken["stale"] = &identity.Session{
		ID:        "stale",
		UserID:    uid,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "stale"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "expired session in store must not authenticate")
}

func TestAuthMiddleware_Local_NeverReadsCookie(t *testing.T) {
	users := newFakeUserRepo()
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "bogus"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, identity.RoleAdmin, probe.id.Role, "local mode ignores cookies entirely")
}

// TestAuthMiddleware_Local_DefaultUserError_ServerError verifies that if
// GetDefaultUser returns an error (e.g. DB unavailable at startup), local mode
// fails closed with a 500: this is a server-state failure (we cannot establish
// the default identity), distinct from a 401 (caller presented no credential).
// The sync.Once caches the error permanently, so subsequent requests also fail.
func TestAuthMiddleware_Local_DefaultUserError_ServerError(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, &errUserRepo{newFakeUserRepo()}, identity.GroupConfig{DefaultGroup: "default"})
	probe := &identityProbe{}

	for i := range 3 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code, "request %d must be a server error", i)
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

	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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

	// The last-used touch is coalesced into the owned flusher; Stop() drains it
	// synchronously (no time.Sleep), then the channel carries exactly the matched
	// token's ID.
	hash := identity.HashToken(plaintext)
	mw.Stop()
	select {
	case touchedID := <-tokens.touchedIDs:
		assert.Equal(t, tokens.byHash[hash].ID, touchedID, "TouchLastUsed must be called with the matched token's ID")
	default:
		t.Fatal("TouchLastUsed was never invoked")
	}
}

// Input validation: non-arb / non-Bearer / missing headers → 401 with NO DB hit
// for the non-arb shapes (proves the ParsePrefix pre-filter).
func TestAuthMiddleware_Bearer_Validation(t *testing.T) {
	for _, hdr := range []string{"Bearer not-an-arb-token", "Basic abc123", ""} {
		users := newFakeUserRepo()
		tokens := newFakeTokenRepo()
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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

		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set("Authorization", "Bearer "+plaintext)
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		// The touch is coalesced into the flusher; Stop() drains it synchronously.
		// A failing TouchLastUsed is swallowed (best-effort), so the request still
		// returned 200 above.
		mw.Stop()
		select {
		case <-tokens.touchedIDs:
		default:
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
		mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
		mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
			mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens, users, identity.GroupConfig{DefaultGroup: "default"})
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
			mw2 := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), tokens2, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
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

func TestAuthMiddleware_OrgMode_PopulatesDepartmentsAndLed(t *testing.T) {
	// A session resolves to a user with stored departments [engineering]; config
	// makes this user a lead of [platform]. Effective Departments = union; Led = [platform].
	uid := uuid.New()
	subj := "alice-subject"
	email := "alice@corp.example"
	users := newFakeUserRepo()
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Email: &email, Username: "alice", Role: identity.RoleUser}
	users.depts[uid] = []string{"engineering"}

	sessions := newFakeSessionRepo()
	sessions.byToken["cookie-token"] = &identity.Session{ID: "cookie-token", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "cookie-token"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !reflect.DeepEqual(got.Departments, []string{"engineering", "platform"}) {
		t.Errorf("Departments = %v, want [engineering platform]", got.Departments)
	}
	if !reflect.DeepEqual(got.LedDepartments, []string{"platform"}) {
		t.Errorf("LedDepartments = %v, want [platform]", got.LedDepartments)
	}
}

func TestAuthMiddleware_LocalMode_DefaultGroupAdmin(t *testing.T) {
	users := newFakeUserRepo() // GetDefaultUser returns users.defaultID

	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got.Role != identity.RoleAdmin {
		t.Errorf("Role = %q, want admin", got.Role)
	}
	if !reflect.DeepEqual(got.Departments, []string{"default"}) {
		t.Errorf("Departments = %v, want [default]", got.Departments)
	}
	if len(got.LedDepartments) != 0 {
		t.Errorf("LedDepartments = %v, want empty in local mode", got.LedDepartments)
	}
}

func TestAuthMiddleware_OrgMode_NoCredentialStill401(t *testing.T) {
	users := newFakeUserRepo()
	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, users, groups)

	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not run without a credential")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestAuthMiddleware_Enrich_ListDepartmentsFailure_StillAuthenticates verifies
// the non-fatal guarantee documented in enrich: a transient ListDepartments read
// error must never lock an authenticated user out. The request must still succeed
// (200), and the identity must include at least the led departments (or the
// default-group fallback when there are none). A failing department read is never
// escalated to a 500 — only auth failures are escalated.
func TestAuthMiddleware_Enrich_ListDepartmentsFailure_StillAuthenticates(t *testing.T) {
	uid := uuid.New()
	subj := "alice-subject"
	email := "alice@corp.example"
	users := newFakeUserRepo()
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Email: &email, Username: "alice", Role: identity.RoleUser}
	users.listDeptErr = errors.New("transient DB read error")

	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	// The user leads "platform" in config, so even with ListDepartments failing
	// the identity still contains the led department.
	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (ListDepartments failure must not lock user out)", rec.Code)
	}
	if got.UserID != uid {
		t.Errorf("UserID = %v, want %v", got.UserID, uid)
	}
	// Even with the DB error, the led department from config is still present.
	if !got.InDepartment("platform") {
		t.Errorf("Departments = %v, want platform (from led, despite DB error)", got.Departments)
	}
}

// TestAuthMiddleware_Enrich_OrgMode_NoStoredDepts_DefaultGroupFallback verifies
// the defensive fallback in enrich: an org-mode user with no stored departments
// and no led departments is placed in the default group rather than left with an
// empty Departments slice. This mirrors what the provisioner snapshots at login
// when the group claim is absent (ResolveDepartments → DefaultGroupOr).
func TestAuthMiddleware_Enrich_OrgMode_NoStoredDepts_DefaultGroupFallback(t *testing.T) {
	uid := uuid.New()
	users := newFakeUserRepo()
	users.byID[uid] = &instance.User{ID: uid, Username: "new-user", Role: identity.RoleUser}
	// No depts stored; not a configured lead.

	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !reflect.DeepEqual(got.Departments, []string{"default"}) {
		t.Errorf("Departments = %v, want [default] (defensive default-group fallback)", got.Departments)
	}
	if len(got.LedDepartments) != 0 {
		t.Errorf("LedDepartments = %v, want empty (user leads no departments)", got.LedDepartments)
	}
}

// TestAuthMiddleware_Enrich_LedDeptAlreadyStored_Deduplicated pins the
// unionDepts deduplication: when a user's stored departments include a
// department they also lead (e.g. snapshot already contains the led dept),
// it must appear exactly once in Departments. The middleware uses its own
// unionDepts rather than EffectiveMembership, so this test covers that path.
func TestAuthMiddleware_Enrich_LedDeptAlreadyStored_Deduplicated(t *testing.T) {
	uid := uuid.New()
	subj := "alice-subject"
	email := "alice@corp.example"
	users := newFakeUserRepo()
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Email: &email, Username: "alice", Role: identity.RoleUser}
	users.depts[uid] = []string{"engineering", "platform"} // platform is also a led dept

	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	// platform must appear exactly once even though it is both stored and led.
	if !reflect.DeepEqual(got.Departments, []string{"engineering", "platform"}) {
		t.Errorf("Departments = %v, want [engineering platform] (no duplicate for led dept already in stored)", got.Departments)
	}
	if !reflect.DeepEqual(got.LedDepartments, []string{"platform"}) {
		t.Errorf("LedDepartments = %v, want [platform]", got.LedDepartments)
	}
}

// TestAuthMiddleware_Enrich_SubjectLead_Match verifies that lead matching
// works by subject (not only by email). The enrich path extracts the user's
// subject and threads it through LedDepartments; this test uses a Leads
// config entry that is the user's subject ID, ensuring the subject path fires.
func TestAuthMiddleware_Enrich_SubjectLead_Match(t *testing.T) {
	uid := uuid.New()
	subj := "alice-oidc-sub-123"
	users := newFakeUserRepo()
	// User has subject but NO email — only subject-based lead matching is possible.
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Username: "alice", Role: identity.RoleUser}

	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	// Leads config uses the subject ID, not an email.
	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice-oidc-sub-123"}},
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !got.LeadsDepartment("platform") {
		t.Errorf("LedDepartments = %v, want to include platform (subject-based lead match)", got.LedDepartments)
	}
	if !got.InDepartment("platform") {
		t.Errorf("Departments = %v, want to include platform (led dept union)", got.Departments)
	}
}

// ---------------------------------------------------------------------------
// FA #27: custom defaultgroup propagates through provisioner + middleware
// ---------------------------------------------------------------------------

// TestCustomDefaultGroup_ProvisionsAndPropagates verifies that a non-"default"
// defaultgroup value in identity.GroupConfig drives both the provisioner
// (snapshot) and the middleware (context identity). A claimless user must end
// up in the CONFIGURED group — not the hardcoded sentinel "default". If either
// layer ignores GroupConfig.DefaultGroup, every custom-defaultgroup deployment
// silently misplaces claimless users.
func TestCustomDefaultGroup_ProvisionsAndPropagates(t *testing.T) {
	uid := uuid.New()
	subj := "carol-subject"

	users := newFakeUserRepo()
	users.bySubject = map[string]*instance.User{}
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Username: "carol", Role: identity.RoleUser}
	users.bySubject[subj] = users.byID[uid]

	// Custom default group — not the literal "default".
	groups := identity.GroupConfig{DefaultGroup: "everyone"}

	// Provisioner: no groups claim → must snapshot ["everyone"].
	prov := instance.NewProvisioner(users, nil, groups)
	_, err := prov.Resolve(context.Background(), instance.Claims{
		Subject: subj, Username: "carol", Groups: nil,
	})
	require.NoError(t, err)

	stored, err := users.ListDepartments(context.Background(), uid)
	require.NoError(t, err)
	assert.Equal(t, []string{"everyone"}, stored,
		"provisioner must snapshot the configured DefaultGroup, not the hardcoded 'default'")

	// Middleware: reads the stored snapshot → context identity must carry "everyone".
	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{
		ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.True(t, got.InDepartment("everyone"),
		"middleware must surface the custom default group 'everyone' from the stored snapshot")
	assert.False(t, got.InDepartment("default"),
		"hardcoded sentinel 'default' must NOT appear when DefaultGroup is configured differently")
}

// ---------------------------------------------------------------------------
// FA #26: claim → normalize → snapshot → context identity seam test
// ---------------------------------------------------------------------------

// TestProvisioner_Snapshot_ReadByMiddleware_Consistency verifies the critical
// invariant of FA #26: the departments written by the provisioner at login time
// (ReplaceDepartments) are the same departments read by the auth middleware at
// request time (ListDepartments). Both halves share the same UserRepository.
//
// Input: raw claim groups ["Engineering", "Platform"] (mixed case)
// Provisioner output: normalized ["engineering", "platform"] stored in repo
// Middleware output: ["engineering", "platform"] + led ["platform"] in context
//
// This single test exercises the full chain without any fakeProvisioner stub.
func TestProvisioner_Snapshot_ReadByMiddleware_Consistency(t *testing.T) {
	uid := uuid.New()
	subj := "bob-subject"
	email := "bob@corp.example"

	// Shared in-memory repo: provisioner writes, middleware reads.
	users := newFakeUserRepo()
	users.bySubject = map[string]*instance.User{}
	users.byID[uid] = &instance.User{
		ID:       uid,
		Subject:  &subj,
		Email:    &email,
		Username: "bob",
		Role:     identity.RoleUser,
	}
	users.bySubject[subj] = users.byID[uid]

	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"bob@corp.example"}},
	}

	// Step 1 — login: provisioner resolves raw claim groups → normalizes →
	// snapshots into the shared repo via ReplaceDepartments.
	prov := instance.NewProvisioner(users, nil, groups)
	_, err := prov.Resolve(context.Background(), instance.Claims{
		Subject:  subj,
		Email:    email,
		Username: "bob",
		Groups:   []string{"Engineering", "Platform"}, // raw, mixed case
	})
	require.NoError(t, err, "provisioner.Resolve must succeed")

	// Verify normalization happened in the stored snapshot.
	stored, err := users.ListDepartments(context.Background(), uid)
	require.NoError(t, err)
	require.Contains(t, stored, "engineering", "NormalizeDept must lowercase 'Engineering'")
	require.Contains(t, stored, "platform", "NormalizeDept must lowercase 'Platform'")

	// Step 2 — request: middleware reads the stored departments from the SAME
	// repo and merges the config-based lead set into the context identity.
	sessions := newFakeSessionRepo()
	sessions.byToken["tok"] = &identity.Session{
		ID: "tok", UserID: uid, ExpiresAt: time.Now().Add(time.Hour),
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	// The middleware must present the normalized claim departments (written by
	// the provisioner) as the caller's effective membership.
	assert.True(t, got.InDepartment("engineering"),
		"middleware must reflect provisioner-stored 'engineering' in Departments")
	assert.True(t, got.InDepartment("platform"),
		"middleware must reflect provisioner-stored 'platform' in Departments")
	assert.True(t, got.LeadsDepartment("platform"),
		"platform lead config must appear in LedDepartments at request time")
}
