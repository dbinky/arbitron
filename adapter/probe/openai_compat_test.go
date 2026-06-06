package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTransport fails the test if any HTTP call is made. Used to prove the
// scheme check short-circuits before the client is invoked.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("HTTP client must not be called")
	return nil, nil
}

// dialErrTransport always returns a transport error, modelling an unreachable
// endpoint without depending on real network teardown timing.
type dialErrTransport struct{}

func (dialErrTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
}

func revealKey(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return key, nil }
}

// Happy-path: every OpenAI-compatible provider lists models through GET
// {base}/models with Bearer auth.
func TestOpenAICompatProbe_ListsModels(t *testing.T) {
	for _, prov := range []string{"openai", "openrouter", "ollama", "azure-openai"} {
		t.Run(prov, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/models", r.URL.Path)
				assert.Equal(t, "Bearer sk-TEST", r.Header.Get("Authorization"))
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
			}))
			defer srv.Close()

			p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
			res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
				Provider:  prov,
				BaseURL:   srv.URL,
				RevealKey: revealKey("sk-TEST"),
			})
			require.NoError(t, err)
			assert.True(t, res.Reachable)
			assert.False(t, res.ModelsUnsupported)
			assert.Equal(t, []string{"gpt-4o", "gpt-4o-mini"}, res.Models)
		})
	}
}

// Edge-case: a trailing slash on base_url still yields a single-slash /models.
func TestOpenAICompatProbe_TrailingSlashBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "openai",
		BaseURL:  srv.URL + "/",
	})
	require.NoError(t, err)
}

// Edge-case: an empty model list is reachable-but-no-models, not an error.
func TestOpenAICompatProbe_EmptyModelList_ReachableNoModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	res, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "openai", BaseURL: srv.URL})
	require.NoError(t, err)
	assert.True(t, res.Reachable)
	assert.False(t, res.ModelsUnsupported)
	assert.Empty(t, res.Models)
}

// Input-validation: a non-http(s) scheme is rejected without any HTTP call.
func TestOpenAICompatProbe_BadScheme_Rejected(t *testing.T) {
	p := newOpenAICompatProbe(Deps{HTTPClient: &http.Client{Transport: failingTransport{t}}})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "openai",
		BaseURL:  "file:///etc/passwd",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}

// Error-handling: 401 and 403 classify as ErrCredentialsRejected (not transport).
func TestOpenAICompatProbe_401_403_CredentialsRejected(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			defer srv.Close()

			p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
			_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "openai", BaseURL: srv.URL})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrCredentialsRejected)
			assert.NotErrorIs(t, err, ErrUnreachable)
		})
	}
}

// Error-handling: a transport failure classifies as ErrUnreachable.
func TestOpenAICompatProbe_Transport_Unreachable(t *testing.T) {
	p := newOpenAICompatProbe(Deps{HTTPClient: &http.Client{Transport: dialErrTransport{}}})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider: "openai",
		BaseURL:  "http://127.0.0.1:1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
	assert.NotErrorIs(t, err, ErrCredentialsRejected)
}

// Error-handling: a malformed 200 body is a decode error, not credentials/transport.
func TestOpenAICompatProbe_MalformedBody_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "openai", BaseURL: srv.URL})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
	assert.NotErrorIs(t, err, ErrCredentialsRejected)
	assert.NotErrorIs(t, err, ErrUnreachable)
}

// Security: the revealed key never appears in the returned error nor in slog output.
func TestOpenAICompatProbe_SecretNeverInError(t *testing.T) {
	const secret = "sk-SECRET-SENTINEL"
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{
		Provider:  "openai",
		BaseURL:   srv.URL,
		RevealKey: revealKey(secret),
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, logBuf.String(), secret)
}

// Security: a response larger than MaxResponseBytes is bounded (OOM backstop).
func TestOpenAICompatProbe_BoundedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(bytes.Repeat([]byte("{"), 1<<20)) // 1 MiB
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client(), MaxResponseBytes: 64})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "openai", BaseURL: srv.URL})
	require.Error(t, err)
	assert.ErrorIs(t, err, errResponseTooLarge)
}

// Security: control characters in an upstream snippet are scrubbed, while the
// status still classifies as credentials-rejected.
func TestOpenAICompatProbe_ControlCharsScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("\n\rINJECTED\x1b[2J"))
	}))
	defer srv.Close()

	p := newOpenAICompatProbe(Deps{HTTPClient: srv.Client()})
	_, err := p.Probe(context.Background(), domprovider.ResolvedBinding{Provider: "openai", BaseURL: srv.URL})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialsRejected)
	assert.NotContains(t, err.Error(), "\n")
	assert.NotContains(t, err.Error(), "\r")
	assert.NotContains(t, err.Error(), "\x1b")
	assert.Contains(t, err.Error(), "INJECTED") // payload retained, just scrubbed
}
