package probe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arbitron/arbitron/adapter/anthropicapi"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHostTransport records the Host of the first outgoing request then
// returns an error, allowing tests to assert what URL the probe targeted without
// making a real network call.
type captureHostTransport struct{ captured string }

func (c *captureHostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.captured = r.URL.Host
	return nil, errors.New("captured: no real connection")
}

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

// seam (anthropicapi ↔ probe): both the chat adapter and the probe must send
// the same anthropic-version header value. This cross-package assertion catches
// version drift — if one constant is bumped without the other, this test fails.
// The probe uses a local copy (adapter/probe.anthropicVersion) to avoid an
// import cycle in production code; this test is the only enforcement of lockstep.
func TestAnthropicProbe_VersionMatchesChatAdapter(t *testing.T) {
	assert.Equal(t, anthropicapi.AnthropicVersionHeader, anthropicVersion,
		"adapter/probe.anthropicVersion must equal adapter/anthropicapi.AnthropicVersionHeader — change both in lockstep")
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

// Error-handling: a transport failure classifies as ErrUnreachable.
func TestAnthropicProbe_Transport_Unreachable(t *testing.T) {
	p := newAnthropicProbe(Deps{HTTPClient: &http.Client{Transport: dialErrTransport{}}})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "anthropic-api",
		BaseURL:  "http://127.0.0.1:1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}

// Error-handling: a 5xx response classifies as ErrUpstreamStatus (not credentials).
func TestAnthropicProbe_500_UpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	p := newAnthropicProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "anthropic-api",
		BaseURL:  srv.URL,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstreamStatus)
	assert.NotErrorIs(t, err, ErrCredentialsRejected)
}

// Error-handling: RevealKey returning an error aborts before any HTTP call.
func TestAnthropicProbe_RevealKeyError(t *testing.T) {
	p := newAnthropicProbe(Deps{HTTPClient: &http.Client{Transport: failingTransport{t}}})
	wantErr := errors.New("decrypt failed")
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "anthropic-api",
		BaseURL:   "https://api.anthropic.com",
		RevealKey: func(context.Context) (string, error) { return "", wantErr },
	})
	require.ErrorIs(t, err, wantErr)
}

// Edge-case: an empty base URL falls back to the default Anthropic endpoint
// (https://api.anthropic.com). Verified by capturing the request host via a
// transport that records and immediately errors.
func TestAnthropicProbe_EmptyBaseURL_UsesDefault(t *testing.T) {
	ct := &captureHostTransport{}
	p := &anthropicProbe{
		httpClient: &http.Client{Transport: ct},
		timeout:    func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} },
	}
	_, _ = p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "anthropic-api",
		BaseURL:  "",
	})
	assert.Contains(t, ct.captured, "api.anthropic.com",
		"empty BaseURL must resolve to the default Anthropic endpoint")
}

// Security: a response larger than MaxResponseBytes is bounded (OOM backstop).
func TestAnthropicProbe_BoundedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("{"), 1<<20)) // 1 MiB
	}))
	defer srv.Close()

	p := newAnthropicProbe(Deps{HTTPClient: srv.Client(), MaxResponseBytes: 64})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "anthropic-api",
		BaseURL:  srv.URL,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errResponseTooLarge)
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
