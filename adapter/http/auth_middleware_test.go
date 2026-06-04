package http

import (
	"context"
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

// ---- tests ----

func TestAuthMiddleware_Local_InjectsAdmin(t *testing.T) {
	users := newFakeUserRepo()
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, users)
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

	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, users)
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), newFakeUserRepo())
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "handler must never run unauthenticated")
}

func TestAuthMiddleware_Org_EmptyCookieValue_401(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), newFakeUserRepo())
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), newFakeUserRepo())
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
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called, "a deleted user cannot ride a stale session")
}

func TestAuthMiddleware_Org_BrowserNav_Redirects(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), newFakeUserRepo())
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
}

func TestAuthMiddleware_Org_ForgedCookie_NeverAuthenticates(t *testing.T) {
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), newFakeUserRepo())
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
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, users)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "bogus"})
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, identity.RoleAdmin, probe.id.Role, "local mode ignores cookies entirely")
}
