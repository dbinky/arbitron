package probe

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedClock = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }

// bedrockDeps wires the test-only endpoint seam at the httptest server and an
// injected clock for a deterministic signature.
func bedrockDeps(srv *httptest.Server) Deps {
	return Deps{
		HTTPClient:      srv.Client(),
		Clock:           fixedClock,
		BedrockEndpoint: func(string) string { return srv.URL },
	}
}

// Happy-path: Bedrock lists foundation models via a SigV4-signed request to the
// control-plane endpoint.
func TestBedrockProbe_ListFoundationModels_Signed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256"), "auth=%q", auth)
		assert.NotEmpty(t, r.Header.Get("X-Amz-Date"))
		_, _ = w.Write([]byte(`{"modelSummaries":[{"modelId":"anthropic.claude-3-5-sonnet"}]}`))
	}))
	defer srv.Close()

	p := newBedrockProbe(bedrockDeps(srv))
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIAEXAMPLE"},
		RevealKey: revealKey("topsecret"),
	})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.Equal(t, []string{"anthropic.claude-3-5-sonnet"}, res.Models)
}

// Edge-case: a composite credential with a session token sends X-Amz-Security-Token.
func TestBedrockProbe_SessionToken_SecurityTokenHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TKN", r.Header.Get("X-Amz-Security-Token"))
		_, _ = w.Write([]byte(`{"modelSummaries":[]}`))
	}))
	defer srv.Close()

	p := newBedrockProbe(bedrockDeps(srv))
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIAEXAMPLE"},
		RevealKey: revealKey(`{"secret":"S","session_token":"TKN"}`),
	})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
}

// Input-validation: a missing region (or access-key-id) is a region/config fault.
func TestBedrockProbe_MissingRegion_RegionConfigError(t *testing.T) {
	p := newBedrockProbe(Deps{Clock: fixedClock})

	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"access-key-id": "AKIAEXAMPLE"},
		RevealKey: revealKey("topsecret"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegionConfig)

	_, err = p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1"},
		RevealKey: revealKey("topsecret"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegionConfig)
}

// Error-handling: a 403 from Bedrock is a SigV4/credentials rejection.
func TestBedrockProbe_403_CredentialsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := newBedrockProbe(bedrockDeps(srv))
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIAEXAMPLE"},
		RevealKey: revealKey("topsecret"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialsRejected)
}

// Security: the secret-access-key never appears in errors or slog (it lives only
// in the SigV4 signature).
func TestBedrockProbe_SecretNeverInError(t *testing.T) {
	const secret = "BEDROCK-SECRET-SENTINEL"
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()

	p := newBedrockProbe(bedrockDeps(srv))
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIAEXAMPLE"},
		RevealKey: revealKey(secret),
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, logBuf.String(), secret)
}
