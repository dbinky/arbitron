package edge_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptedge "github.com/arbitron/arbitron/adapter/edge"
)

// jwksHarness is a minimal Cloudflare-Access-style issuer: it serves an OIDC
// discovery document and a JWKS at the Access certs path, signing tokens with a
// locally-generated RSA key so signatures are real and deterministic. Mirrors
// the idpHarness from adapter/oidc/provider_test.go.
type jwksHarness struct {
	server   *httptest.Server
	signKey  *rsa.PrivateKey
	keyID    string
	issuer   string
	jwksHits int32 // count of JWKS-endpoint fetches (caching assertion)
}

func newJWKSHarness(t *testing.T) *jwksHarness {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	h := &jwksHarness{signKey: key, keyID: "edge-key-1"}

	serveJWKS := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&h.jwksHits, 1)
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     h.keyID,
			Algorithm: "RS256",
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                h.issuer,
			"jwks_uri":                              h.issuer + "/cdn-cgi/access/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	// Both the standard JWKS path and the Cloudflare Access certs path serve the
	// keys, so whichever construction strategy the adapter uses is exercised.
	mux.HandleFunc("/cdn-cgi/access/certs", serveJWKS)
	mux.HandleFunc("/jwks", serveJWKS)

	srv := httptest.NewServer(mux)
	h.server = srv
	h.issuer = srv.URL
	t.Cleanup(srv.Close)
	return h
}

// signToken signs the given claims with the harness key (or an override key for
// the bad-signature case).
func (h *jwksHarness) signToken(t *testing.T, signWith *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	if signWith == nil {
		signWith = h.signKey
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signWith},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", h.keyID),
	)
	require.NoError(t, err)
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

// baseClaims returns a valid in-date Cloudflare-style claim set.
func baseClaims(issuer, aud string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   issuer,
		"aud":   aud,
		"sub":   "subject-123",
		"email": "alice@corp.example",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
}

func newVerifier(t *testing.T, h *jwksHarness, audiences ...string) *adaptedge.Verifier {
	t.Helper()
	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: h.issuer,
		Audiences:  audiences,
	})
	require.NoError(t, err)
	return v
}

func TestVerify_ValidToken_ReturnsEmail(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")

	c := baseClaims(h.issuer, "app-aud")
	c["email"] = "alice@corp.example"
	raw := h.signToken(t, nil, c)

	email, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", email)
}

func TestVerify_Rejections(t *testing.T) {
	t.Run("expired token", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("not-yet-valid (nbf)", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["nbf"] = time.Now().Add(time.Hour).Unix()
		c["exp"] = time.Now().Add(2 * time.Hour).Unix()
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("wrong audience", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "someone-else")
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("wrong issuer", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["iss"] = "https://evil.example"
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("bad signature", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		c := baseClaims(h.issuer, "app-aud")
		email, verr := v.Verify(context.Background(), h.signToken(t, otherKey, c))
		require.Error(t, verr)
		assert.Empty(t, email)
	})

	t.Run("missing email claim", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		delete(c, "email")
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("empty email claim", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["email"] = ""
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("malformed jwt", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		email, err := v.Verify(context.Background(), "not.a.jwt")
		require.Error(t, err)
		assert.Empty(t, email)
	})

	t.Run("empty token", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		email, err := v.Verify(context.Background(), "")
		require.Error(t, err)
		assert.Empty(t, email)
	})
}

func TestVerify_MultipleAudiences(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "aud-a", "aud-b")

	t.Run("token with a listed aud is accepted", func(t *testing.T) {
		c := baseClaims(h.issuer, "aud-b")
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.NoError(t, err)
		assert.Equal(t, "alice@corp.example", email)
	})

	t.Run("token with an unlisted aud is rejected", func(t *testing.T) {
		c := baseClaims(h.issuer, "aud-c")
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, email)
	})
}

func TestVerify_JWKSCachedAcrossCalls(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")

	before := atomic.LoadInt32(&h.jwksHits)
	for i := 0; i < 2; i++ {
		c := baseClaims(h.issuer, "app-aud")
		email, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.NoError(t, err)
		assert.Equal(t, "alice@corp.example", email)
	}
	after := atomic.LoadInt32(&h.jwksHits)
	// go-oidc caches the remote key set: two sequential Verify calls must not
	// trigger two extra JWKS fetches.
	assert.LessOrEqual(t, after-before, int32(1), "JWKS must be cached across Verify calls")
}

func TestNew_UnreachableTeamDomain_FailsFast(t *testing.T) {
	_, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: "http://127.0.0.1:1/does-not-exist",
		Audiences:  []string{"app-aud"},
	})
	require.Error(t, err)
}
