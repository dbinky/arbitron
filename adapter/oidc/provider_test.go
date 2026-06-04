package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptoidc "github.com/arbitron/arbitron/adapter/oidc"
)

const testClientID = "test-client"

// idpHarness is a minimal OIDC issuer: discovery doc, JWKS, and a token endpoint
// that returns a signed ID token configurable per test.
type idpHarness struct {
	server   *httptest.Server
	signKey  *rsa.PrivateKey
	keyID    string
	issuer   string
	tokenFn  func(t *testing.T) (string, int) // returns token-endpoint body + status
}

func newIDPHarness(t *testing.T) *idpHarness {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	h := &idpHarness{signKey: key, keyID: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 h.issuer,
			"authorization_endpoint": h.issuer + "/authorize",
			"token_endpoint":         h.issuer + "/token",
			"jwks_uri":               h.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     h.keyID,
			Algorithm: "RS256",
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, status := h.tokenFn(t)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	srv := httptest.NewServer(mux)
	h.server = srv
	h.issuer = srv.URL
	t.Cleanup(srv.Close)
	return h
}

// signIDToken signs an ID token with the harness key (or an override key for the
// bad-signature case).
func (h *idpHarness) signIDToken(t *testing.T, signWith *rsa.PrivateKey, claims map[string]any) string {
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

func (h *idpHarness) tokenResponse(idToken string) string {
	return fmt.Sprintf(`{"access_token":"at","token_type":"Bearer","id_token":%q}`, idToken)
}

func baseClaims(issuer string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":                issuer,
		"aud":                testClientID,
		"sub":                "subject-123",
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"nonce":              "nc",
		"preferred_username": "alice",
		"email":              "alice@example.com",
	}
}

func newProvider(t *testing.T, h *idpHarness) *adaptoidc.Provider {
	t.Helper()
	p, err := adaptoidc.New(context.Background(), adaptoidc.Config{
		IssuerURL:    h.issuer,
		ClientID:     testClientID,
		ClientSecret: "secret",
		RedirectURL:  "https://app.example.com/auth/callback",
		Scopes:       []string{"profile", "email"},
	})
	require.NoError(t, err)
	return p
}

func TestProvider_Exchange_Success(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		return h.tokenResponse(h.signIDToken(t, nil, baseClaims(h.issuer))), http.StatusOK
	}
	p := newProvider(t, h)

	claims, err := p.Exchange(context.Background(), "any-code")
	require.NoError(t, err)
	assert.Equal(t, "subject-123", claims.Subject)
	assert.Equal(t, "alice", claims.Username)
	assert.Equal(t, "alice@example.com", claims.Email)
	assert.Equal(t, "nc", claims.Nonce, "nonce from the ID token must be surfaced for the callback handler to verify")
}

// SECURITY: email_verified must be decoded from the ID token and surfaced on
// Claims so the provisioner only trusts a verified email for bootstrap-admin
// matching. A true claim => EmailVerified true; absent/false => false.
func TestProvider_Exchange_DecodesEmailVerified(t *testing.T) {
	t.Run("email_verified true is surfaced", func(t *testing.T) {
		h := newIDPHarness(t)
		h.tokenFn = func(t *testing.T) (string, int) {
			c := baseClaims(h.issuer)
			c["email_verified"] = true
			return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
		}
		p := newProvider(t, h)
		claims, err := p.Exchange(context.Background(), "code")
		require.NoError(t, err)
		assert.True(t, claims.EmailVerified, "email_verified=true must surface as Claims.EmailVerified=true")
	})

	t.Run("email_verified false is surfaced", func(t *testing.T) {
		h := newIDPHarness(t)
		h.tokenFn = func(t *testing.T) (string, int) {
			c := baseClaims(h.issuer)
			c["email_verified"] = false
			return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
		}
		p := newProvider(t, h)
		claims, err := p.Exchange(context.Background(), "code")
		require.NoError(t, err)
		assert.False(t, claims.EmailVerified, "email_verified=false must surface as Claims.EmailVerified=false")
	})

	t.Run("absent email_verified defaults to false (untrusted)", func(t *testing.T) {
		h := newIDPHarness(t)
		h.tokenFn = func(t *testing.T) (string, int) {
			// baseClaims has no email_verified key.
			return h.tokenResponse(h.signIDToken(t, nil, baseClaims(h.issuer))), http.StatusOK
		}
		p := newProvider(t, h)
		claims, err := p.Exchange(context.Background(), "code")
		require.NoError(t, err)
		assert.False(t, claims.EmailVerified, "absent email_verified must default to false (do not trust)")
	})
}

func TestProvider_AuthCodeURL_BindsStateAndNonce(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) { return "{}", http.StatusOK }
	p := newProvider(t, h)

	raw := p.AuthCodeURL("st", "nc")
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "st", q.Get("state"))
	assert.Equal(t, "nc", q.Get("nonce"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, "https://app.example.com/auth/callback", q.Get("redirect_uri"))
	assert.Contains(t, q.Get("scope"), "openid", "openid is forced even if cfg omits it")
}

func TestProvider_New_DiscoveryUnreachable(t *testing.T) {
	_, err := adaptoidc.New(context.Background(), adaptoidc.Config{
		IssuerURL: "http://127.0.0.1:1/does-not-exist",
		ClientID:  testClientID,
	})
	require.Error(t, err)
}

func TestProvider_Exchange_TokenEndpointError(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		return `{"error":"invalid_grant"}`, http.StatusBadRequest
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "bad")
	require.Error(t, err)
}

func TestProvider_Exchange_NoIDToken(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		return `{"access_token":"at","token_type":"Bearer"}`, http.StatusOK
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no id_token")
}

func TestProvider_Exchange_EmptySubject(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		c := baseClaims(h.issuer)
		c["sub"] = ""
		return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subject")
}

func TestProvider_Exchange_BadSignature(t *testing.T) {
	h := newIDPHarness(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	h.tokenFn = func(t *testing.T) (string, int) {
		return h.tokenResponse(h.signIDToken(t, otherKey, baseClaims(h.issuer))), http.StatusOK
	}
	p := newProvider(t, h)
	_, err = p.Exchange(context.Background(), "code")
	require.Error(t, err, "a token signed with a key not in the JWKS must be rejected")
}

func TestProvider_Exchange_WrongAudience(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		c := baseClaims(h.issuer)
		c["aud"] = "some-other-client"
		return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "code")
	require.Error(t, err, "a token whose aud is a different client must be rejected")
}

func TestProvider_Exchange_WrongIssuer(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		c := baseClaims(h.issuer)
		c["iss"] = "https://evil-issuer.example.com" // wrong issuer, correct signature
		return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "code")
	require.Error(t, err, "a token with a mismatched iss must be rejected even if signature is valid")
}

func TestProvider_Exchange_ExpiredToken(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		c := baseClaims(h.issuer)
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
	}
	p := newProvider(t, h)
	_, err := p.Exchange(context.Background(), "code")
	require.Error(t, err, "an expired ID token must be rejected")
}

// TestProvider_Exchange_UsernameFallbackChain verifies the fallback order:
// preferred_username → name → email → subject. Real-world IdPs vary in which
// fields they populate; the chain must produce a stable non-empty username.
func TestProvider_Exchange_UsernameFallbackChain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setExtra func(c map[string]any)
		wantUser string
	}{
		{"preferred_username present", func(c map[string]any) {}, "alice"},
		{"name fallback", func(c map[string]any) { delete(c, "preferred_username") }, "alice"},
		{"email fallback", func(c map[string]any) {
			delete(c, "preferred_username")
			delete(c, "name")
		}, "alice@example.com"},
		{"subject fallback", func(c map[string]any) {
			delete(c, "preferred_username")
			delete(c, "name")
			delete(c, "email")
		}, "subject-123"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := newIDPHarness(t)
			h.tokenFn = func(t *testing.T) (string, int) {
				c := baseClaims(h.issuer)
				// baseClaims only sets preferred_username and email; add name for the
				// chain to have something to fall back to before email.
				c["name"] = "alice"
				tc.setExtra(c)
				return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
			}
			p := newProvider(t, h)
			claims, err := p.Exchange(context.Background(), "code")
			require.NoError(t, err)
			assert.Equal(t, tc.wantUser, claims.Username)
		})
	}
}

// TestProvider_Exchange_EmptyNonce_Passthrough verifies that when the ID token
// contains no nonce claim (some IdPs don't support them), Exchange faithfully
// returns Claims{Nonce:""} rather than injecting a default. The callback handler
// must be the one to reject the missing nonce — Exchange must not mask it.
func TestProvider_Exchange_EmptyNonce_Passthrough(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		c := baseClaims(h.issuer)
		delete(c, "nonce") // IdP that doesn't embed a nonce claim
		return h.tokenResponse(h.signIDToken(t, nil, c)), http.StatusOK
	}
	p := newProvider(t, h)
	claims, err := p.Exchange(context.Background(), "code")
	require.NoError(t, err, "a token without a nonce claim is structurally valid OIDC")
	assert.Equal(t, "", claims.Nonce, "Exchange must not inject a nonce — the empty value must propagate to the caller for rejection")
}

// Structural: Claims carries no IdP access/refresh token field, reinforcing that
// the IdP is never on the request hot path.
func TestProvider_Claims_DiscardsIdPTokens(t *testing.T) {
	h := newIDPHarness(t)
	h.tokenFn = func(t *testing.T) (string, int) {
		return h.tokenResponse(h.signIDToken(t, nil, baseClaims(h.issuer))), http.StatusOK
	}
	p := newProvider(t, h)
	claims, err := p.Exchange(context.Background(), "code")
	require.NoError(t, err)

	b, err := json.Marshal(claims)
	require.NoError(t, err)
	lower := strings.ToLower(string(b))
	assert.NotContains(t, lower, "access_token")
	assert.NotContains(t, lower, "refresh_token")
}
