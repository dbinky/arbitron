package probe

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy-path: Anthropic lists models via GET /v1/models with x-api-key and the
// pinned anthropic-version header.
func TestAnthropicProbe_ListsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "sk-ant-TEST", r.Header.Get("x-api-key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4"}]}`))
	}))
	defer srv.Close()

	p := newAnthropicProbe(Deps{HTTPClient: srv.Client()})
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "anthropic-api",
		BaseURL:   srv.URL,
		RevealKey: revealKey("sk-ant-TEST"),
	})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.Equal(t, []string{"claude-sonnet-4"}, res.Models)
}

// Regression: the pinned version stays in lockstep with adapter/anthropicapi.
func TestAnthropicProbe_VersionPinned(t *testing.T) {
	assert.Equal(t, "2023-06-01", anthropicVersion)
}

// Error-handling: a 403 classifies as ErrCredentialsRejected.
func TestAnthropicProbe_403_CredentialsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := newAnthropicProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "anthropic-api",
		BaseURL:   srv.URL,
		RevealKey: revealKey("sk-ant-TEST"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialsRejected)
}

// Security: the api key never appears in the returned error nor in slog output.
func TestAnthropicProbe_SecretNeverInError(t *testing.T) {
	const secret = "sk-ant-SECRET-SENTINEL"
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()

	p := newAnthropicProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "anthropic-api",
		BaseURL:   srv.URL,
		RevealKey: revealKey(secret),
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, logBuf.String(), secret)
}
