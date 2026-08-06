package http_test

// Vertical slice: Cf-Access-Jwt-Assertion header → edge.Verifier → instance.Provisioner
// → enrich → identity.Identity in downstream context.
//
// Uses REAL components throughout — no mocks — so the test exercises the actual
// wiring that main.go does in edge mode:
//   real RSA-signed JWKS server  →  real adaptedge.Verifier
//   real SQLite (":memory:")     →  real sqlite.UserRepo
//   real instance.Provisioner
//   real NewEdgeAuthMiddleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptedge "github.com/arbitron/arbitron/adapter/edge"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// sliceJWKS is a minimal Cloudflare-Access-style JWKS server: real RSA key,
// real OIDC discovery doc, real signature. Mirrors the jwksHarness in
// adapter/edge/verifier_test.go but is declared here so this file is
// self-contained (test helpers are not importable across packages).
type sliceJWKS struct {
	server  *httptest.Server
	signKey *rsa.PrivateKey
	keyID   string
	issuer  string
}

func newSliceJWKS(t *testing.T) *sliceJWKS {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	h := &sliceJWKS{signKey: key, keyID: "k1"}

	serveKeys := func(w http.ResponseWriter, _ *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: h.keyID, Algorithm: "RS256", Use: "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                h.issuer,
			"jwks_uri":                              h.issuer + "/cdn-cgi/access/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/cdn-cgi/access/certs", serveKeys)

	srv := httptest.NewServer(mux)
	h.server = srv
	h.issuer = srv.URL
	t.Cleanup(srv.Close)
	return h
}

// sign builds a real JWT signed with signWith (nil → h.signKey).
func (h *sliceJWKS) sign(t *testing.T, signWith *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	if signWith == nil {
		signWith = h.signKey
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signWith},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", h.keyID),
	)
	require.NoError(t, err)
	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// validClaims returns a well-formed in-date claim set for the given audience.
func (h *sliceJWKS) validClaims(aud string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   h.issuer,
		"aud":   aud,
		"sub":   "edge-sub-1",
		"email": "alice@corp.example",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
}

// sliceEnv holds the real components wired for a vertical-slice test.
type sliceEnv struct {
	jwks *sliceJWKS
	db   *sql.DB
	mw   *adapthttp.AuthMiddleware
}

// newSliceEnv builds the full real stack for edge-mode vertical-slice tests.
// bootstrapAdmins is passed directly to instance.NewProvisioner.
func newSliceEnv(t *testing.T, bootstrapAdmins []string) *sliceEnv {
	t.Helper()
	jwks := newSliceJWKS(t)

	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))

	groups := identity.GroupConfig{DefaultGroup: "default"}
	userRepo := sqlite.NewUserRepo(db)
	prov := instance.NewProvisioner(userRepo, bootstrapAdmins, groups)

	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: jwks.issuer,
		Audiences:  []string{"aud"},
	})
	require.NoError(t, err)

	mw := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov, userRepo, groups)
	return &sliceEnv{jwks: jwks, db: db, mw: mw}
}

// serve routes a single request through the middleware. The downstream handler
// writes 200 and stores the injected identity for inspection.
func (e *sliceEnv) serve(req *http.Request) (*httptest.ResponseRecorder, identity.Identity) {
	var captured identity.Identity
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := identity.FromContext(r.Context())
		captured = id
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	e.mw.Handler(downstream).ServeHTTP(rec, req)
	return rec, captured
}

// requestWithJWT builds a GET request carrying the given raw JWT.
func requestWithJWT(raw string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set(adapthttp.CfAccessJWTHeader, raw)
	return req
}

// --- happy path ---

// TestEdgeSlice_ValidJWT_IdentityInjected is the end-to-end happy path: a
// real signed JWT carries the email through the verifier and provisioner, and
// the resulting identity lands in the downstream context.
func TestEdgeSlice_ValidJWT_IdentityInjected(t *testing.T) {
	env := newSliceEnv(t, nil)
	raw := env.jwks.sign(t, nil, env.jwks.validClaims("aud"))

	rec, id := env.serve(requestWithJWT(raw))

	require.Equal(t, http.StatusOK, rec.Code, "valid JWT must pass through to downstream handler")
	assert.NotEqual(t, [16]byte{}, [16]byte(id.UserID), "UserID must be populated")
	assert.Equal(t, "alice@corp.example", id.Username, "edge user username is the email")
	assert.Equal(t, identity.RoleUser, id.Role, "default role for non-bootstrap email")
	assert.Contains(t, id.Departments, "default", "edge user inherits the default group")
}

// --- success: idempotent provisioning ---

// TestEdgeSlice_SameEmail_SameUser proves that two requests carrying the same
// email are resolved to the same persisted user (ResolveByEmail is idempotent).
func TestEdgeSlice_SameEmail_SameUser(t *testing.T) {
	env := newSliceEnv(t, nil)

	raw := env.jwks.sign(t, nil, env.jwks.validClaims("aud"))
	rec1, id1 := env.serve(requestWithJWT(raw))
	require.Equal(t, http.StatusOK, rec1.Code)

	raw2 := env.jwks.sign(t, nil, env.jwks.validClaims("aud"))
	rec2, id2 := env.serve(requestWithJWT(raw2))
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, id1.UserID, id2.UserID,
		"second request with the same email must resolve to the same persisted user")
}

// --- failure: 401 on all bad-token shapes ---

// TestEdgeSlice_InvalidJWT_401 asserts that every token-level failure produces
// a 401, never a 500 — auth fails closed.
func TestEdgeSlice_InvalidJWT_401(t *testing.T) {
	env := newSliceEnv(t, nil)

	t.Run("missing header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec, _ := env.serve(req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("empty header value", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set(adapthttp.CfAccessJWTHeader, "")
		rec, _ := env.serve(req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("malformed JWT", func(t *testing.T) {
		rec, _ := env.serve(requestWithJWT("not.a.jwt"))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("expired token", func(t *testing.T) {
		c := env.jwks.validClaims("aud")
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		rec, _ := env.serve(requestWithJWT(env.jwks.sign(t, nil, c)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("wrong audience", func(t *testing.T) {
		c := env.jwks.validClaims("wrong-aud")
		rec, _ := env.serve(requestWithJWT(env.jwks.sign(t, nil, c)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("bad signature (different key)", func(t *testing.T) {
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		rec, _ := env.serve(requestWithJWT(env.jwks.sign(t, otherKey, env.jwks.validClaims("aud"))))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("wrong issuer", func(t *testing.T) {
		c := env.jwks.validClaims("aud")
		c["iss"] = "https://evil.example"
		rec, _ := env.serve(requestWithJWT(env.jwks.sign(t, nil, c)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// --- error: DB failure yields 500, not 401 ---

// TestEdgeSlice_DBFailure_500 asserts that a provisioning/DB error maps to
// 500 (we could not check) rather than 401 (no credential). The constraint:
// "only a downstream infra/DB failure during provisioning yields 500."
func TestEdgeSlice_DBFailure_500(t *testing.T) {
	jwks := newSliceJWKS(t)

	// Build a separate DB that we close before the request so every SQL call fails.
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	require.NoError(t, sqlite.RunMigrations(db))

	groups := identity.GroupConfig{DefaultGroup: "default"}
	userRepo := sqlite.NewUserRepo(db)
	prov := instance.NewProvisioner(userRepo, nil, groups)

	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: jwks.issuer,
		Audiences:  []string{"aud"},
	})
	require.NoError(t, err)
	mw := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov, userRepo, groups)

	// Close the DB — the JWT verifier is in-memory and succeeds; the provisioner
	// hits the closed DB and returns a non-ErrInvalidClaims error.
	require.NoError(t, db.Close())

	raw := jwks.sign(t, nil, jwks.validClaims("aud"))
	req := requestWithJWT(raw)
	rec := httptest.NewRecorder()
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw.Handler(downstream).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"a provisioning DB error must yield 500, not 401 — the two are semantically distinct")
}

// --- edge: bootstrap admin receives RoleAdmin through the full stack ---

// TestEdgeSlice_BootstrapAdmin_GetsAdminRole asserts that an email listed in
// bootstrapAdmins is elevated to RoleAdmin end-to-end — verifier → provisioner
// → enrich → identity in context.
func TestEdgeSlice_BootstrapAdmin_GetsAdminRole(t *testing.T) {
	const adminEmail = "boss@corp.example"
	env := newSliceEnv(t, []string{adminEmail})

	c := env.jwks.validClaims("aud")
	c["email"] = adminEmail
	raw := env.jwks.sign(t, nil, c)

	rec, id := env.serve(requestWithJWT(raw))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, identity.RoleAdmin, id.Role,
		"bootstrap admin email must propagate to RoleAdmin through the full edge stack")
	assert.Equal(t, adminEmail, id.Username)
}

// --- Pass 2: robustness and spec alignment ---

// TestEdgeSlice_NeverRedirects asserts that edge mode NEVER issues a redirect
// to /auth/login, even for browser navigations (Accept: text/html GET without
// a JWT). A redirect would loop: /auth/login is not mounted in edge mode.
func TestEdgeSlice_NeverRedirects(t *testing.T) {
	env := newSliceEnv(t, nil)

	// Browser-style navigation: GET, Accept: text/html, no JWT.
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
	rec, _ := env.serve(req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"edge mode must return 401 for browser navigations, never redirect to /auth/login")
	assert.Empty(t, rec.Header().Get("Location"),
		"edge mode must set no Location header — a redirect would loop")
}

// TestEdgeSlice_EmailCaseInsensitive asserts that a JWT carrying a mixed-case
// email resolves to the same persisted user as the lower-cased form. The
// verifier trims; the provisioner lower-cases at the DB query level — so the
// same logical address always maps to one row.
func TestEdgeSlice_EmailCaseInsensitive(t *testing.T) {
	env := newSliceEnv(t, nil)

	// First request with lower-cased email — creates the user.
	c1 := env.jwks.validClaims("aud")
	c1["email"] = "carol@corp.example"
	rec1, id1 := env.serve(requestWithJWT(env.jwks.sign(t, nil, c1)))
	require.Equal(t, http.StatusOK, rec1.Code)

	// Second request with mixed-case email — must return the SAME user, not a
	// duplicate, proving the case-insensitive lookup path through the full stack.
	c2 := env.jwks.validClaims("aud")
	c2["email"] = "Carol@Corp.Example"
	rec2, id2 := env.serve(requestWithJWT(env.jwks.sign(t, nil, c2)))
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, id1.UserID, id2.UserID,
		"mixed-case email must resolve to the same persisted user as the lower-cased form")
}

// TestEdgeSlice_BootstrapAdmin_DemotedWhenRemovedFromList verifies that the
// bootstrap list is config-authoritative: a user previously created as admin
// is demoted on the next login when their email is absent from the new list.
func TestEdgeSlice_BootstrapAdmin_DemotedWhenRemovedFromList(t *testing.T) {
	const adminEmail = "boss@corp.example"
	jwks := newSliceJWKS(t)

	// Shared real DB so both provisioners see the same user row.
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))

	groups := identity.GroupConfig{DefaultGroup: "default"}
	userRepo := sqlite.NewUserRepo(db)

	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: jwks.issuer, Audiences: []string{"aud"},
	})
	require.NoError(t, err)

	// First middleware: adminEmail is in the bootstrap list → RoleAdmin on create.
	prov1 := instance.NewProvisioner(userRepo, []string{adminEmail}, groups)
	mw1 := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov1, userRepo, groups)

	c := jwks.validClaims("aud")
	c["email"] = adminEmail
	raw := jwks.sign(t, nil, c)

	var firstID identity.Identity
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstID, _ = identity.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rec1 := httptest.NewRecorder()
	mw1.Handler(downstream).ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	// First request goes without a JWT; use the JWT-signed request.
	rec1 = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(adapthttp.CfAccessJWTHeader, raw)
	mw1.Handler(downstream).ServeHTTP(rec1, req)
	require.Equal(t, http.StatusOK, rec1.Code)
	assert.Equal(t, identity.RoleAdmin, firstID.Role, "first login with bootstrap email must yield admin")

	// Second middleware: bootstrap list is empty → same user must be demoted.
	prov2 := instance.NewProvisioner(userRepo, nil, groups)
	mw2 := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov2, userRepo, groups)

	var secondID identity.Identity
	downstream2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondID, _ = identity.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	raw2 := jwks.sign(t, nil, c)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set(adapthttp.CfAccessJWTHeader, raw2)
	rec2 := httptest.NewRecorder()
	mw2.Handler(downstream2).ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)

	assert.Equal(t, identity.RoleUser, secondID.Role,
		"bootstrap admin must be demoted when email is removed from the bootstrap list")
	assert.Equal(t, firstID.UserID, secondID.UserID,
		"both logins must resolve to the same persisted user")
}

// TestEdgeSlice_DownstreamNeverCalledOnRejection asserts that the downstream
// handler is never invoked when any token-level failure triggers a 401. This is
// the auth-boundary correctness check: no tool runs if auth fails.
func TestEdgeSlice_DownstreamNeverCalledOnRejection(t *testing.T) {
	env := newSliceEnv(t, nil)

	badRequests := []struct {
		name string
		req  *http.Request
	}{
		{"missing header", httptest.NewRequest(http.MethodGet, "/", nil)},
		{"malformed JWT", requestWithJWT("bad")},
		{
			"expired",
			func() *http.Request {
				c := env.jwks.validClaims("aud")
				c["exp"] = time.Now().Add(-time.Hour).Unix()
				return requestWithJWT(env.jwks.sign(t, nil, c))
			}(),
		},
	}

	for _, tc := range badRequests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			called := false
			downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			rec := httptest.NewRecorder()
			env.mw.Handler(downstream).ServeHTTP(rec, tc.req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, called, "downstream must not be called when JWT verification fails")
		})
	}
}

// TestEdgeSlice_NBFToken_401 asserts that a not-yet-valid token (nbf in the
// future) is rejected with 401. This was missing from the Pass 1 failure table.
func TestEdgeSlice_NBFToken_401(t *testing.T) {
	env := newSliceEnv(t, nil)
	c := env.jwks.validClaims("aud")
	c["nbf"] = time.Now().Add(time.Hour).Unix()
	c["exp"] = time.Now().Add(2 * time.Hour).Unix()
	rec, _ := env.serve(requestWithJWT(env.jwks.sign(t, nil, c)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "not-yet-valid token must be rejected 401")
}

// TestEdgeSlice_GroupClaim_MappedToDepartment exercises the full SSO group
// → department mapping end-to-end with a real RSA-signed JWT:
//
//	real JWT (RSA-signed, "groups" claim) → real adaptedge.Verifier (GroupClaim wired)
//	→ real instance.Provisioner (GroupConfig.Mapping) → ResolveDepartments
//	→ persisted department → real enrich → identity.Departments in context.
//
// This is the "edge mode with a real group claim → department scoping" path
// (live/integration paths sub-item (b)). The locally-generated RSA key exercises
// the same cryptographic verification code path as a real Cloudflare Access token.
func TestEdgeSlice_GroupClaim_MappedToDepartment(t *testing.T) {
	jwks := newSliceJWKS(t)

	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))

	groupCfg := identity.GroupConfig{
		DefaultGroup: "default",
		Mapping:      map[string]string{"guid-123": "engineering"},
	}
	userRepo := sqlite.NewUserRepo(db)
	prov := instance.NewProvisioner(userRepo, nil, groupCfg)

	// Verifier is configured with the group claim name so extractGroups fires.
	v, verr := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: jwks.issuer,
		Audiences:  []string{"aud"},
		GroupClaim: "groups",
	})
	require.NoError(t, verr)
	mw := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov, userRepo, groupCfg)
	env := &sliceEnv{jwks: jwks, db: db, mw: mw}

	// JWT carries the raw GUID as an array-valued group claim.
	c := jwks.validClaims("aud")
	c["groups"] = []string{"guid-123"}
	raw := jwks.sign(t, nil, c)

	rec, id := env.serve(requestWithJWT(raw))

	require.Equal(t, http.StatusOK, rec.Code, "valid JWT with group claim must authenticate")
	assert.Equal(t, []string{"engineering"}, id.Departments,
		"GUID group claim must be mapped to friendly department end-to-end through the full stack")
	assert.Equal(t, identity.RoleUser, id.Role,
		"a group claim must never confer admin — admin is bootstrap email-list only")
}

// TestEdgeSlice_MissingGroupClaim_FallsBackToDefault verifies that a JWT with
// no group claim at all (GroupClaim configured but claim absent) degrades to the
// default group — it is NOT a 401.
func TestEdgeSlice_MissingGroupClaim_FallsBackToDefault(t *testing.T) {
	jwks := newSliceJWKS(t)

	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))

	groupCfg := identity.GroupConfig{
		DefaultGroup: "default",
		Mapping:      map[string]string{"guid-123": "engineering"},
	}
	userRepo := sqlite.NewUserRepo(db)
	prov := instance.NewProvisioner(userRepo, nil, groupCfg)
	v, verr := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: jwks.issuer,
		Audiences:  []string{"aud"},
		GroupClaim: "groups",
	})
	require.NoError(t, verr)
	mw := adapthttp.NewEdgeAuthMiddleware(adapthttp.CfAccessJWTHeader, v, prov, userRepo, groupCfg)
	env := &sliceEnv{jwks: jwks, db: db, mw: mw}

	// JWT has no "groups" claim at all.
	c := jwks.validClaims("aud")
	raw := jwks.sign(t, nil, c)

	rec, id := env.serve(requestWithJWT(raw))

	require.Equal(t, http.StatusOK, rec.Code,
		"absent group claim must NOT produce 401 — it degrades to default group")
	assert.Contains(t, id.Departments, "default",
		"user with no group claim must fall back to the default group")
}
