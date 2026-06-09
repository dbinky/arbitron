package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEdgeVerifier is a configurable identity.EdgeVerifier that records call count.
type fakeEdgeVerifier struct {
	email  string
	groups []string
	err    error
	calls  int
}

func (f *fakeEdgeVerifier) Verify(_ context.Context, _ string) (identity.EdgeIdentity, error) {
	f.calls++
	if f.err != nil {
		return identity.EdgeIdentity{}, f.err
	}
	return identity.EdgeIdentity{Email: f.email, Groups: f.groups}, nil
}

// fakeEmailProvisioner is a configurable emailProvisioner for the forced-error case.
type fakeEmailProvisioner struct {
	user      *instance.User
	err       error
	calls     int
	gotEmail  string
	gotGroups []string
}

func (f *fakeEmailProvisioner) ResolveByEmail(_ context.Context, email string, groups []string) (*instance.User, error) {
	f.calls++
	f.gotEmail = email
	f.gotGroups = groups
	return f.user, f.err
}

const cfHeader = "Cf-Access-Jwt-Assertion"

// newEdgeMW builds an edge-mode middleware over the real provisioner + fake repo.
func newEdgeMW(t *testing.T, users *fakeUserRepo, verifier identity.EdgeVerifier, bootstrap ...string) *AuthMiddleware {
	t.Helper()
	groups := identity.GroupConfig{DefaultGroup: "default"}
	prov := instance.NewProvisioner(users, bootstrap, groups)
	return NewEdgeAuthMiddleware(cfHeader, verifier, prov, users, groups)
}

func TestEdge_ValidHeader_Authenticates(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	mw := newEdgeMW(t, users, verifier)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set(cfHeader, "some.jwt.token")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, probe.called)
	assert.Equal(t, identity.RoleUser, probe.id.Role)
	assert.Equal(t, "alice@corp.example", probe.id.Username)
	assert.Equal(t, []string{"default"}, probe.id.Departments)
}

func TestEdge_AbsentHeader_401_VerifierNotCalled(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	mw := newEdgeMW(t, users, verifier)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
	assert.Equal(t, 0, verifier.calls, "verifier must not run when no token is present")
	assert.Contains(t, rec.Body.String(), "UNAUTHORIZED")
}

func TestEdge_VerifyFailure_401_ProvisionerNotCalled(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{err: errors.New("bad sig")}
	prov := &fakeEmailProvisioner{}
	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewEdgeAuthMiddleware(cfHeader, verifier, prov, users, groups)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set(cfHeader, "forged.jwt")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, probe.called)
	assert.Equal(t, 1, verifier.calls)
	assert.Equal(t, 0, prov.calls, "a verify failure must not reach the provisioner")
}

func TestEdge_ProvisionDBError_500(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	prov := &fakeEmailProvisioner{err: errors.New("db down")}
	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewEdgeAuthMiddleware(cfHeader, verifier, prov, users, groups)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set(cfHeader, "good.jwt")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, probe.called)
	assert.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
}

func TestEdge_SessionCookieIgnored(t *testing.T) {
	t.Run("valid edge header + session cookie → authed via edge (nil session repo, no panic)", func(t *testing.T) {
		users := newFakeUserRepo()
		verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
		mw := newEdgeMW(t, users, verifier) // session/token repos are nil
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set(cfHeader, "good.jwt")
		req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "ignored"})
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		assert.Equal(t, "alice@corp.example", probe.id.Username)
	})

	t.Run("only a session cookie, no edge header → 401", func(t *testing.T) {
		users := newFakeUserRepo()
		verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
		mw := newEdgeMW(t, users, verifier)
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "ignored"})
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.False(t, probe.called)
		assert.Equal(t, 0, verifier.calls)
	})
}

func TestEdge_BootstrapAdminVsDefaultGroup(t *testing.T) {
	t.Run("bootstrap email → admin", func(t *testing.T) {
		users := newFakeUserRepo()
		verifier := &fakeEdgeVerifier{email: "boss@corp.example"}
		mw := newEdgeMW(t, users, verifier, "boss@corp.example")
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set(cfHeader, "good.jwt")
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		assert.Equal(t, identity.RoleAdmin, probe.id.Role)
	})

	t.Run("non-bootstrap email → default-group user that sees all", func(t *testing.T) {
		users := newFakeUserRepo()
		verifier := &fakeEdgeVerifier{email: "dev@corp.example"}
		mw := newEdgeMW(t, users, verifier, "boss@corp.example")
		probe := &identityProbe{}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set(cfHeader, "good.jwt")
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		assert.Equal(t, identity.RoleUser, probe.id.Role)
		assert.Equal(t, []string{"default"}, probe.id.Departments)
	})
}

// Edge: a browser GET to a non-API path without the CF token must return 401,
// NOT a redirect to /auth/login. In edge mode, /auth/login is not mounted
// (authHandler is nil), so redirecting there would cause an infinite redirect
// loop in the browser. Auth happens entirely at the edge (Cloudflare Access
// injects the JWT); Arbitron has no interactive login page in this mode.
func TestEdge_BrowserNavigationNoToken_401NotRedirect(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	mw := newEdgeMW(t, users, verifier)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "browser GET without CF token must be 401, not a login redirect")
	assert.Empty(t, rec.Header().Get("Location"), "must not redirect to /auth/login (not mounted in edge mode)")
	assert.False(t, probe.called)
}

// Edge: a broken EdgeVerifier that returns ("", nil) must map to 401, not 500.
// The EdgeVerifier interface contract says a nil-error return always carries a
// non-empty email, but this guard prevents a misbehaving implementation from
// leaking through to ResolveByEmail("") → ErrInvalidClaims → 500. An empty
// identity claim is always a credential failure (401), never a server fault.
func TestEdge_EmptyEmailFromVerifier_401(t *testing.T) {
	users := newFakeUserRepo()
	// Verifier returns ("", nil) — contract violation but possible with a broken impl.
	verifier := &fakeEdgeVerifier{email: "", err: nil}
	mw := newEdgeMW(t, users, verifier) // real provisioner: ResolveByEmail("") → ErrInvalidClaims
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set(cfHeader, "token.with.empty.email.claim")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "empty email from verifier must yield 401, not 500")
	assert.False(t, probe.called)
}

// Edge: a Bearer-only request (no CF header) must be rejected 401 in edge mode.
// Edge mode short-circuits before the Bearer branch; the token repos are nil
// and must never be consulted.
func TestEdge_BearerOnlyToken_401(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	mw := newEdgeMW(t, users, verifier)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set("Authorization", "Bearer arb_test_abc123")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "Bearer-only request must be 401 in edge mode (CF header required)")
	assert.False(t, probe.called)
	assert.Equal(t, 0, verifier.calls, "verifier must not be called when CF header is absent")
}

// Edge: a ListDepartments failure during enrich must NOT fail the request — the
// user is still authenticated with their led departments + default-group fallback.
// A transient DB read error in enrich must never lock a user out.
func TestEdge_ListDepartmentsFailure_StillAuthed(t *testing.T) {
	users := newFakeUserRepo()
	users.listDeptErr = errors.New("department store down")
	verifier := &fakeEdgeVerifier{email: "alice@corp.example"}
	mw := newEdgeMW(t, users, verifier)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req.Header.Set(cfHeader, "good.jwt")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "ListDepartments failure must not reject the request")
	require.True(t, probe.called)
	assert.Equal(t, "alice@corp.example", probe.id.Username)
	assert.NotEmpty(t, probe.id.Departments, "fallback to default group when stored departments unavailable")
}

func TestEdge_NewUserAutoProvisioned_Idempotent(t *testing.T) {
	users := newFakeUserRepo()
	verifier := &fakeEdgeVerifier{email: "newbie@corp.example"}
	mw := newEdgeMW(t, users, verifier)

	serve := func() identity.Identity {
		probe := &identityProbe{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		req.Header.Set(cfHeader, "good.jwt")
		rec := httptest.NewRecorder()
		mw.Handler(probe.handler()).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, probe.called)
		return probe.id
	}

	first := serve()
	assert.NotEqual(t, uuid.Nil, first.UserID)
	second := serve()
	assert.Equal(t, first.UserID, second.UserID, "returning email must resolve to the same user")
}

// Config+edge+resolution (Area #19): a configured Mapping is applied end-to-end —
// the verifier returns the raw claim value ("guid-123"), the provisioner resolves
// it via ResolveDepartments+Mapping, and the authenticated identity carries the
// friendly department ("engineering"). This bridges loader→GroupConfig→edge middleware.
func TestEdgeMiddleware_ConfiguredMapping_ResolvesToFriendlyDept(t *testing.T) {
	users := newFakeUserRepo()
	// Real provisioner with a mapping: guid-123 → engineering.
	groupCfg := identity.GroupConfig{
		DefaultGroup: "default",
		Mapping:      map[string]string{"guid-123": "engineering"},
	}
	prov := instance.NewProvisioner(users, nil, groupCfg)
	// Verifier returns the raw GUID as the group claim value.
	verifier := &fakeEdgeVerifier{email: "alice@corp.example", groups: []string{"guid-123"}}
	mw := NewEdgeAuthMiddleware(cfHeader, verifier, prov, users, groupCfg)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set(cfHeader, "any-non-empty-token")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"engineering"}, probe.id.Departments,
		"raw GUID group claim must map to friendly department via GroupConfig.Mapping")
}

// Edge: the verified group claim must be threaded through to the provisioner so a
// user is scoped to the department(s) their SSO group resolves to. A nil/empty
// Groups (no claim) preserves the default-group fallback (asserted elsewhere).
func TestEdgeMiddleware_ThreadsVerifiedGroupsToProvisioner(t *testing.T) {
	users := newFakeUserRepo()
	uid := uuid.New()
	prov := &fakeEmailProvisioner{user: &instance.User{ID: uid, Username: "alice", Role: identity.RoleUser}}
	verifier := &fakeEdgeVerifier{email: "alice@corp.example", groups: []string{"eng", "platform"}}
	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewEdgeAuthMiddleware(cfHeader, verifier, prov, users, groups)
	probe := &identityProbe{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set(cfHeader, "any-non-empty-token")
	rec := httptest.NewRecorder()
	mw.Handler(probe.handler()).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice@corp.example", prov.gotEmail)
	assert.Equal(t, []string{"eng", "platform"}, prov.gotGroups, "verified groups must reach the provisioner")
}
