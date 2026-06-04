package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOIDCProvider returns a canned Claims or error.
// AuthCodeURL captures the nonce the handler mints; Exchange echoes it back
// unless claims.Nonce is already set (used in nonce-mismatch tests).
type fakeOIDCProvider struct {
	claims        identity.Claims
	exchErr       error
	lastCode      string
	capturedNonce string // set by AuthCodeURL; echoed by Exchange when claims.Nonce is empty
}

func (f *fakeOIDCProvider) AuthCodeURL(state, nonce string) string {
	f.capturedNonce = nonce
	return "https://idp.example.com/authorize?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce)
}

func (f *fakeOIDCProvider) Exchange(_ context.Context, code string) (identity.Claims, error) {
	f.lastCode = code
	if f.exchErr != nil {
		return identity.Claims{}, f.exchErr
	}
	c := f.claims
	if c.Nonce == "" {
		// Echo back the nonce the handler minted so the replay guard passes in
		// tests that are not specifically testing nonce mismatch.
		c.Nonce = f.capturedNonce
	}
	return c, nil
}

// failingSessionRepo wraps the in-memory fake but fails Create.
type failingSessionRepo struct{ *fakeSessionRepo }

func (f failingSessionRepo) Create(context.Context, *identity.Session) error {
	return errors.New("db down")
}

func newAuthHandler(oidc identity.OIDCProvider, sessions identity.SessionRepository, users instance.UserRepository) *AuthHandler {
	return newAuthHandlerWithBootstrap(oidc, sessions, users)
}

// newAuthHandlerWithBootstrap builds the handler with a real Provisioner over
// the given user repo and optional bootstrap-admin list.
func newAuthHandlerWithBootstrap(oidc identity.OIDCProvider, sessions identity.SessionRepository, users instance.UserRepository, bootstrap ...string) *AuthHandler {
	return NewAuthHandler(oidc, sessions, users, instance.NewProvisioner(users, bootstrap), config.SessionConfig{
		TTLSeconds:   3600,
		CookieName:   "arbitron_session",
		CookieSecure: true,
	})
}

// doLoginThenCallback runs /auth/login to obtain the state cookie, then issues
// the callback with the matching state.
func doLoginThenCallback(t *testing.T, h *AuthHandler, code, next string) *httptest.ResponseRecorder {
	t.Helper()

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/login?next="+url.QueryEscape(next), nil)
	loginRec := httptest.NewRecorder()
	h.Login(loginRec, loginReq)
	require.Equal(t, http.StatusFound, loginRec.Code)

	// Extract state from the redirect URL and the state cookie.
	loc, err := url.Parse(loginRec.Header().Get("Location"))
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)

	stateCookie := findCookie(loginRec.Result().Cookies(), stateCookieName)
	require.NotNil(t, stateCookie, "login must set the state cookie")

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?code="+code+"&state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)
	return cbRec
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestAuthHandler_LoginThenCallback_NewUser(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "sub-new", Username: "newuser", Email: "new@x.com"}}
	sessions := newFakeSessionRepo()
	users := newFakeUserRepo()
	h := newAuthHandler(oidc, sessions, users)

	rec := doLoginThenCallback(t, h, "ok", "/instances")
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/instances", rec.Header().Get("Location"))

	// A user-role user was JIT-created with the subject.
	var found *instance.User
	for _, u := range users.byID {
		if u.Subject != nil && *u.Subject == "sub-new" {
			found = u
		}
	}
	require.NotNil(t, found, "callback must JIT-create the user")
	assert.Equal(t, identity.RoleUser, found.Role)
	require.NotNil(t, found.Email)
	assert.Equal(t, "new@x.com", *found.Email)

	// Session cookie set with a raw token the session repo can resolve.
	sc := findCookie(rec.Result().Cookies(), "arbitron_session")
	require.NotNil(t, sc)
	assert.NotEmpty(t, sc.Value)
	_, ok := sessions.byToken[sc.Value]
	assert.True(t, ok, "session repo holds a session for the raw cookie token")
}

func TestAuthHandler_Callback_ExistingUser(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "sub-existing", Username: "x"}}
	sessions := newFakeSessionRepo()
	users := newFakeUserRepo()
	existing := &instance.User{ID: mustUUID(), Subject: strptr("sub-existing"), Username: "existing", Role: identity.RoleAdmin}
	users.byID[existing.ID] = existing
	// Make GetBySubject resolve it.
	users.bySubject = map[string]*instance.User{"sub-existing": existing}
	h := newAuthHandler(oidc, sessions, users)

	rec := doLoginThenCallback(t, h, "ok", "/")
	assert.Equal(t, http.StatusFound, rec.Code)

	// No new user created; existing role preserved.
	assert.Len(t, users.byID, 1)
	assert.Equal(t, identity.RoleAdmin, existing.Role)
}

func TestAuthHandler_Callback_BootstrapAdmin(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "boss-sub", Username: "boss", Email: "boss@corp.com"}}
	sessions := newFakeSessionRepo()
	users := newFakeUserRepo()
	// Bootstrap by email (case-insensitive).
	h := newAuthHandlerWithBootstrap(oidc, sessions, users, "BOSS@corp.com")

	rec := doLoginThenCallback(t, h, "ok", "/")
	require.Equal(t, http.StatusFound, rec.Code)

	var found *instance.User
	for _, u := range users.byID {
		if u.Subject != nil && *u.Subject == "boss-sub" {
			found = u
		}
	}
	require.NotNil(t, found, "bootstrap subject must be JIT-created")
	assert.Equal(t, identity.RoleAdmin, found.Role, "bootstrap identity is provisioned as admin")
}

func TestAuthHandler_Callback_StateMismatch(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s"}}
	sessions := newFakeSessionRepo()
	users := newFakeUserRepo()
	h := newAuthHandler(oidc, sessions, users)

	// Login to get a state cookie.
	loginRec := httptest.NewRecorder()
	h.Login(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	stateCookie := findCookie(loginRec.Result().Cookies(), stateCookieName)
	require.NotNil(t, stateCookie)

	// Callback with a DIFFERENT state.
	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?code=ok&state=WRONG", nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbRec.Code)
	assert.Empty(t, sessions.byToken, "no session on state mismatch")
}

func TestAuthHandler_Callback_NonceMismatch(t *testing.T) {
	// Exchange returns a nonce that doesn't match what the handler minted.
	// The callback must reject with 400 to guard against replay attacks.
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u", Nonce: "WRONG_NONCE"}}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	rec := doLoginThenCallback(t, h, "ok", "/")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "nonce mismatch must be rejected with 400")
	assert.Empty(t, sessions.byToken, "no session must be created on nonce mismatch")
	assert.Nil(t, findCookie(rec.Result().Cookies(), "arbitron_session"))
}

func TestAuthHandler_Callback_MissingCode(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s"}}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	h.Login(loginRec, loginReq)
	loc, _ := url.Parse(loginRec.Header().Get("Location"))
	state := loc.Query().Get("state")
	stateCookie := findCookie(loginRec.Result().Cookies(), stateCookieName)

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)
	assert.Equal(t, http.StatusBadRequest, cbRec.Code)
}

func TestAuthHandler_Callback_ExchangeFails(t *testing.T) {
	oidc := &fakeOIDCProvider{exchErr: errors.New("idp rejected")}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	rec := doLoginThenCallback(t, h, "bad", "/")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, sessions.byToken)
	assert.Nil(t, findCookie(rec.Result().Cookies(), "arbitron_session"))
}

func TestAuthHandler_Callback_SessionCreateFails(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	sessions := failingSessionRepo{newFakeSessionRepo()}
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	rec := doLoginThenCallback(t, h, "ok", "/")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Nil(t, findCookie(rec.Result().Cookies(), "arbitron_session"))
}

func TestAuthHandler_Login_SanitizesNext(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	for _, tc := range []struct {
		next     string
		wantDest string
	}{
		{"https://evil.com", "/"},
		{"//evil.com", "/"},
		{"/instances/123", "/instances/123"},
	} {
		t.Run(tc.next, func(t *testing.T) {
			sessions := newFakeSessionRepo()
			h := newAuthHandler(oidc, sessions, newFakeUserRepo())
			rec := doLoginThenCallback(t, h, "ok", tc.next)
			require.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, tc.wantDest, rec.Header().Get("Location"))
		})
	}
}

func TestAuthHandler_SessionCookie_Attributes(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	rec := doLoginThenCallback(t, h, "ok", "/")
	sc := findCookie(rec.Result().Cookies(), "arbitron_session")
	require.NotNil(t, sc)
	assert.True(t, sc.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, sc.SameSite)
	assert.Equal(t, "/", sc.Path)
	assert.True(t, sc.Secure)
	assert.False(t, sc.Expires.IsZero())
}

func TestAuthHandler_Callback_SetsRawTokenCookie_NotHash(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	rec := doLoginThenCallback(t, h, "ok", "/")
	sc := findCookie(rec.Result().Cookies(), "arbitron_session")
	require.NotNil(t, sc)
	// The fake session repo keys by the RAW token; the cookie value must be that key.
	_, ok := sessions.byToken[sc.Value]
	assert.True(t, ok)
	assert.GreaterOrEqual(t, len(sc.Value), 32, "raw token must be high-entropy")
}

func TestAuthHandler_Logout_DeletesSession(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	sessions := newFakeSessionRepo()
	sessions.byToken["live"] = &identity.Session{ID: "live"}
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "live"})
	rec := httptest.NewRecorder()
	h.Logout(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	_, ok := sessions.byToken["live"]
	assert.False(t, ok, "logout deletes the session")
	sc := findCookie(rec.Result().Cookies(), "arbitron_session")
	require.NotNil(t, sc)
	assert.True(t, sc.MaxAge < 0, "logout clears the cookie")
}

func TestAuthHandler_Callback_TamperedStateCookie(t *testing.T) {
	oidc := &fakeOIDCProvider{claims: identity.Claims{Subject: "s", Username: "u"}}
	sessions := newFakeSessionRepo()
	h := newAuthHandler(oidc, sessions, newFakeUserRepo())

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	h.Login(loginRec, loginReq)
	loc, _ := url.Parse(loginRec.Header().Get("Location"))
	state := loc.Query().Get("state")
	stateCookie := findCookie(loginRec.Result().Cookies(), stateCookieName)
	require.NotNil(t, stateCookie)

	// Tamper with the signed cookie (break the HMAC).
	stateCookie.Value = stateCookie.Value + "TAMPER"

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/callback?code=ok&state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	h.Callback(cbRec, cbReq)

	assert.Equal(t, http.StatusBadRequest, cbRec.Code, "a tampered state cookie must be rejected")
	assert.Empty(t, sessions.byToken)
}

// helpers shared with this test file only.
func strptr(s string) *string { return &s }

func mustUUID() uuid.UUID { return uuid.New() }

var _ = strings.TrimSpace
