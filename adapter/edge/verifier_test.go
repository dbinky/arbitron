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

// newVerifierWithGroupClaim builds a verifier that also extracts the named group
// claim. Mirrors newVerifier but threads GroupClaim through Config.
func newVerifierWithGroupClaim(t *testing.T, h *jwksHarness, groupClaim string, audiences ...string) *adaptedge.Verifier {
	t.Helper()
	v, err := adaptedge.New(context.Background(), adaptedge.Config{
		TeamDomain: h.issuer,
		Audiences:  audiences,
		GroupClaim: groupClaim,
	})
	require.NoError(t, err)
	return v
}

func TestVerify_GroupClaimArray_ReturnsGroups(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng", "platform"}
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Equal(t, []string{"eng", "platform"}, id.Groups)
}

func TestVerify_GroupClaimSingleString_ReturnsOneGroup(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = "eng" // a single JSON string, not an array
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Equal(t, []string{"eng"}, id.Groups)
}

func TestVerify_GroupClaimAbsent_ReturnsNilGroupsNot401(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud") // no "groups" key
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err, "a missing group claim must NOT fail verification")
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups)
}

func TestVerify_NoGroupClaimConfigured_IgnoresClaim(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "", "app-aud") // GroupClaim == "" => no extraction
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng"} // present, but not configured to be read
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups, "no configured claim => groups not extracted")
}

func TestVerify_GroupClaimMalformedType_DefaultsToNilGroups(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = map[string]any{"unexpected": "object"} // neither string nor array-of-strings
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err, "a malformed group claim must NOT lock out a valid user")
	assert.Equal(t, "alice@corp.example", id.Email)
	assert.Nil(t, id.Groups)
}

func TestVerify_GroupClaimMixedArray_KeepsOnlyStrings(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["groups"] = []any{"eng", 42, "platform", ""} // non-strings and blanks dropped
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, []string{"eng", "platform"}, id.Groups)
}

// TestVerify_GroupClaimEdgeCases covers the claim shapes identified in the
// carry-forward review: empty array, blank-only array, numeric scalar, and
// JSON-null. All are valid tokens — none must fail verification; all must yield
// nil groups so the user falls back to the default group rather than being
// locked out.
func TestVerify_GroupClaimEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		claim any
	}{
		{"empty array", []any{}},
		{"blank-only array", []any{"  ", ""}},
		{"numeric scalar", float64(42)},
		{"null claim", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newJWKSHarness(t)
			v := newVerifierWithGroupClaim(t, h, "groups", "app-aud")
			c := baseClaims(h.issuer, "app-aud")
			c["groups"] = tc.claim
			raw := h.signToken(t, nil, c)

			id, err := v.Verify(context.Background(), raw)
			require.NoError(t, err, "claim shape %q must NOT fail verification", tc.name)
			assert.Equal(t, "alice@corp.example", id.Email)
			assert.Nil(t, id.Groups, "claim shape %q must yield nil groups, never a 401", tc.name)
		})
	}
}

func TestVerify_ValidToken_ReturnsEmail(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")

	c := baseClaims(h.issuer, "app-aud")
	c["email"] = "alice@corp.example"
	raw := h.signToken(t, nil, c)

	id, err := v.Verify(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email)
}

func TestVerify_Rejections(t *testing.T) {
	t.Run("expired token", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("not-yet-valid (nbf)", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["nbf"] = time.Now().Add(time.Hour).Unix()
		c["exp"] = time.Now().Add(2 * time.Hour).Unix()
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("wrong audience", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "someone-else")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("wrong issuer", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["iss"] = "https://evil.example"
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("bad signature", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		c := baseClaims(h.issuer, "app-aud")
		id, verr := v.Verify(context.Background(), h.signToken(t, otherKey, c))
		require.Error(t, verr)
		assert.Empty(t, id.Email)
	})

	t.Run("missing email claim", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		delete(c, "email")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("empty email claim", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		c := baseClaims(h.issuer, "app-aud")
		c["email"] = ""
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("malformed jwt", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		id, err := v.Verify(context.Background(), "not.a.jwt")
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("empty token", func(t *testing.T) {
		h := newJWKSHarness(t)
		v := newVerifier(t, h, "app-aud")
		id, err := v.Verify(context.Background(), "")
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})
}

func TestVerify_MultipleAudiences(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "aud-a", "aud-b")

	t.Run("token with a listed aud is accepted", func(t *testing.T) {
		c := baseClaims(h.issuer, "aud-b")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.NoError(t, err)
		assert.Equal(t, "alice@corp.example", id.Email)
	})

	t.Run("token with an unlisted aud is rejected", func(t *testing.T) {
		c := baseClaims(h.issuer, "aud-c")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})
}

func TestVerify_JWKSCachedAcrossCalls(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")

	before := atomic.LoadInt32(&h.jwksHits)
	for i := 0; i < 2; i++ {
		c := baseClaims(h.issuer, "app-aud")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.NoError(t, err)
		assert.Equal(t, "alice@corp.example", id.Email)
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

// TestNew_EmptyAudiences_ReturnsError verifies that New fails fast when the
// configured audience set is empty (after stripping whitespace). An empty set
// would produce a Verifier that always rejects — surfacing the misconfiguration
// at boot is safer than silently blocking every request.
func TestNew_EmptyAudiences_ReturnsError(t *testing.T) {
	h := newJWKSHarness(t)

	t.Run("nil audiences slice", func(t *testing.T) {
		_, err := adaptedge.New(context.Background(), adaptedge.Config{
			TeamDomain: h.issuer,
			Audiences:  nil,
		})
		require.Error(t, err)
	})

	t.Run("empty audiences slice", func(t *testing.T) {
		_, err := adaptedge.New(context.Background(), adaptedge.Config{
			TeamDomain: h.issuer,
			Audiences:  []string{},
		})
		require.Error(t, err)
	})

	t.Run("whitespace-only audiences", func(t *testing.T) {
		_, err := adaptedge.New(context.Background(), adaptedge.Config{
			TeamDomain: h.issuer,
			Audiences:  []string{"  ", "\t", ""},
		})
		require.Error(t, err)
	})
}

// TestVerify_MissingAudClaim verifies that a token whose aud claim is absent or
// empty is rejected even though go-oidc skips the aud check (SkipClientIDCheck).
// This is the critical invariant from carry-forward issue #2.
func TestVerify_MissingAudClaim(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")

	t.Run("no aud field", func(t *testing.T) {
		c := baseClaims(h.issuer, "app-aud")
		delete(c, "aud")
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})

	t.Run("empty string aud", func(t *testing.T) {
		c := baseClaims(h.issuer, "") // aud: ""
		id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
		require.Error(t, err)
		assert.Empty(t, id.Email)
	})
}

// TestVerify_WhitespaceEmail verifies that a token carrying a whitespace-only
// email claim is rejected (email normalization happens at the provisioner; the
// verifier's responsibility is to reject clearly invalid values).
func TestVerify_WhitespaceEmail(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["email"] = "   "
	id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
	require.Error(t, err)
	assert.Empty(t, id.Email)
}

// TestVerify_EmailTrimmed verifies that the verifier returns the email claim
// with leading/trailing whitespace stripped. The provisioner lowercases but does
// not trim, so a space-padded email returned by the verifier would fail to match
// an existing DB record and create a duplicate user instead.
func TestVerify_EmailTrimmed(t *testing.T) {
	h := newJWKSHarness(t)
	v := newVerifier(t, h, "app-aud")
	c := baseClaims(h.issuer, "app-aud")
	c["email"] = "  alice@corp.example  "
	id, err := v.Verify(context.Background(), h.signToken(t, nil, c))
	require.NoError(t, err)
	assert.Equal(t, "alice@corp.example", id.Email, "email must be returned trimmed")
}
